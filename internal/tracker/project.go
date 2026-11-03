package tracker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cli/go-gh/v2/pkg/repository"
)

const boardViewName = "Board"

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

// CurrentProject maps the working directory's GitHub remote to a project without
// calling GitHub unless refresh is set and the cached metadata has no match.
func (c *Client) CurrentProject(ctx context.Context, refresh bool) (Project, bool, error) {
	repo, ok := currentRepo()
	if !ok {
		return Project{}, false, nil
	}
	meta, err := c.metadata(ctx)
	if err != nil {
		return Project{}, false, err
	}
	if project, ok := meta.projectForRepo(repo); ok || !refresh {
		return project, ok, nil
	}
	if meta, err = c.refreshMetadata(ctx); err != nil {
		return Project{}, false, err
	}
	project, ok := meta.projectForRepo(repo)
	return project, ok, nil
}

// CurrentRepo returns the working directory's GitHub repository.
func CurrentRepo() (Repo, bool) {
	value, ok := currentRepo()
	if !ok {
		return Repo{}, false
	}
	repo, err := ParseRepo(value)
	return repo, err == nil
}

func currentRepo() (string, bool) {
	repo, err := repository.Current()
	if err != nil || repo.Host != defaultHost {
		return "", false
	}
	return repo.Owner + "/" + repo.Name, true
}

func (m *metadata) projectForRepo(repo string) (Project, bool) {
	for _, project := range m.Projects {
		if strings.EqualFold(project.Repo, repo) {
			return project, true
		}
	}
	return Project{}, false
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
	labelID, err := c.writeLabel(ctx, meta, repo)
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

// writeLabel creates the routing label, or restores the repository on a label kept
// by a removed project. The description holds owner/name for the project mapping.
func (c *Client) writeLabel(ctx context.Context, meta *metadata, repo Repo) (string, error) {
	name := labelPrefix + repo.Name
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
		return id, c.describeLabel(ctx, id, repo.String())
	}
	var created struct {
		CreateLabel struct {
			Label struct{ ID string }
		}
	}
	mutation := `mutation($repository: ID!, $name: String!, $description: String!) {
  createLabel(input: {repositoryId: $repository, name: $name, color: "0969da", description: $description}) { label { id } }
}`
	variables = map[string]any{"repository": meta.RepositoryID, "name": name, "description": repo.String()}
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

// RemoveProject unregisters a project with no open tickets. It deletes the board
// and clears the label's repository, so closed tickets keep their label.
func (c *Client) RemoveProject(ctx context.Context, name string) error {
	project, err := c.lookupProject(ctx, name)
	if err != nil {
		return err
	}
	summary, err := c.ProjectSummary(ctx, project)
	if err != nil {
		return err
	}
	if summary.Open > 0 {
		return fmt.Errorf("project %q has %d open tickets: close or move them first", name, summary.Open)
	}
	if project.BoardID != "" {
		mutation := `mutation($id: ID!) { deleteProjectV2(input: {projectId: $id}) { clientMutationId } }`
		if err := c.do(ctx, mutation, map[string]any{"id": project.BoardID}, nil); err != nil {
			return fmt.Errorf("deleting board: %w", err)
		}
	}
	if err := c.describeLabel(ctx, project.LabelID, ""); err != nil {
		return err
	}
	_, err = c.refreshMetadata(ctx)
	return err
}

// ProjectSummary counts a project's tickets in one request.
func (c *Client) ProjectSummary(ctx context.Context, project Project) (ProjectSummary, error) {
	base := fmt.Sprintf(`repo:%s label:"%s" is:issue`, c.repo, project.Label())
	onBoard := fmt.Sprintf(`repo:%s is:issue %s`, c.repo, c.boardOnlyScope(project))
	query := `query($open: String!, $closed: String!, $boardOpen: String!, $boardClosed: String!, $hasBoard: Boolean!) {
  open: search(query: $open, type: ISSUE, first: 1) { issueCount }
  closed: search(query: $closed, type: ISSUE, first: 1) { issueCount }
  boardOpen: search(query: $boardOpen, type: ISSUE, first: 1) @include(if: $hasBoard) { issueCount }
  boardClosed: search(query: $boardClosed, type: ISSUE, first: 1) @include(if: $hasBoard) { issueCount }
}`
	var response struct {
		Open, Closed, BoardOpen, BoardClosed struct{ IssueCount int }
	}
	variables := map[string]any{
		"open": base + " is:open", "closed": base + " is:closed",
		"boardOpen": onBoard + " is:open", "boardClosed": onBoard + " is:closed",
		"hasBoard": project.BoardURL != "",
	}
	if err := c.do(ctx, query, variables, &response); err != nil {
		return ProjectSummary{}, fmt.Errorf("counting tickets: %w", err)
	}
	return ProjectSummary{
		Project: project,
		Open:    response.Open.IssueCount + response.BoardOpen.IssueCount,
		Closed:  response.Closed.IssueCount + response.BoardClosed.IssueCount,
	}, nil
}

// ResolveProject looks up a registered project by name.
func (c *Client) ResolveProject(ctx context.Context, name string) (Project, error) {
	return c.lookupProject(ctx, name)
}

// errNoBoard explains a project registered without a usable board.
func errNoBoard(project Project) error {
	return errors.New("project " + project.Name + " has no board: run project add " + project.Repo)
}
