package tracker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// importInterval paces transfers below GitHub's content creation limit, which
// allows about 500 writes an hour.
const importInterval = 8 * time.Second

// RepoScan counts one repository's open issues by who wrote them. It holds no
// issue text, because public issues are untrusted input.
type RepoScan struct {
	Repo    string
	Private bool
	Open    int
	Movable int
	Others  int
}

type sourceIssue struct {
	ID      string
	Number  int
	movable bool
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

type sourceIssuePage struct {
	IsPrivate bool
	Issues    struct {
		Nodes []struct {
			sourceAuthor
			ID       string
			Number   int
			Comments struct {
				Nodes    []sourceAuthor
				PageInfo struct{ HasNextPage bool }
			}
		}
		PageInfo struct {
			HasNextPage bool
			EndCursor   string
		}
	}
}

// readSource lists a repository's open issues with their authors, oldest first.
// It requests no titles, bodies, or comment text. An issue with more comments
// than one page counts as not movable, because its authors stay unknown.
func (c *Client) readSource(ctx context.Context, repo Repo, authors []string) (bool, []sourceIssue, error) {
	query := `query($owner: String!, $name: String!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    isPrivate
    issues(states: OPEN, first: 100, after: $cursor, orderBy: {field: CREATED_AT, direction: ASC}) {
      nodes { id number author { login } comments(first: 100) { nodes { author { login } } pageInfo { hasNextPage } } }
      pageInfo { hasNextPage endCursor }
    }
  }
}`
	var issues []sourceIssue
	var cursor any
	for {
		var response struct{ Repository sourceIssuePage }
		variables := map[string]any{"owner": repo.Owner, "name": repo.Name, "cursor": cursor}
		if err := c.do(ctx, query, variables, &response); err != nil {
			return false, nil, fmt.Errorf("reading issues of %s: %w", repo, err)
		}
		page := response.Repository
		for _, node := range page.Issues.Nodes {
			movable := page.IsPrivate
			if !movable {
				movable = node.trusted(authors) && !node.Comments.PageInfo.HasNextPage &&
					!slices.ContainsFunc(node.Comments.Nodes, func(comment sourceAuthor) bool { return !comment.trusted(authors) })
			}
			issues = append(issues, sourceIssue{ID: node.ID, Number: node.Number, movable: movable})
		}
		if !page.Issues.PageInfo.HasNextPage {
			return page.IsPrivate, issues, nil
		}
		cursor = page.Issues.PageInfo.EndCursor
	}
}

// ScanRepos reports every unarchived, non-fork repository of the tracker's owner
// with open issues. The owner's text is always trusted; authors adds more logins.
func (c *Client) ScanRepos(ctx context.Context, authors []string) ([]RepoScan, error) {
	query := `query($owner: String!, $cursor: String) {
  repositoryOwner(login: $owner) {
    repositories(first: 100, after: $cursor, ownerAffiliations: [OWNER], isFork: false, isArchived: false, orderBy: {field: NAME, direction: ASC}) {
      nodes { nameWithOwner issues(states: OPEN) { totalCount } }
      pageInfo { hasNextPage endCursor }
    }
  }
}`
	trusted := append([]string{c.repo.Owner}, authors...)
	scans := []RepoScan{}
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
			private, issues, err := c.readSource(ctx, repo, trusted)
			if err != nil {
				return nil, err
			}
			scan := RepoScan{Repo: node.NameWithOwner, Private: private, Open: len(issues)}
			for _, issue := range issues {
				if issue.movable {
					scan.Movable++
				} else {
					scan.Others++
				}
			}
			scans = append(scans, scan)
		}
		if !repositories.PageInfo.HasNextPage {
			return scans, nil
		}
		cursor = repositories.PageInfo.EndCursor
	}
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
	if strings.EqualFold(opts.Repo.String(), c.repo.String()) {
		return ImportResult{}, errors.New("the tracker cannot import from itself")
	}
	meta, err := c.refreshMetadata(ctx)
	if err != nil {
		return ImportResult{}, err
	}
	if !meta.IsPrivate {
		return ImportResult{}, fmt.Errorf("tracker %s is not private: refusing to move issues into it", c.repo)
	}
	_, issues, err := c.readSource(ctx, opts.Repo, append([]string{c.repo.Owner}, opts.Authors...))
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
	project, err := c.AddProject(ctx, opts.Repo)
	if err != nil {
		return result, err
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

// importIssue transfers one issue and files it under its project. The returned
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
	label := fmt.Sprintf(`add: addLabelsToLabelable(input: {labelableId: %q, labelIds: [%q]}) { clientMutationId }`, moved.ID, project.LabelID)
	if err := c.write(ctx, []string{label}); err != nil {
		return moved.Number, fmt.Errorf("moved to ticket #%d, finish it with issue move: %w", moved.Number, err)
	}
	if err := c.writeTodo(ctx, project, moved.ID); err != nil {
		return moved.Number, fmt.Errorf("moved to ticket #%d, set its status with issue status set: %w", moved.Number, err)
	}
	return moved.Number, nil
}

// writeTodo uses the project it is given, because metadata read right after
// project add may not include the new board yet.
func (c *Client) writeTodo(ctx context.Context, project Project, contentID string) error {
	itemID, err := c.addToBoard(ctx, project, contentID)
	if err != nil {
		return err
	}
	field, err := statusMutation("status", project, itemID, StatusTodo)
	if err != nil {
		return err
	}
	return c.write(ctx, []string{field})
}
