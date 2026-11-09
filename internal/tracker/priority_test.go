package tracker

import (
	"slices"
	"testing"

	"github.com/alexgorbatchev/issue-tracker-cli/v2/internal/fakegithub"
)

func TestPriorityLabels(t *testing.T) {
	tests := []struct {
		name      string
		labels    []string
		priority  string
		conflicts []string
		others    []string
	}{
		{"none", []string{"bug"}, "", nil, []string{"bug"}},
		{"one", []string{"bug", "priority:P2"}, "P2", nil, []string{"bug"}},
		{"case ignored", []string{"Priority:p1"}, "P1", nil, []string{}},
		{"most urgent wins", []string{"priority:P3", "bug", "priority:P0"}, "P0", []string{"P0", "P3"}, []string{"bug"}},
		{"repeated is no conflict", []string{"priority:P1", "PRIORITY:P1"}, "P1", nil, []string{}},
		{"not a priority", []string{"priority:high", "priority:P", "priority:P4", "priority:P10", "priority:P01", "priority:P-1", "prio:P1"}, "", nil,
			[]string{"priority:high", "priority:P", "priority:P4", "priority:P10", "priority:P01", "priority:P-1", "prio:P1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ticket := Ticket{Labels: tt.labels}
			if got := ticket.Priority(); got != tt.priority {
				t.Fatalf("Priority() = %q, want %q", got, tt.priority)
			}
			if got := ticket.PriorityConflicts(); !slices.Equal(got, tt.conflicts) {
				t.Fatalf("PriorityConflicts() = %v, want %v", got, tt.conflicts)
			}
			if got := ticket.OtherLabels(); !slices.Equal(got, tt.others) {
				t.Fatalf("OtherLabels() = %v, want %v", got, tt.others)
			}
			if len(ticket.Labels) != len(tt.labels) {
				t.Fatal("OtherLabels must not change the ticket's labels")
			}
		})
	}
}

func TestParsePriority(t *testing.T) {
	for value, want := range map[string]string{"P1": "P1", "p0": "P0", "P3": "P3"} {
		if got, err := parsePriority(value); err != nil || got != want {
			t.Fatalf("parsePriority(%q) = %q, %v, want %q", value, got, err, want)
		}
	}
	for _, value := range []string{"", "1", "high", "P", "P1a", "P4", "P01", "P11"} {
		if _, err := parsePriority(value); err == nil {
			t.Fatalf("parsePriority(%q) must fail", value)
		}
	}
}

func numbers(tickets []Ticket) []int {
	out := []int{}
	for _, ticket := range tickets {
		out = append(out, ticket.Number)
	}
	return out
}

func TestReadyTicketsComeMostUrgentFirst(t *testing.T) {
	route := listRoute(
		ticketSpec{number: 1, project: "go-tui", status: "Todo", created: "2026-01-01T00:00:00Z"},
		ticketSpec{number: 2, project: "go-tui", status: "Todo", created: "2026-01-02T00:00:00Z", priorities: []string{"P3"}},
		ticketSpec{number: 3, project: "go-tui", status: "Todo", created: "2026-01-03T00:00:00Z", priorities: []string{"P1"}},
		ticketSpec{number: 4, project: "go-tui", status: "Todo", created: "2026-01-04T00:00:00Z", priorities: []string{"P3", "P0"}},
		ticketSpec{number: 5, project: "go-tui", status: "Todo", created: "2026-01-05T00:00:00Z", priorities: []string{"P1"}},
	)
	c, gh := newClient(t, boardOnlyRoute(), route)
	tickets, total, err := c.ListTickets(ctx, ListOptions{Project: "go-tui", Ready: true, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got := numbers(tickets); !slices.Equal(got, []int{4, 3, 5}) || total != 5 {
		t.Fatalf("tickets = %v of %d, want the limit applied after sorting: [4 3 5] of 5", got, total)
	}
	wantCount(t, gh, 2)

	c, _ = newClient(t, boardOnlyRoute(), route)
	if tickets, _, err = c.ListTickets(ctx, ListOptions{Project: "go-tui", Ready: true}); err != nil {
		t.Fatal(err)
	}
	if got := numbers(tickets); !slices.Equal(got, []int{4, 3, 5, 2, 1}) {
		t.Fatalf("tickets = %v, want no priority last", got)
	}

	c, _ = newClient(t, boardOnlyRoute(), route)
	if tickets, _, err = c.ListTickets(ctx, ListOptions{Project: "go-tui"}); err != nil {
		t.Fatal(err)
	}
	if got := numbers(tickets); !slices.Equal(got, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("tickets = %v, want other lists oldest first", got)
	}
}

func TestListTicketsFiltersByPriority(t *testing.T) {
	route := listRoute(
		ticketSpec{number: 1, project: "go-tui", status: "Todo", priorities: []string{"P1"}},
		ticketSpec{number: 2, project: "go-tui", status: "Todo", priorities: []string{"P2", "P1"}},
		ticketSpec{number: 3, project: "go-tui", status: "Todo", priorities: []string{"P2"}},
	)
	c, _ := newClient(t, route)
	tickets, total, err := c.ListTickets(ctx, ListOptions{Priority: "p2", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := numbers(tickets); !slices.Equal(got, []int{3}) || total != 1 {
		t.Fatalf("tickets = %v of %d, want only the ticket whose most urgent priority is P2", got, total)
	}
	_, _, err = c.ListTickets(ctx, ListOptions{Priority: "urgent"})
	wantError(t, err, `invalid priority "urgent"`)
}

func TestClaimNextTakesTheMostUrgentTicket(t *testing.T) {
	c, gh := newClient(t, boardOnlyRoute(), listRoute(
		ticketSpec{number: 1, project: "go-tui", status: "Todo"},
		ticketSpec{number: 2, project: "go-tui", status: "Todo", priorities: []string{"P2"}},
	), fakegithub.Route{Match: refsPost, Status: 201, Body: "{}"}, okRoute("updateProjectV2ItemFieldValue"))
	ticket, ok, err := c.ClaimNext(ctx, "go-tui", nil)
	if err != nil || !ok || ticket.Number != 2 {
		t.Fatalf("claimed %+v, %v, %v, want #2", ticket, ok, err)
	}
	wantCount(t, gh, 4)
}
