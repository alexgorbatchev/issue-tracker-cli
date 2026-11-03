package tracker

import (
	"strings"
	"testing"
)

func TestCheckBodyLengthCountsCharacters(t *testing.T) {
	if err := checkBodyLength(strings.Repeat("é", maxBodyLength)); err != nil {
		t.Fatalf("a body at the limit in multibyte characters must pass: %v", err)
	}
	wantError(t, checkBodyLength(strings.Repeat("a", maxBodyLength+1)), "body is 65537 characters, over GitHub's 65536")
}

func TestAnOverlongBodyIsRefusedBeforeAnyRequest(t *testing.T) {
	long := strings.Repeat("a", maxBodyLength+1)
	c, gh := newClient(t)
	_, err := c.CreateTicket(ctx, CreateOptions{Project: "go-tui", Title: "t", Body: long})
	wantError(t, err, "over GitHub's 65536")
	_, err = c.CreateTicket(ctx, CreateOptions{Project: "go-tui", Title: "t", Body: strings.Repeat("a", maxBodyLength-10), DiscoveredFrom: 4})
	wantError(t, err, "over GitHub's 65536")
	wantError(t, c.EditTicket(ctx, 1, EditOptions{Body: &long}), "over GitHub's 65536")
	wantError(t, c.AddComment(ctx, 1, long), "over GitHub's 65536")
	wantCount(t, gh, 0)
}
