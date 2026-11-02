package tracker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

const (
	maxLimitRetries     = 2
	maxLimitWait        = 2 * time.Minute
	secondaryLimitWait  = time.Minute
	secondaryLimitText  = "secondary rate limit"
	graphQLLimitedType  = "RATE_LIMITED"
	graphQLNotFoundType = "NOT_FOUND"
	graphQLScopesType   = "INSUFFICIENT_SCOPES"
)

// BoardAccessError reports a GitHub login without the project scope. Its message
// names the fix; Detail holds GitHub's own explanation for agents.
type BoardAccessError struct {
	Detail string
}

func (e *BoardAccessError) Error() string {
	return "your GitHub login has no access to project boards: run issue-tracker auth refresh"
}

// ErrRateLimited reports that GitHub refused a request because of a rate limit.
var ErrRateLimited = errors.New("GitHub rate limit reached")

// limitTransport applies GitHub's documented responses to rate limiting.
type limitTransport struct {
	next  http.RoundTripper
	sleep func(context.Context, time.Duration) error
	now   func() time.Time
}

func (t *limitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := t.next.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		wait, limited, err := t.retryDelay(resp, attempt)
		if err != nil {
			return nil, err
		}
		if !limited || attempt == maxLimitRetries || wait > maxLimitWait {
			return resp, nil
		}
		if err := rewindBody(req); err != nil {
			return resp, nil
		}
		if err := discardBody(resp); err != nil {
			return nil, err
		}
		if err := t.sleep(req.Context(), wait); err != nil {
			return nil, err
		}
	}
}

// retryDelay follows GitHub's order: retry-after, then x-ratelimit-reset, then at
// least one minute with exponential backoff for a named secondary limit.
func (t *limitTransport) retryDelay(resp *http.Response, attempt int) (time.Duration, bool, error) {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return 0, false, nil
	}
	if seconds, err := strconv.Atoi(resp.Header.Get("retry-after")); err == nil {
		return time.Duration(seconds) * time.Second, true, nil
	}
	if resp.Header.Get("x-ratelimit-remaining") == "0" {
		return resetTime(resp.Header).Sub(t.now()), true, nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, false, fmt.Errorf("reading GitHub response: %w", err)
	}
	if err := resp.Body.Close(); err != nil {
		return 0, false, fmt.Errorf("closing GitHub response: %w", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if !strings.Contains(strings.ToLower(string(body)), secondaryLimitText) {
		return 0, false, nil
	}
	return secondaryLimitWait << attempt, true, nil
}

func resetTime(header http.Header) time.Time {
	seconds, err := strconv.ParseInt(header.Get("x-ratelimit-reset"), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

func rewindBody(req *http.Request) error {
	if req.GetBody == nil {
		if req.Body == nil || req.Body == http.NoBody {
			return nil
		}
		return errors.New("request body cannot be replayed")
	}
	body, err := req.GetBody()
	if err != nil {
		return fmt.Errorf("replaying request body: %w", err)
	}
	req.Body = body
	return nil
}

func discardBody(resp *http.Response) error {
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return fmt.Errorf("draining GitHub response: %w", err)
	}
	if err := resp.Body.Close(); err != nil {
		return fmt.Errorf("closing GitHub response: %w", err)
	}
	return nil
}

func sleepContext(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// describeError turns rate-limit failures into ErrRateLimited with a reset time,
// and missing scopes into BoardAccessError.
func describeError(err error) error {
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) && isLimitStatus(httpErr) {
		reset := resetTime(httpErr.Headers)
		if reset.IsZero() {
			return fmt.Errorf("%w: %s", ErrRateLimited, httpErr.Message)
		}
		return fmt.Errorf("%w: retry after %s", ErrRateLimited, reset.Format(time.RFC3339))
	}
	if hasGraphQLType(err, graphQLLimitedType) {
		return fmt.Errorf("%w: %v", ErrRateLimited, err)
	}
	if item, ok := findGraphQLError(err, graphQLScopesType); ok {
		// GitHub repeats a scope failure for every field in the query.
		return &BoardAccessError{Detail: item.Message}
	}
	return err
}

func isLimitStatus(err *api.HTTPError) bool {
	if err.StatusCode == http.StatusTooManyRequests {
		return true
	}
	return err.StatusCode == http.StatusForbidden && (err.Headers.Get("x-ratelimit-remaining") == "0" ||
		strings.Contains(strings.ToLower(err.Message), secondaryLimitText))
}

func hasGraphQLType(err error, errorType string) bool {
	_, ok := findGraphQLError(err, errorType)
	return ok
}

func findGraphQLError(err error, errorType string) (api.GraphQLErrorItem, bool) {
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) {
		return api.GraphQLErrorItem{}, false
	}
	index := slices.IndexFunc(gqlErr.Errors, func(item api.GraphQLErrorItem) bool {
		return item.Type == errorType
	})
	if index < 0 {
		return api.GraphQLErrorItem{}, false
	}
	return gqlErr.Errors[index], true
}
