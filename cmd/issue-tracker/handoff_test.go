package main

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/alexgorbatchev/issue-tracker-cli/v2/internal/fakegithub"
)

const (
	handoffSHA    = "0123456789abcdef0123456789abcdef01234567"
	handoffSearch = "issues(labels: $labels, states: OPEN"
	compareMatch  = "GET api.github.com/repos/o/go-tui/compare/"
	refsPost      = "POST api.github.com/repos/o/tracker/git/refs"
	slotDelete    = "DELETE api.github.com/repos/o/tracker/git/refs/handoffs/o/go-tui"
)

// handoffJSON is an open handoff in go-tui, created a day and a half ago.
func handoffJSON(created time.Time) string {
	body := `Branch: feat/x\nCommit: ` + handoffSHA + `\nTicket: #7\n\nResume at the parser.`
	return `{"id":"H40","number":40,"title":"Parser","state":"OPEN","url":"https://t/40","createdAt":"` + created.Format(time.RFC3339) + `","updatedAt":"2026-10-01T00:00:00Z",` +
		`"labels":{"nodes":[{"name":"repo:go-tui"},{"name":"handoff"}]},"blockedBy":{"nodes":[]},"parent":null,` +
		`"projectItems":{"nodes":[{"id":"ITEM40","updatedAt":"2026-10-01T00:00:00Z","project":{"id":"B1"},"fieldValueByName":{"name":"Todo"}}]},"body":"` + body + `"}`
}

func handoffSearchRoute(nodes ...string) fakegithub.Route {
	return fakegithub.Route{Match: handoffSearch, Body: `{"data":{"repository":{"isPrivate":true,"issues":{"nodes":[` + strings.Join(nodes, ",") + `],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`}
}

func compareRoute(status string) fakegithub.Route {
	return fakegithub.Route{Match: compareMatch, Body: `{"status":"` + status + `","base_commit":{"sha":"` + handoffSHA + `"}}`}
}

func createHandoffRoutes() []fakegithub.Route {
	return []fakegithub.Route{
		compareRoute("ahead"),
		{Match: "{ id label(name: $label) { id } }", Body: `{"data":{"repository":{"id":"REPO","label":{"id":"LH"}}}}`},
		{Match: refsPost, Status: 201, Body: "{}"},
		{Match: "createIssue(input", Body: `{"data":{"createIssue":{"issue":{"id":"NEW","number":50,"url":"https://t/50"}}}}`},
		{Match: "addProjectV2ItemById", Body: `{"data":{"addProjectV2ItemById":{"item":{"id":"ITEM50"}}}}`},
		ok("updateProjectV2ItemFieldValue"),
	}
}

func TestHandoffCreate(t *testing.T) {
	t.Setenv("AGENT", "1")
	gh := fakegithub.New(createHandoffRoutes()...)
	stdout, _, err := execute(t, fakeOpener(t, gh), "handoff", "create", "--project", "go-tui", "--title", "Parser",
		"--branch", "feat/x", "--sha", "0123456", "--ticket", "7", "--body", "Resume at the parser.")
	if err != nil {
		t.Fatal(err)
	}
	if want := "OK: created handoff #50 in go-tui at feat/x " + handoffSHA + ": https://t/50\n"; stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	if gh.Count() != 6 {
		t.Fatalf("made %d requests, want 6", gh.Count())
	}
}

// git runs git in dir for a test fixture.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestHandoffCreateDefaultsToTheCurrentCheckout(t *testing.T) {
	t.Setenv("AGENT", "1")
	outsideRepository(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "--quiet", "--initial-branch", "feat/checkout")
	gitIn(t, dir, "commit", "--quiet", "--allow-empty", "--message", "work")
	head := gitIn(t, dir, "rev-parse", "HEAD")
	t.Chdir(dir)
	t.Setenv("GH_REPO", "o/go-tui")

	gh := fakegithub.New(createHandoffRoutes()...)
	if _, _, err := execute(t, fakeOpener(t, gh), "handoff", "create", "--title", "Parser"); err != nil {
		t.Fatal(err)
	}
	if path := gh.Requests()[0].Description; !strings.HasSuffix(path, "/compare/"+head+"...feat/checkout") {
		t.Fatalf("the branch and commit come from the checkout: %s", path)
	}

	// An explicit branch keeps the checkout's commit, and the other way round.
	gh = fakegithub.New(createHandoffRoutes()...)
	if _, _, err := execute(t, fakeOpener(t, gh), "handoff", "create", "--title", "Parser", "--branch", "main"); err != nil {
		t.Fatal(err)
	}
	if path := gh.Requests()[0].Description; !strings.HasSuffix(path, "/compare/"+head+"...main") {
		t.Fatalf("compare = %s", path)
	}

	gitIn(t, dir, "checkout", "--quiet", "--detach")
	_, _, err := execute(t, fakeOpener(t, fakegithub.New()), "handoff", "create", "--title", "Parser")
	wantErr(t, err, "no branch to hand off: the checkout has no current branch: pass --branch")

	t.Setenv("GH_REPO", "o/lib")
	_, _, err = execute(t, fakeOpener(t, fakegithub.New()), "handoff", "create", "--title", "Parser", "--project", "go-tui")
	wantErr(t, err, "no branch or commit to hand off: this checkout is o/lib, not o/go-tui: pass --branch and --sha, or run inside a checkout of o/go-tui")
}

func TestHandoffCreateNeedsACommit(t *testing.T) {
	t.Setenv("AGENT", "1")
	outsideRepository(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "--quiet", "--initial-branch", "main")
	t.Chdir(dir)
	t.Setenv("GH_REPO", "o/go-tui")
	_, _, err := execute(t, fakeOpener(t, fakegithub.New()), "handoff", "create", "--title", "Parser")
	wantErr(t, err, "no commit to hand off: the checkout has no commits: pass --sha")
}

func TestHandoffCreateOutsideACheckout(t *testing.T) {
	t.Setenv("AGENT", "1")
	outsideRepository(t)
	gh := fakegithub.New()
	_, _, err := execute(t, fakeOpener(t, gh), "handoff", "create", "--title", "Parser", "--project", "go-tui")
	wantErr(t, err, "no branch or commit to hand off: no git checkout here: pass --branch and --sha, or run inside a checkout of o/go-tui")
	_, _, err = execute(t, fakeOpener(t, gh), "handoff", "create", "--title", "Parser", "--branch", "main", "--sha", handoffSHA)
	wantErr(t, err, "not inside a registered repository: no git checkout here: pass --project <name>")
	_, _, err = execute(t, fakeOpener(t, gh), "handoff", "create", "--title", "Parser", "--project", "go-tui", "--body-file", "missing.md")
	wantErr(t, err, "reading body file")
	_, _, err = execute(t, noSession, "handoff", "create", "--title", "Parser")
	wantErr(t, err, "this command must not need a session")
	if gh.Count() != 0 {
		t.Fatalf("a refused create must not reach GitHub: %d requests", gh.Count())
	}

	gh = fakegithub.New(fakegithub.Route{Match: compareMatch, Status: 404, Body: `{"message":"Not Found"}`})
	_, _, err = execute(t, fakeOpener(t, gh), "handoff", "create", "--title", "Parser", "--project", "go-tui", "--branch", "wip", "--sha", handoffSHA)
	wantErr(t, err, "branch wip or commit "+handoffSHA+" not found in o/go-tui: push them first")
}

func TestHandoffShow(t *testing.T) {
	created := time.Now().Add(-36 * time.Hour)
	t.Setenv("AGENT", "1")
	gh := fakegithub.New(handoffSearchRoute(handoffJSON(created)))
	stdout, _, err := execute(t, fakeOpener(t, gh), "handoff", "show", "--project", "go-tui")
	if err != nil {
		t.Fatal(err)
	}
	if want := "OK: 40\tgo-tui\tfeat/x\t" + handoffSHA + "\n\nResume at the parser.\n"; stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	if gh.Count() != 1 {
		t.Fatalf("made %d requests, want 1", gh.Count())
	}

	t.Setenv("AGENT", "")
	stdout, _, err = execute(t, fakeOpener(t, fakegithub.New(handoffSearchRoute(handoffJSON(created)))), "handoff", "show", "--project", "go-tui")
	if err != nil {
		t.Fatal(err)
	}
	want := "handoff: #40 Parser\nproject: go-tui\nbranch: feat/x\ncommit: " + handoffSHA + "\nticket: #7\nage: 36h\nurl: https://t/40\n\nResume at the parser.\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}

	t.Setenv("AGENT", "1")
	stdout, _, err = execute(t, fakeOpener(t, fakegithub.New(handoffSearchRoute())), "handoff", "show", "--project", "go-tui")
	if err != nil || stdout != "OK: no open handoff in go-tui\n" {
		t.Fatalf("stdout = %q, err = %v", stdout, err)
	}

	_, _, err = execute(t, fakeOpener(t, fakegithub.New(fakegithub.Route{Match: handoffSearch, Status: 500, Body: "{}"})), "handoff", "show", "--project", "go-tui")
	wantErr(t, err, "reading handoff")
	_, _, err = execute(t, noSession, "handoff", "show")
	wantErr(t, err, "this command must not need a session")
}

func TestHandoffShowWarnsForPublicProjects(t *testing.T) {
	t.Setenv("AGENT", "1")
	node := strings.ReplaceAll(handoffJSON(time.Now()), `"repo:go-tui"`, `"repo:lib"`)
	node = strings.ReplaceAll(node, `"id":"B1"`, `"id":"B2"`)
	stdout, _, err := execute(t, fakeOpener(t, fakegithub.New(handoffSearchRoute(node))), "handoff", "show", "--project", "lib")
	if err != nil || !strings.HasPrefix(stdout, "WARN: lib is public") {
		t.Fatalf("stdout = %q, err = %v", stdout, err)
	}
}

func takeRoutes(compare fakegithub.Route) []fakegithub.Route {
	return []fakegithub.Route{
		handoffSearchRoute(handoffJSON(time.Now())),
		{Match: refsPost, Status: 201, Body: "{}"},
		{Match: "isPrivate issue(number: $number)", Body: `{"data":{"repository":{"isPrivate":true,"issue":` + strings.TrimSuffix(handoffJSON(time.Now()), "}") + `}}}}`},
		ok("closeIssue"),
		{Match: "DELETE api.github.com/repos/o/tracker/git/refs/claims/40", Status: 204},
		{Match: slotDelete, Status: 204},
		compare,
	}
}

func TestHandoffTake(t *testing.T) {
	t.Setenv("AGENT", "1")
	gh := fakegithub.New(takeRoutes(compareRoute("ahead"))...)
	stdout, stderr, err := execute(t, fakeOpener(t, gh), "handoff", "take", "--project", "go-tui")
	if err != nil {
		t.Fatal(err)
	}
	if want := "OK: 40\tgo-tui\tfeat/x\t" + handoffSHA + "\n\nResume at the parser.\n"; stdout != want || stderr != "" {
		t.Fatalf("stdout = %q, stderr = %q", stdout, stderr)
	}
	if gh.Count() != 7 {
		t.Fatalf("made %d requests, want 7", gh.Count())
	}

	_, stderr, err = execute(t, fakeOpener(t, fakegithub.New(takeRoutes(compareRoute("diverged"))...)), "handoff", "take", "--project", "go-tui")
	if err != nil || stderr != "WARN: branch feat/x no longer contains commit "+handoffSHA+": check where the work went before resuming\n" {
		t.Fatalf("stderr = %q, err = %v", stderr, err)
	}

	_, stderr, err = execute(t, fakeOpener(t, fakegithub.New(takeRoutes(fakegithub.Route{Match: compareMatch, Status: 500, Body: `{"message":"boom"}`})...)), "handoff", "take", "--project", "go-tui")
	if err != nil || !strings.HasPrefix(stderr, "WARN: could not check that branch feat/x still contains commit "+handoffSHA+": comparing commit") {
		t.Fatalf("stderr = %q, err = %v", stderr, err)
	}

	// A handoff closed before a later failure is still printed, then the error.
	routes := takeRoutes(compareRoute("ahead"))
	routes[5] = fakegithub.Route{Match: slotDelete, Status: 500, Body: `{"message":"boom"}`}
	stdout, _, err = execute(t, fakeOpener(t, fakegithub.New(routes...)), "handoff", "take", "--project", "go-tui")
	wantErr(t, err, "took handoff #40, but freeing the handoff slot of go-tui")
	if want := "OK: 40\tgo-tui\tfeat/x\t" + handoffSHA + "\n\nResume at the parser.\n"; stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}

	routes = takeRoutes(compareRoute("ahead"))
	routes[4] = fakegithub.Route{Match: "DELETE api.github.com/repos/o/tracker/git/refs/claims/40", Status: 500, Body: `{"message":"boom"}`}
	gh = fakegithub.New(routes...)
	stdout, _, err = execute(t, fakeOpener(t, gh), "handoff", "take", "--project", "go-tui")
	wantErr(t, err, "took handoff #40, but releasing ticket #40")
	if want := "OK: 40\tgo-tui\tfeat/x\t" + handoffSHA + "\n\nResume at the parser.\n"; stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	if gh.Count() != 6 {
		t.Fatalf("made %d requests, want 6", gh.Count())
	}

	_, _, err = execute(t, fakeOpener(t, fakegithub.New(handoffSearchRoute())), "handoff", "take", "--project", "go-tui")
	wantErr(t, err, "no open handoff in go-tui")
	_, _, err = execute(t, noSession, "handoff", "take")
	wantErr(t, err, "this command must not need a session")

	gh = fakegithub.New(handoffSearchRoute(handoffJSON(time.Now())), fakegithub.Route{Match: refsPost, Status: 422, Body: `{"message":"Reference already exists"}`})
	_, _, err = execute(t, fakeOpener(t, gh), "handoff", "take", "--project", "go-tui")
	wantErr(t, err, "ticket is already claimed: #40")
}

func TestHandoffUntake(t *testing.T) {
	t.Setenv("AGENT", "1")
	taken := strings.Replace(strings.TrimSuffix(handoffJSON(time.Now()), "}"), `"state":"OPEN"`, `"state":"CLOSED"`, 1) + `,"stateReason":"COMPLETED"}`
	gh := fakegithub.New(
		fakegithub.Route{Match: "body stateReason }", Body: `{"data":{"repository":{"isPrivate":true,"issue":` + taken + `}}}`},
		fakegithub.Route{Match: refsPost, Status: 201, Body: "{}"},
		fakegithub.Route{Match: "DELETE api.github.com/repos/o/tracker/git/refs/claims/40", Status: 204},
		ok("reopenIssue"),
	)
	stdout, _, err := execute(t, fakeOpener(t, gh), "handoff", "untake", "40", "--reason", "wrong project")
	if want := "OK: returned handoff #40 to go-tui at feat/x " + handoffSHA + ": Parser\n"; err != nil || stdout != want {
		t.Fatalf("stdout = %q, want %q, err = %v", stdout, want, err)
	}
	if gh.Count() != 4 {
		t.Fatalf("made %d requests, want 4", gh.Count())
	}
	if reopen := gh.Requests()[3].Description; !strings.Contains(reopen, `body: "wrong project"`) {
		t.Fatalf("reopen = %s", reopen)
	}

	_, _, err = execute(t, fakeOpener(t, fakegithub.New()), "handoff", "untake", "x")
	wantErr(t, err, `invalid ticket number "x"`)
	gh = fakegithub.New(fakegithub.Route{Match: "body stateReason }", Body: `{"data":{"repository":{"isPrivate":true,"issue":` + handoffJSON(time.Now()) + `}}}`})
	_, _, err = execute(t, fakeOpener(t, gh), "handoff", "untake", "40")
	wantErr(t, err, "handoff #40 is open, so nobody has taken it")
}

func TestHandoffDrop(t *testing.T) {
	t.Setenv("AGENT", "1")
	gh := fakegithub.New(handoffSearchRoute(handoffJSON(time.Now())), ok("closeIssue"), fakegithub.Route{Match: slotDelete, Status: 204})
	stdout, _, err := execute(t, fakeOpener(t, gh), "handoff", "drop", "--project", "go-tui", "--reason", "stale")
	if err != nil || stdout != "OK: dropped handoff #40 in go-tui: Parser\n" {
		t.Fatalf("stdout = %q, err = %v", stdout, err)
	}
	if gh.Count() != 3 {
		t.Fatalf("made %d requests, want 3", gh.Count())
	}

	gh = fakegithub.New(handoffSearchRoute(), fakegithub.Route{Match: slotDelete, Status: 204})
	_, _, err = execute(t, fakeOpener(t, gh), "handoff", "drop", "--project", "go-tui")
	wantErr(t, err, "no open handoff in go-tui")
	if gh.Count() != 1 {
		t.Fatalf("drop without --free-slot must leave the slot: %d requests", gh.Count())
	}

	gh = fakegithub.New(handoffSearchRoute(), fakegithub.Route{Match: slotDelete, Status: 204})
	stdout, _, err = execute(t, fakeOpener(t, gh), "handoff", "drop", "--project", "go-tui", "--free-slot")
	if err != nil || stdout != "OK: freed the handoff slot of go-tui, which held no open handoff\n" {
		t.Fatalf("stdout = %q, err = %v", stdout, err)
	}

	gh = fakegithub.New(handoffSearchRoute(), fakegithub.Route{Match: slotDelete, Status: 422, Body: `{"message":"Reference does not exist"}`})
	_, _, err = execute(t, fakeOpener(t, gh), "handoff", "drop", "--project", "go-tui", "--free-slot")
	wantErr(t, err, "no open handoff in go-tui")
	_, _, err = execute(t, noSession, "handoff", "drop")
	wantErr(t, err, "this command must not need a session")
}

func TestClaimNextPointsAtOpenHandoffs(t *testing.T) {
	t.Setenv("AGENT", "1")
	t.Setenv("GH_TOKEN", "tok")
	node := strings.TrimSuffix(strings.ReplaceAll(handoffJSON(time.Now().Add(-3*24*time.Hour)), `,"body":"`, `,"x":"`), "}")
	gh := fakegithub.New(boardOnly(), listRoute(node))
	stdout, stderr, err := execute(t, fakeOpener(t, gh), "issue", "claim", "--next", "--project", "go-tui")
	if err != nil || stdout != "OK: no ready tickets\n" {
		t.Fatalf("a handoff is never claimed as ready work: stdout = %q, err = %v", stdout, err)
	}
	if want := "WARN: open handoff #40 in go-tui, 3d old: review it with issue-tracker handoff show --project go-tui\n"; stderr != want {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
	if gh.Count() != 2 {
		t.Fatalf("made %d requests, want the board search and the ready list", gh.Count())
	}
}

func TestListHandoffs(t *testing.T) {
	t.Setenv("AGENT", "1")
	node := strings.TrimSuffix(strings.ReplaceAll(handoffJSON(time.Now()), `,"body":"`, `,"x":"`), "}")
	gh := fakegithub.New(boardOnly(), listRoute(node, ticketJSON(2, "go-tui", "Todo", "2026-10-01T00:00:00Z")))
	stdout, _, err := execute(t, fakeOpener(t, gh), "issue", "list", "--project", "go-tui", "--handoff")
	if err != nil || !strings.HasPrefix(stdout, "40\t") || strings.Count(stdout, "\n") != 1 {
		t.Fatalf("stdout = %q, err = %v", stdout, err)
	}
	_, _, err = execute(t, fakeOpener(t, fakegithub.New()), "issue", "list", "--project", "go-tui", "--handoff", "--ready")
	wantErr(t, err, "none of the others can be")

	t.Setenv("AGENT", "")
	gh = fakegithub.New(boardOnly(), listRoute(ticketJSON(2, "go-tui", "Todo", "2026-10-01T00:00:00Z")), boardOnly(), listRoute())
	stdout, _, err = execute(t, fakeOpener(t, gh), "issue", "list", "--project", "go-tui", "--handoff")
	if err != nil || !strings.HasPrefix(stdout, "No tickets match.") {
		t.Fatalf("stdout = %q, err = %v", stdout, err)
	}
}

func TestAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0m", 59 * time.Minute: "59m", time.Hour: "1h", 47 * time.Hour: "47h", 48 * time.Hour: "2d", 10*24*time.Hour + time.Hour: "10d",
	} {
		if got := age(d); got != want {
			t.Errorf("age(%v) = %q, want %q", d, got, want)
		}
	}
}

func wantErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want it to contain %q", err, want)
	}
}
