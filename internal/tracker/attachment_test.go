package tracker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexgorbatchev/issue-tracker-cli/v2/internal/fakegithub"
)

// fakeGH puts a stub gh first on PATH that records its arguments.
func fakeGH(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "args")
	content := "#!/bin/sh\necho \"$@\" >> " + log + "\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// tempFiles creates empty files with these names and returns their paths.
func tempFiles(t *testing.T, names ...string) []string {
	t.Helper()
	dir := t.TempDir()
	paths := make([]string, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	return paths
}

func TestAddAttachmentsAppendsMediaThroughGH(t *testing.T) {
	log := fakeGH(t, "exit 0")
	files := tempFiles(t, "a.png", "b.mp4")
	c, gh := newClient(t)
	if err := c.AddAttachments(ctx, 12, nil, files); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if want := "issue edit 12 --repo o/tracker --attach " + files[0] + " --attach " + files[1] + "\n"; string(data) != want {
		t.Fatalf("args = %q, want %q", data, want)
	}
	wantCount(t, gh, 0)
	if err := c.AddAttachments(ctx, 12, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestGHUploadsAsTheClientsAccount(t *testing.T) {
	t.Setenv("GH_TOKEN", "active-tok")
	token := filepath.Join(t.TempDir(), "token")
	fakeGH(t, `printf %s "$GH_TOKEN" > `+token)
	c, _ := newClient(t)
	if err := c.AddAttachments(ctx, 12, nil, tempFiles(t, "a.png")); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, token); got != "tok" {
		t.Fatalf("gh ran with token %q, want the client's", got)
	}
}

func TestAddAttachmentsReportsGHFailure(t *testing.T) {
	fakeGH(t, "echo upload refused >&2; exit 1")
	c, _ := newClient(t)
	wantError(t, c.AddAttachments(ctx, 12, nil, tempFiles(t, "a.png")), "upload refused")

	t.Setenv("PATH", t.TempDir())
	t.Setenv("GH_PATH", "")
	wantError(t, c.AddAttachments(ctx, 12, nil, tempFiles(t, "a.png")), "needs the gh binary")
	wantError(t, c.AddAttachments(ctx, 12, nil, []string{"/missing.png"}), "attachment:")
}

func attachmentTicket() fakegithub.Route {
	return inspectRoute(`"body":"![x](https://github.com/user-attachments/assets/abc) https://github.com/user-attachments/files/1/log.txt","blocking":{"nodes":[]},"subIssues":{"nodes":[]},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}`)
}

func TestPullAttachments(t *testing.T) {
	c, gh := newClient(t, attachmentTicket(),
		fakegithub.Route{Match: "GET github.com/user-attachments/assets/abc", Body: "png", Headers: map[string]string{"Content-Type": "image/png"}},
		fakegithub.Route{Match: "GET github.com/user-attachments/files/1/log.txt", Body: "log", Headers: map[string]string{"Content-Type": "text/plain"}})
	dir := filepath.Join(t.TempDir(), "pulled")
	files, err := c.PullAttachments(ctx, 7, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || filepath.Base(files[0]) != "abc.png" || filepath.Base(files[1]) != "log.txt" {
		t.Fatalf("files = %v", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil || string(data) != "png" {
		t.Fatalf("data = %q, err = %v", data, err)
	}
	info, err := os.Stat(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	requests := gh.Requests()
	if got := requests[1].Header.Get("Authorization"); got != "Bearer tok" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestPullAttachmentFailures(t *testing.T) {
	c, _ := newClient(t, attachmentTicket(), fakegithub.Route{Match: "GET github.com/user-attachments/assets/abc", Status: 404, Body: "{}"})
	_, err := c.PullAttachments(ctx, 7, t.TempDir())
	wantError(t, err, "HTTP 404")

	c, _ = newClient(t, fakegithub.Route{Match: "subIssues(first", Status: 500, Body: "{}"})
	_, err = c.PullAttachments(ctx, 7, t.TempDir())
	wantError(t, err, "reading ticket")

	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c, _ = newClient(t, attachmentTicket())
	_, err = c.PullAttachments(ctx, 7, filepath.Join(blocker, "sub"))
	wantError(t, err, "creating attachment directory")

	c, _ = newClient(t, attachmentTicket(), fakegithub.Route{Match: "GET github.com", Body: "x", Headers: map[string]string{"Content-Type": "x/unknown"}})
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "abc"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = c.PullAttachments(ctx, 7, dir)
	wantError(t, err, "creating attachment file")
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("remote", "add", "origin", "git@github.com:o/go-tui.git")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".tmp/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCheckOutsideWorktree(t *testing.T) {
	repo := gitRepo(t)
	tests := []struct {
		name string
		dir  string
		ok   bool
	}{
		{"outside any worktree", filepath.Join(t.TempDir(), "new", "dir"), true},
		{"tracked location", filepath.Join(repo, "shots"), false},
		{"worktree root", repo, false},
		{"ignored location", filepath.Join(repo, ".tmp", "shots"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkOutsideWorktree(tt.dir)
			if tt.ok && err != nil {
				t.Fatal(err)
			}
			if !tt.ok {
				wantError(t, err, "inside a Git worktree")
			}
		})
	}
}

func TestPullRefusesWorktreeBeforeCallingGitHub(t *testing.T) {
	c, gh := newClient(t)
	_, err := c.PullAttachments(ctx, 7, filepath.Join(gitRepo(t), "shots"))
	wantError(t, err, "inside a Git worktree")
	wantCount(t, gh, 0)
}

func TestAttachmentName(t *testing.T) {
	tests := []struct{ url, contentType, want string }{
		{"https://github.com/user-attachments/assets/abc", "image/png", "abc.png"},
		{"https://github.com/user-attachments/files/1/report.pdf", "application/pdf", "report.pdf"},
		{"https://github.com/user-attachments/assets/abc", "", "abc"},
		{"https://github.com/user-attachments/assets/abc", "application/x-unknown-thing", "abc"},
	}
	for _, tt := range tests {
		if got := attachmentName(tt.url, tt.contentType); !strings.HasPrefix(got, strings.TrimSuffix(tt.want, ".png")) {
			t.Fatalf("attachmentName(%q, %q) = %q", tt.url, tt.contentType, got)
		}
	}
}
