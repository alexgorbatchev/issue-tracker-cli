package tracker

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// removedNote replaces a link to a deleted attachment.
const removedNote = "(attachment removed)"

// assetName matches the names uploadAsset writes: the ticket's number, a random part,
// and the file's name.
var assetName = regexp.MustCompile(`^(\d+)-[0-9a-f]{8}-.`)

// RemoveAttachments deletes a ticket's files from the attachments release, then marks
// their links in its body and comments as removed. Deleting comes first, because
// removing a leaked secret matters more than tidy text. It takes asset names or links.
func (c *Client) RemoveAttachments(ctx context.Context, number int, files []string) ([]string, error) {
	names := make([]string, 0, len(files))
	for _, file := range files {
		if attachmentURL.MatchString(file) {
			return nil, fmt.Errorf("%s is in GitHub's attachment storage, which the API cannot delete from: remove its link with issue edit", file)
		}
		name, err := url.PathUnescape(path.Base(file))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", file, err)
		}
		if match := assetName.FindStringSubmatch(name); match == nil || match[1] != strconv.Itoa(number) {
			return nil, fmt.Errorf("%s is not an attachment of #%d", file, number)
		}
		names = append(names, name)
	}
	detail, err := c.InspectTicket(ctx, number)
	if err != nil {
		return nil, err
	}
	ids, err := c.assetIDs(ctx)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if _, ok := ids[name]; !ok {
			return nil, fmt.Errorf("attachment %s is not in the attachments release", name)
		}
	}
	for _, name := range names {
		if err := c.deleteAsset(ctx, ids[name]); err != nil {
			return nil, fmt.Errorf("deleting %s: %w", name, err)
		}
	}
	if err := c.unlinkAssets(ctx, detail, names); err != nil {
		return nil, fmt.Errorf("deleted %s, but marking their links failed: %w", strings.Join(names, ", "), err)
	}
	return names, nil
}

func (c *Client) deleteAsset(ctx context.Context, id int64) error {
	return c.rest.DoWithContext(ctx, http.MethodDelete, fmt.Sprintf("repos/%s/releases/assets/%d", c.repo, id), nil, nil)
}

// unlinkAssets replaces the links to the named assets in a ticket's body and comments
// with removedNote, keeping a link's text, in one request.
func (c *Client) unlinkAssets(ctx context.Context, detail TicketDetail, names []string) error {
	asset := c.releaseAssetURL()
	link := regexp.MustCompile(`!?\[([^\]]*)\]\((` + asset.String() + `)\)`)
	removed := func(target string) bool {
		name, err := url.PathUnescape(path.Base(target))
		return err == nil && slices.Contains(names, name)
	}
	unlink := func(text string) string {
		text = link.ReplaceAllStringFunc(text, func(match string) string {
			parts := link.FindStringSubmatch(match)
			if !removed(parts[2]) {
				return match
			}
			return strings.TrimSpace(parts[1] + " " + removedNote)
		})
		return asset.ReplaceAllStringFunc(text, func(target string) string {
			if removed(target) {
				return removedNote
			}
			return target
		})
	}
	var fields []string
	if body := unlink(detail.Body); body != detail.Body {
		fields = append(fields, fmt.Sprintf(`body: updateIssue(input: {id: %q, body: %q}) { clientMutationId }`, detail.ID, body))
	}
	for i, comment := range detail.Comments {
		if body := unlink(comment.Body); body != comment.Body {
			fields = append(fields, fmt.Sprintf(`comment%d: updateIssueComment(input: {id: %q, body: %q}) { clientMutationId }`, i, comment.id, body))
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return c.write(ctx, fields)
}

// PruneAttachments lists the assets of the attachments release that their ticket no
// longer links to, and deletes them when remove is set. A failed write after an
// upload, or an edit that drops a link, leaves such an asset. Assets whose names this
// tool did not write are left alone.
func (c *Client) PruneAttachments(ctx context.Context, remove bool) ([]string, error) {
	releaseID, err := c.attachmentsRelease(ctx, false)
	if err != nil || releaseID == 0 {
		return nil, err
	}
	ids, err := c.listAssets(ctx, releaseID)
	if err != nil {
		return nil, err
	}
	byTicket := map[int][]string{}
	for name := range ids {
		if match := assetName.FindStringSubmatch(name); match != nil {
			number, _ := strconv.Atoi(match[1]) // the pattern admits only digits
			byTicket[number] = append(byTicket[number], name)
		}
	}
	numbers := make([]int, 0, len(byTicket))
	for number := range byTicket {
		numbers = append(numbers, number)
	}
	slices.Sort(numbers)
	linked := map[string]bool{}
	for batch := range slices.Chunk(numbers, MaxInspect) {
		details, _, err := c.readDetails(ctx, batch)
		if err != nil {
			return nil, err
		}
		for _, detail := range details {
			for _, link := range detail.Attachments {
				if name, err := url.PathUnescape(path.Base(link)); err == nil {
					linked[name] = true
				}
			}
		}
	}
	var unlinked []string
	for _, number := range numbers {
		for _, name := range byTicket[number] {
			if !linked[name] {
				unlinked = append(unlinked, name)
			}
		}
	}
	slices.Sort(unlinked)
	if !remove {
		return unlinked, nil
	}
	for _, name := range unlinked {
		if err := c.deleteAsset(ctx, ids[name]); err != nil {
			return nil, fmt.Errorf("deleting %s: %w", name, err)
		}
	}
	return unlinked, nil
}
