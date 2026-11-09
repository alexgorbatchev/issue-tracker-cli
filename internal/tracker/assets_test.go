package tracker

import (
	"mime"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexgorbatchev/issue-tracker-cli/v2/internal/fakegithub"
)

// fakeGHStdin is fakeGH that also records what gh reads from standard input.
func fakeGHStdin(t *testing.T) (string, string) {
	t.Helper()
	stdin := filepath.Join(t.TempDir(), "stdin")
	return fakeGH(t, "cat > "+stdin), stdin
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const assetLink = "https://github.com/o/tracker/releases/download/attachments/12-a1b2c3d4-run.log"

func releaseRoute() fakegithub.Route {
	return fakegithub.Route{Match: "GET api.github.com/repos/o/tracker/releases/tags/attachments", Body: `{"id":5}`}
}

func uploadRoute() fakegithub.Route {
	return fakegithub.Route{Match: "POST uploads.github.com/repos/o/tracker/releases/5/assets", Status: 201, Body: `{"id":9,"name":"12-a1b2c3d4-run.log","browser_download_url":"` + assetLink + `"}`}
}

func bodyRoute(body string) fakegithub.Route {
	return fakegithub.Route{Match: "issue(number: $number) { body }", Body: `{"data":{"repository":{"issue":{"body":"` + body + `"}}}}`}
}

func TestAnAttachedFileIsLinkedWhereTheBodyReferencesIt(t *testing.T) {
	log, stdin := fakeGHStdin(t)
	files := tempFiles(t, "run.log", "shot.png")
	body := "Crash.\n\n[the log](" + files[0] + ")\n\n![after](" + files[1] + ")"
	c, gh := newClient(t, releaseRoute(), uploadRoute())
	if err := c.AddAttachments(ctx, 12, &body, files); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 2)
	upload := gh.Requests()[1]
	contentType := mime.TypeByExtension(".log")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if upload.Body != "run.log" || upload.Header.Get("Content-Type") != contentType || upload.Header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("upload = %+v", upload)
	}
	if want := "Crash.\n\n[the log](" + assetLink + ")\n\n![after](" + files[1] + ")"; readFile(t, stdin) != want {
		t.Fatalf("gh read %q, want %q", readFile(t, stdin), want)
	}
	if want := "issue edit 12 --repo o/tracker --body-file - --attach " + files[1] + "\n"; readFile(t, log) != want {
		t.Fatalf("args = %q, want %q", readFile(t, log), want)
	}
}

func TestAnUnreferencedFileIsAppendedToTheCurrentBody(t *testing.T) {
	files := tempFiles(t, "run.log")
	c, gh := newClient(t, bodyRoute("Existing"), releaseRoute(), uploadRoute(), idsRoute(12), okRoute("updateIssue"))
	if err := c.AddAttachments(ctx, 12, nil, files); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 5)
	input := gh.Requests()[4].Variables["input"].(map[string]any)
	if want := "Existing\n\n[run.log](" + assetLink + ")"; input["body"] != want {
		t.Fatalf("body = %q, want %q", input["body"], want)
	}
}

func TestTheAttachmentsReleaseIsCreatedOnFirstUse(t *testing.T) {
	missing := fakegithub.Route{Match: "GET api.github.com/repos/o/tracker/releases/tags/attachments", Status: 404, Body: `{"message":"Not Found"}`, Times: 1}
	created := fakegithub.Route{Match: "POST api.github.com/repos/o/tracker/releases", Status: 201, Body: `{"id":5}`}
	c, gh := newClient(t, missing, created, uploadRoute())
	body := ""
	files := tempFiles(t, "run.log")
	if err := c.AddAttachments(ctx, 12, &body, files); err == nil {
		t.Fatal("the body write has no route, so the call must fail after the upload")
	}
	release := gh.Requests()[1]
	for _, want := range []string{`"tag_name":"attachments"`, `"prerelease":true`, `"make_latest":"false"`} {
		if !strings.Contains(release.Body, want) {
			t.Fatalf("release = %s, missing %s", release.Body, want)
		}
	}

	raced := fakegithub.Route{Match: "POST api.github.com/repos/o/tracker/releases", Status: 422, Body: `{"message":"Validation Failed"}`}
	c, gh = newClient(t, missing, raced, releaseRoute(), uploadRoute(), idsRoute(12), okRoute("updateIssue"))
	if err := c.AddAttachments(ctx, 12, &body, files); err != nil {
		t.Fatalf("a release created by another agent must be used: %v", err)
	}
	if input := gh.Requests()[5].Variables["input"].(map[string]any); input["body"] != "[run.log]("+assetLink+")" {
		t.Fatalf("body = %q", input["body"])
	}
}

func TestAttachmentFailures(t *testing.T) {
	files := tempFiles(t, "run.log")
	body := "x"
	tests := []struct {
		name   string
		routes []fakegithub.Route
		want   string
	}{
		{"release unreadable", []fakegithub.Route{{Match: "releases/tags/attachments", Status: 500, Body: "{}"}}, "reading attachments release"},
		{"release refused", []fakegithub.Route{{Match: "releases/tags/attachments", Status: 404, Body: "{}"}, {Match: "POST api.github.com/repos/o/tracker/releases", Status: 403, Body: "{}"}}, "creating attachments release"},
		{"upload refused", []fakegithub.Route{releaseRoute(), {Match: "POST uploads.github.com", Status: 422, Body: `{"message":"bad"}`}}, `HTTP 422: {"message":"bad"}`},
		{"upload unreadable", []fakegithub.Route{releaseRoute(), {Match: "POST uploads.github.com", Status: 201, Body: "not json"}}, "uploading"},
		{"body unreadable", []fakegithub.Route{{Match: "issue(number: $number) { body }", Status: 500, Body: "{}"}}, "reading ticket #12"},
		{"ticket missing", []fakegithub.Route{{Match: "issue(number: $number) { body }", Body: `{"data":{"repository":{"issue":null}}}`}}, "ticket #12 not found"},
	}
	for _, tt := range tests {
		c, _ := newClient(t, tt.routes...)
		var err error
		if strings.HasPrefix(tt.name, "body") || strings.HasPrefix(tt.name, "ticket") {
			err = c.AddAttachments(ctx, 12, nil, files)
		} else {
			err = c.AddAttachments(ctx, 12, &body, files)
		}
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error = %v, want %q", tt.name, err, tt.want)
		}
	}

	long := strings.Repeat("a", maxBodyLength-10)
	c, _ := newClient(t, releaseRoute(), uploadRoute())
	wantError(t, c.AddAttachments(ctx, 12, &long, files), "over GitHub's 65536")
}

func TestCommentsTakeAttachments(t *testing.T) {
	log, stdin := fakeGHStdin(t)
	files := tempFiles(t, "shot.png")
	c, gh := newClient(t)
	if err := c.AddComment(ctx, 12, "Before ![x]("+files[0]+")", files); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 0)
	if want := "issue comment 12 --repo o/tracker --body-file - --attach " + files[0] + "\n"; readFile(t, log) != want {
		t.Fatalf("args = %q", readFile(t, log))
	}
	if readFile(t, stdin) != "Before ![x]("+files[0]+")" {
		t.Fatalf("gh read %q", readFile(t, stdin))
	}

	logs := tempFiles(t, "run.log")
	c, gh = newClient(t, releaseRoute(), uploadRoute(), idsRoute(12), okRoute("addComment"))
	if err := c.AddComment(ctx, 12, "", logs); err != nil {
		t.Fatal(err)
	}
	if body := gh.Requests()[3].Variables["body"]; body != "[run.log]("+assetLink+")" {
		t.Fatalf("body = %q", body)
	}

	c, gh = newClient(t)
	wantError(t, c.AddComment(ctx, 12, "x", []string{"/missing.log"}), "attachment:")
	c, _ = newClient(t, releaseRoute(), fakegithub.Route{Match: "POST uploads.github.com", Status: 500, Body: "{}"})
	wantError(t, c.AddComment(ctx, 12, "x", logs), "HTTP 500")
	c, _ = newClient(t, releaseRoute(), uploadRoute())
	wantError(t, c.AddComment(ctx, 12, strings.Repeat("a", maxBodyLength-10), logs), "over GitHub's 65536")
	wantCount(t, gh, 0)
}

func TestEditAttachesWithANewTitle(t *testing.T) {
	log := fakeGH(t, "cat > /dev/null")
	title := "New"
	files := tempFiles(t, "shot.png")
	c, gh := newClient(t, idsRoute(12), okRoute("updateIssue"))
	if err := c.EditTicket(ctx, 12, EditOptions{Title: &title, Attachments: files}); err != nil {
		t.Fatal(err)
	}
	if input := gh.Requests()[1].Variables["input"].(map[string]any); input["title"] != "New" || input["body"] != nil {
		t.Fatalf("input = %v", input)
	}
	if want := "issue edit 12 --repo o/tracker --attach " + files[0] + "\n"; readFile(t, log) != want {
		t.Fatalf("args = %q", readFile(t, log))
	}
	wantError(t, c.EditTicket(ctx, 12, EditOptions{Attachments: []string{"/missing.png"}}), "attachment:")
	c, _ = newClient(t, fakegithub.Route{Match: "n12: issue", Status: 500, Body: "{}"})
	wantError(t, c.EditTicket(ctx, 12, EditOptions{Title: &title, Attachments: files}), "500")
}

func releaseTicket(link string) fakegithub.Route {
	return inspectRoute(`"body":"[log](` + link + `) https://github.com/other/repo/releases/download/attachments/x.log","blocking":{"nodes":[]},"subIssues":{"nodes":[]},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}`)
}

func TestPullDownloadsReleaseAttachments(t *testing.T) {
	assets := fakegithub.Route{Match: "GET api.github.com/repos/o/tracker/releases/5/assets", Body: `[{"id":9,"name":"12-a1b2c3d4-run.log"}]`}
	asset := fakegithub.Route{Match: "GET api.github.com/repos/o/tracker/releases/assets/9", Body: "log lines", Headers: map[string]string{"Content-Type": "application/octet-stream"}}
	c, gh := newClient(t, releaseTicket(assetLink), releaseRoute(), assets, asset)
	dir := t.TempDir()
	files, err := c.PullAttachments(ctx, 7, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != filepath.Join(dir, "12-a1b2c3d4-run.log") || readFile(t, files[0]) != "log lines" {
		t.Fatalf("another repository's release is not this tracker's attachment: %v", files)
	}
	download := gh.Requests()[3]
	if download.Header.Get("Accept") != "application/octet-stream" || download.Header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("download = %+v", download)
	}

	c, _ = newClient(t, releaseTicket(strings.Replace(assetLink, "run.log", "gone.log", 1)), releaseRoute(), assets)
	_, err = c.PullAttachments(ctx, 7, t.TempDir())
	wantError(t, err, "no longer in the attachments release")

	c, _ = newClient(t, releaseTicket(assetLink), fakegithub.Route{Match: "releases/tags/attachments", Status: 404, Body: "{}"})
	_, err = c.PullAttachments(ctx, 7, t.TempDir())
	wantError(t, err, "no attachments release")

	c, _ = newClient(t, releaseTicket(assetLink), releaseRoute(), fakegithub.Route{Match: "releases/5/assets", Status: 500, Body: "{}"})
	_, err = c.PullAttachments(ctx, 7, t.TempDir())
	wantError(t, err, "listing attachments")

	c, _ = newClient(t, releaseTicket(assetLink), fakegithub.Route{Match: "releases/tags/attachments", Status: 500, Body: "{}"})
	_, err = c.PullAttachments(ctx, 7, t.TempDir())
	wantError(t, err, "reading attachments release")

	c, _ = newClient(t, releaseTicket("https://github.com/o/tracker/releases/download/attachments/%zz"), releaseRoute(), assets)
	_, err = c.PullAttachments(ctx, 7, t.TempDir())
	wantError(t, err, "reading https://")
}
