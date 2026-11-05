package tracker

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/alexgorbatchev/issue-tracker-cli/internal/fakegithub"
)

func TestBoardDescription(t *testing.T) {
	url := "https://github.com/o/go-tui"
	long := strings.Repeat("é", 350)
	tests := []struct {
		name, description, want string
	}{
		{"description then link", "Terminal UI", "Terminal UI\n\n" + url},
		{"surrounding space", "  Terminal UI \n", "Terminal UI\n\n" + url},
		{"no description", "", url},
		{"blank description", "   ", url},
		{"exactly at the limit", strings.Repeat("a", 300-2-len(url)), strings.Repeat("a", 300-2-len(url)) + "\n\n" + url},
		{"cut to the limit", long, strings.Repeat("é", 300-3-len(url)) + "…\n\n" + url},
		{"cut drops trailing space", strings.Repeat("a", 300-5-len(url)) + "  bbbbbbbbbb", strings.Repeat("a", 300-5-len(url)) + "…\n\n" + url},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := boardDescription(tt.description, url)
			if got != tt.want {
				t.Fatalf("boardDescription = %q, want %q", got, tt.want)
			}
			if n := utf8.RuneCountInString(got); n > maxBoardDescription {
				t.Fatalf("description is %d characters, over GitHub's %d", n, maxBoardDescription)
			}
		})
	}
}

func descriptionsRoute(body string) fakegithub.Route {
	return fakegithub.Route{Match: "b0: node(id:", Body: body}
}

func TestSyncDescriptionsWritesOnlyChangedBoards(t *testing.T) {
	read := descriptionsRoute(`{"data":{
		"b0":{"shortDescription":""},"r0":{"description":"Terminal UI","url":"https://github.com/o/go-tui"},
		"b1":{"shortDescription":"Library\n\nhttps://github.com/o/lib"},"r1":{"description":"Library","url":"https://github.com/o/lib"}}}`)
	c, gh := newClient(t, read, okRoute("d0: updateProjectV2(input"))
	projects := testMetadata().Projects
	component := projects[0]
	component.Name, component.Path = "go-tui/docs", "docs"
	projects = append(projects, component)
	result, err := c.SyncDescriptions(ctx, projects)
	if err != nil {
		t.Fatal(err)
	}
	if result.Boards != 2 || strings.Join(result.Updated, ",") != "go-tui" {
		t.Fatalf("result = %+v, want the component's shared board read once and only go-tui updated", result)
	}
	if len(result.Problems) != 1 || result.Problems[0] != "project bare has no board: run project add o/bare" {
		t.Fatalf("problems = %q", result.Problems)
	}
	wantCount(t, gh, 2)
	write := gh.Requests()[1]
	if write.Description != `mutation($d0: String!) { d0: updateProjectV2(input: {projectId: "B1", shortDescription: $d0}) { clientMutationId } }` ||
		write.Variables["d0"] != "Terminal UI\n\nhttps://github.com/o/go-tui" {
		t.Fatalf("write = %s %v", write.Description, write.Variables)
	}
}

func TestSyncDescriptionsSkipsTheWriteWhenCurrent(t *testing.T) {
	read := descriptionsRoute(`{"data":{"b0":{"shortDescription":"https://github.com/o/lib"},"r0":{"description":null,"url":"https://github.com/o/lib"}}}`)
	c, gh := newClient(t, read)
	result, err := c.SyncDescriptions(ctx, testMetadata().Projects[1:2])
	if err != nil || len(result.Updated) != 0 || result.Boards != 1 {
		t.Fatalf("result = %+v, %v", result, err)
	}
	wantCount(t, gh, 1)
}

func TestSyncDescriptionsReportsMissingRepositoriesAndBoards(t *testing.T) {
	read := descriptionsRoute(`{"data":{
		"b0":{"shortDescription":""},"r0":null,
		"b1":null,"r1":{"description":"Library","url":"https://github.com/o/lib"}},
		"errors":[{"type":"NOT_FOUND","message":"not found"}]}`)
	c, gh := newClient(t, read)
	result, err := c.SyncDescriptions(ctx, testMetadata().Projects[:2])
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"repository o/go-tui of project go-tui not found", "project lib has no board: run project add o/lib"}
	if strings.Join(result.Problems, "|") != strings.Join(want, "|") || len(result.Updated) != 0 {
		t.Fatalf("result = %+v", result)
	}
	wantCount(t, gh, 1)
}

func TestSyncDescriptionsBatchesFiftyBoards(t *testing.T) {
	var projects []Project
	first, second := []string{}, []string{}
	for i := range 60 {
		name := fmt.Sprintf("p%d", i)
		projects = append(projects, Project{Name: name, Repo: "o/" + name, BoardID: "B" + name})
		node := fmt.Sprintf(`"b%[1]d":{"shortDescription":""},"r%[1]d":{"description":"","url":"https://github.com/o/%[2]s"}`, i%descriptionBatch, name)
		if i < descriptionBatch {
			first = append(first, node)
		} else {
			second = append(second, node)
		}
	}
	gh := fakegithub.New(
		fakegithub.Route{Match: `b0: node(id: "Bp0")`, Body: `{"data":{` + strings.Join(first, ",") + `}}`},
		fakegithub.Route{Match: `b0: node(id: "Bp50")`, Body: `{"data":{` + strings.Join(second, ",") + `}}`},
		okRoute("updateProjectV2(input"),
	)
	result, err := clientWith(t, gh, testMetadata()).SyncDescriptions(ctx, projects)
	if err != nil || len(result.Updated) != 60 || result.Boards != 60 {
		t.Fatalf("result = %+v, %v", result, err)
	}
	wantCount(t, gh, 4)
	requests := gh.Requests()
	if strings.Count(requests[2].Description, "updateProjectV2(") != 50 || strings.Count(requests[3].Description, "updateProjectV2(") != 10 {
		t.Fatal("writes must hold 50 boards each")
	}
	if requests[3].Variables["d9"] != "https://github.com/o/p59" {
		t.Fatalf("last write = %v", requests[3].Variables)
	}
}

func TestSyncDescriptionsFailures(t *testing.T) {
	read := descriptionsRoute(`{"data":{"b0":{"shortDescription":""},"r0":{"description":"x","url":"https://github.com/o/lib"}}}`)
	tests := []struct {
		name     string
		routes   []fakegithub.Route
		projects []Project
		want     string
	}{
		{"read", []fakegithub.Route{{Match: "b0: node(id:", Status: 500, Body: "{}"}}, testMetadata().Projects[1:2], "reading board descriptions"},
		{"write", []fakegithub.Route{read, {Match: "d0: updateProjectV2(input", Status: 500, Body: "{}"}}, testMetadata().Projects[1:2], "writing board descriptions"},
		{"bad repository", nil, []Project{{Name: "x", Repo: "nonsense", BoardID: "B"}}, "invalid repository"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newClient(t, tt.routes...)
			_, err := c.SyncDescriptions(ctx, tt.projects)
			wantError(t, err, tt.want)
		})
	}
}
