package tracker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/cli/go-gh/v2/pkg/repository"
)

const (
	boardViewName = "Board"
	// GitHub rejects longer label names and descriptions, which hold the registration.
	maxLabelName        = 50
	maxLabelDescription = 100
)

// Ticket statuses, which are the columns of every project board.
const (
	StatusTodo  = "Todo"
	StatusDoing = "Doing"
	StatusDone  = "Done"
)

var boardColumns = []struct{ name, color string }{
	{StatusTodo, "GRAY"},
	{StatusDoing, "BLUE"},
	{StatusDone, "GREEN"},
}

// ProjectSummary describes a project and its ticket counts.
type ProjectSummary struct {
	Project Project
	Open    int
	Closed  int
}

// Projects lists registered projects from GitHub.
func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	meta, err := c.refreshMetadata(ctx)
	if err != nil {
		return nil, err
	}
	return meta.Projects, nil
}

// ProjectRepo returns the repository registered under a project name from the
// cached IDs, which it reads from GitHub only when there is no cache.
func (c *Client) ProjectRepo(ctx context.Context, name string) (string, bool, error) {
	meta, err := c.metadata(ctx)
	if err != nil {
		return "", false, err
	}
	project, ok := meta.project(name)
	return project.Repo, ok, nil
}

// CurrentProject maps the working directory's GitHub remote, and its directory in the
// checkout, to a project without calling GitHub unless refresh is set and the cached
// metadata has no match.
func (c *Client) CurrentProject(ctx context.Context, refresh bool) (Project, bool, error) {
	current, err := CurrentRepo()
	if err != nil {
		return Project{}, false, nil
	}
	repo := current.String()
	dir := currentDir(ctx)
	meta, err := c.metadata(ctx)
	if err != nil {
		return Project{}, false, err
	}
	if project, ok := meta.projectAt(repo, dir); ok || !refresh {
		return project, ok, nil
	}
	if meta, err = c.refreshMetadata(ctx); err != nil {
		return Project{}, false, err
	}
	project, ok := meta.projectAt(repo, dir)
	return project, ok, nil
}

// CurrentRepo returns the working directory's github.com repository, or why it has
// none: no git checkout, no remote on a known GitHub host, or a repository on
// another host.
func CurrentRepo() (Repo, error) {
	repo, err := repository.Current()
	if err != nil {
		return Repo{}, noRepoReason(err)
	}
	if repo.Host != defaultHost {
		return Repo{}, fmt.Errorf("%s/%s/%s is on %s, and projects are %s repositories", repo.Host, repo.Owner, repo.Name, repo.Host, defaultHost)
	}
	return Repo{Owner: repo.Owner, Name: repo.Name}, nil
}

// noRepoReason names why go-gh found no repository in one line. Its own errors span
// lines when they quote git, which breaks the one-line results agents read.
func noRepoReason(err error) error {
	if os.Getenv("GH_REPO") != "" {
		return err
	}
	if exec.Command("git", "rev-parse", "--git-dir").Run() != nil {
		return errors.New("no git checkout here")
	}
	remotes, remoteErr := exec.Command("git", "remote").Output()
	switch {
	case remoteErr != nil:
		first, _, _ := strings.Cut(err.Error(), "\n")
		return errors.New(first)
	case strings.TrimSpace(string(remotes)) == "":
		return errors.New("this checkout has no git remote")
	}
	return errors.New("no git remote points to a GitHub host the GitHub CLI is logged in to")
}

// currentDir returns the working directory relative to the top of its checkout,
// empty at the top. GH_REPO names a repository that need not be this checkout, so
// it selects the whole repository.
func currentDir(ctx context.Context) string {
	if os.Getenv("GH_REPO") != "" {
		return ""
	}
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-prefix").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSpace(string(out)), "/")
}

// validDir accepts a directory inside a repository, written relative to its top.
func validDir(dir string) error {
	if dir == "" || dir != path.Clean(dir) || dir == "." || path.IsAbs(dir) || dir == ".." || strings.HasPrefix(dir, "../") || strings.Contains(dir, ":") {
		return fmt.Errorf("invalid path %q: expected a directory relative to the top of the repository", dir)
	}
	return nil
}

// validComponent accepts the part of a component's name after its repository.
func validComponent(name string) error {
	if name == "" || strings.ContainsAny(name, "/:, \t\n") {
		return fmt.Errorf("invalid component name %q: expected one word without slashes, colons, commas, or spaces", name)
	}
	return nil
}

// AddProject registers a code repository. Writing the label comes last, so a
// failed run leaves nothing registered and can simply be repeated.
func (c *Client) AddProject(ctx context.Context, repo Repo) (Project, error) {
	meta, err := c.refreshMetadata(ctx)
	if err != nil {
		return Project{}, err
	}
	project, exists := meta.project(repo.Name)
	if exists && !strings.EqualFold(project.Repo, repo.String()) {
		return Project{}, fmt.Errorf("project %q is already registered for %s", repo.Name, project.Repo)
	}
	if exists && project.BoardID != "" {
		return project, nil
	}
	if err := c.verifyRepo(ctx, repo); err != nil {
		return Project{}, err
	}
	item, ok := meta.board(repo.Name)
	if !ok {
		if item, err = c.createBoard(ctx, meta, repo.Name); err != nil {
			return Project{}, err
		}
	}
	labelID, err := c.writeLabel(ctx, meta, labelPrefix+repo.Name, repo.String())
	if err != nil {
		return Project{}, err
	}
	if meta, err = c.refreshMetadata(ctx); err != nil {
		return Project{}, err
	}
	project, _ = meta.project(repo.Name)
	project.LabelID, project.Name, project.Repo = labelID, repo.Name, repo.String()
	project.attach(item)
	return project, nil
}

func (c *Client) verifyRepo(ctx context.Context, repo Repo) error {
	var response struct {
		Repository struct{ ID string }
	}
	variables := map[string]any{"owner": repo.Owner, "name": repo.Name}
	err := c.do(ctx, `query($owner: String!, $name: String!) { repository(owner: $owner, name: $name) { id } }`, variables, &response)
	if hasGraphQLType(err, graphQLNotFoundType) {
		return fmt.Errorf("repository %s not found", repo)
	}
	return err
}

func (c *Client) createBoard(ctx context.Context, meta *metadata, title string) (board, error) {
	var created struct {
		CreateProjectV2 struct {
			ProjectV2 struct {
				ID, URL string
				Field   struct{ ID string }
			}
		}
	}
	mutation := `mutation($owner: ID!, $repository: ID!, $title: String!) {
  createProjectV2(input: {ownerId: $owner, repositoryId: $repository, title: $title}) {
    projectV2 { id url field(name: "Status") { ... on ProjectV2SingleSelectField { id } } }
  }
}`
	variables := map[string]any{"owner": meta.OwnerID, "repository": meta.RepositoryID, "title": title}
	if err := c.do(ctx, mutation, variables, &created); err != nil {
		return board{}, fmt.Errorf("creating board: %w", err)
	}
	node := created.CreateProjectV2.ProjectV2
	options, err := c.writeColumns(ctx, node.ID, node.Field.ID)
	if err != nil {
		return board{}, err
	}
	return board{ID: node.ID, Title: title, URL: node.URL, StatusFieldID: node.Field.ID, StatusOptions: options}, nil
}

// writeColumns replaces the default status options and adds a board layout view
// in one request, because a new project opens as a table with an In Progress column.
func (c *Client) writeColumns(ctx context.Context, boardID, fieldID string) (map[string]string, error) {
	columns := make([]string, 0, len(boardColumns))
	for _, column := range boardColumns {
		columns = append(columns, fmt.Sprintf(`{name: %q, color: %s, description: ""}`, column.name, column.color))
	}
	mutation := fmt.Sprintf(`mutation {
  columns: updateProjectV2Field(input: {fieldId: %q, singleSelectOptions: [%s]}) {
    projectV2Field { ... on ProjectV2SingleSelectField { options { id name } } }
  }
  view: createProjectV2View(input: {projectId: %q, name: %q, layout: BOARD_LAYOUT}) { clientMutationId }
}`, fieldID, strings.Join(columns, ", "), boardID, boardViewName)
	var response struct {
		Columns struct {
			ProjectV2Field struct {
				Options []struct{ ID, Name string }
			}
		}
	}
	if err := c.do(ctx, mutation, nil, &response); err != nil {
		return nil, fmt.Errorf("configuring board: %w", err)
	}
	options := make(map[string]string, len(boardColumns))
	for _, option := range response.Columns.ProjectV2Field.Options {
		options[option.Name] = option.ID
	}
	return options, nil
}

// AddComponent registers a directory of a registered repository as a project of its
// own, named repository/component after the directory unless component is given. It
// uses the repository's board, so registering it writes only its label.
func (c *Client) AddComponent(ctx context.Context, repo Repo, dir, component string) (Project, error) {
	if err := validDir(dir); err != nil {
		return Project{}, err
	}
	if component == "" {
		component = path.Base(dir)
	}
	if err := validComponent(component); err != nil {
		return Project{}, err
	}
	project := Project{Name: repo.Name + "/" + component, Repo: repo.String(), Path: dir}
	if n := utf8.RuneCountInString(project.Label()); n > maxLabelName {
		return Project{}, fmt.Errorf("label %s is %d characters, over GitHub's %d: pass a shorter --name", project.Label(), n, maxLabelName)
	}
	if n := utf8.RuneCountInString(project.registration()); n > maxLabelDescription {
		return Project{}, fmt.Errorf("registration %s is %d characters, over GitHub's %d for a label description", project.registration(), n, maxLabelDescription)
	}
	meta, err := c.refreshMetadata(ctx)
	if err != nil {
		return Project{}, err
	}
	root, ok := meta.project(repo.Name)
	if !ok || !strings.EqualFold(root.Repo, repo.String()) {
		return Project{}, fmt.Errorf("repository %s is not registered: run project add %s first", repo, repo)
	}
	if root.BoardID == "" {
		return Project{}, errNoBoard(root)
	}
	for _, other := range meta.components(root) {
		switch {
		case other.Name == project.Name && other.Path == dir:
			// Repeating a registration gives a component registered before the
			// Component field its option.
			if err := c.ensureComponentOption(ctx, meta, root, other); err != nil {
				return Project{}, err
			}
			found, _ := meta.project(other.Name)
			return found, nil
		case other.Name == project.Name:
			return Project{}, fmt.Errorf("project %q is already registered for %s", project.Name, other.registration())
		case other.Path == dir:
			return Project{}, fmt.Errorf("%s is already registered as project %q", dir, other.Name)
		}
	}
	if err := c.ensureComponentOption(ctx, meta, root, project); err != nil {
		return Project{}, err
	}
	labelID, err := c.writeLabel(ctx, meta, project.Label(), project.registration())
	if err != nil {
		return Project{}, err
	}
	// The component shares everything else with its repository, so the cache is
	// updated here instead of read again from GitHub.
	root, _ = meta.project(root.Name)
	root.Name, root.Path, root.LabelID = project.Name, project.Path, labelID
	meta.Projects = append(meta.Projects, root)
	if err := c.saveMetadata(meta); err != nil {
		return Project{}, err
	}
	return root, nil
}

// fieldOption is one option of a single-select board field. Updating a field's
// options replaces them, so each existing one is sent back whole with its ID, which
// keeps the values tickets already have.
type fieldOption struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

const fieldOptionsSelection = `projectV2Field { ... on ProjectV2SingleSelectField { id options { id name } } }`

type fieldOptionsResult struct {
	ProjectV2Field struct {
		ID      string
		Options []struct{ ID, Name string }
	}
}

// ensureComponentOption gives the repository's board a Component option for the
// component, creating the field on first use, and records it in the cache.
func (c *Client) ensureComponentOption(ctx context.Context, meta *metadata, root, component Project) error {
	if _, ok := root.ComponentOptions[component.componentName()]; ok {
		return nil
	}
	option := fieldOption{Name: component.componentName(), Color: "GRAY", Description: component.Path}
	var result fieldOptionsResult
	if root.ComponentFieldID == "" {
		var response struct{ CreateProjectV2Field fieldOptionsResult }
		mutation := `mutation($board: ID!, $name: String!, $options: [ProjectV2SingleSelectFieldOptionInput!]) {
  createProjectV2Field(input: {projectId: $board, dataType: SINGLE_SELECT, name: $name, singleSelectOptions: $options}) { ` + fieldOptionsSelection + ` }
}`
		variables := map[string]any{"board": root.BoardID, "name": componentField, "options": []fieldOption{option}}
		if err := c.do(ctx, mutation, variables, &response); err != nil {
			return fmt.Errorf("creating Component field: %w", err)
		}
		result = response.CreateProjectV2Field
	} else {
		options, err := c.fieldOptions(ctx, root.ComponentFieldID)
		if err != nil {
			return err
		}
		if result, err = c.writeFieldOptions(ctx, root.ComponentFieldID, append(options, option)); err != nil {
			return err
		}
	}
	options := make(map[string]string, len(result.ProjectV2Field.Options))
	for _, option := range result.ProjectV2Field.Options {
		options[option.Name] = option.ID
	}
	meta.setComponentField(root.Name, result.ProjectV2Field.ID, options)
	return c.saveMetadata(meta)
}

// removeComponentOption deletes a component's option from its repository's board,
// and the field with its last option. GitHub clears the value on every ticket that
// had it, so those tickets fall back to the repository.
func (c *Client) removeComponentOption(ctx context.Context, component Project) error {
	if _, ok := component.ComponentOptions[component.componentName()]; !ok {
		return nil
	}
	options, err := c.fieldOptions(ctx, component.ComponentFieldID)
	if err != nil {
		return err
	}
	options = slices.DeleteFunc(options, func(option fieldOption) bool { return option.Name == component.componentName() })
	if len(options) > 0 {
		_, err = c.writeFieldOptions(ctx, component.ComponentFieldID, options)
		return err
	}
	mutation := `mutation($field: ID!) { deleteProjectV2Field(input: {fieldId: $field}) { clientMutationId } }`
	if err := c.do(ctx, mutation, map[string]any{"field": component.ComponentFieldID}, nil); err != nil {
		return fmt.Errorf("deleting Component field: %w", err)
	}
	return nil
}

// fieldOptions reads a field's options with their colors and descriptions, which
// the cache does not hold.
func (c *Client) fieldOptions(ctx context.Context, fieldID string) ([]fieldOption, error) {
	var response struct {
		Node struct{ Options []fieldOption }
	}
	query := `query($id: ID!) { node(id: $id) { ... on ProjectV2SingleSelectField { options { id name color description } } } }`
	if err := c.do(ctx, query, map[string]any{"id": fieldID}, &response); err != nil {
		return nil, fmt.Errorf("reading Component options: %w", err)
	}
	return response.Node.Options, nil
}

func (c *Client) writeFieldOptions(ctx context.Context, fieldID string, options []fieldOption) (fieldOptionsResult, error) {
	var response struct{ UpdateProjectV2Field fieldOptionsResult }
	mutation := `mutation($field: ID!, $options: [ProjectV2SingleSelectFieldOptionInput!]) {
  updateProjectV2Field(input: {fieldId: $field, singleSelectOptions: $options}) { ` + fieldOptionsSelection + ` }
}`
	if err := c.do(ctx, mutation, map[string]any{"field": fieldID, "options": options}, &response); err != nil {
		return fieldOptionsResult{}, fmt.Errorf("updating Component options: %w", err)
	}
	return response.UpdateProjectV2Field, nil
}

// writeLabel creates a routing label, or restores the registration on a label kept
// by a removed project. The description maps the project to its repository.
func (c *Client) writeLabel(ctx context.Context, meta *metadata, name, description string) (string, error) {
	var existing struct {
		Repository struct {
			Label *struct{ ID string }
		}
	}
	variables := map[string]any{"owner": c.repo.Owner, "name": c.repo.Name, "label": name}
	query := `query($owner: String!, $name: String!, $label: String!) { repository(owner: $owner, name: $name) { label(name: $label) { id } } }`
	if err := c.do(ctx, query, variables, &existing); err != nil {
		return "", fmt.Errorf("reading label: %w", err)
	}
	if existing.Repository.Label != nil {
		id := existing.Repository.Label.ID
		return id, c.describeLabel(ctx, id, description)
	}
	var created struct {
		CreateLabel struct {
			Label struct{ ID string }
		}
	}
	mutation := `mutation($repository: ID!, $name: String!, $description: String!) {
  createLabel(input: {repositoryId: $repository, name: $name, color: "0969da", description: $description}) { label { id } }
}`
	variables = map[string]any{"repository": meta.RepositoryID, "name": name, "description": description}
	if err := c.do(ctx, mutation, variables, &created); err != nil {
		return "", fmt.Errorf("creating label: %w", err)
	}
	return created.CreateLabel.Label.ID, nil
}

func (c *Client) describeLabel(ctx context.Context, labelID, description string) error {
	mutation := `mutation($id: ID!, $description: String!) { updateLabel(input: {id: $id, description: $description}) { clientMutationId } }`
	if err := c.do(ctx, mutation, map[string]any{"id": labelID, "description": description}, nil); err != nil {
		return fmt.Errorf("updating label: %w", err)
	}
	return nil
}

// RemoveProject unregisters a project with no open tickets and clears its label's
// registration, so closed tickets keep their label. A repository also loses its
// board, so closed tickets that were only on the board get the label first, or they
// would lose their project with it. A repository with components keeps them until
// they are removed, because they use its board.
func (c *Client) RemoveProject(ctx context.Context, name string) error {
	var project Project
	var summary ProjectSummary
	var onBoard []Ticket
	err := c.withProject(ctx, name, func(meta *metadata, found Project) (bool, error) {
		var stale bool
		var err error
		project = found
		summary, onBoard, stale, err = c.summarize(ctx, meta, found)
		if err != nil || stale {
			return stale, err
		}
		if components := meta.components(found); len(components) > 0 {
			return false, fmt.Errorf("project %q has components: remove %s first", name, components[0].Name)
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	if summary.Open > 0 {
		return fmt.Errorf("project %q has %d open tickets: close or move them first", name, summary.Open)
	}
	if len(onBoard) > 0 {
		fields := make([]string, 0, len(onBoard))
		for i, ticket := range onBoard {
			fields = append(fields, fmt.Sprintf(`label%d: addLabelsToLabelable(input: {labelableId: %q, labelIds: [%q]}) { clientMutationId }`, i, ticket.ID, project.LabelID))
		}
		if err := c.write(ctx, fields); err != nil {
			return fmt.Errorf("labelling closed tickets: %w", err)
		}
	}
	if project.BoardID != "" && !project.Component() {
		mutation := `mutation($id: ID!) { deleteProjectV2(input: {projectId: $id}) { clientMutationId } }`
		if err := c.do(ctx, mutation, map[string]any{"id": project.BoardID}, nil); err != nil {
			return fmt.Errorf("deleting board: %w", err)
		}
	}
	// The option goes first, so a failure leaves the component registered and the
	// removal can simply be repeated.
	if project.Component() {
		if err := c.removeComponentOption(ctx, project); err != nil {
			return err
		}
	}
	if err := c.describeLabel(ctx, project.LabelID, ""); err != nil {
		return err
	}
	_, err = c.refreshMetadata(ctx)
	return err
}

// ProjectSummary counts a project's tickets. A repository's counts include its
// components' tickets.
func (c *Client) ProjectSummary(ctx context.Context, project Project) (ProjectSummary, error) {
	var summary ProjectSummary
	err := c.withProject(ctx, project.Name, func(meta *metadata, found Project) (bool, error) {
		var stale bool
		var err error
		summary, _, stale, err = c.summarize(ctx, meta, found)
		return stale, err
	})
	return summary, err
}

// summarize counts labelled tickets in one request. Board-only tickets are read
// rather than counted, because a search count would include tickets labelled for
// another project. It reports stale cached components instead of counting.
func (c *Client) summarize(ctx context.Context, meta *metadata, project Project) (ProjectSummary, []Ticket, bool, error) {
	labels := meta.scopeLabels(project)
	onBoard, err := c.boardOnlyTickets(ctx, meta, project, []string{stateOpen, StateClosed}, ListOptions{})
	if err != nil {
		return ProjectSummary{}, nil, false, fmt.Errorf("counting tickets: %w", err)
	}
	base := fmt.Sprintf(`repo:%s %s is:issue`, c.repo, labelScope(labels))
	query := `query($owner: String!, $name: String!, $open: String!, $closed: String!, $registrations: String, $checkRegistrations: Boolean!) {
  repository(owner: $owner, name: $name) { ` + registrationFields + ` }
  open: search(query: $open, type: ISSUE, first: 1) { issueCount }
  closed: search(query: $closed, type: ISSUE, first: 1) { issueCount }
}`
	var response struct {
		Repository struct {
			Registrations struct{ Nodes []labelNode }
		}
		Open, Closed struct{ IssueCount int }
	}
	variables := c.variables(registrationVariables(project))
	variables["open"], variables["closed"] = base+" is:open", base+" is:closed"
	if err := c.do(ctx, query, variables, &response); err != nil {
		return ProjectSummary{}, nil, false, fmt.Errorf("counting tickets: %w", err)
	}
	if meta.staleComponents(project, response.Repository.Registrations.Nodes) {
		return ProjectSummary{}, nil, true, nil
	}
	summary := ProjectSummary{Project: project, Open: response.Open.IssueCount, Closed: response.Closed.IssueCount}
	for _, ticket := range onBoard {
		if ticket.State == stateOpen {
			summary.Open++
		} else {
			summary.Closed++
		}
	}
	return summary, onBoard, false, nil
}

// ResolveProject looks up a registered project by name.
func (c *Client) ResolveProject(ctx context.Context, name string) (Project, error) {
	return c.lookupProject(ctx, name)
}

// errNoBoard explains a project registered without a usable board.
func errNoBoard(project Project) error {
	return errors.New("project " + project.Name + " has no board: run project add " + project.Repo)
}
