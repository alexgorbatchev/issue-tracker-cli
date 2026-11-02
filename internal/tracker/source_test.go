package tracker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alexgorbatchev/issue-tracker-cli/internal/fakegithub"
)

type sourceSpec struct {
	number   int
	author   string
	comments []string
	more     bool
}

func (s sourceSpec) json() string {
	author := "null"
	if s.author != "" {
		author = fmt.Sprintf(`{"login":%q}`, s.author)
	}
	comments := make([]string, 0, len(s.comments))
	for _, login := range s.comments {
		comments = append(comments, fmt.Sprintf(`{"author":{"login":%q}}`, login))
	}
	return fmt.Sprintf(`{"id":"S%d","number":%d,"author":%s,"comments":{"nodes":[%s],"pageInfo":{"hasNextPage":%t}}}`,
		s.number, s.number, author, strings.Join(comments, ","), s.more)
}

func sourceRoute(private bool, specs ...sourceSpec) fakegithub.Route {
	nodes := make([]string, 0, len(specs))
	for _, spec := range specs {
		nodes = append(nodes, spec.json())
	}
	return fakegithub.Route{
		Match: "issues(states: OPEN, first: 100",
		Body: fmt.Sprintf(`{"data":{"repository":{"isPrivate":%t,"issues":{"nodes":[%s],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
			private, strings.Join(nodes, ",")),
	}
}

func transferRoute(numbers ...int) []fakegithub.Route {
	routes := make([]fakegithub.Route, 0, len(numbers))
	for _, number := range numbers {
		routes = append(routes, fakegithub.Route{
			Match: "transferIssue(input", Times: 1,
			Body: fmt.Sprintf(`{"data":{"transferIssue":{"issue":{"id":"T%d","number":%d}}}}`, number, number),
		})
	}
	return routes
}

func importRoutes(source fakegithub.Route, numbers ...int) []fakegithub.Route {
	routes := []fakegithub.Route{
		{Match: "labels(first: 100", Body: metadataJSON},
		{Match: "r0: repository", Body: `{"data":{"r0":{"isPrivate":false}}}`},
		source,
	}
	routes = append(routes, transferRoute(numbers...)...)
	return append(routes,
		okRoute("addLabelsToLabelable"),
		fakegithub.Route{Match: "addProjectV2ItemById", Body: `{"data":{"addProjectV2ItemById":{"item":{"id":"ITEM"}}}}`},
		okRoute("updateProjectV2ItemFieldValue"))
}

func importClient(t *testing.T, routes ...fakegithub.Route) (*Client, *fakegithub.GitHub, *[]time.Duration) {
	t.Helper()
	c, gh := newClient(t, routes...)
	pauses := &[]time.Duration{}
	c.pause = func(_ context.Context, wait time.Duration) error {
		*pauses = append(*pauses, wait)
		return nil
	}
	return c, gh, pauses
}

func countRequests(gh *fakegithub.GitHub, match string) int {
	count := 0
	for _, req := range gh.Requests() {
		if strings.Contains(req.Description, match) {
			count++
		}
	}
	return count
}

func TestReadSourceAsksForAuthorsOnly(t *testing.T) {
	c, gh := newClient(t, sourceRoute(false))
	if _, _, err := c.readSource(ctx, Repo{"o", "code"}, []string{"o"}); err != nil {
		t.Fatal(err)
	}
	query := gh.Requests()[0].Description
	for _, field := range []string{"title", "body"} {
		if strings.Contains(query, field) {
			t.Fatalf("public issue text must not be requested, found %s in:\n%s", field, query)
		}
	}
}

func TestReadSourceEligibility(t *testing.T) {
	tests := []struct {
		name    string
		private bool
		spec    sourceSpec
		authors []string
		want    bool
	}{
		{"public, only the owner", false, sourceSpec{number: 1, author: "o", comments: []string{"o"}}, []string{"o"}, true},
		{"logins ignore case", false, sourceSpec{number: 1, author: "O"}, []string{"o"}, true},
		{"public, trusted bot", false, sourceSpec{number: 1, author: "o", comments: []string{"bot"}}, []string{"o", "bot"}, true},
		{"public, stranger's issue", false, sourceSpec{number: 1, author: "stranger"}, []string{"o"}, false},
		{"public, stranger's comment", false, sourceSpec{number: 1, author: "o", comments: []string{"stranger"}}, []string{"o"}, false},
		{"public, deleted author", false, sourceSpec{number: 1}, []string{"o"}, false},
		{"public, unread comments", false, sourceSpec{number: 1, author: "o", more: true}, []string{"o"}, false},
		{"private, anyone", true, sourceSpec{number: 1, author: "stranger", comments: []string{"other"}}, []string{"o"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newClient(t, sourceRoute(tt.private, tt.spec))
			_, issues, err := c.readSource(ctx, Repo{"o", "code"}, tt.authors)
			if err != nil {
				t.Fatal(err)
			}
			if len(issues) != 1 || issues[0].movable != tt.want {
				t.Fatalf("issues = %+v, want movable %t", issues, tt.want)
			}
		})
	}
}

func TestReadSourceFollowsPages(t *testing.T) {
	first := fakegithub.Route{Match: "issues(states: OPEN", Times: 1, Body: `{"data":{"repository":{"isPrivate":true,"issues":{"nodes":[` +
		sourceSpec{number: 1, author: "o"}.json() + `],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}}`}
	c, gh := newClient(t, first, sourceRoute(true, sourceSpec{number: 2, author: "o"}))
	_, issues, err := c.readSource(ctx, Repo{"o", "code"}, []string{"o"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 || gh.Requests()[1].Variables["cursor"] != "next" {
		t.Fatalf("issues = %+v, variables = %v", issues, gh.Requests()[1].Variables)
	}
}

func TestScanRepos(t *testing.T) {
	first := fakegithub.Route{Match: "repositoryOwner(login", Times: 1, Body: `{"data":{"repositoryOwner":{"repositories":{"nodes":[
{"nameWithOwner":"o/quiet","issues":{"totalCount":0}},
{"nameWithOwner":"o/tracker","issues":{"totalCount":9}},
{"nameWithOwner":"o/code","issues":{"totalCount":2}}],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}}`}
	second := fakegithub.Route{Match: "repositoryOwner(login", Body: `{"data":{"repositoryOwner":{"repositories":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`}
	c, gh := newClient(t, first, second, sourceRoute(false, sourceSpec{number: 1, author: "o"}, sourceSpec{number: 2, author: "bot"}, sourceSpec{number: 3, author: "stranger"}))
	scans, err := c.ScanRepos(ctx, []string{"bot"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scans) != 1 || scans[0] != (RepoScan{Repo: "o/code", Open: 3, Movable: 2, Others: 1}) {
		t.Fatalf("scans = %+v", scans)
	}
	wantCount(t, gh, 3)
}

func TestScanReposFailures(t *testing.T) {
	repositories := fakegithub.Route{Match: "repositoryOwner(login", Body: `{"data":{"repositoryOwner":{"repositories":{"nodes":[{"nameWithOwner":"bad","issues":{"totalCount":1}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`}
	c, _ := newClient(t, repositories)
	_, err := c.ScanRepos(ctx, nil)
	wantError(t, err, "invalid repository")

	code := fakegithub.Route{Match: "repositoryOwner(login", Body: `{"data":{"repositoryOwner":{"repositories":{"nodes":[{"nameWithOwner":"o/code","issues":{"totalCount":1}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`}
	c, _ = newClient(t, code, fakegithub.Route{Match: "issues(states: OPEN", Status: 500, Body: "{}"})
	_, err = c.ScanRepos(ctx, nil)
	wantError(t, err, "reading issues of o/code")
}

func TestImportDryRunWritesNothing(t *testing.T) {
	c, gh, _ := importClient(t, importRoutes(sourceRoute(false, sourceSpec{number: 1, author: "o"}, sourceSpec{number: 2, author: "stranger"}))...)
	result, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "go-tui"}, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if result != (ImportResult{Remaining: 1, Skipped: 1}) {
		t.Fatalf("result = %+v", result)
	}
	if count := countRequests(gh, "mutation"); count != 0 {
		t.Fatalf("dry run made %d writes", count)
	}
}

func TestImportMovesTrustedIssuesAndPaces(t *testing.T) {
	source := sourceRoute(false, sourceSpec{number: 1, author: "o"}, sourceSpec{number: 2, author: "stranger"}, sourceSpec{number: 3, author: "o"})
	c, gh, pauses := importClient(t, importRoutes(source, 71, 72)...)
	var moves []string
	result, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "go-tui"}, OnMove: func(from, to int) error {
		moves = append(moves, fmt.Sprintf("%d>%d", from, to))
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result != (ImportResult{Moved: 2, Skipped: 1}) {
		t.Fatalf("result = %+v", result)
	}
	if strings.Join(moves, " ") != "1>71 3>72" {
		t.Fatalf("moves = %v", moves)
	}
	if len(*pauses) != 1 || (*pauses)[0] != importInterval {
		t.Fatalf("pauses = %v", *pauses)
	}
	if countRequests(gh, "transferIssue(input") != 2 || !strings.Contains(transferQuery(t, gh), "createLabelsIfMissing: true") {
		t.Fatalf("transfers = %d: %s", countRequests(gh, "transferIssue(input"), transferQuery(t, gh))
	}
	for _, req := range gh.Requests() {
		if strings.Contains(req.Description, "transferIssue(input") && req.Variables["issue"] == "S2" {
			t.Fatal("an issue with text from a stranger must stay put")
		}
	}
}

func transferQuery(t *testing.T, gh *fakegithub.GitHub) string {
	t.Helper()
	for _, req := range gh.Requests() {
		if strings.Contains(req.Description, "transferIssue(input") {
			return req.Description
		}
	}
	t.Fatal("no transfer")
	return ""
}

func TestImportLimitLeavesTheRest(t *testing.T) {
	source := sourceRoute(true, sourceSpec{number: 1, author: "o"}, sourceSpec{number: 2, author: "o"})
	c, gh, _ := importClient(t, importRoutes(source, 71, 72)...)
	result, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "go-tui"}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result != (ImportResult{Moved: 1, Remaining: 1}) || countRequests(gh, "transferIssue(input") != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestImportRegistersNothingWithoutMovableIssues(t *testing.T) {
	c, gh, _ := importClient(t, importRoutes(sourceRoute(false, sourceSpec{number: 1, author: "stranger"}))...)
	result, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "new"}})
	if err != nil {
		t.Fatal(err)
	}
	if result != (ImportResult{Skipped: 1}) || countRequests(gh, "mutation") != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestImportRefusals(t *testing.T) {
	c, _, _ := importClient(t)
	_, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"O", "Tracker"}})
	wantError(t, err, "cannot import from itself")

	public := strings.Replace(metadataJSON, `"isPrivate":true`, `"isPrivate":false`, 1)
	if public == metadataJSON {
		t.Fatal("metadata fixture is no longer private")
	}
	c, gh, _ := importClient(t, fakegithub.Route{Match: "labels(first: 100", Body: public}, fakegithub.Route{Match: "r0: repository", Body: `{"data":{"r0":{"isPrivate":false}}}`})
	_, err = c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "go-tui"}})
	wantError(t, err, "is not private")
	if countRequests(gh, "issues(states: OPEN") != 0 {
		t.Fatal("a public tracker must stop the run before any issue is read")
	}
}

func TestImportNamesTheTicketWhenSetupFails(t *testing.T) {
	source := sourceRoute(true, sourceSpec{number: 1, author: "o"}, sourceSpec{number: 2, author: "o"})
	tests := []struct {
		name  string
		match string
		want  string
	}{
		{"label", "addLabelsToLabelable", "moved to ticket #71, finish it with issue move"},
		{"status", "addProjectV2ItemById", "moved to ticket #71, set its status with issue status set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routes := []fakegithub.Route{{Match: tt.match, Status: 500, Body: "{}"}}
			c, gh, _ := importClient(t, append(routes, importRoutes(source, 71, 72)...)...)
			var moved []int
			result, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "go-tui"}, OnMove: func(_, to int) error {
				moved = append(moved, to)
				return nil
			}})
			wantError(t, err, "o/go-tui#1: "+tt.want)
			if result.Moved != 1 || result.Remaining != 1 || len(moved) != 1 || countRequests(gh, "transferIssue(input") != 1 {
				t.Fatalf("result = %+v, moved = %v", result, moved)
			}
		})
	}
}

func TestImportStops(t *testing.T) {
	source := sourceRoute(true, sourceSpec{number: 1, author: "o"}, sourceSpec{number: 2, author: "o"})
	boom := errors.New("boom")

	t.Run("failed transfer", func(t *testing.T) {
		routes := []fakegithub.Route{{Match: "transferIssue(input", Status: 500, Body: "{}"}}
		c, _, _ := importClient(t, append(routes, importRoutes(source)...)...)
		result, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "go-tui"}})
		wantError(t, err, "transferring")
		if result.Moved != 0 || result.Remaining != 2 {
			t.Fatalf("result = %+v", result)
		}
	})
	t.Run("cancelled between issues", func(t *testing.T) {
		c, gh, _ := importClient(t, importRoutes(source, 71, 72)...)
		c.pause = func(context.Context, time.Duration) error { return boom }
		if _, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "go-tui"}}); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if countRequests(gh, "transferIssue(input") != 1 {
			t.Fatal("the pause must stop the run")
		}
	})
	t.Run("progress write fails", func(t *testing.T) {
		c, _, _ := importClient(t, importRoutes(source, 71, 72)...)
		_, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "go-tui"}, OnMove: func(_, _ int) error { return boom }})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("registration fails", func(t *testing.T) {
		c, _, _ := importClient(t, importRoutes(source)...)
		_, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"other", "go-tui"}})
		wantError(t, err, "already registered")
	})
	t.Run("source read fails", func(t *testing.T) {
		routes := importRoutes(fakegithub.Route{Match: "issues(states: OPEN", Status: 500, Body: "{}"})
		c, _, _ := importClient(t, routes...)
		_, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "go-tui"}})
		wantError(t, err, "reading issues of o/go-tui")
	})
	t.Run("metadata read fails", func(t *testing.T) {
		c, _, _ := importClient(t, fakegithub.Route{Match: "labels(first: 100", Status: 500, Body: "{}"})
		if _, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "go-tui"}}); err == nil {
			t.Fatal("want an error")
		}
	})
}

func TestImportWritesStatusWhenMetadataLagsBehindANewBoard(t *testing.T) {
	routes := append([]fakegithub.Route{emptyMetadata()}, boardRoutes()...)
	routes = append(routes, sourceRoute(true, sourceSpec{number: 1, author: "o"}))
	routes = append(routes, transferRoute(71)...)
	routes = append(routes,
		okRoute("addLabelsToLabelable"),
		fakegithub.Route{Match: "addProjectV2ItemById", Body: `{"data":{"addProjectV2ItemById":{"item":{"id":"ITEM"}}}}`},
		okRoute("updateProjectV2ItemFieldValue"))
	c, gh, _ := importClient(t, routes...)
	result, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "new"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Moved != 1 || !strings.Contains(lastDescription(gh), `singleSelectOptionId: "t"`) {
		t.Fatalf("result = %+v, last write = %s", result, lastDescription(gh))
	}
}
