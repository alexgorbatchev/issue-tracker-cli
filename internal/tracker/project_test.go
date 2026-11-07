package tracker

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/alexgorbatchev/issue-tracker-cli/internal/fakegithub"
)

func TestParseRepo(t *testing.T) {
	tests := []struct {
		value string
		ok    bool
	}{
		{"o/r", true}, {"", false}, {"o", false}, {"/r", false}, {"o/", false}, {"o/r/x", false}, {"o r/x", false},
	}
	for _, tt := range tests {
		repo, err := ParseRepo(tt.value)
		if (err == nil) != tt.ok {
			t.Fatalf("ParseRepo(%q) err = %v", tt.value, err)
		}
		if tt.ok && repo.String() != tt.value {
			t.Fatalf("String() = %q", repo.String())
		}
	}
}

func TestNewUsesEnvironmentToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "tok")
	c, err := New(Options{Repo: Repo{"o", "r"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Repo().Name != "r" {
		t.Fatalf("repo = %+v", c.Repo())
	}
}

func TestNewUsesTheConfiguredAccount(t *testing.T) {
	t.Setenv("GH_TOKEN", "env-tok")
	t.Setenv("GH_PATH", "")
	log := fakeGH(t, "echo bot-tok")
	c, err := New(Options{Repo: Repo{"o", "r"}, Account: "bot"})
	if err != nil {
		t.Fatal(err)
	}
	if c.token != "bot-tok" {
		t.Fatalf("token = %q, want the account's token over GH_TOKEN", c.token)
	}
	if args := readFile(t, log); args != "auth token --hostname github.com --user bot\n" {
		t.Fatalf("gh args = %q", args)
	}

	fakeGH(t, "echo 'no oauth token found for github.com account bot' >&2; exit 1")
	_, err = New(Options{Repo: Repo{"o", "r"}, Account: "bot"})
	wantError(t, err, "reading the token of GitHub account bot")
	wantError(t, err, "no oauth token found")

	fakeGH(t, "exit 1")
	_, err = New(Options{Repo: Repo{"o", "r"}, Account: "bot"})
	wantError(t, err, "reading the token of GitHub account bot: exit status 1: log in with")

	fakeGH(t, "echo")
	_, err = New(Options{Repo: Repo{"o", "r"}, Account: "bot"})
	wantError(t, err, "reading the token of GitHub account bot: gh printed no token: log in with")

	t.Setenv("PATH", t.TempDir())
	_, err = New(Options{Repo: Repo{"o", "r"}, Account: "bot"})
	wantError(t, err, "using GitHub account bot needs the gh binary")
}

func TestBoardAccessNamesTheConfiguredAccount(t *testing.T) {
	scopes := fakegithub.Route{Match: "isPrivate issue(number: $number)", Body: `{"errors":[{"type":"INSUFFICIENT_SCOPES","message":"needs read:project"}]}`}
	c := clientWith(t, fakegithub.New(scopes), testMetadata())
	c.account = "bot"
	_, err := c.readTicket(ctx, 1)
	wantError(t, err, "GitHub account bot has no access to project boards: run gh auth switch --user bot and issue-tracker auth refresh, then switch back")

	c = clientWith(t, fakegithub.New(scopes), testMetadata())
	_, err = c.readTicket(ctx, 1)
	wantError(t, err, "your GitHub login has no access to project boards: run issue-tracker auth refresh")
}

func emptyMetadata() fakegithub.Route {
	return fakegithub.Route{Match: "labels(first: 100, query", Body: strings.ReplaceAll(strings.ReplaceAll(metadataJSON, `{"id":"L1","name":"repo:go-tui","description":"o/go-tui"},`, ""), `"title":"go-tui"`, `"title":"unrelated"`)}
}

func boardRoutes() []fakegithub.Route {
	return []fakegithub.Route{
		{Match: "$owner: String!, $name: String!) { repository(owner: $owner, name: $name) { description url } }", Body: `{"data":{"repository":{"description":"Terminal UI","url":"https://github.com/o/new"}}}`},
		{Match: "createProjectV2(input", Body: `{"data":{"createProjectV2":{"projectV2":{"id":"BN","url":"https://b/n","field":{"id":"FN"}}}}}`},
		{Match: "updateProjectV2Field(input", Body: `{"data":{"columns":{"projectV2Field":{"options":[{"id":"t","name":"Todo"},{"id":"g","name":"Doing"},{"id":"d","name":"Done"}]}},"view":{}}}`},
		{Match: "label(name: $label)", Body: `{"data":{"repository":{"label":null}}}`},
		{Match: "createLabel(input", Body: `{"data":{"createLabel":{"label":{"id":"LN"}}}}`},
	}
}

func TestAddProjectCreatesBoardThenLabel(t *testing.T) {
	routes := append([]fakegithub.Route{emptyMetadata()}, boardRoutes()...)
	gh := fakegithub.New(routes...)
	c := clientWith(t, gh, nil)
	project, err := c.AddProject(ctx, Repo{"o", "new"})
	if err != nil {
		t.Fatal(err)
	}
	if project.Name != "new" || project.Repo != "o/new" || project.LabelID != "LN" || project.BoardID != "BN" || project.StatusOptions[StatusDoing] != "g" {
		t.Fatalf("project = %+v", project)
	}
	var columns, label int
	for i, req := range gh.Requests() {
		if strings.Contains(req.Description, "updateProjectV2Field") {
			columns = i
			if !strings.Contains(req.Description, `name: "Doing"`) || !strings.Contains(req.Description, "BOARD_LAYOUT") {
				t.Fatalf("columns = %s", req.Description)
			}
			if !strings.Contains(req.Description, `updateProjectV2(input: {projectId: "BN", shortDescription: $description})`) ||
				req.Variables["description"] != "Terminal UI\n\nhttps://github.com/o/new" {
				t.Fatalf("the new board must be described in the same request: %s %v", req.Description, req.Variables)
			}
		}
		if strings.Contains(req.Description, "createLabel") {
			label = i
			if req.Variables["description"] != "o/new" {
				t.Fatalf("label = %v", req.Variables)
			}
		}
	}
	if label < columns {
		t.Fatal("the label registers the project, so it must be written last")
	}
}

func TestAddProjectIsIdempotent(t *testing.T) {
	gh := fakegithub.New(metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	c := clientWith(t, gh, nil)
	if _, err := c.AddProject(ctx, Repo{"o", "go-tui"}); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 2)
}

func TestAddProjectNamesOtherOwnersRepositoriesInFull(t *testing.T) {
	routes := append([]fakegithub.Route{metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`)}, boardRoutes()...)
	gh := fakegithub.New(routes...)
	project, err := clientWith(t, gh, nil).AddProject(ctx, Repo{"someorg", "go-tui"})
	if err != nil {
		t.Fatal(err)
	}
	if project.Name != "someorg/go-tui" || project.Repo != "someorg/go-tui" {
		t.Fatalf("project = %+v, want another owner's repository named owner/name beside o/go-tui", project)
	}
	labels, boards := 0, 0
	for _, req := range gh.Requests() {
		if strings.Contains(req.Description, "createLabel(input") {
			labels++
			if req.Variables["name"] != "repo:someorg/go-tui" || req.Variables["description"] != "someorg/go-tui" {
				t.Fatalf("label = %v", req.Variables)
			}
		}
		if strings.Contains(req.Description, "createProjectV2(input") {
			boards++
			if req.Variables["title"] != "someorg/go-tui" {
				t.Fatalf("board = %v", req.Variables)
			}
		}
	}
	if labels != 1 || boards != 1 {
		t.Fatalf("created %d labels and %d boards, want one of each", labels, boards)
	}
	wantCount(t, gh, 9)
}

func TestAddProjectRefusesNameCollision(t *testing.T) {
	legacy := strings.Replace(metadataJSON, `"description":"o/go-tui"`, `"description":"someorg/go-tui"`, 1)
	gh := fakegithub.New(fakegithub.Route{Match: "labels(first: 100, query", Body: legacy}, visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	_, err := clientWith(t, gh, nil).AddProject(ctx, Repo{"o", "go-tui"})
	wantError(t, err, `project "go-tui" is already registered for someorg/go-tui`)

	component := strings.Replace(metadataJSON, `{"id":"L1","name":"repo:go-tui","description":"o/go-tui"},`, `{"id":"L1","name":"repo:go-tui","description":"o/go-tui"},{"id":"LD","name":"repo:go-tui/docs","description":"o/go-tui:docs"},`, 1)
	gh = fakegithub.New(fakegithub.Route{Match: "labels(first: 100, query", Body: component}, visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	_, err = clientWith(t, gh, nil).AddProject(ctx, Repo{"go-tui", "docs"})
	wantError(t, err, `project "go-tui/docs" is already registered for o/go-tui`)
}

func TestAddProjectKeepsALegacyName(t *testing.T) {
	legacy := strings.Replace(metadataJSON, `"description":"o/go-tui"`, `"description":"someorg/go-tui"`, 1)
	gh := fakegithub.New(fakegithub.Route{Match: "labels(first: 100, query", Body: legacy}, visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	project, err := clientWith(t, gh, nil).AddProject(ctx, Repo{"someorg", "go-tui"})
	if err != nil || project.Name != "go-tui" {
		t.Fatalf("a repository registered under its short name keeps it: %+v, %v", project, err)
	}
	wantCount(t, gh, 2)
}

func TestAddProjectRestoresRemovedProject(t *testing.T) {
	routes := append([]fakegithub.Route{
		emptyMetadata(),
		labelRoute("removed: o/new", 2),
		okRoute("updateLabel"),
	}, boardRoutes()...)
	gh := fakegithub.New(routes...)
	project, err := clientWith(t, gh, nil).AddProject(ctx, Repo{"o", "new"})
	if err != nil {
		t.Fatal(err)
	}
	if project.LabelID != "OLD" {
		t.Fatalf("project = %+v", project)
	}
}

// labelRoute answers the label read with a label holding tickets.
func labelRoute(description string, tickets int) fakegithub.Route {
	return fakegithub.Route{Match: "label(name: $label)", Body: fmt.Sprintf(`{"data":{"repository":{"label":{"id":"OLD","description":%q,"issues":{"totalCount":%d}}}}}`, description, tickets)}
}

func TestAddProjectRefusesALabelHoldingOtherTickets(t *testing.T) {
	tests := []struct{ name, description, want string }{
		{"removed for another repository", "removed: someorg/new", "label repo:new holds the 3 tickets of removed project someorg/new: rename the label on GitHub before registering o/new"},
		{"cleared before removals were recorded", "", `label repo:new has no registration and holds 3 tickets, perhaps of a project removed before removals were recorded: if they belong to o/new, set the label's description to "removed: o/new" on GitHub and repeat; otherwise rename the label first`},
		{"registered elsewhere", "someorg/new", "label repo:new is registered for someorg/new"},
		{"free text", "docs", `label repo:new holds 3 tickets and its description "docs" is not a registration: rename the label on GitHub before registering o/new`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh := fakegithub.New(emptyMetadata(), labelRoute(tt.description, 3))
			_, err := clientWith(t, gh, nil).AddProject(ctx, Repo{"o", "new"})
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			wantCount(t, gh, 2)
		})
	}
}

func TestAddProjectReusesALabelThatMovesNothing(t *testing.T) {
	for _, label := range []fakegithub.Route{labelRoute("removed: someorg/new", 0), labelRoute("docs", 0), labelRoute("removed: O/New", 4)} {
		routes := append([]fakegithub.Route{emptyMetadata(), label, okRoute("updateLabel")}, boardRoutes()...)
		gh := fakegithub.New(routes...)
		project, err := clientWith(t, gh, nil).AddProject(ctx, Repo{"o", "new"})
		if err != nil || project.LabelID != "OLD" {
			t.Fatalf("%s: project = %+v, err = %v", label.Body, project, err)
		}
	}
}

func TestAddProjectReusesBoardFromFailedRun(t *testing.T) {
	metadata := strings.ReplaceAll(emptyMetadata().Body, `"title":"unrelated"`, `"title":"new"`)
	routes := []fakegithub.Route{
		{Match: "labels(first: 100, query", Body: metadata},
		boardRoutes()[0], boardRoutes()[3], boardRoutes()[4], okRoute("d0: updateProjectV2(input"),
	}
	gh := fakegithub.New(routes...)
	project, err := clientWith(t, gh, nil).AddProject(ctx, Repo{"o", "new"})
	if err != nil {
		t.Fatal(err)
	}
	if project.BoardID != "B1" {
		t.Fatalf("project = %+v", project)
	}
	described := false
	for _, req := range gh.Requests() {
		if strings.Contains(req.Description, `d0: updateProjectV2(input: {projectId: "B1", shortDescription: $d0})`) {
			described = req.Variables["d0"] == "Terminal UI\n\nhttps://github.com/o/new"
		}
		if strings.Contains(req.Description, "createProjectV2(input") {
			t.Fatal("the board from the failed run must be reused")
		}
	}
	if !described {
		t.Fatalf("the reused board must be described: %v", gh.Requests())
	}
	// An empty tracker skips both visibility reads.
	wantCount(t, gh, 6)

	failing := append(routes[:4:4], fakegithub.Route{Match: "d0: updateProjectV2(input", Status: 500, Body: "{}"})
	_, err = clientWith(t, fakegithub.New(failing...), nil).AddProject(ctx, Repo{"o", "new"})
	wantError(t, err, "writing board descriptions")
}

func TestAddProjectFailures(t *testing.T) {
	routes := boardRoutes()
	fail := func(i int) []fakegithub.Route {
		failing := append([]fakegithub.Route{emptyMetadata()}, routes...)
		failing[i+1].Status, failing[i+1].Body = 500, "{}"
		return failing
	}
	tests := []struct {
		name   string
		routes []fakegithub.Route
		want   string
	}{
		{"metadata", []fakegithub.Route{{Match: "labels(first: 100, query", Status: 500, Body: "{}"}}, "reading tracker metadata"},
		{"missing repo", []fakegithub.Route{emptyMetadata(), boardRoutes()[3], {Match: "{ repository(owner: $owner, name: $name) { description url } }", Body: `{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","message":"x"}]}`}}, "not found"},
		{"board", fail(1), "creating board"},
		{"columns", fail(2), "configuring board"},
		{"label read", fail(3), "reading label"},
		{"label create", fail(4), "creating label"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := clientWith(t, fakegithub.New(tt.routes...), nil).AddProject(ctx, Repo{"o", "new"})
			wantError(t, err, tt.want)
		})
	}
}

func summaryRoute(open int) fakegithub.Route {
	return fakegithub.Route{Match: "open: search(query: $open", Body: fmt.Sprintf(`{"data":{"open":{"issueCount":%d},"closed":{"issueCount":4}}}`, open)}
}

func TestRemoveProject(t *testing.T) {
	c, gh := newClient(t, boardOnlyRoute(), summaryRoute(0), okRoute("deleteProjectV2"), okRoute("updateLabel"), metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	if err := c.RemoveProject(ctx, "go-tui"); err != nil {
		t.Fatal(err)
	}
	requests := gh.Requests()
	if requests[3].Variables["description"] != "removed: o/go-tui" {
		t.Fatalf("label = %v", requests[3].Variables)
	}
	query := requests[1].Variables["open"].(string)
	if !strings.Contains(query, `label:"repo:go-tui"`) || !strings.Contains(query, "repo:o/tracker") {
		t.Fatalf("search = %s", query)
	}
}

func TestRemoveProjectWithoutBoard(t *testing.T) {
	c, gh := newClient(t, summaryRoute(0), okRoute("updateLabel"), metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	if err := c.RemoveProject(ctx, "bare"); err != nil {
		t.Fatal(err)
	}
	for _, req := range gh.Requests() {
		if strings.Contains(req.Description, "deleteProjectV2") {
			t.Fatal("no board to delete")
		}
	}
}

func TestRemoveProjectRefusals(t *testing.T) {
	c, gh := newClient(t, boardOnlyRoute(), summaryRoute(2))
	wantError(t, c.RemoveProject(ctx, "go-tui"), "2 open tickets")
	wantCount(t, gh, 2)

	c, _ = newClient(t, metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	wantError(t, c.RemoveProject(ctx, "unknown"), "unknown project")

	c, _ = newClient(t, boardOnlyRoute(), fakegithub.Route{Match: "open: search", Status: 500, Body: "{}"})
	wantError(t, c.RemoveProject(ctx, "go-tui"), "counting tickets")

	c, _ = newClient(t, boardOnlyRoute(), summaryRoute(0), fakegithub.Route{Match: "deleteProjectV2", Status: 500, Body: "{}"})
	wantError(t, c.RemoveProject(ctx, "go-tui"), "deleting board")

	c, _ = newClient(t, boardOnlyRoute(), summaryRoute(0), okRoute("deleteProjectV2"), fakegithub.Route{Match: "updateLabel", Status: 500, Body: "{}"})
	wantError(t, c.RemoveProject(ctx, "go-tui"), "updating label")
}

func TestProjectIn(t *testing.T) {
	// Exercise discovery inside a real enclosing checkout, even with system scratch.
	t.Setenv("GOTMPDIR", gitRepo(t))
	t.Run("scratch inside a checkout", checkProjectIn)
}

func checkProjectIn(t *testing.T) {
	t.Setenv("GH_TOKEN", "tok")
	t.Setenv("GH_REPO", "")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	repo := gitRepo(t)
	t.Chdir(repo)

	c, gh := newClient(t)
	project, ok, err := c.ProjectIn(ctx, here(t), false)
	if err != nil || !ok || project.Name != "go-tui" {
		t.Fatalf("project = %+v, ok = %v, err = %v", project, ok, err)
	}
	wantCount(t, gh, 0)
	if detected, err := CurrentRepo(); err != nil || detected.String() != "o/go-tui" {
		t.Fatalf("CurrentRepo() = %v, %v", detected, err)
	}

	if out, err := exec.Command("git", "-C", repo, "remote", "set-url", "origin", "git@github.com:o/unregistered.git").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	_, ok, err = c.ProjectIn(ctx, here(t), false)
	if err != nil || ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	wantCount(t, gh, 0)

	gh.Add(metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	if _, ok, err = c.ProjectIn(ctx, here(t), true); err != nil || ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	wantCount(t, gh, 2)

	outsideRepository(t)
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	reasons := []struct {
		name  string
		setup func()
		want  string
	}{
		{"no checkout", func() {}, "no git checkout here"},
		{"no remote", func() { git("remote", "remove", "origin"); t.Chdir(repo) }, "this checkout has no git remote"},
		{"no GitHub remote", func() { git("remote", "add", "origin", "git@gitlab.example.com:o/go-tui.git") }, "no git remote points to a GitHub host the GitHub CLI is logged in to"},
		{"another host", func() { t.Setenv("GH_REPO", "ghe.example.com/o/go-tui") }, "ghe.example.com/o/go-tui is on ghe.example.com, and projects are github.com repositories"},
	}
	for _, tt := range reasons {
		tt.setup()
		if _, err := CurrentRepo(); err == nil || err.Error() != tt.want {
			t.Fatalf("%s: CurrentRepo() error = %v, want exactly %q", tt.name, err, tt.want)
		}
	}
	t.Setenv("GH_REPO", "not a repository")
	if _, err := CurrentRepo(); err == nil || !strings.Contains(err.Error(), `expected the "[HOST/]OWNER/REPO" format`) {
		t.Fatalf("a bad GH_REPO keeps go-gh's reason: %v", err)
	}
	t.Setenv("GH_REPO", "")
	t.Setenv("PATH", t.TempDir())
	_, err = CurrentRepo()
	if err == nil || !strings.Contains(err.Error(), "git executable") || strings.Contains(err.Error(), "\n") || err.Error() == "no git checkout here" {
		t.Fatalf("without git, go-gh's reason stays, on one line: %v", err)
	}
}

func TestProjectRepoReadsTheCache(t *testing.T) {
	c, gh := newClient(t)
	repo, ok, err := c.ProjectRepo(ctx, "lib")
	if err != nil || !ok || repo != "o/lib" {
		t.Fatalf("ProjectRepo(lib) = %q, %v, %v", repo, ok, err)
	}
	if _, ok, err = c.ProjectRepo(ctx, "missing"); err != nil || ok {
		t.Fatalf("an unregistered name: ok = %v, err = %v", ok, err)
	}
	wantCount(t, gh, 0)

	failing := fakegithub.New(fakegithub.Route{Match: "labels(first: 100, query", Status: 500, Body: "{}"})
	_, _, err = clientWith(t, failing, nil).ProjectRepo(ctx, "lib")
	wantError(t, err, "reading tracker metadata")
}

func TestProjectInFailures(t *testing.T) {
	t.Setenv("GH_TOKEN", "tok")
	t.Setenv("GH_REPO", "o/unregistered")
	c, _ := newClient(t, fakegithub.Route{Match: "labels(first: 100, query", Status: 500, Body: "{}"})
	_, _, err := c.ProjectIn(ctx, here(t), true)
	wantError(t, err, "reading tracker metadata")

	public := clientWith(t, fakegithub.New(), &metadata{})
	public.requirePrivate = true
	public.meta = nil
	_, _, err = public.ProjectIn(ctx, here(t), false)
	wantError(t, err, "not private")
}

func TestProjectSummaryCountsBoardOnlyTickets(t *testing.T) {
	counts := fakegithub.Route{Match: "open: search", Body: `{"data":{"open":{"issueCount":2},"closed":{"issueCount":5}}}`}
	onBoard := boardOnlyRoute(
		ticketSpec{number: 5, status: "Todo"},
		ticketSpec{number: 6, status: "Done", state: "CLOSED"},
		ticketSpec{number: 7, status: "Todo", project: "lib"},
	)
	c, gh := newClient(t, onBoard, counts)
	summary, err := c.ProjectSummary(ctx, testMetadata().Projects[0])
	if err != nil {
		t.Fatal(err)
	}
	if summary.Open != 3 || summary.Closed != 6 {
		t.Fatalf("summary = %+v: a ticket labelled for another project must not count", summary)
	}
	wantCount(t, gh, 2)
	if got := gh.Requests()[0].Variables["search"]; got != `repo:o/tracker is:issue sort:created-asc project:o/1 -label:"repo:go-tui"` {
		t.Fatalf("search = %v", got)
	}

	bare, gh := newClient(t, counts)
	if _, err := bare.ProjectSummary(ctx, testMetadata().Projects[2]); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 1)
}

func TestProjectSummaryBoardSearchFailure(t *testing.T) {
	c, _ := newClient(t, fakegithub.Route{Match: "search(query: $search", Status: 500, Body: "{}"})
	_, err := c.ProjectSummary(ctx, testMetadata().Projects[0])
	wantError(t, err, "counting tickets")
}

func TestRemoveProjectLabelsClosedBoardOnlyTickets(t *testing.T) {
	counts := fakegithub.Route{Match: "open: search", Body: `{"data":{"open":{"issueCount":0},"closed":{"issueCount":0}}}`}
	closed := []ticketSpec{{number: 5, status: "Done", state: "CLOSED"}, {number: 6, status: "Done", state: "CLOSED"}}
	c, gh := newClient(t, boardOnlyRoute(closed...), counts,
		okRoute("addLabelsToLabelable"), okRoute("deleteProjectV2"), okRoute("updateLabel"), metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	if err := c.RemoveProject(ctx, "go-tui"); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 7)
	requests := gh.Requests()
	write := requests[2].Description
	if !strings.Contains(write, `labelableId: "I5"`) || !strings.Contains(write, `labelableId: "I6"`) || !strings.Contains(write, `labelIds: ["L1"]`) {
		t.Fatalf("write = %s", write)
	}
	if !strings.Contains(requests[3].Description, "deleteProjectV2") {
		t.Fatalf("the label must land before the board goes: %s", requests[3].Description)
	}

	failing, _ := newClient(t, boardOnlyRoute(ticketSpec{number: 5, status: "Done", state: "CLOSED"}), counts,
		fakegithub.Route{Match: "addLabelsToLabelable", Status: 500, Body: "{}"})
	wantError(t, failing.RemoveProject(ctx, "go-tui"), "labelling closed tickets")
}

// GitHub can omit a board it just created from the tracker's projects, so a
// registration keeps the board it created, and the commands after it find it
// without asking GitHub again.
func TestARegistrationIsUsableAtOnce(t *testing.T) {
	lagging := strings.Replace(metadataJSON, `"labels":{"nodes":[`, `"labels":{"nodes":[{"id":"LN","name":"repo:new","description":"o/new"},`, 1)
	first := emptyMetadata()
	first.Times = 1
	routes := append([]fakegithub.Route{first, {Match: "labels(first: 100, query", Body: lagging},
		visibilityRoute(`{"data":{"r0":{"isPrivate":true},"r1":{"isPrivate":true}}}`)}, boardRoutes()...)
	cache := t.TempDir()
	open := func(gh *fakegithub.GitHub) *Client {
		c, err := New(Options{Repo: Repo{"o", "tracker"}, CacheDir: cache, Transport: gh, Token: "tok"})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	gh := fakegithub.New(routes...)
	if project, err := open(gh).AddProject(ctx, Repo{"o", "new"}); err != nil || project.BoardID != "BN" {
		t.Fatalf("project = %+v, err = %v", project, err)
	}
	wantCount(t, gh, 8)

	gh = fakegithub.New(createRoute(), addToBoardRoute(), okRoute("updateProjectV2ItemFieldValue"))
	ticket, err := open(gh).CreateTicket(ctx, CreateOptions{Project: "new", Title: "t"})
	if err != nil || ticket.Number != 50 {
		t.Fatalf("issue create after project add: %+v, %v", ticket, err)
	}
	wantCount(t, gh, 3)

	gh = fakegithub.New(ticketRoute(ticketSpec{number: 1, project: "go-tui", status: StatusTodo}), okRoute("addLabelsToLabelable"),
		addToBoardRoute(), okRoute("updateProjectV2ItemFieldValue"))
	if err := open(gh).MoveTicket(ctx, 1, "new"); err != nil {
		t.Fatalf("issue move after project add: %v", err)
	}
	wantCount(t, gh, 4)

	gh = fakegithub.New(ticketRoute(ticketSpec{number: 1, project: "new"}), addToBoardRoute(), okRoute("updateProjectV2ItemFieldValue"))
	if err := open(gh).SetStatus(ctx, 1, StatusTodo); err != nil {
		t.Fatalf("issue status set after project add: %v", err)
	}
	wantCount(t, gh, 3)
}

// A cache written before a project's board appeared, by another run or another
// account, refreshes once in every command that needs the board.
func TestABoardCreatedSinceTheCacheIsFound(t *testing.T) {
	c, gh := newClient(t, append(bareRefresh(true), createRoute(), addToBoardRoute(), okRoute("updateProjectV2ItemFieldValue"))...)
	if _, err := c.CreateTicket(ctx, CreateOptions{Project: "bare", Title: "t"}); err != nil {
		t.Fatalf("issue create: %v", err)
	}
	wantCount(t, gh, 5)

	c, gh = newClient(t, append(bareRefresh(true), ticketRoute(ticketSpec{number: 1, project: "go-tui", status: StatusTodo}),
		okRoute("addLabelsToLabelable"), addToBoardRoute(), okRoute("updateProjectV2ItemFieldValue"))...)
	if err := c.MoveTicket(ctx, 1, "bare"); err != nil {
		t.Fatalf("issue move: %v", err)
	}
	wantCount(t, gh, 6)

	c, gh = newClient(t, append(bareRefresh(true), ticketRoute(ticketSpec{number: 1, project: "bare"}), addToBoardRoute(), okRoute("updateProjectV2ItemFieldValue"))...)
	if err := c.SetStatus(ctx, 1, StatusTodo); err != nil {
		t.Fatalf("issue status set: %v", err)
	}
	wantCount(t, gh, 5)
}
