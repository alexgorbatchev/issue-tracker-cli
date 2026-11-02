package tracker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alexgorbatchev/issue-tracker-cli/internal/fakegithub"
	"github.com/cli/go-gh/v2/pkg/api"
)

func limited(sleeps *[]time.Duration, now time.Time, routes ...fakegithub.Route) (*limitTransport, *fakegithub.GitHub) {
	gh := fakegithub.New(routes...)
	return &limitTransport{
		next: gh,
		now:  func() time.Time { return now },
		sleep: func(_ context.Context, wait time.Duration) error {
			*sleeps = append(*sleeps, wait)
			return nil
		},
	}, gh
}

func post(t *testing.T, transport http.RoundTripper) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://api.github.com/repos/o/r/git/refs", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestLimitTransportRetries(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	secondary := `{"message":"You have exceeded a secondary rate limit"}`
	tests := []struct {
		name       string
		routes     []fakegithub.Route
		wantSleeps []time.Duration
		wantStatus int
		wantCalls  int
	}{
		{
			"retry-after is honored",
			[]fakegithub.Route{
				{Match: "refs", Status: 429, Headers: map[string]string{"retry-after": "7"}, Times: 1},
				{Match: "refs", Status: 200, Body: "{}"},
			},
			[]time.Duration{7 * time.Second}, 200, 2,
		},
		{
			"primary limit waits until a near reset",
			[]fakegithub.Route{
				{Match: "refs", Status: 403, Headers: map[string]string{"x-ratelimit-remaining": "0", "x-ratelimit-reset": strconv.FormatInt(now.Unix()+30, 10)}, Times: 1},
				{Match: "refs", Status: 200, Body: "{}"},
			},
			[]time.Duration{30 * time.Second}, 200, 2,
		},
		{
			"primary limit with a distant reset returns at once",
			[]fakegithub.Route{{Match: "refs", Status: 403, Headers: map[string]string{"x-ratelimit-remaining": "0", "x-ratelimit-reset": strconv.FormatInt(now.Unix()+3600, 10)}}},
			nil, 403, 1,
		},
		{
			"secondary limit backs off then stops",
			[]fakegithub.Route{{Match: "refs", Status: 403, Body: secondary}},
			[]time.Duration{time.Minute, 2 * time.Minute}, 403, 3,
		},
		{
			"other forbidden responses pass through with their body",
			[]fakegithub.Route{{Match: "refs", Status: 403, Body: `{"message":"Resource not accessible"}`}},
			nil, 403, 1,
		},
		{"success is untouched", []fakegithub.Route{{Match: "refs", Status: 200}}, nil, 200, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sleeps []time.Duration
			transport, gh := limited(&sleeps, now, tt.routes...)
			resp := post(t, transport)
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if fmt.Sprint(sleeps) != fmt.Sprint(tt.wantSleeps) {
				t.Fatalf("sleeps = %v, want %v", sleeps, tt.wantSleeps)
			}
			wantCount(t, gh, tt.wantCalls)
			if requests := gh.Requests(); requests[len(requests)-1].Body != `{"a":1}` {
				t.Fatalf("retry lost the request body: %q", requests[len(requests)-1].Body)
			}
		})
	}
}

func TestLimitTransportKeepsBodyOfUnrelatedForbidden(t *testing.T) {
	var sleeps []time.Duration
	transport, _ := limited(&sleeps, time.Now(), fakegithub.Route{Match: "refs", Status: 403, Body: "denied"})
	data, err := io.ReadAll(post(t, transport).Body)
	if err != nil || string(data) != "denied" {
		t.Fatalf("body = %q, err = %v", data, err)
	}
}

func TestSleepContextStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if err := sleepContext(context.Background(), time.Nanosecond); err != nil {
		t.Fatal(err)
	}
}

func TestSleepFailureStopsRetry(t *testing.T) {
	gh := fakegithub.New(fakegithub.Route{Match: "refs", Status: 429, Headers: map[string]string{"retry-after": "1"}})
	transport := &limitTransport{next: gh, now: time.Now, sleep: func(context.Context, time.Duration) error { return context.Canceled }}
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/refs", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestRewindBody(t *testing.T) {
	plain, err := http.NewRequest(http.MethodGet, "https://x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rewindBody(plain); err != nil {
		t.Fatal(err)
	}
	stream, err := http.NewRequest(http.MethodPost, "https://x", io.NopCloser(strings.NewReader("x")))
	if err != nil {
		t.Fatal(err)
	}
	wantError(t, rewindBody(stream), "cannot be replayed")
	failing, err := http.NewRequest(http.MethodPost, "https://x", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	failing.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("gone") }
	wantError(t, rewindBody(failing), "replaying")
}

func TestDescribeError(t *testing.T) {
	reset := http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"2000000000"}}
	plain := errors.New("boom")
	tests := []struct {
		name    string
		err     error
		limited bool
		want    string
	}{
		{"429", &api.HTTPError{StatusCode: 429, Message: "slow down", Headers: http.Header{}}, true, "slow down"},
		{"primary", &api.HTTPError{StatusCode: 403, Headers: reset}, true, "retry after"},
		{"secondary", &api.HTTPError{StatusCode: 403, Message: "a Secondary Rate Limit", Headers: http.Header{}}, true, "Secondary"},
		{"forbidden", &api.HTTPError{StatusCode: 403, Message: "no", Headers: http.Header{}}, false, "no"},
		{"graphql limited", &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: graphQLLimitedType, Message: "out"}}}, true, "out"},
		{"missing scope", &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: graphQLScopesType, Message: "scope"}}}, false, "issue-tracker auth refresh"},
		{"other", plain, false, "boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeError(tt.err)
			wantError(t, got, tt.want)
			if errors.Is(got, ErrRateLimited) != tt.limited {
				t.Fatalf("limited = %v, want %v", errors.Is(got, ErrRateLimited), tt.limited)
			}
		})
	}
}

func TestDescribeErrorReportsMissingScopeOnce(t *testing.T) {
	item := api.GraphQLErrorItem{Type: graphQLScopesType, Message: "needs read:project"}
	err := describeError(&api.GraphQLError{Errors: []api.GraphQLErrorItem{item, item, item}})
	var access *BoardAccessError
	if !errors.As(err, &access) || access.Detail != "needs read:project" || strings.Contains(err.Error(), access.Detail) {
		t.Fatalf("error = %#v", err)
	}
}
