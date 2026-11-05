package tracker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// maxBoardDescription is GitHub's limit on a board's short description, in
	// characters. A repository's description can be longer.
	maxBoardDescription = 300
	// descriptionBatch bounds the boards one request reads or writes.
	descriptionBatch = 50
)

// boardDescription is a board's short description: its repository's description,
// then a blank line and the repository's URL. A description too long to fit beside
// the URL is cut and ends with an ellipsis, so the link always survives.
func boardDescription(description, url string) string {
	description = strings.TrimSpace(description)
	if description == "" {
		return url
	}
	suffix := "\n\n" + url
	room := maxBoardDescription - utf8.RuneCountInString(suffix)
	if runes := []rune(description); len(runes) > room {
		description = strings.TrimSpace(string(runes[:room-1])) + "…"
	}
	return description + suffix
}

// repoDetails is what a board's description is made from.
type repoDetails struct {
	Description string
	URL         string
}

// boardUpdate is a board description to write, and the project it belongs to.
type boardUpdate struct {
	Project     string
	BoardID     string
	Description string
}

// DescriptionResult reports a description sync. Problems name the boards that were
// left unchanged and why; the other boards are synced regardless.
type DescriptionResult struct {
	Boards   int
	Updated  []string
	Problems []string
}

// SyncDescriptions sets each project's board description from its repository. A
// component shares its repository's board, so it syncs that board. It reads every
// board and repository in one request per 50 boards and writes only the boards
// whose description differs, in one request per 50.
func (c *Client) SyncDescriptions(ctx context.Context, projects []Project) (DescriptionResult, error) {
	var result DescriptionResult
	var boards []Project
	seen := map[string]bool{}
	for _, project := range projects {
		if project.BoardID == "" {
			result.Problems = append(result.Problems, errNoBoard(project).Error())
			continue
		}
		if seen[project.BoardID] {
			continue
		}
		seen[project.BoardID] = true
		project.Name = project.Root()
		boards = append(boards, project)
	}
	result.Boards = len(boards)
	var updates []boardUpdate
	for start := 0; start < len(boards); start += descriptionBatch {
		chunk := boards[start:min(start+descriptionBatch, len(boards))]
		pending, problems, err := c.compareDescriptions(ctx, chunk)
		if err != nil {
			return DescriptionResult{}, err
		}
		result.Problems = append(result.Problems, problems...)
		for _, update := range pending {
			result.Updated = append(result.Updated, update.Project)
		}
		updates = append(updates, pending...)
	}
	if err := c.writeDescriptions(ctx, updates); err != nil {
		return DescriptionResult{}, err
	}
	return result, nil
}

// compareDescriptions reads the boards and their repositories in one request, and
// returns the updates for boards whose description differs.
func (c *Client) compareDescriptions(ctx context.Context, boards []Project) ([]boardUpdate, []string, error) {
	var query strings.Builder
	query.WriteString("query {")
	for i, project := range boards {
		repo, err := ParseRepo(project.Repo)
		if err != nil {
			return nil, nil, err
		}
		fmt.Fprintf(&query, " b%d: node(id: %q) { ... on ProjectV2 { shortDescription } }", i, project.BoardID)
		fmt.Fprintf(&query, " r%d: repository(owner: %q, name: %q) { description url }", i, repo.Owner, repo.Name)
	}
	query.WriteString(" }")
	response := map[string]*struct{ ShortDescription, Description, URL string }{}
	// A renamed or deleted repository or board is reported, not fatal, so that the
	// other boards still sync.
	if err := c.do(ctx, query.String(), nil, &response); err != nil && !hasGraphQLType(err, graphQLNotFoundType) {
		return nil, nil, fmt.Errorf("reading board descriptions: %w", err)
	}
	var pending []boardUpdate
	var problems []string
	for i, project := range boards {
		board, repo := response["b"+strconv.Itoa(i)], response["r"+strconv.Itoa(i)]
		switch {
		case repo == nil:
			problems = append(problems, fmt.Sprintf("repository %s of project %s not found", project.Repo, project.Name))
		case board == nil:
			problems = append(problems, errNoBoard(project).Error())
		default:
			want := boardDescription(repo.Description, repo.URL)
			if board.ShortDescription != want {
				pending = append(pending, boardUpdate{Project: project.Name, BoardID: project.BoardID, Description: want})
			}
		}
	}
	return pending, problems, nil
}

// writeDescriptions sets board descriptions, 50 boards to a request. Descriptions
// travel as variables, because they are free text.
func (c *Client) writeDescriptions(ctx context.Context, updates []boardUpdate) error {
	for start := 0; start < len(updates); start += descriptionBatch {
		chunk := updates[start:min(start+descriptionBatch, len(updates))]
		params := make([]string, 0, len(chunk))
		fields := make([]string, 0, len(chunk))
		variables := make(map[string]any, len(chunk))
		for i, update := range chunk {
			name := "d" + strconv.Itoa(i)
			params = append(params, "$"+name+": String!")
			fields = append(fields, fmt.Sprintf("%s: updateProjectV2(input: {projectId: %q, shortDescription: $%s}) { clientMutationId }", name, update.BoardID, name))
			variables[name] = update.Description
		}
		mutation := "mutation(" + strings.Join(params, ", ") + ") { " + strings.Join(fields, " ") + " }"
		if err := c.do(ctx, mutation, variables, nil); err != nil {
			return fmt.Errorf("writing board descriptions: %w", err)
		}
	}
	return nil
}
