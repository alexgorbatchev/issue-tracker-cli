// Package fakegithub is an in-process GitHub for tests. It answers requests by
// substring match and records them, so tests can assert API call budgets.
package fakegithub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Route answers requests whose description contains Match. The description is the
// GraphQL query for GraphQL calls, and "METHOD host/path" for everything else.
type Route struct {
	Match   string
	Status  int
	Body    string
	Headers map[string]string
	// Times limits how often the route answers; zero means without limit.
	Times int
}

// Request is one recorded call.
type Request struct {
	Description string
	Variables   map[string]any
	Header      http.Header
	Body        string
}

// GitHub implements http.RoundTripper.
type GitHub struct {
	mu       sync.Mutex
	routes   []Route
	requests []Request
}

// New returns a GitHub answering with routes, checked in order.
func New(routes ...Route) *GitHub {
	return &GitHub{routes: routes}
}

// Add appends routes.
func (g *GitHub) Add(routes ...Route) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.routes = append(g.routes, routes...)
}

// Requests returns every recorded call.
func (g *GitHub) Requests() []Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Request(nil), g.requests...)
}

// Count returns how many calls were made.
func (g *GitHub) Count() int {
	return len(g.Requests())
}

// RoundTrip records the request and returns the first matching route's response.
func (g *GitHub) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		if body, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
	}
	record := describe(req, body)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = append(g.requests, record)
	for i := range g.routes {
		route := &g.routes[i]
		if !strings.Contains(record.Description, route.Match) {
			continue
		}
		if route.Times < 0 {
			continue
		}
		if route.Times > 0 {
			if route.Times == 1 {
				route.Times = -1
			} else {
				route.Times--
			}
		}
		return respond(req, *route), nil
	}
	return respond(req, Route{Status: http.StatusTeapot, Body: `{"message":"no route for ` + strings.ReplaceAll(record.Description, `"`, `'`) + `"}`}), nil
}

func describe(req *http.Request, body []byte) Request {
	record := Request{Header: req.Header.Clone(), Body: string(body)}
	var graphql struct {
		Query     string
		Variables map[string]any
	}
	if strings.HasSuffix(req.URL.Path, "/graphql") && json.Unmarshal(body, &graphql) == nil {
		record.Description, record.Variables = graphql.Query, graphql.Variables
		return record
	}
	record.Description = req.Method + " " + req.URL.Host + req.URL.Path
	return record
}

func respond(req *http.Request, route Route) *http.Response {
	status := route.Status
	if status == 0 {
		status = http.StatusOK
	}
	header := http.Header{"Content-Type": {"application/json"}}
	for key, value := range route.Headers {
		header.Set(key, value)
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     header,
		Body:       io.NopCloser(bytes.NewBufferString(route.Body)),
		Request:    req,
	}
}
