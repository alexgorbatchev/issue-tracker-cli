package main

import (
	"strings"
	"testing"
	"time"

	"github.com/alexgorbatchev/issue-tracker-cli/v2/internal/fakegithub"
	"github.com/alexgorbatchev/issue-tracker-cli/v2/internal/tracker"
)

const unknownVisibility = `{"data":{"r0":null,"r1":{"isPrivate":false}},"errors":[{"type":"NOT_FOUND","path":["r0"],"message":"unavailable"}]}`

func TestUnknownVisibilityWarnings(t *testing.T) {
	for _, mode := range []string{"0", "1"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AGENT", mode)
			gh := fakegithub.New()
			open := fakeVisibilityOpener(t, gh, unknownVisibility)
			for _, kind := range []string{"claim", "handoff", "inspect"} {
				t.Run(kind, func(t *testing.T) {
					cmd := newRootCommand(open)
					var out strings.Builder
					cmd.SetOut(&out)
					s, err := open(cmd)
					if err != nil {
						t.Fatal(err)
					}
					ticket := tracker.Ticket{Number: 1, Project: "go-tui", Title: "Task"}
					switch kind {
					case "claim":
						err = reportClaim(cmd, s, ticket)
					case "handoff":
						err = printHandoff(cmd, s, tracker.Handoff{Ticket: ticket}, time.Now())
					case "inspect":
						err = printDetails(&out, []tracker.TicketDetail{{Ticket: ticket, Visibility: tracker.VisibilityUnknown}, {Ticket: ticket, Visibility: tracker.VisibilityUnknown}})
					}
					if err != nil {
						t.Fatal(err)
					}
					output := out.String()
					prefix := "[WARN] "
					if mode == "1" {
						prefix = "WARN: "
					}
					if !strings.HasPrefix(output, prefix+"go-tui has unknown visibility (repository missing or inaccessible); treat as public until privacy is confirmed:") || strings.Count(output, "has unknown visibility") != 1 || !strings.Contains(output, "keep ticket text, attachments, and tracker identity private") {
						t.Fatalf("output = %q", output)
					}
				})
			}
			if gh.Count() != 0 {
				t.Fatalf("cached warnings made %d requests", gh.Count())
			}
		})
	}
}

func TestUnknownVisibilityProjectOutput(t *testing.T) {
	for _, mode := range []string{"0", "1"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AGENT", mode)
			var out strings.Builder
			project := tracker.Project{Name: "demo", Repo: "o/demo", Visibility: tracker.VisibilityUnknown}
			if err := printProjects(&out, []tracker.Project{project}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "unknown") || strings.Contains(out.String(), "public") {
				t.Fatalf("list = %q", out.String())
			}
			out.Reset()
			if err := printSummary(&out, tracker.ProjectSummary{Project: project}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "visibility: unknown\n") {
				t.Fatalf("inspect = %q", out.String())
			}
		})
	}
}

func TestInspectPropagatesUnknownVisibility(t *testing.T) {
	t.Setenv("AGENT", "1")
	outsideRepository(t)
	node := ticketJSON(1, "go-tui", "Todo", "2026-10-01T00:00:00Z") + `,"body":"Body","blocking":{"nodes":[]},"subIssues":{"nodes":[]},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false}}}`
	gh := fakegithub.New(fakegithub.Route{Match: "subIssues(first", Body: `{"data":{"repository":{"isPrivate":true,"n1":` + node + `}}}`})
	out, _, err := execute(t, fakeVisibilityOpener(t, gh, unknownVisibility), "issue", "inspect", "1")
	if err != nil || !strings.HasPrefix(out, "WARN: go-tui has unknown visibility") {
		t.Fatalf("output = %q, error = %v", out, err)
	}
	if gh.Count() != 1 {
		t.Fatalf("requests = %d, want 1", gh.Count())
	}
}
