package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	cobrahelptree "github.com/alexgorbatchev/cobra-help-tree/v4"
	"github.com/alexgorbatchev/issue-tracker-cli/internal/config"
	"github.com/alexgorbatchev/issue-tracker-cli/internal/fakegithub"
	"github.com/alexgorbatchev/issue-tracker-cli/internal/tracker"
	"github.com/spf13/cobra"
)

const skillAlert = "ALERT: Agents must read `AGENT=1 issue-tracker skill` before using this tool.\n"

func noSession() (*session, error) {
	return nil, errors.New("this command must not need a session")
}

func execute(t *testing.T, open opener, args ...string) (string, string, error) {
	t.Helper()
	cmd := newRootCommand(open)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

// fakeOpener returns an opener backed by gh with two cached projects.
func fakeOpener(t *testing.T, gh *fakegithub.GitHub) opener {
	t.Helper()
	cache := t.TempDir()
	options := map[string]string{"Todo": "t", "Doing": "g", "Done": "d"}
	meta := map[string]any{
		"repository_id": "REPO", "owner_id": "OWNER", "head_oid": "sha", "is_private": true,
		"projects": []map[string]any{
			{"name": "go-tui", "repo": "o/go-tui", "label_id": "L1", "board_id": "B1", "board_url": "https://b/1", "status_field_id": "F", "status_options": options},
			{"name": "lib", "repo": "o/lib", "public": true, "label_id": "L2", "board_id": "B2", "board_url": "https://b/2", "status_field_id": "F", "status_options": options},
		},
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "o-tracker.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return func() (*session, error) {
		client, err := tracker.New(tracker.Options{Repo: tracker.Repo{Owner: "o", Name: "tracker"}, CacheDir: cache, Transport: gh, Token: "tok"})
		if err != nil {
			return nil, err
		}
		return &session{client: client, cfg: config.Config{Repo: "o/tracker", StaleDays: 3}, cacheRoot: cache}, nil
	}
}

func outsideRepository(t *testing.T) {
	t.Helper()
	t.Setenv("GH_TOKEN", "tok")
	t.Setenv("GH_REPO", "")
	t.Chdir(t.TempDir())
}

// boards maps each cached project to its board, which holds its tickets' items.
var boards = map[string]string{"go-tui": "B1", "lib": "B2"}

func ticketJSON(number int, project, status, updated string) string {
	blob := `{"id":"I1","number":` + strconv.Itoa(number) + `,"title":"Crash","state":"OPEN","url":"https://t/1","updatedAt":"UPDATED",` +
		`"labels":{"nodes":[{"name":"repo:PROJECT"}]},"blockedBy":{"nodes":[]},"parent":null,` +
		`"projectItems":{"nodes":[{"id":"ITEM","updatedAt":"UPDATED","project":{"id":"BOARD"},"fieldValueByName":{"name":"STATUS"}}]}`
	return strings.NewReplacer("UPDATED", updated, "PROJECT", project, "STATUS", status, "BOARD", boards[project]).Replace(blob)
}

func ticketRoute(project, status string) fakegithub.Route {
	return fakegithub.Route{Match: "isPrivate issue(number: $number)", Body: `{"data":{"repository":{"isPrivate":true,"issue":` + ticketJSON(1, project, status, "2026-10-01T00:00:00Z") + `}}}}`}
}

func listRoute(nodes ...string) fakegithub.Route {
	for i := range nodes {
		nodes[i] += "}"
	}
	return fakegithub.Route{Match: "issues(first: 100", Body: `{"data":{"repository":{"isPrivate":true,"issues":{"nodes":[` + strings.Join(nodes, ",") + `],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`}
}

func ok(match string) fakegithub.Route {
	return fakegithub.Route{Match: match, Body: `{"data":{}}`}
}

func idsRoute() fakegithub.Route {
	return fakegithub.Route{Match: "n1: issue(number: 1)", Body: `{"data":{"repository":{"isPrivate":true,"n1":{"id":"I1"},"n2":{"id":"I2"}}}}`}
}

func listPage(next bool, total int, nodes ...string) fakegithub.Route {
	page := listRoute(nodes...)
	page.Body = strings.Replace(page.Body, `"nodes":[`, fmt.Sprintf(`"totalCount":%d,"nodes":[`, total), 1)
	page.Body = strings.Replace(page.Body, `"hasNextPage":false,"endCursor":""`, fmt.Sprintf(`"hasNextPage":%v,"endCursor":"c"`, next), 1)
	page.Times = 1
	return page
}

func TestListReportsTruncation(t *testing.T) {
	ticket := func(number int, status string) string {
		return ticketJSON(number, "go-tui", status, "2026-10-01T00:00:00Z")
	}
	fullPage := []string{}
	for number := 1; number <= 100; number++ {
		fullPage = append(fullPage, ticket(number, "Todo"))
	}
	tests := []struct {
		name     string
		args     []string
		pages    []fakegithub.Route
		rows     int
		lastRow  string
		requests int
		warning  string
	}{
		{"GitHub reports the total", []string{"--limit", "2"}, []fakegithub.Route{listPage(true, 500, ticket(1, "Todo"), ticket(2, "Todo"), ticket(3, "Todo"))}, 2, "2\t", 1, "WARN: showing the first 2 of 500 tickets: pass --limit 0 to list them all\n"},
		{"default limit stays on one page", nil, []fakegithub.Route{listPage(true, 533, fullPage...)}, 100, "100\t", 1, "WARN: showing the first 100 of 533 tickets: pass --limit 0 to list them all\n"},
		{"exactly the limit", []string{"--limit", "3"}, []fakegithub.Route{listPage(false, 3, ticket(1, "Todo"), ticket(2, "Todo"), ticket(3, "Todo"))}, 3, "3\t", 1, ""},
		{"no limit reads every page", []string{"--limit", "0"}, []fakegithub.Route{listPage(true, 3, ticket(1, "Todo"), ticket(2, "Todo")), listPage(false, 3, ticket(3, "Todo"))}, 3, "3\t", 2, ""},
		{"client filter counts the rest", []string{"--status", "Todo", "--limit", "1"}, []fakegithub.Route{listPage(true, 4, ticket(1, "Todo"), ticket(2, "Todo")), listPage(false, 4, ticket(3, "Todo"), ticket(4, "Doing"))}, 1, "1\t", 2, "WARN: showing the first 1 of 3 tickets: pass --limit 0 to list them all\n"},
		{"client filter that fits reads on", []string{"--status", "Doing", "--limit", "1"}, []fakegithub.Route{listPage(true, 2, ticket(1, "Doing")), listPage(false, 2, ticket(2, "Todo"))}, 1, "1\t", 2, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AGENT", "1")
			outsideRepository(t)
			gh := fakegithub.New(tt.pages...)
			stdout, stderr, err := execute(t, fakeOpener(t, gh), append([]string{"issue", "list", "--all-projects"}, tt.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			rows := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
			if len(rows) != tt.rows || !strings.HasPrefix(rows[len(rows)-1], tt.lastRow) {
				t.Fatalf("stdout = %q", stdout)
			}
			if stderr != tt.warning {
				t.Fatalf("stderr = %q", stderr)
			}
			if got := len(gh.Requests()); got != tt.requests {
				t.Fatalf("requests = %d, want %d", got, tt.requests)
			}
		})
	}
}

func TestWriteCommandsReportResults(t *testing.T) {
	merged := fakegithub.Route{Match: "pullRequest(number: 88)", Body: `{"data":{"r0":{"pullRequest":{"merged":true,"url":"https://p/88"}}}}`}
	claimRef := fakegithub.Route{Match: "POST api.github.com/repos/o/tracker/git/refs", Status: 201, Body: "{}"}
	drop := fakegithub.Route{Match: "DELETE", Status: 204}
	status := ok("updateProjectV2ItemFieldValue")
	created := fakegithub.Route{Match: "createIssue(input", Body: `{"data":{"createIssue":{"issue":{"id":"N","number":50,"url":"https://t/50"}}}}`}
	tests := []struct {
		name   string
		routes []fakegithub.Route
		args   []string
		want   string
	}{
		{"claim", []fakegithub.Route{ticketRoute("go-tui", "Todo"), claimRef, status}, []string{"issue", "claim", "1"}, "OK: claimed #1 in go-tui: Crash"},
		{"claim public warns", []fakegithub.Route{ticketRoute("lib", "Todo"), claimRef, status}, []string{"issue", "claim", "1"}, "WARN: lib is public"},
		{"claim next", []fakegithub.Route{boardOnly(), listRoute(ticketJSON(1, "go-tui", "Todo", "2026-10-01T00:00:00Z")), claimRef, status}, []string{"issue", "claim", "--next", "--project", "go-tui"}, "OK: claimed #1"},
		{"claim next empty", []fakegithub.Route{listRoute()}, []string{"issue", "claim", "--next"}, "OK: no ready tickets"},
		{"release", []fakegithub.Route{ticketRoute("go-tui", "Doing"), drop, status}, []string{"issue", "release", "1"}, "OK: released #1"},
		{"close", []fakegithub.Route{ticketRoute("go-tui", "Todo"), merged, ok("closeIssue")}, []string{"issue", "close", "1", "--pr", "88"}, "OK: closed #1"},
		{"close reason", []fakegithub.Route{ticketRoute("go-tui", "Todo"), ok("closeIssue")}, []string{"issue", "close", "1", "--reason", "not-planned"}, "OK: closed #1"},
		{"reopen", []fakegithub.Route{ticketRoute("go-tui", "Done"), ok("reopenIssue"), status}, []string{"issue", "reopen", "1"}, "OK: reopened #1"},
		{"create", []fakegithub.Route{created, {Match: "addProjectV2ItemById", Body: `{"data":{"addProjectV2ItemById":{"item":{"id":"IT"}}}}`}, status}, []string{"issue", "create", "--project", "go-tui", "--title", "T", "--body", "B"}, "OK: created #50 in go-tui"},
		{"edit title", []fakegithub.Route{idsRoute(), ok("updateIssue")}, []string{"issue", "edit", "1", "--title", "New"}, "OK: edited #1"},
		{"edit body", []fakegithub.Route{idsRoute(), ok("updateIssue")}, []string{"issue", "edit", "1", "--body", "New"}, "OK: edited #1"},
		{"comment", []fakegithub.Route{idsRoute(), ok("addComment")}, []string{"issue", "comment", "add", "1", "--body", "Hi"}, "OK: commented on #1"},
		{"status", []fakegithub.Route{ticketRoute("go-tui", "Todo"), status}, []string{"issue", "status", "set", "1", "Done"}, "OK: set #1 to Done"},
		{"label add", []fakegithub.Route{{Match: "POST api.github.com/repos/o/tracker/issues/1/labels", Body: "[]"}}, []string{"issue", "label", "add", "1", "bug"}, "OK: updated labels on #1"},
		{"label rm", []fakegithub.Route{{Match: "DELETE api.github.com/repos/o/tracker/issues/1/labels/bug", Body: "[]"}}, []string{"issue", "label", "rm", "1", "bug"}, "OK: updated labels on #1"},
		{"blocker add", []fakegithub.Route{idsRoute(), ok("addBlockedBy")}, []string{"issue", "blocker", "add", "1", "2"}, "OK: updated #1"},
		{"blocker rm", []fakegithub.Route{idsRoute(), ok("removeBlockedBy")}, []string{"issue", "blocker", "rm", "1", "2"}, "OK: updated #1"},
		{"sub add", []fakegithub.Route{idsRoute(), ok("addSubIssue")}, []string{"issue", "sub", "add", "1", "2"}, "OK: updated #1"},
		{"sub rm", []fakegithub.Route{idsRoute(), ok("removeSubIssue")}, []string{"issue", "sub", "rm", "1", "2"}, "OK: updated #1"},
		{
			"move",
			[]fakegithub.Route{ticketRoute("go-tui", "Todo"), ok("addLabelsToLabelable"), {Match: "addProjectV2ItemById", Body: `{"data":{"addProjectV2ItemById":{"item":{"id":"X"}}}}`}, status},
			[]string{"issue", "move", "1", "lib"}, "OK: moved #1 to lib",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AGENT", "1")
			outsideRepository(t)
			stdout, _, err := execute(t, fakeOpener(t, fakegithub.New(tt.routes...)), tt.args...)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(stdout, tt.want) {
				t.Fatalf("stdout = %q, want %q", stdout, tt.want)
			}
		})
	}
}

func TestSubListOutput(t *testing.T) {
	route := fakegithub.Route{Match: "subIssues(first: 100) { nodes", Body: `{"data":{"repository":{"isPrivate":true,"issue":{"subIssues":{"nodes":[{"number":3,"title":"Done one","state":"CLOSED"},{"number":4,"title":"Open one","state":"OPEN"}]}}}}}`}
	empty := fakegithub.Route{Match: "subIssues(first: 100) { nodes", Body: `{"data":{"repository":{"isPrivate":true,"issue":{"subIssues":{"nodes":[]}}}}}`}
	tests := []struct {
		name  string
		agent string
		route fakegithub.Route
		want  []string
	}{
		{"agent", "1", route, []string{"3\tCLOSED\tDone one\n4\tOPEN\tOpen one\n", "OK: #1: 1 of 2 done, 1 open\n"}},
		{"human", "0", route, []string{"Done one", "[OK] #1: 1 of 2 done, 1 open\n"}},
		{"human empty", "0", empty, []string{"No sub-issues.\n", "[OK] #1: 0 of 0 done, 0 open\n"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AGENT", tt.agent)
			stdout, _, err := execute(t, fakeOpener(t, fakegithub.New(tt.route)), "issue", "sub", "list", "1")
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.want {
				if !strings.Contains(stdout, want) {
					t.Fatalf("stdout = %q, want %q", stdout, want)
				}
			}
		})
	}
}

func parentJSON(number int, subs string) string {
	return strings.Replace(ticketJSON(number, "go-tui", "Todo", "2026-10-01T00:00:00Z"), `"parent":null,`, `"parent":null,"subIssueStates":{"nodes":[`+subs+`]},`, 1)
}

func parentRoute(json string) fakegithub.Route {
	return fakegithub.Route{Match: "isPrivate issue(number: $number)", Body: `{"data":{"repository":{"isPrivate":true,"issue":` + json + `}}}}`}
}

func firstRead(json string) fakegithub.Route {
	route := parentRoute(json)
	route.Times = 1
	return route
}

func TestSubCloseEligible(t *testing.T) {
	closed, open := `{"state":"CLOSED"}`, `{"state":"OPEN"}`
	list := func() fakegithub.Route { return listRoute(parentJSON(1, closed), parentJSON(2, closed+","+open)) }
	claimed := strings.Replace(parentJSON(1, closed), `"name":"Todo"`, `"name":"Doing"`, 1)
	tests := []struct {
		name     string
		routes   []fakegithub.Route
		args     []string
		want     string
		writes   int
		requests int
	}{
		{"dry run", []fakegithub.Route{boardOnly(), list()}, []string{"--dry-run", "--project", "go-tui"}, "OK: would close #1 in go-tui: Crash\n", 0, 2},
		{"closes", []fakegithub.Route{boardOnly(), list(), parentRoute(parentJSON(1, closed)), ok("closeIssue")}, []string{"--project", "go-tui"}, "OK: closed #1 in go-tui: Crash\n", 1, 4},
		{"claimed after the list", []fakegithub.Route{boardOnly(), list(), parentRoute(claimed)}, []string{"--project", "go-tui"}, "OK: skipped #1: no longer eligible\n", 0, 3},
		{
			"continues past a skipped parent",
			[]fakegithub.Route{boardOnly(), listRoute(parentJSON(1, closed), parentJSON(3, closed)), firstRead(claimed), parentRoute(parentJSON(3, closed)), ok("closeIssue")},
			[]string{"--project", "go-tui"}, "OK: skipped #1: no longer eligible\nOK: closed #3 in go-tui: Crash\n", 1, 5,
		},
		{"nothing eligible", []fakegithub.Route{listRoute(parentJSON(2, open))}, []string{"--all-projects"}, "OK: no eligible parents\n", 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AGENT", "1")
			outsideRepository(t)
			gh := fakegithub.New(tt.routes...)
			args := append([]string{"issue", "sub", "close-eligible"}, tt.args...)
			stdout, _, err := execute(t, fakeOpener(t, gh), args...)
			if err != nil {
				t.Fatal(err)
			}
			if stdout != tt.want {
				t.Fatalf("stdout = %q, want %q", stdout, tt.want)
			}
			writes := 0
			for _, req := range gh.Requests() {
				if strings.Contains(req.Description, "closeIssue") {
					writes++
				}
			}
			if writes != tt.writes || gh.Count() != tt.requests {
				t.Fatalf("close writes = %d, want %d; requests = %d, want %d", writes, tt.writes, gh.Count(), tt.requests)
			}
		})
	}
}

func TestSubCloseEligibleFailures(t *testing.T) {
	t.Setenv("AGENT", "1")
	outsideRepository(t)
	if _, _, err := execute(t, fakeOpener(t, fakegithub.New()), "issue", "sub", "close-eligible", "--project", "x", "--all-projects"); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("err = %v", err)
	}
	list := listRoute(parentJSON(1, `{"state":"CLOSED"}`))
	gh := fakegithub.New(list, fakegithub.Route{Match: "isPrivate issue(number: $number)", Status: 500, Body: "{}"})
	if _, _, err := execute(t, fakeOpener(t, gh), "issue", "sub", "close-eligible"); err == nil {
		t.Fatal("a failed close must fail the command")
	}
}

func TestIssueListOutput(t *testing.T) {
	old := ticketJSON(1, "go-tui", "Doing", "2026-01-01T00:00:00Z")
	t.Run("agent uses tabs", func(t *testing.T) {
		t.Setenv("AGENT", "1")
		outsideRepository(t)
		stdout, _, err := execute(t, fakeOpener(t, fakegithub.New(listRoute(old))), "issue", "list", "--all-projects")
		if err != nil {
			t.Fatal(err)
		}
		if want := "1\tDoing (stale)\t\tgo-tui\t\tCrash\n"; stdout != want {
			t.Fatalf("stdout = %q, want %q", stdout, want)
		}
	})
	t.Run("human uses a table", func(t *testing.T) {
		t.Setenv("AGENT", "0")
		outsideRepository(t)
		stdout, _, err := execute(t, fakeOpener(t, fakegithub.New(listRoute(old))), "issue", "list", "--all-projects", "--stale", "--stale-days", "2")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"│ # ", "Project", "DOING (stale)", "Crash", "│"} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("stdout = %q, missing %q", stdout, want)
			}
		}
	})
	t.Run("human tables leave out the project the list is scoped to", func(t *testing.T) {
		t.Setenv("AGENT", "0")
		t.Setenv("GH_TOKEN", "tok")
		t.Setenv("GH_REPO", "o/go-tui")
		gh := fakegithub.New(boardOnly(), listRoute(ticketJSON(1, "go-tui", "Todo", "2026-10-01T00:00:00Z")))
		stdout, _, err := execute(t, fakeOpener(t, gh), "issue", "list")
		if err != nil || strings.Contains(stdout, "Project") || strings.Contains(stdout, "go-tui") || !strings.Contains(stdout, "Crash") {
			t.Fatalf("stdout = %q, err = %v", stdout, err)
		}
		gh = fakegithub.New(boardOnly(), listRoute(ticketJSON(1, "go-tui", "Todo", "2026-10-01T00:00:00Z"), ticketJSON(2, "lib", "Todo", "2026-10-01T00:00:00Z")))
		stdout, _, err = execute(t, fakeOpener(t, gh), "issue", "list")
		if err != nil || !strings.Contains(stdout, "Project") || !strings.Contains(stdout, "lib") {
			t.Fatalf("a ticket from another project brings the column back: stdout = %q, err = %v", stdout, err)
		}
		t.Setenv("AGENT", "1")
		gh = fakegithub.New(boardOnly(), listRoute(ticketJSON(1, "go-tui", "Todo", "2026-10-01T00:00:00Z")))
		if stdout, _, err = execute(t, fakeOpener(t, gh), "issue", "list"); err != nil || stdout != "1\tTodo\t\tgo-tui\t\tCrash\n" {
			t.Fatalf("agents keep every column: stdout = %q, err = %v", stdout, err)
		}
	})
	t.Run("priority has its own column and conflicts warn", func(t *testing.T) {
		t.Setenv("AGENT", "1")
		outsideRepository(t)
		urgent := strings.Replace(ticketJSON(1, "go-tui", "Todo", "2026-10-01T00:00:00Z"), `{"name":"repo:go-tui"}`, `{"name":"repo:go-tui"},{"name":"priority:P2"},{"name":"bug"},{"name":"Priority:p1"}`, 1)
		stdout, stderr, err := execute(t, fakeOpener(t, fakegithub.New(listRoute(urgent))), "issue", "list", "--all-projects", "--priority", "p1")
		if err != nil {
			t.Fatal(err)
		}
		if want := "1\tTodo\tP1\tgo-tui\tbug\tCrash\n"; stdout != want {
			t.Fatalf("stdout = %q, want %q", stdout, want)
		}
		if want := "WARN: #1 has priorities P1, P2: ranked as P1\n"; stderr != want {
			t.Fatalf("stderr = %q, want %q", stderr, want)
		}
		_, _, err = execute(t, fakeOpener(t, fakegithub.New()), "issue", "list", "--all-projects", "--priority", "high")
		if err == nil || !strings.Contains(err.Error(), `invalid priority "high"`) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("human tables fit the terminal", func(t *testing.T) {
		t.Setenv("AGENT", "0")
		outsideRepository(t)
		long := strings.Replace(ticketJSON(2, "go-tui", "Todo", "2026-01-01T00:00:00Z"), `"title":"Crash"`, `"title":"Select the matching project item when a ticket belongs to multiple boards"`, 1)
		long = strings.Replace(long, `{"name":"repo:go-tui"}`, `{"name":"repo:go-tui"},{"name":"priority:P2"},{"name":"dependencies"},{"name":"stdout-review"}`, 1)
		widest := func(stdout string) int {
			widest := 0
			for _, line := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
				widest = max(widest, utf8.RuneCountInString(line))
			}
			return widest
		}

		t.Setenv("COLUMNS", "200")
		stdout, _, err := execute(t, fakeOpener(t, fakegithub.New(listRoute(long))), "issue", "list", "--all-projects")
		if err != nil {
			t.Fatal(err)
		}
		natural := widest(stdout)
		if natural >= 200 || !strings.Contains(stdout, " P2 ") || !strings.Contains(stdout, "dependencies, stdout-review") || strings.Contains(stdout, "priority:") {
			t.Fatalf("a table narrower than the terminal keeps its width and one line per row:\n%s", stdout)
		}

		t.Setenv("COLUMNS", "60")
		stdout, _, err = execute(t, fakeOpener(t, fakegithub.New(listRoute(long))), "issue", "list", "--all-projects")
		if err != nil {
			t.Fatal(err)
		}
		if got := widest(stdout); got != 60 {
			t.Fatalf("widest line = %d, want 60:\n%s", got, stdout)
		}
		for _, word := range []string{"Select", "boards", "dependencies"} {
			if !strings.Contains(stdout, word) {
				t.Fatalf("wrapping must keep %q whole:\n%s", word, stdout)
			}
		}
	})
	t.Run("human reports an empty list", func(t *testing.T) {
		t.Setenv("AGENT", "0")
		outsideRepository(t)
		stdout, _, err := execute(t, fakeOpener(t, fakegithub.New(listRoute())), "issue", "list", "--all-projects")
		if err != nil || !strings.Contains(stdout, "No tickets found.") {
			t.Fatalf("stdout = %q, err = %v", stdout, err)
		}
	})
}

// boardOnly answers the search for board tickets with no routing label.
func boardOnly() fakegithub.Route {
	return fakegithub.Route{
		Match: "search(query: $search",
		Body:  `{"data":{"repository":{"isPrivate":true},"search":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}`,
		Times: 1,
	}
}

func TestListOutsideARegisteredRepositoryNeedsAScope(t *testing.T) {
	t.Setenv("AGENT", "1")
	t.Setenv("GH_TOKEN", "tok")
	refresh := func(label string) fakegithub.Route {
		return fakegithub.Route{Match: "labels(first: 100, query", Body: `{"data":{"repository":{"id":"R","isPrivate":true,"owner":{"id":"O"},"defaultBranchRef":{"target":{"oid":"s"}},
"labels":{"nodes":[` + label + `],"pageInfo":{"hasNextPage":false,"endCursor":""}},
"projectsV2":{"nodes":[{"id":"BN","title":"new","url":"https://b/9","field":{"id":"F","options":[{"id":"t","name":"Todo"}]}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`}
	}
	visibility := fakegithub.Route{Match: "r0: repository", Body: `{"data":{"r0":{"isPrivate":true}}}`}

	other := `{"id":"L1","name":"repo:go-tui","description":"o/go-tui"}`
	t.Setenv("GH_REPO", "o/unknown")
	gh := fakegithub.New(refresh(other), visibility)
	_, _, err := execute(t, fakeOpener(t, gh), "issue", "list")
	if err == nil || !strings.Contains(err.Error(), "o/unknown is not a registered project: register it with issue-tracker project add o/unknown, or pass --all-projects") {
		t.Fatalf("err = %v", err)
	}
	if gh.Count() != 2 {
		t.Fatalf("made %d requests, want only the refresh and its visibility read", gh.Count())
	}

	t.Setenv("GH_REPO", "o/new")
	gh = fakegithub.New(refresh(other+`,{"id":"LN","name":"repo:new","description":"o/new"}`), visibility, boardOnly(), listRoute(ticketJSON(1, "new", "Todo", "2026-10-01T00:00:00Z")))
	stdout, _, err := execute(t, fakeOpener(t, gh), "issue", "list")
	if err != nil || !strings.HasPrefix(stdout, "1\t") {
		t.Fatalf("a repository registered on another machine lists after a refresh: stdout = %q, err = %v", stdout, err)
	}
	if gh.Count() != 4 {
		t.Fatalf("made %d requests, want the refresh, its visibility read, the board search, and the list", gh.Count())
	}

	outsideRepository(t)
	_, _, err = execute(t, fakeOpener(t, fakegithub.New()), "issue", "list")
	if err == nil || !strings.Contains(err.Error(), "not inside a registered repository: pass --project <name>, or --all-projects") {
		t.Fatalf("err = %v", err)
	}
}

func TestCurrentRepositorySelectsProject(t *testing.T) {
	t.Setenv("AGENT", "1")
	t.Setenv("GH_TOKEN", "tok")
	t.Setenv("GH_REPO", "o/go-tui")
	gh := fakegithub.New(boardOnly(), boardOnly(), listRoute())
	open := fakeOpener(t, gh)
	if _, _, err := execute(t, open, "issue", "list"); err != nil {
		t.Fatal(err)
	}
	if labels := gh.Requests()[1].Variables["labels"]; labels == nil {
		t.Fatal("a registered repository must scope the list")
	}
	if _, _, err := execute(t, open, "issue", "list", "--all-projects"); err != nil {
		t.Fatal(err)
	}
	if labels := gh.Requests()[2].Variables["labels"]; labels != nil {
		t.Fatalf("--all-projects must not scope: %v", labels)
	}
	if _, _, err := execute(t, open, "issue", "list", "--project", "lib"); err != nil {
		t.Fatal(err)
	}
	if labels := gh.Requests()[4].Variables["labels"].([]any); labels[0] != "repo:lib" {
		t.Fatalf("an explicit project must win: %v", labels)
	}
	gh.Add(boardOnly())
	if _, _, err := execute(t, open, "issue", "list", "--unrouted"); err != nil {
		t.Fatal(err)
	}
	if search := gh.Requests()[5].Variables["search"].(string); !strings.Contains(search, `-label:"repo:go-tui"`) || strings.Contains(search, ` label:`) {
		t.Fatalf("--unrouted must ignore the current repository: %q", search)
	}
}

func TestLabelAndSearchFlags(t *testing.T) {
	t.Setenv("AGENT", "1")
	outsideRepository(t)
	search := fakegithub.Route{Match: "search(query: $search", Body: `{"data":{"repository":{"isPrivate":true},"search":{"nodes":[` + ticketJSON(1, "go-tui", "Todo", "2026-10-01T00:00:00Z") + `}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}`}
	gh := fakegithub.New(search)
	if _, _, err := execute(t, fakeOpener(t, gh), "issue", "list", "--search", "crash resize", "--project", "go-tui"); err != nil {
		t.Fatal(err)
	}
	if got := gh.Requests()[1].Variables["search"]; got != `repo:o/tracker is:issue sort:created-asc is:open label:"repo:go-tui" crash resize` {
		t.Fatalf("search = %v", got)
	}
	stdout, _, err := execute(t, fakeOpener(t, fakegithub.New(listRoute(ticketJSON(1, "go-tui", "Todo", "2026-10-01T00:00:00Z")))), "issue", "list", "--all-projects", "--label", "bug")
	if err != nil || stdout != "" {
		t.Fatalf("stdout = %q, err = %v", stdout, err)
	}
	_, _, err = execute(t, fakeOpener(t, fakegithub.New()), "issue", "label", "add", "1", "repo:lib")
	if err == nil || !strings.Contains(err.Error(), "use issue move") {
		t.Fatalf("err = %v", err)
	}
}

func TestProjectCommands(t *testing.T) {
	t.Setenv("AGENT", "1")
	metadata := fakegithub.Route{Match: "labels(first: 100, query", Body: `{"data":{"repository":{"id":"R","isPrivate":true,"owner":{"id":"O"},"defaultBranchRef":{"target":{"oid":"s"}},
"labels":{"nodes":[{"id":"L1","name":"repo:go-tui","description":"o/go-tui"},{"id":"LD","name":"repo:go-tui/docs","description":"o/go-tui:site/docs"}],"pageInfo":{"hasNextPage":false,"endCursor":""}},
"projectsV2":{"nodes":[{"id":"B1","title":"go-tui","url":"https://b/1","field":{"id":"F","options":[{"id":"t","name":"Todo"}]}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`}
	visibility := fakegithub.Route{Match: "r0: repository", Body: `{"data":{"r0":{"isPrivate":false},"r1":{"isPrivate":false}}}`}
	summary := fakegithub.Route{Match: "open: search", Body: `{"data":{"open":{"issueCount":2},"closed":{"issueCount":5}}}`}
	tests := []struct {
		name   string
		routes []fakegithub.Route
		args   []string
		want   string
	}{
		{"list", []fakegithub.Route{metadata, visibility}, []string{"project", "list"}, "go-tui\to/go-tui\tpublic\thttps://b/1\t.\n"},
		{"inspect", []fakegithub.Route{boardOnly(), summary}, []string{"project", "inspect", "go-tui"}, "path: .\nvisibility: private\nboard: https://b/1\nopen: 2\nclosed: 5\n"},
		{"list components", []fakegithub.Route{metadata, visibility}, []string{"project", "list"}, "go-tui/docs\to/go-tui\tpublic\thttps://b/1\tsite/docs\n"},
		{"add existing", []fakegithub.Route{metadata, visibility}, []string{"project", "add", "o/go-tui"}, "OK: registered project go-tui"},
		{"add existing component", []fakegithub.Route{metadata, visibility, {Match: "createProjectV2Field(input", Body: `{"data":{"createProjectV2Field":{"projectV2Field":{"id":"FC","options":[{"id":"O1","name":"docs"},{"id":"O2","name":"example"}]}}}}`}}, []string{"project", "add", "o/go-tui", "--path", "site/docs"}, "OK: registered project go-tui/docs for site/docs in o/go-tui"},
		{"add component", []fakegithub.Route{metadata, visibility, {Match: "createProjectV2Field(input", Body: `{"data":{"createProjectV2Field":{"projectV2Field":{"id":"FC","options":[{"id":"O1","name":"docs"},{"id":"O2","name":"example"}]}}}}`}, {Match: "label(name: $label)", Body: `{"data":{"repository":{"label":null}}}`}, {Match: "createLabel(input", Body: `{"data":{"createLabel":{"label":{"id":"LN"}}}}`}},
			[]string{"project", "add", "o/go-tui", "--path", "cmd/demo", "--name", "example"}, "OK: registered project go-tui/example for cmd/demo in o/go-tui"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outsideRepository(t)
			stdout, _, err := execute(t, fakeOpener(t, fakegithub.New(tt.routes...)), tt.args...)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(stdout, tt.want) {
				t.Fatalf("stdout = %q, want %q", stdout, tt.want)
			}
		})
	}
}

func TestCloseNeedsExactlyOneKindOfEvidence(t *testing.T) {
	t.Setenv("AGENT", "1")
	outsideRepository(t)
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"issue", "close", "1"}, "at least one of the flags in the group [pr landed-pr reason] is required"},
		{[]string{"issue", "close", "1", "--pr", "2", "--landed-pr", "2"}, "none of the others can be"},
		{[]string{"issue", "close", "1", "--landed-pr", "2", "--reason", "completed"}, "none of the others can be"},
	}
	for _, tt := range tests {
		_, _, err := execute(t, noSession, tt.args...)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("%v: err = %v, want %q", tt.args, err, tt.want)
		}
	}
}

func TestProjectRm(t *testing.T) {
	t.Setenv("AGENT", "1")
	outsideRepository(t)
	_, _, err := execute(t, noSession, "project", "rm", "go-tui")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v", err)
	}
	gh := fakegithub.New(
		boardOnly(),
		fakegithub.Route{Match: "open: search", Body: `{"data":{"open":{"issueCount":0},"closed":{"issueCount":0}}}`},
		ok("deleteProjectV2"), ok("updateLabel"),
		fakegithub.Route{Match: "labels(first: 100, query", Body: `{"data":{"repository":{"id":"R","isPrivate":true,"owner":{"id":"O"},"defaultBranchRef":{"target":{"oid":"s"}},"labels":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},"projectsV2":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`},
	)
	stdout, _, err := execute(t, fakeOpener(t, gh), "project", "rm", "go-tui", "--yes")
	if err != nil || !strings.Contains(stdout, "OK: removed project go-tui") {
		t.Fatalf("stdout = %q, err = %v", stdout, err)
	}
}

func TestIssueInspectPrintsDetail(t *testing.T) {
	t.Setenv("AGENT", "1")
	outsideRepository(t)
	body := ticketJSON(1, "lib", "Todo", "2026-10-01T00:00:00Z")
	body = strings.Replace(body, `"parent":null`, `"parent":{"number":5,"state":"OPEN","blockedBy":{"nodes":[]},"parent":null}`, 1)
	body = strings.Replace(body, `"blockedBy":{"nodes":[]},"parent":{`, `"blockedBy":{"nodes":[{"number":3,"state":"OPEN"}]},"parent":{`, 1)
	body = strings.Replace(body, `{"name":"repo:lib"}`, `{"name":"repo:lib"},{"name":"bug"},{"name":"priority:P3"},{"name":"priority:P0"}`, 1)
	route := fakegithub.Route{Match: "subIssues(first", Body: `{"data":{"repository":{"isPrivate":true,"n1":` + body + `,"body":"Text https://github.com/user-attachments/assets/abc","blocking":{"nodes":[{"number":4,"state":"OPEN"}]},"subIssues":{"nodes":[{"number":6,"title":"Child","state":"OPEN"}]},"comments":{"nodes":[{"author":{"login":"a"},"createdAt":"2026-01-01T00:00:00Z","body":"Note"}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}}`}
	stdout, _, err := execute(t, fakeOpener(t, fakegithub.New(route)), "issue", "inspect", "1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\npriority: P0\nlabels: bug\n", "WARN: lib is public", "WARN: #1 has priorities P0, P3: ranked as P0", "status: Todo", "parent: 5", "blocked_by: 3", "blocking: 4", "sub_issue: 6 OPEN Child", "attachment: https://github.com/user-attachments/assets/abc", "body:\nText", "comment: a 2026-01-01T00:00:00Z\nNote"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, missing %q", stdout, want)
		}
	}

	t.Setenv("AGENT", "0")
	stdout, _, err = execute(t, fakeOpener(t, fakegithub.New(route)), "issue", "inspect", "1")
	if err != nil || !strings.Contains(stdout, "status: TODO\n") {
		t.Fatalf("people see the status in upper case: stdout = %q, err = %v", stdout, err)
	}
}

func TestIssueInspectWarnsOncePerPublicProject(t *testing.T) {
	t.Setenv("AGENT", "1")
	outsideRepository(t)
	detail := func(number int) string {
		return fmt.Sprintf(`"n%d":`, number) + ticketJSON(number, "lib", "Todo", "2026-10-01T00:00:00Z") + `,"body":"Body","blocking":{"nodes":[]},"subIssues":{"nodes":[]},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}`
	}
	gh := fakegithub.New(fakegithub.Route{Match: "subIssues(first", Body: `{"data":{"repository":{"isPrivate":true,` + detail(2) + `,` + detail(1) + `}}}`})
	stdout, _, err := execute(t, fakeOpener(t, gh), "issue", "inspect", "2", "1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(stdout, "WARN: lib is public") != 1 || !strings.HasPrefix(stdout, "WARN: ") {
		t.Fatalf("stdout = %q, want one leading WARN for the shared public project", stdout)
	}
	if !strings.Contains(stdout, "body:\nBody\n\nnumber: 1\n") || strings.Index(stdout, "number: 2") > strings.Index(stdout, "number: 1") {
		t.Fatalf("stdout = %q, want #2 then #1 separated by a blank line", stdout)
	}
	if gh.Count() != 1 {
		t.Fatalf("made %d requests, want 1", gh.Count())
	}
}

func TestAttachmentCommands(t *testing.T) {
	t.Setenv("AGENT", "1")
	outsideRepository(t)
	bin := t.TempDir()
	script := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(filepath.Join(bin, "gh"), script, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	shots := t.TempDir()
	for _, name := range []string{"a.png", "b.png"} {
		if err := os.WriteFile(filepath.Join(shots, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stdout, _, err := execute(t, fakeOpener(t, fakegithub.New()), "issue", "attachment", "add", "1", filepath.Join(shots, "a.png"), filepath.Join(shots, "b.png"))
	if err != nil || !strings.Contains(stdout, "OK: attached 2 file(s) to #1") {
		t.Fatalf("stdout = %q, err = %v", stdout, err)
	}

	stdout, _, err = execute(t, fakeOpener(t, fakegithub.New()), "issue", "comment", "add", "1", "--attach", filepath.Join(shots, "a.png"))
	if err != nil || !strings.Contains(stdout, "OK: commented on #1") {
		t.Fatalf("a comment can be only an attachment: stdout = %q, err = %v", stdout, err)
	}
	stdout, _, err = execute(t, fakeOpener(t, fakegithub.New()), "issue", "edit", "1", "--attach", filepath.Join(shots, "b.png"))
	if err != nil || !strings.Contains(stdout, "OK: edited #1") {
		t.Fatalf("stdout = %q, err = %v", stdout, err)
	}

	body := ticketJSON(1, "go-tui", "Todo", "2026-10-01T00:00:00Z")
	gh := fakegithub.New(
		fakegithub.Route{Match: "subIssues(first", Body: `{"data":{"repository":{"isPrivate":true,"n1":` + body + `,"body":"https://github.com/user-attachments/assets/abc","blocking":{"nodes":[]},"subIssues":{"nodes":[]},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}}`},
		fakegithub.Route{Match: "GET github.com/user-attachments/assets/abc", Body: "png", Headers: map[string]string{"Content-Type": "image/png"}},
	)
	home := t.TempDir()
	t.Setenv("HOME", home)
	open := fakeOpener(t, gh)
	dir := filepath.Join(home, "pulled")
	stdout, _, err = execute(t, open, "issue", "attachment", "pull", "1", "--dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("~", "pulled", "abc.png") + "\n"; stdout != want {
		t.Fatalf("stdout = %q, want the home directory folded: %q", stdout, want)
	}
	stdout, _, err = execute(t, open, "issue", "attachment", "pull", "1")
	if err != nil || !strings.Contains(stdout, filepath.Join("attachments", "1", "abc.png")) {
		t.Fatalf("stdout = %q, err = %v", stdout, err)
	}
}

func TestBodyInput(t *testing.T) {
	t.Setenv("AGENT", "1")
	outsideRepository(t)
	file := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(file, []byte("from file"), 0o600); err != nil {
		t.Fatal(err)
	}
	gh := fakegithub.New(idsRoute(), ok("addComment"), ok("updateIssue"))
	open := fakeOpener(t, gh)
	if _, _, err := execute(t, open, "issue", "comment", "add", "1", "--body-file", file); err != nil {
		t.Fatal(err)
	}
	if got := gh.Requests()[1].Variables["body"]; got != "from file" {
		t.Fatalf("body = %v", got)
	}

	cmd := newRootCommand(open)
	cmd.SetIn(strings.NewReader("from stdin"))
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetArgs([]string{"issue", "edit", "1", "--body-file", "-"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	input := gh.Requests()[3].Variables["input"].(map[string]any)
	if input["body"] != "from stdin" {
		t.Fatalf("input = %v", input)
	}
}

func TestArgumentErrors(t *testing.T) {
	t.Setenv("AGENT", "1")
	outsideRepository(t)
	open := fakeOpener(t, fakegithub.New())
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"claim needs a target", []string{"issue", "claim"}, "pass a ticket number or --next"},
		{"claim rejects both", []string{"issue", "claim", "1", "--next"}, "pass a ticket number or --next"},
		{"claim number", []string{"issue", "claim", "x"}, "invalid ticket number"},
		{"inspect number", []string{"issue", "inspect", "0"}, "invalid ticket number"},
		{"inspect at most 50", append([]string{"issue", "inspect"}, strings.Fields(strings.Repeat("1 ", 51))...), "accepts between 1 and 50 arg(s), received 51"},
		{"attachment number", []string{"issue", "attachment", "pull", "x"}, "invalid ticket number"},
		{"action number", []string{"issue", "release", "x"}, "invalid ticket number"},
		{"relation number", []string{"issue", "blocker", "add", "1", "x"}, "invalid ticket number"},
		{"close needs evidence", []string{"issue", "close", "1"}, "at least one of the flags"},
		{"close rejects both", []string{"issue", "close", "1", "--pr", "1", "--reason", "duplicate"}, "none of the others"},
		{"create needs a title", []string{"issue", "create"}, "title"},
		{"create needs a project", []string{"issue", "create", "--title", "T"}, "not in a registered repository"},
		{"label number", []string{"issue", "label", "add", "x", "bug"}, "invalid ticket number"},
		{"body conflict", []string{"issue", "comment", "add", "1", "--body", "a", "--body-file", "b"}, "none of the others"},
		{"empty comment", []string{"issue", "comment", "add", "1"}, "comment is empty"},
		{"missing body file", []string{"issue", "comment", "add", "1", "--body-file", "/missing"}, "reading body file"},
		{"list scope conflict", []string{"issue", "list", "--project", "x", "--all-projects"}, "not both"},
		{"unrouted with a project", []string{"issue", "list", "--unrouted", "--project", "x"}, "none of the others can be"},
		{"unrouted with all projects", []string{"issue", "list", "--unrouted", "--all-projects"}, "none of the others can be"},
		{"unrouted is never ready", []string{"issue", "list", "--unrouted", "--ready"}, "none of the others can be"},
		{"project add outside a repository", []string{"project", "add"}, "pass owner/repo"},
		{"project add bad repo", []string{"project", "add", "bad"}, "invalid repository"},
		{"project add name without path", []string{"project", "add", "o/go-tui", "--name", "docs"}, "pass --path too"},
		{"project inspect outside a repository", []string{"project", "inspect"}, "pass a project"},
		{"unknown command", []string{"nope"}, "unknown command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := execute(t, open, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSessionFailureStopsCommands(t *testing.T) {
	failed := func() (*session, error) { return nil, errors.New("no session") }
	for _, args := range [][]string{
		{"project", "add", "o/r"}, {"project", "inspect", "x"}, {"project", "list"}, {"project", "rm", "x", "--yes"},
		{"project", "scan"}, {"project", "import", "o/r"},
		{"issue", "claim", "1"}, {"issue", "create", "--title", "T"}, {"issue", "inspect", "1"}, {"issue", "list"},
		{"issue", "attachment", "pull", "1"}, {"issue", "release", "1"},
	} {
		if _, _, err := execute(t, failed, args...); err == nil || err.Error() != "no session" {
			t.Fatalf("%v: err = %v", args, err)
		}
	}
}

func TestGitHubFailuresPropagate(t *testing.T) {
	t.Setenv("AGENT", "1")
	outsideRepository(t)
	fail := fakegithub.Route{Match: "", Status: 500, Body: "{}"}
	for _, args := range [][]string{
		{"issue", "list"}, {"issue", "inspect", "1"}, {"issue", "claim", "1"}, {"issue", "claim", "--next"},
		{"issue", "attachment", "pull", "1", "--dir", t.TempDir()},
		{"issue", "create", "--project", "go-tui", "--title", "T"},
		{"project", "inspect", "go-tui"}, {"project", "list"}, {"project", "add", "o/r"},
		{"project", "scan"}, {"project", "import", "o/r"},
	} {
		if _, _, err := execute(t, fakeOpener(t, fakegithub.New(fail)), args...); err == nil {
			t.Fatalf("%v: want an error", args)
		}
	}
}

func TestProjectAddUsesCurrentRepository(t *testing.T) {
	t.Setenv("GH_TOKEN", "tok")
	t.Setenv("GH_REPO", "o/here")
	repo, err := addTarget(nil)
	if err != nil || repo.String() != "o/here" {
		t.Fatalf("repo = %v, err = %v", repo, err)
	}
}

func TestRunFormatsErrors(t *testing.T) {
	tests := []struct {
		agent string
		want  string
	}{
		{"1", "ERR: invalid ticket number \"x\"\n"},
		{"0", "[ERROR] invalid ticket number \"x\"\n"},
	}
	for _, tt := range tests {
		t.Setenv("AGENT", tt.agent)
		cmd := newRootCommand(noSession)
		var stderr bytes.Buffer
		cmd.SetErr(&stderr)
		cmd.SetArgs([]string{"issue", "inspect", "x"})
		if code := run(context.Background(), cmd); code != 1 {
			t.Fatalf("code = %d", code)
		}
		if stderr.String() != tt.want {
			t.Fatalf("stderr = %q, want %q", stderr.String(), tt.want)
		}
	}
	cmd := newRootCommand(noSession)
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetArgs([]string{"--version"})
	if code := run(context.Background(), cmd); code != 0 {
		t.Fatalf("code = %d", code)
	}
}

func TestOpenTracker(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("GH_TOKEN", "tok")
	t.Setenv(config.RepoEnv, "")
	t.Setenv(config.StaleDaysEnv, "")
	t.Setenv(config.RequirePrivateEnv, "")
	t.Setenv(config.AccountEnv, "")
	t.Setenv(config.AgentAccountEnv, "")
	if _, err := openTracker(); err == nil || !strings.Contains(err.Error(), "no tracker repository") {
		t.Fatalf("err = %v", err)
	}
	t.Setenv(config.RepoEnv, "bad")
	if _, err := openTracker(); err == nil {
		t.Fatal("want an invalid repository error")
	}
	t.Setenv(config.StaleDaysEnv, "x")
	if _, err := openTracker(); err == nil {
		t.Fatal("want a configuration error")
	}
	t.Setenv(config.StaleDaysEnv, "4")
	t.Setenv(config.RepoEnv, "o/tracker")
	s, err := openTracker()
	if err != nil {
		t.Fatal(err)
	}
	if s.cfg.StaleDays != 4 || s.client.Repo().Name != "tracker" {
		t.Fatalf("session = %+v", s)
	}
}

func TestOpenTrackerActsAsTheAgentAccountForAgents(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("GH_PATH", "")
	t.Setenv(config.RepoEnv, "o/tracker")
	t.Setenv(config.AccountEnv, "me")
	t.Setenv(config.AgentAccountEnv, "bot")
	for agent, want := range map[string]string{"1": "--user bot", "0": "--user me"} {
		t.Setenv("AGENT", agent)
		log := stubGH(t, "echo tok")
		if _, err := openTracker(); err != nil {
			t.Fatal(err)
		}
		if data, err := os.ReadFile(log); err != nil || !strings.Contains(string(data), want) {
			t.Fatalf("AGENT=%s: gh calls = %q, %v, want %s", agent, data, err, want)
		}
	}
}

func TestVersionPrintsOnlyTheVersion(t *testing.T) {
	stdout, _, err := execute(t, noSession, "--version")
	if err != nil || stdout != currentVersion()+"\n" {
		t.Fatalf("stdout = %q, err = %v", stdout, err)
	}
}

func TestRootPrintsHelp(t *testing.T) {
	t.Setenv("AGENT", "0")
	stdout, _, err := execute(t, noSession)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"├─ issue", "╰─ project", "ISSUE_TRACKER_REPO"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("help is missing %q:\n%s", want, stdout)
		}
	}
}

func TestSkillCommand(t *testing.T) {
	want := "Install the issue-tracker skill using the Skills CLI:\n\n  npx skills add alexgorbatchev/issue-tracker-cli --skill issue-tracker\n\nSkill:\n\n  https://github.com/alexgorbatchev/issue-tracker-cli/tree/main/skills/issue-tracker\n"
	for _, mode := range []string{"0", "1"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AGENT", mode)
			stdout, _, err := execute(t, noSession, "skill")
			if err != nil {
				t.Fatal(err)
			}
			if stdout != want {
				t.Fatalf("stdout = %q", stdout)
			}
		})
	}
	if _, _, err := execute(t, noSession, "skill", "extra"); err == nil {
		t.Fatal("skill must reject arguments")
	}
}

func walk(cmd *cobra.Command, visit func(*cobra.Command)) {
	if cmd.Hidden {
		return
	}
	visit(cmd)
	for _, child := range cmd.Commands() {
		walk(child, visit)
	}
}

func TestAgentHelpStartsWithSkillAlert(t *testing.T) {
	t.Setenv("AGENT", "1")
	root := newRootCommand(noSession)
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	var paths [][]string
	walk(root, func(cmd *cobra.Command) {
		path := strings.Fields(cmd.CommandPath())[1:]
		paths = append(paths, append(path, "--help"))
	})
	if len(paths) < 25 {
		t.Fatalf("only %d help screens", len(paths))
	}
	for _, args := range paths {
		stdout, _, err := execute(t, noSession, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.HasPrefix(stdout, skillAlert) {
			t.Fatalf("%v help does not start with the alert:\n%s", args, stdout)
		}
	}
}

func TestSkillDocumentsCommandInterface(t *testing.T) {
	path := filepath.Join("..", "..", "skills", "issue-tracker", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `name: "issue-tracker"`) {
		t.Fatal("the skill's frontmatter name must equal the --skill value")
	}
	root := newRootCommand(noSession)
	if omissions := cobrahelptree.SkillOmissions(root, techCatalog, string(data)); len(omissions) > 0 {
		t.Fatalf("SKILL.md is missing:\n%s", strings.Join(omissions, "\n"))
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("disk full")
}

func TestRunFailsWhenHelpCannotBeWritten(t *testing.T) {
	for _, tt := range []struct {
		name string
		mode string
		args []string
		want string
	}{
		{"human flag", "0", []string{"--help"}, "[ERROR] "},
		{"agent flag", "1", []string{"--help"}, "ERR: "},
		{"bare invocation", "0", nil, "[ERROR] "},
		{"help command", "0", []string{"help", "issue"}, "[ERROR] "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AGENT", tt.mode)
			t.Cleanup(func() { helpWriteError = nil })
			stderr := new(bytes.Buffer)
			cmd := newRootCommand(noSession)
			cmd.SetOut(failingWriter{})
			cmd.SetErr(stderr)
			cmd.SetArgs(tt.args)
			if code := run(context.Background(), cmd); code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			if !strings.HasPrefix(stderr.String(), tt.want) || strings.Count(stderr.String(), "disk full") != 1 {
				t.Fatalf("stderr = %q", stderr)
			}
		})
	}
}

func TestProjectScanOutput(t *testing.T) {
	outsideRepository(t)
	repositories := fakegithub.Route{Match: "repositoryOwner(login", Body: `{"data":{"repositoryOwner":{"repositories":{"nodes":[{"nameWithOwner":"o/code","issues":{"totalCount":2}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`}
	issues := fakegithub.Route{Match: "r0: repository(owner:", Body: `{"data":{"r0":{"isPrivate":false,"issues":{"nodes":[
{"id":"A","number":1,"author":{"login":"o"},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false}}},
{"id":"B","number":2,"author":{"login":"stranger"},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false}}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`}
	tests := []struct {
		name  string
		agent string
		args  []string
		want  string
	}{
		{"agent", "1", []string{"project", "scan"}, "o/code\tpublic\t2\t1\t1\n"},
		{"trusted author", "1", []string{"project", "scan", "--author", "stranger"}, "o/code\tpublic\t2\t2\t0\n"},
		{"human", "0", []string{"project", "scan"}, "FROM OTHERS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AGENT", tt.agent)
			stdout, _, err := execute(t, fakeOpener(t, fakegithub.New(repositories, issues)), tt.args...)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.ToUpper(stdout), strings.ToUpper(tt.want)) {
				t.Fatalf("stdout = %q, want %q", stdout, tt.want)
			}
		})
	}
	t.Run("nothing open", func(t *testing.T) {
		t.Setenv("AGENT", "0")
		empty := fakegithub.Route{Match: "repositoryOwner(login", Body: `{"data":{"repositoryOwner":{"repositories":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`}
		stdout, _, err := execute(t, fakeOpener(t, fakegithub.New(empty)), "project", "scan")
		if err != nil || !strings.Contains(stdout, "No open issues found.") {
			t.Fatalf("stdout = %q, err = %v", stdout, err)
		}
	})
}

func TestProjectImportOutput(t *testing.T) {
	t.Setenv("AGENT", "1")
	outsideRepository(t)
	metadata := fakegithub.Route{Match: "labels(first: 100, query", Body: `{"data":{"repository":{"id":"R","isPrivate":true,"owner":{"id":"O"},"defaultBranchRef":{"target":{"oid":"s"}},
"labels":{"nodes":[{"id":"L1","name":"repo:go-tui","description":"o/go-tui"}],"pageInfo":{"hasNextPage":false,"endCursor":""}},
"projectsV2":{"nodes":[{"id":"B1","title":"go-tui","url":"https://b/1","field":{"id":"F","options":[{"id":"t","name":"Todo"}]}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`}
	visibility := fakegithub.Route{Match: "r0: repository", Body: `{"data":{"r0":{"isPrivate":false}}}`}
	source := fakegithub.Route{Match: "issues(states: OPEN, first: 100", Body: `{"data":{"repository":{"isPrivate":true,"issues":{"nodes":[
{"id":"A","number":1,"author":{"login":"o"},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false}}},
{"id":"B","number":2,"author":{"login":"o"},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false}}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`}
	transfer := fakegithub.Route{Match: "transferIssue(input", Body: `{"data":{"transferIssue":{"issue":{"id":"T","number":71}}}}`}
	file := fakegithub.Route{Match: "item: addProjectV2ItemById", Body: `{"data":{"label":{"clientMutationId":null},"item":{"item":{"id":"ITEM"}}}}`}
	status := fakegithub.Route{Match: "updateProjectV2ItemFieldValue", Body: `{"data":{}}`}
	tests := []struct {
		name      string
		args      []string
		want      string
		transfers int
	}{
		{"dry run", []string{"--dry-run"}, "OK: o/go-tui: moved 0, 2 left to move, skipped 0 with text from other people\n", 0},
		{"limit", []string{"--limit", "1"}, "OK: moved o/go-tui#1 to #71\nOK: o/go-tui: moved 1, 1 left to move, skipped 0 with text from other people\n", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh := fakegithub.New(metadata, visibility, source, transfer, file, status)
			stdout, _, err := execute(t, fakeOpener(t, gh), append([]string{"project", "import", "o/go-tui"}, tt.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			if stdout != tt.want {
				t.Fatalf("stdout = %q, want %q", stdout, tt.want)
			}
			transfers := 0
			for _, req := range gh.Requests() {
				if strings.Contains(req.Description, "transferIssue(input") {
					transfers++
				}
			}
			if transfers != tt.transfers {
				t.Fatalf("transfers = %d, want %d", transfers, tt.transfers)
			}
		})
	}
}

func TestProjectImportRejectsBadRepository(t *testing.T) {
	outsideRepository(t)
	if _, _, err := execute(t, fakeOpener(t, fakegithub.New()), "project", "import", "bad"); err == nil || !strings.Contains(err.Error(), "invalid repository") {
		t.Fatalf("err = %v", err)
	}
}

func TestAttachmentCleanupCommands(t *testing.T) {
	t.Setenv("AGENT", "1")
	outsideRepository(t)
	link := "https://github.com/o/tracker/releases/download/attachments/1-a1b2c3d4-run.log"
	ticket := fakegithub.Route{Match: "subIssues(first", Body: `{"data":{"repository":{"isPrivate":true,"n1":` + ticketJSON(1, "go-tui", "Todo", "2026-10-01T00:00:00Z") +
		`,"body":"[log](` + link + `)","blocking":{"nodes":[]},"subIssues":{"nodes":[]},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}}`}
	release := fakegithub.Route{Match: "GET api.github.com/repos/o/tracker/releases/tags/attachments", Body: `{"id":5}`}
	assets := fakegithub.Route{Match: "GET api.github.com/repos/o/tracker/releases/5/assets", Body: `[{"id":8,"name":"1-a1b2c3d4-run.log"},{"id":9,"name":"1-b2c3d4e5-old.log"}]`}
	deleted := fakegithub.Route{Match: "DELETE api.github.com/repos/o/tracker/releases/assets", Status: 204}

	gh := fakegithub.New(ticket, release, assets, deleted, ok("updateIssue"))
	stdout, _, err := execute(t, fakeOpener(t, gh), "issue", "attachment", "rm", "1", link)
	if err != nil || stdout != "OK: removed 1 file(s) from #1\n" {
		t.Fatalf("stdout = %q, err = %v", stdout, err)
	}

	stdout, stderr, err := execute(t, fakeOpener(t, fakegithub.New(ticket, release, assets)), "issue", "attachment", "prune")
	if err != nil || stdout != "1-b2c3d4e5-old.log\n" || !strings.Contains(stderr, "WARN: found 1 unlinked attachment(s): pass --yes to delete them") {
		t.Fatalf("stdout = %q, stderr = %q, err = %v", stdout, stderr, err)
	}
	gh = fakegithub.New(ticket, release, assets, deleted)
	stdout, _, err = execute(t, fakeOpener(t, gh), "issue", "attachment", "prune", "--yes")
	if err != nil || stdout != "1-b2c3d4e5-old.log\nOK: deleted 1 unlinked attachment(s)\n" {
		t.Fatalf("stdout = %q, err = %v", stdout, err)
	}
	none := fakegithub.Route{Match: "GET api.github.com/repos/o/tracker/releases/5/assets", Body: `[{"id":8,"name":"1-a1b2c3d4-run.log"}]`}
	stdout, _, err = execute(t, fakeOpener(t, fakegithub.New(ticket, release, none)), "issue", "attachment", "prune")
	if err != nil || stdout != "OK: no unlinked attachments\n" {
		t.Fatalf("stdout = %q, err = %v", stdout, err)
	}
	if _, _, err = execute(t, fakeOpener(t, fakegithub.New()), "issue", "attachment", "prune"); err == nil {
		t.Fatal("a failed read must fail the command")
	}
}
