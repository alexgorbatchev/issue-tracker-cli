package tracker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"
)

// attachmentsTag names the prerelease that holds the files GitHub's attachment
// endpoint refuses, which takes only images and video. Its assets are served only to
// people with access to the tracker.
const attachmentsTag = "attachments"

// mediaExtensions are the files GitHub's attachment endpoint accepts, which gh
// uploads. Every other file becomes an asset of the attachments release.
var mediaExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": true,
	".mp4": true, ".mov": true, ".webm": true,
}

func isMedia(file string) bool {
	return mediaExtensions[strings.ToLower(filepath.Ext(file))]
}

// fileReference matches a Markdown link or image target, such as the ./run.log in
// [log](./run.log).
var fileReference = regexp.MustCompile(`\]\(([^()\s]+)\)`)

type releaseAsset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// checkFiles stops before any request when a file to attach cannot be read.
func checkFiles(files []string) error {
	for _, file := range files {
		if _, err := os.Stat(file); err != nil {
			return fmt.Errorf("attachment: %w", err)
		}
	}
	return nil
}

// linkFiles uploads each file gh cannot to the attachments release and links it from
// body: a reference such as [log](./run.log) is pointed at the asset, and a file the
// text does not reference is appended. It returns the body and the files left for gh.
func (c *Client) linkFiles(ctx context.Context, number int, body string, files []string) (string, []string, error) {
	var media, other []string
	for _, file := range files {
		if isMedia(file) {
			media = append(media, file)
		} else {
			other = append(other, file)
		}
	}
	if len(other) == 0 {
		return body, media, nil
	}
	releaseID, err := c.attachmentsRelease(ctx, true)
	if err != nil {
		return "", nil, err
	}
	for _, file := range other {
		link, err := c.uploadAsset(ctx, releaseID, number, file)
		if err != nil {
			return "", nil, err
		}
		referenced := false
		body = fileReference.ReplaceAllStringFunc(body, func(match string) string {
			if filepath.Clean(match[2:len(match)-1]) != filepath.Clean(file) {
				return match
			}
			referenced = true
			return "](" + link + ")"
		})
		if !referenced {
			body = appendParagraph(body, fmt.Sprintf("[%s](%s)", filepath.Base(file), link))
		}
	}
	return body, media, nil
}

func appendParagraph(body, paragraph string) string {
	if body == "" {
		return paragraph
	}
	return body + "\n\n" + paragraph
}

// attachmentsRelease finds the attachments release, creating it when create is set
// and reporting 0 when it is missing otherwise. Two agents can create it at once, so
// the one GitHub refuses reads the other's.
func (c *Client) attachmentsRelease(ctx context.Context, create bool) (int64, error) {
	var found struct {
		ID int64 `json:"id"`
	}
	tagPath := fmt.Sprintf("repos/%s/releases/tags/%s", c.repo, attachmentsTag)
	err := c.rest.DoWithContext(ctx, http.MethodGet, tagPath, nil, &found)
	switch {
	case err == nil:
		return found.ID, nil
	case !isNotFound(err):
		return 0, fmt.Errorf("reading attachments release: %w", err)
	case !create:
		return 0, nil
	}
	request, err := json.Marshal(map[string]any{
		"tag_name": attachmentsTag, "name": "Ticket attachments", "prerelease": true, "make_latest": "false",
		"body": "Files attached to tickets by issue-tracker. Deleting an asset breaks the links to it.",
	})
	if err != nil {
		return 0, fmt.Errorf("encoding attachments release: %w", err)
	}
	err = c.rest.DoWithContext(ctx, http.MethodPost, fmt.Sprintf("repos/%s/releases", c.repo), strings.NewReader(string(request)), &found)
	if isUnprocessable(err) {
		err = c.rest.DoWithContext(ctx, http.MethodGet, tagPath, nil, &found)
	}
	if err != nil {
		return 0, fmt.Errorf("creating attachments release: %w", err)
	}
	return found.ID, nil
}

func isNotFound(err error) bool {
	var httpErr *api.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound
}

// uploadAsset stores a file in the attachments release, named after its ticket and
// a random part so that names never collide, and returns the URL people open it at.
func (c *Client) uploadAsset(ctx context.Context, releaseID int64, number int, file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", fmt.Errorf("attachment: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only; a close failure changes nothing
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("attachment: %w", err)
	}
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("naming attachment: %w", err)
	}
	name := fmt.Sprintf("%d-%s-%s", number, hex.EncodeToString(random), filepath.Base(file))
	target := fmt.Sprintf("https://uploads.%s/repos/%s/releases/%d/assets?name=%s", defaultHost, c.repo, releaseID, url.QueryEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, f)
	if err != nil {
		return "", fmt.Errorf("preparing upload: %w", err)
	}
	req.ContentLength = info.Size()
	contentType := mime.TypeByExtension(filepath.Ext(file))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("uploading %s: %w", file, err)
	}
	defer func() { _ = resp.Body.Close() }() // the decoded body is all that matters
	if resp.StatusCode != http.StatusCreated {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("uploading %s: HTTP %d: %s", file, resp.StatusCode, strings.TrimSpace(string(message)))
	}
	var asset releaseAsset
	if err := json.NewDecoder(resp.Body).Decode(&asset); err != nil {
		return "", fmt.Errorf("uploading %s: %w", file, err)
	}
	return asset.BrowserDownloadURL, nil
}

// releaseAssetURL matches the links linkFiles writes, for this tracker only.
func (c *Client) releaseAssetURL() *regexp.Regexp {
	return regexp.MustCompile(`https://github\.com/` + regexp.QuoteMeta(c.repo.String()) + `/releases/download/` + attachmentsTag + `/[^\s()<>\[\]]+`)
}

// assetIDs maps the attachments release's asset names to their IDs, 100 per request.
func (c *Client) assetIDs(ctx context.Context) (map[string]int64, error) {
	releaseID, err := c.attachmentsRelease(ctx, false)
	if err != nil {
		return nil, err
	}
	if releaseID == 0 {
		return nil, errors.New("the tracker has no attachments release")
	}
	ids := map[string]int64{}
	for page := 1; ; page++ {
		var assets []releaseAsset
		listPath := fmt.Sprintf("repos/%s/releases/%d/assets?per_page=100&page=%d", c.repo, releaseID, page)
		if err := c.rest.DoWithContext(ctx, http.MethodGet, listPath, nil, &assets); err != nil {
			return nil, fmt.Errorf("listing attachments: %w", err)
		}
		for _, asset := range assets {
			ids[asset.Name] = asset.ID
		}
		if len(assets) < 100 {
			return ids, nil
		}
	}
}

// downloadAsset fetches a release asset through the API, which redirects to GitHub's
// storage host; Go drops the token on that redirect.
func (c *Client) downloadAsset(ctx context.Context, link string, ids map[string]int64, dir string) (string, error) {
	name, err := url.PathUnescape(path.Base(link))
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", link, err)
	}
	id, ok := ids[name]
	if !ok {
		return "", fmt.Errorf("attachment %s is no longer in the attachments release", name)
	}
	target := fmt.Sprintf("https://api.%s/repos/%s/releases/assets/%d", defaultHost, c.repo, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", fmt.Errorf("preparing download: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Authorization", "Bearer "+c.token)
	return c.save(req, dir, func(string) string { return name })
}
