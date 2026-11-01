package fakegithub

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func send(t *testing.T, g *GitHub, method, url, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := g.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(data)
}

func TestRoutesMatchInOrderAndRespectTimes(t *testing.T) {
	g := New(Route{Match: "viewer", Body: "first", Times: 2}, Route{Match: "viewer", Body: "second", Headers: map[string]string{"x-test": "1"}})
	for _, want := range []string{"first", "first", "second"} {
		_, body := send(t, g, http.MethodPost, "https://api.github.com/graphql", `{"query":"query { viewer }"}`)
		if body != want {
			t.Fatalf("body = %q, want %q", body, want)
		}
	}
	if g.Count() != 3 {
		t.Fatalf("count = %d", g.Count())
	}
}

func TestRESTDescriptionAndUnmatchedRoute(t *testing.T) {
	g := New()
	g.Add(Route{Match: "DELETE api.github.com/repos/o/r", Status: http.StatusNoContent})
	if status, _ := send(t, g, http.MethodDelete, "https://api.github.com/repos/o/r", ""); status != http.StatusNoContent {
		t.Fatalf("status = %d", status)
	}
	if status, body := send(t, g, http.MethodGet, "https://github.com/missing", ""); status != http.StatusTeapot || !strings.Contains(body, "GET github.com/missing") {
		t.Fatalf("status = %d body = %s", status, body)
	}
}
