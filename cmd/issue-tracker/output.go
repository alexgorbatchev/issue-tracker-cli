package main

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	lgtable "charm.land/lipgloss/v2/table"
	cobrahelptree "github.com/alexgorbatchev/cobra-help-tree/v4"
	"github.com/alexgorbatchev/issue-tracker-cli/v2/internal/displaypath"
	"github.com/alexgorbatchev/issue-tracker-cli/v2/internal/tracker"
)

// report prints a one-line result: OK: for agents, [OK] for people.
func report(w io.Writer, format string, args ...any) error {
	prefix := "[OK] "
	if cobrahelptree.IsAgentMode() {
		prefix = "OK: "
	}
	_, err := fmt.Fprintf(w, prefix+format+"\n", args...)
	return err
}

func warn(w io.Writer, format string, args ...any) error {
	prefix := "[WARN] "
	if cobrahelptree.IsAgentMode() {
		prefix = "WARN: "
	}
	_, err := fmt.Fprintf(w, prefix+format+"\n", args...)
	return err
}

func warnPublic(w io.Writer, project string) error {
	return warn(w, "%s is public: keep ticket text, attachments, numbers, and the tracker name out of its commits, PRs, and branch names", project)
}

// cellStyle pads cells so that wrapped text stays clear of the borders.
var cellStyle = lipgloss.NewStyle().Padding(0, 1)

// table renders rows for people. When the table is wider than the terminal, its
// columns shrink and their text wraps so that every line fits.
func table(w io.Writer, header []string, rows [][]string) error {
	t := lgtable.New().
		Border(lipgloss.NormalBorder()).
		Headers(header...).
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style { return cellStyle })
	if width := cobrahelptree.GetTerminalWidth(); width > 0 && lipgloss.Width(t.String()) > width {
		t.Width(width)
	}
	_, err := fmt.Fprintln(w, t.String())
	return err
}

// displayStatus shows a board status in upper case for people. Agents get the
// board's own name, which is what --status and the skill use.
func displayStatus(status string) string {
	if cobrahelptree.IsAgentMode() {
		return status
	}
	return strings.ToUpper(status)
}

func tsv(w io.Writer, rows [][]string) error {
	for _, row := range rows {
		if _, err := fmt.Fprintln(w, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	return nil
}

// printTickets prints a list scoped to project, or to every project when it is
// empty. A table leaves out the Project column when every ticket belongs to the
// scoped project, which the reader already knows.
func printTickets(w io.Writer, tickets []tracker.Ticket, project string, staleDays int, now time.Time) error {
	// A table wraps labels at the spaces after their commas.
	separator := ", "
	if cobrahelptree.IsAgentMode() {
		separator = ","
	}
	rows := make([][]string, 0, len(tickets))
	for _, ticket := range tickets {
		status := displayStatus(ticket.Status)
		if ticket.Stale(staleDays, now) {
			status += " (stale)"
		}
		rows = append(rows, []string{strconv.Itoa(ticket.Number), status, ticket.Priority(), ticket.Project, strings.Join(ticket.OtherLabels(), separator), ticket.Title})
	}
	if cobrahelptree.IsAgentMode() {
		return tsv(w, rows)
	}
	if project == "" || slices.ContainsFunc(tickets, func(ticket tracker.Ticket) bool { return ticket.Project != project }) {
		return table(w, []string{"#", "Status", "Priority", "Project", "Labels", "Title"}, rows)
	}
	for i, row := range rows {
		rows[i] = slices.Delete(row, 3, 4)
	}
	return table(w, []string{"#", "Status", "Priority", "Labels", "Title"}, rows)
}

// warnPriorityConflicts names each ticket with more than one priority label, which
// ranks by its most urgent one until someone removes the others.
func warnPriorityConflicts(w io.Writer, tickets []tracker.Ticket) error {
	for _, ticket := range tickets {
		if conflicts := ticket.PriorityConflicts(); conflicts != nil {
			if err := warn(w, "#%d has priorities %s: ranked as %s", ticket.Number, strings.Join(conflicts, ", "), conflicts[0]); err != nil {
				return err
			}
		}
	}
	return nil
}

// statusCounts describes a scope's tickets for a person who got an empty result:
// its open tickets by board status, then its closed ones.
func statusCounts(scope string, open map[string]int, closed int) string {
	if scope == "" {
		scope = "All projects"
	}
	parts := []string{}
	unset := 0
	for _, n := range open {
		unset += n
	}
	counts := open
	for _, status := range []string{tracker.StatusTodo, tracker.StatusDoing, tracker.StatusDone} {
		parts = append(parts, fmt.Sprintf("%d %s", counts[status], displayStatus(status)))
		unset -= counts[status]
	}
	if unset > 0 {
		parts = append(parts, fmt.Sprintf("%d without a status", unset))
	}
	return fmt.Sprintf("%s: %s; %d closed", scope, strings.Join(parts, ", "), closed)
}

func printProjects(w io.Writer, projects []tracker.Project) error {
	rows := make([][]string, 0, len(projects))
	for _, project := range projects {
		rows = append(rows, []string{project.Name, project.Repo, visibility(project), project.BoardURL, projectPath(project)})
	}
	if cobrahelptree.IsAgentMode() {
		return tsv(w, rows)
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "0 projects registered.")
		return err
	}
	return table(w, []string{"Project", "Repository", "Visibility", "Board", "Path"}, rows)
}

// projectPath shows a component's directory, and . for a whole repository.
func projectPath(project tracker.Project) string {
	if project.Path == "" {
		return "."
	}
	return project.Path
}

func visibility(project tracker.Project) string {
	if project.Public {
		return "public"
	}
	return "private"
}

func printSummary(w io.Writer, summary tracker.ProjectSummary) error {
	project := summary.Project
	_, err := fmt.Fprintf(w, "project: %s\nrepository: %s\npath: %s\nvisibility: %s\nboard: %s\nopen: %d\nclosed: %d\n",
		project.Name, project.Repo, projectPath(project), visibility(project), project.BoardURL, summary.Open, summary.Closed)
	return err
}

func joinNumbers(numbers []int) string {
	parts := make([]string, 0, len(numbers))
	for _, number := range numbers {
		parts = append(parts, strconv.Itoa(number))
	}
	return strings.Join(parts, ",")
}

// printDetails warns once for each public project, then prints the tickets
// separated by blank lines.
func printDetails(w io.Writer, details []tracker.TicketDetail) error {
	warned := map[string]bool{}
	for _, detail := range details {
		if detail.PublicRepo && !warned[detail.Project] {
			warned[detail.Project] = true
			if err := warnPublic(w, detail.Project); err != nil {
				return err
			}
		}
	}
	tickets := make([]tracker.Ticket, 0, len(details))
	for _, detail := range details {
		tickets = append(tickets, detail.Ticket)
	}
	if err := warnPriorityConflicts(w, tickets); err != nil {
		return err
	}
	for i, detail := range details {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if err := printDetail(w, detail); err != nil {
			return err
		}
	}
	return nil
}

func printDetail(w io.Writer, detail tracker.TicketDetail) error {
	var b strings.Builder
	fmt.Fprintf(&b, "number: %d\ntitle: %s\nstate: %s\nstatus: %s\nproject: %s\nurl: %s\n",
		detail.Number, detail.Title, detail.State, displayStatus(detail.Status), detail.Project, detail.URL)
	if priority := detail.Priority(); priority != "" {
		fmt.Fprintf(&b, "priority: %s\n", priority)
	}
	if labels := detail.OtherLabels(); len(labels) > 0 {
		fmt.Fprintf(&b, "labels: %s\n", strings.Join(labels, ","))
	}
	if detail.ParentEpic {
		fmt.Fprintf(&b, "epic: %d\n", detail.Parent)
	} else if detail.Parent != 0 {
		fmt.Fprintf(&b, "parent: %d\n", detail.Parent)
	}
	if len(detail.Blockers) > 0 {
		fmt.Fprintf(&b, "blocked_by: %s\n", joinNumbers(detail.Blockers))
	}
	if len(detail.Blocking) > 0 {
		fmt.Fprintf(&b, "blocking: %s\n", joinNumbers(detail.Blocking))
	}
	for _, sub := range detail.SubIssues {
		fmt.Fprintf(&b, "sub_issue: %d %s %s\n", sub.Number, sub.State, sub.Title)
	}
	for _, url := range detail.Attachments {
		fmt.Fprintf(&b, "attachment: %s\n", url)
	}
	fmt.Fprintf(&b, "body:\n%s\n", detail.Body)
	for _, comment := range detail.Comments {
		fmt.Fprintf(&b, "comment: %s %s\n%s\n", comment.Author, comment.CreatedAt.Format(time.RFC3339), comment.Body)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func printPaths(w io.Writer, paths []string) error {
	for _, path := range paths {
		if _, err := fmt.Fprintln(w, displaypath.Fold(path)); err != nil {
			return err
		}
	}
	return nil
}

func printScans(w io.Writer, scans []tracker.RepoScan) error {
	rows := make([][]string, 0, len(scans))
	for _, scan := range scans {
		visibility := "public"
		if scan.Private {
			visibility = "private"
		}
		rows = append(rows, []string{scan.Repo, visibility, strconv.Itoa(scan.Open), strconv.Itoa(scan.Movable), strconv.Itoa(scan.Others)})
	}
	if cobrahelptree.IsAgentMode() {
		return tsv(w, rows)
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "0 repositories with open issues.")
		return err
	}
	return table(w, []string{"Repository", "Visibility", "Open", "Can move", "From others"}, rows)
}
