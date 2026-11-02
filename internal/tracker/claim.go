package tracker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"
)

const claimRefPrefix = "claims/"

// ErrAlreadyClaimed reports that another agent holds the claim.
var ErrAlreadyClaimed = errors.New("ticket is already claimed")

// ClaimTicket takes a ticket for the caller. GitHub rejects a second create of the
// same ref, which makes the claim atomic where labels, assignees, and project
// fields would accept both writers. GraphQL cannot see this namespace, so claims use REST.
func (c *Client) ClaimTicket(ctx context.Context, number int) (Ticket, error) {
	ticket, err := c.readTicket(ctx, number)
	if err != nil {
		return Ticket{}, err
	}
	if ticket.State != stateOpen {
		return Ticket{}, fmt.Errorf("ticket #%d is closed", number)
	}
	return c.claim(ctx, ticket)
}

// ClaimNext claims the oldest ready ticket, moving on when another agent wins a
// candidate. It reports false when no ticket is ready. Project may be empty.
func (c *Client) ClaimNext(ctx context.Context, project string) (Ticket, bool, error) {
	candidates, _, err := c.ListTickets(ctx, ListOptions{Project: project, Ready: true})
	if err != nil {
		return Ticket{}, false, err
	}
	for _, candidate := range candidates {
		ticket, err := c.claim(ctx, candidate)
		if errors.Is(err, ErrAlreadyClaimed) {
			continue
		}
		if err != nil {
			return Ticket{}, false, err
		}
		return ticket, true, nil
	}
	return Ticket{}, false, nil
}

func (c *Client) claim(ctx context.Context, ticket Ticket) (Ticket, error) {
	if err := c.createClaim(ctx, ticket.Number); err != nil {
		return Ticket{}, err
	}
	if err := c.writeStatus(ctx, ticket, StatusDoing); err != nil {
		if releaseErr := c.deleteClaim(ctx, ticket.Number); releaseErr != nil {
			return Ticket{}, errors.Join(err, releaseErr)
		}
		return Ticket{}, err
	}
	ticket.Status = StatusDoing
	return ticket, nil
}

func (c *Client) createClaim(ctx context.Context, number int) error {
	meta, err := c.metadata(ctx)
	if err != nil {
		return err
	}
	err = c.postClaim(ctx, number, meta.HeadOID)
	if err == nil || isClaimConflict(err) {
		return claimError(err, number)
	}
	if !isUnprocessable(err) {
		return fmt.Errorf("claiming ticket #%d: %w", number, describeError(err))
	}
	// The cached commit can vanish after a history rewrite; refresh it and retry once.
	if meta, err = c.refreshMetadata(ctx); err != nil {
		return err
	}
	return claimError(c.postClaim(ctx, number, meta.HeadOID), number)
}

func (c *Client) postClaim(ctx context.Context, number int, oid string) error {
	body := strings.NewReader(fmt.Sprintf(`{"ref": "refs/%s%d", "sha": %q}`, claimRefPrefix, number, oid))
	return c.rest.DoWithContext(ctx, http.MethodPost, c.refsPath(), body, nil)
}

func claimError(err error, number int) error {
	if err == nil {
		return nil
	}
	if isClaimConflict(err) {
		return fmt.Errorf("%w: #%d", ErrAlreadyClaimed, number)
	}
	return fmt.Errorf("claiming ticket #%d: %w", number, describeError(err))
}

func (c *Client) refsPath() string {
	return fmt.Sprintf("repos/%s/git/refs", c.repo)
}

func isUnprocessable(err error) bool {
	var httpErr *api.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnprocessableEntity
}

func isClaimConflict(err error) bool {
	var httpErr *api.HTTPError
	return isUnprocessable(err) && errors.As(err, &httpErr) && strings.Contains(httpErr.Message, "already exists")
}

// ReleaseTicket gives up a claim and returns the ticket to Todo.
func (c *Client) ReleaseTicket(ctx context.Context, number int) error {
	ticket, err := c.readTicket(ctx, number)
	if err != nil {
		return err
	}
	if err := c.deleteClaim(ctx, number); err != nil {
		return err
	}
	return c.writeStatus(ctx, ticket, StatusTodo)
}

// deleteClaim removes the claim ref. A ref that is already gone needs no action.
func (c *Client) deleteClaim(ctx context.Context, number int) error {
	path := fmt.Sprintf("%s/%s%d", c.refsPath(), claimRefPrefix, number)
	err := c.rest.DoWithContext(ctx, http.MethodDelete, path, nil, nil)
	if err == nil || isUnprocessable(err) {
		return nil
	}
	return fmt.Errorf("releasing ticket #%d: %w", number, describeError(err))
}
