package tracker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexgorbatchev/issue-tracker-cli/internal/fakegithub"
)

const metadataJSON = `{"data":{"repository":{"id":"REPO","isPrivate":true,"owner":{"id":"OWNER"},"defaultBranchRef":{"target":{"oid":"sha1"}},
"labels":{"nodes":[
 {"id":"L1","name":"repo:go-tui","description":"o/go-tui"},
 {"id":"L9","name":"repo:gone","description":""},
 {"id":"L8","name":"repo:mismatch","description":"o/other"},
 {"id":"L7","name":"other-repo:x","description":"o/x"}
],"pageInfo":{"hasNextPage":false,"endCursor":""}},
"projectsV2":{"nodes":[{"id":"B1","title":"go-tui","url":"https://b/1","field":{"id":"F1","options":[{"id":"T","name":"Todo"},{"id":"G","name":"Doing"},{"id":"D","name":"Done"}]}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`

func metadataRoute() fakegithub.Route {
	return fakegithub.Route{Match: "labels(first: 100", Body: metadataJSON}
}

func visibilityRoute(body string) fakegithub.Route {
	return fakegithub.Route{Match: "r0: repository", Body: body}
}

func TestRefreshBuildsProjectsAndCachesThem(t *testing.T) {
	gh := fakegithub.New(metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	c := clientWith(t, gh, nil)
	projects, err := c.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("projects = %+v; labels without a matching repository must not register", projects)
	}
	project := projects[0]
	if project.Name != "go-tui" || project.BoardID != "B1" || project.StatusOptions["Done"] != "D" || project.Public {
		t.Fatalf("project = %+v", project)
	}
	wantCount(t, gh, 2)

	info, err := os.Stat(c.cachePath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache mode = %v, want owner-only", info.Mode().Perm())
	}
	data, err := os.ReadFile(c.cachePath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "body") || strings.Contains(string(data), "title\":\"Ticket") {
		t.Fatalf("cache must hold no ticket data: %s", data)
	}

	fresh, err := New(Options{Repo: c.repo, CacheDir: c.cacheDir, Transport: gh, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.lookupProject(context.Background(), "go-tui"); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 2)
}

func TestRefreshFollowsBothConnections(t *testing.T) {
	first := `{"data":{"repository":{"id":"REPO","isPrivate":true,"owner":{"id":"O"},"defaultBranchRef":{"target":{"oid":"s"}},
"labels":{"nodes":[{"id":"L1","name":"repo:a","description":"o/a"}],"pageInfo":{"hasNextPage":true,"endCursor":"next"}},
"projectsV2":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`
	second := `{"data":{"repository":{"id":"REPO","isPrivate":true,"owner":{"id":"O"},"defaultBranchRef":{"target":{"oid":"s"}},
"labels":{"nodes":[{"id":"L2","name":"repo:b","description":"o/b"}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`
	gh := fakegithub.New(
		fakegithub.Route{Match: "labels(first: 100", Body: first, Times: 1},
		fakegithub.Route{Match: "labels(first: 100", Body: second},
		visibilityRoute(`{"data":{"r0":{"isPrivate":true},"r1":{"isPrivate":false}}}`),
	)
	projects, err := clientWith(t, gh, nil).Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 || !projects[1].Public {
		t.Fatalf("projects = %+v", projects)
	}
	requests := gh.Requests()
	if requests[1].Variables["labels"] != "next" || requests[1].Variables["withBoards"] != false {
		t.Fatalf("second page variables = %v", requests[1].Variables)
	}
}

func TestUnresolvableRepositoryCountsAsPublic(t *testing.T) {
	gh := fakegithub.New(metadataRoute(), visibilityRoute(`{"data":{"r0":null},"errors":[{"type":"NOT_FOUND","message":"gone"}]}`))
	projects, err := clientWith(t, gh, nil).Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !projects[0].Public {
		t.Fatal("an unknown repository must count as public")
	}
}

func TestRequirePrivate(t *testing.T) {
	public := strings.Replace(metadataJSON, `"isPrivate":true`, `"isPrivate":false`, 1)
	gh := fakegithub.New(fakegithub.Route{Match: "labels(first: 100", Body: public})
	c, err := New(Options{Repo: Repo{"o", "tracker"}, CacheDir: t.TempDir(), Transport: gh, Token: "tok", RequirePrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Projects(context.Background())
	wantError(t, err, "not private")
	if _, err := os.Stat(c.cachePath()); err == nil {
		t.Fatal("a refused tracker must not be cached")
	}

	off := clientWith(t, fakegithub.New(fakegithub.Route{Match: "labels(first: 100", Body: public}, visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`)), nil)
	if _, err := off.Projects(context.Background()); err != nil {
		t.Fatalf("the check is off by default: %v", err)
	}
}

func TestCachedPublicTrackerIsRefused(t *testing.T) {
	writer := clientWith(t, fakegithub.New(), &metadata{IsPrivate: false})
	c, err := New(Options{Repo: writer.repo, CacheDir: writer.cacheDir, Transport: fakegithub.New(), Token: "tok", RequirePrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.lookupProject(context.Background(), "x")
	wantError(t, err, "not private")
}

func TestMetadataRecovery(t *testing.T) {
	gh := fakegithub.New(metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	c := clientWith(t, gh, nil)
	if err := os.MkdirAll(c.cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.cachePath(), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.lookupProject(context.Background(), "go-tui"); err != nil {
		t.Fatalf("a corrupt cache must rebuild: %v", err)
	}
	wantCount(t, gh, 2)
}

func TestMetadataReadFailure(t *testing.T) {
	c := clientWith(t, fakegithub.New(), nil)
	if err := os.MkdirAll(c.cachePath(), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := c.metadata(context.Background())
	wantError(t, err, "reading metadata cache")
	wantError(t, c.saveMetadata(&metadata{}), "replacing metadata cache")
}

func TestSaveMetadataNeedsWritableParent(t *testing.T) {
	c := clientWith(t, fakegithub.New(), nil)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c.cacheDir = filepath.Join(blocker, "cache")
	wantError(t, c.saveMetadata(&metadata{}), "creating cache directory")
}

func TestLookupMissRefreshesOnce(t *testing.T) {
	gh := fakegithub.New(metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	c := clientWith(t, gh, &metadata{IsPrivate: true})
	project, err := c.ResolveProject(context.Background(), "go-tui")
	if err != nil || project.Name != "go-tui" {
		t.Fatalf("project = %+v, err = %v", project, err)
	}
	_, err = c.lookupProject(context.Background(), "unknown")
	wantError(t, err, "unknown project")
	wantCount(t, gh, 4)
}

func TestWithFreshIDsRetriesOnce(t *testing.T) {
	gh := fakegithub.New(metadataRoute(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	c := clientWith(t, gh, testMetadata())
	calls := 0
	err := c.withFreshIDs(context.Background(), func(meta *metadata) error {
		calls++
		if calls == 1 {
			return graphQLError(graphQLNotFoundType)
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("calls = %d, err = %v", calls, err)
	}
}
