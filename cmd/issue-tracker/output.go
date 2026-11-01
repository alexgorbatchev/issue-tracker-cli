package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	cobrahelptree "github.com/alexgorbatchev/cobra-help-tree/v3"
	"github.com/alexgorbatchev/issue-tracker-cli/internal/displaypath"
	"github.com/alexgorbatchev/issue-tracker-cli/internal/tracker"
	"github.com/olekukonko/tablewriter"
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

func warnPublic(w io.Writer, project string) error {
	prefix := "[WARN] "
	if cobrahelptree.IsAgentMode() {
		prefix = "WARN: "
	}
	_, err := fmt.Fprintf(w, "%s%s is public: keep ticket text, attachments, numbers, and the tracker name out of its commits, PRs, and branch names\n", prefix, project)
	return err
}

func table(w io.Writer, header []string, rows [][]string) error {
	t := tablewriter.NewTable(w, tablewriter.WithHeader(header))
	for _, row := range rows {
		if err := t.Append(row); err != nil {
			return fmt.Errorf("rendering table: %w", err)
		}
	}
	if err := t.Render(); err != nil {
		return fmt.Errorf("rendering table: %w", err)
	}
	return nil
}

func tsv(w io.Writer, rows [][]string) error {
	for _, row := range rows {
		if _, err := fmt.Fprintln(w, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	return nil
}

func printTickets(w io.Writer, tickets []tracker.Ticket, staleDays int, now time.Time) error {
	rows := make([][]string, 0, len(tickets))
	for _, ticket := range tickets {
		status := ticket.Status
		if ticket.Stale(staleDays, now) {
			status += " (stale)"
		}
		rows = append(rows, []string{strconv.Itoa(ticket.Number), status, ticket.Project, ticket.Title})
	}
	if cobrahelptree.IsAgentMode() {
		return tsv(w, rows)
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No tickets found.")
		return err
	}
	return table(w, []string{"Ticket", "Status", "Project", "Title"}, rows)
}

func printProjects(w io.Writer, projects []tracker.Project) error {
	rows := make([][]string, 0, len(projects))
	for _, project := range projects {
		rows = append(rows, []string{project.Name, project.Repo, visibility(project), project.BoardURL})
	}
	if cobrahelptree.IsAgentMode() {
		return tsv(w, rows)
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No projects registered.")
		return err
	}
	return table(w, []string{"Project", "Repository", "Visibility", "Board"}, rows)
}

func visibility(project tracker.Project) string {
	if project.Public {
		return "public"
	}
	return "private"
}

func printSummary(w io.Writer, summary tracker.ProjectSummary) error {
	project := summary.Project
	_, err := fmt.Fprintf(w, "project: %s\nrepository: %s\nvisibility: %s\nboard: %s\nopen: %d\nclosed: %d\n",
		project.Name, project.Repo, visibility(project), project.BoardURL, summary.Open, summary.Closed)
	return err
}

func joinNumbers(numbers []int) string {
	parts := make([]string, 0, len(numbers))
	for _, number := range numbers {
		parts = append(parts, strconv.Itoa(number))
	}
	return strings.Join(parts, ",")
}

func printDetail(w io.Writer, detail tracker.TicketDetail) error {
	if detail.PublicRepo {
		if err := warnPublic(w, detail.Project); err != nil {
			return err
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "number: %d\ntitle: %s\nstate: %s\nstatus: %s\nproject: %s\nurl: %s\n",
		detail.Number, detail.Title, detail.State, detail.Status, detail.Project, detail.URL)
	if detail.Parent != 0 {
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
