package tracker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// importWrites is the requests that change GitHub for each moved issue.
	importWrites = 3
	// importInterval keeps those writes near 450 an hour, because GitHub allows
	// about 500 content creation requests an hour.
	importInterval = time.Hour * importWrites / 450
	// scanBatchSize bounds one query to about 10 points.
	scanBatchSize = 10
)

// sourceFields selects authors only. Keep titles, bodies, and comment text out of
// it, because public issues are untrusted input.
const sourceFields = `isPrivate
    issues(states: OPEN, first: 100, after: $cursor, orderBy: {field: CREATED_AT, direction: ASC}) {
      nodes { id number author { login } comments(first: 100) { nodes { author { login } } pageInfo { hasNextPage } } }
      pageInfo { hasNextPage endCursor }
    }`

// RepoScan counts one repository's open issues by who wrote them. It holds no
// issue text, because public issues are untrusted input. Others counts issues with
// text from anyone untrusted, including in a private repository where they can move.
type RepoScan struct {
	Repo    string
	Private bool
	Open    int
	Movable int
	Others  int
}

type sourceIssue struct {
	ID          string
	Number      int
	movable     bool
	trustedOnly bool
}

type sourceAuthor struct {
	Author *struct{ Login string }
}

func (a sourceAuthor) trusted(authors []string) bool {
	if a.Author == nil {
		return false // A deleted account can no longer be attributed.
	}
	return slices.ContainsFunc(authors, func(login string) bool {
		return strings.EqualFold(login, a.Author.Login)
	})
}

type sourceNode struct {
	sourceAuthor
	ID       string
	Number   int
	State    string
	Comments struct {
		Nodes    []sourceAuthor
		PageInfo struct{ HasNextPage bool }
	}
}

// trustedOnly reports whether every author is known and trusted. An issue with
// more comments than one page does not qualify, because its authors stay unknown.
func (n sourceNode) trustedOnly(authors []string) bool {
	return n.trusted(authors) && !n.Comments.PageInfo.HasNextPage &&
		!slices.ContainsFunc(n.Comments.Nodes, func(comment sourceAuthor) bool { return !comment.trusted(authors) })
}

type sourceIssuePage struct {
	IsPrivate bool
	Issues    struct {
		Nodes    []sourceNode
		PageInfo struct {
			HasNextPage bool
			EndCursor   string
		}
	}
}

func (p sourceIssuePage) issues(authors []string) []sourceIssue {
	issues := make([]sourceIssue, 0, len(p.Issues.Nodes))
	for _, node := range p.Issues.Nodes {
		trustedOnly := node.trustedOnly(authors)
		issues = append(issues, sourceIssue{ID: node.ID, Number: node.Number, movable: p.IsPrivate || trustedOnly, trustedOnly: trustedOnly})
	}
	return issues
}

// readSource lists a repository's open issues with their authors, oldest first,
// from the page after cursor.
func (c *Client) readSource(ctx context.Context, repo Repo, authors []string, cursor any) (bool, []sourceIssue, error) {
	query := `query($owner: String!, $name: String!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    ` + sourceFields + `
  }
}`
	var issues []sourceIssue
	for {
		var response struct{ Repository sourceIssuePage }
		variables := map[string]any{"owner": repo.Owner, "name": repo.Name, "cursor": cursor}
		if err := c.do(ctx, query, variables, &response); err != nil {
			return false, nil, fmt.Errorf("reading issues of %s: %w", repo, err)
		}
		page := response.Repository
		issues = append(issues, page.issues(authors)...)
		if !page.Issues.PageInfo.HasNextPage {
			return page.IsPrivate, issues, nil
		}
		cursor = page.Issues.PageInfo.EndCursor
	}
}

// stillMovable rereads one public issue just before its transfer, because a run
// takes hours and anyone can comment on a queued issue meanwhile.
func (c *Client) stillMovable(ctx context.Context, issueID string, authors []string) (bool, error) {
	query := `query($id: ID!) {
  node(id: $id) { ... on Issue { state author { login } comments(first: 100) { nodes { author { login } } pageInfo { hasNextPage } } } }
}`
	var response struct{ Node *sourceNode }
	if err := c.do(ctx, query, map[string]any{"id": issueID}, &response); err != nil {
		return false, fmt.Errorf("rereading authors: %w", err)
	}
	return response.Node != nil && response.Node.State == stateOpen && response.Node.trustedOnly(authors), nil
}

// reposWithIssues lists the owner's unarchived, non-fork repositories with open
// issues, leaving out the tracker.
func (c *Client) reposWithIssues(ctx context.Context) ([]Repo, error) {
	query := `query($owner: String!, $cursor: String) {
  repositoryOwner(login: $owner) {
    repositories(first: 100, after: $cursor, ownerAffiliations: [OWNER], isFork: false, isArchived: false, orderBy: {field: NAME, direction: ASC}) {
      nodes { nameWithOwner issues(states: OPEN) { totalCount } }
      pageInfo { hasNextPage endCursor }
    }
  }
}`
	var repos []Repo
	var cursor any
	for {
		var response struct {
			RepositoryOwner struct {
				Repositories struct {
					Nodes []struct {
						NameWithOwner string
						Issues        struct{ TotalCount int }
					}
					PageInfo struct {
						HasNextPage bool
						EndCursor   string
					}
				}
			}
		}
		if err := c.do(ctx, query, map[string]any{"owner": c.repo.Owner, "cursor": cursor}, &response); err != nil {
			return nil, fmt.Errorf("listing repositories: %w", err)
		}
		repositories := response.RepositoryOwner.Repositories
		for _, node := range repositories.Nodes {
			if node.Issues.TotalCount == 0 || strings.EqualFold(node.NameWithOwner, c.repo.String()) {
				continue
			}
			repo, err := ParseRepo(node.NameWithOwner)
			if err != nil {
				return nil, err
			}
			repos = append(repos, repo)
		}
		if !repositories.PageInfo.HasNextPage {
			return repos, nil
		}
		cursor = repositories.PageInfo.EndCursor
	}
}

// scanBatch reads several repositories in one request, following up only on
// repositories with more than one page of issues.
func (c *Client) scanBatch(ctx context.Context, repos []Repo, authors []string) ([]RepoScan, error) {
	var fields strings.Builder
	for i, repo := range repos {
		fmt.Fprintf(&fields, "r%d: repository(owner: %q, name: %q) {\n    %s\n  }\n", i, repo.Owner, repo.Name, sourceFields)
	}
	var response map[string]sourceIssuePage
	query := "query($cursor: String) {\n" + fields.String() + "}"
	if err := c.do(ctx, query, map[string]any{"cursor": nil}, &response); err != nil {
		return nil, fmt.Errorf("reading issues: %w", err)
	}
	scans := make([]RepoScan, 0, len(repos))
	for i, repo := range repos {
		page := response["r"+strconv.Itoa(i)]
		issues := page.issues(authors)
		if page.Issues.PageInfo.HasNextPage {
			_, rest, err := c.readSource(ctx, repo, authors, page.Issues.PageInfo.EndCursor)
			if err != nil {
				return nil, err
			}
			issues = append(issues, rest...)
		}
		scan := RepoScan{Repo: repo.String(), Private: page.IsPrivate, Open: len(issues)}
		for _, issue := range issues {
			if issue.movable {
				scan.Movable++
			}
			if !issue.trustedOnly {
				scan.Others++
			}
		}
		scans = append(scans, scan)
	}
	return scans, nil
}

// ScanRepos reports every unarchived, non-fork repository of the tracker's owner
// with open issues. The owner's text is always trusted; authors adds more logins.
func (c *Client) ScanRepos(ctx context.Context, authors []string) ([]RepoScan, error) {
	repos, err := c.reposWithIssues(ctx)
	if err != nil {
		return nil, err
	}
	trusted := append([]string{c.repo.Owner}, authors...)
	scans := []RepoScan{}
	for batch := range slices.Chunk(repos, scanBatchSize) {
		batchScans, err := c.scanBatch(ctx, batch, trusted)
		if err != nil {
			return nil, err
		}
		scans = append(scans, batchScans...)
	}
	return scans, nil
}

// ImportOptions describes moving a repository's open issues into the tracker.
type ImportOptions struct {
	Repo    Repo
	Authors []string
	DryRun  bool
	Limit   int
	// OnMove reports each transfer as it finishes, because a run can take an hour.
	OnMove func(source, ticket int) error
}

// ImportResult counts a run. Remaining is what a later run would still move.
type ImportResult struct {
	Moved     int
	Remaining int
	Skipped   int
}

// ImportIssues transfers a repository's movable issues into the tracker. A public
// issue cannot move back, so the tracker must be private, and only issues written
// entirely by trusted authors move.
func (c *Client) ImportIssues(ctx context.Context, opts ImportOptions) (ImportResult, error) {
	if opts.Limit < 0 {
		return ImportResult{}, fmt.Errorf("invalid limit %d: expected 0 or more", opts.Limit)
	}
	if strings.EqualFold(opts.Repo.String(), c.repo.String()) {
		return ImportResult{}, errors.New("the tracker cannot import from itself")
	}
	if !strings.EqualFold(opts.Repo.Owner, c.repo.Owner) {
		return ImportResult{}, fmt.Errorf("%s has a different owner than tracker %s: GitHub only transfers issues between repositories of one owner", opts.Repo, c.repo)
	}
	meta, err := c.refreshMetadata(ctx)
	if err != nil {
		return ImportResult{}, err
	}
	if !meta.IsPrivate {
		return ImportResult{}, fmt.Errorf("tracker %s is not private: refusing to move issues into it", c.repo)
	}
	trusted := append([]string{c.repo.Owner}, opts.Authors...)
	private, issues, err := c.readSource(ctx, opts.Repo, trusted, nil)
	if err != nil {
		return ImportResult{}, err
	}
	var result ImportResult
	var movable []sourceIssue
	for _, issue := range issues {
		if issue.movable {
			movable = append(movable, issue)
		} else {
			result.Skipped++
		}
	}
	result.Remaining = len(movable)
	if opts.DryRun || len(movable) == 0 {
		return result, nil
	}
	project, ok := meta.project(opts.Repo.Name)
	if !ok || project.BoardID == "" || !strings.EqualFold(project.Repo, opts.Repo.String()) {
		if project, err = c.AddProject(ctx, opts.Repo); err != nil {
			return result, err
		}
	}
	for i, issue := range movable {
		if opts.Limit > 0 && result.Moved == opts.Limit {
			break
		}
		if i > 0 {
			if err := c.pause(ctx, importInterval); err != nil {
				return result, err
			}
		}
		if !private {
			ok, err := c.stillMovable(ctx, issue.ID, trusted)
			if err != nil {
				return result, fmt.Errorf("%s#%d: %w", opts.Repo, issue.Number, err)
			}
			if !ok {
				result.Remaining--
				result.Skipped++
				continue
			}
		}
		number, err := c.importIssue(ctx, meta.RepositoryID, project, issue)
		if number != 0 {
			result.Moved++
			result.Remaining--
			if opts.OnMove != nil {
				if err := opts.OnMove(issue.Number, number); err != nil {
					return result, err
				}
			}
		}
		if err != nil {
			return result, fmt.Errorf("%s#%d: %w", opts.Repo, issue.Number, err)
		}
	}
	return result, nil
}

// importIssue transfers one issue and files it under its project in importWrites
// requests. The returned
// number is set once the transfer succeeded, even when a later step failed.
func (c *Client) importIssue(ctx context.Context, repositoryID string, project Project, issue sourceIssue) (int, error) {
	var response struct {
		TransferIssue struct {
			Issue struct {
				ID     string
				Number int
			}
		}
	}
	mutation := `mutation($issue: ID!, $repository: ID!) {
  transferIssue(input: {issueId: $issue, repositoryId: $repository, createLabelsIfMissing: true}) { issue { id number } }
}`
	if err := c.do(ctx, mutation, map[string]any{"issue": issue.ID, "repository": repositoryID}, &response); err != nil {
		return 0, fmt.Errorf("transferring: %w", err)
	}
	moved := response.TransferIssue.Issue
	if err := c.fileTicket(ctx, project, moved.ID); err != nil {
		return moved.Number, fmt.Errorf("moved to ticket #%d, finish it with issue move: %w", moved.Number, err)
	}
	return moved.Number, nil
}

// fileTicket labels a ticket, adds it to the board, and sets Todo in two requests.
// It uses the project it is given, because metadata read right after project add
// may not include the new board yet.
func (c *Client) fileTicket(ctx context.Context, project Project, contentID string) error {
	var response struct {
		Item struct {
			Item struct{ ID string }
		}
	}
	mutation := `mutation($content: ID!, $label: ID!, $board: ID!) {
  label: addLabelsToLabelable(input: {labelableId: $content, labelIds: [$label]}) { clientMutationId }
  item: addProjectV2ItemById(input: {projectId: $board, contentId: $content}) { item { id } }
}`
	variables := map[string]any{"content": contentID, "label": project.LabelID, "board": project.BoardID}
	if err := c.do(ctx, mutation, variables, &response); err != nil {
		return err
	}
	field, err := statusMutation("status", project, response.Item.Item.ID, StatusTodo)
	if err != nil {
		return err
	}
	return c.write(ctx, []string{field})
}
