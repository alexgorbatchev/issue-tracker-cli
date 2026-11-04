package tracker

import (
	"errors"
	"strings"
	"testing"

	"github.com/alexgorbatchev/issue-tracker-cli/internal/fakegithub"
)

const (
	refsPost   = "POST api.github.com/repos/o/tracker/git/refs"
	claimExist = `{"message":"Reference already exists"}`
)

func TestClaimTicket(t *testing.T) {
	c, gh := newClient(t, ticketRoute(ticketSpec{number: 12, project: "go-tui", status: "Todo"}),
		fakegithub.Route{Match: refsPost, Status: 201, Body: "{}"}, okRoute("updateProjectV2ItemFieldValue"))
	ticket, err := c.ClaimTicket(ctx, 12)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Status != StatusDoing {
		t.Fatalf("ticket = %+v", ticket)
	}
	wantCount(t, gh, 3)
	body := gh.Requests()[1].Body
	if !strings.Contains(body, `"refs/claims/12"`) || !strings.Contains(body, `"headsha"`) {
		t.Fatalf("claim body = %s", body)
	}
	if !strings.Contains(lastDescription(gh), `"opt-doing"`) {
		t.Fatalf("status write = %s", lastDescription(gh))
	}
}

func TestClaimWritesTheStatusOfTheProjectsItem(t *testing.T) {
	// GitHub can list another board's item first, and that item rejects the
	// project's Status field.
	spec := ticketSpec{number: 12, project: "go-tui", status: "Todo", otherBoard: "PERSONAL"}
	stale := testMetadata()
	stale.Projects = stale.Projects[1:]
	gh := fakegithub.New(ticketRoute(spec), metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`),
		fakegithub.Route{Match: refsPost, Status: 201, Body: "{}"}, okRoute("updateProjectV2ItemFieldValue"))
	if _, err := clientWith(t, gh, stale).ClaimTicket(ctx, 12); err != nil {
		t.Fatal(err)
	}
	write := lastDescription(gh)
	if !strings.Contains(write, `itemId: "ITEM12"`) || strings.Contains(write, "OTHER12") {
		t.Fatalf("a project registered elsewhere must use its own item after a refresh: %s", write)
	}
}

func TestClaimAddsATicketMissingFromItsBoard(t *testing.T) {
	spec := ticketSpec{number: 12, project: "lib", status: "Todo"}
	c, gh := newClient(t, ticketRoute(spec), fakegithub.Route{Match: refsPost, Status: 201, Body: "{}"},
		addToBoardRoute(), okRoute("updateProjectV2ItemFieldValue"))
	if _, err := c.ClaimTicket(ctx, 12); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 4)
	if add := gh.Requests()[2].Body; !strings.Contains(add, `"B2"`) {
		t.Fatalf("add = %s, want the ticket added to its project's board", add)
	}
	write := lastDescription(gh)
	if !strings.Contains(write, `itemId: "ITEM50"`) || strings.Contains(write, "deleteProjectV2Item") {
		t.Fatalf("the other board's item must stay and take no write: %s", write)
	}
}

func TestClaimLostRace(t *testing.T) {
	c, gh := newClient(t, ticketRoute(ticketSpec{number: 12, project: "go-tui", status: "Todo"}),
		fakegithub.Route{Match: refsPost, Status: 422, Body: claimExist})
	if _, err := c.ClaimTicket(ctx, 12); !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("err = %v", err)
	}
	wantCount(t, gh, 2)
}

func TestClaimRefusesClosedTicket(t *testing.T) {
	c, _ := newClient(t, ticketRoute(ticketSpec{number: 12, state: "CLOSED"}))
	_, err := c.ClaimTicket(ctx, 12)
	wantError(t, err, "is closed")
}

func TestClaimReleasesLockWhenStatusFails(t *testing.T) {
	c, gh := newClient(t, ticketRoute(ticketSpec{number: 12, project: "go-tui", status: "Todo"}),
		fakegithub.Route{Match: refsPost, Status: 201, Body: "{}"},
		fakegithub.Route{Match: "updateProjectV2ItemFieldValue", Status: 500, Body: "{}"},
		fakegithub.Route{Match: "DELETE api.github.com/repos/o/tracker/git/refs/claims/12", Status: 204})
	if _, err := c.ClaimTicket(ctx, 12); err == nil {
		t.Fatal("want the status failure")
	}
	if !strings.Contains(lastDescription(gh), "DELETE") {
		t.Fatalf("the claim must be rolled back: %s", lastDescription(gh))
	}
}

func TestClaimRollbackFailureIsReported(t *testing.T) {
	c, _ := newClient(t, ticketRoute(ticketSpec{number: 12, project: "go-tui", status: "Todo"}),
		fakegithub.Route{Match: refsPost, Status: 201, Body: "{}"},
		fakegithub.Route{Match: "updateProjectV2ItemFieldValue", Status: 500, Body: "{}"},
		fakegithub.Route{Match: "DELETE", Status: 500, Body: "{}"})
	_, err := c.ClaimTicket(ctx, 12)
	wantError(t, err, "releasing ticket #12")
}

func TestClaimRetriesWithFreshCommit(t *testing.T) {
	c, gh := newClient(t, ticketRoute(ticketSpec{number: 12, project: "go-tui", status: "Todo"}),
		fakegithub.Route{Match: refsPost, Status: 422, Body: `{"message":"Object does not exist"}`, Times: 1},
		fakegithub.Route{Match: refsPost, Status: 201, Body: "{}"},
		metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`), okRoute("updateProjectV2ItemFieldValue"))
	if _, err := c.ClaimTicket(ctx, 12); err != nil {
		t.Fatal(err)
	}
	requests := gh.Requests()
	if !strings.Contains(requests[len(requests)-2].Body, `"sha1"`) {
		t.Fatalf("retry body = %s", requests[len(requests)-2].Body)
	}
}

func TestClaimReportsOtherFailures(t *testing.T) {
	c, _ := newClient(t, ticketRoute(ticketSpec{number: 12, project: "go-tui"}), fakegithub.Route{Match: refsPost, Status: 500, Body: "{}"})
	_, err := c.ClaimTicket(ctx, 12)
	wantError(t, err, "claiming ticket #12")

	c, _ = newClient(t, ticketRoute(ticketSpec{number: 12, project: "go-tui"}),
		fakegithub.Route{Match: refsPost, Status: 422, Body: `{"message":"Object does not exist"}`},
		metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	_, err = c.ClaimTicket(ctx, 12)
	wantError(t, err, "claiming ticket #12")

	c, _ = newClient(t, ticketRoute(ticketSpec{number: 12, project: "go-tui"}),
		fakegithub.Route{Match: refsPost, Status: 422, Body: `{"message":"Object does not exist"}`},
		fakegithub.Route{Match: "labels(first: 100, query", Status: 500, Body: "{}"})
	_, err = c.ClaimTicket(ctx, 12)
	wantError(t, err, "reading tracker metadata")
}

func TestClaimNextSkipsLostTickets(t *testing.T) {
	c, gh := newClient(t, boardOnlyRoute(), listRoute(
		ticketSpec{number: 1, project: "go-tui", status: "Doing"},
		ticketSpec{number: 2, project: "go-tui", status: "Todo"},
		ticketSpec{number: 3, project: "go-tui", status: "Todo"},
	),
		fakegithub.Route{Match: refsPost, Status: 422, Body: claimExist, Times: 1},
		fakegithub.Route{Match: refsPost, Status: 201, Body: "{}"}, okRoute("updateProjectV2ItemFieldValue"))
	ticket, ok, err := c.ClaimNext(ctx, "go-tui")
	if err != nil || !ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	if ticket.Number != 3 {
		t.Fatalf("ticket = %+v, want the next candidate after a lost race", ticket)
	}
	wantCount(t, gh, 5)
}

func TestClaimNextSkipsAParentWithOpenSubIssues(t *testing.T) {
	c, _ := newClient(t, listRoute(ticketSpec{number: 1, status: "Todo", subs: openSub}))
	_, ok, err := c.ClaimNext(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a parent with open sub-issues must not be claimed")
	}
}

func TestClaimNextWithNothingReady(t *testing.T) {
	c, _ := newClient(t, listRoute(ticketSpec{number: 1, status: "Doing"}))
	if _, ok, err := c.ClaimNext(ctx, ""); ok || err != nil {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
}

func TestClaimNextFailures(t *testing.T) {
	c, _ := newClient(t, fakegithub.Route{Match: "issues(first", Status: 500, Body: "{}"})
	_, _, err := c.ClaimNext(ctx, "")
	wantError(t, err, "listing tickets")

	c, _ = newClient(t, listRoute(ticketSpec{number: 1, project: "go-tui"}), fakegithub.Route{Match: refsPost, Status: 500, Body: "{}"})
	_, _, err = c.ClaimNext(ctx, "")
	wantError(t, err, "claiming ticket")
}

func TestReleaseTicket(t *testing.T) {
	c, gh := newClient(t, ticketRoute(ticketSpec{number: 12, project: "go-tui", status: "Doing"}),
		fakegithub.Route{Match: "DELETE api.github.com/repos/o/tracker/git/refs/claims/12", Status: 204}, okRoute("updateProjectV2ItemFieldValue"))
	if err := c.ReleaseTicket(ctx, 12); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 3)
	if !strings.Contains(lastDescription(gh), `"opt-todo"`) {
		t.Fatalf("status = %s", lastDescription(gh))
	}
}

func TestReleaseToleratesMissingClaim(t *testing.T) {
	c, _ := newClient(t, ticketRoute(ticketSpec{number: 12, project: "go-tui", status: "Doing"}),
		fakegithub.Route{Match: "DELETE", Status: 422, Body: `{"message":"Reference does not exist"}`}, okRoute("updateProjectV2ItemFieldValue"))
	if err := c.ReleaseTicket(ctx, 12); err != nil {
		t.Fatal(err)
	}
	c, _ = newClient(t, ticketRoute(ticketSpec{number: 12, project: "go-tui", status: "Doing"}), fakegithub.Route{Match: "DELETE", Status: 500, Body: "{}"})
	wantError(t, c.ReleaseTicket(ctx, 12), "releasing ticket")
}
