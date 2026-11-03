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

func emptyMetadata() fakegithub.Route {
	return fakegithub.Route{Match: "labels(first: 100, query", Body: strings.ReplaceAll(strings.ReplaceAll(metadataJSON, `{"id":"L1","name":"repo:go-tui","description":"o/go-tui"},`, ""), `"title":"go-tui"`, `"title":"unrelated"`)}
}

func boardRoutes() []fakegithub.Route {
	return []fakegithub.Route{
		{Match: "$owner: String!, $name: String!) { repository(owner: $owner, name: $name) { id } }", Body: `{"data":{"repository":{"id":"CODE"}}}`},
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

func TestAddProjectRefusesNameCollision(t *testing.T) {
	gh := fakegithub.New(metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	_, err := clientWith(t, gh, nil).AddProject(ctx, Repo{"someorg", "go-tui"})
	wantError(t, err, "already registered for o/go-tui")
}

func TestAddProjectRestoresRemovedProject(t *testing.T) {
	routes := append([]fakegithub.Route{
		emptyMetadata(),
		{Match: "label(name: $label)", Body: `{"data":{"repository":{"label":{"id":"OLD"}}}}`},
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

func TestAddProjectReusesBoardFromFailedRun(t *testing.T) {
	metadata := strings.ReplaceAll(emptyMetadata().Body, `"title":"unrelated"`, `"title":"new"`)
	routes := []fakegithub.Route{
		{Match: "labels(first: 100, query", Body: metadata},
		boardRoutes()[0], boardRoutes()[3], boardRoutes()[4],
	}
	gh := fakegithub.New(routes...)
	project, err := clientWith(t, gh, nil).AddProject(ctx, Repo{"o", "new"})
	if err != nil {
		t.Fatal(err)
	}
	if project.BoardID != "B1" {
		t.Fatalf("project = %+v", project)
	}
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
		{"missing repo", []fakegithub.Route{emptyMetadata(), {Match: "{ repository(owner: $owner, name: $name) { id } }", Body: `{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","message":"x"}]}`}}, "not found"},
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
	c, gh := newClient(t, summaryRoute(0), okRoute("deleteProjectV2"), okRoute("updateLabel"), metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	if err := c.RemoveProject(ctx, "go-tui"); err != nil {
		t.Fatal(err)
	}
	requests := gh.Requests()
	if requests[2].Variables["description"] != "" {
		t.Fatalf("label = %v", requests[2].Variables)
	}
	query := requests[0].Variables["open"].(string)
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
	c, gh := newClient(t, summaryRoute(2))
	wantError(t, c.RemoveProject(ctx, "go-tui"), "2 open tickets")
	wantCount(t, gh, 1)

	c, _ = newClient(t, metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	wantError(t, c.RemoveProject(ctx, "unknown"), "unknown project")

	c, _ = newClient(t, fakegithub.Route{Match: "open: search", Status: 500, Body: "{}"})
	wantError(t, c.RemoveProject(ctx, "go-tui"), "counting tickets")

	c, _ = newClient(t, summaryRoute(0), fakegithub.Route{Match: "deleteProjectV2", Status: 500, Body: "{}"})
	wantError(t, c.RemoveProject(ctx, "go-tui"), "deleting board")

	c, _ = newClient(t, summaryRoute(0), okRoute("deleteProjectV2"), fakegithub.Route{Match: "updateLabel", Status: 500, Body: "{}"})
	wantError(t, c.RemoveProject(ctx, "go-tui"), "updating label")
}

func TestCurrentProject(t *testing.T) {
	t.Setenv("GH_TOKEN", "tok")
	t.Setenv("GH_REPO", "")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	repo := gitRepo(t)
	t.Chdir(repo)

	c, gh := newClient(t)
	project, ok, err := c.CurrentProject(ctx, false)
	if err != nil || !ok || project.Name != "go-tui" {
		t.Fatalf("project = %+v, ok = %v, err = %v", project, ok, err)
	}
	wantCount(t, gh, 0)
	if detected, ok := CurrentRepo(); !ok || detected.String() != "o/go-tui" {
		t.Fatalf("CurrentRepo() = %v, %v", detected, ok)
	}

	if out, err := exec.Command("git", "-C", repo, "remote", "set-url", "origin", "git@github.com:o/unregistered.git").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	_, ok, err = c.CurrentProject(ctx, false)
	if err != nil || ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	wantCount(t, gh, 0)

	gh.Add(metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	if _, ok, err = c.CurrentProject(ctx, true); err != nil || ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	wantCount(t, gh, 2)

	t.Chdir(t.TempDir())
	if _, ok, err := c.CurrentProject(ctx, true); ok || err != nil {
		t.Fatalf("outside a repository: ok = %v, err = %v", ok, err)
	}
	if _, ok := CurrentRepo(); ok {
		t.Fatal("no repository here")
	}
}

func TestCurrentProjectFailures(t *testing.T) {
	t.Setenv("GH_TOKEN", "tok")
	t.Setenv("GH_REPO", "o/unregistered")
	c, _ := newClient(t, fakegithub.Route{Match: "labels(first: 100, query", Status: 500, Body: "{}"})
	_, _, err := c.CurrentProject(ctx, true)
	wantError(t, err, "reading tracker metadata")

	public := clientWith(t, fakegithub.New(), &metadata{})
	public.requirePrivate = true
	public.meta = nil
	_, _, err = public.CurrentProject(ctx, false)
	wantError(t, err, "not private")
}
