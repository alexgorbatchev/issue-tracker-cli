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
	c, gh := newClient(t, listRoute(
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
	wantCount(t, gh, 4)
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
