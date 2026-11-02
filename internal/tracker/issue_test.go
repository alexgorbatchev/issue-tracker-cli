package tracker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexgorbatchev/issue-tracker-cli/internal/fakegithub"
)

var ctx = context.Background()

const (
	openSub   = `{"state":"OPEN"}`
	closedSub = `{"state":"CLOSED"}`
)

func TestClosable(t *testing.T) {
	tests := []struct {
		name string
		spec ticketSpec
		want bool
	}{
		{"every sub-issue closed", ticketSpec{number: 1, status: "Todo", subs: closedSub + "," + closedSub}, true},
		{"one sub-issue open", ticketSpec{number: 1, status: "Todo", subs: closedSub + "," + openSub}, false},
		{"no sub-issues", ticketSpec{number: 1, status: "Todo"}, false},
		{"claimed", ticketSpec{number: 1, status: "Doing", subs: closedSub}, false},
		{"already closed", ticketSpec{number: 1, state: "CLOSED", subs: closedSub}, false},
		{"open blocker", ticketSpec{number: 1, status: "Todo", subs: closedSub, blockers: `{"number":9,"state":"OPEN"}`}, false},
		{"labelled blocked", ticketSpec{number: 1, status: "Todo", subs: closedSub, label: "blocked"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newClient(t, ticketRoute(tt.spec))
			ticket, err := c.readTicket(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			if ticket.Closable() != tt.want {
				t.Fatalf("Closable() = %v, ticket = %+v", ticket.Closable(), ticket)
			}
		})
	}
}

func TestListClosableKeepsOnlyParents(t *testing.T) {
	first := listRoute(ticketSpec{number: 1, status: "Todo"}, ticketSpec{number: 2, status: "Todo", subs: closedSub})
	c, gh := newClient(t, first)
	tickets, total, err := c.ListTickets(ctx, ListOptions{Closable: true, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(tickets) != 1 || tickets[0].Number != 2 || total != 1 {
		t.Fatalf("tickets = %+v, total = %d", tickets, total)
	}
	wantCount(t, gh, 1)
}

func subIssuesRoute(body string) fakegithub.Route {
	return fakegithub.Route{Match: "subIssues(first: 100) { nodes { number title state } }", Body: body}
}

func TestListSubIssues(t *testing.T) {
	body := `{"data":{"repository":{"isPrivate":true,"issue":{"subIssues":{"nodes":[{"number":3,"title":"A","state":"CLOSED"},{"number":4,"title":"B","state":"OPEN"}]}}}}}`
	c, gh := newClient(t, subIssuesRoute(body))
	subs, err := c.ListSubIssues(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 2 || subs[0].Number != 3 || subs[1].State != "OPEN" {
		t.Fatalf("subs = %+v", subs)
	}
	wantCount(t, gh, 1)
}

func TestListSubIssuesFailures(t *testing.T) {
	public := `{"data":{"repository":{"isPrivate":false,"issue":{"subIssues":{"nodes":[]}}}}}`
	tests := []struct {
		name  string
		route fakegithub.Route
		want  string
	}{
		{"not found", subIssuesRoute(notFoundJSON), "ticket #1 not found"},
		{"no issue", subIssuesRoute(`{"data":{"repository":{"isPrivate":true,"issue":null}}}`), "ticket #1 not found"},
		{"server error", fakegithub.Route{Match: "subIssues(first: 100)", Status: 500, Body: "{}"}, "reading sub-issues of #1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newClient(t, tt.route)
			_, err := c.ListSubIssues(ctx, 1)
			wantError(t, err, tt.want)
		})
	}
	gh := fakegithub.New(subIssuesRoute(public))
	c, err := New(Options{Repo: Repo{Owner: "o", Name: "tracker"}, CacheDir: t.TempDir(), Transport: gh, Token: "tok", RequirePrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ListSubIssues(ctx, 1)
	wantError(t, err, "not private")
}

func TestReady(t *testing.T) {
	openBlocker := `{"number":9,"state":"OPEN"}`
	blockedParent := `{"number":5,"state":"OPEN","blockedBy":{"nodes":[{"state":"OPEN"}]},"parent":null}`
	blockedGrandparent := `{"number":5,"state":"OPEN","blockedBy":{"nodes":[]},"parent":{"number":4,"state":"OPEN","blockedBy":{"nodes":[{"state":"OPEN"}]}}}`
	openParent := `{"number":5,"state":"OPEN","blockedBy":{"nodes":[]},"parent":null}`
	closedParent := `{"number":5,"state":"CLOSED","blockedBy":{"nodes":[{"state":"OPEN"}]},"parent":null}`
	tests := []struct {
		name string
		spec ticketSpec
		want bool
	}{
		{"open with no status", ticketSpec{number: 1}, true},
		{"todo", ticketSpec{number: 1, status: "Todo"}, true},
		{"closed", ticketSpec{number: 1, state: "CLOSED"}, false},
		{"doing", ticketSpec{number: 1, status: "Doing"}, false},
		{"done on the board", ticketSpec{number: 1, status: "Done"}, false},
		{"open blocker", ticketSpec{number: 1, blockers: openBlocker}, false},
		{"closed blocker", ticketSpec{number: 1, blockers: `{"number":9,"state":"CLOSED"}`}, true},
		{"blocked parent", ticketSpec{number: 1, parent: blockedParent}, false},
		{"blocked grandparent", ticketSpec{number: 1, parent: blockedGrandparent}, false},
		{"unblocked parent", ticketSpec{number: 1, parent: openParent}, true},
		{"closed parent keeps child ready", ticketSpec{number: 1, parent: closedParent}, true},
		{"labelled blocked", ticketSpec{number: 1, status: "Todo", label: "blocked"}, false},
		{"labelled blocked in another case", ticketSpec{number: 1, status: "Todo", label: "Blocked"}, false},
		{"unrelated label", ticketSpec{number: 1, status: "Todo", label: "blocked-upstream"}, true},
		{"open sub-issue", ticketSpec{number: 1, status: "Todo", subs: openSub + "," + closedSub}, false},
		{"all sub-issues closed", ticketSpec{number: 1, status: "Todo", subs: closedSub}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newClient(t, ticketRoute(tt.spec))
			ticket, err := c.readTicket(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			if ticket.Ready() != tt.want {
				t.Fatalf("Ready() = %v, want %v: %+v", ticket.Ready(), tt.want, ticket)
			}
		})
	}
}

func TestStale(t *testing.T) {
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		ticket Ticket
		want   bool
	}{
		{"old doing", Ticket{State: stateOpen, Status: StatusDoing, Activity: now.AddDate(0, 0, -4)}, true},
		{"recent doing", Ticket{State: stateOpen, Status: StatusDoing, Activity: now.AddDate(0, 0, -2)}, false},
		{"old todo", Ticket{State: stateOpen, Status: StatusTodo, Activity: now.AddDate(0, 0, -30)}, false},
		{"old closed", Ticket{State: StateClosed, Status: StatusDoing, Activity: now.AddDate(0, 0, -30)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ticket.Stale(3, now); got != tt.want {
				t.Fatalf("Stale() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBoardActivityCountsTowardStaleness(t *testing.T) {
	body := ticketSpec{number: 1, status: "Doing", updated: "2026-01-01T00:00:00Z"}.json()
	body = strings.Replace(body, `"id":"ITEM1","updatedAt":"2026-01-01T00:00:00Z"`, `"id":"ITEM1","updatedAt":"2026-06-01T00:00:00Z"`, 1)
	c, _ := newClient(t, fakegithub.Route{Match: "isPrivate issue(number: $number)", Body: `{"data":{"repository":{"isPrivate":true,"issue":` + body + `}}}}`})
	ticket, err := c.readTicket(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Activity.Month() != time.June {
		t.Fatalf("activity = %v, want the later board update", ticket.Activity)
	}
}

func TestListTicketsFilters(t *testing.T) {
	specs := []ticketSpec{
		{number: 1, project: "go-tui", status: "Todo"},
		{number: 2, project: "go-tui", status: "Doing", updated: "2026-01-01T00:00:00Z"},
		{number: 3, project: "go-tui", status: "Doing", updated: "2026-10-30T00:00:00Z"},
		{number: 4, project: "go-tui", status: "Todo", blockers: `{"number":1,"state":"OPEN"}`},
	}
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		opts ListOptions
		want string
	}{
		{"all", ListOptions{}, "[1 2 3 4]"},
		{"ready", ListOptions{Ready: true}, "[1]"},
		{"status ignores case", ListOptions{Status: "doing"}, "[2 3]"},
		{"stale", ListOptions{Stale: true, StaleDays: 3, Now: now}, "[2]"},
		{"limit", ListOptions{Limit: 2}, "[1 2]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, gh := newClient(t, listRoute(specs...))
			tickets, _, err := c.ListTickets(ctx, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			numbers := make([]int, 0, len(tickets))
			for _, ticket := range tickets {
				numbers = append(numbers, ticket.Number)
			}
			if got := fmt.Sprint(numbers); got != tt.want {
				t.Fatalf("tickets = %s, want %s", got, tt.want)
			}
			wantCount(t, gh, 1)
		})
	}
}

func TestListTicketsScopesAndPages(t *testing.T) {
	first := listRoute(ticketSpec{number: 1, project: "go-tui"})
	first.Body = strings.Replace(first.Body, `"hasNextPage":false,"endCursor":""`, `"hasNextPage":true,"endCursor":"c1"`, 1)
	first.Times = 1
	c, gh := newClient(t, first, listRoute(ticketSpec{number: 2, project: "go-tui"}))
	tickets, _, err := c.ListTickets(ctx, ListOptions{Project: "go-tui", State: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tickets) != 2 || tickets[0].Project != "go-tui" {
		t.Fatalf("tickets = %+v", tickets)
	}
	requests := gh.Requests()
	if requests[0].Variables["labels"].([]any)[0] != "repo:go-tui" {
		t.Fatalf("labels = %v", requests[0].Variables["labels"])
	}
	if len(requests[0].Variables["states"].([]any)) != 2 || requests[1].Variables["after"] != "c1" {
		t.Fatalf("variables = %v / %v", requests[0].Variables, requests[1].Variables)
	}
}

func TestListTicketsTakesTheSearchTotalFromTheFirstPage(t *testing.T) {
	page := fmt.Sprintf(`{"data":{"repository":{"isPrivate":true},"search":{"issueCount":40,"nodes":[%s}],"pageInfo":{"hasNextPage":true,"endCursor":"c1"}}}}`,
		ticketSpec{number: 1, project: "go-tui", status: "Todo"}.json())
	c, gh := newClient(t, fakegithub.Route{Match: "search(query: $search", Body: page})
	tickets, total, err := c.ListTickets(ctx, ListOptions{Search: "crash", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(tickets) != 1 || total != 40 {
		t.Fatalf("tickets = %d, total = %d", len(tickets), total)
	}
	wantCount(t, gh, 1)
}

func TestListTicketsRejectsANegativeLimit(t *testing.T) {
	c, gh := newClient(t)
	_, _, err := c.ListTickets(ctx, ListOptions{Limit: -1})
	wantError(t, err, "invalid limit -1: expected 0 or more")
	wantCount(t, gh, 0)
}

func TestListTicketsSearchPages(t *testing.T) {
	page := func(cursor string, next bool) string {
		return fmt.Sprintf(`{"data":{"repository":{"isPrivate":true},"search":{"nodes":[%s}],"pageInfo":{"hasNextPage":%v,"endCursor":%q}}}}`,
			ticketSpec{number: 1, project: "go-tui", status: "Todo"}.json(), next, cursor)
	}
	c, gh := newClient(t, fakegithub.Route{Match: "search(query: $search", Body: page("c1", true), Times: 1}, fakegithub.Route{Match: "search(query: $search", Body: page("", false)})
	tickets, _, err := c.ListTickets(ctx, ListOptions{Search: "crash", State: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tickets) != 2 {
		t.Fatalf("tickets = %+v", tickets)
	}
	requests := gh.Requests()
	if requests[0].Variables["search"] != "repo:o/tracker is:issue sort:created-asc crash" || requests[1].Variables["after"] != "c1" {
		t.Fatalf("variables = %v / %v", requests[0].Variables, requests[1].Variables)
	}

	failing, _ := newClient(t, fakegithub.Route{Match: "search(query", Status: 500, Body: "{}"})
	_, _, err = failing.ListTickets(ctx, ListOptions{Search: "crash"})
	wantError(t, err, "listing tickets")
}

func TestLabels(t *testing.T) {
	c, gh := newClient(t,
		fakegithub.Route{Match: "POST api.github.com/repos/o/tracker/issues/3/labels", Body: "[]"},
		fakegithub.Route{Match: "DELETE api.github.com/repos/o/tracker/issues/3/labels/needs%20info", Body: "[]"},
		fakegithub.Route{Match: "DELETE api.github.com/repos/o/tracker/issues/3/labels/bug", Body: "[]"})
	if err := c.AddLabels(ctx, 3, []string{"blocked"}); err != nil {
		t.Fatal(err)
	}
	if body := gh.Requests()[0].Body; body != `{"labels":["blocked"]}` {
		t.Fatalf("body = %s", body)
	}
	if err := c.RemoveLabels(ctx, 3, []string{"bug"}); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 2)
	wantError(t, c.AddLabels(ctx, 3, []string{"repo:lib"}), "use issue move")
	wantError(t, c.RemoveLabels(ctx, 3, []string{"repo:lib"}), "use issue move")
	wantCount(t, gh, 2)

	failing, _ := newClient(t, fakegithub.Route{Match: "issues/3/labels", Status: 500, Body: "{}"})
	wantError(t, failing.AddLabels(ctx, 3, []string{"x"}), "labeling ticket")
	wantError(t, failing.RemoveLabels(ctx, 3, []string{"x"}), "removing label")
}

func TestCreateTicketLabels(t *testing.T) {
	c, gh := newClient(t, createRoute(), addToBoardRoute(), okRoute("updateProjectV2ItemFieldValue"), fakegithub.Route{Match: "POST api.github.com/repos/o/tracker/issues/50/labels", Body: "[]"})
	if _, err := c.CreateTicket(ctx, CreateOptions{Project: "go-tui", Title: "t", Labels: []string{"bug", "needs info"}}); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 4)
	if body := gh.Requests()[3].Body; body != `{"labels":["bug","needs info"]}` {
		t.Fatalf("body = %s", body)
	}
	_, err := c.CreateTicket(ctx, CreateOptions{Project: "go-tui", Title: "t", Labels: []string{"repo:lib"}})
	wantError(t, err, "use issue move")
	wantCount(t, gh, 4)
}

func TestTicketKeepsPlainLabels(t *testing.T) {
	c, _ := newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui"}))
	ticket, err := c.readTicket(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Project != "go-tui" || fmt.Sprint(ticket.Labels) != "[bug]" {
		t.Fatalf("ticket = %+v", ticket)
	}
}

func TestListTicketsErrors(t *testing.T) {
	c, _ := newClient(t)
	_, _, err := c.ListTickets(ctx, ListOptions{State: "paused"})
	wantError(t, err, "invalid state")

	closed, gh := newClient(t, listRoute())
	if _, _, err := closed.ListTickets(ctx, ListOptions{State: "closed"}); err != nil {
		t.Fatal(err)
	}
	if got := gh.Requests()[0].Variables["states"].([]any)[0]; got != "CLOSED" {
		t.Fatalf("states = %v", got)
	}

	public := listRoute()
	public.Body = strings.Replace(public.Body, `"isPrivate":true`, `"isPrivate":false`, 1)
	gh = fakegithub.New(public)
	c = clientWith(t, gh, testMetadata())
	c.requirePrivate = true
	_, _, err = c.ListTickets(ctx, ListOptions{})
	wantError(t, err, "not private")

	failing, _ := newClient(t, fakegithub.Route{Match: "issues(first", Status: 500, Body: "{}"})
	_, _, err = failing.ListTickets(ctx, ListOptions{})
	wantError(t, err, "listing tickets")
}

func inspectRoute(extra string) fakegithub.Route {
	base := ticketSpec{number: 7, project: "lib", status: "Todo"}.json()
	return fakegithub.Route{Match: "subIssues(first", Body: `{"data":{"repository":{"isPrivate":true,"issue":` + base + `,` + extra + `}}}}`}
}

func TestInspectTicket(t *testing.T) {
	comments := `"comments":{"nodes":[{"author":{"login":"alex"},"createdAt":"2026-01-01T00:00:00Z","body":"see https://github.com/user-attachments/assets/aaa-1"}],"pageInfo":{"hasNextPage":true,"endCursor":"c1"}}`
	body := `"body":"![x](https://github.com/user-attachments/assets/aaa-1) ![y](https://github.com/user-attachments/files/9/report.pdf) https://evil.example/user-attachments/assets/z","blocking":{"nodes":[{"number":8,"state":"OPEN"},{"number":9,"state":"CLOSED"}]},"subIssues":{"nodes":[{"number":10,"title":"Child","state":"OPEN"}]}`
	more := `{"data":{"repository":{"issue":{"comments":{"nodes":[{"author":null,"createdAt":"2026-01-02T00:00:00Z","body":"ghost"}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}}`
	c, gh := newClient(t, inspectRoute(body+","+comments), fakegithub.Route{Match: "comments(first: 100, after: $after)", Body: more})
	detail, err := c.InspectTicket(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Comments) != 2 || detail.Comments[0].Author != "alex" || detail.Comments[1].Author != "" {
		t.Fatalf("comments = %+v", detail.Comments)
	}
	if want := []string{"https://github.com/user-attachments/assets/aaa-1", "https://github.com/user-attachments/files/9/report.pdf"}; fmt.Sprint(detail.Attachments) != fmt.Sprint(want) {
		t.Fatalf("attachments = %v; other hosts must never match", detail.Attachments)
	}
	if fmt.Sprint(detail.Blocking) != "[8]" || len(detail.SubIssues) != 1 || !detail.PublicRepo {
		t.Fatalf("detail = %+v", detail)
	}
	wantCount(t, gh, 2)
}

func TestInspectTicketErrors(t *testing.T) {
	c, _ := newClient(t, fakegithub.Route{Match: "subIssues(first", Body: notFoundJSON})
	_, err := c.InspectTicket(ctx, 7)
	wantError(t, err, "not found")

	c, _ = newClient(t, fakegithub.Route{Match: "subIssues(first", Status: 500, Body: "{}"})
	_, err = c.InspectTicket(ctx, 7)
	wantError(t, err, "reading ticket")

	route := inspectRoute(`"body":"","blocking":{"nodes":[]},"subIssues":{"nodes":[]},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}`)
	route.Body = strings.Replace(route.Body, `"isPrivate":true`, `"isPrivate":false`, 1)
	gh := fakegithub.New(route)
	c = clientWith(t, gh, testMetadata())
	c.requirePrivate = true
	_, err = c.InspectTicket(ctx, 7)
	wantError(t, err, "not private")

	route = inspectRoute(`"body":"","blocking":{"nodes":[]},"subIssues":{"nodes":[]},"comments":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"c"}}`)
	c, _ = newClient(t, route, fakegithub.Route{Match: "comments(first: 100, after: $after)", Status: 500, Body: "{}"})
	_, err = c.InspectTicket(ctx, 7)
	wantError(t, err, "reading comments")
}

func TestReadTicketErrors(t *testing.T) {
	c, _ := newClient(t, fakegithub.Route{Match: "isPrivate issue(number", Body: notFoundJSON})
	_, err := c.readTicket(ctx, 3)
	wantError(t, err, "ticket #3 not found")

	c, _ = newClient(t, fakegithub.Route{Match: "isPrivate issue(number", Status: 500, Body: "{}"})
	_, err = c.readTicket(ctx, 3)
	wantError(t, err, "reading ticket #3")

	route := ticketRoute(ticketSpec{number: 3})
	route.Body = strings.Replace(route.Body, `"isPrivate":true`, `"isPrivate":false`, 1)
	c, _ = newClient(t, route)
	c.requirePrivate = true
	_, err = c.readTicket(ctx, 3)
	wantError(t, err, "not private")
}

func createRoute() fakegithub.Route {
	return fakegithub.Route{Match: "createIssue(input", Body: `{"data":{"createIssue":{"issue":{"id":"NEW","number":50,"url":"https://t/50"}}}}`}
}

func boardCollisionRoute() fakegithub.Route {
	return fakegithub.Route{Match: "addProjectV2ItemById", Body: `{"errors":[{"message":"Content already exists in this project."}]}`}
}

func addToBoardRoute() fakegithub.Route {
	return fakegithub.Route{Match: "addProjectV2ItemById", Body: `{"data":{"addProjectV2ItemById":{"item":{"id":"ITEM50"}}}}`}
}

func TestCreateTicketAddsItToTheBoardItself(t *testing.T) {
	c, gh := newClient(t, createRoute(), addToBoardRoute(), okRoute("updateProjectV2ItemFieldValue"))
	ticket, err := c.CreateTicket(ctx, CreateOptions{Project: "go-tui", Title: "Crash", Body: "Details", DiscoveredFrom: 12})
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Number != 50 || ticket.Status != StatusTodo {
		t.Fatalf("ticket = %+v", ticket)
	}
	wantCount(t, gh, 3)
	input := gh.Requests()[0].Variables["input"].(map[string]any)
	if input["body"] != "Details\n\nDiscovered while working on #12" {
		t.Fatalf("body = %q", input["body"])
	}
	if _, racesTheBoardAdd := input["projectV2Ids"]; fmt.Sprint(input["labelIds"]) != "[L1]" || racesTheBoardAdd {
		t.Fatalf("input = %v", input)
	}
	add := gh.Requests()[1].Variables
	if add["board"] != "B1" || add["content"] != "NEW" {
		t.Fatalf("board add = %v", add)
	}
	if last := lastDescription(gh); !strings.Contains(last, `itemId: "ITEM50"`) || !strings.Contains(last, `singleSelectOptionId: "opt-todo"`) {
		t.Fatalf("status write = %s", last)
	}
}

func TestCreateTicketRelations(t *testing.T) {
	ids := fakegithub.Route{Match: "n1: issue(number: 1)", Body: `{"data":{"repository":{"isPrivate":true,"n1":{"id":"I1"},"n2":{"id":"I2"},"n3":{"id":"I3"}}}}`}
	c, gh := newClient(t, ids, createRoute(), addToBoardRoute(), okRoute("addBlockedBy"))
	if _, err := c.CreateTicket(ctx, CreateOptions{Project: "go-tui", Title: "t", DiscoveredFrom: 4, BlockedBy: []int{1}, Blocking: []int{2}, Parent: 3}); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 4)
	input := gh.Requests()[1].Variables["input"].(map[string]any)
	if input["parentIssueId"] != "I3" || input["body"] != "Discovered while working on #4" {
		t.Fatalf("input = %v", input)
	}
	links := lastDescription(gh)
	if !strings.Contains(links, `issueId: "NEW", blockingIssueId: "I1"`) || !strings.Contains(links, `issueId: "I2", blockingIssueId: "NEW"`) {
		t.Fatalf("links = %s", links)
	}
}

func TestCreateTicketReportsAFailedBoardAdd(t *testing.T) {
	c, _ := newClient(t, createRoute(), fakegithub.Route{Match: "addProjectV2ItemById", Status: 500, Body: "{}"})
	ticket, err := c.CreateTicket(ctx, CreateOptions{Project: "go-tui", Title: "t"})
	wantError(t, err, "ticket #50 was created but linking it failed: adding ticket to board")
	if ticket.Number != 50 {
		t.Fatalf("ticket = %+v", ticket)
	}
}

func TestCreateTicketContinuesWhenTheBoardAlreadyHasIt(t *testing.T) {
	const onBoard = `{"data":{"node":{"projectItems":{"nodes":[{"id":"OTHER","project":{"id":"B9"}},{"id":"ITEM50","project":{"id":"B1"}}]}}}}`
	const notYet = `{"data":{"node":{"projectItems":{"nodes":[{"id":"OTHER","project":{"id":"B9"}}]}}}}`
	tests := []struct {
		name  string
		reads []string
		want  int
	}{
		{"item readable at once", []string{onBoard}, 4},
		{"item readable after a delay", []string{notYet, onBoard}, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routes := []fakegithub.Route{createRoute(), boardCollisionRoute()}
			for _, body := range tt.reads {
				routes = append(routes, fakegithub.Route{Match: "projectItems(first: 100)", Body: body, Times: 1})
			}
			c, gh := newClient(t, append(routes, okRoute("updateProjectV2ItemFieldValue"))...)
			var waits int
			c.pause = func(context.Context, time.Duration) error {
				waits++
				return nil
			}
			if _, err := c.CreateTicket(ctx, CreateOptions{Project: "go-tui", Title: "t"}); err != nil {
				t.Fatal(err)
			}
			wantCount(t, gh, tt.want)
			if waits != len(tt.reads)-1 {
				t.Fatalf("waits = %d", waits)
			}
			if last := lastDescription(gh); !strings.Contains(last, `itemId: "ITEM50"`) {
				t.Fatalf("status write = %s", last)
			}
		})
	}
}

func TestCreateTicketGivesUpOnAnUnreadableBoardItem(t *testing.T) {
	missing := fakegithub.Route{Match: "projectItems(first: 100)", Body: `{"data":{"node":{"projectItems":{"nodes":[]}}}}`}
	c, gh := newClient(t, createRoute(), boardCollisionRoute(), missing)
	c.pause = func(context.Context, time.Duration) error { return nil }
	ticket, err := c.CreateTicket(ctx, CreateOptions{Project: "go-tui", Title: "t"})
	wantError(t, err, "ticket #50 was created but linking it failed")
	wantError(t, err, "cannot be read yet")
	wantCount(t, gh, 2+boardItemReads)
	if ticket.Number != 50 {
		t.Fatalf("ticket = %+v", ticket)
	}
}

func TestSetStatusContinuesWhenTheBoardAlreadyHasIt(t *testing.T) {
	read := fakegithub.Route{Match: "projectItems(first: 100)", Body: `{"data":{"node":{"projectItems":{"nodes":[{"id":"ITEM7","project":{"id":"B1"}}]}}}}`}
	c, gh := newClient(t, boardCollisionRoute(), read, okRoute("updateProjectV2ItemFieldValue"))
	if err := c.writeStatus(ctx, Ticket{ID: "T7", Number: 7, Project: "go-tui"}, StatusDone); err != nil {
		t.Fatal(err)
	}
	if last := lastDescription(gh); !strings.Contains(last, `itemId: "ITEM7"`) {
		t.Fatalf("status write = %s", last)
	}
}

func TestCreateTicketFailures(t *testing.T) {
	c, gh := newClient(t)
	_, err := c.CreateTicket(ctx, CreateOptions{Project: "go-tui", Title: "t", Attachments: []string{"/missing.png"}})
	wantError(t, err, "attachment")
	wantCount(t, gh, 0)

	_, err = c.CreateTicket(ctx, CreateOptions{Project: "bare", Title: "t"})
	wantError(t, err, "has no board")

	c, _ = newClient(t, fakegithub.Route{Match: "createIssue(input", Status: 500, Body: "{}"})
	_, err = c.CreateTicket(ctx, CreateOptions{Project: "go-tui", Title: "t"})
	wantError(t, err, "creating ticket")

	c, _ = newClient(t, createRoute(), addToBoardRoute(), fakegithub.Route{Match: "updateProjectV2ItemFieldValue", Status: 500, Body: "{}"})
	ticket, err := c.CreateTicket(ctx, CreateOptions{Project: "go-tui", Title: "t"})
	wantError(t, err, "#50 was created")
	if ticket.Number != 50 {
		t.Fatalf("a partial failure must still name the ticket: %+v", ticket)
	}
}

func TestIssueIDsErrors(t *testing.T) {
	c, _ := newClient(t, fakegithub.Route{Match: "n1: issue", Body: `{"data":{"repository":{"isPrivate":true,"n1":null}}}`})
	_, err := c.issueIDs(ctx, []int{1})
	wantError(t, err, "ticket #1 not found")

	c, _ = newClient(t, fakegithub.Route{Match: "n1: issue", Body: notFoundJSON})
	_, err = c.issueIDs(ctx, []int{1})
	wantError(t, err, "does not exist")

	c, _ = newClient(t, fakegithub.Route{Match: "n1: issue", Status: 500, Body: "{}"})
	_, err = c.issueIDs(ctx, []int{1})
	wantError(t, err, "reading tickets")

	c, _ = newClient(t, fakegithub.Route{Match: "n1: issue", Body: `{"data":{"repository":{"isPrivate":false,"n1":{"id":"I1"}}}}`})
	c.requirePrivate = true
	_, err = c.issueIDs(ctx, []int{1})
	wantError(t, err, "not private")
}

func idsRoute(numbers ...int) fakegithub.Route {
	parts := []string{`"isPrivate":true`}
	for _, number := range numbers {
		parts = append(parts, fmt.Sprintf(`"n%[1]d":{"id":"I%[1]d"}`, number))
	}
	return fakegithub.Route{Match: fmt.Sprintf("n%[1]d: issue(number: %[1]d)", numbers[0]), Body: `{"data":{"repository":{` + strings.Join(parts, ",") + `}}}`}
}

func TestEditCommentAndRelationsUseTwoRequests(t *testing.T) {
	title, body := "New", "Text"
	tests := []struct {
		name string
		run  func(*Client) error
		want string
	}{
		{"edit", func(c *Client) error { return c.EditTicket(ctx, 1, EditOptions{Title: &title, Body: &body}) }, "updateIssue"},
		{"comment", func(c *Client) error { return c.AddComment(ctx, 1, "hi") }, "addComment"},
		{"add blockers", func(c *Client) error { return c.SetBlockers(ctx, 1, []int{2, 3}, true) }, "addBlockedBy"},
		{"remove blockers", func(c *Client) error { return c.SetBlockers(ctx, 1, []int{2}, false) }, "removeBlockedBy"},
		{"add sub-issues", func(c *Client) error { return c.SetSubIssues(ctx, 1, []int{2}, true) }, "addSubIssue"},
		{"remove sub-issues", func(c *Client) error { return c.SetSubIssues(ctx, 1, []int{2}, false) }, "removeSubIssue"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, gh := newClient(t, idsRoute(1, 2, 3), okRoute(tt.want))
			if err := tt.run(c); err != nil {
				t.Fatal(err)
			}
			wantCount(t, gh, 2)
			if !strings.Contains(lastDescription(gh), tt.want) {
				t.Fatalf("write = %s", lastDescription(gh))
			}
		})
	}
}

func TestEditNeedsAChange(t *testing.T) {
	c, gh := newClient(t)
	wantError(t, c.EditTicket(ctx, 1, EditOptions{}), "nothing to change")
	wantCount(t, gh, 0)
	gh.Add(fakegithub.Route{Match: "n1: issue", Status: 500, Body: "{}"})
	title := "x"
	wantError(t, c.EditTicket(ctx, 1, EditOptions{Title: &title}), "reading tickets")
	wantError(t, c.AddComment(ctx, 1, "x"), "reading tickets")
	wantError(t, c.SetBlockers(ctx, 1, []int{2}, true), "reading tickets")
}

func TestSetStatus(t *testing.T) {
	c, gh := newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui", status: "Todo"}), okRoute("updateProjectV2ItemFieldValue"))
	if err := c.SetStatus(ctx, 1, StatusDone); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 2)
	wantError(t, c.SetStatus(ctx, 1, StatusDoing), "use issue claim")
	wantError(t, c.SetStatus(ctx, 1, "Blocked"), "invalid status")

	c, _ = newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui", status: "Doing"}))
	wantError(t, c.SetStatus(ctx, 1, StatusTodo), "is claimed")

	c, _ = newClient(t, ticketRoute(ticketSpec{number: 1, status: "Todo"}))
	wantError(t, c.SetStatus(ctx, 1, StatusDone), "no project label")

	c, _ = newClient(t, ticketRoute(ticketSpec{number: 1, project: "bare", status: "Todo"}))
	wantError(t, c.SetStatus(ctx, 1, StatusDone), "has no board")

	c, gh = newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui"}),
		fakegithub.Route{Match: "addProjectV2ItemById", Body: `{"data":{"addProjectV2ItemById":{"item":{"id":"NEWITEM"}}}}`}, okRoute("updateProjectV2ItemFieldValue"))
	if err := c.SetStatus(ctx, 1, StatusTodo); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 3)
	if !strings.Contains(lastDescription(gh), "NEWITEM") {
		t.Fatalf("write = %s", lastDescription(gh))
	}

	c, _ = newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui"}), fakegithub.Route{Match: "addProjectV2ItemById", Status: 500, Body: "{}"})
	wantError(t, c.SetStatus(ctx, 1, StatusTodo), "adding ticket to board")

	missing := testMetadata()
	missing.Projects[0].StatusOptions = map[string]string{}
	gh = fakegithub.New(ticketRoute(ticketSpec{number: 1, project: "go-tui", status: "Todo"}))
	wantError(t, clientWith(t, gh, missing).SetStatus(ctx, 1, StatusDone), "no Done column")
}

func TestMoveTicket(t *testing.T) {
	c, gh := newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui", status: "Doing"}), okRoute("addLabelsToLabelable"),
		fakegithub.Route{Match: "addProjectV2ItemById", Body: `{"data":{"addProjectV2ItemById":{"item":{"id":"NEWITEM"}}}}`}, okRoute("updateProjectV2ItemFieldValue"))
	if err := c.MoveTicket(ctx, 1, "lib"); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 4)
	move := gh.Requests()[1].Description
	for _, want := range []string{`labelIds: ["L2"]`, `labelIds: ["L1"]`, "deleteProjectV2Item"} {
		if !strings.Contains(move, want) {
			t.Fatalf("move = %s, missing %s", move, want)
		}
	}
	if !strings.Contains(lastDescription(gh), `"opt-doing"`) {
		t.Fatalf("status must survive a move: %s", lastDescription(gh))
	}

	same, gh := newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui", status: StatusTodo}))
	if err := same.MoveTicket(ctx, 1, "go-tui"); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 1)

	c, _ = newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui"}))
	wantError(t, c.MoveTicket(ctx, 1, "bare"), "has no board")

	c, _ = newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui"}), fakegithub.Route{Match: "addLabelsToLabelable", Status: 500, Body: "{}"})
	wantError(t, c.MoveTicket(ctx, 1, "lib"), "moving ticket")
}

func TestMoveTicketWithoutProject(t *testing.T) {
	c, gh := newClient(t, ticketRoute(ticketSpec{number: 1}), okRoute("addLabelsToLabelable"),
		fakegithub.Route{Match: "addProjectV2ItemById", Body: `{"data":{"addProjectV2ItemById":{"item":{"id":"NEWITEM"}}}}`}, okRoute("updateProjectV2ItemFieldValue"))
	if err := c.MoveTicket(ctx, 1, "lib"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastDescription(gh), `"opt-todo"`) {
		t.Fatalf("a ticket with no status starts at Todo: %s", lastDescription(gh))
	}
}

func TestReopenTicket(t *testing.T) {
	c, gh := newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui", state: "CLOSED", status: "Done"}), okRoute("reopenIssue"), okRoute("updateProjectV2ItemFieldValue"))
	if err := c.ReopenTicket(ctx, 1); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 3)
	c, _ = newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui"}), fakegithub.Route{Match: "reopenIssue", Status: 500, Body: "{}"})
	wantError(t, c.ReopenTicket(ctx, 1), "reopening")
}

func pullRoute(merged bool) fakegithub.Route {
	return fakegithub.Route{Match: "pullRequest(number: 88)", Body: fmt.Sprintf(`{"data":{"r0":{"pullRequest":{"merged":%v,"url":"https://github.com/o/go-tui/pull/88"}}}}`, merged)}
}

func TestCloseTicketWithMergedPR(t *testing.T) {
	c, gh := newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui", status: "Todo"}), pullRoute(true), okRoute("closeIssue"))
	if err := c.CloseTicket(ctx, 1, CloseOptions{PRs: []string{"88"}}); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 3)
	write := lastDescription(gh)
	for _, want := range []string{"Merged in https://github.com/o/go-tui/pull/88", "stateReason: COMPLETED", `"opt-done"`} {
		if !strings.Contains(write, want) {
			t.Fatalf("write = %s, missing %s", write, want)
		}
	}
}

func TestCloseClaimedTicketDeletesClaim(t *testing.T) {
	c, gh := newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui", status: "Doing"}), pullRoute(true), okRoute("closeIssue"),
		fakegithub.Route{Match: "DELETE api.github.com/repos/o/tracker/git/refs/claims/1", Status: 204})
	if err := c.CloseTicket(ctx, 1, CloseOptions{PRs: []string{"88"}}); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 4)
}

func TestCloseAcceptsPRFromAnotherProject(t *testing.T) {
	pulls := fakegithub.Route{Match: `r1: repository(owner: "o", name: "lib") { pullRequest(number: 14)`, Body: `{"data":{"r0":{"pullRequest":{"merged":true,"url":"https://a/88"}},"r1":{"pullRequest":{"merged":true,"url":"https://b/14"}}}}`}
	c, gh := newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui", status: "Todo"}), pulls, okRoute("closeIssue"))
	if err := c.CloseTicket(ctx, 1, CloseOptions{PRs: []string{"88", "lib#14"}}); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 3)
	if !strings.Contains(lastDescription(gh), "https://a/88, https://b/14") {
		t.Fatalf("write = %s", lastDescription(gh))
	}
}

func TestCloseWithReason(t *testing.T) {
	for reason, want := range map[string]string{ReasonNotPlanned: "NOT_PLANNED", ReasonDuplicate: "DUPLICATE"} {
		c, gh := newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui", status: "Todo"}), okRoute("closeIssue"))
		if err := c.CloseTicket(ctx, 1, CloseOptions{Reason: reason}); err != nil {
			t.Fatal(err)
		}
		wantCount(t, gh, 2)
		write := lastDescription(gh)
		if !strings.Contains(write, "stateReason: "+want) || strings.Contains(write, "addComment") {
			t.Fatalf("write = %s", write)
		}
	}
}

func TestCloseRefusals(t *testing.T) {
	ticket := ticketRoute(ticketSpec{number: 1, project: "go-tui", status: "Todo"})
	tests := []struct {
		name   string
		routes []fakegithub.Route
		opts   CloseOptions
		want   string
	}{
		{"unmerged", []fakegithub.Route{ticket, pullRoute(false)}, CloseOptions{PRs: []string{"88"}}, "is not merged"},
		{"missing PR", []fakegithub.Route{ticket, {Match: "pullRequest(number: 88)", Body: `{"data":{"r0":{"pullRequest":null}},"errors":[{"type":"NOT_FOUND","message":"x"}]}`}}, CloseOptions{PRs: []string{"88"}}, "not found"},
		{"PR read failure", []fakegithub.Route{ticket, {Match: "pullRequest(number: 88)", Status: 500, Body: "{}"}}, CloseOptions{PRs: []string{"88"}}, "reading pull requests"},
		{"bad number", []fakegithub.Route{ticket}, CloseOptions{PRs: []string{"abc"}}, "invalid pull request"},
		{"unknown project", []fakegithub.Route{ticket, metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`)}, CloseOptions{PRs: []string{"nope#1"}}, "unknown project"},
		{"bad reason", []fakegithub.Route{ticket}, CloseOptions{Reason: "bored"}, "invalid reason"},
		{"bare PR on a ticket with no project", []fakegithub.Route{ticketRoute(ticketSpec{number: 1})}, CloseOptions{PRs: []string{"88"}}, "has no project"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, gh := newClient(t, tt.routes...)
			wantError(t, c.CloseTicket(ctx, 1, tt.opts), tt.want)
			for _, req := range gh.Requests() {
				if strings.Contains(req.Description, "closeIssue") {
					t.Fatal("a refused close must not write")
				}
			}
		})
	}
}

func TestCloseWriteFailure(t *testing.T) {
	c, _ := newClient(t, ticketRoute(ticketSpec{number: 1, project: "go-tui", status: "Todo"}), fakegithub.Route{Match: "closeIssue", Status: 500, Body: "{}"})
	wantError(t, c.CloseTicket(ctx, 1, CloseOptions{Reason: ReasonNotPlanned}), "closing ticket")
}

func TestAttachmentsOnCreate(t *testing.T) {
	fakeGH(t, "exit 0")
	file := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, _ := newClient(t, createRoute(), addToBoardRoute(), okRoute("updateProjectV2ItemFieldValue"))
	if _, err := c.CreateTicket(ctx, CreateOptions{Project: "go-tui", Title: "t", Attachments: []string{file}}); err != nil {
		t.Fatal(err)
	}
	fakeGH(t, "echo denied >&2; exit 1")
	ticket, err := c.CreateTicket(ctx, CreateOptions{Project: "go-tui", Title: "t", Attachments: []string{file}})
	wantError(t, err, "#50 was created but attaching failed")
	if ticket.Number != 50 {
		t.Fatalf("ticket = %+v", ticket)
	}
}

func TestCloseReasonCompletedClosesAsCompleted(t *testing.T) {
	reason, err := stateReason(ReasonCompleted)
	if err != nil || reason != "COMPLETED" {
		t.Fatalf("reason = %q, err = %v", reason, err)
	}
	_, err = stateReason("finished")
	wantError(t, err, "expected completed, not-planned, or duplicate")
}

func TestMoveSetsTheStatusOfAnItemAlreadyOnTheBoard(t *testing.T) {
	body := strings.Replace(ticketRoute(ticketSpec{number: 12, project: "go-tui", status: StatusTodo}).Body, `"fieldValueByName":{"name":"Todo"}`, `"fieldValueByName":null`, 1)
	route := fakegithub.Route{Match: ticketRoute(ticketSpec{}).Match, Body: body}
	c, gh := newClient(t, route, okRoute("updateProjectV2ItemFieldValue"))
	if err := c.MoveTicket(ctx, 12, "go-tui"); err != nil {
		t.Fatal(err)
	}
	if last := lastDescription(gh); !strings.Contains(last, `singleSelectOptionId: "opt-todo"`) || countRequests(gh, "addProjectV2ItemById") != 0 {
		t.Fatalf("last write = %s", last)
	}
}

func TestMoveFinishesAnImportThatLostItsStatus(t *testing.T) {
	route := ticketRoute(ticketSpec{number: 12, project: "go-tui"})
	add := fakegithub.Route{Match: "addProjectV2ItemById", Body: `{"data":{"addProjectV2ItemById":{"item":{"id":"ITEM"}}}}`}
	c, gh := newClient(t, route, add, okRoute("updateProjectV2ItemFieldValue"))
	if err := c.MoveTicket(ctx, 12, "go-tui"); err != nil {
		t.Fatal(err)
	}
	if last := lastDescription(gh); !strings.Contains(last, `singleSelectOptionId: "opt-todo"`) {
		t.Fatalf("last write = %s", last)
	}
}
