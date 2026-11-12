package tracker

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexgorbatchev/issue-tracker-cli/v2/internal/fakegithub"
	"github.com/cli/go-gh/v2/pkg/api"
)

const notFoundJSON = `{"data":{"repository":{"issue":null}},"errors":[{"type":"NOT_FOUND","message":"not found"}]}`

func statusOptions() map[string]string {
	return map[string]string{StatusTodo: "opt-todo", StatusDoing: "opt-doing", StatusDone: "opt-done"}
}

func testMetadata() *metadata {
	return &metadata{
		RepositoryID: "REPO", OwnerID: "OWNER", HeadOID: "headsha", IsPrivate: true,
		Projects: []Project{
			{Name: "go-tui", Repo: "o/go-tui", Visibility: VisibilityPrivate, LabelID: "L1", BoardID: "B1", BoardURL: "https://b/1", StatusFieldID: "F1", StatusOptions: statusOptions()},
			{Name: "lib", Repo: "o/lib", Visibility: VisibilityPublic, LabelID: "L2", BoardID: "B2", BoardURL: "https://b/2", StatusFieldID: "F2", StatusOptions: statusOptions()},
			{Name: "bare", Repo: "o/bare", Visibility: VisibilityPrivate, LabelID: "L3"},
		},
	}
}

func clientWith(t *testing.T, gh *fakegithub.GitHub, meta *metadata) *Client {
	t.Helper()
	c, err := New(Options{Repo: Repo{Owner: "o", Name: "tracker"}, CacheDir: t.TempDir(), Transport: gh, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if meta != nil {
		if err := c.saveMetadata(meta); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func newClient(t *testing.T, routes ...fakegithub.Route) (*Client, *fakegithub.GitHub) {
	t.Helper()
	gh := fakegithub.New(routes...)
	return clientWith(t, gh, testMetadata()), gh
}

type ticketSpec struct {
	number   int
	state    string
	project  string
	status   string
	blockers string
	parent   string
	created  string
	updated  string
	label    string
	subs     string
	// otherBoard puts the ticket on that board ahead of the one status implies.
	otherBoard string
	// component is the ticket's Component value on board B1.
	component string
	// board holds the item that status implies. Default: B1.
	board string
	// priorities adds a priority:<value> label for each value.
	priorities []string
	// handoff adds the handoff label.
	handoff bool
	// epic adds the epic label.
	epic bool
}

func (s ticketSpec) json() string {
	state, updated := s.state, s.updated
	if state == "" {
		state = "OPEN"
	}
	if updated == "" {
		updated = "2026-10-01T00:00:00Z"
	}
	created := s.created
	if created == "" {
		created = "2026-01-01T00:00:00Z"
	}
	labels := ""
	if s.project != "" {
		labels = fmt.Sprintf(`{"name":"repo:%s"},{"name":"bug"}`, s.project)
	}
	if s.label != "" {
		labels = fmt.Sprintf(`{"name":%q}`, s.label)
	}
	for _, priority := range s.priorities {
		labels = strings.TrimPrefix(labels+fmt.Sprintf(`,{"name":"priority:%s"}`, priority), ",")
	}
	if s.handoff {
		labels = strings.TrimPrefix(labels+`,{"name":"handoff"}`, ",")
	}
	if s.epic {
		labels = strings.TrimPrefix(labels+`,{"name":"epic"}`, ",")
	}
	board := s.board
	if board == "" {
		board = "B1"
	}
	items := ""
	if s.status != "" {
		component := "null"
		if s.component != "" {
			component = fmt.Sprintf(`{"name":%q}`, s.component)
		}
		items = fmt.Sprintf(`{"id":"ITEM%d","updatedAt":%q,"project":{"id":%q},"fieldValueByName":{"name":%q},"component":%s}`, s.number, updated, board, s.status, component)
	}
	if s.otherBoard != "" {
		items = strings.TrimSuffix(fmt.Sprintf(`{"id":"OTHER%d","updatedAt":%q,"project":{"id":%q},"fieldValueByName":{"name":"Elsewhere"}},%s`, s.number, updated, s.otherBoard, items), ",")
	}
	parent := "null"
	if s.parent != "" {
		parent = s.parent
	}
	return fmt.Sprintf(`{"id":"I%[1]d","number":%[1]d,"title":"Ticket %[1]d","state":%[2]q,"url":"https://t/%[1]d","createdAt":%[9]q,"updatedAt":%[3]q,
"labels":{"nodes":[%[4]s]},"blockedBy":{"nodes":[%[5]s]},"parent":%[6]s,"subIssueStates":{"nodes":[%[8]s]},"projectItems":{"nodes":[%[7]s]}`, s.number, state, updated, labels, s.blockers, parent, items, s.subs, created)
}

func ticketRoute(spec ticketSpec) fakegithub.Route {
	return fakegithub.Route{
		Match: "isPrivate issue(number: $number)",
		Body:  `{"data":{"repository":{"isPrivate":true,"issue":` + spec.json() + `}}}}`,
	}
}

func listRoute(specs ...ticketSpec) fakegithub.Route {
	nodes := make([]string, 0, len(specs))
	for _, spec := range specs {
		nodes = append(nodes, spec.json()+"}")
	}
	return fakegithub.Route{
		Match: "issues(first: 100",
		Body:  `{"data":{"repository":{"isPrivate":true,"issues":{"totalCount":` + fmt.Sprint(len(nodes)) + `,"nodes":[` + strings.Join(nodes, ",") + `],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
	}
}

// boardOnlyRoute answers the search for board tickets with no routing label.
func boardOnlyRoute(specs ...ticketSpec) fakegithub.Route {
	nodes := make([]string, 0, len(specs))
	for _, spec := range specs {
		nodes = append(nodes, spec.json()+"}")
	}
	return fakegithub.Route{
		Match: "search(query: $search",
		Body:  `{"data":{"repository":{"isPrivate":true},"search":{"nodes":[` + strings.Join(nodes, ",") + `],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}`,
		Times: 1,
	}
}

func okRoute(match string) fakegithub.Route {
	return fakegithub.Route{Match: match, Body: `{"data":{}}`}
}

func wantCount(t *testing.T, gh *fakegithub.GitHub, want int) {
	t.Helper()
	if got := gh.Count(); got != want {
		lines := make([]string, 0, got)
		for _, req := range gh.Requests() {
			lines = append(lines, strings.Join(strings.Fields(req.Description), " "))
		}
		t.Fatalf("made %d GitHub requests, want %d:\n%s", got, want, strings.Join(lines, "\n"))
	}
}

func wantError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
}

func lastDescription(gh *fakegithub.GitHub) string {
	requests := gh.Requests()
	return requests[len(requests)-1].Description
}

func graphQLError(errorType string) error {
	return &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: errorType, Message: "failed"}}}
}

func outsideRepository(t *testing.T) {
	t.Helper()
	dir, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Scratch may live inside the source checkout; do not discover its parents.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	t.Chdir(dir)
}

// here is the working directory's repository.
func here(t *testing.T) Repo {
	t.Helper()
	repo, err := CurrentRepo()
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

// bareRefresh answers a metadata refresh that lists project bare, with its board
// when board is true, and the visibility read that follows it.
func bareRefresh(board bool) []fakegithub.Route {
	body := strings.Replace(metadataJSON, `"labels":{"nodes":[`, `"labels":{"nodes":[{"id":"L3","name":"repo:bare","description":"o/bare"},`, 1)
	if board {
		body = strings.Replace(body, `"projectsV2":{"nodes":[`, `"projectsV2":{"nodes":[{"id":"B3","title":"bare","url":"https://b/3","field":{"id":"F3","options":[{"id":"T3","name":"Todo"},{"id":"G3","name":"Doing"},{"id":"D3","name":"Done"}]}},`, 1)
	}
	return []fakegithub.Route{
		{Match: "labels(first: 100, query", Body: body},
		visibilityRoute(`{"data":{"r0":{"isPrivate":true},"r1":{"isPrivate":true}}}`),
	}
}
