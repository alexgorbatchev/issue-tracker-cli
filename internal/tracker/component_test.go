package tracker

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/alexgorbatchev/issue-tracker-cli/internal/fakegithub"
)

// monoMetadata registers the repository o/mono on board B1 with three components,
// one nested inside another, next to an unrelated project.
func monoMetadata() *metadata {
	shared := func(project Project) Project {
		project.BoardID, project.BoardURL, project.StatusFieldID, project.StatusOptions = "B1", "https://b/1", "F1", statusOptions()
		return project
	}
	return &metadata{
		RepositoryID: "REPO", OwnerID: "OWNER", HeadOID: "headsha", IsPrivate: true,
		Projects: []Project{
			shared(Project{Name: "mono", Repo: "o/mono", LabelID: "LM"}),
			shared(Project{Name: "mono/web", Repo: "o/mono", Path: "apps/web", LabelID: "LW"}),
			shared(Project{Name: "mono/admin", Repo: "o/mono", Path: "apps/web/admin", LabelID: "LA"}),
			shared(Project{Name: "mono/api", Repo: "o/mono", Path: "services/api", LabelID: "LS"}),
			{Name: "lib", Repo: "o/lib", LabelID: "L2", BoardID: "B2", BoardURL: "https://b/2", StatusFieldID: "F2", StatusOptions: statusOptions()},
		},
	}
}

func monoClient(t *testing.T, routes ...fakegithub.Route) (*Client, *fakegithub.GitHub) {
	t.Helper()
	gh := fakegithub.New(routes...)
	return clientWith(t, gh, monoMetadata()), gh
}

// registrations answers the component check of a listing with these labels, given
// as name and description pairs.
func registrations(pairs ...string) string {
	nodes := make([]string, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		nodes = append(nodes, `{"id":"X","name":"`+pairs[i]+`","description":"`+pairs[i+1]+`"}`)
	}
	return `"registrations":{"nodes":[` + strings.Join(nodes, ",") + `]},`
}

// monoRegistrations is what GitHub reports for the cached components of o/mono.
var monoRegistrations = registrations(
	"repo:mono/web", "o/mono:apps/web",
	"repo:mono/admin", "o/mono:apps/web/admin",
	"repo:mono/api", "o/mono:services/api",
	"repo:mono-other", "o/mono-other",
	"bug", "Something isn't working",
)

// monoListRoute is listRoute with the component check answered.
func monoListRoute(registered string, specs ...ticketSpec) fakegithub.Route {
	route := listRoute(specs...)
	route.Body = strings.Replace(route.Body, `"isPrivate":true,`, `"isPrivate":true,`+registered, 1)
	return route
}

func TestParseRegistration(t *testing.T) {
	tests := []struct {
		name, description string
		want              Project
		ok                bool
	}{
		{"mono", "o/mono", Project{Name: "mono", Repo: "o/mono"}, true},
		{"mono/api", "o/mono:services/api", Project{Name: "mono/api", Repo: "o/mono", Path: "services/api"}, true},
		{"mono", "o/other", Project{}, false},
		{"mono", "", Project{}, false},
		{"mono/api", "o/other:services/api", Project{}, false},
		{"mono/a/b", "o/mono:a/b", Project{}, false},
		{"mono/", "o/mono:a", Project{}, false},
		{"mono/api", "o/mono:", Project{}, false},
		{"mono/api", "o/mono:../api", Project{}, false},
		{"mono/api", "o/mono:/abs", Project{}, false},
		{"mono/api", "o/mono:.", Project{}, false},
		{"mono/api", "o/mono:a//b", Project{}, false},
		{"mono/api", "o/mono:a:b", Project{}, false},
		{"mono/api", "bad:services/api", Project{}, false},
		{"x/mono", "x/mono", Project{Name: "x/mono", Repo: "x/mono"}, true},
		{"x/mono/api", "x/mono:services/api", Project{Name: "x/mono/api", Repo: "x/mono", Path: "services/api"}, true},
		{"mono/api", "x/mono:services/api", Project{Name: "mono/api", Repo: "x/mono", Path: "services/api"}, true},
		{"y/mono", "x/mono", Project{}, false},
		{"y/mono/api", "x/mono:services/api", Project{}, false},
	}
	for _, tt := range tests {
		got, ok := parseRegistration(tt.name, tt.description)
		if ok != tt.ok || got.Name != tt.want.Name || got.Repo != tt.want.Repo || got.Path != tt.want.Path {
			t.Errorf("parseRegistration(%q, %q) = %+v, %v; want %+v, %v", tt.name, tt.description, got, ok, tt.want, tt.ok)
		}
	}
}

func TestRefreshGivesComponentsTheirRepositoryBoard(t *testing.T) {
	body := strings.Replace(metadataJSON, `{"id":"L1","name":"repo:go-tui","description":"o/go-tui"},`,
		`{"id":"L1","name":"repo:go-tui","description":"o/go-tui"},{"id":"LC","name":"repo:go-tui/docs","description":"o/go-tui:site/docs"},{"id":"LX","name":"repo:go-tui/x","description":"o/elsewhere:x"},`, 1)
	gh := fakegithub.New(fakegithub.Route{Match: "labels(first: 100, query", Body: body}, visibilityRoute(`{"data":{"r0":{"isPrivate":true},"r1":{"isPrivate":true}}}`))
	projects, err := clientWith(t, gh, nil).Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("a component registered for another repository must be dropped: %+v", projects)
	}
	component := projects[1]
	if component.Name != "go-tui/docs" || component.Path != "site/docs" || component.LabelID != "LC" || component.BoardID != "B1" || component.StatusOptions[StatusDoing] != "G" {
		t.Fatalf("component = %+v", component)
	}
	if !component.Component() || component.Root() != "go-tui" || projects[0].Component() || projects[0].Root() != "go-tui" {
		t.Fatalf("roots = %q, %q", projects[0].Root(), component.Root())
	}
}

func TestProjectAtPicksTheDeepestComponent(t *testing.T) {
	meta := monoMetadata()
	tests := []struct{ repo, dir, want string }{
		{"o/mono", "", "mono"},
		{"o/mono", "docs", "mono"},
		{"O/Mono", "apps/web", "mono/web"},
		{"o/mono", "apps/web/src", "mono/web"},
		{"o/mono", "apps/web/admin/src", "mono/admin"},
		{"o/mono", "apps/webkit", "mono"},
		{"o/mono", "services/api", "mono/api"},
		{"o/lib", "services/api", "lib"},
	}
	for _, tt := range tests {
		if got, ok := meta.projectAt(tt.repo, tt.dir); !ok || got.Name != tt.want {
			t.Errorf("projectAt(%q, %q) = %q, %v; want %q", tt.repo, tt.dir, got.Name, ok, tt.want)
		}
	}
	meta.Projects = meta.Projects[1:]
	if got, ok := meta.projectAt("o/mono", "docs"); ok {
		t.Fatalf("outside every component of an unregistered repository: %+v", got)
	}
	if got, ok := meta.projectAt("o/mono", "apps/web"); !ok || got.Name != "mono/web" {
		t.Fatalf("a component works without its repository's project: %+v", got)
	}
}

func TestProjectInFollowsTheDirectory(t *testing.T) {
	t.Setenv("GH_TOKEN", "tok")
	t.Setenv("GH_REPO", "")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	repo := gitRepo(t)
	dir := filepath.Join(repo, "apps", "web", "src")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := monoMetadata()
	for i := range meta.Projects {
		if meta.Projects[i].Repo == "o/mono" {
			meta.Projects[i].Repo = "o/go-tui"
		}
	}
	gh := fakegithub.New()
	c := clientWith(t, gh, meta)

	t.Chdir(dir)
	project, ok, err := c.ProjectIn(ctx, here(t), false)
	if err != nil || !ok || project.Name != "mono/web" {
		t.Fatalf("in a component: project = %+v, ok = %v, err = %v", project, ok, err)
	}
	t.Chdir(repo)
	if project, ok, err = c.ProjectIn(ctx, here(t), false); err != nil || !ok || project.Name != "mono" {
		t.Fatalf("at the top: project = %+v, ok = %v, err = %v", project, ok, err)
	}
	t.Chdir(dir)
	t.Setenv("GH_REPO", "o/go-tui")
	if project, ok, err = c.ProjectIn(ctx, here(t), false); err != nil || !ok || project.Name != "mono" {
		t.Fatalf("GH_REPO names a repository, not a directory: project = %+v, ok = %v, err = %v", project, ok, err)
	}
	wantCount(t, gh, 0)

	t.Chdir(t.TempDir())
	t.Setenv("GH_REPO", "")
	if dir := currentDir(ctx); dir != "" {
		t.Fatalf("outside a checkout: dir = %q", dir)
	}
}

func TestAComponentLabelIsMoreSpecificThanItsRepository(t *testing.T) {
	for _, labels := range []string{
		`{"name":"repo:mono"},{"name":"repo:mono/api"}`,
		`{"name":"repo:mono/api"},{"name":"repo:mono"}`,
	} {
		spec := ticketSpec{number: 1}.json()
		node := strings.Replace(spec, `"labels":{"nodes":[]}`, `"labels":{"nodes":[`+labels+`]}`, 1) + "}"
		route := fakegithub.Route{Match: "isPrivate issue(number: $number)", Body: `{"data":{"repository":{"isPrivate":true,"issue":` + node + `}}}`}
		c, _ := monoClient(t, route)
		ticket, err := c.readTicket(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		if ticket.Project != "mono/api" {
			t.Fatalf("labels %s: project = %q", labels, ticket.Project)
		}
	}
}

func TestABoardOnlyTicketBelongsToTheRepository(t *testing.T) {
	c, _ := monoClient(t, ticketRoute(ticketSpec{number: 1, status: "Todo", otherBoard: "ELSEWHERE"}))
	ticket, err := c.readTicket(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Project != "mono" || ticket.Status != StatusTodo {
		t.Fatalf("an unlabelled ticket on a shared board must route to the repository: %+v", ticket)
	}
}

func TestARepositoryListIncludesItsComponents(t *testing.T) {
	onBoard := boardOnlyRoute(ticketSpec{number: 3, status: "Todo", created: "2026-02-01T00:00:00Z"})
	c, gh := monoClient(t, onBoard, monoListRoute(monoRegistrations,
		ticketSpec{number: 1, project: "mono/api", status: "Todo"},
		ticketSpec{number: 2, project: "mono", status: "Todo", created: "2026-03-01T00:00:00Z"},
	))
	tickets, total, err := c.ListTickets(ctx, ListOptions{Project: "mono"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(tickets) != 3 || tickets[0].Project != "mono/api" || tickets[1].Project != "mono" || tickets[1].Number != 3 {
		t.Fatalf("tickets = %+v, total = %d", tickets, total)
	}
	wantCount(t, gh, 2)
	search, list := gh.Requests()[0], gh.Requests()[1]
	for _, label := range []string{`-label:"repo:mono"`, `-label:"repo:mono/web"`, `-label:"repo:mono/admin"`, `-label:"repo:mono/api"`} {
		if !strings.Contains(search.Variables["search"].(string), label) {
			t.Fatalf("the board search must leave out every routing label of the repository, missing %s: %v", label, search.Variables["search"])
		}
	}
	labels := list.Variables["labels"].([]any)
	if len(labels) != 4 || labels[0] != "repo:mono" || labels[3] != "repo:mono/api" {
		t.Fatalf("labels = %v", labels)
	}
	if list.Variables["registrations"] != "repo:mono/" || list.Variables["checkRegistrations"] != true {
		t.Fatalf("the repository's components must be checked: %v", list.Variables)
	}
}

func TestAComponentListReadsItsBoardForComponentValues(t *testing.T) {
	onBoard := boardOnlyRoute(
		ticketSpec{number: 2, status: "Todo", component: "api"},
		ticketSpec{number: 3, status: "Todo", component: "web"},
		ticketSpec{number: 4, status: "Todo"},
	)
	c, gh := monoClient(t, onBoard, monoListRoute("", ticketSpec{number: 1, project: "mono/api", status: "Todo"}))
	tickets, total, err := c.ListTickets(ctx, ListOptions{Project: "mono/api"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(tickets) != 2 || tickets[0].Number != 1 || tickets[1].Number != 2 || tickets[1].Project != "mono/api" {
		t.Fatalf("a component keeps its labelled tickets and the board tickets that name it: %+v", tickets)
	}
	wantCount(t, gh, 2)
	search := gh.Requests()[0].Variables["search"].(string)
	for _, label := range []string{`-label:"repo:mono"`, `-label:"repo:mono/web"`, `-label:"repo:mono/api"`} {
		if !strings.Contains(search, label) {
			t.Fatalf("the board search leaves out every label of the repository, missing %s: %q", label, search)
		}
	}
	request := gh.Requests()[1]
	if labels := request.Variables["labels"].([]any); len(labels) != 1 || labels[0] != "repo:mono/api" {
		t.Fatalf("labels = %v", labels)
	}
	if request.Variables["checkRegistrations"] != false {
		t.Fatalf("a component has no components to check: %v", request.Variables)
	}
}

func TestASearchCoversTheRepositoryAndItsComponents(t *testing.T) {
	results := boardOnlyRoute()
	results.Times = 0
	results.Body = strings.Replace(results.Body, `"isPrivate":true`, strings.TrimSuffix(`"isPrivate":true,`+monoRegistrations, ","), 1)
	c, gh := monoClient(t, results)
	if _, _, err := c.ListTickets(ctx, ListOptions{Project: "mono", Search: "crash"}); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 2)
	search := gh.Requests()[1].Variables["search"].(string)
	if !strings.Contains(search, `label:"repo:mono","repo:mono/web","repo:mono/admin","repo:mono/api"`) || !strings.HasSuffix(search, "crash") {
		t.Fatalf("search = %q", search)
	}
}

// componentAdded is metadata in which another machine has registered mono/docs.
func componentAdded() fakegithub.Route {
	body := strings.Replace(metadataJSON, `{"id":"L1","name":"repo:go-tui","description":"o/go-tui"},`,
		`{"id":"LM","name":"repo:mono","description":"o/mono"},{"id":"LD","name":"repo:mono/docs","description":"o/mono:docs"},`, 1)
	body = strings.Replace(body, `"title":"go-tui"`, `"title":"mono"`, 1)
	return fakegithub.Route{Match: "labels(first: 100, query", Body: body}
}

func TestAnotherOwnersComponentIsNamedAfterItsProject(t *testing.T) {
	body := strings.Replace(metadataJSON, `{"id":"L1","name":"repo:go-tui","description":"o/go-tui"},`, `{"id":"LM","name":"repo:x/mono","description":"x/mono"},`, 1)
	body = strings.Replace(body, `"title":"go-tui"`, `"title":"x/mono"`, 1)
	label := fakegithub.Route{Match: "label(name: $label)", Body: `{"data":{"repository":{"label":null}}}`}
	created := fakegithub.Route{Match: "createLabel(input", Body: `{"data":{"createLabel":{"label":{"id":"LN"}}}}`}
	gh := fakegithub.New(fakegithub.Route{Match: "labels(first: 100, query", Body: body}, visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`), createdField("api"), label, created)
	c := clientWith(t, gh, nil)
	project, err := c.AddComponent(ctx, Repo{"x", "mono"}, "services/api", "")
	if err != nil {
		t.Fatal(err)
	}
	if project.Name != "x/mono/api" || project.Root() != "x/mono" || project.componentName() != "api" || project.BoardID != "B1" {
		t.Fatalf("project = %+v, root = %q", project, project.Root())
	}
	if vars := gh.Requests()[len(gh.Requests())-1].Variables; vars["name"] != "repo:x/mono/api" || vars["description"] != "x/mono:services/api" {
		t.Fatalf("label = %v", vars)
	}
	if root := (Project{Name: "x/mono", Repo: "x/mono"}); root.Root() != "x/mono" {
		t.Fatalf("a repository is its own root: %q", root.Root())
	}
}

func TestAddComponentKeepsAnotherOwnersRepository(t *testing.T) {
	body := strings.Replace(metadataJSON, `{"id":"L1","name":"repo:go-tui","description":"o/go-tui"},`,
		`{"id":"LC","name":"repo:cli","description":"o/cli"},{"id":"LG","name":"repo:cli/go-gh","description":"cli/go-gh"},`, 1)
	body = strings.Replace(body, `"title":"go-tui"`, `"title":"cli"`, 1)
	gh := fakegithub.New(fakegithub.Route{Match: "labels(first: 100, query", Body: body}, visibilityRoute(`{"data":{"r0":{"isPrivate":true},"r1":{"isPrivate":true}}}`))
	_, err := clientWith(t, gh, nil).AddComponent(ctx, Repo{"o", "cli"}, "go-gh", "")
	wantError(t, err, `project "cli/go-gh" is already registered for cli/go-gh`)
	wantCount(t, gh, 2)
}

func TestAddComponentTakesBackOnlyItsOwnRemovedLabel(t *testing.T) {
	visibility := visibilityRoute(`{"data":{"r0":{"isPrivate":true},"r1":{"isPrivate":true}}}`)
	removed := func(description string) fakegithub.Route {
		return fakegithub.Route{Match: "label(name: $label)", Body: fmt.Sprintf(`{"data":{"repository":{"label":{"id":"OLD","description":%q}}}}`, description)}
	}
	gh := fakegithub.New(componentAdded(), visibility, removed("removed: o/mono:services/api"), createdField("api"), okRoute("updateLabel"))
	project, err := clientWith(t, gh, nil).AddComponent(ctx, Repo{"o", "mono"}, "services/api", "")
	if err != nil || project.LabelID != "OLD" {
		t.Fatalf("a removed component takes its label back: %+v, %v", project, err)
	}
	if vars := gh.Requests()[len(gh.Requests())-1].Variables; vars["id"] != "OLD" || vars["description"] != "o/mono:services/api" {
		t.Fatalf("label = %v", vars)
	}

	gh = fakegithub.New(componentAdded(), visibility, removed("removed: o/mono:old/api"))
	_, err = clientWith(t, gh, nil).AddComponent(ctx, Repo{"o", "mono"}, "services/api", "")
	wantError(t, err, "label repo:mono/api holds the closed tickets of removed project o/mono:old/api")
	wantCount(t, gh, 3)
}

func TestAddComponentUsesALegacyRootName(t *testing.T) {
	body := strings.Replace(metadataJSON, `{"id":"L1","name":"repo:go-tui","description":"o/go-tui"},`, `{"id":"LM","name":"repo:mono","description":"x/mono"},`, 1)
	body = strings.Replace(body, `"title":"go-tui"`, `"title":"mono"`, 1)
	label := fakegithub.Route{Match: "label(name: $label)", Body: `{"data":{"repository":{"label":null}}}`}
	created := fakegithub.Route{Match: "createLabel(input", Body: `{"data":{"createLabel":{"label":{"id":"LN"}}}}`}
	gh := fakegithub.New(fakegithub.Route{Match: "labels(first: 100, query", Body: body}, visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`), createdField("api"), label, created)
	project, err := clientWith(t, gh, nil).AddComponent(ctx, Repo{"x", "mono"}, "services/api", "")
	if err != nil || project.Name != "mono/api" || project.Root() != "mono" {
		t.Fatalf("a component of a short-named repository takes its name: %+v, %v", project, err)
	}
	if vars := gh.Requests()[len(gh.Requests())-1].Variables; vars["name"] != "repo:mono/api" || vars["description"] != "x/mono:services/api" {
		t.Fatalf("label = %v", vars)
	}
	wantCount(t, gh, 5)
}

func TestAComponentRegisteredElsewhereRefreshesTheList(t *testing.T) {
	meta := monoMetadata()
	meta.Projects = meta.Projects[:1]
	stale := monoListRoute(registrations("repo:mono/docs", "o/mono:docs"))
	stale.Times = 1
	gh := fakegithub.New(boardOnlyRoute(), stale, componentAdded(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`), boardOnlyRoute(),
		monoListRoute(registrations("repo:mono/docs", "o/mono:docs"), ticketSpec{number: 4, project: "mono/docs", status: "Todo"}))
	c := clientWith(t, gh, meta)
	tickets, _, err := c.ListTickets(ctx, ListOptions{Project: "mono"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tickets) != 1 || tickets[0].Project != "mono/docs" {
		t.Fatalf("tickets = %+v", tickets)
	}
	wantCount(t, gh, 6)
	if labels := gh.Requests()[5].Variables["labels"].([]any); len(labels) != 2 || labels[1] != "repo:mono/docs" {
		t.Fatalf("the second read must include the new component: %v", labels)
	}
}

func TestAStaleCheckRefreshesOnlyOnce(t *testing.T) {
	meta := monoMetadata()
	meta.Projects = meta.Projects[:1]
	ghost := monoListRoute(registrations("repo:mono/ghost", "o/mono:ghost"))
	gh := fakegithub.New(boardOnlyRoute(), ghost, metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	_, _, err := clientWith(t, gh, meta).ListTickets(ctx, ListOptions{Project: "mono"})
	wantError(t, err, `unknown project "mono"`)

	gh = fakegithub.New(boardOnlyRoute(), ghost, componentAdded(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`), boardOnlyRoute())
	tickets, _, err := clientWith(t, gh, meta).ListTickets(ctx, ListOptions{Project: "mono"})
	if err != nil || len(tickets) != 0 {
		t.Fatalf("a label search that still disagrees must not loop: %+v, %v", tickets, err)
	}
	wantCount(t, gh, 6)

	gh = fakegithub.New(boardOnlyRoute(), ghost, fakegithub.Route{Match: "labels(first: 100, query", Status: 500, Body: "{}"})
	_, _, err = clientWith(t, gh, meta).ListTickets(ctx, ListOptions{Project: "mono"})
	wantError(t, err, "reading tracker metadata")
}

func TestStaleComponents(t *testing.T) {
	meta := monoMetadata()
	root := meta.Projects[0]
	moved := registrations("repo:mono/web", "o/mono:web", "repo:mono/admin", "o/mono:apps/web/admin", "repo:mono/api", "o/mono:services/api")
	tests := []struct {
		name    string
		project Project
		labels  string
		want    bool
	}{
		{"same", root, monoRegistrations, false},
		{"moved", root, moved, true},
		{"removed", root, registrations("repo:mono/web", "o/mono:apps/web"), true},
		{"component", meta.Projects[1], "", false},
		{"unscoped", Project{}, "", false},
	}
	for _, tt := range tests {
		var page ticketPage
		route := monoListRoute(tt.labels)
		gh := fakegithub.New(route)
		page, err := clientWith(t, gh, meta).issuesPage([]string{stateOpen}, nil, root)(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := meta.staleComponents(tt.project, page.Registrations); got != tt.want {
			t.Errorf("%s: stale = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestMovingWithinARepositoryKeepsTheBoardItem(t *testing.T) {
	c, gh := monoClient(t, ticketRoute(ticketSpec{number: 1, project: "mono", status: "Doing"}), okRoute("addLabelsToLabelable"))
	if err := c.MoveTicket(ctx, 1, "mono/api"); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 2)
	write := lastDescription(gh)
	if !strings.Contains(write, `labelIds: ["LS"]`) || !strings.Contains(write, `removeLabelsFromLabelable(input: {labelableId: "I1", labelIds: ["LM"]})`) || strings.Contains(write, "deleteProjectV2Item") {
		t.Fatalf("write = %s", write)
	}

	c, gh = monoClient(t, ticketRoute(ticketSpec{number: 2, project: "mono/api", status: "Todo"}), okRoute("addLabelsToLabelable"),
		fakegithub.Route{Match: "addProjectV2ItemById", Body: `{"data":{"addProjectV2ItemById":{"item":{"id":"NEW"}}}}`}, okRoute("updateProjectV2ItemFieldValue"))
	if err := c.MoveTicket(ctx, 2, "lib"); err != nil {
		t.Fatal(err)
	}
	if write := gh.Requests()[1].Description; !strings.Contains(write, "deleteProjectV2Item") {
		t.Fatalf("leaving the shared board drops the item: %s", write)
	}
}

func TestARepositorySummaryCountsItsComponents(t *testing.T) {
	counts := fakegithub.Route{Match: "open: search", Body: `{"data":{"repository":{` + strings.TrimSuffix(monoRegistrations, ",") + `},"open":{"issueCount":2},"closed":{"issueCount":5}}}`}
	c, gh := monoClient(t, boardOnlyRoute(), counts)
	summary, err := c.ProjectSummary(ctx, monoMetadata().Projects[0])
	if err != nil {
		t.Fatal(err)
	}
	if summary.Open != 2 || summary.Closed != 5 {
		t.Fatalf("summary = %+v", summary)
	}
	wantCount(t, gh, 2)
	if open := gh.Requests()[1].Variables["open"].(string); !strings.Contains(open, `label:"repo:mono","repo:mono/web","repo:mono/admin","repo:mono/api"`) {
		t.Fatalf("open = %q", open)
	}

	c, gh = monoClient(t, boardOnlyRoute(ticketSpec{number: 9, status: "Todo", component: "api"}), summaryRoute(1))
	if summary, err = c.ProjectSummary(ctx, monoMetadata().Projects[3]); err != nil || summary.Open != 2 {
		t.Fatalf("a component counts the board tickets that name it: summary = %+v, err = %v", summary, err)
	}
	wantCount(t, gh, 2)
}

func TestAStaleSummaryCountsAgain(t *testing.T) {
	meta := monoMetadata()
	meta.Projects = meta.Projects[:1]
	stale := fakegithub.Route{Match: "open: search", Times: 1, Body: `{"data":{"repository":{` + strings.TrimSuffix(registrations("repo:mono/docs", "o/mono:docs"), ",") + `},"open":{"issueCount":0},"closed":{"issueCount":0}}}`}
	fresh := fakegithub.Route{Match: "open: search", Body: `{"data":{"repository":{` + strings.TrimSuffix(registrations("repo:mono/docs", "o/mono:docs"), ",") + `},"open":{"issueCount":3},"closed":{"issueCount":0}}}`}
	gh := fakegithub.New(boardOnlyRoute(), stale, componentAdded(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`), boardOnlyRoute(), fresh)
	summary, err := clientWith(t, gh, meta).ProjectSummary(ctx, meta.Projects[0])
	if err != nil || summary.Open != 3 {
		t.Fatalf("summary = %+v, err = %v", summary, err)
	}
	wantCount(t, gh, 6)
}

func TestRemovingAComponentKeepsTheBoard(t *testing.T) {
	c, gh := monoClient(t, boardOnlyRoute(), summaryRoute(0), okRoute("updateLabel"), componentAdded(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	if err := c.RemoveProject(ctx, "mono/api"); err != nil {
		t.Fatal(err)
	}
	for _, req := range gh.Requests() {
		if strings.Contains(req.Description, "deleteProjectV2(") {
			t.Fatalf("a component has no board of its own: %s", req.Description)
		}
	}
	wantCount(t, gh, 5)

	c, _ = monoClient(t, boardOnlyRoute(), fakegithub.Route{Match: "open: search", Body: `{"data":{"repository":{` + strings.TrimSuffix(monoRegistrations, ",") + `},"open":{"issueCount":0},"closed":{"issueCount":0}}}`})
	wantError(t, c.RemoveProject(ctx, "mono"), `project "mono" has components: remove mono/web first`)

	c, _ = monoClient(t, fakegithub.Route{Match: "open: search", Status: 500, Body: "{}"})
	wantError(t, c.RemoveProject(ctx, "mono/api"), "counting tickets")
}

// createdField answers the creation of the Component field with these options.
func createdField(names ...string) fakegithub.Route {
	options := make([]string, 0, len(names))
	for _, name := range names {
		options = append(options, `{"id":"O-`+name+`","name":"`+name+`"}`)
	}
	return fakegithub.Route{Match: "createProjectV2Field(input", Body: `{"data":{"createProjectV2Field":{"projectV2Field":{"id":"FC","options":[` + strings.Join(options, ",") + `]}}}}`}
}

func TestAddComponent(t *testing.T) {
	registered := componentAdded()
	visibility := visibilityRoute(`{"data":{"r0":{"isPrivate":true},"r1":{"isPrivate":true}}}`)
	label := fakegithub.Route{Match: "label(name: $label)", Body: `{"data":{"repository":{"label":null}}}`}
	created := fakegithub.Route{Match: "createLabel(input", Body: `{"data":{"createLabel":{"label":{"id":"LN"}}}}`}
	gh := fakegithub.New(registered, visibility, createdField("api"), label, created)
	c := clientWith(t, gh, nil)
	project, err := c.AddComponent(ctx, Repo{"o", "mono"}, "services/api", "")
	if err != nil {
		t.Fatal(err)
	}
	if project.Name != "mono/api" || project.Path != "services/api" || project.LabelID != "LN" || project.BoardID != "B1" || project.Repo != "o/mono" || project.ComponentOptions["api"] != "O-api" {
		t.Fatalf("project = %+v", project)
	}
	wantCount(t, gh, 5)
	requests := gh.Requests()
	if !strings.Contains(requests[2].Description, "label(name: $label)") {
		t.Fatalf("the label must be checked before anything is written: %s", requests[2].Description)
	}
	field := requests[3]
	if !strings.Contains(field.Description, "createProjectV2Field") || field.Variables["board"] != "B1" || field.Variables["name"] != "Component" {
		t.Fatalf("the Component field must be created before the label: %s %v", field.Description, field.Variables)
	}
	if option := field.Variables["options"].([]any)[0].(map[string]any); option["name"] != "api" || option["description"] != "services/api" || option["color"] != "GRAY" || option["id"] != nil {
		t.Fatalf("option = %v", option)
	}
	if vars := requests[4].Variables; vars["name"] != "repo:mono/api" || vars["description"] != "o/mono:services/api" {
		t.Fatalf("label = %v", vars)
	}
	cached, err := c.lookupProject(ctx, "mono/api")
	if err != nil || cached.LabelID != "LN" || cached.ComponentFieldID != "FC" {
		t.Fatalf("the new component must be cached without a refresh: %+v, %v", cached, err)
	}
	if root, _ := c.lookupProject(ctx, "mono"); root.ComponentOptions["api"] != "O-api" {
		t.Fatalf("the repository shares the field: %+v", root)
	}
	wantCount(t, gh, 5)

	gh = fakegithub.New(registered, visibility, createdField("console"), label, created)
	if project, err = clientWith(t, gh, nil).AddComponent(ctx, Repo{"o", "mono"}, "apps/web/admin", "console"); err != nil || project.Name != "mono/console" {
		t.Fatalf("project = %+v, err = %v", project, err)
	}

	gh = fakegithub.New(registered, visibility, createdField("docs"))
	if project, err = clientWith(t, gh, nil).AddComponent(ctx, Repo{"o", "mono"}, "docs", ""); err != nil || project.LabelID != "LD" || project.ComponentOptions["docs"] != "O-docs" {
		t.Fatalf("repeating a registration gives an older component its option: %+v, %v", project, err)
	}
	wantCount(t, gh, 3)

	gh = fakegithub.New(withField(registered, `{"id":"O-docs","name":"docs"}`), visibility)
	if project, err = clientWith(t, gh, nil).AddComponent(ctx, Repo{"o", "mono"}, "docs", ""); err != nil || project.ComponentOptions["docs"] != "O-docs" {
		t.Fatalf("repeating a complete registration is safe: %+v, %v", project, err)
	}
	wantCount(t, gh, 2)
}

// withField gives the mono board of a metadata route a Component field with options.
func withField(route fakegithub.Route, options ...string) fakegithub.Route {
	route.Body = strings.Replace(route.Body, `"title":"mono","url":"https://b/1","field":{"id":"F1","options":[{"id":"T","name":"Todo"},{"id":"G","name":"Doing"},{"id":"D","name":"Done"}]}`,
		`"title":"mono","url":"https://b/1","field":{"id":"F1","options":[{"id":"T","name":"Todo"},{"id":"G","name":"Doing"},{"id":"D","name":"Done"}]},"fields":{"nodes":[{},{"id":"F1","name":"Status","options":[]},{"id":"FC","name":"Component","options":[`+strings.Join(options, ",")+`]}]}`, 1)
	return route
}

func TestAddingAComponentKeepsTheOtherOptions(t *testing.T) {
	registered := withField(componentAdded(), `{"id":"O-docs","name":"docs"}`)
	visibility := visibilityRoute(`{"data":{"r0":{"isPrivate":true},"r1":{"isPrivate":true}}}`)
	read := fakegithub.Route{Match: "node(id: $id)", Body: `{"data":{"node":{"options":[{"id":"O-docs","name":"docs","color":"BLUE","description":"Manual"}]}}}`}
	update := fakegithub.Route{Match: "updateProjectV2Field(input", Body: `{"data":{"updateProjectV2Field":{"projectV2Field":{"id":"FC","options":[{"id":"O-docs","name":"docs"},{"id":"O-api","name":"api"}]}}}}`}
	label := fakegithub.Route{Match: "label(name: $label)", Body: `{"data":{"repository":{"label":null}}}`}
	created := fakegithub.Route{Match: "createLabel(input", Body: `{"data":{"createLabel":{"label":{"id":"LN"}}}}`}
	gh := fakegithub.New(registered, visibility, read, update, label, created)
	project, err := clientWith(t, gh, nil).AddComponent(ctx, Repo{"o", "mono"}, "services/api", "")
	if err != nil || project.ComponentOptions["api"] != "O-api" || project.ComponentOptions["docs"] != "O-docs" {
		t.Fatalf("project = %+v, err = %v", project, err)
	}
	wantCount(t, gh, 6)
	options := gh.Requests()[4].Variables["options"].([]any)
	kept := options[0].(map[string]any)
	if len(options) != 2 || kept["id"] != "O-docs" || kept["color"] != "BLUE" || kept["description"] != "Manual" || options[1].(map[string]any)["name"] != "api" {
		t.Fatalf("existing options must be sent back whole: %v", options)
	}
}

func TestAddComponentFailures(t *testing.T) {
	registered := componentAdded()
	visibility := visibilityRoute(`{"data":{"r0":{"isPrivate":true},"r1":{"isPrivate":true}}}`)
	noBoard := registered
	noBoard.Body = strings.Replace(registered.Body, `"title":"mono"`, `"title":"elsewhere"`, 1)
	long := strings.Repeat("x", 41)
	noLabel := fakegithub.Route{Match: "label(name: $label)", Body: `{"data":{"repository":{"label":null}}}`}
	tests := []struct {
		name           string
		routes         []fakegithub.Route
		repo           Repo
		dir, component string
		want           string
	}{
		{"empty path", nil, Repo{"o", "mono"}, "", "", "invalid path"},
		{"parent path", nil, Repo{"o", "mono"}, "../x", "", "invalid path"},
		{"unclean path", nil, Repo{"o", "mono"}, "a/../b", "", "invalid path"},
		{"name with slash", nil, Repo{"o", "mono"}, "a", "b/c", "invalid component name"},
		{"name with space", nil, Repo{"o", "mono"}, "a", "b c", "invalid component name"},
		{"long label", []fakegithub.Route{registered, visibility}, Repo{"o", "mono"}, "a", long, "over GitHub's 50"},
		{"long description", []fakegithub.Route{registered, visibility}, Repo{"o", "mono"}, strings.Repeat("d/", 50) + "x", "", "over GitHub's 100"},
		{"no room to remove", []fakegithub.Route{registered, visibility}, Repo{"o", "mono"}, strings.Repeat("d/", 46) + "x", "", `characters with "removed: ", over GitHub's 100`},
		{"unregistered", []fakegithub.Route{registered, visibility}, Repo{"o", "other"}, "a", "", "repository o/other is not registered: run project add o/other first"},
		{"other owner", []fakegithub.Route{registered, visibility}, Repo{"p", "mono"}, "a", "", "repository p/mono is not registered"},
		{"no board", []fakegithub.Route{noBoard, visibility}, Repo{"o", "mono"}, "a", "", "has no board"},
		{"name taken", []fakegithub.Route{registered, visibility}, Repo{"o", "mono"}, "site/docs", "docs", `project "mono/docs" is already registered for o/mono:docs`},
		{"path taken", []fakegithub.Route{registered, visibility}, Repo{"o", "mono"}, "docs", "manual", `docs is already registered as project "mono/docs"`},
		{"refresh fails", []fakegithub.Route{{Match: "labels(first: 100, query", Status: 500, Body: "{}"}}, Repo{"o", "mono"}, "a", "", "reading tracker metadata"},
		{"label fails", []fakegithub.Route{registered, visibility, createdField("a"), {Match: "label(name: $label)", Status: 500, Body: "{}"}}, Repo{"o", "mono"}, "a", "", "reading label"},
		{"field fails", []fakegithub.Route{registered, visibility, noLabel, {Match: "createProjectV2Field", Status: 500, Body: "{}"}}, Repo{"o", "mono"}, "a", "", "creating Component field"},
		{"backfill fails", []fakegithub.Route{registered, visibility, {Match: "createProjectV2Field", Status: 500, Body: "{}"}}, Repo{"o", "mono"}, "docs", "", "creating Component field"},
		{"options unreadable", []fakegithub.Route{withField(registered), visibility, noLabel, {Match: "node(id: $id)", Status: 500, Body: "{}"}}, Repo{"o", "mono"}, "a", "", "reading Component options"},
		{"options rejected", []fakegithub.Route{withField(registered), visibility, noLabel, {Match: "node(id: $id)", Body: `{"data":{"node":{"options":[]}}}`}, {Match: "updateProjectV2Field", Status: 500, Body: "{}"}}, Repo{"o", "mono"}, "a", "", "updating Component options"},
	}
	for _, tt := range tests {
		gh := fakegithub.New(tt.routes...)
		_, err := clientWith(t, gh, nil).AddComponent(ctx, tt.repo, tt.dir, tt.component)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error = %v, want %q", tt.name, err, tt.want)
		}
	}
}

func TestLabelScope(t *testing.T) {
	if got := labelScope(nil); got != "" {
		t.Fatalf("labelScope(nil) = %q", got)
	}
	if got := labelScope([]string{"repo:a", "repo:a/b"}); got != `label:"repo:a","repo:a/b"` {
		t.Fatalf("labelScope = %q", got)
	}
	if names := monoMetadata().scopeLabels(monoMetadata().Projects[1]); !slices.Equal(names, []string{"repo:mono/web"}) {
		t.Fatalf("a component's scope is its own label: %v", names)
	}
}
