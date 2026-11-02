package tracker

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	return append(routes, recheckRoute(sourceSpec{author: "o"}), fileRoute(), okRoute("updateProjectV2ItemFieldValue"))
}

func recheckRoute(spec sourceSpec) fakegithub.Route {
	return fakegithub.Route{Match: "node(id: $id)", Body: `{"data":{"node":` + strings.Replace(spec.json(), `{`, `{"state":"OPEN",`, 1) + `}}`}
}

func fileRoute() fakegithub.Route {
	return fakegithub.Route{Match: "item: addProjectV2ItemById", Body: `{"data":{"label":{"clientMutationId":null},"item":{"item":{"id":"ITEM"}}}}`}
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
	if _, _, err := c.readSource(ctx, Repo{"o", "code"}, []string{"o"}, nil); err != nil {
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
			_, issues, err := c.readSource(ctx, Repo{"o", "code"}, tt.authors, nil)
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
	_, issues, err := c.readSource(ctx, Repo{"o", "code"}, []string{"o"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 || gh.Requests()[1].Variables["cursor"] != "next" {
		t.Fatalf("issues = %+v, variables = %v", issues, gh.Requests()[1].Variables)
	}
}

func batchRoute(pages ...string) fakegithub.Route {
	fields := make([]string, 0, len(pages))
	for i, page := range pages {
		fields = append(fields, fmt.Sprintf(`"r%d":%s`, i, page))
	}
	return fakegithub.Route{Match: "r0: repository(owner:", Body: `{"data":{` + strings.Join(fields, ",") + `}}`}
}

func sourcePage(private bool, next string, specs ...sourceSpec) string {
	nodes := make([]string, 0, len(specs))
	for _, spec := range specs {
		nodes = append(nodes, spec.json())
	}
	return fmt.Sprintf(`{"isPrivate":%t,"issues":{"nodes":[%s],"pageInfo":{"hasNextPage":%t,"endCursor":%q}}}`,
		private, strings.Join(nodes, ","), next != "", next)
}

func repositoriesRoute(next string, names ...string) fakegithub.Route {
	nodes := make([]string, 0, len(names))
	for _, name := range names {
		count := 1
		if name == "o/quiet" {
			count = 0
		}
		nodes = append(nodes, fmt.Sprintf(`{"nameWithOwner":%q,"issues":{"totalCount":%d}}`, name, count))
	}
	return fakegithub.Route{Match: "repositoryOwner(login", Times: 1, Body: fmt.Sprintf(
		`{"data":{"repositoryOwner":{"repositories":{"nodes":[%s],"pageInfo":{"hasNextPage":%t,"endCursor":%q}}}}}`,
		strings.Join(nodes, ","), next != "", next)}
}

func TestScanRepos(t *testing.T) {
	first := repositoriesRoute("next", "o/quiet", "o/tracker", "o/code")
	second := repositoriesRoute("", "o/private")
	batch := batchRoute(
		sourcePage(false, "", sourceSpec{number: 1, author: "o"}, sourceSpec{number: 2, author: "bot"}, sourceSpec{number: 3, author: "stranger"}),
		sourcePage(true, "", sourceSpec{number: 1, author: "o"}, sourceSpec{number: 2, author: "stranger"}),
	)
	c, gh := newClient(t, first, second, batch)
	scans, err := c.ScanRepos(ctx, []string{"bot"})
	if err != nil {
		t.Fatal(err)
	}
	want := []RepoScan{
		{Repo: "o/code", Open: 3, Movable: 2, Others: 1},
		{Repo: "o/private", Private: true, Open: 2, Movable: 2, Others: 1},
	}
	if !slices.Equal(scans, want) {
		t.Fatalf("scans = %+v", scans)
	}
	wantCount(t, gh, 3)
	if variables := gh.Requests()[1].Variables; variables["cursor"] != "next" {
		t.Fatalf("repository page variables = %v", variables)
	}
}

func TestScanReposBatchesRepositories(t *testing.T) {
	names := make([]string, 0, scanBatchSize+1)
	pages := make([]string, 0, scanBatchSize+1)
	for i := range scanBatchSize + 1 {
		names = append(names, fmt.Sprintf("o/code%d", i))
		pages = append(pages, sourcePage(false, "", sourceSpec{number: 1, author: "o"}))
	}
	c, gh := newClient(t, repositoriesRoute("", names...), batchRoute(pages...))
	scans, err := c.ScanRepos(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(scans) != scanBatchSize+1 {
		t.Fatalf("scans = %+v", scans)
	}
	wantCount(t, gh, 3)
}

func TestScanReposFollowsExtraIssuePages(t *testing.T) {
	batch := batchRoute(sourcePage(false, "more", sourceSpec{number: 1, author: "o"}))
	c, gh := newClient(t, repositoriesRoute("", "o/code"), batch, sourceRoute(false, sourceSpec{number: 2, author: "o"}))
	scans, err := c.ScanRepos(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(scans) != 1 || scans[0].Open != 2 || gh.Requests()[2].Variables["cursor"] != "more" {
		t.Fatalf("scans = %+v, variables = %v", scans, gh.Requests()[2].Variables)
	}
}

func TestScanReposFailures(t *testing.T) {
	c, _ := newClient(t, repositoriesRoute("", "bad"))
	_, err := c.ScanRepos(ctx, nil)
	wantError(t, err, "invalid repository")

	c, _ = newClient(t, repositoriesRoute("", "o/code"), fakegithub.Route{Match: "r0: repository(owner:", Status: 500, Body: "{}"})
	_, err = c.ScanRepos(ctx, nil)
	wantError(t, err, "reading issues")

	batch := batchRoute(sourcePage(false, "more"))
	c, _ = newClient(t, repositoriesRoute("", "o/code"), batch, fakegithub.Route{Match: "repository(owner: $owner", Status: 500, Body: "{}"})
	_, err = c.ScanRepos(ctx, nil)
	wantError(t, err, "reading issues of o/code")

	c, _ = newClient(t, fakegithub.Route{Match: "repositoryOwner(login", Status: 500, Body: "{}"})
	_, err = c.ScanRepos(ctx, nil)
	wantError(t, err, "listing repositories")
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

func TestImportRequestBudget(t *testing.T) {
	tests := []struct {
		name    string
		private bool
		want    int
	}{
		{"private: metadata, source, then three writes", true, 6},
		{"public: one more read to recheck authors", false, 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, gh, _ := importClient(t, importRoutes(sourceRoute(tt.private, sourceSpec{number: 1, author: "o"}), 71)...)
			if _, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "go-tui"}}); err != nil {
				t.Fatal(err)
			}
			wantCount(t, gh, tt.want)
			if countRequests(gh, "mutation") != importWrites {
				t.Fatalf("writes = %d, want %d", countRequests(gh, "mutation"), importWrites)
			}
		})
	}
}

func TestImportRecheckStopsATransfer(t *testing.T) {
	tests := []struct {
		name  string
		route fakegithub.Route
	}{
		{"someone else commented", recheckRoute(sourceSpec{author: "o", comments: []string{"stranger"}})},
		{"closed meanwhile", fakegithub.Route{Match: "node(id: $id)", Body: `{"data":{"node":{"state":"CLOSED","author":{"login":"o"},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}`}},
		{"deleted meanwhile", fakegithub.Route{Match: "node(id: $id)", Body: `{"data":{"node":null}}`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routes := append([]fakegithub.Route{tt.route}, importRoutes(sourceRoute(false, sourceSpec{number: 1, author: "o"}), 71)...)
			c, gh, _ := importClient(t, routes...)
			result, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "go-tui"}})
			if err != nil {
				t.Fatal(err)
			}
			if result != (ImportResult{Skipped: 1}) || countRequests(gh, "transferIssue(input") != 0 {
				t.Fatalf("result = %+v, transfers = %d", result, countRequests(gh, "transferIssue(input"))
			}
		})
	}
	t.Run("private repositories need no recheck", func(t *testing.T) {
		c, gh, _ := importClient(t, importRoutes(sourceRoute(true, sourceSpec{number: 1, author: "stranger"}), 71)...)
		if _, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "go-tui"}}); err != nil {
			t.Fatal(err)
		}
		if countRequests(gh, "node(id: $id)") != 0 {
			t.Fatal("a private issue needs no second read")
		}
	})
	t.Run("recheck failure stops the run", func(t *testing.T) {
		routes := append([]fakegithub.Route{{Match: "node(id: $id)", Status: 500, Body: "{}"}}, importRoutes(sourceRoute(false, sourceSpec{number: 1, author: "o"}), 71)...)
		c, gh, _ := importClient(t, routes...)
		_, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "go-tui"}})
		wantError(t, err, "rereading authors")
		if countRequests(gh, "transferIssue(input") != 0 {
			t.Fatal("an unverified issue must not move")
		}
	})
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

	_, err = c.ImportIssues(ctx, ImportOptions{Repo: Repo{"someone", "code"}})
	wantError(t, err, "different owner")

	_, err = c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "code"}, Limit: -1})
	wantError(t, err, "invalid limit")

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
	for _, match := range []string{"item: addProjectV2ItemById", "updateProjectV2ItemFieldValue"} {
		t.Run(match, func(t *testing.T) {
			routes := []fakegithub.Route{{Match: match, Status: 500, Body: "{}"}}
			c, gh, _ := importClient(t, append(routes, importRoutes(source, 71, 72)...)...)
			var moved []int
			result, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "go-tui"}, OnMove: func(_, to int) error {
				moved = append(moved, to)
				return nil
			}})
			wantError(t, err, "o/go-tui#1: moved to ticket #71, finish it with issue move")
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
		routes := []fakegithub.Route{emptyMetadata(), source, {Match: "repository(owner: $owner, name: $name) { id }", Status: 500, Body: "{}"}}
		c, gh, _ := importClient(t, routes...)
		if _, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "new"}}); err == nil {
			t.Fatal("want an error")
		}
		if countRequests(gh, "transferIssue(input") != 0 {
			t.Fatal("nothing may move without a project")
		}
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
	routes = append(routes, fileRoute(), okRoute("updateProjectV2ItemFieldValue"))
	c, gh, _ := importClient(t, routes...)
	result, err := c.ImportIssues(ctx, ImportOptions{Repo: Repo{"o", "new"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Moved != 1 || !strings.Contains(lastDescription(gh), `singleSelectOptionId: "t"`) {
		t.Fatalf("result = %+v, last write = %s", result, lastDescription(gh))
	}
}
