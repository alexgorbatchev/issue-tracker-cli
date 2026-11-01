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
	"strconv"

	"github.com/cli/go-gh/v2"
)

// AddAttachments uploads files to a ticket. GitHub has no documented upload API,
// so this is the one operation that runs the gh binary.
func (c *Client) AddAttachments(ctx context.Context, number int, files []string) error {
	if len(files) == 0 {
		return nil
	}
	args := []string{"issue", "edit", strconv.Itoa(number), "--repo", c.repo.String()}
	for _, file := range files {
		args = append(args, "--attach", file)
	}
	if _, stderr, err := gh.ExecContext(ctx, args...); err != nil {
		return fmt.Errorf("uploading attachments: %w: %s", err, stderr.String())
	}
	return nil
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
	for _, url := range detail.Attachments {
		file, err := c.download(ctx, url, dir)
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
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading attachment: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() // read-only response; a close failure changes nothing
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: HTTP %d", url, resp.StatusCode)
	}
	file := filepath.Join(dir, attachmentName(url, resp.Header.Get("Content-Type")))
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
