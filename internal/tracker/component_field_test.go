package tracker

import (
	"strings"
	"testing"

	"github.com/alexgorbatchev/issue-tracker-cli/internal/fakegithub"
)

// fieldMetadata is monoMetadata with a Component field holding web and api, but not
// admin.
func fieldMetadata() *metadata {
	meta := monoMetadata()
	meta.setComponentField("mono", "FC", map[string]string{"web": "OW", "api": "OS"})
	return meta
}

func fieldClient(t *testing.T, routes ...fakegithub.Route) (*Client, *fakegithub.GitHub) {
	t.Helper()
	gh := fakegithub.New(routes...)
	return clientWith(t, gh, fieldMetadata()), gh
}

func TestRefreshReadsTheComponentField(t *testing.T) {
	gh := fakegithub.New(withField(componentAdded(), `{"id":"O-docs","name":"docs"}`), visibilityRoute(`{"data":{"r0":{"isPrivate":true},"r1":{"isPrivate":true}}}`))
	projects, err := clientWith(t, gh, nil).Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range projects {
		if project.ComponentFieldID != "FC" || project.ComponentOptions["docs"] != "O-docs" {
			t.Fatalf("every project on the board shares its Component field: %+v", project)
		}
	}
}

func TestABoardTicketRoutesByItsComponentValue(t *testing.T) {
	tests := []struct{ component, want string }{
		{"api", "mono/api"},
		{"ghost", "mono"},
		{"", "mono"},
	}
	for _, tt := range tests {
		c, _ := fieldClient(t, ticketRoute(ticketSpec{number: 1, status: "Todo", component: tt.component, otherBoard: "ELSEWHERE"}))
		ticket, err := c.readTicket(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		if ticket.Project != tt.want || !ticket.boardRouted {
			t.Errorf("component %q: project = %q", tt.component, ticket.Project)
		}
	}

	c, _ := fieldClient(t, ticketRoute(ticketSpec{number: 1, project: "mono", status: "Todo", component: "api"}))
	if ticket, err := c.readTicket(ctx, 1); err != nil || ticket.Project != "mono" {
		t.Fatalf("a label wins over the Component value: %+v, %v", ticket, err)
	}
}

func TestARepositoryListShowsEachBoardTicketsComponent(t *testing.T) {
	onBoard := boardOnlyRoute(ticketSpec{number: 2, status: "Todo", component: "web"})
	c, _ := fieldClient(t, onBoard, monoListRoute(monoRegistrations))
	tickets, _, err := c.ListTickets(ctx, ListOptions{Project: "mono"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tickets) != 1 || tickets[0].Project != "mono/web" {
		t.Fatalf("tickets = %+v", tickets)
	}
}

func TestMovingABoardTicketChangesOnlyItsComponent(t *testing.T) {
	c, gh := fieldClient(t, ticketRoute(ticketSpec{number: 1, status: "Doing"}), okRoute("updateProjectV2ItemFieldValue"))
	if err := c.MoveTicket(ctx, 1, "mono/api"); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 2)
	write := lastDescription(gh)
	if !strings.Contains(write, `itemId: "ITEM1", fieldId: "FC", value: {singleSelectOptionId: "OS"}`) || strings.Contains(write, "Labels") {
		t.Fatalf("a board ticket needs no label to move: %s", write)
	}

	c, gh = fieldClient(t, ticketRoute(ticketSpec{number: 1, status: "Todo", component: "api"}), okRoute("clearProjectV2ItemFieldValue"))
	if err := c.MoveTicket(ctx, 1, "mono"); err != nil {
		t.Fatal(err)
	}
	if write := lastDescription(gh); !strings.Contains(write, `clearProjectV2ItemFieldValue(input: {projectId: "B1", itemId: "ITEM1", fieldId: "FC"})`) || strings.Contains(write, "Labels") {
		t.Fatalf("moving back to the repository clears the value: %s", write)
	}
}

func TestMovingALabelledTicketKeepsTheFieldInStep(t *testing.T) {
	c, gh := fieldClient(t, ticketRoute(ticketSpec{number: 1, project: "mono/api", status: "Todo"}), okRoute("addLabelsToLabelable"))
	if err := c.MoveTicket(ctx, 1, "mono"); err != nil {
		t.Fatal(err)
	}
	write := lastDescription(gh)
	for _, want := range []string{`labelIds: ["LM"]`, `removeLabelsFromLabelable(input: {labelableId: "I1", labelIds: ["LS"]})`, "clearProjectV2ItemFieldValue"} {
		if !strings.Contains(write, want) {
			t.Fatalf("write = %s, missing %s", write, want)
		}
	}

	c, gh = fieldClient(t, ticketRoute(ticketSpec{number: 1, status: "Todo"}), okRoute("addLabelsToLabelable"))
	if err := c.MoveTicket(ctx, 1, "mono/admin"); err != nil {
		t.Fatal(err)
	}
	if write := lastDescription(gh); !strings.Contains(write, `labelIds: ["LA"]`) || strings.Contains(write, "ProjectV2ItemFieldValue") {
		t.Fatalf("a component with no option is reached by its label: %s", write)
	}
}

func TestMovingOntoTheBoardSetsTheComponent(t *testing.T) {
	add := fakegithub.Route{Match: "addProjectV2ItemById", Body: `{"data":{"addProjectV2ItemById":{"item":{"id":"NEW"}}}}`}
	onLib := ticketRoute(ticketSpec{number: 1, project: "lib", status: "Todo"})
	onLib.Body = strings.Replace(onLib.Body, `"project":{"id":"B1"}`, `"project":{"id":"B2"}`, 1)
	c, gh := fieldClient(t, onLib, okRoute("addLabelsToLabelable"), add, okRoute("updateProjectV2ItemFieldValue"))
	if err := c.MoveTicket(ctx, 1, "mono/api"); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 4)
	write := lastDescription(gh)
	if !strings.Contains(write, `itemId: "NEW", fieldId: "FC", value: {singleSelectOptionId: "OS"}`) || !strings.Contains(write, `fieldId: "F1"`) {
		t.Fatalf("the new item gets its Component with its status: %s", write)
	}
}

func TestCreatingAComponentTicketSetsItsComponent(t *testing.T) {
	c, gh := fieldClient(t, createRoute(), addToBoardRoute(), okRoute("updateProjectV2ItemFieldValue"))
	if _, err := c.CreateTicket(ctx, CreateOptions{Project: "mono/api", Title: "t"}); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 3)
	if write := lastDescription(gh); !strings.Contains(write, `value: {singleSelectOptionId: "OS"}`) {
		t.Fatalf("write = %s", write)
	}

	c, gh = fieldClient(t, createRoute(), addToBoardRoute(), okRoute("updateProjectV2ItemFieldValue"))
	if _, err := c.CreateTicket(ctx, CreateOptions{Project: "mono", Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if write := lastDescription(gh); strings.Contains(write, "FC") {
		t.Fatalf("a repository ticket needs no Component value: %s", write)
	}
}

// optionsRoute answers the read of the Component field's options.
func optionsRoute(options string) fakegithub.Route {
	return fakegithub.Route{Match: "node(id: $id)", Body: `{"data":{"node":{"options":[` + options + `]}}}`}
}

func TestRemovingAComponentRemovesItsOption(t *testing.T) {
	options := optionsRoute(`{"id":"OW","name":"web","color":"BLUE","description":"apps/web"},{"id":"OS","name":"api","color":"GRAY","description":"services/api"}`)
	update := fakegithub.Route{Match: "updateProjectV2Field(input", Body: `{"data":{"updateProjectV2Field":{"projectV2Field":{"id":"FC","options":[{"id":"OW","name":"web"}]}}}}`}
	c, gh := fieldClient(t, boardOnlyRoute(), summaryRoute(0), options, update, okRoute("updateLabel"), componentAdded(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	if err := c.RemoveProject(ctx, "mono/api"); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 7)
	requests := gh.Requests()
	kept := requests[3].Variables["options"].([]any)
	if len(kept) != 1 || kept[0].(map[string]any)["id"] != "OW" || kept[0].(map[string]any)["color"] != "BLUE" {
		t.Fatalf("the other options must stay whole: %v", kept)
	}
	if !strings.Contains(requests[4].Description, "updateLabel") {
		t.Fatalf("the label is cleared after the option: %s", requests[4].Description)
	}

	last := optionsRoute(`{"id":"OS","name":"api","color":"GRAY","description":"services/api"}`)
	c, gh = fieldClient(t, boardOnlyRoute(), summaryRoute(0), last, okRoute("deleteProjectV2Field"), okRoute("updateLabel"), componentAdded(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	if err := c.RemoveProject(ctx, "mono/api"); err != nil {
		t.Fatal(err)
	}
	if field := gh.Requests()[3]; !strings.Contains(field.Description, "deleteProjectV2Field") || field.Variables["field"] != "FC" {
		t.Fatalf("the last option takes the field with it: %s", field.Description)
	}

	c, gh = fieldClient(t, boardOnlyRoute(), summaryRoute(0), fakegithub.Route{Match: "node(id: $id)", Status: 500, Body: "{}"})
	wantError(t, c.RemoveProject(ctx, "mono/api"), "reading Component options")
	for _, req := range gh.Requests() {
		if strings.Contains(req.Description, "updateLabel") {
			t.Fatal("a failed option removal must leave the component registered")
		}
	}

	c, _ = fieldClient(t, boardOnlyRoute(), summaryRoute(0), last, fakegithub.Route{Match: "deleteProjectV2Field", Status: 500, Body: "{}"})
	wantError(t, c.RemoveProject(ctx, "mono/api"), "deleting Component field")

	c, gh = fieldClient(t, boardOnlyRoute(), summaryRoute(0), okRoute("updateLabel"), componentAdded(), visibilityRoute(`{"data":{"r0":{"isPrivate":true}}}`))
	if err := c.RemoveProject(ctx, "mono/admin"); err != nil {
		t.Fatal(err)
	}
	wantCount(t, gh, 5)
}
