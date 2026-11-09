package tracker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexgorbatchev/issue-tracker-cli/v2/internal/fakegithub"
	ghconfig "github.com/cli/go-gh/v2/pkg/config"
)

func TestMetadataCacheFollowsEffectiveAuthentication(t *testing.T) {
	for _, mode := range []string{"configured account", "environment token", "active login", "rotated token"} {
		t.Run(mode, func(t *testing.T) {
			cache := t.TempDir()
			t.Setenv("GH_PATH", "")
			t.Setenv("GH_TOKEN", "")
			t.Setenv("GITHUB_TOKEN", "")
			readConfig := ghconfig.Read
			ghconfig.Read = func(*ghconfig.Config) (*ghconfig.Config, error) { return ghconfig.ReadFromString(""), nil }
			t.Cleanup(func() { ghconfig.Read = readConfig })
			fakeGH(t, `printf '%s\n' "$ISSUE_TRACKER_TEST_TOKEN"`)

			for _, step := range []struct {
				account, token, board string
				cached                bool
			}{
				{"agent", "test-agent-credential", "", false},
				{"owner", "test-owner-credential", "B1", false},
				{"owner", "test-owner-credential", "B1", true},
				{"agent", "test-agent-credential", "", true},
			} {
				gh := fakegithub.New()
				if !step.cached {
					route := metadataRoute()
					if step.board == "" {
						// A registered project remains visible while its private board does not.
						route.Body = strings.Replace(route.Body, `"title":"go-tui"`, `"title":"unrelated"`, 1)
					}
					gh.Add(route, visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
				}
				opts := Options{Repo: Repo{"o", "tracker"}, CacheDir: cache, Transport: gh}
				switch mode {
				case "configured account":
					t.Setenv("GH_TOKEN", "ignored-environment-credential")
					t.Setenv("ISSUE_TRACKER_TEST_TOKEN", step.token)
					opts.Account = step.account
				case "environment token":
					t.Setenv("GH_TOKEN", step.token)
				case "active login":
					t.Setenv("ISSUE_TRACKER_TEST_TOKEN", step.token)
				case "rotated token":
					opts.Account, opts.Token = "same-login", step.token
				}
				c, err := New(opts)
				if err != nil {
					t.Fatal(err)
				}
				project, err := c.ResolveProject(ctx, "go-tui")
				if err != nil || project.BoardID != step.board {
					t.Fatalf("account %s, cached %v: project = %+v, error = %v", step.account, step.cached, project, err)
				}
				requests := 2
				if step.cached {
					requests = 0
				}
				wantCount(t, gh, requests)
				for _, request := range gh.Requests() {
					if !strings.HasSuffix(request.Header.Get("Authorization"), " "+step.token) {
						t.Fatal("metadata was fetched with a different credential")
					}
				}
			}

			files, err := os.ReadDir(cache)
			if err != nil || len(files) != 2 {
				t.Fatalf("want one cache per effective credential, got %d files: %v", len(files), err)
			}
			for _, file := range files {
				data, err := os.ReadFile(filepath.Join(cache, file.Name()))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(file.Name()+string(data), "credential") {
					t.Fatal("metadata cache exposes a credential")
				}
			}
		})
	}
}

func TestMetadataCacheSeparatesTrackerRepositories(t *testing.T) {
	cache := t.TempDir()
	for i, repo := range []Repo{{"o-team", "tracker"}, {"o", "team-tracker"}, {"o-team", "tracker"}} {
		gh := fakegithub.New()
		board := "B1"
		if i == 1 {
			board = "B2"
		}
		if i < 2 {
			route := metadataRoute()
			route.Body = strings.ReplaceAll(route.Body, "B1", board)
			gh.Add(route, visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
		}
		c, err := New(Options{Repo: repo, CacheDir: cache, Transport: gh, Token: "tok"})
		if err != nil {
			t.Fatal(err)
		}
		project, err := c.ResolveProject(ctx, "go-tui")
		if err != nil || project.BoardID != board {
			t.Fatalf("repository %s: project = %+v, error = %v", repo, project, err)
		}
		if i < 2 {
			wantCount(t, gh, 2)
		} else {
			wantCount(t, gh, 0)
		}
	}
}

func TestMetadataIgnoresUnscopedCache(t *testing.T) {
	gh := fakegithub.New(metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	c := clientWith(t, gh, nil)
	legacy, err := json.Marshal(&metadata{IsPrivate: true, Projects: []Project{{Name: "go-tui", Repo: "o/go-tui"}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.cacheDir, "o-tracker.json")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	project, err := c.ResolveProject(ctx, "go-tui")
	if err != nil || project.BoardID != "B1" {
		t.Fatalf("an unscoped cache must not be reused: project = %+v, error = %v", project, err)
	}
	wantCount(t, gh, 2)
	if data, err := os.ReadFile(path); err != nil || string(data) != string(legacy) {
		t.Fatalf("the unscoped cache must remain untouched: %v", err)
	}
}

func TestClaimReportsRegisteredButInvisibleBoard(t *testing.T) {
	c, gh := newClient(t, append(bareRefresh(false), ticketRoute(ticketSpec{number: 12, project: "bare"}),
		fakegithub.Route{Match: refsPost, Status: 201, Body: "{}"},
		fakegithub.Route{Match: "DELETE api.github.com/repos/o/tracker/git/refs/claims/12", Status: 204})...)
	c.account = "agent"
	_, err := c.ClaimTicket(ctx, 12)
	wantError(t, err, "registered but its board is missing or inaccessible")
	wantError(t, err, "GitHub account agent")
	wantError(t, err, "Write access")
	if strings.Contains(err.Error(), "project add") {
		t.Fatalf("invisible boards must not suggest registering again: %v", err)
	}
	wantCount(t, gh, 5)
}

func TestStatusDistinguishesAnUnregisteredProject(t *testing.T) {
	c, gh := newClient(t)
	err := c.writeStatus(ctx, Ticket{Number: 12, Project: "unknown"}, StatusTodo)
	wantError(t, err, `unknown project "unknown": register it with project add`)
	wantCount(t, gh, 0)
}
