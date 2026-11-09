package tracker

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/alexgorbatchev/issue-tracker-cli/v2/internal/fakegithub"
)

// epicParent is a ticket's parent epic as ticketFields read it.
func epicParent(number int) string {
	return fmt.Sprintf(`{"number":%d,"state":"OPEN","blockedBy":{"nodes":[]},"labels":{"nodes":[{"name":"epic"}]},"parent":null}`, number)
}

// relativesRoute answers the read of tickets for epic checks with the given nodes,
// keyed by number.
func relativesRoute(nodes map[int]string) fakegithub.Route {
	parts := []string{`"isPrivate":true`}
	for number, node := range nodes {
		parts = append(parts, fmt.Sprintf(`"n%d":%s`, number, node))
	}
	return fakegithub.Route{Match: "parent { number }", Body: `{"data":{"repository":{` + strings.Join(parts, ",") + `}}}`}
}

func relativeJSON(epic bool, state string, parent int) string {
	labels := ""
	if epic {
		labels = `{"name":"epic"}`
	}
	parentJSON := "null"
	if parent != 0 {
		parentJSON = fmt.Sprintf(`{"number":%d}`, parent)
	}
	return fmt.Sprintf(`{"id":"ID%d","state":%q,"labels":{"nodes":[%s]},"parent":%s}`, parent, state, labels, parentJSON)
}

var (
	openEpic   = relativeJSON(true, "OPEN", 0)
	closedEpic = relativeJSON(true, "CLOSED", 0)
	loose      = `{"id":"LOOSE","state":"OPEN","labels":{"nodes":[]},"parent":null}`
)

func epicLabelRoute() fakegithub.Route {
	return fakegithub.Route{Match: "label(name: $label)", Body: `{"data":{"repository":{"id":"REPO","label":{"id":"LEPIC"}}}}`}
}

func TestEpicsAreNeverReady(t *testing.T) {
	route := listRoute(
		ticketSpec{number: 1, project: "go-tui", status: "Todo", epic: true},
		ticketSpec{number: 2, project: "go-tui", status: "Todo"},
	)
	c, _ := newClient(t, route)
	tickets, _, err := c.ListTickets(ctx, ListOptions{Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := numbers(tickets); !slices.Equal(got, []int{2}) {
		t.Fatalf("ready = %v, want the empty epic left out", got)
	}
}

func TestClaimRefusesAnEpic(t *testing.T) {
	c, gh := newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui", status: "Todo", epic: true}))
	_, err := c.ClaimTicket(ctx, 1)
	wantError(t, err, "ticket #1 is an epic: claim one of its tickets")
	wantCount(t, gh, 1)
}

func TestReadyTicketsOfAnActiveEpicComeFirstWithinAPriority(t *testing.T) {
	route := listRoute(
		ticketSpec{number: 1, project: "go-tui", status: "Todo", created: "2026-01-01T00:00:00Z", priorities: []string{"P1"}},
		ticketSpec{number: 2, project: "go-tui", status: "Todo", created: "2026-01-02T00:00:00Z", priorities: []string{"P1"}, parent: epicParent(10)},
		ticketSpec{number: 3, project: "go-tui", status: "Doing", created: "2026-01-03T00:00:00Z", parent: epicParent(10)},
		ticketSpec{number: 4, project: "go-tui", status: "Todo", created: "2026-01-04T00:00:00Z", priorities: []string{"P0"}},
		ticketSpec{number: 5, project: "go-tui", status: "Todo", created: "2026-01-05T00:00:00Z", parent: epicParent(11)},
		ticketSpec{number: 6, project: "go-tui", status: "Todo", created: "2026-01-06T00:00:00Z", parent: epicParent(10)},
		ticketSpec{number: 7, project: "go-tui", status: "Todo", created: "2026-01-07T00:00:00Z", priorities: []string{"P1"}, parent: epicParent(10)},
	)
	c, gh := newClient(t, boardOnlyRoute(), route)
	tickets, _, err := c.ListTickets(ctx, ListOptions{Project: "go-tui", Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	// P0 first; then the P1 tickets of active epic 10 ahead of the older P1; then,
	// with no priority, epic 10's ticket ahead of idle epic 11's older one.
	if got := numbers(tickets); !slices.Equal(got, []int{4, 2, 7, 1, 6, 5}) {
		t.Fatalf("tickets = %v, want [4 2 7 1 6 5]", got)
	}
	wantCount(t, gh, 2)
}

func TestCreateEpic(t *testing.T) {
	relatives := relativesRoute(map[int]string{7: loose, 8: loose})
	c, gh := newClient(t, epicLabelRoute(), relatives, createRoute(), addToBoardRoute(), okRoute("updateProjectV2ItemFieldValue"))
	epic, err := c.CreateEpic(ctx, EpicOptions{Project: "go-tui", Title: "Big", Add: []int{7, 8, 7}})
	if err != nil {
		t.Fatal(err)
	}
	if epic.Number != 50 {
		t.Fatalf("epic = %+v", epic)
	}
	wantCount(t, gh, 5)
	input := gh.Requests()[2].Variables["input"].(map[string]any)
	if fmt.Sprint(input["labelIds"]) != "[L1 LEPIC]" {
		t.Fatalf("the create must carry the epic label: %v", input)
	}
	links := lastDescription(gh)
	if strings.Count(links, "addSubIssue") != 2 || !strings.Contains(links, `issueId: "NEW", subIssueId: "LOOSE"`) || strings.Contains(links, "replaceParent") {
		t.Fatalf("links = %s", links)
	}
}

func TestCreateEpicRefusesBeforeCreating(t *testing.T) {
	tests := []struct {
		name   string
		routes []fakegithub.Route
		opts   EpicOptions
		want   string
	}{
		{"reserved label", nil, EpicOptions{Project: "go-tui", Title: "t", Labels: []string{"handoff"}}, `label "handoff" makes a ticket a handoff`},
		{"label read fails", []fakegithub.Route{{Match: "label(name: $label)", Status: 500, Body: "{}"}}, EpicOptions{Project: "go-tui", Title: "t"}, "reading epic label"},
		{"nested epic", []fakegithub.Route{epicLabelRoute(), relativesRoute(map[int]string{7: openEpic})}, EpicOptions{Project: "go-tui", Title: "t", Add: []int{7}}, "ticket #7 is an epic: epics do not nest"},
		{"ticket with a parent", []fakegithub.Route{epicLabelRoute(), relativesRoute(map[int]string{7: relativeJSON(false, "OPEN", 3)})}, EpicOptions{Project: "go-tui", Title: "t", Add: []int{7}}, "ticket #7 already belongs to #3: create the epic without it, then run epic add --move"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, gh := newClient(t, tt.routes...)
			_, err := c.CreateEpic(ctx, tt.opts)
			wantError(t, err, tt.want)
			for _, req := range gh.Requests() {
				if strings.Contains(req.Description, "createIssue") {
					t.Fatal("nothing may be created")
				}
			}
		})
	}
}

func TestCreateEpicCreatesItsLabel(t *testing.T) {
	missing := fakegithub.Route{Match: "label(name: $label)", Body: `{"data":{"repository":{"id":"REPO","label":null}}}`}
	created := fakegithub.Route{Match: "createLabel(input", Body: `{"data":{"createLabel":{"label":{"id":"LNEW"}}}}`}
	c, gh := newClient(t, missing, created, createRoute(), addToBoardRoute(), okRoute("updateProjectV2ItemFieldValue"))
	if _, err := c.CreateEpic(ctx, EpicOptions{Project: "go-tui", Title: "Big"}); err != nil {
		t.Fatal(err)
	}
	if vars := gh.Requests()[1].Variables; vars["name"] != "epic" || vars["color"] != epicLabelColor {
		t.Fatalf("label = %v", vars)
	}
	if input := gh.Requests()[2].Variables["input"].(map[string]any); fmt.Sprint(input["labelIds"]) != "[L1 LNEW]" {
		t.Fatalf("input = %v", input)
	}
}

func TestCreateTicketInAnEpicNeedsAnOpenEpic(t *testing.T) {
	tests := []struct {
		name string
		node string
		want string
	}{
		{"not an epic", loose, "ticket #3 is not an epic: run epic promote 3 first"},
		{"closed epic", closedEpic, "epic #3 is closed: reopen it first"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, gh := newClient(t, relativesRoute(map[int]string{3: tt.node}))
			_, err := c.CreateTicket(ctx, CreateOptions{Project: "go-tui", Title: "t", Epic: 3})
			wantError(t, err, tt.want)
			wantCount(t, gh, 1)
		})
	}
}

func TestAddToEpic(t *testing.T) {
	nodes := map[int]string{1: openEpic, 2: loose, 3: relativeJSON(false, "OPEN", 1), 4: relativeJSON(false, "OPEN", 9)}
	c, gh := newClient(t, relativesRoute(nodes), okRoute("addSubIssue"))
	added, err := c.AddToEpic(ctx, 1, []int{2, 3, 4, 2}, true)
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Fatalf("added = %d, want the ticket already in the epic skipped", added)
	}
	wantCount(t, gh, 2)
	write := lastDescription(gh)
	if !strings.Contains(write, `subIssueId: "LOOSE" }`) && !strings.Contains(write, `subIssueId: "LOOSE"})`) {
		t.Fatalf("write = %s", write)
	}
	if !strings.Contains(write, `subIssueId: "ID9", replaceParent: true`) {
		t.Fatalf("a moved ticket must replace its parent: %s", write)
	}
}

func TestAddToEpicWritesNothingWhenEverythingIsThere(t *testing.T) {
	c, gh := newClient(t, relativesRoute(map[int]string{1: openEpic, 3: relativeJSON(false, "OPEN", 1)}))
	added, err := c.AddToEpic(ctx, 1, []int{3}, false)
	if err != nil || added != 0 {
		t.Fatalf("added = %d, err = %v", added, err)
	}
	wantCount(t, gh, 1)
}

func TestAddToEpicFailures(t *testing.T) {
	tests := []struct {
		name    string
		nodes   map[int]string
		tickets []int
		write   bool
		want    string
	}{
		{"not an epic", map[int]string{1: loose, 2: loose}, []int{2}, false, "ticket #1 is not an epic: run epic promote 1 first"},
		{"closed epic", map[int]string{1: closedEpic, 2: loose}, []int{2}, false, "epic #1 is closed"},
		{"itself", map[int]string{1: openEpic}, []int{1}, false, "epic #1 cannot contain itself"},
		{"nested", map[int]string{1: openEpic, 2: openEpic}, []int{2}, false, "ticket #2 is an epic: epics do not nest"},
		{"other parent", map[int]string{1: openEpic, 2: relativeJSON(false, "OPEN", 9)}, []int{2}, false, "ticket #2 already belongs to #9: pass --move to move it"},
		{"write fails", map[int]string{1: openEpic, 2: loose}, []int{2}, true, "adding tickets to epic #1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routes := []fakegithub.Route{relativesRoute(tt.nodes)}
			if tt.write {
				routes = append(routes, fakegithub.Route{Match: "addSubIssue", Status: 500, Body: "{}"})
			}
			c, _ := newClient(t, routes...)
			_, err := c.AddToEpic(ctx, 1, tt.tickets, false)
			wantError(t, err, tt.want)
		})
	}
	c, _ := newClient(t, fakegithub.Route{Match: "parent { number }", Status: 500, Body: "{}"})
	_, err := c.AddToEpic(ctx, 1, []int{2}, false)
	wantError(t, err, "reading tickets")
}

func TestRemoveFromEpic(t *testing.T) {
	c, gh := newClient(t, relativesRoute(map[int]string{1: openEpic, 2: relativeJSON(false, "OPEN", 1)}), okRoute("removeSubIssue"))
	if err := c.RemoveFromEpic(ctx, 1, []int{2}); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 2)
	if write := lastDescription(gh); !strings.Contains(write, `removeSubIssue(input: {issueId: "ID0", subIssueId: "ID1"})`) {
		t.Fatalf("write = %s", write)
	}
	tests := []struct {
		name   string
		routes []fakegithub.Route
		want   string
	}{
		{"read fails", []fakegithub.Route{{Match: "parent { number }", Status: 500, Body: "{}"}}, "reading tickets"},
		{"not an epic", []fakegithub.Route{relativesRoute(map[int]string{1: loose, 2: loose})}, "ticket #1 is not an epic"},
		{"not in the epic", []fakegithub.Route{relativesRoute(map[int]string{1: openEpic, 2: loose})}, "ticket #2 is not in epic #1"},
		{"write fails", []fakegithub.Route{relativesRoute(map[int]string{1: openEpic, 2: relativeJSON(false, "OPEN", 1)}), {Match: "removeSubIssue", Status: 500, Body: "{}"}}, "removing tickets from epic #1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newClient(t, tt.routes...)
			wantError(t, c.RemoveFromEpic(ctx, 1, []int{2}), tt.want)
		})
	}
}

// epicDetailRoute answers an epic inspect with the epic and its tickets' nodes.
func epicDetailRoute(epic ticketSpec, children ...ticketSpec) fakegithub.Route {
	nodes := make([]string, 0, len(children))
	for _, child := range children {
		nodes = append(nodes, child.json()+"}")
	}
	body := epic.json() + `,"body":"Plan","blocking":{"nodes":[]},"subIssues":{"nodes":[]},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false}},"children":{"nodes":[` + strings.Join(nodes, ",") + `]}}`
	return fakegithub.Route{Match: "children: subIssues(first: 100)", Body: `{"data":{"repository":{"isPrivate":true,"issue":` + body + `}}}`}
}

func TestInspectEpic(t *testing.T) {
	route := epicDetailRoute(ticketSpec{number: 1, project: "go-tui", status: "Todo", epic: true},
		ticketSpec{number: 2, project: "lib", status: "Doing", board: "B2"},
		ticketSpec{number: 3, project: "go-tui", status: "Done", state: "CLOSED"},
	)
	c, gh := newClient(t, route)
	epic, err := c.InspectEpic(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if epic.Body != "Plan" || len(epic.Tickets) != 2 || epic.Tickets[0].Project != "lib" || epic.Tickets[0].Status != StatusDoing || epic.Done() != 1 {
		t.Fatalf("epic = %+v", epic)
	}
	wantCount(t, gh, 1)

	c, _ = newClient(t, epicDetailRoute(ticketSpec{number: 1, project: "go-tui", status: "Todo"}))
	_, err = c.InspectEpic(ctx, 1)
	wantError(t, err, "ticket #1 is not an epic: run issue inspect 1")

	c, _ = newClient(t, fakegithub.Route{Match: "children: subIssues", Body: notFoundJSON})
	_, err = c.InspectEpic(ctx, 1)
	wantError(t, err, "ticket #1 not found")
}

func TestCloseEpic(t *testing.T) {
	tests := []struct {
		name   string
		spec   ticketSpec
		reason string
		want   string
	}{
		{"completed", ticketSpec{number: 1, project: "go-tui", status: "Todo", epic: true, subs: closedSub}, ReasonCompleted, ""},
		{"not planned with open tickets", ticketSpec{number: 1, project: "go-tui", status: "Todo", epic: true, subs: openSub}, ReasonNotPlanned, ""},
		{"completed with open tickets", ticketSpec{number: 1, project: "go-tui", status: "Todo", epic: true, subs: openSub + "," + closedSub}, ReasonCompleted, "epic #1 has 1 open ticket(s): close them first, or close the epic as not-planned"},
		{"not an epic", ticketSpec{number: 1, project: "go-tui", status: "Todo"}, ReasonCompleted, "ticket #1 is not an epic: use issue close"},
		{"duplicate", ticketSpec{number: 1, epic: true}, ReasonDuplicate, `invalid reason "duplicate"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, gh := newClient(t, ticketRoute(tt.spec), okRoute("closeIssue"))
			err := c.CloseEpic(ctx, 1, tt.reason)
			if tt.want != "" {
				wantError(t, err, tt.want)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if write := lastDescription(gh); !strings.Contains(write, "closeIssue") {
				t.Fatalf("write = %s", write)
			}
		})
	}
	c, _ := newClient(t, fakegithub.Route{Match: "isPrivate issue(number: $number)", Body: notFoundJSON})
	wantError(t, c.CloseEpic(ctx, 1, ReasonCompleted), "ticket #1 not found")
}

func TestCloseEligibleEpic(t *testing.T) {
	c, gh := newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui", status: "Todo", epic: true, subs: closedSub}), okRoute("closeIssue"))
	closed, err := c.CloseEligibleEpic(ctx, 1)
	if err != nil || !closed {
		t.Fatalf("closed = %v, err = %v", closed, err)
	}
	wantCount(t, gh, 2)
	c, gh = newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui", status: "Todo", subs: closedSub}))
	if closed, err = c.CloseEligibleEpic(ctx, 1); err != nil || closed {
		t.Fatalf("a parent that is not an epic stays open: closed = %v, err = %v", closed, err)
	}
	wantCount(t, gh, 1)
	c, _ = newClient(t, fakegithub.Route{Match: "isPrivate issue(number: $number)", Body: notFoundJSON})
	_, err = c.CloseEligibleEpic(ctx, 1)
	wantError(t, err, "ticket #1 not found")
}

// closeRoute answers the read of a ticket that issue close makes, with its parent
// epic in full.
func closeRoute(ticket ticketSpec, epic *ticketSpec) fakegithub.Route {
	parent := "null"
	if epic != nil {
		parent = epic.json() + "}"
	}
	return fakegithub.Route{Match: "epicParent: parent", Body: `{"data":{"repository":{"isPrivate":true,"issue":` + ticket.json() + `,"epicParent":` + parent + `}}}}`}
}

func TestClosingTheLastTicketClosesItsEpic(t *testing.T) {
	child := ticketSpec{number: 2, project: "go-tui", status: "Todo", parent: epicParent(1)}
	epic := func(subs string, extra ...func(*ticketSpec)) *ticketSpec {
		spec := ticketSpec{number: 1, project: "go-tui", status: "Todo", epic: true, subs: subs}
		for _, change := range extra {
			change(&spec)
		}
		return &spec
	}
	tests := []struct {
		name   string
		ticket ticketSpec
		epic   *ticketSpec
		closes int
	}{
		{"last open ticket", child, epic(openSub + "," + closedSub), 2},
		{"another ticket open", child, epic(openSub + "," + openSub), 1},
		{"blocked epic", child, epic(openSub, func(s *ticketSpec) { s.blockers = `{"number":9,"state":"OPEN"}` }), 1},
		{"parent is not an epic", child, epic(openSub, func(s *ticketSpec) { s.epic = false }), 1},
		{"no parent", ticketSpec{number: 2, project: "go-tui", status: "Todo"}, nil, 1},
		{"already closed ticket", ticketSpec{number: 2, project: "go-tui", status: "Done", state: "CLOSED", parent: epicParent(1)}, epic(closedSub), 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, gh := newClient(t, closeRoute(tt.ticket, tt.epic), okRoute("closeIssue"))
			closed, err := c.CloseTicket(ctx, 2, CloseOptions{Reason: ReasonCompleted})
			if err != nil {
				t.Fatal(err)
			}
			if (closed.Number == 1) != (tt.closes == 2) {
				t.Fatalf("closed epic = %+v", closed)
			}
			wantCount(t, gh, tt.closes+1)
			if tt.closes == 2 && !strings.Contains(lastDescription(gh), `closeIssue(input: {issueId: "I1", stateReason: COMPLETED})`) {
				t.Fatalf("epic close = %s", lastDescription(gh))
			}
		})
	}
}

func TestCloseTicketReportsAFailedEpicClose(t *testing.T) {
	child := ticketSpec{number: 2, project: "go-tui", status: "Todo", parent: epicParent(1)}
	epic := ticketSpec{number: 1, project: "go-tui", status: "Todo", epic: true, subs: openSub}
	childClose := fakegithub.Route{Match: `issueId: "I2"`, Body: `{"data":{}}`}
	epicClose := fakegithub.Route{Match: `issueId: "I1"`, Status: 500, Body: "{}"}
	c, _ := newClient(t, closeRoute(child, &epic), childClose, epicClose)
	_, err := c.CloseTicket(ctx, 2, CloseOptions{Reason: ReasonCompleted})
	wantError(t, err, "closed #2, but closing its epic #1 failed")
}

func TestIssueCloseRefusesAnEpic(t *testing.T) {
	c, gh := newClient(t, closeRoute(ticketSpec{number: 1, project: "go-tui", status: "Todo", epic: true}, nil))
	_, err := c.CloseTicket(ctx, 1, CloseOptions{Reason: ReasonCompleted})
	wantError(t, err, "ticket #1 is an epic: use epic close")
	wantCount(t, gh, 1)
}

// familyRoute answers the read epic promote makes, with the ticket's sub-issues.
func familyRoute(spec ticketSpec, children ...ticketSpec) fakegithub.Route {
	route := epicDetailRoute(spec, children...)
	route.Body = strings.Replace(route.Body, `,"body":"Plan","blocking":{"nodes":[]},"subIssues":{"nodes":[]},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false}}`, "", 1)
	return route
}

func TestPromoteEpic(t *testing.T) {
	labelPost := fakegithub.Route{Match: "POST api.github.com/repos/o/tracker/issues/1/labels", Body: "[]"}
	c, gh := newClient(t, familyRoute(ticketSpec{number: 1, project: "go-tui", status: "Todo", subs: openSub}, ticketSpec{number: 2}), labelPost)
	if err := c.PromoteEpic(ctx, 1); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 2)
	if body := gh.Requests()[1].Body; body != `{"labels":["epic"]}` {
		t.Fatalf("label body = %s", body)
	}
	tests := []struct {
		name     string
		spec     ticketSpec
		children []ticketSpec
		want     string
	}{
		{"already an epic", ticketSpec{number: 1, project: "go-tui", status: "Todo", epic: true}, nil, "ticket #1 is already an epic"},
		{"closed", ticketSpec{number: 1, project: "go-tui", status: "Done", state: "CLOSED"}, nil, "ticket #1 is closed: reopen it first"},
		{"handoff", ticketSpec{number: 1, project: "go-tui", status: "Todo", handoff: true}, nil, "ticket #1 is a handoff"},
		{"has a parent", ticketSpec{number: 1, project: "go-tui", status: "Todo", parent: epicParent(5)}, nil, "ticket #1 belongs to #5: epics do not nest"},
		{"epic sub-issue", ticketSpec{number: 1, project: "go-tui", status: "Todo"}, []ticketSpec{{number: 2, epic: true}}, "sub-issue #2 of #1 is an epic: epics do not nest"},
		{"claimed", ticketSpec{number: 1, project: "go-tui", status: "Doing"}, nil, "ticket #1 is claimed: release or close it first"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, gh := newClient(t, familyRoute(tt.spec, tt.children...))
			wantError(t, c.PromoteEpic(ctx, 1), tt.want)
			wantCount(t, gh, 1)
		})
	}
	c, _ = newClient(t, fakegithub.Route{Match: "children: subIssues", Body: notFoundJSON})
	wantError(t, c.PromoteEpic(ctx, 1), "ticket #1 not found")
}

func TestDemoteEpic(t *testing.T) {
	labelDelete := fakegithub.Route{Match: "DELETE api.github.com/repos/o/tracker/issues/1/labels/epic", Body: "[]"}
	c, gh := newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui", status: "Todo", epic: true}), labelDelete)
	if err := c.DemoteEpic(ctx, 1); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 2)
	tests := []struct {
		name string
		spec ticketSpec
		want string
	}{
		{"not an epic", ticketSpec{number: 1, project: "go-tui", status: "Todo"}, "ticket #1 is not an epic"},
		{"has tickets", ticketSpec{number: 1, project: "go-tui", status: "Todo", epic: true, subs: closedSub}, "epic #1 has 1 ticket(s): detach them with epic rm first"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, gh := newClient(t, ticketRoute(tt.spec))
			wantError(t, c.DemoteEpic(ctx, 1), tt.want)
			wantCount(t, gh, 1)
		})
	}
	c, _ = newClient(t, fakegithub.Route{Match: "isPrivate issue(number: $number)", Body: notFoundJSON})
	wantError(t, c.DemoteEpic(ctx, 1), "ticket #1 not found")
}

func TestReservedLabelsKeepTheirCommands(t *testing.T) {
	c, gh := newClient(t)
	wantError(t, c.AddLabels(ctx, 1, []string{"Epic"}), `label "Epic" makes a ticket an epic: use epic create, epic promote, or epic demote instead`)
	wantError(t, c.RemoveLabels(ctx, 1, []string{"epic"}), "makes a ticket an epic")
	wantError(t, c.AddLabels(ctx, 1, []string{"HANDOFF"}), `label "HANDOFF" makes a ticket a handoff: use handoff create instead`)
	_, err := c.CreateTicket(ctx, CreateOptions{Project: "go-tui", Title: "t", Labels: []string{"epic"}})
	wantError(t, err, "makes a ticket an epic")
	wantCount(t, gh, 0)
}

func TestInspectShowsAnEpicParent(t *testing.T) {
	c, _ := newClient(t, ticketRoute(ticketSpec{number: 2, project: "go-tui", status: "Todo", parent: epicParent(1)}))
	ticket, err := c.readTicket(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Parent != 1 || !ticket.ParentEpic {
		t.Fatalf("ticket = %+v", ticket)
	}
}
