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
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cli/go-gh/v2/pkg/api"
)

// StateClosed is GitHub's state of a closed ticket.
const StateClosed = "CLOSED"

const (
	listPageSize  = 100
	stateOpen     = "OPEN"
	nestedLimit   = 20
	commentsLimit = 100
	// labelsLimit is the largest page GitHub serves. A shorter page could hide the
	// blocked label or a routing label, and each ticket holds only a few labels.
	labelsLimit = 100
	// searchCap is the most results GitHub serves for one search query.
	searchCap = 1000
	// subIssuesLimit is GitHub's maximum per parent, so one page holds them all.
	subIssuesLimit = 100

	labelBlocked = "blocked"

	// maxBodyLength is the most characters GitHub accepts in an issue or comment
	// body. GitHub rejects a longer one with 422 Validation Failed.
	maxBodyLength = 65536

	boardItemTaken = "Content already exists in this project"
	boardItemReads = 3
	boardItemWait  = 2 * time.Second
)

// ticketFields is the shared selection for one ticket. Reading blockers, parents,
// and board status here keeps list and claim commands to a single request.
var ticketFields = fmt.Sprintf(`%[3]s
  blockedBy(first: %[1]d) { nodes { number state } }
  parent {
    number state blockedBy(first: %[1]d) { nodes { state } } labels(first: %[4]d) { nodes { name } }
    parent { number state blockedBy(first: %[1]d) { nodes { state } } }
  }
  subIssueStates: subIssues(first: %[2]d) { nodes { state } }`, nestedLimit, subIssuesLimit, boardTicketFields, labelsLimit)

// boardTicketFields is what routes a ticket and gives its board status, without its
// relations. An epic reads its tickets with these.
var boardTicketFields = fmt.Sprintf(`id number title state url createdAt updatedAt
  labels(first: %[1]d) { nodes { name } }
  projectItems(first: 5) {
    nodes {
      id updatedAt project { id }
      fieldValueByName(name: "Status") { ... on ProjectV2ItemFieldSingleSelectValue { name } }
      component: fieldValueByName(name: %[2]q) { ... on ProjectV2ItemFieldSingleSelectValue { name } }
    }
  }`, labelsLimit, componentField)

type issueRef struct {
	Number int
	Title  string
	State  string
}

type ancestorNode struct {
	Number    int
	State     string
	BlockedBy struct{ Nodes []issueRef }
	Labels    labelNames
}

// labelNames is a label connection read for names only.
type labelNames struct{ Nodes []struct{ Name string } }

// has compares the way GitHub does, because label names ignore case there.
func (l labelNames) has(name string) bool {
	return slices.ContainsFunc(l.Nodes, func(label struct{ Name string }) bool {
		return strings.EqualFold(label.Name, name)
	})
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
	CreatedAt time.Time
	UpdatedAt time.Time
	Labels    labelNames
	BlockedBy struct{ Nodes []issueRef }
	Parent    *struct {
		ancestorNode
		Parent *ancestorNode
	}
	// SubIssueStates counts states itself because subIssuesSummary leaves tickets
	// closed as not planned out of its completed count.
	SubIssueStates struct{ Nodes []issueRef }
	ProjectItems   struct {
		Nodes []struct {
			ID               string
			UpdatedAt        time.Time
			Project          struct{ ID string }
			FieldValueByName *struct{ Name string }
			Component        *struct{ Name string }
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
	ParentEpic    bool
	Blockers      []int
	ParentBlocked bool
	SubIssues     int
	OpenSubIssues int
	Activity      time.Time

	itemID      string
	boardID     string
	component   string
	items       []boardItem
	created     time.Time
	boardRouted bool
}

// boardItem is a ticket's place on one board. A ticket can be on several, because
// GitHub Projects is not limited to the boards this tool manages.
type boardItem struct {
	id, boardID, status, component string
}

// useBoard makes a ticket's item on the board the current one, and reports whether
// the ticket is on it.
func (t *Ticket) useBoard(boardID string) bool {
	for _, item := range t.items {
		if item.boardID == boardID {
			t.itemID, t.boardID, t.Status, t.component = item.id, item.boardID, item.status, item.component
			return true
		}
	}
	return false
}

// Ready reports whether a ticket can be picked up: routed to a project, open,
// unblocked, with no blocked ancestor and no open sub-issues, not in progress, and
// neither a handoff nor an epic.
// An unrouted ticket waits for someone to give it a project. A parent waits for its
// children, so the work gets claimed rather than its container, and an epic is a
// container even before it has tickets. The blocked label covers waits with no
// ticket to name, such as a person's input.
func (t Ticket) Ready() bool {
	return t.Project != "" && t.State == stateOpen && len(t.Blockers) == 0 && !t.ParentBlocked && t.OpenSubIssues == 0 &&
		t.Status != StatusDoing && t.Status != StatusDone && !t.labelled(labelBlocked) && !t.Handoff() && !t.Epic()
}

// Handoff reports a ticket that hands work to the next agent. It is picked up with
// handoff take, not claimed as ready work.
func (t Ticket) Handoff() bool {
	return t.labelled(labelHandoff)
}

// Created is when the ticket was created.
func (t Ticket) Created() time.Time {
	return t.created
}

// Closable reports an open epic whose tickets are all closed. A claimed epic stays
// open, because closing it would take the ticket from whoever holds it, and so does
// a blocked one, because something still has to happen first.
func (t Ticket) Closable() bool {
	return t.Epic() && t.State == stateOpen && t.SubIssues > 0 && t.OpenSubIssues == 0 && t.Status != StatusDoing &&
		len(t.Blockers) == 0 && !t.labelled(labelBlocked)
}

// labelled compares the way GitHub does, because label names ignore case there.
func (t Ticket) labelled(name string) bool {
	return slices.ContainsFunc(t.Labels, func(label string) bool {
		return strings.EqualFold(label, name)
	})
}

// Stale reports a Doing ticket with no activity for the given number of days.
func (t Ticket) Stale(days int, now time.Time) bool {
	return t.State == stateOpen && t.Status == StatusDoing &&
		t.Activity.Before(now.AddDate(0, 0, -days))
}

func (n ticketNode) ticket() Ticket {
	t := Ticket{
		ID: n.ID, Number: n.Number, Title: n.Title, State: n.State, URL: n.URL, created: n.CreatedAt,
		Blockers: openNumbers(n.BlockedBy.Nodes), Activity: n.UpdatedAt,
		SubIssues: len(n.SubIssueStates.Nodes), OpenSubIssues: len(openNumbers(n.SubIssueStates.Nodes)),
	}
	for _, label := range n.Labels.Nodes {
		if name, ok := strings.CutPrefix(label.Name, labelPrefix); ok {
			// A component's label is more specific than its repository's.
			if !strings.HasPrefix(t.Project, name+"/") {
				t.Project = name
			}
			continue
		}
		t.Labels = append(t.Labels, label.Name)
	}
	if n.Parent != nil {
		t.Parent, t.ParentEpic = n.Parent.Number, n.Parent.Labels.has(labelEpic)
		t.ParentBlocked = n.Parent.blocked() || n.Parent.Parent.blocked()
	}
	for _, item := range n.ProjectItems.Nodes {
		entry := boardItem{id: item.ID, boardID: item.Project.ID}
		if item.FieldValueByName != nil {
			entry.status = item.FieldValueByName.Name
		}
		if item.Component != nil {
			entry.component = item.Component.Name
		}
		t.items = append(t.items, entry)
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
	node, err := readIssue[ticketNode](ctx, c, number, ticketFields)
	if err != nil {
		return Ticket{}, err
	}
	ticket, err := c.routed(ctx, node.ticket())
	if err != nil {
		return Ticket{}, fmt.Errorf("reading ticket #%d: %w", number, err)
	}
	return ticket, nil
}

// readIssue reads one issue of the tracker with the given fields into a node.
func readIssue[N any](ctx context.Context, c *Client, number int, fields string) (*N, error) {
	var response struct {
		Repository struct {
			IsPrivate bool
			Issue     *N
		}
	}
	query := `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) { isPrivate issue(number: $number) { ` + fields + ` } }
}`
	err := c.do(ctx, query, c.variables(map[string]any{"number": number}), &response)
	if hasGraphQLType(err, graphQLNotFoundType) || (err == nil && response.Repository.Issue == nil) {
		return nil, fmt.Errorf("ticket #%d not found in %s", number, c.repo)
	}
	if err != nil {
		return nil, fmt.Errorf("reading ticket #%d: %w", number, err)
	}
	if err := c.checkPrivate(response.Repository.IsPrivate); err != nil {
		return nil, err
	}
	return response.Repository.Issue, nil
}

// routed gives a ticket with no routing label the project whose board holds it, so a
// ticket created inside a board needs no label. A label wins when the two disagree.
// An unknown board or project can be one registered on another machine, so it
// refreshes the cached IDs once. A ticket takes its item and status only from its
// project's board, because GitHub lists a ticket's boards in no particular order
// and an item on another board rejects that board's fields. A ticket on no
// registered board has no item, so writes leave its other boards alone.
func (c *Client) routed(ctx context.Context, t Ticket) (Ticket, error) {
	if len(t.items) == 0 {
		return t, nil
	}
	meta, err := c.metadata(ctx)
	if err != nil {
		return t, err
	}
	if t.Project != "" {
		project, ok := meta.project(t.Project)
		if !ok && !c.boardsChecked {
			c.boardsChecked = true
			if meta, err = c.refreshMetadata(ctx); err != nil {
				return t, err
			}
			project, _ = meta.project(t.Project)
		}
		t.useBoard(project.BoardID)
		return t, nil
	}
	if t.routeTo(meta) || c.boardsChecked {
		return t, nil
	}
	c.boardsChecked = true
	if meta, err = c.refreshMetadata(ctx); err != nil {
		return t, err
	}
	t.routeTo(meta)
	return t, nil
}

func (t *Ticket) routeTo(meta *metadata) bool {
	for _, project := range meta.Projects {
		// A component shares its repository's board, whose Component value picks it.
		if !project.Component() && t.useBoard(project.BoardID) {
			t.Project, t.boardRouted = meta.boardProject(project.Name, t.component), true
			return true
		}
	}
	return false
}

// ListOptions filters tickets. Project is empty for all projects. Unrouted lists only
// tickets with no project, and excludes Project.
type ListOptions struct {
	Project   string
	Unrouted  bool
	State     string
	Status    string
	Label     string
	Priority  string
	Search    string
	Ready     bool
	Closable  bool
	Epic      bool
	Handoff   bool
	Stale     bool
	StaleDays int
	Limit     int
	Now       time.Time
	// Tally, when set, counts every ticket the listing reads, before filtering.
	Tally *Tally

	// activeEpics, when set, collects the epics with a ticket in Doing among every
	// ticket the listing reads, before filtering.
	activeEpics map[int]bool
}

// observe records what a listing learns from a ticket before filtering it.
func (o ListOptions) observe(t Ticket) {
	if o.Tally != nil {
		o.Tally.add(t)
	}
	if o.activeEpics != nil && t.ParentEpic && t.State == stateOpen && t.Status == StatusDoing {
		o.activeEpics[t.Parent] = true
	}
}

// Tally counts the tickets a listing read before filtering them: open tickets by
// board status, and closed tickets. OpenRead and ClosedRead report that the listing
// read every ticket in that state, which needs no search text and a read to the end.
// Handoffs holds the open handoffs it read.
type Tally struct {
	Open       map[string]int
	Closed     int
	OpenRead   bool
	ClosedRead bool
	Handoffs   []Ticket
}

func (t *Tally) reset() {
	*t = Tally{Open: map[string]int{}}
}

func (t *Tally) add(ticket Ticket) {
	if ticket.State == StateClosed {
		t.Closed++
		return
	}
	t.Open[ticket.Status]++
	if ticket.Handoff() {
		t.Handoffs = append(t.Handoffs, ticket)
	}
}

// read records that a listing read every ticket of its states.
func (t *Tally) read(states []string) {
	t.OpenRead = slices.Contains(states, stateOpen)
	t.ClosedRead = slices.Contains(states, StateClosed)
}

// clientFiltered reports a filter GitHub does not apply, so its count excludes it.
func (o ListOptions) clientFiltered() bool {
	return o.Status != "" || o.Label != "" || o.Priority != "" || o.Ready || o.Closable || o.Epic || o.Handoff || o.Stale || o.Unrouted
}

func (o ListOptions) matches(t Ticket) bool {
	switch {
	case o.Unrouted && t.Project != "":
		return false
	case o.Status != "" && !strings.EqualFold(t.Status, o.Status):
		return false
	case o.Label != "" && !t.labelled(o.Label):
		return false
	case o.Priority != "" && t.Priority() != o.Priority:
		return false
	case o.Ready && !t.Ready():
		return false
	case o.Closable && !t.Closable():
		return false
	case o.Epic && !t.Epic():
		return false
	case o.Handoff && !t.Handoff():
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
		return []string{StateClosed}, nil
	case "all":
		return []string{stateOpen, StateClosed}, nil
	}
	return nil, fmt.Errorf("invalid state %q: expected open, closed, or all", state)
}

// ticketPage is one page of tickets from either listing query.
type ticketPage struct {
	IsPrivate     bool
	Nodes         []ticketNode
	PageInfo      pageInfo
	Total         int
	Registrations []labelNode
}

// pageReader fetches the page after a cursor, with nil for the first page.
type pageReader func(context.Context, any) (ticketPage, error)

// registrationFields reads the routing labels of a repository's components with a
// listing. A component registered on another machine causes no cache miss, so
// without this its tickets would be left out of the repository's lists.
const registrationFields = `registrations: labels(query: $registrations, first: 100) @include(if: $checkRegistrations) { nodes { id name description } }`

// registrationVariables asks for the labels of a repository project's components.
// A component has none, and an unscoped listing has no project to check.
func registrationVariables(project Project) map[string]any {
	check := project.Name != "" && !project.Component()
	query := ""
	if check {
		query = project.Label() + "/"
	}
	return map[string]any{"registrations": query, "checkRegistrations": check}
}

// withProject runs a read of a project, and runs it once more with refreshed metadata
// when the read reports that the cached components are stale.
func (c *Client) withProject(ctx context.Context, name string, read func(*metadata, Project) (bool, error)) error {
	project, err := c.lookupProject(ctx, name)
	if err != nil {
		return err
	}
	meta, err := c.metadata(ctx)
	if err != nil {
		return err
	}
	stale, err := read(meta, project)
	if err != nil || !stale {
		return err
	}
	if meta, err = c.refreshMetadata(ctx); err != nil {
		return err
	}
	project, ok := meta.project(name)
	if !ok {
		return fmt.Errorf("unknown project %q: register it with project add", name)
	}
	_, err = read(meta, project)
	return err
}

// ListTickets returns matching tickets, oldest first, and how many tickets match
// in all, which exceeds the tickets returned when Limit cuts the list. GitHub
// counts what it filters on the first page. A client-side filter has no such
// count, so it reads every page whatever the limit. A repository's list includes
// its components' tickets. Ready tickets come most urgent first, then within a
// priority the tickets of an epic with a ticket in Doing, then oldest first, so that
// the next claim takes the most urgent work and finishes a started epic.
func (c *Client) ListTickets(ctx context.Context, opts ListOptions) ([]Ticket, int, error) {
	if opts.Limit < 0 {
		return nil, 0, fmt.Errorf("invalid limit %d: expected 0 or more", opts.Limit)
	}
	if opts.Priority != "" {
		priority, err := parsePriority(opts.Priority)
		if err != nil {
			return nil, 0, err
		}
		opts.Priority = priority
	}
	if !opts.Ready {
		return c.oldestTickets(ctx, opts)
	}
	// Ready reads every page anyway, so sorting every match before the limit costs
	// no request.
	limit := opts.Limit
	opts.Limit = 0
	opts.activeEpics = map[int]bool{}
	tickets, total, err := c.oldestTickets(ctx, opts)
	if err != nil {
		return nil, 0, err
	}
	mostUrgentFirst(tickets, opts.activeEpics)
	if limit > 0 && len(tickets) > limit {
		tickets = tickets[:limit]
	}
	return tickets, total, nil
}

// oldestTickets lists matching tickets oldest first.
func (c *Client) oldestTickets(ctx context.Context, opts ListOptions) ([]Ticket, int, error) {
	states, err := listStates(opts.State)
	if err != nil {
		return nil, 0, err
	}
	if opts.Unrouted {
		if opts.Project != "" {
			return nil, 0, errors.New("unrouted tickets have no project: pass a project or unrouted, not both")
		}
		meta, err := c.metadata(ctx)
		if err != nil {
			return nil, 0, err
		}
		tickets, total, _, err := c.listTickets(ctx, opts, states, meta, Project{})
		return tickets, total, err
	}
	if opts.Project == "" {
		tickets, total, _, err := c.listTickets(ctx, opts, states, &metadata{}, Project{})
		return tickets, total, err
	}
	var tickets []Ticket
	var total int
	err = c.withProject(ctx, opts.Project, func(meta *metadata, project Project) (bool, error) {
		var stale bool
		var err error
		tickets, total, stale, err = c.listTickets(ctx, opts, states, meta, project)
		return stale, err
	})
	if err != nil {
		return nil, 0, err
	}
	return tickets, total, nil
}

// listTickets lists one project's tickets, or every ticket when project is empty. It
// stops after the first page when the cached components are stale.
func (c *Client) listTickets(ctx context.Context, opts ListOptions, states []string, meta *metadata, project Project) ([]Ticket, int, bool, error) {
	if opts.Tally != nil {
		opts.Tally.reset()
	}
	var labels []string
	var onBoard []Ticket
	if project.Name != "" {
		labels = meta.scopeLabels(project)
		var err error
		if onBoard, err = c.boardOnlyTickets(ctx, meta, project, states, opts); err != nil {
			return nil, 0, false, fmt.Errorf("listing tickets: %w", err)
		}
	}
	read := c.issuesPage(states, labels, project)
	switch {
	case opts.Unrouted:
		read = c.searchPage(states, meta.unroutedScope(), opts.Search, project)
	case opts.Search != "":
		read = c.searchPage(states, labelScope(labels), opts.Search, project)
	}
	tickets := []Ticket{}
	matched := 0
	var cursor any
	for {
		page, err := read(ctx, cursor)
		if err != nil {
			return nil, 0, false, fmt.Errorf("listing tickets: %w", err)
		}
		if err := c.checkPrivate(page.IsPrivate); err != nil {
			return nil, 0, false, err
		}
		if cursor == nil && meta.staleComponents(project, page.Registrations) {
			return nil, 0, true, nil
		}
		for _, node := range page.Nodes {
			ticket, err := c.routed(ctx, node.ticket())
			if err != nil {
				return nil, 0, false, fmt.Errorf("listing tickets: %w", err)
			}
			opts.observe(ticket)
			if !opts.matches(ticket) {
				continue
			}
			matched++
			if opts.Limit == 0 || len(tickets) < opts.Limit {
				tickets = append(tickets, ticket)
			}
		}
		if opts.Limit > 0 && len(tickets) == opts.Limit && !opts.clientFiltered() {
			return oldestFirst(tickets, onBoard, opts.Limit), page.Total + len(onBoard), false, nil
		}
		if !page.PageInfo.HasNextPage {
			if opts.Tally != nil && opts.Search == "" && !opts.Unrouted {
				opts.Tally.read(states)
			}
			return oldestFirst(tickets, onBoard, opts.Limit), matched + len(onBoard), false, nil
		}
		cursor = page.PageInfo.EndCursor
	}
}

// labelScope is the search qualifier matching any of the labels.
func labelScope(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(labels))
	for _, label := range labels {
		quoted = append(quoted, strconv.Quote(label))
	}
	return "label:" + strings.Join(quoted, ",")
}

// boardOnlyTickets reads the tickets on a project's board that have no routing label
// of its repository or components. The issues connection filters by label only, so
// these need a search of their own. Each is routed by its Component value, and a
// component keeps only its own.
func (c *Client) boardOnlyTickets(ctx context.Context, meta *metadata, project Project, states []string, opts ListOptions) ([]Ticket, error) {
	if project.BoardURL == "" {
		return nil, nil
	}
	read := c.searchPage(states, c.boardOnlyScope(project, meta.familyLabels(project)), opts.Search, Project{})
	tickets := []Ticket{}
	var cursor any
	for {
		page, err := read(ctx, cursor)
		if err != nil {
			return nil, err
		}
		if err := c.checkPrivate(page.IsPrivate); err != nil {
			return nil, err
		}
		for _, node := range page.Nodes {
			ticket := node.ticket()
			// The search cannot exclude a label from another project, which wins.
			if ticket.Project != "" || !ticket.useBoard(project.BoardID) {
				continue
			}
			ticket.Project, ticket.boardRouted = meta.boardProject(project.Root(), ticket.component), true
			if project.Component() && ticket.Project != project.Name {
				continue
			}
			opts.observe(ticket)
			if opts.matches(ticket) {
				tickets = append(tickets, ticket)
			}
		}
		if !page.PageInfo.HasNextPage {
			return tickets, nil
		}
		cursor = page.PageInfo.EndCursor
	}
}

// boardOnlyScope is the search for a project's board without any of its routing labels.
func (c *Client) boardOnlyScope(project Project, labels []string) string {
	terms := []string{fmt.Sprintf("project:%s/%s", c.repo.Owner, path.Base(project.BoardURL))}
	for _, label := range labels {
		terms = append(terms, "-label:"+strconv.Quote(label))
	}
	return strings.Join(terms, " ")
}

// oldestFirst merges board-only tickets into a list and applies the limit again.
func oldestFirst(tickets, onBoard []Ticket, limit int) []Ticket {
	if len(onBoard) == 0 {
		return tickets
	}
	merged := append(tickets, onBoard...)
	slices.SortStableFunc(merged, func(a, b Ticket) int { return a.created.Compare(b.created) })
	if limit > 0 && len(merged) > limit {
		return merged[:limit]
	}
	return merged
}

func (c *Client) issuesPage(states, labels []string, project Project) pageReader {
	query := `query($owner: String!, $name: String!, $states: [IssueState!], $labels: [String!], $after: String, $registrations: String, $checkRegistrations: Boolean!) {
  repository(owner: $owner, name: $name) {
    isPrivate
    ` + registrationFields + `
    issues(first: ` + strconv.Itoa(listPageSize) + `, after: $after, states: $states, labels: $labels, orderBy: {field: CREATED_AT, direction: ASC}) {
      totalCount
      nodes { ` + ticketFields + ` }
      pageInfo { hasNextPage endCursor }
    }
  }
}`
	return func(ctx context.Context, cursor any) (ticketPage, error) {
		var response struct {
			Repository struct {
				IsPrivate     bool
				Registrations struct{ Nodes []labelNode }
				Issues        struct {
					TotalCount int
					Nodes      []ticketNode
					PageInfo   pageInfo
				}
			}
		}
		variables := c.variables(registrationVariables(project))
		variables["states"], variables["labels"], variables["after"] = states, labels, cursor
		if err := c.do(ctx, query, variables, &response); err != nil {
			return ticketPage{}, err
		}
		repository := response.Repository
		issues := repository.Issues
		return ticketPage{IsPrivate: repository.IsPrivate, Nodes: issues.Nodes, PageInfo: issues.PageInfo, Total: issues.TotalCount, Registrations: repository.Registrations.Nodes}, nil
	}
}

// searchPage matches ticket text with GitHub search, which the issues connection
// lacks. A search query stops at searchCap results, so a longer one continues with a
// new query from the last creation time. That query starts with the tickets created
// in the same second, which were already served.
func (c *Client) searchPage(states []string, scope, text string, project Project) pageReader {
	terms := []string{"repo:" + c.repo.String(), "is:issue", "sort:created-asc"}
	if len(states) == 1 {
		terms = append(terms, "is:"+strings.ToLower(states[0]))
	}
	if scope != "" {
		terms = append(terms, scope)
	}
	query := `query($owner: String!, $name: String!, $search: String!, $after: String, $registrations: String, $checkRegistrations: Boolean!) {
  repository(owner: $owner, name: $name) { isPrivate ` + registrationFields + ` }
  search(query: $search, type: ISSUE, first: ` + strconv.Itoa(listPageSize) + `, after: $after) {
    issueCount
    nodes { ... on Issue { ` + ticketFields + ` } }
    pageInfo { hasNextPage endCursor }
  }
}`
	var since time.Time
	served, total := 0, 0
	seen := map[int]bool{}
	return func(ctx context.Context, cursor any) (ticketPage, error) {
		var response struct {
			Repository struct {
				IsPrivate     bool
				Registrations struct{ Nodes []labelNode }
			}
			Search struct {
				IssueCount int
				Nodes      []ticketNode
				PageInfo   pageInfo
			}
		}
		search := slices.Clone(terms)
		if !since.IsZero() {
			search = append(search, "created:>="+since.Format(time.RFC3339))
		}
		if text != "" {
			search = append(search, text)
		}
		if cursor == "" {
			cursor = nil
		}
		variables := c.variables(registrationVariables(project))
		variables["search"], variables["after"] = strings.Join(search, " "), cursor
		if err := c.do(ctx, query, variables, &response); err != nil {
			return ticketPage{}, err
		}
		result := response.Search
		if total == 0 {
			total = result.IssueCount
		}
		page := ticketPage{IsPrivate: response.Repository.IsPrivate, Total: total, PageInfo: result.PageInfo, Registrations: response.Repository.Registrations.Nodes}
		for _, node := range result.Nodes {
			if !seen[node.Number] {
				seen[node.Number] = true
				page.Nodes = append(page.Nodes, node)
			}
		}
		served += len(result.Nodes)
		if result.PageInfo.HasNextPage || served < searchCap || result.IssueCount <= served || len(result.Nodes) == 0 {
			return page, nil
		}
		last := result.Nodes[len(result.Nodes)-1].CreatedAt
		if !last.After(since) {
			return ticketPage{}, fmt.Errorf("more than %d tickets were created at %s: search cannot page past them", searchCap, last.Format(time.RFC3339))
		}
		since, served = last, 0
		page.PageInfo = pageInfo{HasNextPage: true}
		return page, nil
	}
}

// Comment is one comment on a ticket.
type Comment struct {
	Author    string
	CreatedAt time.Time
	Body      string

	id string
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
		ID        string
		Author    *struct{ Login string }
		CreatedAt time.Time
		Body      string
	}
	PageInfo pageInfo
}

func (p commentPage) comments() []Comment {
	comments := make([]Comment, 0, len(p.Nodes))
	for _, node := range p.Nodes {
		comment := Comment{CreatedAt: node.CreatedAt, Body: node.Body, id: node.ID}
		if node.Author != nil {
			comment.Author = node.Author.Login
		}
		comments = append(comments, comment)
	}
	return comments
}

var attachmentURL = regexp.MustCompile(`https://github\.com/user-attachments/(?:assets|files)/[A-Za-z0-9._~%/-]+`)

// MaxInspect is the most tickets InspectTickets reads in one call. GitHub charges
// 2 points for 20 of them and 5 points for 50.
const MaxInspect = 50

type detailNode struct {
	ticketNode
	Body      string
	Blocking  struct{ Nodes []issueRef }
	SubIssues struct{ Nodes []SubIssue }
	Comments  commentPage
}

var detailFields = fmt.Sprintf(`%s
      body
      blocking(first: %[2]d) { nodes { number state } }
      subIssues(first: %[3]d) { nodes { number title state } }
      comments(first: %[3]d) { nodes { id author { login } createdAt body } pageInfo { hasNextPage endCursor } }`, ticketFields, nestedLimit, commentsLimit)

// InspectTicket reads a ticket with everything an agent needs to work on it.
func (c *Client) InspectTicket(ctx context.Context, number int) (TicketDetail, error) {
	details, err := c.InspectTickets(ctx, []int{number})
	if err != nil {
		return TicketDetail{}, err
	}
	return details[0], nil
}

// InspectTickets reads tickets in the order given, each repeated number once, in one
// request plus one per further 100 comments on a ticket.
func (c *Client) InspectTickets(ctx context.Context, numbers []int) ([]TicketDetail, error) {
	numbers = uniqueNumbers(numbers)
	if len(numbers) > MaxInspect {
		return nil, fmt.Errorf("inspect at most %d tickets at once, not %d", MaxInspect, len(numbers))
	}
	details, missing, err := c.readDetails(ctx, numbers)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%s not found in %s", ticketNumbers(missing), c.repo)
	}
	return details, nil
}

// readDetails reads up to MaxInspect tickets in one request, in order, and reports
// the numbers GitHub has no ticket for instead of failing on them.
func (c *Client) readDetails(ctx context.Context, numbers []int) ([]TicketDetail, []int, error) {
	var query strings.Builder
	query.WriteString("query($owner: String!, $name: String!) {\n  repository(owner: $owner, name: $name) {\n    isPrivate")
	for _, number := range numbers {
		fmt.Fprintf(&query, "\n    n%d: issue(number: %[1]d) {\n      %s\n    }", number, detailFields)
	}
	query.WriteString("\n  }\n}")
	var response struct {
		Repository map[string]json.RawMessage
	}
	err := c.do(ctx, query.String(), c.variables(nil), &response)
	if err != nil && !hasGraphQLType(err, graphQLNotFoundType) {
		return nil, nil, fmt.Errorf("reading %s: %w", ticketNumbers(numbers), err)
	}
	nodes := make([]detailNode, len(numbers))
	var missing []int
	found := make([]bool, len(numbers))
	for i, number := range numbers {
		raw := response.Repository["n"+strconv.Itoa(number)]
		if len(raw) == 0 || string(raw) == "null" {
			missing = append(missing, number)
			continue
		}
		if err := json.Unmarshal(raw, &nodes[i]); err != nil {
			return nil, nil, fmt.Errorf("reading ticket #%d: %w", number, err)
		}
		found[i] = true
	}
	// A not-found error with no missing ticket is about something else.
	if err != nil && len(missing) == 0 {
		return nil, nil, fmt.Errorf("reading %s: %w", ticketNumbers(numbers), err)
	}
	if len(missing) == len(numbers) {
		return nil, missing, nil
	}
	var isPrivate bool
	if err := json.Unmarshal(response.Repository["isPrivate"], &isPrivate); err != nil {
		return nil, nil, fmt.Errorf("reading tracker visibility: %w", err)
	}
	if err := c.checkPrivate(isPrivate); err != nil {
		return nil, nil, err
	}
	details := make([]TicketDetail, 0, len(numbers))
	for i, number := range numbers {
		if !found[i] {
			continue
		}
		detail, err := c.detail(ctx, number, nodes[i])
		if err != nil {
			return nil, nil, err
		}
		details = append(details, detail)
	}
	return details, missing, nil
}

func uniqueNumbers(numbers []int) []int {
	seen := make(map[int]bool, len(numbers))
	unique := make([]int, 0, len(numbers))
	for _, number := range numbers {
		if !seen[number] {
			seen[number] = true
			unique = append(unique, number)
		}
	}
	return unique
}

// ticketNumbers names tickets in errors: "ticket #1" or "tickets #1, #2".
func ticketNumbers(numbers []int) string {
	parts := make([]string, 0, len(numbers))
	for _, number := range numbers {
		parts = append(parts, "#"+strconv.Itoa(number))
	}
	if len(parts) == 1 {
		return "ticket " + parts[0]
	}
	return "tickets " + strings.Join(parts, ", ")
}

func (c *Client) detail(ctx context.Context, number int, issue detailNode) (TicketDetail, error) {
	ticket, err := c.routed(ctx, issue.ticket())
	if err != nil {
		return TicketDetail{}, fmt.Errorf("reading ticket #%d: %w", number, err)
	}
	detail := TicketDetail{
		Ticket: ticket, Body: issue.Body, Blocking: openNumbers(issue.Blocking.Nodes),
		SubIssues: issue.SubIssues.Nodes, Comments: issue.Comments.comments(),
	}
	if issue.Comments.PageInfo.HasNextPage {
		more, err := c.remainingComments(ctx, number, issue.Comments.PageInfo.EndCursor)
		if err != nil {
			return TicketDetail{}, err
		}
		detail.Comments = append(detail.Comments, more...)
	}
	detail.Attachments = findAttachments(detail.Body, detail.Comments, attachmentURL, c.releaseAssetURL())
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
      comments(first: %d, after: $after) { nodes { id author { login } createdAt body } pageInfo { hasNextPage endCursor } }
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

// findAttachments lists the links in a ticket's text that patterns match, in order
// and each once.
func findAttachments(body string, comments []Comment, patterns ...*regexp.Regexp) []string {
	seen := map[string]bool{}
	urls := []string{}
	add := func(text string) {
		for _, pattern := range patterns {
			for _, link := range pattern.FindAllString(text, -1) {
				if !seen[link] {
					seen[link] = true
					urls = append(urls, link)
				}
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
	nodes, err := readNodes[struct{ ID string }](ctx, c, numbers, "id")
	if err != nil {
		return nil, err
	}
	ids := make(map[int]string, len(nodes))
	for number, node := range nodes {
		ids[number] = node.ID
	}
	return ids, nil
}

// readNodes reads several tickets with the given fields in one request. A ticket
// GitHub does not have fails the read.
func readNodes[N any](ctx context.Context, c *Client, numbers []int, fields string) (map[int]N, error) {
	if len(numbers) == 0 {
		return map[int]N{}, nil
	}
	var selection strings.Builder
	for _, number := range numbers {
		fmt.Fprintf(&selection, " n%[1]d: issue(number: %[1]d) { %s }", number, fields)
	}
	query := `query($owner: String!, $name: String!) { repository(owner: $owner, name: $name) { isPrivate` + selection.String() + ` } }`
	var response struct {
		Repository map[string]json.RawMessage
	}
	if err := c.do(ctx, query, c.variables(nil), &response); err != nil {
		if hasGraphQLType(err, graphQLNotFoundType) {
			return nil, fmt.Errorf("one of tickets %v does not exist in %s", numbers, c.repo)
		}
		return nil, fmt.Errorf("reading tickets: %w", err)
	}
	var isPrivate bool
	if raw := response.Repository["isPrivate"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &isPrivate); err != nil {
			return nil, fmt.Errorf("reading tracker visibility: %w", err)
		}
	}
	if err := c.checkPrivate(isPrivate); err != nil {
		return nil, err
	}
	nodes := make(map[int]N, len(numbers))
	for _, number := range numbers {
		raw := response.Repository["n"+strconv.Itoa(number)]
		if len(raw) == 0 || string(raw) == "null" {
			return nil, fmt.Errorf("ticket #%d not found in %s", number, c.repo)
		}
		var node N
		if err := json.Unmarshal(raw, &node); err != nil {
			return nil, fmt.Errorf("reading ticket #%d: %w", number, err)
		}
		nodes[number] = node
	}
	return nodes, nil
}

func statusMutation(alias string, project Project, itemID, status string) (string, error) {
	optionID, ok := project.StatusOptions[status]
	if !ok {
		return "", fmt.Errorf("board for %s has no %s column: run project add %s", project.Name, status, project.Repo)
	}
	return fmt.Sprintf(`%s: updateProjectV2ItemFieldValue(input: {projectId: %q, itemId: %q, fieldId: %q, value: {singleSelectOptionId: %q}}) { clientMutationId }`,
		alias, project.BoardID, itemID, project.StatusFieldID, optionID), nil
}

// componentMutation sets a board item's Component value to the project's component,
// or clears it for the repository. It reports false when the board has no Component
// field or no option for the component, so the value cannot route the ticket.
func componentMutation(alias string, project Project, itemID string) (string, bool) {
	if project.ComponentFieldID == "" {
		return "", false
	}
	if !project.Component() {
		return fmt.Sprintf(`%s: clearProjectV2ItemFieldValue(input: {projectId: %q, itemId: %q, fieldId: %q}) { clientMutationId }`,
			alias, project.BoardID, itemID, project.ComponentFieldID), true
	}
	optionID, ok := project.ComponentOptions[project.componentName()]
	if !ok {
		return "", false
	}
	return fmt.Sprintf(`%s: updateProjectV2ItemFieldValue(input: {projectId: %q, itemId: %q, fieldId: %q, value: {singleSelectOptionId: %q}}) { clientMutationId }`,
		alias, project.BoardID, itemID, project.ComponentFieldID, optionID), true
}

// CreateOptions describes a new ticket.
type CreateOptions struct {
	Project        string
	Title          string
	Body           string
	DiscoveredFrom int
	// Epic is the epic the ticket belongs to, or zero.
	Epic        int
	BlockedBy   []int
	Blocking    []int
	Labels      []string
	Attachments []string
	// labelIDs are labels the ticket carries from creation, such as a handoff's.
	labelIDs []string
	// children are tickets that become the new ticket's sub-issues, for a new epic.
	children []int
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
	if err := checkBodyLength(createBody(opts)); err != nil {
		return Ticket{}, err
	}
	project, err := c.lookupProject(ctx, opts.Project)
	if err != nil {
		return Ticket{}, err
	}
	related := slices.Concat(opts.BlockedBy, opts.Blocking, opts.children)
	if opts.Epic != 0 {
		related = append(related, opts.Epic)
	}
	relatives, err := c.relatives(ctx, uniqueNumbers(related))
	if err != nil {
		return Ticket{}, err
	}
	if opts.Epic != 0 {
		if err := relatives[opts.Epic].checkEpic(opts.Epic); err != nil {
			return Ticket{}, err
		}
		if err := relatives[opts.Epic].checkRoom(opts.Epic, 1); err != nil {
			return Ticket{}, err
		}
	}
	for _, child := range opts.children {
		if err := relatives[child].checkNewChild(child); err != nil {
			return Ticket{}, err
		}
	}
	ids := make(map[int]string, len(relatives))
	for number, relative := range relatives {
		ids[number] = relative.ID
	}
	var created createdTicket
	err = c.withFreshIDs(ctx, func(meta *metadata) error {
		fresh, ok := meta.project(opts.Project)
		if !ok {
			return fmt.Errorf("unknown project %q: register it with project add", opts.Project)
		}
		if fresh.BoardID == "" {
			return c.errNoBoard(fresh)
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
	body := createBody(opts)
	if err := c.AddAttachments(ctx, created.Number, &body, opts.Attachments); err != nil {
		return ticket, fmt.Errorf("ticket #%d was created but attaching failed: %w", created.Number, err)
	}
	return ticket, nil
}

type createdTicket struct {
	ID, URL string
	Number  int
}

func (c *Client) insertTicket(ctx context.Context, meta *metadata, project Project, opts CreateOptions, ids map[int]string) (createdTicket, error) {
	input := map[string]any{
		"repositoryId": meta.RepositoryID, "title": opts.Title, "body": createBody(opts),
		"labelIds": append([]string{project.LabelID}, opts.labelIDs...),
	}
	if opts.Epic != 0 {
		input["parentIssueId"] = ids[opts.Epic]
	}
	var response struct {
		CreateIssue struct{ Issue createdTicket }
	}
	mutation := `mutation($input: CreateIssueInput!) {
  createIssue(input: $input) { issue { id number url } }
}`
	if err := c.do(ctx, mutation, map[string]any{"input": input}, &response); err != nil {
		return createdTicket{}, fmt.Errorf("creating ticket: %w", err)
	}
	return response.CreateIssue.Issue, nil
}

// checkBodyLength refuses a body GitHub would reject, before any request. GitHub
// counts characters, not bytes.
func checkBodyLength(body string) error {
	if n := utf8.RuneCountInString(body); n > maxBodyLength {
		return fmt.Errorf("body is %d characters, over GitHub's %d: attach long output as a file with --attach", n, maxBodyLength)
	}
	return nil
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
	// createIssue is not given the board because it adds the item asynchronously,
	// which would race this add on every ticket.
	var itemID string
	err := c.withFreshIDs(ctx, func(meta *metadata) error {
		fresh, ok := meta.project(project.Name)
		if !ok {
			return fmt.Errorf("unknown project %q: register it with project add", project.Name)
		}
		if fresh.BoardID == "" {
			return c.errNoBoard(fresh)
		}
		var err error
		project = fresh
		itemID, err = c.addToBoard(ctx, fresh, created.ID)
		return err
	})
	if err != nil {
		return err
	}
	field, err := statusMutation("status", project, itemID, StatusTodo)
	if err != nil {
		return err
	}
	fields := []string{field}
	if component, ok := componentMutation("component", project, itemID); ok && project.Component() {
		fields = append(fields, component)
	}
	for i, number := range opts.BlockedBy {
		fields = append(fields, blockMutation("blockedBy"+strconv.Itoa(i), created.ID, ids[number]))
	}
	for i, number := range opts.Blocking {
		fields = append(fields, blockMutation("blocking"+strconv.Itoa(i), ids[number], created.ID))
	}
	for i, number := range opts.children {
		fields = append(fields, subIssueMutation("child"+strconv.Itoa(i), "addSubIssue", created.ID, ids[number], false))
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
	Title       *string
	Body        *string
	Attachments []string
}

// EditTicket changes a ticket's title or body, and attaches files to the body.
func (c *Client) EditTicket(ctx context.Context, number int, opts EditOptions) error {
	if opts.Title == nil && opts.Body == nil && len(opts.Attachments) == 0 {
		return errors.New("nothing to change: pass a title, body, or attachment")
	}
	if opts.Body != nil {
		if err := checkBodyLength(*opts.Body); err != nil {
			return err
		}
	}
	if len(opts.Attachments) > 0 {
		if err := checkFiles(opts.Attachments); err != nil {
			return err
		}
		if opts.Title != nil {
			if err := c.EditTicket(ctx, number, EditOptions{Title: opts.Title}); err != nil {
				return err
			}
		}
		return c.AddAttachments(ctx, number, opts.Body, opts.Attachments)
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

// SetRelations adds or removes blockers of a ticket.
func (c *Client) SetBlockers(ctx context.Context, number int, blockers []int, add bool) error {
	return c.relate(ctx, number, blockers, func(alias, issueID, otherID string) string {
		if add {
			return blockMutation(alias, issueID, otherID)
		}
		return fmt.Sprintf(`%s: removeBlockedBy(input: {issueId: %q, blockingIssueId: %q}) { clientMutationId }`, alias, issueID, otherID)
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
		if !ok {
			return fmt.Errorf("unknown project %q: register it with project add", ticket.Project)
		}
		if project.BoardID == "" {
			return c.errNoBoard(project)
		}
		itemID := ticket.itemID
		fields := []string{}
		if itemID == "" {
			var err error
			if itemID, err = c.addToBoard(ctx, project, ticket.ID); err != nil {
				return err
			}
			// A new item has no Component value, so a component's ticket gets one.
			if field, ok := componentMutation("component", project, itemID); ok && project.Component() {
				fields = append(fields, field)
			}
		}
		field, err := statusMutation("status", project, itemID, status)
		if err != nil {
			return err
		}
		return c.write(ctx, append(fields, field))
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
	err := c.do(ctx, mutation, map[string]any{"board": project.BoardID, "content": contentID}, &response)
	if isOnBoard(err) {
		return c.readBoardItem(ctx, project, contentID)
	}
	if err != nil {
		return "", fmt.Errorf("adding ticket to board: %w", err)
	}
	return response.AddProjectV2ItemById.Item.ID, nil
}

// isOnBoard reports an add that lost to GitHub adding the same item, as its
// built-in sub-issue workflow does for a new ticket whose parent is on the board.
// Any other error in the same response is a real failure.
func isOnBoard(err error) bool {
	var gqlErr *api.GraphQLError
	return errors.As(err, &gqlErr) && len(gqlErr.Errors) > 0 && !slices.ContainsFunc(gqlErr.Errors, func(item api.GraphQLErrorItem) bool {
		return !strings.Contains(item.Message, boardItemTaken)
	})
}

// readBoardItem finds an item GitHub reports as added. The read can briefly lag
// behind that report, so it retries before giving up.
func (c *Client) readBoardItem(ctx context.Context, project Project, contentID string) (string, error) {
	query := `query($content: ID!) {
  node(id: $content) { ... on Issue { projectItems(first: 100) { nodes { id project { id } } } } }
}`
	for attempt := range boardItemReads {
		if attempt > 0 {
			if err := c.pause(ctx, boardItemWait); err != nil {
				return "", err
			}
		}
		var response struct {
			Node struct {
				ProjectItems struct {
					Nodes []struct {
						ID      string
						Project struct{ ID string }
					}
				}
			}
		}
		if err := c.do(ctx, query, map[string]any{"content": contentID}, &response); err != nil {
			return "", fmt.Errorf("reading board item: %w", err)
		}
		for _, item := range response.Node.ProjectItems.Nodes {
			if item.Project.ID == project.BoardID {
				return item.ID, nil
			}
		}
	}
	return "", errors.New("ticket is on the board, but its item cannot be read yet")
}

// ensureLabel returns a label's ID, creating the label on first use.
func (c *Client) ensureLabel(ctx context.Context, name, color, description string) (string, error) {
	var response struct {
		Repository struct {
			ID    string
			Label *struct{ ID string }
		}
	}
	query := `query($owner: String!, $name: String!, $label: String!) { repository(owner: $owner, name: $name) { id label(name: $label) { id } } }`
	if err := c.do(ctx, query, c.variables(map[string]any{"label": name}), &response); err != nil {
		return "", fmt.Errorf("reading %s label: %w", name, err)
	}
	if response.Repository.Label != nil {
		return response.Repository.Label.ID, nil
	}
	var created struct {
		CreateLabel struct {
			Label struct{ ID string }
		}
	}
	mutation := `mutation($repository: ID!, $name: String!, $color: String!, $description: String!) {
  createLabel(input: {repositoryId: $repository, name: $name, color: $color, description: $description}) { label { id } }
}`
	variables := map[string]any{"repository": response.Repository.ID, "name": name, "color": color, "description": description}
	if err := c.do(ctx, mutation, variables, &created); err != nil {
		return "", fmt.Errorf("creating %s label: %w", name, err)
	}
	return created.CreateLabel.Label.ID, nil
}

// AddLabels adds labels to a ticket. GitHub creates a name it does not know yet.
func (c *Client) AddLabels(ctx context.Context, number int, labels []string) error {
	if err := checkPlainLabels(labels); err != nil {
		return err
	}
	return c.addLabels(ctx, number, labels)
}

func (c *Client) addLabels(ctx context.Context, number int, labels []string) error {
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
	return c.removeLabels(ctx, number, labels)
}

func (c *Client) removeLabels(ctx context.Context, number int, labels []string) error {
	for _, label := range labels {
		path := fmt.Sprintf("repos/%s/issues/%d/labels/%s", c.repo, number, url.PathEscape(label))
		if err := c.rest.DoWithContext(ctx, http.MethodDelete, path, nil, nil); err != nil {
			return fmt.Errorf("removing label %q from ticket #%d: %w", label, number, describeError(err))
		}
	}
	return nil
}

// checkPlainLabels keeps routing labels under project add and issue move, and the
// labels that make a ticket an epic or a handoff under their own commands, which
// keep the rules those tickets follow.
func checkPlainLabels(labels []string) error {
	for _, label := range labels {
		switch {
		case strings.HasPrefix(label, labelPrefix):
			return fmt.Errorf("label %q routes a ticket: use issue move instead", label)
		case strings.EqualFold(label, labelEpic):
			return fmt.Errorf("label %q makes a ticket an epic: use epic create, epic promote, or epic demote instead", label)
		case strings.EqualFold(label, labelHandoff):
			return fmt.Errorf("label %q makes a ticket a handoff: use handoff create instead", label)
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
	project, err := c.lookupBoard(ctx, target)
	if err != nil {
		return err
	}
	// A component uses its repository's board, so moving between them keeps the item
	// and sets its Component value. A ticket routed by the board needs no label for it.
	sameBoard := ticket.itemID != "" && ticket.boardID == project.BoardID
	component, routes := "", false
	if sameBoard {
		component, routes = componentMutation("component", project, ticket.itemID)
	}
	var fields []string
	if !routes || !ticket.boardRouted {
		fields = append(fields, fmt.Sprintf(`add: addLabelsToLabelable(input: {labelableId: %q, labelIds: [%q]}) { clientMutationId }`, ticket.ID, project.LabelID))
	}
	if ticket.Project != "" && !ticket.boardRouted {
		previous, err := c.lookupProject(ctx, ticket.Project)
		if err != nil {
			return err
		}
		fields = append(fields, fmt.Sprintf(`remove: removeLabelsFromLabelable(input: {labelableId: %q, labelIds: [%q]}) { clientMutationId }`, ticket.ID, previous.LabelID))
	}
	if routes {
		fields = append(fields, component)
	}
	if ticket.itemID != "" && !sameBoard {
		fields = append(fields, fmt.Sprintf(`drop: deleteProjectV2Item(input: {projectId: %q, itemId: %q}) { clientMutationId }`, ticket.boardID, ticket.itemID))
	}
	if err := c.write(ctx, fields); err != nil {
		return fmt.Errorf("moving ticket #%d: %w", number, err)
	}
	if sameBoard {
		return nil
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

// CloseOptions gives the evidence for closing a ticket: merged pull requests, a pull
// request whose head landed without GitHub marking it merged, or a reason. Comment,
// when set, is posted with the close.
type CloseOptions struct {
	PRs      []string
	LandedPR string
	Reason   string
	Comment  string
}

// closeFields read a ticket with its parent in full, so that closing the last open
// ticket of an epic can close the epic without another read.
var closeFields = ticketFields + `
  epicParent: parent { ` + ticketFields + ` }`

type closeNode struct {
	ticketNode
	EpicParent *ticketNode
}

// CloseTicket closes a ticket after confirming every pull request merged. When it
// was the last open ticket of its epic, the epic closes too, as completed, unless it
// is claimed or blocked. It returns that epic, or a zero ticket.
func (c *Client) CloseTicket(ctx context.Context, number int, opts CloseOptions) (Ticket, error) {
	node, err := readIssue[closeNode](ctx, c, number, closeFields)
	if err != nil {
		return Ticket{}, err
	}
	ticket, err := c.routed(ctx, node.ticket())
	if err != nil {
		return Ticket{}, fmt.Errorf("reading ticket #%d: %w", number, err)
	}
	if ticket.Epic() {
		return Ticket{}, fmt.Errorf("ticket #%d is an epic: use epic close", number)
	}
	if err := c.closeTicket(ctx, ticket, opts); err != nil {
		return Ticket{}, err
	}
	if node.EpicParent == nil {
		return Ticket{}, nil
	}
	epic, err := c.routed(ctx, node.EpicParent.ticket())
	if err != nil {
		return Ticket{}, fmt.Errorf("closed #%d, but reading its epic #%d failed: %w", number, epic.Number, err)
	}
	if ticket.State == stateOpen {
		epic.OpenSubIssues--
	}
	if !epic.Closable() {
		return Ticket{}, nil
	}
	if err := c.closeTicket(ctx, epic, CloseOptions{Reason: ReasonCompleted}); err != nil {
		return Ticket{}, fmt.Errorf("closed #%d, but closing its epic #%d failed: %w", number, epic.Number, err)
	}
	return epic, nil
}

func (c *Client) closeTicket(ctx context.Context, ticket Ticket, opts CloseOptions) error {
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
		fields = append(fields, commentMutation(ticket, fmt.Sprintf("Merged in %s", strings.Join(urls, ", "))))
	}
	if opts.LandedPR != "" {
		body, err := c.landedPullRequest(ctx, ticket, opts.LandedPR)
		if err != nil {
			return err
		}
		fields = append(fields, commentMutation(ticket, body))
	}
	if opts.Comment != "" {
		fields = append(fields, commentMutation(ticket, opts.Comment))
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
		return fmt.Errorf("closing ticket #%d: %w", ticket.Number, err)
	}
	if ticket.Status == StatusDoing {
		return c.deleteClaim(ctx, ticket.Number)
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

func commentMutation(ticket Ticket, body string) string {
	return fmt.Sprintf(`comment: addComment(input: {subjectId: %q, body: %q}) { clientMutationId }`, ticket.ID, body)
}

// landedPullRequest proves that a closed, unmerged pull request's head is on its base
// branch, and returns the comment recording it. GitHub marks a pull request merged
// only when its head lands while it is open, so a closed one stays unmerged for good.
func (c *Client) landedPullRequest(ctx context.Context, ticket Ticket, value string) (string, error) {
	ref, err := c.parsePullRef(ctx, ticket, value)
	if err != nil {
		return "", err
	}
	query := `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) { url state merged headRefOid baseRefName }
  }
}`
	var response struct {
		Repository *struct {
			PullRequest *struct {
				URL, State, HeadRefOid, BaseRefName string
				Merged                              bool
			}
		}
	}
	variables := map[string]any{"owner": ref.repo.Owner, "name": ref.repo.Name, "number": ref.number}
	if err := c.do(ctx, query, variables, &response); err != nil && !hasGraphQLType(err, graphQLNotFoundType) {
		return "", fmt.Errorf("reading pull request: %w", err)
	}
	if response.Repository == nil || response.Repository.PullRequest == nil {
		return "", fmt.Errorf("pull request %s#%d not found", ref.repo, ref.number)
	}
	pull := response.Repository.PullRequest
	if pull.Merged {
		return "", fmt.Errorf("pull request %s#%d is merged: use --pr", ref.repo, ref.number)
	}
	if pull.State != StateClosed {
		return "", fmt.Errorf("pull request %s#%d is still open: refusing to close", ref.repo, ref.number)
	}
	var comparison struct{ Status string }
	path := fmt.Sprintf("repos/%s/compare/%s...%s?per_page=1", ref.repo, url.PathEscape(pull.BaseRefName), pull.HeadRefOid)
	if err := c.rest.DoWithContext(ctx, http.MethodGet, path, nil, &comparison); err != nil {
		return "", fmt.Errorf("comparing pull request %s#%d: %w", ref.repo, ref.number, describeError(err))
	}
	if comparison.Status != "behind" && comparison.Status != "identical" {
		return "", fmt.Errorf("pull request %s#%d head %s is not on %s: refusing to close", ref.repo, ref.number, pull.HeadRefOid, pull.BaseRefName)
	}
	return fmt.Sprintf("Landed from %s at %s on %s. GitHub does not report it as merged.", pull.URL, pull.HeadRefOid, pull.BaseRefName), nil
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
