package tracker

import (
	"strings"
	"testing"

	"github.com/alexgorbatchev/issue-tracker-cli/v2/internal/fakegithub"
)

const (
	runLog   = "https://github.com/o/tracker/releases/download/attachments/7-a1b2c3d4-run.log"
	traceLog = "https://github.com/o/tracker/releases/download/attachments/7-b2c3d4e5-trace.log"
)

// linkedTicket is #7 with run.log linked from its body and both logs from a comment.
func linkedTicket() fakegithub.Route {
	return inspectRoute(`"body":"Crash. [the log](` + runLog + `)","blocking":{"nodes":[]},"subIssues":{"nodes":[]},` +
		`"comments":{"nodes":[{"id":"C1","author":{"login":"a"},"createdAt":"2026-01-01T00:00:00Z","body":"again ` + runLog + ` and ![t](` + traceLog + `)"}],"pageInfo":{"hasNextPage":false,"endCursor":""}}`)
}

func assetsRoute(names ...string) fakegithub.Route {
	nodes := make([]string, 0, len(names))
	for i, name := range names {
		nodes = append(nodes, `{"id":`+string(rune('1'+i))+`,"name":"`+name+`"}`)
	}
	return fakegithub.Route{Match: "GET api.github.com/repos/o/tracker/releases/5/assets", Body: "[" + strings.Join(nodes, ",") + "]"}
}

func TestRemoveAttachmentsDeletesThenMarksTheLinks(t *testing.T) {
	c, gh := newClient(t, linkedTicket(), releaseRoute(), assetsRoute("7-a1b2c3d4-run.log", "7-b2c3d4e5-trace.log"),
		fakegithub.Route{Match: "DELETE api.github.com/repos/o/tracker/releases/assets/1", Status: 204}, okRoute("updateIssueComment"))
	names, err := c.RemoveAttachments(ctx, 7, []string{runLog})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "7-a1b2c3d4-run.log" {
		t.Fatalf("names = %v", names)
	}
	wantCount(t, gh, 5)
	write := lastDescription(gh)
	for _, want := range []string{
		`updateIssue(input: {id: "I7", body: "Crash. the log (attachment removed)"})`,
		`updateIssueComment(input: {id: "C1", body: "again (attachment removed) and ![t](` + traceLog + `)"})`,
	} {
		if !strings.Contains(write, want) {
			t.Fatalf("write = %s, missing %s", write, want)
		}
	}

	c, gh = newClient(t, linkedTicket(), releaseRoute(), assetsRoute("7-c3d4e5f6-other.log"),
		fakegithub.Route{Match: "DELETE api.github.com", Status: 204})
	if _, err := c.RemoveAttachments(ctx, 7, []string{"7-c3d4e5f6-other.log"}); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 4)
}

func TestRemoveAttachmentsRefusals(t *testing.T) {
	c, gh := newClient(t)
	tests := []struct{ file, want string }{
		{"https://github.com/user-attachments/assets/abc", "the API cannot delete from"},
		{"8-a1b2c3d4-run.log", "not an attachment of #7"},
		{"run.log", "not an attachment of #7"},
		{"https://github.com/o/tracker/releases/download/attachments/%zz", "reading"},
	}
	for _, tt := range tests {
		_, err := c.RemoveAttachments(ctx, 7, []string{tt.file})
		wantError(t, err, tt.want)
	}
	wantCount(t, gh, 0)

	c, gh = newClient(t, linkedTicket(), releaseRoute(), assetsRoute("7-b2c3d4e5-trace.log"))
	_, err := c.RemoveAttachments(ctx, 7, []string{runLog})
	wantError(t, err, "not in the attachments release")
	for _, req := range gh.Requests() {
		if strings.HasPrefix(req.Description, "DELETE") {
			t.Fatal("nothing may be deleted until every file is found")
		}
	}

	failures := []struct {
		name   string
		routes []fakegithub.Route
		want   string
	}{
		{"ticket", []fakegithub.Route{{Match: "subIssues(first", Status: 500, Body: "{}"}}, "reading"},
		{"release", []fakegithub.Route{linkedTicket(), {Match: "releases/tags", Status: 500, Body: "{}"}}, "reading attachments release"},
		{"delete", []fakegithub.Route{linkedTicket(), releaseRoute(), assetsRoute("7-a1b2c3d4-run.log"), {Match: "DELETE api.github.com", Status: 500, Body: "{}"}}, "deleting 7-a1b2c3d4-run.log"},
		{"links", []fakegithub.Route{linkedTicket(), releaseRoute(), assetsRoute("7-a1b2c3d4-run.log"), {Match: "DELETE api.github.com", Status: 204}, {Match: "updateIssue", Status: 500, Body: "{}"}}, "deleted 7-a1b2c3d4-run.log, but marking their links failed"},
	}
	for _, tt := range failures {
		c, _ := newClient(t, tt.routes...)
		if _, err := c.RemoveAttachments(ctx, 7, []string{runLog}); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error = %v, want %q", tt.name, err, tt.want)
		}
	}
}

// pruneDetails answers the batch read: #7 links run.log, and #9 is gone.
func pruneDetails() fakegithub.Route {
	base := ticketSpec{number: 7, project: "lib", status: "Todo"}.json()
	return fakegithub.Route{Match: "n9: issue(number: 9)", Body: `{"data":{"repository":{"isPrivate":true,"n7":` + base +
		`,"body":"[log](` + runLog + `)","blocking":{"nodes":[]},"subIssues":{"nodes":[]},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}},"n9":null}},"errors":[{"type":"NOT_FOUND","message":"gone"}]}`}
}

func TestPruneAttachmentsFindsFilesNoTicketLinks(t *testing.T) {
	assets := assetsRoute("7-a1b2c3d4-run.log", "7-b2c3d4e5-trace.log", "9-c3d4e5f6-gone.log", "manual.txt")
	c, gh := newClient(t, releaseRoute(), assets, pruneDetails())
	names, err := c.PruneAttachments(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, " ") != "7-b2c3d4e5-trace.log 9-c3d4e5f6-gone.log" {
		t.Fatalf("names = %v", names)
	}
	wantCount(t, gh, 3)

	c, gh = newClient(t, releaseRoute(), assets, pruneDetails(), fakegithub.Route{Match: "DELETE api.github.com", Status: 204})
	if names, err = c.PruneAttachments(ctx, true); err != nil || len(names) != 2 {
		t.Fatalf("names = %v, err = %v", names, err)
	}
	var deleted []string
	for _, req := range gh.Requests() {
		if strings.HasPrefix(req.Description, "DELETE") {
			deleted = append(deleted, req.Description)
		}
	}
	if strings.Join(deleted, " ") != "DELETE api.github.com/repos/o/tracker/releases/assets/2 DELETE api.github.com/repos/o/tracker/releases/assets/3" {
		t.Fatalf("deleted = %v", deleted)
	}
}

func TestPruneAttachmentsFailures(t *testing.T) {
	c, gh := newClient(t, fakegithub.Route{Match: "releases/tags/attachments", Status: 404, Body: "{}"})
	if names, err := c.PruneAttachments(ctx, true); err != nil || names != nil {
		t.Fatalf("a tracker without the release has nothing to prune: %v, %v", names, err)
	}
	wantCount(t, gh, 1)

	assets := assetsRoute("7-b2c3d4e5-trace.log")
	tests := []struct {
		name   string
		routes []fakegithub.Route
		want   string
	}{
		{"release", []fakegithub.Route{{Match: "releases/tags", Status: 500, Body: "{}"}}, "reading attachments release"},
		{"assets", []fakegithub.Route{releaseRoute(), {Match: "releases/5/assets", Status: 500, Body: "{}"}}, "listing attachments"},
		{"tickets", []fakegithub.Route{releaseRoute(), assets, {Match: "subIssues(first", Status: 500, Body: "{}"}}, "reading ticket"},
		{"delete", []fakegithub.Route{releaseRoute(), assets, inspectRoute(`"body":"","blocking":{"nodes":[]},"subIssues":{"nodes":[]},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}`), {Match: "DELETE api.github.com", Status: 500, Body: "{}"}}, "deleting 7-b2c3d4e5-trace.log"},
	}
	for _, tt := range tests {
		c, _ := newClient(t, tt.routes...)
		if _, err := c.PruneAttachments(ctx, true); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error = %v, want %q", tt.name, err, tt.want)
		}
	}
}
