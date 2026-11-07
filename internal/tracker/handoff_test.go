package tracker

import (
	"errors"
	"strings"
	"testing"

	"github.com/alexgorbatchev/issue-tracker-cli/internal/fakegithub"
)

const (
	fullSHA      = "0123456789abcdef0123456789abcdef01234567"
	compareMatch = "GET api.github.com/repos/o/go-tui/compare/"
	slotPath     = "api.github.com/repos/o/tracker/git/refs/handoffs/o/go-tui"
	labelMatch   = "{ id label(name: $label) { id } }"
	handoffQuery = "search(query: $search, type: ISSUE, first: 2)"
	handoffBody  = "Branch: feat/x\nCommit: " + fullSHA + "\nTicket: #7\n\nResume at the parser."
)

func commitCompareRoute(status string) fakegithub.Route {
	return fakegithub.Route{Match: compareMatch, Body: `{"status":"` + status + `","base_commit":{"sha":"` + fullSHA + `"}}`}
}

func handoffLabelRoute() fakegithub.Route {
	return fakegithub.Route{Match: labelMatch, Body: `{"data":{"repository":{"id":"REPO","label":{"id":"LH"}}}}`}
}

// handoffRoute answers the search for a project's open handoff with these tickets.
func handoffRoute(body string, specs ...ticketSpec) fakegithub.Route {
	nodes := make([]string, 0, len(specs))
	for _, spec := range specs {
		nodes = append(nodes, spec.json()+`,"body":`+quote(body)+`}`)
	}
	return fakegithub.Route{Match: handoffQuery, Body: `{"data":{"repository":{"isPrivate":true},"search":{"nodes":[` + strings.Join(nodes, ",") + `]}}}`}
}

func quote(text string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`).Replace(text) + `"`
}

func openHandoff() ticketSpec {
	return ticketSpec{number: 40, project: "go-tui", status: StatusTodo, handoff: true, created: "2026-11-01T00:00:00Z"}
}

func requestsMatching(gh *fakegithub.GitHub, match string) []fakegithub.Request {
	var found []fakegithub.Request
	for _, req := range gh.Requests() {
		if strings.Contains(req.Description, match) {
			found = append(found, req)
		}
	}
	return found
}

func TestCreateHandoff(t *testing.T) {
	c, gh := newClient(t, commitCompareRoute("ahead"), handoffLabelRoute(), fakegithub.Route{Match: refsPost, Status: 201, Body: "{}"},
		createRoute(), addToBoardRoute(), okRoute("updateProjectV2ItemFieldValue"))
	handoff, err := c.CreateHandoff(ctx, HandoffOptions{Project: "go-tui", Title: "parser", Text: "Resume at the parser.", Branch: "feat/x", SHA: "0123456", Related: 7})
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Ticket.Number != 50 || handoff.SHA != fullSHA || handoff.Branch != "feat/x" {
		t.Fatalf("handoff = %+v", handoff)
	}
	wantCount(t, gh, 6)
	if path := requestsMatching(gh, compareMatch)[0].Description; !strings.HasSuffix(path, "/compare/0123456...feat/x") {
		t.Fatalf("compare = %s", path)
	}
	if slot := requestsMatching(gh, refsPost)[0].Body; !strings.Contains(slot, `"refs/handoffs/o/go-tui"`) || !strings.Contains(slot, `"headsha"`) {
		t.Fatalf("slot = %s", slot)
	}
	input := requestsMatching(gh, "createIssue(input")[0].Variables["input"].(map[string]any)
	if input["body"] != handoffBody {
		t.Fatalf("body = %q", input["body"])
	}
	if labels := input["labelIds"].([]any); len(labels) != 2 || labels[0] != "L1" || labels[1] != "LH" {
		t.Fatalf("a handoff carries its label from creation, so it is never ready work: %v", labels)
	}
	// The slot is taken before the ticket, so a second handoff fails before anything else.
	if requests := gh.Requests(); !strings.Contains(requests[2].Description, refsPost) {
		t.Fatalf("slot must come before the ticket: %s", requests[2].Description)
	}
}

func TestCreateHandoffCreatesItsLabel(t *testing.T) {
	missing := fakegithub.Route{Match: labelMatch, Body: `{"data":{"repository":{"id":"REPO","label":null}}}`}
	created := fakegithub.Route{Match: "createLabel(input", Body: `{"data":{"createLabel":{"label":{"id":"LNEW"}}}}`}
	c, gh := newClient(t, commitCompareRoute("identical"), missing, created, fakegithub.Route{Match: refsPost, Status: 201, Body: "{}"},
		createRoute(), addToBoardRoute(), okRoute("updateProjectV2ItemFieldValue"))
	if _, err := c.CreateHandoff(ctx, HandoffOptions{Project: "go-tui", Title: "t", Branch: "main", SHA: fullSHA}); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 7)
	if vars := requestsMatching(gh, "createLabel(input")[0].Variables; vars["name"] != "handoff" || vars["repository"] != "REPO" {
		t.Fatalf("label = %v", vars)
	}
	input := requestsMatching(gh, "createIssue(input")[0].Variables["input"].(map[string]any)
	if labels := input["labelIds"].([]any); labels[1] != "LNEW" || input["body"] != "Branch: main\nCommit: "+fullSHA {
		t.Fatalf("input = %v", input)
	}
}

func TestCreateHandoffRefusals(t *testing.T) {
	conflict := fakegithub.Route{Match: refsPost, Status: 422, Body: claimExist}
	tests := []struct {
		name   string
		routes []fakegithub.Route
		opts   HandoffOptions
		want   string
		count  int
	}{
		{"no branch", nil, HandoffOptions{Project: "go-tui", SHA: fullSHA}, "a handoff needs a branch and a commit", 0},
		{"no commit", nil, HandoffOptions{Project: "go-tui", Branch: "main"}, "a handoff needs a branch and a commit", 0},
		{"negative ticket", nil, HandoffOptions{Project: "go-tui", Branch: "main", SHA: fullSHA, Related: -1}, "invalid ticket #-1", 0},
		{"unknown project", []fakegithub.Route{metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`)}, HandoffOptions{Project: "nope", Branch: "main", SHA: fullSHA}, `unknown project "nope"`, 2},
		{"commit not on the branch", []fakegithub.Route{commitCompareRoute("behind")}, HandoffOptions{Project: "go-tui", Branch: "main", SHA: fullSHA}, "branch main of o/go-tui does not contain commit " + fullSHA + ": push the commit first", 1},
		{"diverged", []fakegithub.Route{commitCompareRoute("diverged")}, HandoffOptions{Project: "go-tui", Branch: "main", SHA: fullSHA}, "does not contain commit", 1},
		{"unpushed", []fakegithub.Route{{Match: compareMatch, Status: 404, Body: `{"message":"Not Found"}`}}, HandoffOptions{Project: "go-tui", Branch: "wip", SHA: "abc1234"}, "branch wip or commit abc1234 not found in o/go-tui: push them first", 1},
		{"compare fails", []fakegithub.Route{{Match: compareMatch, Status: 500, Body: `{"message":"boom"}`}}, HandoffOptions{Project: "go-tui", Branch: "main", SHA: fullSHA}, "comparing commit", 1},
		{"long body", []fakegithub.Route{commitCompareRoute("ahead")}, HandoffOptions{Project: "go-tui", Branch: "main", SHA: fullSHA, Text: strings.Repeat("x", maxBodyLength)}, "over GitHub's 65536", 1},
		{"label read fails", []fakegithub.Route{commitCompareRoute("ahead"), {Match: labelMatch, Status: 500, Body: "{}"}}, HandoffOptions{Project: "go-tui", Branch: "main", SHA: fullSHA}, "reading handoff label", 2},
		{"label create fails", []fakegithub.Route{commitCompareRoute("ahead"), {Match: labelMatch, Body: `{"data":{"repository":{"id":"REPO","label":null}}}`}, {Match: "createLabel(input", Status: 500, Body: "{}"}}, HandoffOptions{Project: "go-tui", Branch: "main", SHA: fullSHA}, "creating handoff label", 3},
		{"open handoff", []fakegithub.Route{commitCompareRoute("ahead"), handoffLabelRoute(), conflict, handoffRoute(handoffBody, openHandoff())}, HandoffOptions{Project: "go-tui", Branch: "main", SHA: fullSHA}, "go-tui already has open handoff #40: take or drop it first", 4},
		{"slot left behind", []fakegithub.Route{commitCompareRoute("ahead"), handoffLabelRoute(), conflict, handoffRoute("")}, HandoffOptions{Project: "go-tui", Branch: "main", SHA: fullSHA}, "the handoff slot of go-tui is taken, but it has no open handoff, perhaps after a failed create: run issue-tracker handoff drop to free it", 4},
		{"conflict unreadable", []fakegithub.Route{commitCompareRoute("ahead"), handoffLabelRoute(), conflict, {Match: handoffQuery, Status: 500, Body: "{}"}}, HandoffOptions{Project: "go-tui", Branch: "main", SHA: fullSHA}, "go-tui already has an open handoff: reading handoff", 4},
		{"slot fails", []fakegithub.Route{commitCompareRoute("ahead"), handoffLabelRoute(), {Match: refsPost, Status: 500, Body: `{"message":"boom"}`}}, HandoffOptions{Project: "go-tui", Branch: "main", SHA: fullSHA}, "opening the handoff slot of go-tui", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, gh := newClient(t, tt.routes...)
			_, err := c.CreateHandoff(ctx, tt.opts)
			wantError(t, err, tt.want)
			wantCount(t, gh, tt.count)
			if len(requestsMatching(gh, "createIssue")) > 0 {
				t.Fatal("a refused handoff must create no ticket")
			}
		})
	}
}

func TestCreateHandoffRetriesAVanishedCommit(t *testing.T) {
	gone := fakegithub.Route{Match: refsPost, Status: 422, Body: `{"message":"Object does not exist"}`, Times: 1}
	c, gh := newClient(t, commitCompareRoute("ahead"), handoffLabelRoute(), gone, metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`),
		fakegithub.Route{Match: refsPost, Status: 201, Body: "{}"}, createRoute(), addToBoardRoute(), okRoute("updateProjectV2ItemFieldValue"))
	if _, err := c.CreateHandoff(ctx, HandoffOptions{Project: "go-tui", Title: "t", Branch: "main", SHA: fullSHA}); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 9)

	c, gh = newClient(t, commitCompareRoute("ahead"), handoffLabelRoute(), gone, fakegithub.Route{Match: "labels(first: 100, query", Status: 500, Body: "{}"})
	_, err := c.CreateHandoff(ctx, HandoffOptions{Project: "go-tui", Title: "t", Branch: "main", SHA: fullSHA})
	wantError(t, err, "reading tracker metadata")
	wantCount(t, gh, 4)
}

func TestCreateHandoffFreesTheSlotWhenNoTicketWasCreated(t *testing.T) {
	routes := []fakegithub.Route{commitCompareRoute("ahead"), handoffLabelRoute(), {Match: refsPost, Status: 201, Body: "{}"},
		{Match: "createIssue(input", Status: 500, Body: "{}"}}
	c, gh := newClient(t, append(routes, fakegithub.Route{Match: "DELETE " + slotPath, Status: 204})...)
	_, err := c.CreateHandoff(ctx, HandoffOptions{Project: "go-tui", Title: "t", Branch: "main", SHA: fullSHA})
	wantError(t, err, "creating ticket")
	wantCount(t, gh, 5)
	if len(requestsMatching(gh, "DELETE "+slotPath)) != 1 {
		t.Fatal("a failed create must free the slot")
	}

	c, gh = newClient(t, append(routes, fakegithub.Route{Match: "DELETE " + slotPath, Status: 500, Body: `{"message":"boom"}`})...)
	_, err = c.CreateHandoff(ctx, HandoffOptions{Project: "go-tui", Title: "t", Branch: "main", SHA: fullSHA})
	wantError(t, err, "creating ticket")
	wantError(t, err, "freeing the handoff slot of go-tui")
	wantCount(t, gh, 5)

	// A created ticket keeps the slot, and the error names it.
	c, gh = newClient(t, commitCompareRoute("ahead"), handoffLabelRoute(), fakegithub.Route{Match: refsPost, Status: 201, Body: "{}"},
		createRoute(), fakegithub.Route{Match: "addProjectV2ItemById", Status: 500, Body: "{}"})
	handoff, err := c.CreateHandoff(ctx, HandoffOptions{Project: "go-tui", Title: "t", Branch: "main", SHA: fullSHA})
	wantError(t, err, "ticket #50 was created but linking it failed")
	if handoff.Ticket.Number != 50 || len(requestsMatching(gh, "DELETE")) != 0 {
		t.Fatalf("handoff = %+v", handoff)
	}
}

func TestShowHandoff(t *testing.T) {
	c, gh := newClient(t, handoffRoute(handoffBody, openHandoff()))
	handoff, err := c.ShowHandoff(ctx, "go-tui")
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Ticket.Number != 40 || handoff.Branch != "feat/x" || handoff.SHA != fullSHA || handoff.Related != 7 ||
		handoff.Text != "Resume at the parser." || handoff.Ticket.Created().IsZero() {
		t.Fatalf("handoff = %+v", handoff)
	}
	wantCount(t, gh, 1)
	search := gh.Requests()[0].Variables["search"]
	for _, want := range []string{"repo:o/tracker", "is:open", `label:"handoff"`, `label:"repo:go-tui"`} {
		if !strings.Contains(search.(string), want) {
			t.Fatalf("search = %v, missing %s", search, want)
		}
	}

	c, gh = newClient(t, handoffRoute(""))
	_, err = c.ShowHandoff(ctx, "go-tui")
	if !errors.Is(err, ErrNoHandoff) || err.Error() != "no open handoff in go-tui" {
		t.Fatalf("err = %v", err)
	}
	wantCount(t, gh, 1)

	c, _ = newClient(t, handoffRoute("Branch: x\r\n\r\ntext", openHandoff()))
	_, err = c.ShowHandoff(ctx, "go-tui")
	wantError(t, err, `handoff #40 has no "Branch:" and "Commit:" lines at the top of its body: drop it and create it again`)

	c, _ = newClient(t, fakegithub.Route{Match: handoffQuery, Body: `{"data":{"repository":{"isPrivate":false},"search":{"nodes":[]}}}`})
	c.requirePrivate = true
	_, err = c.ShowHandoff(ctx, "go-tui")
	wantError(t, err, "not private")

	c, _ = newClient(t, metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	_, err = c.ShowHandoff(ctx, "nope")
	wantError(t, err, `unknown project "nope"`)
}

// A component shares its repository's slot, so its handoff is found from either.
func TestAComponentSharesItsRepositorysHandoff(t *testing.T) {
	c, gh := monoClient(t, handoffRoute("Branch: b\nCommit: "+fullSHA, ticketSpec{number: 41, project: "mono/api", handoff: true}))
	handoff, err := c.ShowHandoff(ctx, "mono/api")
	if err != nil || handoff.Ticket.Project != "mono/api" {
		t.Fatalf("handoff = %+v, err = %v", handoff, err)
	}
	search := gh.Requests()[0].Variables["search"].(string)
	if !strings.Contains(search, `"repo:mono"`) || !strings.Contains(search, `"repo:mono/api"`) {
		t.Fatalf("search = %s", search)
	}
	if path := c.slotPath(monoMetadata().Projects[3]); !strings.HasSuffix(path, "/git/refs/handoffs/o/mono") {
		t.Fatalf("slot = %s", path)
	}
}

func takeRoutes(fresh ticketSpec, compare fakegithub.Route) []fakegithub.Route {
	return []fakegithub.Route{
		handoffRoute(handoffBody, openHandoff()),
		{Match: refsPost, Status: 201, Body: "{}"},
		ticketRoute(fresh),
		okRoute("closeIssue"),
		{Match: "DELETE api.github.com/repos/o/tracker/git/refs/claims/40", Status: 204},
		{Match: "DELETE " + slotPath, Status: 204},
		compare,
	}
}

func TestTakeHandoff(t *testing.T) {
	c, gh := newClient(t, takeRoutes(openHandoff(), commitCompareRoute("ahead"))...)
	taken, err := c.TakeHandoff(ctx, "go-tui")
	if err != nil {
		t.Fatal(err)
	}
	if taken.Moved || taken.Ticket.Number != 40 || taken.Branch != "feat/x" || taken.SHA != fullSHA || taken.Text != "Resume at the parser." {
		t.Fatalf("taken = %+v", taken)
	}
	wantCount(t, gh, 7)
	if claim := requestsMatching(gh, refsPost)[0].Body; !strings.Contains(claim, `"refs/claims/40"`) {
		t.Fatalf("take claims the handoff through the atomic claim: %s", claim)
	}
	closing := requestsMatching(gh, "closeIssue")[0].Description
	if !strings.Contains(closing, "stateReason: COMPLETED") || !strings.Contains(closing, `"opt-done"`) {
		t.Fatalf("close = %s", closing)
	}
	if len(requestsMatching(gh, "DELETE "+slotPath)) != 1 {
		t.Fatal("take must free the slot")
	}

	for _, status := range []string{"behind", "diverged"} {
		c, _ = newClient(t, takeRoutes(openHandoff(), commitCompareRoute(status))...)
		if taken, err = c.TakeHandoff(ctx, "go-tui"); err != nil || !taken.Moved {
			t.Fatalf("%s: a moved branch still hands off: %+v, %v", status, taken, err)
		}
	}
	c, _ = newClient(t, takeRoutes(openHandoff(), fakegithub.Route{Match: compareMatch, Status: 404, Body: "{}"})...)
	if taken, err = c.TakeHandoff(ctx, "go-tui"); err != nil || !taken.Moved {
		t.Fatalf("a deleted branch still hands off: %+v, %v", taken, err)
	}
	c, _ = newClient(t, takeRoutes(openHandoff(), fakegithub.Route{Match: compareMatch, Status: 500, Body: "{}"})...)
	_, err = c.TakeHandoff(ctx, "go-tui")
	wantError(t, err, "took handoff #40, but comparing commit")
}

func TestTakeHandoffHasOneWinner(t *testing.T) {
	// The other agent holds the claim.
	c, gh := newClient(t, handoffRoute(handoffBody, openHandoff()), fakegithub.Route{Match: refsPost, Status: 422, Body: claimExist})
	_, err := c.TakeHandoff(ctx, "go-tui")
	if !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("err = %v", err)
	}
	wantCount(t, gh, 2)

	// The other agent finished and deleted its claim after this search.
	closed := openHandoff()
	closed.state = "CLOSED"
	c, gh = newClient(t, handoffRoute(handoffBody, openHandoff()), fakegithub.Route{Match: refsPost, Status: 201, Body: "{}"}, ticketRoute(closed),
		fakegithub.Route{Match: "DELETE api.github.com/repos/o/tracker/git/refs/claims/40", Status: 204})
	_, err = c.TakeHandoff(ctx, "go-tui")
	wantError(t, err, "ticket is already claimed: #40")
	wantCount(t, gh, 4)
	if len(requestsMatching(gh, "closeIssue")) != 0 {
		t.Fatal("a handoff someone else took must not be closed again")
	}

	c, gh = newClient(t, handoffRoute(handoffBody, openHandoff()), fakegithub.Route{Match: refsPost, Status: 201, Body: "{}"}, ticketRoute(closed),
		fakegithub.Route{Match: "DELETE api.github.com/repos/o/tracker/git/refs/claims/40", Status: 500, Body: "{}"})
	_, err = c.TakeHandoff(ctx, "go-tui")
	wantError(t, err, "ticket is already claimed")
	wantError(t, err, "releasing ticket #40")
	wantCount(t, gh, 4)
}

func TestTakeHandoffFailures(t *testing.T) {
	c, gh := newClient(t, handoffRoute(""))
	_, err := c.TakeHandoff(ctx, "go-tui")
	wantError(t, err, "no open handoff in go-tui")
	wantCount(t, gh, 1)

	c, _ = newClient(t, metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	_, err = c.TakeHandoff(ctx, "nope")
	wantError(t, err, `unknown project "nope"`)

	c, _ = newClient(t, handoffRoute(handoffBody, openHandoff()), fakegithub.Route{Match: refsPost, Status: 201, Body: "{}"},
		fakegithub.Route{Match: "isPrivate issue(number: $number)", Status: 500, Body: "{}"},
		fakegithub.Route{Match: "DELETE api.github.com/repos/o/tracker/git/refs/claims/40", Status: 204})
	_, err = c.TakeHandoff(ctx, "go-tui")
	wantError(t, err, "reading ticket #40")

	routes := takeRoutes(openHandoff(), commitCompareRoute("ahead"))
	routes[3] = fakegithub.Route{Match: "closeIssue", Status: 500, Body: "{}"}
	c, _ = newClient(t, routes...)
	_, err = c.TakeHandoff(ctx, "go-tui")
	wantError(t, err, "closing ticket #40")

	routes = takeRoutes(openHandoff(), commitCompareRoute("ahead"))
	routes[5] = fakegithub.Route{Match: "DELETE " + slotPath, Status: 500, Body: `{"message":"boom"}`}
	c, _ = newClient(t, routes...)
	_, err = c.TakeHandoff(ctx, "go-tui")
	wantError(t, err, "took handoff #40, but freeing the handoff slot of go-tui")
}

func TestDropHandoff(t *testing.T) {
	c, gh := newClient(t, handoffRoute(handoffBody, openHandoff()), okRoute("closeIssue"), fakegithub.Route{Match: "DELETE " + slotPath, Status: 204})
	handoff, ok, err := c.DropHandoff(ctx, "go-tui", "stale")
	if err != nil || !ok || handoff.Ticket.Number != 40 {
		t.Fatalf("handoff = %+v, ok = %v, err = %v", handoff, ok, err)
	}
	wantCount(t, gh, 3)
	closing := requestsMatching(gh, "closeIssue")[0].Description
	for _, want := range []string{"stateReason: NOT_PLANNED", `body: "stale"`, `"opt-done"`} {
		if !strings.Contains(closing, want) {
			t.Fatalf("close = %s, missing %s", closing, want)
		}
	}
	if len(requestsMatching(gh, "refs/claims")) != 0 {
		t.Fatal("drop must not claim the handoff")
	}

	c, gh = newClient(t, handoffRoute(handoffBody, openHandoff()), okRoute("closeIssue"), fakegithub.Route{Match: "DELETE " + slotPath, Status: 204})
	if _, _, err := c.DropHandoff(ctx, "go-tui", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(requestsMatching(gh, "closeIssue")[0].Description, "addComment") {
		t.Fatal("drop without a reason posts no comment")
	}
}

func TestDropHandoffFreesALeftoverSlot(t *testing.T) {
	c, gh := newClient(t, handoffRoute(""), fakegithub.Route{Match: "DELETE " + slotPath, Status: 204})
	if _, ok, err := c.DropHandoff(ctx, "go-tui", ""); err != nil || ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	wantCount(t, gh, 2)

	c, gh = newClient(t, handoffRoute(""), fakegithub.Route{Match: "DELETE " + slotPath, Status: 422, Body: `{"message":"Reference does not exist"}`})
	_, _, err := c.DropHandoff(ctx, "go-tui", "")
	if !errors.Is(err, ErrNoHandoff) || err.Error() != "no open handoff in go-tui" {
		t.Fatalf("err = %v", err)
	}
	wantCount(t, gh, 2)
}

func TestDropHandoffFailures(t *testing.T) {
	c, _ := newClient(t, handoffRoute(""), fakegithub.Route{Match: "DELETE " + slotPath, Status: 500, Body: `{"message":"boom"}`})
	_, _, err := c.DropHandoff(ctx, "go-tui", "")
	wantError(t, err, "freeing the handoff slot of go-tui")

	c, _ = newClient(t, fakegithub.Route{Match: handoffQuery, Status: 500, Body: "{}"})
	_, _, err = c.DropHandoff(ctx, "go-tui", "")
	wantError(t, err, "reading handoff")

	c, _ = newClient(t, metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	_, _, err = c.DropHandoff(ctx, "nope", "")
	wantError(t, err, `unknown project "nope"`)

	c, gh := newClient(t, handoffRoute(handoffBody, openHandoff()))
	_, _, err = c.DropHandoff(ctx, "go-tui", strings.Repeat("x", maxBodyLength+1))
	wantError(t, err, "over GitHub's 65536")
	wantCount(t, gh, 1)

	c, _ = newClient(t, handoffRoute(handoffBody, openHandoff()), fakegithub.Route{Match: "closeIssue", Status: 500, Body: "{}"})
	_, _, err = c.DropHandoff(ctx, "go-tui", "")
	wantError(t, err, "closing ticket #40")

	c, _ = newClient(t, handoffRoute(handoffBody, openHandoff()), okRoute("closeIssue"), fakegithub.Route{Match: "DELETE " + slotPath, Status: 500, Body: `{"message":"boom"}`})
	_, _, err = c.DropHandoff(ctx, "go-tui", "")
	wantError(t, err, "dropped handoff #40, but freeing the handoff slot of go-tui")
}

func TestHandoffsAreNotReadyWork(t *testing.T) {
	plain := ticketSpec{number: 1, project: "go-tui", status: StatusTodo}
	c, gh := newClient(t, boardOnlyRoute(), listRoute(openHandoff(), plain))
	var seen Tally
	tickets, _, err := c.ListTickets(ctx, ListOptions{Project: "go-tui", Ready: true, Tally: &seen})
	if err != nil || len(tickets) != 1 || tickets[0].Number != 1 {
		t.Fatalf("a handoff is not ready work: %+v, %v", tickets, err)
	}
	if len(seen.Handoffs) != 1 || seen.Handoffs[0].Number != 40 {
		t.Fatalf("the listing reports the handoffs it read: %+v", seen.Handoffs)
	}
	wantCount(t, gh, 2)

	c, _ = newClient(t, boardOnlyRoute(), listRoute(openHandoff(), plain))
	tickets, _, err = c.ListTickets(ctx, ListOptions{Project: "go-tui", Handoff: true})
	if err != nil || len(tickets) != 1 || tickets[0].Number != 40 || !tickets[0].Handoff() {
		t.Fatalf("--handoff lists only handoffs: %+v, %v", tickets, err)
	}

	c, _ = newClient(t, boardOnlyRoute(), listRoute(openHandoff()), fakegithub.Route{Match: refsPost, Status: 201, Body: "{}"})
	if _, ok, err := c.ClaimNext(ctx, "go-tui", nil); err != nil || ok {
		t.Fatalf("claim --next must pass over a handoff: ok = %v, err = %v", ok, err)
	}
}
