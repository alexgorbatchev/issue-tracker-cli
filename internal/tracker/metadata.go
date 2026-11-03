package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const labelPrefix = "repo:"

// Project is a code repository registered with the tracker, or a component: one
// directory of a registered repository with its own label on the repository's board.
type Project struct {
	Name          string            `json:"name"`
	Repo          string            `json:"repo"`
	Path          string            `json:"path,omitempty"`
	Public        bool              `json:"public"`
	LabelID       string            `json:"label_id"`
	BoardID       string            `json:"board_id,omitempty"`
	BoardURL      string            `json:"board_url,omitempty"`
	StatusFieldID string            `json:"status_field_id,omitempty"`
	StatusOptions map[string]string `json:"status_options,omitempty"`
}

// Label returns the routing label for the project.
func (p Project) Label() string {
	return labelPrefix + p.Name
}

// Component reports a project registered for one directory of its repository.
func (p Project) Component() bool {
	return p.Path != ""
}

// Root names the repository project whose board this project uses, which is the
// project itself unless it is a component.
func (p Project) Root() string {
	root, _, _ := strings.Cut(p.Name, "/")
	return root
}

// registration is the label description that registers a component: owner/name:path.
func (p Project) registration() string {
	return p.Repo + ":" + p.Path
}

// parseRegistration reads a routing label. A repository's label is named after it,
// and a component's after its repository and the component, as in repo:name/api.
func parseRegistration(name, description string) (Project, bool) {
	value, dir, component := strings.Cut(description, ":")
	repo, err := ParseRepo(value)
	if err != nil {
		return Project{}, false
	}
	project := Project{Name: name, Repo: repo.String(), Path: dir}
	if !component {
		if name != repo.Name {
			return Project{}, false
		}
		return project, true
	}
	root, child, ok := strings.Cut(name, "/")
	if !ok || root != repo.Name || validComponent(child) != nil || validDir(dir) != nil {
		return Project{}, false
	}
	return project, true
}

// board is a GitHub Project linked to the tracker repository.
type board struct {
	ID            string            `json:"id"`
	Title         string            `json:"title"`
	URL           string            `json:"url"`
	StatusFieldID string            `json:"status_field_id"`
	StatusOptions map[string]string `json:"status_options"`
}

// metadata holds IDs that rarely change. It carries no ticket data.
type metadata struct {
	RepositoryID string    `json:"repository_id"`
	OwnerID      string    `json:"owner_id"`
	HeadOID      string    `json:"head_oid"`
	IsPrivate    bool      `json:"is_private"`
	Projects     []Project `json:"projects"`
	Boards       []board   `json:"boards"`
}

func (m *metadata) project(name string) (Project, bool) {
	for _, project := range m.Projects {
		if project.Name == name {
			return project, true
		}
	}
	return Project{}, false
}

// projectAt picks the project for a directory of a repository: the component with
// the longest path holding it, or else the repository's own project.
func (m *metadata) projectAt(repo, dir string) (Project, bool) {
	var found Project
	ok := false
	for _, project := range m.Projects {
		if !strings.EqualFold(project.Repo, repo) || !within(dir, project.Path) {
			continue
		}
		if !ok || len(project.Path) > len(found.Path) {
			found, ok = project, true
		}
	}
	return found, ok
}

// within reports whether dir is path or below it. An empty path is the whole repository.
func within(dir, path string) bool {
	return path == "" || dir == path || strings.HasPrefix(dir, path+"/")
}

// components lists the components registered under a repository project.
func (m *metadata) components(project Project) []Project {
	if project.Component() {
		return nil
	}
	var found []Project
	for _, other := range m.Projects {
		if other.Component() && other.Root() == project.Name {
			found = append(found, other)
		}
	}
	return found
}

// scopeLabels lists the routing labels of a project's tickets. A repository covers
// the tickets of its components too.
func (m *metadata) scopeLabels(project Project) []string {
	labels := []string{project.Label()}
	for _, component := range m.components(project) {
		labels = append(labels, component.Label())
	}
	return labels
}

// staleComponents reports whether the labels read from GitHub register other
// components of a repository project than the cache holds.
func (m *metadata) staleComponents(project Project, labels []labelNode) bool {
	if project.Name == "" || project.Component() {
		return false
	}
	registered := map[string]string{}
	for _, label := range labels {
		name, ok := strings.CutPrefix(label.Name, labelPrefix)
		if !ok {
			continue
		}
		if found, ok := parseRegistration(name, label.Description); ok && found.Component() && found.Root() == project.Name {
			registered[found.Name] = found.Path
		}
	}
	cached := m.components(project)
	if len(cached) != len(registered) {
		return true
	}
	for _, component := range cached {
		if dir, ok := registered[component.Name]; !ok || dir != component.Path {
			return true
		}
	}
	return false
}

// unroutedScope is the search for tickets with none of the registered routing labels.
// Its results still hold tickets routed through a board or by a label registered
// since the cache was written, which routing drops afterwards.
func (m *metadata) unroutedScope() string {
	terms := make([]string, 0, len(m.Projects))
	for _, project := range m.Projects {
		terms = append(terms, "-label:"+strconv.Quote(project.Label()))
	}
	return strings.Join(terms, " ")
}

func (m *metadata) board(title string) (board, bool) {
	for _, item := range m.Boards {
		if item.Title == title {
			return item, true
		}
	}
	return board{}, false
}

func (c *Client) cachePath() string {
	return filepath.Join(c.cacheDir, c.repo.Owner+"-"+c.repo.Name+".json")
}

// metadata returns cached IDs, fetching them on the first use.
func (c *Client) metadata(ctx context.Context) (*metadata, error) {
	if c.meta != nil {
		return c.meta, nil
	}
	data, err := os.ReadFile(c.cachePath())
	if errors.Is(err, fs.ErrNotExist) {
		return c.refreshMetadata(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("reading metadata cache: %w", err)
	}
	var cached metadata
	if err := json.Unmarshal(data, &cached); err != nil {
		return c.refreshMetadata(ctx)
	}
	if err := c.checkPrivate(cached.IsPrivate); err != nil {
		return nil, err
	}
	c.meta = &cached
	return c.meta, nil
}

func (c *Client) saveMetadata(meta *metadata) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("encoding metadata cache: %w", err)
	}
	if err := os.MkdirAll(c.cacheDir, 0o700); err != nil {
		return fmt.Errorf("creating cache directory: %w", err)
	}
	tmp, err := os.CreateTemp(c.cacheDir, "metadata-*")
	if err != nil {
		return fmt.Errorf("creating metadata cache: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close() // the write failure is the error worth reporting
		return fmt.Errorf("writing metadata cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing metadata cache: %w", err)
	}
	if err := os.Rename(tmp.Name(), c.cachePath()); err != nil {
		return fmt.Errorf("replacing metadata cache: %w", err)
	}
	c.meta = meta
	return nil
}

const metadataQuery = `query($owner: String!, $name: String!, $labels: String, $boards: String, $withLabels: Boolean!, $withBoards: Boolean!) {
  repository(owner: $owner, name: $name) {
    id
    isPrivate
    owner { id }
    defaultBranchRef { target { oid } }
    labels(first: 100, query: "repo:", after: $labels) @include(if: $withLabels) {
      nodes { id name description }
      pageInfo { hasNextPage endCursor }
    }
    projectsV2(first: 100, after: $boards) @include(if: $withBoards) {
      nodes {
        id
        title
        url
        field(name: "Status") {
          ... on ProjectV2SingleSelectField { id options { id name } }
        }
      }
      pageInfo { hasNextPage endCursor }
    }
  }
}`

// labelNode is a label of the tracker repository.
type labelNode struct{ ID, Name, Description string }

type pageInfo struct {
	HasNextPage bool
	EndCursor   string
}

type metadataPage struct {
	Repository struct {
		ID               string
		IsPrivate        bool
		Owner            struct{ ID string }
		DefaultBranchRef struct {
			Target struct{ OID string }
		}
		Labels struct {
			Nodes    []labelNode
			PageInfo pageInfo
		}
		ProjectsV2 struct {
			Nodes []struct {
				ID, Title, URL string
				Field          struct {
					ID      string
					Options []struct{ ID, Name string }
				}
			}
			PageInfo pageInfo
		}
	}
}

// refreshMetadata rebuilds the cache from GitHub. Both connections share one
// request per page, so this costs one request until a connection overflows.
func (c *Client) refreshMetadata(ctx context.Context) (*metadata, error) {
	meta := &metadata{}
	variables := map[string]any{
		"owner": c.repo.Owner, "name": c.repo.Name,
		"labels": nil, "boards": nil, "withLabels": true, "withBoards": true,
	}
	for {
		var page metadataPage
		if err := c.do(ctx, metadataQuery, variables, &page); err != nil {
			return nil, fmt.Errorf("reading tracker metadata: %w", err)
		}
		meta.collect(page)
		labels, boards := page.Repository.Labels.PageInfo, page.Repository.ProjectsV2.PageInfo
		if !labels.HasNextPage && !boards.HasNextPage {
			break
		}
		variables["labels"], variables["boards"] = labels.EndCursor, boards.EndCursor
		variables["withLabels"], variables["withBoards"] = labels.HasNextPage, boards.HasNextPage
	}
	if err := c.checkPrivate(meta.IsPrivate); err != nil {
		return nil, err
	}
	meta.linkBoards()
	if err := c.readVisibility(ctx, meta); err != nil {
		return nil, err
	}
	if err := c.saveMetadata(meta); err != nil {
		return nil, err
	}
	return meta, nil
}

// readVisibility records which projects are public. A repository GitHub cannot
// resolve counts as public so that the copying warning errs on the safe side.
func (c *Client) readVisibility(ctx context.Context, meta *metadata) error {
	if len(meta.Projects) == 0 {
		return nil
	}
	var query strings.Builder
	query.WriteString("query {")
	for i, project := range meta.Projects {
		repo, err := ParseRepo(project.Repo)
		if err != nil {
			return err
		}
		fmt.Fprintf(&query, " r%d: repository(owner: %q, name: %q) { isPrivate }", i, repo.Owner, repo.Name)
	}
	query.WriteString(" }")
	response := map[string]*struct{ IsPrivate bool }{}
	if err := c.do(ctx, query.String(), nil, &response); err != nil && !hasGraphQLType(err, graphQLNotFoundType) {
		return fmt.Errorf("reading project visibility: %w", err)
	}
	for i := range meta.Projects {
		node := response["r"+strconv.Itoa(i)]
		meta.Projects[i].Public = node == nil || !node.IsPrivate
	}
	return nil
}

func (m *metadata) collect(page metadataPage) {
	m.RepositoryID, m.OwnerID = page.Repository.ID, page.Repository.Owner.ID
	m.HeadOID, m.IsPrivate = page.Repository.DefaultBranchRef.Target.OID, page.Repository.IsPrivate
	for _, label := range page.Repository.Labels.Nodes {
		name, ok := strings.CutPrefix(label.Name, labelPrefix)
		if !ok {
			continue
		}
		project, ok := parseRegistration(name, label.Description)
		if !ok {
			continue // an unregistered project keeps its label but loses its repository
		}
		project.LabelID = label.ID
		m.Projects = append(m.Projects, project)
	}
	for _, node := range page.Repository.ProjectsV2.Nodes {
		item := board{ID: node.ID, Title: node.Title, URL: node.URL, StatusFieldID: node.Field.ID}
		item.StatusOptions = make(map[string]string, len(node.Field.Options))
		for _, option := range node.Field.Options {
			item.StatusOptions[option.Name] = option.ID
		}
		m.Boards = append(m.Boards, item)
	}
}

// linkBoards gives each project its board. A component uses its repository's board,
// so a ticket keeps its status when it moves between them.
func (m *metadata) linkBoards() {
	for i := range m.Projects {
		if item, ok := m.board(m.Projects[i].Root()); ok {
			m.Projects[i].attach(item)
		}
	}
}

func (p *Project) attach(item board) {
	p.BoardID, p.BoardURL = item.ID, item.URL
	p.StatusFieldID, p.StatusOptions = item.StatusFieldID, item.StatusOptions
}

// lookupProject finds a project, refreshing once so that a project registered on
// another machine appears as an ordinary cache miss.
func (c *Client) lookupProject(ctx context.Context, name string) (Project, error) {
	meta, err := c.metadata(ctx)
	if err != nil {
		return Project{}, err
	}
	if project, ok := meta.project(name); ok {
		return project, nil
	}
	if meta, err = c.refreshMetadata(ctx); err != nil {
		return Project{}, err
	}
	if project, ok := meta.project(name); ok {
		return project, nil
	}
	return Project{}, fmt.Errorf("unknown project %q: register it with project add", name)
}

// withFreshIDs retries once with refreshed metadata when GitHub rejects a stale ID.
func (c *Client) withFreshIDs(ctx context.Context, write func(*metadata) error) error {
	meta, err := c.metadata(ctx)
	if err != nil {
		return err
	}
	err = write(meta)
	if !hasGraphQLType(err, graphQLNotFoundType) {
		return err
	}
	if meta, err = c.refreshMetadata(ctx); err != nil {
		return err
	}
	return write(meta)
}
