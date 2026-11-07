package tracker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	// labelHandoff marks a ticket that hands work to the next agent. It keeps the
	// ticket out of ready work, because picking it up takes handoff take.
	labelHandoff = "handoff"
	// handoffRefPrefix holds one ref per repository while a handoff is open. Creating
	// it is atomic, which keeps a project to one open handoff. The ref names the
	// repository as owner/name, so no slot's ref is a directory of another's.
	handoffRefPrefix = "handoffs/"

	handoffBranch = "Branch: "
	handoffCommit = "Commit: "
	handoffTicket = "Ticket: #"
)

// ErrNoHandoff reports that a project has no open handoff.
var ErrNoHandoff = errors.New("no open handoff")

// Handoff is a ticket that hands work to the next agent: where the work stopped and
// what it needs.
type Handoff struct {
	Ticket  Ticket
	Branch  string
	SHA     string
	Related int
	Text    string
}

// HandoffOptions describe a handoff to create. Related is a ticket the work belongs
// to, or zero.
type HandoffOptions struct {
	Project string
	Title   string
	Text    string
	Branch  string
	SHA     string
	Related int
}

// TakenHandoff is a handoff that was picked up. Moved reports that its branch no
// longer contains its commit; Unchecked holds why that could not be checked.
type TakenHandoff struct {
	Handoff
	Moved     bool
	Unchecked error
}

// CreateHandoff opens a project's one handoff. The slot ref is created first, so a
// second handoff fails before anything else is written, and is deleted again when
// the ticket cannot be created.
func (c *Client) CreateHandoff(ctx context.Context, opts HandoffOptions) (Handoff, error) {
	if opts.Branch == "" || opts.SHA == "" {
		return Handoff{}, errors.New("a handoff needs a branch and a commit: pass --branch and --sha")
	}
	if opts.Related < 0 {
		return Handoff{}, fmt.Errorf("invalid ticket #%d", opts.Related)
	}
	project, err := c.lookupBoard(ctx, opts.Project)
	if err != nil {
		return Handoff{}, err
	}
	repo, err := ParseRepo(project.Repo)
	if err != nil {
		return Handoff{}, err
	}
	sha, contained, err := c.compareCommit(ctx, repo, opts.SHA, opts.Branch)
	if err != nil {
		return Handoff{}, err
	}
	if !contained {
		return Handoff{}, fmt.Errorf("branch %s of %s does not contain commit %s: push the commit first", opts.Branch, repo, sha)
	}
	handoff := Handoff{Branch: opts.Branch, SHA: sha, Related: opts.Related, Text: opts.Text}
	body := handoff.body()
	if err := checkBodyLength(body); err != nil {
		return Handoff{}, err
	}
	labelID, err := c.handoffLabel(ctx)
	if err != nil {
		return Handoff{}, err
	}
	if err := c.createSlot(ctx, project); err != nil {
		return Handoff{}, err
	}
	ticket, err := c.CreateTicket(ctx, CreateOptions{Project: project.Name, Title: opts.Title, Body: body, labelIDs: []string{labelID}})
	if err != nil && ticket.Number == 0 {
		if freeErr := c.deleteSlot(ctx, project); freeErr != nil {
			return Handoff{}, errors.Join(err, freeErr)
		}
		return Handoff{}, err
	}
	handoff.Ticket = ticket
	return handoff, err
}

// ShowHandoff reads a project's open handoff, or reports ErrNoHandoff.
func (c *Client) ShowHandoff(ctx context.Context, name string) (Handoff, error) {
	project, err := c.lookupProject(ctx, name)
	if err != nil {
		return Handoff{}, err
	}
	return c.findHandoff(ctx, project)
}

// TakeHandoff picks up a project's open handoff: it claims the ticket through the
// atomic claim ref, so one of two agents wins, then closes it as completed and frees
// the slot. It reports whether the branch still contains the commit. Once the
// handoff is closed, an error comes back with the handoff, so its contents are
// never lost.
func (c *Client) TakeHandoff(ctx context.Context, name string) (TakenHandoff, error) {
	project, err := c.lookupProject(ctx, name)
	if err != nil {
		return TakenHandoff{}, err
	}
	repo, err := ParseRepo(project.Repo)
	if err != nil {
		return TakenHandoff{}, err
	}
	handoff, err := c.findHandoff(ctx, project)
	if err != nil {
		return TakenHandoff{}, err
	}
	number := handoff.Ticket.Number
	if err := c.createClaim(ctx, number); err != nil {
		return TakenHandoff{}, err
	}
	// The listing can predate another agent's pickup, which deletes its claim once the
	// ticket is closed, so the claim is confirmed against a fresh read.
	ticket, err := c.readTicket(ctx, number)
	if err == nil && ticket.State != stateOpen {
		err = fmt.Errorf("%w: #%d", ErrAlreadyClaimed, number)
	}
	if err != nil {
		return TakenHandoff{}, c.releaseAfter(ctx, number, err)
	}
	// The check only feeds a warning, so it runs before closing and never blocks it.
	taken := TakenHandoff{Handoff: handoff}
	taken.Ticket = ticket
	_, contained, err := c.compareCommit(ctx, repo, handoff.SHA, handoff.Branch)
	switch {
	case errors.Is(err, errNotOnRemote):
		taken.Moved = true
	case err != nil:
		taken.Unchecked = err
	default:
		taken.Moved = !contained
	}
	closing := ticket
	closing.Status = StatusDoing // closing deletes the claim it holds
	if err := c.closeTicket(ctx, closing, CloseOptions{Reason: ReasonCompleted}); err != nil {
		return TakenHandoff{}, c.releaseAfter(ctx, number, err)
	}
	if err := c.deleteSlot(ctx, project); err != nil {
		return taken, fmt.Errorf("took handoff #%d, but %w: run issue-tracker handoff drop --free-slot", number, err)
	}
	return taken, nil
}

// releaseAfter gives up a claim that a failed pickup took, so the handoff can be
// taken again.
func (c *Client) releaseAfter(ctx context.Context, number int, err error) error {
	if releaseErr := c.deleteClaim(ctx, number); releaseErr != nil {
		return errors.Join(err, releaseErr)
	}
	return err
}

// DropHandoff closes a project's open handoff as not planned without claiming it,
// and frees the slot. With no open handoff, freeSlot frees a slot that a failed
// create left behind and reports false. A slot without a handoff can also belong to
// a create still running, so it is freed only when asked. It reports ErrNoHandoff
// when there was nothing to drop or free.
func (c *Client) DropHandoff(ctx context.Context, name, reason string, freeSlot bool) (Handoff, bool, error) {
	project, err := c.lookupProject(ctx, name)
	if err != nil {
		return Handoff{}, false, err
	}
	if err := checkBodyLength(reason); err != nil {
		return Handoff{}, false, err
	}
	handoff, err := c.findHandoff(ctx, project)
	if errors.Is(err, ErrNoHandoff) {
		if !freeSlot {
			return Handoff{}, false, err
		}
		freed, err := c.freeSlot(ctx, project)
		if err == nil && !freed {
			err = fmt.Errorf("%w in %s", ErrNoHandoff, project.Root())
		}
		return Handoff{}, false, err
	}
	if err != nil {
		return Handoff{}, false, err
	}
	if err := c.closeTicket(ctx, handoff.Ticket, CloseOptions{Reason: ReasonNotPlanned, Comment: reason}); err != nil {
		return Handoff{}, false, err
	}
	if err := c.deleteSlot(ctx, project); err != nil {
		return Handoff{}, false, fmt.Errorf("dropped handoff #%d, but %w", handoff.Ticket.Number, err)
	}
	return handoff, true, nil
}

// handoffNode is a ticket with the body that holds its handoff.
type handoffNode struct {
	ticketNode
	Body string
}

// findHandoff reads a repository's open handoff. It lists the tracker's open
// handoffs through the issues connection, which reflects writes at once, unlike
// search, so a handoff created a moment ago is found. A tracker holds at most one
// per repository, so this is one request until more than 100 repositories hold one.
// A component's handoff belongs to its repository's slot.
func (c *Client) findHandoff(ctx context.Context, project Project) (Handoff, error) {
	query := `query($owner: String!, $name: String!, $labels: [String!], $after: String) {
  repository(owner: $owner, name: $name) {
    isPrivate
    issues(labels: $labels, states: OPEN, first: 100, after: $after, orderBy: {field: CREATED_AT, direction: ASC}) {
      nodes { ` + ticketFields + ` body }
      pageInfo { hasNextPage endCursor }
    }
  }
}`
	variables := c.variables(map[string]any{"labels": []string{labelHandoff}, "after": nil})
	for {
		var response struct {
			Repository struct {
				IsPrivate bool
				Issues    struct {
					Nodes    []handoffNode
					PageInfo pageInfo
				}
			}
		}
		if err := c.do(ctx, query, variables, &response); err != nil {
			return Handoff{}, fmt.Errorf("reading handoff: %w", err)
		}
		if err := c.checkPrivate(response.Repository.IsPrivate); err != nil {
			return Handoff{}, err
		}
		meta, err := c.metadata(ctx)
		if err != nil {
			return Handoff{}, err
		}
		for _, node := range response.Repository.Issues.Nodes {
			ticket, err := c.routed(ctx, node.ticket())
			if err != nil {
				return Handoff{}, fmt.Errorf("reading handoff: %w", err)
			}
			if owner, ok := meta.project(ticket.Project); ok && strings.EqualFold(owner.Repo, project.Repo) {
				return parseHandoff(ticket, node.Body)
			}
		}
		page := response.Repository.Issues.PageInfo
		if !page.HasNextPage {
			return Handoff{}, fmt.Errorf("%w in %s", ErrNoHandoff, project.Root())
		}
		variables["after"] = page.EndCursor
	}
}

func (h Handoff) body() string {
	lines := []string{handoffBranch + h.Branch, handoffCommit + h.SHA}
	if h.Related != 0 {
		lines = append(lines, handoffTicket+strconv.Itoa(h.Related))
	}
	body := strings.Join(lines, "\n")
	if h.Text != "" {
		body += "\n\n" + h.Text
	}
	return body
}

// parseHandoff reads the lines create wrote at the top of the body. The text after
// the first blank line is the handoff's own.
func parseHandoff(ticket Ticket, body string) (Handoff, error) {
	handoff := Handoff{Ticket: ticket}
	header, text, _ := strings.Cut(strings.ReplaceAll(body, "\r\n", "\n"), "\n\n")
	handoff.Text = text
	for _, line := range strings.Split(header, "\n") {
		switch {
		case strings.HasPrefix(line, handoffBranch):
			handoff.Branch = strings.TrimPrefix(line, handoffBranch)
		case strings.HasPrefix(line, handoffCommit):
			handoff.SHA = strings.TrimPrefix(line, handoffCommit)
		case strings.HasPrefix(line, handoffTicket):
			handoff.Related, _ = strconv.Atoi(strings.TrimPrefix(line, handoffTicket))
		}
	}
	if handoff.Branch == "" || handoff.SHA == "" {
		return handoff, fmt.Errorf("handoff #%d has no %q and %q lines at the top of its body: drop it and create it again", ticket.Number, strings.TrimSpace(handoffBranch), strings.TrimSpace(handoffCommit))
	}
	return handoff, nil
}

// handoffLabel returns the handoff label's ID, creating the label on first use. A
// new ticket carries it from creation, so it is never ready work in between.
func (c *Client) handoffLabel(ctx context.Context) (string, error) {
	var response struct {
		Repository struct {
			ID    string
			Label *struct{ ID string }
		}
	}
	query := `query($owner: String!, $name: String!, $label: String!) { repository(owner: $owner, name: $name) { id label(name: $label) { id } } }`
	if err := c.do(ctx, query, c.variables(map[string]any{"label": labelHandoff}), &response); err != nil {
		return "", fmt.Errorf("reading handoff label: %w", err)
	}
	if response.Repository.Label != nil {
		return response.Repository.Label.ID, nil
	}
	var created struct {
		CreateLabel struct {
			Label struct{ ID string }
		}
	}
	mutation := `mutation($repository: ID!, $name: String!, $description: String!) {
  createLabel(input: {repositoryId: $repository, name: $name, color: "8250df", description: $description}) { label { id } }
}`
	variables := map[string]any{"repository": response.Repository.ID, "name": labelHandoff, "description": "Work handed to the next agent: issue-tracker handoff take"}
	if err := c.do(ctx, mutation, variables, &created); err != nil {
		return "", fmt.Errorf("creating handoff label: %w", err)
	}
	return created.CreateLabel.Label.ID, nil
}

func (c *Client) slotPath(project Project) string {
	return c.refsPath() + "/" + handoffRefPrefix + project.Repo
}

// createSlot takes a repository's handoff slot. It points at the tracker's default
// branch, like a claim, and refreshes that commit once if it has gone.
func (c *Client) createSlot(ctx context.Context, project Project) error {
	meta, err := c.metadata(ctx)
	if err != nil {
		return err
	}
	err = c.postSlot(ctx, project, meta.HeadOID)
	if err != nil && isUnprocessable(err) && !isClaimConflict(err) {
		if meta, err = c.refreshMetadata(ctx); err != nil {
			return err
		}
		err = c.postSlot(ctx, project, meta.HeadOID)
	}
	if err == nil {
		return nil
	}
	if !isClaimConflict(err) {
		return fmt.Errorf("opening the handoff slot of %s: %w", project.Root(), describeError(err))
	}
	open, findErr := c.findHandoff(ctx, project)
	if errors.Is(findErr, ErrNoHandoff) {
		return fmt.Errorf("the handoff slot of %s is taken, but it has no open handoff: another handoff create may still be running, so retry in a moment; if none is, a create failed partway, and issue-tracker handoff drop --free-slot frees the slot", project.Root())
	}
	if open.Ticket.Number == 0 {
		return fmt.Errorf("%s already has an open handoff: %w", project.Root(), findErr)
	}
	return fmt.Errorf("%s already has open handoff #%d: take or drop it first", project.Root(), open.Ticket.Number)
}

func (c *Client) postSlot(ctx context.Context, project Project, oid string) error {
	body := strings.NewReader(fmt.Sprintf(`{"ref": "refs/%s%s", "sha": %q}`, handoffRefPrefix, project.Repo, oid))
	return c.rest.DoWithContext(ctx, http.MethodPost, c.refsPath(), body, nil)
}

// freeSlot deletes a repository's handoff slot and reports whether there was one.
func (c *Client) freeSlot(ctx context.Context, project Project) (bool, error) {
	err := c.rest.DoWithContext(ctx, http.MethodDelete, c.slotPath(project), nil, nil)
	if err == nil {
		return true, nil
	}
	if isUnprocessable(err) {
		return false, nil
	}
	return false, fmt.Errorf("freeing the handoff slot of %s: %w", project.Root(), describeError(err))
}

func (c *Client) deleteSlot(ctx context.Context, project Project) error {
	_, err := c.freeSlot(ctx, project)
	return err
}

// errNotOnRemote reports a branch or commit GitHub cannot find in a repository.
var errNotOnRemote = errors.New("not found")

// compareCommit resolves a commit to its full SHA and reports whether a branch
// contains it, in one request: the branch contains the commit when it is identical
// to it or ahead of it.
func (c *Client) compareCommit(ctx context.Context, repo Repo, sha, branch string) (string, bool, error) {
	var response struct {
		Status     string
		BaseCommit struct{ SHA string } `json:"base_commit"`
	}
	path := fmt.Sprintf("repos/%s/compare/%s...%s", repo, url.PathEscape(sha), strings.ReplaceAll(url.PathEscape(branch), "%2F", "/"))
	err := c.rest.DoWithContext(ctx, http.MethodGet, path, nil, &response)
	if isNotFound(err) {
		return "", false, fmt.Errorf("branch %s or commit %s %w in %s: push them first", branch, sha, errNotOnRemote, repo)
	}
	if err != nil {
		return "", false, fmt.Errorf("comparing commit %s with branch %s of %s: %w", sha, branch, repo, describeError(err))
	}
	return response.BaseCommit.SHA, response.Status == "identical" || response.Status == "ahead", nil
}
