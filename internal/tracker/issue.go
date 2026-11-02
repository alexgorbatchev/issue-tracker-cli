package tracker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	listPageSize  = 100
	stateOpen     = "OPEN"
	stateClosed   = "CLOSED"
	nestedLimit   = 20
	commentsLimit = 100
)

// ticketFields is the shared selection for one ticket. Reading blockers, parents,
// and board status here keeps list and claim commands to a single request.
var ticketFields = fmt.Sprintf(`id number title state url updatedAt
  labels(first: %[1]d) { nodes { name } }
  blockedBy(first: %[1]d) { nodes { number state } }
  parent {
    number state blockedBy(first: %[1]d) { nodes { state } }
    parent { number state blockedBy(first: %[1]d) { nodes { state } } }
  }
  projectItems(first: 5) {
    nodes {
      id updatedAt project { id }
      fieldValueByName(name: "Status") { ... on ProjectV2ItemFieldSingleSelectValue { name } }
    }
  }`, nestedLimit)

type issueRef struct {
	Number int
	Title  string
	State  string
}

type ancestorNode struct {
	Number    int
	State     string
	BlockedBy struct{ Nodes []issueRef }
}

func (a *ancestorNode) blocked() bool {
	if a == nil || a.State != stateOpen {
		return false
	}
	return len(openNumbers(a.BlockedBy.Nodes)) > 0
}

type ticketNode struct {
	ID        string
	Number    int
	Title     string
	State     string
	URL       string
	UpdatedAt time.Time
	Labels    struct{ Nodes []struct{ Name string } }
	BlockedBy struct{ Nodes []issueRef }
	Parent    *struct {
		ancestorNode
		Parent *ancestorNode
	}
	ProjectItems struct {
		Nodes []struct {
			ID               string
			UpdatedAt        time.Time
			Project          struct{ ID string }
			FieldValueByName *struct{ Name string }
		}
	}
}

// Ticket is one issue in the tracker.
type Ticket struct {
	ID            string
	Number        int
	Title         string
	State         string
	URL           string
	Project       string
	Labels        []string
	Status        string
	Parent        int
	Blockers      []int
	ParentBlocked bool
	Activity      time.Time

	itemID  string
	boardID string
}

// Ready reports whether a ticket can be picked up: open, unblocked, with no blocked
// ancestor, and not in progress. Parents stay ready while their children are open.
func (t Ticket) Ready() bool {
	return t.State == stateOpen && len(t.Blockers) == 0 && !t.ParentBlocked &&
		t.Status != StatusDoing && t.Status != StatusDone
}

// Stale reports a Doing ticket with no activity for the given number of days.
func (t Ticket) Stale(days int, now time.Time) bool {
	return t.State == stateOpen && t.Status == StatusDoing &&
		t.Activity.Before(now.AddDate(0, 0, -days))
}

func (n ticketNode) ticket() Ticket {
	t := Ticket{
		ID: n.ID, Number: n.Number, Title: n.Title, State: n.State, URL: n.URL,
		Blockers: openNumbers(n.BlockedBy.Nodes), Activity: n.UpdatedAt,
	}
	for _, label := range n.Labels.Nodes {
		if name, ok := strings.CutPrefix(label.Name, labelPrefix); ok {
			t.Project = name
			continue
		}
		t.Labels = append(t.Labels, label.Name)
	}
	if n.Parent != nil {
		t.Parent = n.Parent.Number
		t.ParentBlocked = n.Parent.blocked() || n.Parent.Parent.blocked()
	}
	if items := n.ProjectItems.Nodes; len(items) > 0 {
		item := items[0]
		t.itemID, t.boardID = item.ID, item.Project.ID
		if item.FieldValueByName != nil {
			t.Status = item.FieldValueByName.Name
		}
		if item.UpdatedAt.After(t.Activity) {
			t.Activity = item.UpdatedAt
		}
	}
	return t
}

func openNumbers(refs []issueRef) []int {
	numbers := []int{}
	for _, ref := range refs {
		if ref.State == stateOpen {
			numbers = append(numbers, ref.Number)
		}
	}
	return numbers
}

func (c *Client) variables(extra map[string]any) map[string]any {
	variables := map[string]any{"owner": c.repo.Owner, "name": c.repo.Name}
	for key, value := range extra {
		variables[key] = value
	}
	return variables
}

func (c *Client) readTicket(ctx context.Context, number int) (Ticket, error) {
	var response struct {
		Repository struct {
			IsPrivate bool
			Issue     *ticketNode
		}
	}
	query := `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) { isPrivate issue(number: $number) { ` + ticketFields + ` } }
}`
	err := c.do(ctx, query, c.variables(map[string]any{"number": number}), &response)
	if hasGraphQLType(err, graphQLNotFoundType) || (err == nil && response.Repository.Issue == nil) {
		return Ticket{}, fmt.Errorf("ticket #%d not found in %s", number, c.repo)
	}
	if err != nil {
		return Ticket{}, fmt.Errorf("reading ticket #%d: %w", number, err)
	}
	if err := c.checkPrivate(response.Repository.IsPrivate); err != nil {
		return Ticket{}, err
	}
	return response.Repository.Issue.ticket(), nil
}

// ListOptions filters tickets. Project is empty for all projects.
type ListOptions struct {
	Project   string
	State     string
	Status    string
	Label     string
	Search    string
	Ready     bool
	Stale     bool
	StaleDays int
	Limit     int
	Now       time.Time
}

func (o ListOptions) matches(t Ticket) bool {
	switch {
	case o.Status != "" && !strings.EqualFold(t.Status, o.Status):
		return false
	case o.Label != "" && !slices.Contains(t.Labels, o.Label):
		return false
	case o.Ready && !t.Ready():
		return false
	case o.Stale && !t.Stale(o.StaleDays, o.Now):
		return false
	}
	return true
}

func listStates(state string) ([]string, error) {
	switch state {
	case "", "open":
		return []string{stateOpen}, nil
	case "closed":
		return []string{stateClosed}, nil
	case "all":
		return []string{stateOpen, stateClosed}, nil
	}
	return nil, fmt.Errorf("invalid state %q: expected open, closed, or all", state)
}

// ticketPage is one page of tickets from either listing query.
type ticketPage struct {
	IsPrivate bool
	Nodes     []ticketNode
	PageInfo  pageInfo
}

// pageReader fetches the page after a cursor, with nil for the first page.
type pageReader func(context.Context, any) (ticketPage, error)

// ListTickets returns matching tickets, oldest first, in as few pages as Limit allows.
func (c *Client) ListTickets(ctx context.Context, opts ListOptions) ([]Ticket, error) {
	states, err := listStates(opts.State)
	if err != nil {
		return nil, err
	}
	label := ""
	if opts.Project != "" {
		project, err := c.lookupProject(ctx, opts.Project)
		if err != nil {
			return nil, err
		}
		label = project.Label()
	}
	read := c.issuesPage(states, label)
	if opts.Search != "" {
		read = c.searchPage(states, label, opts.Search)
	}
	tickets := []Ticket{}
	var cursor any
	for {
		page, err := read(ctx, cursor)
		if err != nil {
			return nil, fmt.Errorf("listing tickets: %w", err)
		}
		if err := c.checkPrivate(page.IsPrivate); err != nil {
			return nil, err
		}
		for _, node := range page.Nodes {
			if ticket := node.ticket(); opts.matches(ticket) {
				tickets = append(tickets, ticket)
			}
			if opts.Limit > 0 && len(tickets) == opts.Limit {
				return tickets, nil
			}
		}
		if !page.PageInfo.HasNextPage {
			return tickets, nil
		}
		cursor = page.PageInfo.EndCursor
	}
}

func (c *Client) issuesPage(states []string, label string) pageReader {
	var labels []string
	if label != "" {
		labels = []string{label}
	}
	query := `query($owner: String!, $name: String!, $states: [IssueState!], $labels: [String!], $after: String) {
  repository(owner: $owner, name: $name) {
    isPrivate
    issues(first: ` + strconv.Itoa(listPageSize) + `, after: $after, states: $states, labels: $labels, orderBy: {field: CREATED_AT, direction: ASC}) {
      nodes { ` + ticketFields + ` }
      pageInfo { hasNextPage endCursor }
    }
  }
}`
	return func(ctx context.Context, cursor any) (ticketPage, error) {
		var response struct {
			Repository struct {
				IsPrivate bool
				Issues    struct {
					Nodes    []ticketNode
					PageInfo pageInfo
				}
			}
		}
		variables := c.variables(map[string]any{"states": states, "labels": labels, "after": cursor})
		if err := c.do(ctx, query, variables, &response); err != nil {
			return ticketPage{}, err
		}
		issues := response.Repository.Issues
		return ticketPage{IsPrivate: response.Repository.IsPrivate, Nodes: issues.Nodes, PageInfo: issues.PageInfo}, nil
	}
}

// searchPage matches ticket text with GitHub search, which the issues connection lacks.
func (c *Client) searchPage(states []string, label, text string) pageReader {
	terms := []string{"repo:" + c.repo.String(), "is:issue", "sort:created-asc"}
	if len(states) == 1 {
		terms = append(terms, "is:"+strings.ToLower(states[0]))
	}
	if label != "" {
		terms = append(terms, fmt.Sprintf(`label:"%s"`, label))
	}
	search := strings.Join(append(terms, text), " ")
	query := `query($owner: String!, $name: String!, $search: String!, $after: String) {
  repository(owner: $owner, name: $name) { isPrivate }
  search(query: $search, type: ISSUE, first: ` + strconv.Itoa(listPageSize) + `, after: $after) {
    nodes { ... on Issue { ` + ticketFields + ` } }
    pageInfo { hasNextPage endCursor }
  }
}`
	return func(ctx context.Context, cursor any) (ticketPage, error) {
		var response struct {
			Repository struct{ IsPrivate bool }
			Search     struct {
				Nodes    []ticketNode
				PageInfo pageInfo
			}
		}
		variables := c.variables(map[string]any{"search": search, "after": cursor})
		if err := c.do(ctx, query, variables, &response); err != nil {
			return ticketPage{}, err
		}
		return ticketPage{IsPrivate: response.Repository.IsPrivate, Nodes: response.Search.Nodes, PageInfo: response.Search.PageInfo}, nil
	}
}

// Comment is one comment on a ticket.
type Comment struct {
	Author    string
	CreatedAt time.Time
	Body      string
}

// TicketDetail is a ticket with its text, relations, and attachment URLs.
type TicketDetail struct {
	Ticket
	Body        string
	Comments    []Comment
	Blocking    []int
	SubIssues   []SubIssue
	Attachments []string
	PublicRepo  bool
}

// SubIssue is a child ticket.
type SubIssue struct {
	Number int
	Title  string
	State  string
}

type commentPage struct {
	Nodes []struct {
		Author    *struct{ Login string }
		CreatedAt time.Time
		Body      string
	}
	PageInfo pageInfo
}

func (p commentPage) comments() []Comment {
	comments := make([]Comment, 0, len(p.Nodes))
	for _, node := range p.Nodes {
		comment := Comment{CreatedAt: node.CreatedAt, Body: node.Body}
		if node.Author != nil {
			comment.Author = node.Author.Login
		}
		comments = append(comments, comment)
	}
	return comments
}

var attachmentURL = regexp.MustCompile(`https://github\.com/user-attachments/(?:assets|files)/[A-Za-z0-9._~%/-]+`)

// InspectTicket reads a ticket with everything an agent needs to work on it.
func (c *Client) InspectTicket(ctx context.Context, number int) (TicketDetail, error) {
	var response struct {
		Repository struct {
			IsPrivate bool
			Issue     *struct {
				ticketNode
				Body      string
				Blocking  struct{ Nodes []issueRef }
				SubIssues struct {
					Nodes []SubIssue
				}
				Comments commentPage
			}
		}
	}
	query := fmt.Sprintf(`query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    isPrivate
    issue(number: $number) {
      %s
      body
      blocking(first: %[2]d) { nodes { number state } }
      subIssues(first: %[3]d) { nodes { number title state } }
      comments(first: %[3]d) { nodes { author { login } createdAt body } pageInfo { hasNextPage endCursor } }
    }
  }
}`, ticketFields, nestedLimit, commentsLimit)
	err := c.do(ctx, query, c.variables(map[string]any{"number": number}), &response)
	if hasGraphQLType(err, graphQLNotFoundType) || (err == nil && response.Repository.Issue == nil) {
		return TicketDetail{}, fmt.Errorf("ticket #%d not found in %s", number, c.repo)
	}
	if err != nil {
		return TicketDetail{}, fmt.Errorf("reading ticket #%d: %w", number, err)
	}
	if err := c.checkPrivate(response.Repository.IsPrivate); err != nil {
		return TicketDetail{}, err
	}
	issue := response.Repository.Issue
	detail := TicketDetail{
		Ticket: issue.ticket(), Body: issue.Body, Blocking: openNumbers(issue.Blocking.Nodes),
		SubIssues: issue.SubIssues.Nodes, Comments: issue.Comments.comments(),
	}
	if issue.Comments.PageInfo.HasNextPage {
		more, err := c.remainingComments(ctx, number, issue.Comments.PageInfo.EndCursor)
		if err != nil {
			return TicketDetail{}, err
		}
		detail.Comments = append(detail.Comments, more...)
	}
	detail.Attachments = findAttachments(detail.Body, detail.Comments)
	if detail.Project != "" {
		if meta, err := c.metadata(ctx); err == nil {
			project, ok := meta.project(detail.Project)
			detail.PublicRepo = !ok || project.Public
		}
	}
	return detail, nil
}

func (c *Client) remainingComments(ctx context.Context, number int, cursor string) ([]Comment, error) {
	query := fmt.Sprintf(`query($owner: String!, $name: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    issue(number: $number) {
      comments(first: %d, after: $after) { nodes { author { login } createdAt body } pageInfo { hasNextPage endCursor } }
    }
  }
}`, commentsLimit)
	comments := []Comment{}
	for {
		var response struct {
			Repository struct {
				Issue struct{ Comments commentPage }
			}
		}
		variables := c.variables(map[string]any{"number": number, "after": cursor})
		if err := c.do(ctx, query, variables, &response); err != nil {
			return nil, fmt.Errorf("reading comments: %w", err)
		}
		page := response.Repository.Issue.Comments
		comments = append(comments, page.comments()...)
		if !page.PageInfo.HasNextPage {
			return comments, nil
		}
		cursor = page.PageInfo.EndCursor
	}
}

func findAttachments(body string, comments []Comment) []string {
	seen := map[string]bool{}
	urls := []string{}
	add := func(text string) {
		for _, link := range attachmentURL.FindAllString(text, -1) {
			if !seen[link] {
				seen[link] = true
				urls = append(urls, link)
			}
		}
	}
	add(body)
	for _, comment := range comments {
		add(comment.Body)
	}
	return urls
}

// issueIDs resolves ticket numbers to node IDs in one request.
func (c *Client) issueIDs(ctx context.Context, numbers []int) (map[int]string, error) {
	if len(numbers) == 0 {
		return map[int]string{}, nil
	}
	var selection strings.Builder
	for _, number := range numbers {
		fmt.Fprintf(&selection, " n%[1]d: issue(number: %[1]d) { id }", number)
	}
	query := `query($owner: String!, $name: String!) { repository(owner: $owner, name: $name) { isPrivate` + selection.String() + ` } }`
	var response struct {
		Repository map[string]any
	}
	if err := c.do(ctx, query, c.variables(nil), &response); err != nil {
		if hasGraphQLType(err, graphQLNotFoundType) {
			return nil, fmt.Errorf("one of tickets %v does not exist in %s", numbers, c.repo)
		}
		return nil, fmt.Errorf("reading tickets: %w", err)
	}
	isPrivate, _ := response.Repository["isPrivate"].(bool)
	if err := c.checkPrivate(isPrivate); err != nil {
		return nil, err
	}
	ids := make(map[int]string, len(numbers))
	for _, number := range numbers {
		node, _ := response.Repository["n"+strconv.Itoa(number)].(map[string]any)
		id, _ := node["id"].(string)
		if id == "" {
			return nil, fmt.Errorf("ticket #%d not found in %s", number, c.repo)
		}
		ids[number] = id
	}
	return ids, nil
}

func statusMutation(alias string, project Project, itemID, status string) (string, error) {
	optionID, ok := project.StatusOptions[status]
	if !ok {
		return "", fmt.Errorf("board for %s has no %s column: run project add %s", project.Name, status, project.Repo)
	}
	return fmt.Sprintf(`%s: updateProjectV2ItemFieldValue(input: {projectId: %q, itemId: %q, fieldId: %q, value: {singleSelectOptionId: %q}}) { clientMutationId }`,
		alias, project.BoardID, itemID, project.StatusFieldID, optionID), nil
}

// CreateOptions describes a new ticket.
type CreateOptions struct {
	Project        string
	Title          string
	Body           string
	DiscoveredFrom int
	Parent         int
	BlockedBy      []int
	Blocking       []int
	Labels         []string
	Attachments    []string
}

// CreateTicket creates a ticket on its project board with status Todo. A failure
// after creation names the ticket so the caller can finish it instead of duplicating it.
func (c *Client) CreateTicket(ctx context.Context, opts CreateOptions) (Ticket, error) {
	for _, path := range opts.Attachments {
		if _, err := os.Stat(path); err != nil {
			return Ticket{}, fmt.Errorf("attachment: %w", err)
		}
	}
	if err := checkPlainLabels(opts.Labels); err != nil {
		return Ticket{}, err
	}
	project, err := c.lookupProject(ctx, opts.Project)
	if err != nil {
		return Ticket{}, err
	}
	related := append(append([]int{}, opts.BlockedBy...), opts.Blocking...)
	if opts.Parent != 0 {
		related = append(related, opts.Parent)
	}
	ids, err := c.issueIDs(ctx, related)
	if err != nil {
		return Ticket{}, err
	}
	var created createdTicket
	err = c.withFreshIDs(ctx, func(meta *metadata) error {
		fresh, ok := meta.project(opts.Project)
		if !ok {
			return fmt.Errorf("unknown project %q: register it with project add", opts.Project)
		}
		if fresh.BoardID == "" {
			return errNoBoard(fresh)
		}
		project = fresh
		created, err = c.insertTicket(ctx, meta, fresh, opts, ids)
		return err
	})
	if err != nil {
		return Ticket{}, err
	}
	ticket := Ticket{ID: created.ID, Number: created.Number, Title: opts.Title, URL: created.URL, State: stateOpen, Project: project.Name, Status: StatusTodo}
	if err := c.linkCreated(ctx, project, created, opts, ids); err != nil {
		return ticket, fmt.Errorf("ticket #%d was created but linking it failed: %w", created.Number, err)
	}
	if err := c.AddAttachments(ctx, created.Number, opts.Attachments); err != nil {
		return ticket, fmt.Errorf("ticket #%d was created but attaching failed: %w", created.Number, err)
	}
	return ticket, nil
}

type createdTicket struct {
	ID, URL      string
	Number       int
	ProjectItems struct{ Nodes []struct{ ID string } }
}

func (c *Client) insertTicket(ctx context.Context, meta *metadata, project Project, opts CreateOptions, ids map[int]string) (createdTicket, error) {
	input := map[string]any{
		"repositoryId": meta.RepositoryID, "title": opts.Title, "body": createBody(opts),
		"labelIds": []string{project.LabelID}, "projectV2Ids": []string{project.BoardID},
	}
	if opts.Parent != 0 {
		input["parentIssueId"] = ids[opts.Parent]
	}
	var response struct {
		CreateIssue struct{ Issue createdTicket }
	}
	mutation := `mutation($input: CreateIssueInput!) {
  createIssue(input: $input) { issue { id number url projectItems(first: 1) { nodes { id } } } }
}`
	if err := c.do(ctx, mutation, map[string]any{"input": input}, &response); err != nil {
		return createdTicket{}, fmt.Errorf("creating ticket: %w", err)
	}
	return response.CreateIssue.Issue, nil
}

func createBody(opts CreateOptions) string {
	if opts.DiscoveredFrom == 0 {
		return opts.Body
	}
	line := fmt.Sprintf("Discovered while working on #%d", opts.DiscoveredFrom)
	if opts.Body == "" {
		return line
	}
	return opts.Body + "\n\n" + line
}

func (c *Client) linkCreated(ctx context.Context, project Project, created createdTicket, opts CreateOptions, ids map[int]string) error {
	// createIssue can return before GitHub has created the board item. Adding it
	// again is safe and returns the same item.
	var itemID string
	if items := created.ProjectItems.Nodes; len(items) > 0 {
		itemID = items[0].ID
	} else {
		var err error
		if itemID, err = c.addToBoard(ctx, project, created.ID); err != nil {
			return err
		}
	}
	field, err := statusMutation("status", project, itemID, StatusTodo)
	if err != nil {
		return err
	}
	fields := []string{field}
	for i, number := range opts.BlockedBy {
		fields = append(fields, blockMutation("blockedBy"+strconv.Itoa(i), created.ID, ids[number]))
	}
	for i, number := range opts.Blocking {
		fields = append(fields, blockMutation("blocking"+strconv.Itoa(i), ids[number], created.ID))
	}
	if err := c.write(ctx, fields); err != nil {
		return err
	}
	if len(opts.Labels) == 0 {
		return nil
	}
	return c.AddLabels(ctx, created.Number, opts.Labels)
}

func blockMutation(alias, issueID, blockerID string) string {
	return fmt.Sprintf(`%s: addBlockedBy(input: {issueId: %q, blockingIssueId: %q}) { clientMutationId }`, alias, issueID, blockerID)
}

// write sends several mutations as one request, which GitHub runs in order.
func (c *Client) write(ctx context.Context, fields []string) error {
	if len(fields) == 0 {
		return nil
	}
	return c.do(ctx, "mutation { "+strings.Join(fields, " ")+" }", nil, nil)
}

// EditOptions holds ticket fields to change. Nil fields stay unchanged.
type EditOptions struct {
	Title *string
	Body  *string
}

// EditTicket changes a ticket's title or body.
func (c *Client) EditTicket(ctx context.Context, number int, opts EditOptions) error {
	if opts.Title == nil && opts.Body == nil {
		return errors.New("nothing to change: pass a title or body")
	}
	ids, err := c.issueIDs(ctx, []int{number})
	if err != nil {
		return err
	}
	input := map[string]any{"id": ids[number]}
	if opts.Title != nil {
		input["title"] = *opts.Title
	}
	if opts.Body != nil {
		input["body"] = *opts.Body
	}
	mutation := `mutation($input: UpdateIssueInput!) { updateIssue(input: $input) { clientMutationId } }`
	return c.do(ctx, mutation, map[string]any{"input": input}, nil)
}

// AddComment adds a comment to a ticket.
func (c *Client) AddComment(ctx context.Context, number int, body string) error {
	ids, err := c.issueIDs(ctx, []int{number})
	if err != nil {
		return err
	}
	mutation := `mutation($id: ID!, $body: String!) { addComment(input: {subjectId: $id, body: $body}) { clientMutationId } }`
	return c.do(ctx, mutation, map[string]any{"id": ids[number], "body": body}, nil)
}

// SetRelations adds or removes blockers of a ticket.
func (c *Client) SetBlockers(ctx context.Context, number int, blockers []int, add bool) error {
	return c.relate(ctx, number, blockers, func(alias, issueID, otherID string) string {
		if add {
			return blockMutation(alias, issueID, otherID)
		}
		return fmt.Sprintf(`%s: removeBlockedBy(input: {issueId: %q, blockingIssueId: %q}) { clientMutationId }`, alias, issueID, otherID)
	})
}

// SetSubIssues attaches or detaches sub-issues of a parent ticket.
func (c *Client) SetSubIssues(ctx context.Context, parent int, children []int, add bool) error {
	name := "removeSubIssue"
	if add {
		name = "addSubIssue"
	}
	return c.relate(ctx, parent, children, func(alias, issueID, otherID string) string {
		return fmt.Sprintf(`%s: %s(input: {issueId: %q, subIssueId: %q}) { clientMutationId }`, alias, name, issueID, otherID)
	})
}

func (c *Client) relate(ctx context.Context, number int, others []int, mutation func(alias, issueID, otherID string) string) error {
	ids, err := c.issueIDs(ctx, append([]int{number}, others...))
	if err != nil {
		return err
	}
	fields := make([]string, 0, len(others))
	for i, other := range others {
		fields = append(fields, mutation("m"+strconv.Itoa(i), ids[number], ids[other]))
	}
	return c.write(ctx, fields)
}

// SetStatus moves an unclaimed ticket to Todo or Done. Doing belongs to claims.
func (c *Client) SetStatus(ctx context.Context, number int, status string) error {
	if status == StatusDoing {
		return errors.New("use issue claim to move a ticket to Doing")
	}
	if status != StatusTodo && status != StatusDone {
		return fmt.Errorf("invalid status %q: expected Todo or Done", status)
	}
	ticket, err := c.readTicket(ctx, number)
	if err != nil {
		return err
	}
	if ticket.Status == StatusDoing {
		return fmt.Errorf("ticket #%d is claimed: use issue release or issue close", number)
	}
	return c.writeStatus(ctx, ticket, status)
}

func (c *Client) writeStatus(ctx context.Context, ticket Ticket, status string) error {
	if ticket.Project == "" {
		return fmt.Errorf("ticket #%d has no project label", ticket.Number)
	}
	return c.withFreshIDs(ctx, func(meta *metadata) error {
		project, ok := meta.project(ticket.Project)
		if !ok || project.BoardID == "" {
			return fmt.Errorf("project %q has no board: run project add", ticket.Project)
		}
		itemID := ticket.itemID
		if itemID == "" {
			var err error
			if itemID, err = c.addToBoard(ctx, project, ticket.ID); err != nil {
				return err
			}
		}
		field, err := statusMutation("status", project, itemID, status)
		if err != nil {
			return err
		}
		return c.write(ctx, []string{field})
	})
}

func (c *Client) addToBoard(ctx context.Context, project Project, contentID string) (string, error) {
	var response struct {
		AddProjectV2ItemById struct {
			Item struct{ ID string }
		}
	}
	mutation := `mutation($board: ID!, $content: ID!) {
  addProjectV2ItemById(input: {projectId: $board, contentId: $content}) { item { id } }
}`
	if err := c.do(ctx, mutation, map[string]any{"board": project.BoardID, "content": contentID}, &response); err != nil {
		return "", fmt.Errorf("adding ticket to board: %w", err)
	}
	return response.AddProjectV2ItemById.Item.ID, nil
}

// AddLabels adds labels to a ticket. GitHub creates a name it does not know yet.
func (c *Client) AddLabels(ctx context.Context, number int, labels []string) error {
	if err := checkPlainLabels(labels); err != nil {
		return err
	}
	body, err := json.Marshal(map[string][]string{"labels": labels})
	if err != nil {
		return fmt.Errorf("encoding labels: %w", err)
	}
	path := fmt.Sprintf("repos/%s/issues/%d/labels", c.repo, number)
	if err := c.rest.DoWithContext(ctx, http.MethodPost, path, bytes.NewReader(body), nil); err != nil {
		return fmt.Errorf("labeling ticket #%d: %w", number, describeError(err))
	}
	return nil
}

// RemoveLabels removes labels from a ticket, one request per label.
func (c *Client) RemoveLabels(ctx context.Context, number int, labels []string) error {
	if err := checkPlainLabels(labels); err != nil {
		return err
	}
	for _, label := range labels {
		path := fmt.Sprintf("repos/%s/issues/%d/labels/%s", c.repo, number, url.PathEscape(label))
		if err := c.rest.DoWithContext(ctx, http.MethodDelete, path, nil, nil); err != nil {
			return fmt.Errorf("removing label %q from ticket #%d: %w", label, number, describeError(err))
		}
	}
	return nil
}

// checkPlainLabels keeps routing labels under project add and issue move.
func checkPlainLabels(labels []string) error {
	for _, label := range labels {
		if strings.HasPrefix(label, labelPrefix) {
			return fmt.Errorf("label %q routes a ticket: use issue move instead", label)
		}
	}
	return nil
}

// MoveTicket reassigns a ticket to another project and keeps its status.
func (c *Client) MoveTicket(ctx context.Context, number int, target string) error {
	ticket, err := c.readTicket(ctx, number)
	if err != nil {
		return err
	}
	if ticket.Project == target {
		if ticket.Status != "" {
			return nil
		}
		// A failed import can leave the label without a board status.
		return c.writeStatus(ctx, ticket, StatusTodo)
	}
	project, err := c.lookupProject(ctx, target)
	if err != nil {
		return err
	}
	if project.BoardID == "" {
		return errNoBoard(project)
	}
	fields := []string{fmt.Sprintf(`add: addLabelsToLabelable(input: {labelableId: %q, labelIds: [%q]}) { clientMutationId }`, ticket.ID, project.LabelID)}
	if ticket.Project != "" {
		previous, err := c.lookupProject(ctx, ticket.Project)
		if err != nil {
			return err
		}
		fields = append(fields, fmt.Sprintf(`remove: removeLabelsFromLabelable(input: {labelableId: %q, labelIds: [%q]}) { clientMutationId }`, ticket.ID, previous.LabelID))
	}
	if ticket.itemID != "" {
		fields = append(fields, fmt.Sprintf(`drop: deleteProjectV2Item(input: {projectId: %q, itemId: %q}) { clientMutationId }`, ticket.boardID, ticket.itemID))
	}
	if err := c.write(ctx, fields); err != nil {
		return fmt.Errorf("moving ticket #%d: %w", number, err)
	}
	status := ticket.Status
	if status == "" {
		status = StatusTodo
	}
	ticket.Project, ticket.itemID = target, ""
	return c.writeStatus(ctx, ticket, status)
}

// ReopenTicket reopens a closed ticket with status Todo.
func (c *Client) ReopenTicket(ctx context.Context, number int) error {
	ticket, err := c.readTicket(ctx, number)
	if err != nil {
		return err
	}
	mutation := `mutation($id: ID!) { reopenIssue(input: {issueId: $id}) { clientMutationId } }`
	if err := c.do(ctx, mutation, map[string]any{"id": ticket.ID}, nil); err != nil {
		return fmt.Errorf("reopening ticket #%d: %w", number, err)
	}
	return c.writeStatus(ctx, ticket, StatusTodo)
}

// CloseReason values for closing a ticket without a pull request.
const (
	ReasonCompleted  = "completed"
	ReasonNotPlanned = "not-planned"
	ReasonDuplicate  = "duplicate"
)

// CloseOptions gives the evidence for closing a ticket: merged pull requests, or a reason.
type CloseOptions struct {
	PRs    []string
	Reason string
}

// CloseTicket closes a ticket after confirming every pull request merged.
func (c *Client) CloseTicket(ctx context.Context, number int, opts CloseOptions) error {
	ticket, err := c.readTicket(ctx, number)
	if err != nil {
		return err
	}
	reason, err := stateReason(opts.Reason)
	if err != nil {
		return err
	}
	var fields []string
	if len(opts.PRs) > 0 {
		urls, err := c.mergedPullRequests(ctx, ticket, opts.PRs)
		if err != nil {
			return err
		}
		body := fmt.Sprintf("Merged in %s", strings.Join(urls, ", "))
		fields = append(fields, fmt.Sprintf(`comment: addComment(input: {subjectId: %q, body: %q}) { clientMutationId }`, ticket.ID, body))
	}
	fields = append(fields, fmt.Sprintf(`close: closeIssue(input: {issueId: %q, stateReason: %s}) { clientMutationId }`, ticket.ID, reason))
	if ticket.Project != "" && ticket.itemID != "" {
		if project, err := c.lookupProject(ctx, ticket.Project); err == nil && project.BoardID != "" {
			field, err := statusMutation("status", project, ticket.itemID, StatusDone)
			if err != nil {
				return err
			}
			fields = append(fields, field)
		}
	}
	if err := c.write(ctx, fields); err != nil {
		return fmt.Errorf("closing ticket #%d: %w", number, err)
	}
	if ticket.Status == StatusDoing {
		return c.deleteClaim(ctx, number)
	}
	return nil
}

func stateReason(reason string) (string, error) {
	switch reason {
	case "", ReasonCompleted:
		return "COMPLETED", nil
	case ReasonNotPlanned:
		return "NOT_PLANNED", nil
	case ReasonDuplicate:
		return "DUPLICATE", nil
	}
	return "", fmt.Errorf("invalid reason %q: expected %s, %s, or %s", reason, ReasonCompleted, ReasonNotPlanned, ReasonDuplicate)
}

type pullRef struct {
	repo   Repo
	number int
}

// parsePullRef reads 88 or project#88. The ticket's project supplies the repository
// of a bare number, because the ticket already identifies it.
func (c *Client) parsePullRef(ctx context.Context, ticket Ticket, value string) (pullRef, error) {
	name, digits, qualified := strings.Cut(value, "#")
	if !qualified {
		name, digits = ticket.Project, value
	}
	number, err := strconv.Atoi(digits)
	if err != nil || number < 1 {
		return pullRef{}, fmt.Errorf("invalid pull request %q: expected a number or project#number", value)
	}
	if name == "" {
		return pullRef{}, fmt.Errorf("ticket #%d has no project: use project#number", ticket.Number)
	}
	project, err := c.lookupProject(ctx, name)
	if err != nil {
		return pullRef{}, err
	}
	repo, err := ParseRepo(project.Repo)
	if err != nil {
		return pullRef{}, err
	}
	return pullRef{repo: repo, number: number}, nil
}

func (c *Client) mergedPullRequests(ctx context.Context, ticket Ticket, values []string) ([]string, error) {
	refs := make([]pullRef, 0, len(values))
	for _, value := range values {
		ref, err := c.parsePullRef(ctx, ticket, value)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	var query strings.Builder
	query.WriteString("query {")
	for i, ref := range refs {
		fmt.Fprintf(&query, ` r%d: repository(owner: %q, name: %q) { pullRequest(number: %d) { merged url } }`, i, ref.repo.Owner, ref.repo.Name, ref.number)
	}
	query.WriteString(" }")
	response := map[string]*struct {
		PullRequest *struct {
			Merged bool
			URL    string
		}
	}{}
	if err := c.do(ctx, query.String(), nil, &response); err != nil && !hasGraphQLType(err, graphQLNotFoundType) {
		return nil, fmt.Errorf("reading pull requests: %w", err)
	}
	urls := make([]string, 0, len(refs))
	for i, ref := range refs {
		node := response["r"+strconv.Itoa(i)]
		if node == nil || node.PullRequest == nil {
			return nil, fmt.Errorf("pull request %s#%d not found", ref.repo, ref.number)
		}
		if !node.PullRequest.Merged {
			return nil, fmt.Errorf("pull request %s#%d is not merged: refusing to close", ref.repo, ref.number)
		}
		urls = append(urls, node.PullRequest.URL)
	}
	return urls, nil
}
