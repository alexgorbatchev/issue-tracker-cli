package tracker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/cli/go-gh/v2"
)

// AddAttachments uploads files to a ticket's body. With body set, it replaces the
// body and the body's references to the files are pointed at them; otherwise the
// files are appended to the current body. Images and video go through gh, the one
// operation that runs it, because GitHub's attachment endpoint is undocumented.
func (c *Client) AddAttachments(ctx context.Context, number int, body *string, files []string) error {
	if len(files) == 0 {
		return nil
	}
	if err := checkFiles(files); err != nil {
		return err
	}
	text, replace := "", body != nil
	if replace {
		text = *body
	} else if slices.ContainsFunc(files, func(file string) bool { return !isMedia(file) }) {
		current, err := c.ticketBody(ctx, number)
		if err != nil {
			return err
		}
		text, replace = current, true
	}
	text, media, err := c.linkFiles(ctx, number, text, files)
	if err != nil {
		return err
	}
	if err := checkBodyLength(text); err != nil {
		return err
	}
	if len(media) == 0 {
		return c.EditTicket(ctx, number, EditOptions{Body: &text})
	}
	args := []string{"issue", "edit", strconv.Itoa(number), "--repo", c.repo.String()}
	if !replace {
		return runGH(ctx, nil, append(args, attachArgs(media)...))
	}
	return runGH(ctx, strings.NewReader(text), append(append(args, "--body-file", "-"), attachArgs(media)...))
}

// AddComment adds a comment to a ticket, with files attached where its text
// references them, or after it.
func (c *Client) AddComment(ctx context.Context, number int, body string, files []string) error {
	if err := checkBodyLength(body); err != nil {
		return err
	}
	if err := checkFiles(files); err != nil {
		return err
	}
	text, media, err := c.linkFiles(ctx, number, body, files)
	if err != nil {
		return err
	}
	if err := checkBodyLength(text); err != nil {
		return err
	}
	if len(media) > 0 {
		args := []string{"issue", "comment", strconv.Itoa(number), "--repo", c.repo.String(), "--body-file", "-"}
		return runGH(ctx, strings.NewReader(text), append(args, attachArgs(media)...))
	}
	ids, err := c.issueIDs(ctx, []int{number})
	if err != nil {
		return err
	}
	mutation := `mutation($id: ID!, $body: String!) { addComment(input: {subjectId: $id, body: $body}) { clientMutationId } }`
	return c.do(ctx, mutation, map[string]any{"id": ids[number], "body": text}, nil)
}

func attachArgs(files []string) []string {
	args := make([]string, 0, 2*len(files))
	for _, file := range files {
		args = append(args, "--attach", file)
	}
	return args
}

// runGH runs gh with stdin, which carries a body so that gh can place the images
// the body references.
func runGH(ctx context.Context, stdin io.Reader, args []string) error {
	path, err := gh.Path()
	if err != nil {
		return fmt.Errorf("uploading attachments needs the gh binary: %w", err)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = stdin
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("uploading attachments: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// ticketBody reads a ticket's current body.
func (c *Client) ticketBody(ctx context.Context, number int) (string, error) {
	var response struct {
		Repository struct {
			Issue *struct{ Body string }
		}
	}
	query := `query($owner: String!, $name: String!, $number: Int!) { repository(owner: $owner, name: $name) { issue(number: $number) { body } } }`
	if err := c.do(ctx, query, c.variables(map[string]any{"number": number}), &response); err != nil {
		return "", fmt.Errorf("reading ticket #%d: %w", number, err)
	}
	if response.Repository.Issue == nil {
		return "", fmt.Errorf("ticket #%d not found in %s", number, c.repo)
	}
	return response.Repository.Issue.Body, nil
}

// PullAttachments downloads a ticket's attachments into dir and returns their paths.
func (c *Client) PullAttachments(ctx context.Context, number int, dir string) ([]string, error) {
	if err := checkOutsideWorktree(dir); err != nil {
		return nil, err
	}
	detail, err := c.InspectTicket(ctx, number)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating attachment directory: %w", err)
	}
	files := make([]string, 0, len(detail.Attachments))
	releaseAsset := c.releaseAssetURL()
	var ids map[string]int64
	for _, url := range detail.Attachments {
		var file string
		if releaseAsset.MatchString(url) {
			if ids == nil {
				if ids, err = c.assetIDs(ctx); err != nil {
					return nil, err
				}
			}
			file, err = c.downloadAsset(ctx, url, ids, dir)
		} else {
			file, err = c.download(ctx, url, dir)
		}
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

// checkOutsideWorktree stops a pull into a tracked location, where a later git add
// would publish a private screenshot. Ignored paths inside a worktree are allowed.
func checkOutsideWorktree(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolving attachment directory: %w", err)
	}
	existing := abs
	for {
		if _, err := os.Stat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return nil
		}
		existing = parent
	}
	if err := exec.Command("git", "-C", existing, "rev-parse", "--is-inside-work-tree").Run(); err != nil {
		return nil // outside any worktree
	}
	if err := exec.Command("git", "-C", existing, "check-ignore", "-q", abs).Run(); err == nil {
		return nil
	}
	return errors.New("attachment directory is inside a Git worktree and not ignored: choose an ignored or external directory")
}

// download fetches one asset. The URL always matched attachmentURL, so the token
// only reaches github.com; Go drops it when GitHub redirects to its storage host.
func (c *Client) download(ctx context.Context, url, dir string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("preparing download: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	return c.save(req, dir, func(contentType string) string { return attachmentName(url, contentType) })
}

// save writes a download into dir, under the name the response's content type gives.
func (c *Client) save(req *http.Request, dir string, name func(contentType string) string) (string, error) {
	url := req.URL.String()
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading attachment: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() // read-only response; a close failure changes nothing
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: HTTP %d", url, resp.StatusCode)
	}
	file := filepath.Join(dir, name(resp.Header.Get("Content-Type")))
	out, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("creating attachment file: %w", err)
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close() // the copy failure is the error worth reporting
		return "", fmt.Errorf("writing attachment: %w", err)
	}
	if err := out.Close(); err != nil {
		return "", fmt.Errorf("closing attachment: %w", err)
	}
	return file, nil
}

func attachmentName(url, contentType string) string {
	name := path.Base(url)
	if path.Ext(name) != "" {
		return name
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return name
	}
	extensions, err := mime.ExtensionsByType(mediaType)
	if err != nil || len(extensions) == 0 {
		return name
	}
	return name + extensions[0]
}
