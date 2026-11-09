package tracker

import (
	"context"
	"fmt"
	"strconv"
)

const (
	// labelEpic marks a ticket that groups other tickets. It keeps the ticket out of
	// ready work from creation, before it has any tickets of its own.
	labelEpic = "epic"
	// epicLabelColor and epicLabelDescription describe the label on first use.
	epicLabelColor       = "3e4b9e"
	epicLabelDescription = "Groups tickets: issue-tracker epic inspect"
)

// Epic reports a ticket that groups other tickets. Agents claim its tickets, never
// the epic itself.
func (t Ticket) Epic() bool {
	return t.labelled(labelEpic)
}

// relative is a ticket read for the checks that relate it to an epic.
type relative struct {
	ID     string
	State  string
	Labels labelNames
	Parent *struct{ Number int }
}

var relativeFields = fmt.Sprintf(`id state labels(first: %d) { nodes { name } } parent { number }`, labelsLimit)

func (r relative) epic() bool {
	return r.Labels.has(labelEpic)
}

func (r relative) parent() int {
	if r.Parent == nil {
		return 0
	}
	return r.Parent.Number
}

// checkEpic refuses a ticket that cannot take new tickets.
func (r relative) checkEpic(number int) error {
	if !r.epic() {
		return fmt.Errorf("ticket #%d is not an epic: run epic promote %d first", number, number)
	}
	if r.State != stateOpen {
		return fmt.Errorf("epic #%d is closed: reopen it first", number)
	}
	return nil
}

// checkNewChild refuses a ticket that cannot join an epic that does not exist yet.
func (r relative) checkNewChild(number int) error {
	if r.epic() {
		return fmt.Errorf("ticket #%d is an epic: epics do not nest", number)
	}
	if parent := r.parent(); parent != 0 {
		return fmt.Errorf("ticket #%d already belongs to #%d: create the epic without it, then run epic add --move", number, parent)
	}
	return nil
}

// relatives reads tickets for the epic checks in one request.
func (c *Client) relatives(ctx context.Context, numbers []int) (map[int]relative, error) {
	return readNodes[relative](ctx, c, numbers, relativeFields)
}

// EpicOptions describes a new epic. Add holds existing tickets that become its
// tickets.
type EpicOptions struct {
	Project     string
	Title       string
	Body        string
	Add         []int
	BlockedBy   []int
	Labels      []string
	Attachments []string
}

// CreateEpic creates an epic on its project's board with status Todo. The epic
// label is set by the create itself, so the epic is never ready work.
func (c *Client) CreateEpic(ctx context.Context, opts EpicOptions) (Ticket, error) {
	if err := checkPlainLabels(opts.Labels); err != nil {
		return Ticket{}, err
	}
	labelID, err := c.ensureLabel(ctx, labelEpic, epicLabelColor, epicLabelDescription)
	if err != nil {
		return Ticket{}, err
	}
	return c.CreateTicket(ctx, CreateOptions{
		Project: opts.Project, Title: opts.Title, Body: opts.Body, BlockedBy: opts.BlockedBy,
		Labels: opts.Labels, Attachments: opts.Attachments, labelIDs: []string{labelID}, children: uniqueNumbers(opts.Add),
	})
}

// AddToEpic makes tickets the epic's tickets and reports how many it added. A
// ticket already in the epic is left alone. Move takes a ticket from another
// parent; without it, such a ticket fails the command before anything changes.
func (c *Client) AddToEpic(ctx context.Context, epic int, tickets []int, move bool) (int, error) {
	tickets = uniqueNumbers(tickets)
	relatives, err := c.relatives(ctx, uniqueNumbers(append([]int{epic}, tickets...)))
	if err != nil {
		return 0, err
	}
	if err := relatives[epic].checkEpic(epic); err != nil {
		return 0, err
	}
	var fields []string
	for _, number := range tickets {
		child := relatives[number]
		switch parent := child.parent(); {
		case number == epic:
			return 0, fmt.Errorf("epic #%d cannot contain itself", epic)
		case child.epic():
			return 0, fmt.Errorf("ticket #%d is an epic: epics do not nest", number)
		case parent == epic:
			continue
		case parent != 0 && !move:
			return 0, fmt.Errorf("ticket #%d already belongs to #%d: pass --move to move it", number, parent)
		}
		replace := child.parent() != 0
		fields = append(fields, subIssueMutation("m"+strconv.Itoa(len(fields)), "addSubIssue", relatives[epic].ID, child.ID, replace))
	}
	if err := c.write(ctx, fields); err != nil {
		return 0, fmt.Errorf("adding tickets to epic #%d: %w", epic, err)
	}
	return len(fields), nil
}

// RemoveFromEpic detaches tickets from an epic. Every ticket must belong to it.
func (c *Client) RemoveFromEpic(ctx context.Context, epic int, tickets []int) error {
	tickets = uniqueNumbers(tickets)
	relatives, err := c.relatives(ctx, uniqueNumbers(append([]int{epic}, tickets...)))
	if err != nil {
		return err
	}
	if !relatives[epic].epic() {
		return fmt.Errorf("ticket #%d is not an epic", epic)
	}
	fields := make([]string, 0, len(tickets))
	for i, number := range tickets {
		if relatives[number].parent() != epic {
			return fmt.Errorf("ticket #%d is not in epic #%d", number, epic)
		}
		fields = append(fields, subIssueMutation("m"+strconv.Itoa(i), "removeSubIssue", relatives[epic].ID, relatives[number].ID, false))
	}
	if err := c.write(ctx, fields); err != nil {
		return fmt.Errorf("removing tickets from epic #%d: %w", epic, err)
	}
	return nil
}

func subIssueMutation(alias, name, issueID, subIssueID string, replace bool) string {
	input := fmt.Sprintf("issueId: %q, subIssueId: %q", issueID, subIssueID)
	if replace {
		input += ", replaceParent: true"
	}
	return fmt.Sprintf(`%s: %s(input: {%s}) { clientMutationId }`, alias, name, input)
}

// EpicDetail is an epic with everything about it and its tickets, each routed to
// its project with its board status.
type EpicDetail struct {
	TicketDetail
	Tickets []Ticket
}

// Done counts the epic's closed tickets, closed for any reason.
func (d EpicDetail) Done() int {
	done := 0
	for _, ticket := range d.Tickets {
		if ticket.State == StateClosed {
			done++
		}
	}
	return done
}

var childFields = fmt.Sprintf(`children: subIssues(first: %d) { nodes { %s } }`, subIssuesLimit, boardTicketFields)

type epicNode struct {
	detailNode
	Children struct{ Nodes []ticketNode }
}

// InspectEpic reads an epic and its tickets in one request, plus one per further
// 100 comments on the epic.
func (c *Client) InspectEpic(ctx context.Context, number int) (EpicDetail, error) {
	node, err := readIssue[epicNode](ctx, c, number, detailFields+"\n      "+childFields)
	if err != nil {
		return EpicDetail{}, err
	}
	detail, err := c.detail(ctx, number, node.detailNode)
	if err != nil {
		return EpicDetail{}, err
	}
	if !detail.Epic() {
		return EpicDetail{}, fmt.Errorf("ticket #%d is not an epic: run issue inspect %d", number, number)
	}
	epic := EpicDetail{TicketDetail: detail, Tickets: make([]Ticket, 0, len(node.Children.Nodes))}
	for _, child := range node.Children.Nodes {
		ticket, err := c.routed(ctx, child.ticket())
		if err != nil {
			return EpicDetail{}, fmt.Errorf("reading ticket #%d: %w", child.Number, err)
		}
		epic.Tickets = append(epic.Tickets, ticket)
	}
	return epic, nil
}

// CloseEpic closes an epic. Completed needs every ticket of the epic closed;
// not-planned leaves its open tickets as they are.
func (c *Client) CloseEpic(ctx context.Context, number int, reason string) error {
	if reason != ReasonCompleted && reason != ReasonNotPlanned {
		return fmt.Errorf("invalid reason %q: expected %s or %s", reason, ReasonCompleted, ReasonNotPlanned)
	}
	epic, err := c.readTicket(ctx, number)
	if err != nil {
		return err
	}
	if !epic.Epic() {
		return fmt.Errorf("ticket #%d is not an epic: use issue close", number)
	}
	if reason == ReasonCompleted && epic.OpenSubIssues > 0 {
		return fmt.Errorf("epic #%d has %d open ticket(s): close them first, or close the epic as %s", number, epic.OpenSubIssues, ReasonNotPlanned)
	}
	return c.closeTicket(ctx, epic, CloseOptions{Reason: reason})
}

// CloseEligibleEpic closes an epic whose tickets are all closed, as completed, and
// reports whether it did. The list that found the epic can be out of date, so this
// decides from a fresh read.
func (c *Client) CloseEligibleEpic(ctx context.Context, number int) (bool, error) {
	ticket, err := c.readTicket(ctx, number)
	if err != nil {
		return false, err
	}
	if !ticket.Closable() {
		return false, nil
	}
	return true, c.closeTicket(ctx, ticket, CloseOptions{Reason: ReasonCompleted})
}

type familyNode struct {
	ticketNode
	Children struct{ Nodes []ticketNode }
}

// PromoteEpic makes a ticket an epic. Its sub-issues become the epic's tickets, so
// none of them may be an epic, and the ticket may not belong to another ticket,
// because epics do not nest.
func (c *Client) PromoteEpic(ctx context.Context, number int) error {
	node, err := readIssue[familyNode](ctx, c, number, ticketFields+"\n  "+childFields)
	if err != nil {
		return err
	}
	ticket := node.ticket()
	switch {
	case ticket.Epic():
		return fmt.Errorf("ticket #%d is already an epic", number)
	case ticket.State != stateOpen:
		return fmt.Errorf("ticket #%d is closed: reopen it first", number)
	case ticket.Handoff():
		return fmt.Errorf("ticket #%d is a handoff", number)
	case ticket.Parent != 0:
		return fmt.Errorf("ticket #%d belongs to #%d: epics do not nest", number, ticket.Parent)
	}
	for _, child := range node.Children.Nodes {
		if child.Labels.has(labelEpic) {
			return fmt.Errorf("sub-issue #%d of #%d is an epic: epics do not nest", child.Number, number)
		}
	}
	// The read carries no board status for an unrouted ticket; a claim can exist
	// only on a routed one.
	routed, err := c.routed(ctx, ticket)
	if err != nil {
		return fmt.Errorf("reading ticket #%d: %w", number, err)
	}
	if routed.Status == StatusDoing {
		return fmt.Errorf("ticket #%d is claimed: release or close it first", number)
	}
	return c.addLabels(ctx, number, []string{labelEpic})
}

// DemoteEpic makes an epic an ordinary ticket. It must have no tickets left.
func (c *Client) DemoteEpic(ctx context.Context, number int) error {
	ticket, err := c.readTicket(ctx, number)
	if err != nil {
		return err
	}
	if !ticket.Epic() {
		return fmt.Errorf("ticket #%d is not an epic", number)
	}
	if ticket.SubIssues > 0 {
		return fmt.Errorf("epic #%d has %d ticket(s): detach them with epic rm first", number, ticket.SubIssues)
	}
	return c.removeLabels(ctx, number, []string{labelEpic})
}
