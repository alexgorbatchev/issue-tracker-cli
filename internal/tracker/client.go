// Package tracker manages projects and tickets held in one GitHub repository.
package tracker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/auth"
)

const defaultHost = "github.com"

// Repo identifies a GitHub repository.
type Repo struct {
	Owner string
	Name  string
}

// ParseRepo reads an owner/name repository reference.
func ParseRepo(value string) (Repo, error) {
	owner, name, ok := strings.Cut(value, "/")
	if !ok || owner == "" || name == "" || strings.ContainsAny(name, "/ ") || strings.ContainsAny(owner, " ") {
		return Repo{}, fmt.Errorf("invalid repository %q: expected owner/name", value)
	}
	return Repo{Owner: owner, Name: name}, nil
}

func (r Repo) String() string {
	return r.Owner + "/" + r.Name
}

// Options configures a Client. Transport and Token exist for tests; zero values
// use the live GitHub configuration.
type Options struct {
	Repo           Repo
	CacheDir       string
	RequirePrivate bool
	Transport      http.RoundTripper
	Token          string
}

// Client talks to the tracker repository.
type Client struct {
	repo           Repo
	gql            *api.GraphQLClient
	rest           *api.RESTClient
	http           *http.Client
	token          string
	cacheDir       string
	requirePrivate bool
	meta           *metadata
}

// New builds a Client. It leaves go-gh's response cache disabled because that
// cache can replay GraphQL mutations and stored 429 responses.
func New(opts Options) (*Client, error) {
	token := opts.Token
	if token == "" {
		token, _ = auth.TokenForHost(defaultHost)
	}
	if token == "" {
		return nil, errors.New("not logged in to GitHub: run gh auth login")
	}
	next := opts.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	transport := &limitTransport{next: next, sleep: sleepContext, now: time.Now}
	clientOptions := api.ClientOptions{Host: defaultHost, AuthToken: token, Transport: transport}
	gql, err := api.NewGraphQLClient(clientOptions)
	if err != nil {
		return nil, fmt.Errorf("creating GitHub client: %w", err)
	}
	rest, err := api.NewRESTClient(clientOptions)
	if err != nil {
		return nil, fmt.Errorf("creating GitHub client: %w", err)
	}
	return &Client{
		repo:           opts.Repo,
		gql:            gql,
		rest:           rest,
		http:           &http.Client{Transport: transport},
		token:          token,
		cacheDir:       opts.CacheDir,
		requirePrivate: opts.RequirePrivate,
	}, nil
}

// Repo returns the tracker repository.
func (c *Client) Repo() Repo {
	return c.repo
}

// checkPrivate enforces require_private using a value read from GitHub.
func (c *Client) checkPrivate(isPrivate bool) error {
	if c.requirePrivate && !isPrivate {
		return fmt.Errorf("tracker %s is not private: refusing to continue because require_private is on", c.repo)
	}
	return nil
}

func (c *Client) do(ctx context.Context, query string, variables map[string]any, response any) error {
	if err := c.gql.DoWithContext(ctx, query, variables, response); err != nil {
		return describeError(err)
	}
	return nil
}
