package tracker

import (
	"strings"
	"testing"

	"github.com/alexgorbatchev/issue-tracker-cli/v2/internal/fakegithub"
)

func TestUnroutedListsOnlyTicketsWithNoProject(t *testing.T) {
	results := boardOnlyRoute(
		ticketSpec{number: 1, label: "bug"},
		ticketSpec{number: 2, status: "Todo"},
		ticketSpec{number: 3, project: "registered-elsewhere"},
		ticketSpec{number: 4},
	)
	c, gh := newClient(t, results)
	tickets, total, err := c.ListTickets(ctx, ListOptions{Unrouted: true})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(tickets) != 2 || tickets[0].Number != 1 || tickets[1].Number != 4 {
		t.Fatalf("tickets = %+v, total = %d", tickets, total)
	}
	wantCount(t, gh, 1)
	search := gh.Requests()[0].Variables["search"].(string)
	for _, term := range []string{`-label:"repo:go-tui"`, `-label:"repo:lib"`, `-label:"repo:bare"`, "is:open"} {
		if !strings.Contains(search, term) {
			t.Fatalf("search = %q, want %s", search, term)
		}
	}
	if gh.Requests()[0].Variables["checkRegistrations"] != false {
		t.Fatal("an unrouted list has no repository to check components for")
	}
}

func TestUnroutedFilters(t *testing.T) {
	results := boardOnlyRoute(ticketSpec{number: 1, label: "bug"}, ticketSpec{number: 4})
	c, gh := newClient(t, results)
	tickets, total, err := c.ListTickets(ctx, ListOptions{Unrouted: true, Label: "bug", Search: "crash", State: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(tickets) != 1 || tickets[0].Number != 1 {
		t.Fatalf("tickets = %+v, total = %d", tickets, total)
	}
	if search := gh.Requests()[0].Variables["search"].(string); strings.Contains(search, "is:open") || !strings.HasSuffix(search, "crash") {
		t.Fatalf("search = %q", search)
	}

	_, _, err = c.ListTickets(ctx, ListOptions{Unrouted: true, Project: "go-tui"})
	wantError(t, err, "pass a project or unrouted, not both")

	failing := clientWith(t, fakegithub.New(fakegithub.Route{Match: "labels(first: 100, query", Status: 500, Body: "{}"}), nil)
	_, _, err = failing.ListTickets(ctx, ListOptions{Unrouted: true})
	wantError(t, err, "reading tracker metadata")
}

func TestAnUnroutedTicketIsNeverClaimed(t *testing.T) {
	c, gh := newClient(t, ticketRoute(ticketSpec{number: 7}))
	_, err := c.ClaimTicket(ctx, 7)
	wantError(t, err, "ticket #7 has no project: give it a repo: label on GitHub or run issue move 7 <project>")
	wantCount(t, gh, 1)

	c, gh = newClient(t, listRoute(ticketSpec{number: 7}))
	if _, ok, err := c.ClaimNext(ctx, "", nil); ok || err != nil {
		t.Fatalf("claim --next must pass over an unrouted ticket: ok = %v, err = %v", ok, err)
	}
	wantCount(t, gh, 1)
}
