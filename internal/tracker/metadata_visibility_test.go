package tracker

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/alexgorbatchev/issue-tracker-cli/v2/internal/fakegithub"
)

const deniedVisibility = `{"data":{"r0":null},"errors":[{"type":"FORBIDDEN","path":["r0"],"message":"Resource protected by organization SAML enforcement"}]}`

func TestVisibilityKeepsAccessibleRepositories(t *testing.T) {
	metadataBody := strings.Replace(metadataJSON, `"labels":{"nodes":[`, `"labels":{"nodes":[{"id":"L2","name":"repo:lib","description":"o/lib"},`, 1)
	for _, errorType := range []string{"NOT_FOUND", "FORBIDDEN"} {
		for _, path := range []string{`["r0"]`, `["r0","isPrivate"]`} {
			t.Run(errorType+path, func(t *testing.T) {
				body := fmt.Sprintf(`{"data":{"r0":null,"r1":{"isPrivate":true}},"errors":[{"type":%q,"path":%s,"message":"unavailable"}]}`, errorType, path)
				gh := fakegithub.New(fakegithub.Route{Match: metadataRoute().Match, Body: metadataBody}, visibilityRoute(body))
				c := clientWith(t, gh, nil)
				projects, err := c.Projects(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(projects) != 2 {
					t.Fatalf("projects = %+v", projects)
				}
				for _, project := range projects {
					want := VisibilityPrivate
					if project.Name == "lib" {
						want = VisibilityUnknown
					}
					if project.Visibility != want {
						t.Fatalf("visibility = %+v", project)
					}
				}
				cached, err := c.lookupProject(ctx, "lib")
				if err != nil || cached.Visibility != VisibilityUnknown {
					t.Fatalf("cached unavailable project = %+v, %v", cached, err)
				}
				wantCount(t, gh, 2)
			})
		}
	}
}

func TestVisibilityRejectsRequestFailures(t *testing.T) {
	tests := []struct {
		name, body, want string
		status           int
	}{
		{"authentication", `{"message":"Bad credentials"}`, "Bad credentials", 401},
		{"http permission", `{"message":"Forbidden"}`, "Forbidden", 403},
		{"server", `{"message":"unavailable"}`, "unavailable", 503},
		{"invalid response", `{`, "unexpected end", 200},
		{"global denial", `{"errors":[{"type":"FORBIDDEN","message":"global"}]}`, "global", 200},
		{"unknown alias", `{"errors":[{"type":"FORBIDDEN","path":["r9"],"message":"unknown alias"}]}`, "unknown alias", 200},
		{"invalid alias", `{"errors":[{"type":"FORBIDDEN","path":["r00"],"message":"invalid alias"}]}`, "invalid alias", 200},
		{"non-string path", `{"errors":[{"type":"FORBIDDEN","path":[0],"message":"invalid path"}]}`, "invalid path", 200},
		{"other field", `{"errors":[{"type":"FORBIDDEN","path":["r0","name"],"message":"other field"}]}`, "other field", 200},
		{"mixed failure", `{"errors":[{"type":"NOT_FOUND","path":["r0"],"message":"gone"},{"type":"INTERNAL","path":["r0"],"message":"server failure"}]}`, "server failure", 200},
		{"rate limit", `{"errors":[{"type":"NOT_FOUND","path":["r0"],"message":"gone"},{"type":"RATE_LIMITED","message":"rate limit"}]}`, "GitHub rate limit reached", 200},
		{"board scopes", `{"errors":[{"type":"NOT_FOUND","path":["r0"],"message":"gone"},{"type":"INSUFFICIENT_SCOPES","message":"needs project"}]}`, "no access to project boards", 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route := visibilityRoute(tt.body)
			route.Status = tt.status
			gh := fakegithub.New(metadataRoute(), route)
			c := clientWith(t, gh, nil)
			_, err := c.Projects(ctx)
			wantError(t, err, "reading project visibility")
			wantError(t, err, tt.want)
			if _, err := os.Stat(c.cachePath()); !os.IsNotExist(err) {
				t.Fatalf("failed visibility must not be cached: %v", err)
			}
			wantCount(t, gh, 2)
		})
	}
}

func TestVisibilityDiscardsDeniedFields(t *testing.T) {
	metadataBody := strings.Replace(metadataJSON, `"labels":{"nodes":[`, `"labels":{"nodes":[{"id":"L2","name":"repo:lib","description":"o/lib"},`, 1)
	body := `{"data":{"r0":{"isPrivate":true},"r1":{"isPrivate":true}},"errors":[{"type":"NOT_FOUND","path":["r0"],"message":"gone"},{"type":"FORBIDDEN","path":["r1","isPrivate"],"message":"denied"}]}`
	gh := fakegithub.New(fakegithub.Route{Match: metadataRoute().Match, Body: metadataBody}, visibilityRoute(body))
	projects, err := clientWith(t, gh, nil).Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 || projects[0].Visibility != VisibilityUnknown || projects[1].Visibility != VisibilityUnknown {
		t.Fatalf("failed fields cannot establish privacy: %+v", projects)
	}
	wantCount(t, gh, 2)
}

func TestAddProjectWithUnrelatedDeniedVisibility(t *testing.T) {
	before := metadataRoute()
	before.Times = 1
	after := metadataRoute()
	after.Body = strings.Replace(metadataJSON, `"labels":{"nodes":[`, `"labels":{"nodes":[{"id":"LN","name":"repo:new","description":"o/new"},`, 1)
	denied := visibilityRoute(deniedVisibility)
	denied.Times = 1
	finalVisibility := visibilityRoute(`{"data":{"r0":{"isPrivate":true},"r1":null},"errors":[{"type":"FORBIDDEN","path":["r1"],"message":"Resource protected by organization SAML enforcement"}]}`)
	routes := append([]fakegithub.Route{before, after, denied, finalVisibility}, boardRoutes()...)
	gh := fakegithub.New(routes...)
	c := clientWith(t, gh, nil)
	project, err := c.AddProject(ctx, Repo{"o", "new"})
	if err != nil {
		t.Fatal(err)
	}
	if project.Name != "new" || project.Repo != "o/new" || project.LabelID != "LN" || project.BoardID != "BN" || project.Visibility != VisibilityPrivate {
		t.Fatalf("registered project = %+v", project)
	}
	unavailable, err := c.lookupProject(ctx, "go-tui")
	if err != nil || unavailable.Visibility != VisibilityUnknown {
		t.Fatalf("unavailable project must retain unknown visibility: %+v, %v", unavailable, err)
	}
	wantCount(t, gh, 9)
}

func TestAddProjectStillRequiresTargetAccess(t *testing.T) {
	tests := []struct {
		name, match string
		calls       int
	}{
		{"tracker metadata", metadataRoute().Match, 1},
		{"target repository", boardRoutes()[0].Match, 4},
		{"target board", boardRoutes()[1].Match, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			failure := fakegithub.Route{Match: tt.match, Body: `{"errors":[{"type":"FORBIDDEN","path":["repository"],"message":"target denied"}]}`}
			routes := append([]fakegithub.Route{failure, metadataRoute(), visibilityRoute(deniedVisibility)}, boardRoutes()...)
			gh := fakegithub.New(routes...)
			_, err := clientWith(t, gh, nil).AddProject(ctx, Repo{"o", "new"})
			wantError(t, err, "target denied")
			wantCount(t, gh, tt.calls)
		})
	}
}

func TestVisibilityStatesAndCache(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		want       Visibility
	}{
		{"private", `{"data":{"r0":{"isPrivate":true}}}`, VisibilityPrivate},
		{"public", `{"data":{"r0":{"isPrivate":false}}}`, VisibilityPublic},
		{"missing repository", `{"data":{"r0":null}}`, VisibilityUnknown},
		{"missing alias", `{"data":{}}`, VisibilityUnknown},
		{"missing privacy", `{"data":{"r0":{}}}`, VisibilityUnknown},
		{"null privacy", `{"data":{"r0":{"isPrivate":null}}}`, VisibilityUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gh := fakegithub.New(metadataRoute(), visibilityRoute(tt.body))
			c := clientWith(t, gh, nil)
			projects, err := c.Projects(ctx)
			if err != nil || len(projects) != 1 || projects[0].Visibility != tt.want {
				t.Fatalf("projects = %+v, error = %v", projects, err)
			}
			c.meta = nil
			project, err := c.ResolveProject(ctx, "go-tui")
			if err != nil || project.Visibility != tt.want {
				t.Fatalf("cached project = %+v, error = %v", project, err)
			}
			wantCount(t, gh, 2)
		})
	}
}

func TestVisibilityRefreshesLegacyCache(t *testing.T) {
	for _, value := range []string{`"public":true`, `"public":false`, `"visibility":"invalid"`} {
		t.Run(value, func(t *testing.T) {
			gh := fakegithub.New(metadataRoute(), visibilityRoute(`{"data":{"r0":null}}`))
			c := clientWith(t, gh, nil)
			if err := os.WriteFile(c.cachePath(), []byte(`{"is_private":true,"projects":[{"name":"go-tui","repo":"o/go-tui",`+value+`}]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			project, err := c.ResolveProject(ctx, "go-tui")
			if err != nil || project.Visibility != VisibilityUnknown {
				t.Fatalf("project = %+v, error = %v", project, err)
			}
			c.meta = nil
			project, err = c.ResolveProject(ctx, "go-tui")
			if err != nil || project.Visibility != VisibilityUnknown {
				t.Fatalf("refreshed cache = %+v, error = %v", project, err)
			}
			wantCount(t, gh, 2)
		})
	}
}
