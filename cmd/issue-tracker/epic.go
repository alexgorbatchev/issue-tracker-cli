package main

import (
	"fmt"
	"io"
	"strconv"

	cobrahelptree "github.com/alexgorbatchev/cobra-help-tree/v4"
	"github.com/alexgorbatchev/issue-tracker-cli/internal/tracker"
	"github.com/spf13/cobra"
)

func newEpicCommand(open opener) *cobra.Command {
	cmd := &cobra.Command{Use: "epic", Short: "Group tickets under epics, which agents never claim themselves"}
	cmd.AddCommand(
		newEpicCreateCommand(open), newEpicListCommand(open), newEpicInspectCommand(open),
		newEpicAddCommand(open), newEpicRmCommand(open), newEpicCloseCommand(open),
		newEpicCloseEligibleCommand(open), newEpicPromoteCommand(open), newEpicDemoteCommand(open),
	)
	return cmd
}

func newEpicCreateCommand(open opener) *cobra.Command {
	var opts tracker.EpicOptions
	var bodyFile string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an epic in one project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open(cmd)
			if err != nil {
				return err
			}
			if opts.Body, err = readBody(cmd, opts.Body, bodyFile); err != nil {
				return err
			}
			project, err := s.requireProject(cmd, opts.Project, "pass --project <name>")
			if err != nil {
				return err
			}
			opts.Project = project.Name
			epic, err := s.client.CreateEpic(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return report(cmd.OutOrStdout(), "created epic #%d in %s: %s", epic.Number, epic.Project, epic.URL)
		},
	}
	cmd.Flags().StringVar(&opts.Title, "title", "", "Epic title")
	if err := cmd.MarkFlagRequired("title"); err != nil {
		panic(err) // the flag is defined on the line above
	}
	addBodyFlags(cmd, &opts.Body, &bodyFile)
	cmd.Flags().StringVar(&opts.Project, "project", "", "Project for the epic; defaults to the current repository's project")
	cmd.Flags().IntSliceVar(&opts.Add, "add", nil, "Existing ticket that becomes one of the epic's tickets; repeatable")
	cmd.Flags().IntSliceVar(&opts.BlockedBy, "blocked-by", nil, "Tickets that must close first; repeatable")
	cmd.Flags().StringArrayVar(&opts.Labels, "label", nil, "Label to apply; repeatable")
	cmd.Flags().StringArrayVar(&opts.Attachments, "attach", nil, attachUsage)
	return cmd
}

func newEpicListCommand(open opener) *cobra.Command {
	var project, state string
	var allProjects bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List epics with their progress, oldest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open(cmd)
			if err != nil {
				return err
			}
			name, err := s.listScope(cmd, project, allProjects)
			if err != nil {
				return err
			}
			epics, _, err := s.client.ListTickets(cmd.Context(), tracker.ListOptions{Project: name, State: state, Epic: true})
			if err != nil {
				return err
			}
			return printEpics(cmd.OutOrStdout(), epics)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Limit to a project; defaults to the current repository's project")
	cmd.Flags().BoolVar(&allProjects, "all-projects", false, "List every project; needed outside a registered repository")
	cmd.Flags().StringVar(&state, "state", "open", "Epic state: open, closed, or all")
	return cmd
}

// progress shows how many of an epic's tickets are closed, as done/total.
func progress(done, total int) string {
	return strconv.Itoa(done) + "/" + strconv.Itoa(total)
}

func printEpics(w io.Writer, epics []tracker.Ticket) error {
	rows := make([][]string, 0, len(epics))
	for _, epic := range epics {
		rows = append(rows, []string{strconv.Itoa(epic.Number), epic.Project, progress(epic.SubIssues-epic.OpenSubIssues, epic.SubIssues), displayStatus(epic.Status), epic.Title})
	}
	if cobrahelptree.IsAgentMode() {
		return tsv(w, rows)
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "0 epics.")
		return err
	}
	return table(w, []string{"#", "Project", "Done", "Status", "Title"}, rows)
}

func newEpicInspectCommand(open opener) *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <epic>",
		Short: "Show an epic, its tickets, and how many are done",
		Args:  cobra.ExactArgs(1),
		RunE: withTicket(open, func(cmd *cobra.Command, s *session, number int) (string, error) {
			epic, err := s.client.InspectEpic(cmd.Context(), number)
			if err != nil {
				return "", err
			}
			// The ticket rows below replace the sub_issue lines.
			detail := epic.TicketDetail
			detail.SubIssues = nil
			w := cmd.OutOrStdout()
			if err := printDetails(w, []tracker.TicketDetail{detail}); err != nil {
				return "", err
			}
			if err := printEpicTickets(w, epic.Tickets); err != nil {
				return "", err
			}
			done := epic.Done()
			return fmt.Sprintf("#%d: %d of %d done, %d open", number, done, len(epic.Tickets), len(epic.Tickets)-done), nil
		}),
	}
}

func printEpicTickets(w io.Writer, tickets []tracker.Ticket) error {
	rows := make([][]string, 0, len(tickets))
	for _, ticket := range tickets {
		rows = append(rows, []string{strconv.Itoa(ticket.Number), ticket.State, displayStatus(ticket.Status), ticket.Project, ticket.Title})
	}
	if cobrahelptree.IsAgentMode() {
		return tsv(w, rows)
	}
	if len(rows) == 0 {
		// The progress line that follows already says 0 of 0.
		return nil
	}
	return table(w, []string{"#", "State", "Status", "Project", "Title"}, rows)
}

func newEpicAddCommand(open opener) *cobra.Command {
	var move bool
	cmd := &cobra.Command{
		Use:   "add <epic> <ticket...>",
		Short: "Make tickets the epic's tickets",
		Args:  cobra.MinimumNArgs(2),
		RunE: withTicket(open, func(cmd *cobra.Command, s *session, epic int) (string, error) {
			tickets, err := parseNumbers(cmd.Flags().Args()[1:])
			if err != nil {
				return "", err
			}
			added, err := s.client.AddToEpic(cmd.Context(), epic, tickets, move)
			return fmt.Sprintf("added %d ticket(s) to epic #%d", added, epic), err
		}),
	}
	cmd.Flags().BoolVar(&move, "move", false, "Take tickets that belong to another parent from it")
	return cmd
}

func newEpicRmCommand(open opener) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <epic> <ticket...>",
		Short: "Detach tickets from the epic",
		Args:  cobra.MinimumNArgs(2),
		RunE: withTicket(open, func(cmd *cobra.Command, s *session, epic int) (string, error) {
			tickets, err := parseNumbers(cmd.Flags().Args()[1:])
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("removed %d ticket(s) from epic #%d", len(tickets), epic), s.client.RemoveFromEpic(cmd.Context(), epic, tickets)
		}),
	}
}

func newEpicCloseCommand(open opener) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "close <epic>",
		Short: "Close an epic",
		Args:  cobra.ExactArgs(1),
		RunE: withTicket(open, func(cmd *cobra.Command, s *session, epic int) (string, error) {
			return fmt.Sprintf("closed epic #%d", epic), s.client.CloseEpic(cmd.Context(), epic, reason)
		}),
	}
	cmd.Flags().StringVar(&reason, "reason", "", "completed, which needs every ticket closed, or not-planned")
	if err := cmd.MarkFlagRequired("reason"); err != nil {
		panic(err) // the flag is defined on the line above
	}
	return cmd
}

func newEpicCloseEligibleCommand(open opener) *cobra.Command {
	var project string
	var allProjects, dryRun bool
	cmd := &cobra.Command{
		Use:   "close-eligible",
		Short: "Close epics whose tickets are all closed",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open(cmd)
			if err != nil {
				return err
			}
			name, err := s.listScope(cmd, project, allProjects)
			if err != nil {
				return err
			}
			var seen tracker.Tally
			epics, _, err := s.client.ListTickets(cmd.Context(), tracker.ListOptions{Project: name, Closable: true, Tally: &seen})
			if err != nil {
				return err
			}
			if len(epics) == 0 {
				return reportNone(cmd, s, "no eligible epics", name, seen)
			}
			for _, epic := range epics {
				if err := closeEligibleEpic(cmd, s, epic, dryRun); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Limit to a project; defaults to the current repository's project")
	cmd.Flags().BoolVar(&allProjects, "all-projects", false, "Cover every project; needed outside a registered repository")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "List the epics without closing them")
	return cmd
}

func closeEligibleEpic(cmd *cobra.Command, s *session, epic tracker.Ticket, dryRun bool) error {
	if dryRun {
		return report(cmd.OutOrStdout(), "would close #%d in %s: %s", epic.Number, epic.Project, epic.Title)
	}
	closed, err := s.client.CloseEligibleEpic(cmd.Context(), epic.Number)
	if err != nil {
		return err
	}
	if !closed {
		return report(cmd.OutOrStdout(), "skipped #%d: no longer eligible", epic.Number)
	}
	return report(cmd.OutOrStdout(), "closed #%d in %s: %s", epic.Number, epic.Project, epic.Title)
}

func newEpicPromoteCommand(open opener) *cobra.Command {
	return &cobra.Command{
		Use:   "promote <ticket>",
		Short: "Make a ticket an epic, keeping its sub-issues as the epic's tickets",
		Args:  cobra.ExactArgs(1),
		RunE: withTicket(open, func(cmd *cobra.Command, s *session, number int) (string, error) {
			return fmt.Sprintf("promoted #%d to an epic", number), s.client.PromoteEpic(cmd.Context(), number)
		}),
	}
}

func newEpicDemoteCommand(open opener) *cobra.Command {
	return &cobra.Command{
		Use:   "demote <epic>",
		Short: "Make an epic with no tickets an ordinary ticket",
		Args:  cobra.ExactArgs(1),
		RunE: withTicket(open, func(cmd *cobra.Command, s *session, number int) (string, error) {
			return fmt.Sprintf("demoted epic #%d", number), s.client.DemoteEpic(cmd.Context(), number)
		}),
	}
}
