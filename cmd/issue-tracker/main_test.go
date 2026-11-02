package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func ticketJSON(number int, project, status, updated string) string {
	blob := `{"id":"I1","number":1,"title":"Crash","state":"OPEN","url":"https://t/1","updatedAt":"UPDATED",` +
		`"labels":{"nodes":[{"name":"repo:PROJECT"}]},"blockedBy":{"nodes":[]},"parent":null,` +
		`"projectItems":{"nodes":[{"id":"ITEM","updatedAt":"UPDATED","project":{"id":"B1"},"fieldValueByName":{"name":"STATUS"}}]}`
	return strings.NewReplacer("UPDATED", updated, "PROJECT", project, "STATUS", status).Replace(blob)
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

func TestWriteCommandsReportResults(t *testing.T) {
	merged := fakegithub.Route{Match: "pullRequest(number: 88)", Body: `{"data":{"r0":{"pullRequest":{"merged":true,"url":"https://p/88"}}}}`}
	claimRef := fakegithub.Route{Match: "POST api.github.com/repos/o/tracker/git/refs", Status: 201, Body: "{}"}
	drop := fakegithub.Route{Match: "DELETE", Status: 204}
	status := ok("updateProjectV2ItemFieldValue")
	created := fakegithub.Route{Match: "createIssue(input", Body: `{"data":{"createIssue":{"issue":{"id":"N","number":50,"url":"https://t/50","projectItems":{"nodes":[{"id":"IT"}]}}}}}`}
	tests := []struct {
		name   string
		routes []fakegithub.Route
		args   []string
		want   string
	}{
		{"claim", []fakegithub.Route{ticketRoute("go-tui", "Todo"), claimRef, status}, []string{"issue", "claim", "1"}, "OK: claimed #1 in go-tui: Crash"},
		{"claim public warns", []fakegithub.Route{ticketRoute("lib", "Todo"), claimRef, status}, []string{"issue", "claim", "1"}, "WARN: lib is public"},
		{"claim next", []fakegithub.Route{listRoute(ticketJSON(1, "go-tui", "Todo", "2026-10-01T00:00:00Z")), claimRef, status}, []string{"issue", "claim", "--next", "--project", "go-tui"}, "OK: claimed #1"},
		{"claim next empty", []fakegithub.Route{listRoute()}, []string{"issue", "claim", "--next"}, "OK: no ready tickets"},
		{"release", []fakegithub.Route{ticketRoute("go-tui", "Doing"), drop, status}, []string{"issue", "release", "1"}, "OK: released #1"},
		{"close", []fakegithub.Route{ticketRoute("go-tui", "Todo"), merged, ok("closeIssue")}, []string{"issue", "close", "1", "--pr", "88"}, "OK: closed #1"},
		{"close reason", []fakegithub.Route{ticketRoute("go-tui", "Todo"), ok("closeIssue")}, []string{"issue", "close", "1", "--reason", "not-planned"}, "OK: closed #1"},
		{"reopen", []fakegithub.Route{ticketRoute("go-tui", "Done"), ok("reopenIssue"), status}, []string{"issue", "reopen", "1"}, "OK: reopened #1"},
		{"create", []fakegithub.Route{created, status}, []string{"issue", "create", "--project", "go-tui", "--title", "T", "--body", "B"}, "OK: created #50 in go-tui"},
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

func TestIssueListOutput(t *testing.T) {
	old := ticketJSON(1, "go-tui", "Doing", "2026-01-01T00:00:00Z")
	t.Run("agent uses tabs", func(t *testing.T) {
		t.Setenv("AGENT", "1")
		outsideRepository(t)
		stdout, _, err := execute(t, fakeOpener(t, fakegithub.New(listRoute(old))), "issue", "list")
		if err != nil {
			t.Fatal(err)
		}
		if want := "1\tDoing (stale)\tgo-tui\t\tCrash\n"; stdout != want {
			t.Fatalf("stdout = %q, want %q", stdout, want)
		}
	})
	t.Run("human uses a table", func(t *testing.T) {
		t.Setenv("AGENT", "0")
		outsideRepository(t)
		stdout, _, err := execute(t, fakeOpener(t, fakegithub.New(listRoute(old))), "issue", "list", "--stale", "--stale-days", "2")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"TICKET", "Doing (stale)", "Crash", "│"} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("stdout = %q, missing %q", stdout, want)
			}
		}
	})
	t.Run("human reports an empty list", func(t *testing.T) {
		t.Setenv("AGENT", "0")
		outsideRepository(t)
		stdout, _, err := execute(t, fakeOpener(t, fakegithub.New(listRoute())), "issue", "list")
		if err != nil || !strings.Contains(stdout, "No tickets found.") {
			t.Fatalf("stdout = %q, err = %v", stdout, err)
		}
	})
}

func TestCurrentRepositorySelectsProject(t *testing.T) {
	t.Setenv("AGENT", "1")
	t.Setenv("GH_TOKEN", "tok")
	t.Setenv("GH_REPO", "o/go-tui")
	gh := fakegithub.New(listRoute())
	open := fakeOpener(t, gh)
	if _, _, err := execute(t, open, "issue", "list"); err != nil {
		t.Fatal(err)
	}
	if labels := gh.Requests()[0].Variables["labels"]; labels == nil {
		t.Fatal("a registered repository must scope the list")
	}
	if _, _, err := execute(t, open, "issue", "list", "--all-projects"); err != nil {
		t.Fatal(err)
	}
	if labels := gh.Requests()[1].Variables["labels"]; labels != nil {
		t.Fatalf("--all-projects must not scope: %v", labels)
	}
	if _, _, err := execute(t, open, "issue", "list", "--project", "lib"); err != nil {
		t.Fatal(err)
	}
	if labels := gh.Requests()[2].Variables["labels"].([]any); labels[0] != "repo:lib" {
		t.Fatalf("an explicit project must win: %v", labels)
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
	if got := gh.Requests()[0].Variables["search"]; got != `repo:o/tracker is:issue sort:created-asc is:open label:"repo:go-tui" crash resize` {
		t.Fatalf("search = %v", got)
	}
	stdout, _, err := execute(t, fakeOpener(t, fakegithub.New(listRoute(ticketJSON(1, "go-tui", "Todo", "2026-10-01T00:00:00Z")))), "issue", "list", "--label", "bug")
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
	metadata := fakegithub.Route{Match: "labels(first: 100", Body: `{"data":{"repository":{"id":"R","isPrivate":true,"owner":{"id":"O"},"defaultBranchRef":{"target":{"oid":"s"}},
"labels":{"nodes":[{"id":"L1","name":"repo:go-tui","description":"o/go-tui"}],"pageInfo":{"hasNextPage":false,"endCursor":""}},
"projectsV2":{"nodes":[{"id":"B1","title":"go-tui","url":"https://b/1","field":{"id":"F","options":[{"id":"t","name":"Todo"}]}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`}
	visibility := fakegithub.Route{Match: "r0: repository", Body: `{"data":{"r0":{"isPrivate":false}}}`}
	summary := fakegithub.Route{Match: "open: search", Body: `{"data":{"open":{"issueCount":2},"closed":{"issueCount":5}}}`}
	tests := []struct {
		name   string
		routes []fakegithub.Route
		args   []string
		want   string
	}{
		{"list", []fakegithub.Route{metadata, visibility}, []string{"project", "list"}, "go-tui\to/go-tui\tpublic\thttps://b/1\n"},
		{"inspect", []fakegithub.Route{summary}, []string{"project", "inspect", "go-tui"}, "visibility: private\nboard: https://b/1\nopen: 2\nclosed: 5\n"},
		{"add existing", []fakegithub.Route{metadata, visibility}, []string{"project", "add", "o/go-tui"}, "OK: registered project go-tui"},
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

func TestProjectRm(t *testing.T) {
	t.Setenv("AGENT", "1")
	outsideRepository(t)
	_, _, err := execute(t, noSession, "project", "rm", "go-tui")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v", err)
	}
	gh := fakegithub.New(
		fakegithub.Route{Match: "open: search", Body: `{"data":{"open":{"issueCount":0},"closed":{"issueCount":0}}}`},
		ok("deleteProjectV2"), ok("updateLabel"),
		fakegithub.Route{Match: "labels(first: 100", Body: `{"data":{"repository":{"id":"R","isPrivate":true,"owner":{"id":"O"},"defaultBranchRef":{"target":{"oid":"s"}},"labels":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},"projectsV2":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`},
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
	body = strings.Replace(body, `{"name":"repo:lib"}`, `{"name":"repo:lib"},{"name":"bug"}`, 1)
	route := fakegithub.Route{Match: "subIssues(first", Body: `{"data":{"repository":{"isPrivate":true,"issue":` + body + `,"body":"Text https://github.com/user-attachments/assets/abc","blocking":{"nodes":[{"number":4,"state":"OPEN"}]},"subIssues":{"nodes":[{"number":6,"title":"Child","state":"OPEN"}]},"comments":{"nodes":[{"author":{"login":"a"},"createdAt":"2026-01-01T00:00:00Z","body":"Note"}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}}`}
	stdout, _, err := execute(t, fakeOpener(t, fakegithub.New(route)), "issue", "inspect", "1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"labels: bug", "WARN: lib is public", "status: Todo", "parent: 5", "blocked_by: 3", "blocking: 4", "sub_issue: 6 OPEN Child", "attachment: https://github.com/user-attachments/assets/abc", "body:\nText", "comment: a 2026-01-01T00:00:00Z\nNote"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, missing %q", stdout, want)
		}
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
	stdout, _, err := execute(t, fakeOpener(t, fakegithub.New()), "issue", "attachment", "add", "1", "a.png", "b.png")
	if err != nil || !strings.Contains(stdout, "OK: attached 2 file(s) to #1") {
		t.Fatalf("stdout = %q, err = %v", stdout, err)
	}

	body := ticketJSON(1, "go-tui", "Todo", "2026-10-01T00:00:00Z")
	gh := fakegithub.New(
		fakegithub.Route{Match: "subIssues(first", Body: `{"data":{"repository":{"isPrivate":true,"issue":` + body + `,"body":"https://github.com/user-attachments/assets/abc","blocking":{"nodes":[]},"subIssues":{"nodes":[]},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}}`},
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
		{"project add outside a repository", []string{"project", "add"}, "pass owner/repo"},
		{"project add bad repo", []string{"project", "add", "bad"}, "invalid repository"},
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
	issues := fakegithub.Route{Match: "issues(states: OPEN, first: 100", Body: `{"data":{"repository":{"isPrivate":false,"issues":{"nodes":[
{"id":"A","number":1,"author":{"login":"o"},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false}}},
{"id":"B","number":2,"author":{"login":"stranger"},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false}}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`}
	tests := []struct {
		name  string
		agent string
		want  string
	}{
		{"agent", "1", "o/code\tpublic\t2\t1\t1\n"},
		{"human", "0", "FROM OTHERS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AGENT", tt.agent)
			stdout, _, err := execute(t, fakeOpener(t, fakegithub.New(repositories, issues)), "project", "scan")
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

func TestProjectImportRejectsBadRepository(t *testing.T) {
	outsideRepository(t)
	if _, _, err := execute(t, fakeOpener(t, fakegithub.New()), "project", "import", "bad"); err == nil || !strings.Contains(err.Error(), "invalid repository") {
		t.Fatalf("err = %v", err)
	}
}
