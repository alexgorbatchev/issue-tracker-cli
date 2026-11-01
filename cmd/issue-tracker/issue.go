package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/alexgorbatchev/issue-tracker-cli/internal/tracker"
	"github.com/spf13/cobra"
)

func newIssueCommand(open opener) *cobra.Command {
	cmd := &cobra.Command{Use: "issue", Short: "Manage tickets"}
	cmd.AddCommand(
		newIssueClaimCommand(open), newIssueCloseCommand(open), newIssueCreateCommand(open),
		newIssueEditCommand(open), newIssueInspectCommand(open), newIssueListCommand(open),
		newIssueMoveCommand(open), newIssueReleaseCommand(open), newIssueReopenCommand(open),
		newAttachmentCommand(open), newBlockerCommand(open), newCommentCommand(open),
		newLabelCommand(open), newStatusCommand(open), newSubCommand(open),
	)
	return cmd
}

func parseNumber(value string) (int, error) {
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 {
		return 0, fmt.Errorf("invalid ticket number %q", value)
	}
	return number, nil
}

func parseNumbers(values []string) ([]int, error) {
	numbers := make([]int, 0, len(values))
	for _, value := range values {
		number, err := parseNumber(value)
		if err != nil {
			return nil, err
		}
		numbers = append(numbers, number)
	}
	return numbers, nil
}

// ticketAction is a command that acts on one ticket and reports the result.
type ticketAction func(cmd *cobra.Command, s *session, number int) (string, error)

func withTicket(open opener, action ticketAction) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		number, err := parseNumber(args[0])
		if err != nil {
			return err
		}
		s, err := open()
		if err != nil {
			return err
		}
		message, err := action(cmd, s, number)
		if err != nil {
			return err
		}
		return report(cmd.OutOrStdout(), "%s", message)
	}
}

// readBody returns text from --body, or from --body-file where - means stdin.
func readBody(cmd *cobra.Command, body, file string) (string, error) {
	if file == "" {
		return body, nil
	}
	if file == "-" {
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("reading body from stdin: %w", err)
		}
		return string(data), nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("reading body file: %w", err)
	}
	return string(data), nil
}

func addBodyFlags(cmd *cobra.Command, body, file *string) {
	cmd.Flags().StringVar(body, "body", "", "Text of the ticket or comment")
	cmd.Flags().StringVar(file, "body-file", "", "File holding the text; use - for standard input")
	cmd.MarkFlagsMutuallyExclusive("body", "body-file")
}

func newIssueClaimCommand(open opener) *cobra.Command {
	var next bool
	var project string
	cmd := &cobra.Command{
		Use:   "claim [number]",
		Short: "Take a ticket so that no other agent works on it",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if next == (len(args) == 1) {
				return errors.New("pass a ticket number or --next")
			}
			s, err := open()
			if err != nil {
				return err
			}
			if next {
				return claimNext(cmd, s, project)
			}
			number, err := parseNumber(args[0])
			if err != nil {
				return err
			}
			ticket, err := s.client.ClaimTicket(cmd.Context(), number)
			if err != nil {
				return err
			}
			return reportClaim(cmd, s, ticket)
		},
	}
	cmd.Flags().BoolVar(&next, "next", false, "Claim the oldest ready ticket")
	cmd.Flags().StringVar(&project, "project", "", "Limit --next to this project; defaults to the current repository's project")
	return cmd
}

func claimNext(cmd *cobra.Command, s *session, name string) error {
	name, err := s.scope(cmd, name, false)
	if err != nil {
		return err
	}
	ticket, ok, err := s.client.ClaimNext(cmd.Context(), name)
	if err != nil {
		return err
	}
	if !ok {
		return report(cmd.OutOrStdout(), "no ready tickets")
	}
	return reportClaim(cmd, s, ticket)
}

func reportClaim(cmd *cobra.Command, s *session, ticket tracker.Ticket) error {
	if project, err := s.client.ResolveProject(cmd.Context(), ticket.Project); err == nil && project.Public {
		if err := warnPublic(cmd.OutOrStdout(), project.Name); err != nil {
			return err
		}
	}
	return report(cmd.OutOrStdout(), "claimed #%d in %s: %s", ticket.Number, ticket.Project, ticket.Title)
}

// scope picks the project for a list: an explicit name, nothing with all set, or
// the current repository's project using cached metadata.
func (s *session) scope(cmd *cobra.Command, name string, all bool) (string, error) {
	if name != "" || all {
		return name, nil
	}
	project, ok, err := s.client.CurrentProject(cmd.Context(), false)
	if err != nil || !ok {
		return "", err
	}
	return project.Name, nil
}

func newIssueCloseCommand(open opener) *cobra.Command {
	var opts tracker.CloseOptions
	cmd := &cobra.Command{
		Use:   "close <number>",
		Short: "Close a ticket after confirming its pull requests merged",
		Args:  cobra.ExactArgs(1),
		RunE: withTicket(open, func(cmd *cobra.Command, s *session, number int) (string, error) {
			return fmt.Sprintf("closed #%d", number), s.client.CloseTicket(cmd.Context(), number, opts)
		}),
	}
	cmd.Flags().StringArrayVar(&opts.PRs, "pr", nil, "Merged pull request as a number or project#number; repeatable")
	cmd.Flags().StringVar(&opts.Reason, "reason", "", "Close without a pull request: not-planned or duplicate")
	cmd.MarkFlagsOneRequired("pr", "reason")
	cmd.MarkFlagsMutuallyExclusive("pr", "reason")
	return cmd
}

func newIssueCreateCommand(open opener) *cobra.Command {
	var opts tracker.CreateOptions
	var bodyFile string
	var blockedBy, blocking []int
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a ticket in one project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open()
			if err != nil {
				return err
			}
			body, err := readBody(cmd, opts.Body, bodyFile)
			if err != nil {
				return err
			}
			project, err := s.requireProject(cmd, opts.Project)
			if err != nil {
				return err
			}
			opts.Project, opts.Body, opts.BlockedBy, opts.Blocking = project.Name, body, blockedBy, blocking
			ticket, err := s.client.CreateTicket(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return report(cmd.OutOrStdout(), "created #%d in %s: %s", ticket.Number, ticket.Project, ticket.URL)
		},
	}
	cmd.Flags().StringVar(&opts.Title, "title", "", "Ticket title")
	if err := cmd.MarkFlagRequired("title"); err != nil {
		panic(err) // the flag is defined on the line above
	}
	addBodyFlags(cmd, &opts.Body, &bodyFile)
	cmd.Flags().StringVar(&opts.Project, "project", "", "Project for the ticket; defaults to the current repository's project")
	cmd.Flags().StringArrayVar(&opts.Attachments, "attach", nil, "Image or video to upload; repeatable")
	cmd.Flags().StringArrayVar(&opts.Labels, "label", nil, "Label to apply; repeatable")
	cmd.Flags().IntVar(&opts.Parent, "parent", 0, "Parent ticket number")
	cmd.Flags().IntVar(&opts.DiscoveredFrom, "discovered-from", 0, "Ticket that surfaced this work; does not block anything")
	cmd.Flags().IntSliceVar(&blockedBy, "blocked-by", nil, "Tickets that must close first; repeatable")
	cmd.Flags().IntSliceVar(&blocking, "blocking", nil, "Tickets waiting on this one; repeatable")
	return cmd
}

func newIssueEditCommand(open opener) *cobra.Command {
	var title, body, bodyFile string
	cmd := &cobra.Command{
		Use:   "edit <number>",
		Short: "Change a ticket's title or body",
		Args:  cobra.ExactArgs(1),
		RunE: withTicket(open, func(cmd *cobra.Command, s *session, number int) (string, error) {
			var opts tracker.EditOptions
			if cmd.Flags().Changed("title") {
				opts.Title = &title
			}
			if cmd.Flags().Changed("body") || cmd.Flags().Changed("body-file") {
				text, err := readBody(cmd, body, bodyFile)
				if err != nil {
					return "", err
				}
				opts.Body = &text
			}
			return fmt.Sprintf("edited #%d", number), s.client.EditTicket(cmd.Context(), number, opts)
		}),
	}
	cmd.Flags().StringVar(&title, "title", "", "New ticket title")
	addBodyFlags(cmd, &body, &bodyFile)
	return cmd
}

func newIssueInspectCommand(open opener) *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <number>",
		Short: "Show a ticket with its comments and attachments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := parseNumber(args[0])
			if err != nil {
				return err
			}
			s, err := open()
			if err != nil {
				return err
			}
			detail, err := s.client.InspectTicket(cmd.Context(), number)
			if err != nil {
				return err
			}
			return printDetail(cmd.OutOrStdout(), detail)
		},
	}
}

func newIssueListCommand(open opener) *cobra.Command {
	var opts tracker.ListOptions
	var allProjects bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tickets, oldest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open()
			if err != nil {
				return err
			}
			if allProjects && opts.Project != "" {
				return errors.New("pass --project or --all-projects, not both")
			}
			if opts.Project, err = s.scope(cmd, opts.Project, allProjects); err != nil {
				return err
			}
			if !cmd.Flags().Changed("stale-days") {
				opts.StaleDays = s.cfg.StaleDays
			}
			opts.Now = time.Now()
			tickets, err := s.client.ListTickets(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return printTickets(cmd.OutOrStdout(), tickets, opts.StaleDays, opts.Now)
		},
	}
	cmd.Flags().StringVar(&opts.Project, "project", "", "Limit to a project; defaults to the current repository's project")
	cmd.Flags().BoolVar(&allProjects, "all-projects", false, "List every project from inside a registered repository")
	cmd.Flags().StringVar(&opts.State, "state", "open", "Ticket state: open, closed, or all")
	cmd.Flags().StringVar(&opts.Status, "status", "", "Board status: Todo, Doing, or Done")
	cmd.Flags().StringVar(&opts.Label, "label", "", "Only tickets with this label")
	cmd.Flags().StringVar(&opts.Search, "search", "", "Only tickets matching this GitHub search text")
	cmd.Flags().BoolVar(&opts.Ready, "ready", false, "Only tickets that can be picked up now")
	cmd.Flags().BoolVar(&opts.Stale, "stale", false, "Only Doing tickets with no recent activity")
	cmd.Flags().IntVar(&opts.StaleDays, "stale-days", 0, "Days without activity before a Doing ticket is stale; overrides the configured value")
	cmd.Flags().IntVar(&opts.Limit, "limit", 100, "Maximum tickets to show; 0 shows all")
	return cmd
}

func newIssueMoveCommand(open opener) *cobra.Command {
	return &cobra.Command{
		Use:   "move <number> <project>",
		Short: "Reassign a ticket to another project",
		Args:  cobra.ExactArgs(2),
		RunE: withTicket(open, func(cmd *cobra.Command, s *session, number int) (string, error) {
			target := cmd.Flags().Arg(1)
			return fmt.Sprintf("moved #%d to %s", number, target), s.client.MoveTicket(cmd.Context(), number, target)
		}),
	}
}

func newIssueReleaseCommand(open opener) *cobra.Command {
	return &cobra.Command{
		Use:   "release <number>",
		Short: "Give up a claim and return the ticket to Todo",
		Args:  cobra.ExactArgs(1),
		RunE: withTicket(open, func(cmd *cobra.Command, s *session, number int) (string, error) {
			return fmt.Sprintf("released #%d", number), s.client.ReleaseTicket(cmd.Context(), number)
		}),
	}
}

func newIssueReopenCommand(open opener) *cobra.Command {
	return &cobra.Command{
		Use:   "reopen <number>",
		Short: "Reopen a closed ticket",
		Args:  cobra.ExactArgs(1),
		RunE: withTicket(open, func(cmd *cobra.Command, s *session, number int) (string, error) {
			return fmt.Sprintf("reopened #%d", number), s.client.ReopenTicket(cmd.Context(), number)
		}),
	}
}

func newAttachmentCommand(open opener) *cobra.Command {
	cmd := &cobra.Command{Use: "attachment", Short: "Manage images and videos on a ticket"}
	var dir string
	pull := &cobra.Command{
		Use:   "pull <number>",
		Short: "Download a ticket's attachments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := parseNumber(args[0])
			if err != nil {
				return err
			}
			s, err := open()
			if err != nil {
				return err
			}
			target := dir
			if target == "" {
				target = filepath.Join(s.cacheRoot, "attachments", args[0])
			}
			files, err := s.client.PullAttachments(cmd.Context(), number, target)
			if err != nil {
				return err
			}
			return printPaths(cmd.OutOrStdout(), files)
		},
	}
	pull.Flags().StringVar(&dir, "dir", "", "Download directory; defaults to the cache, and must be outside a Git worktree or ignored by it")
	add := &cobra.Command{
		Use:   "add <number> <file...>",
		Short: "Upload images or videos",
		Args:  cobra.MinimumNArgs(2),
		RunE: withTicket(open, func(cmd *cobra.Command, s *session, number int) (string, error) {
			files := cmd.Flags().Args()[1:]
			return fmt.Sprintf("attached %d file(s) to #%d", len(files), number), s.client.AddAttachments(cmd.Context(), number, files)
		}),
	}
	cmd.AddCommand(add, pull)
	return cmd
}

// relation builds add and rm commands that change links between tickets.
func relation(open opener, name, short string, labels [2]string, write func(*cobra.Command, *session, int, []int, bool) error) *cobra.Command {
	cmd := &cobra.Command{Use: name, Short: short}
	build := func(verb, label string, add bool) *cobra.Command {
		return &cobra.Command{
			Use:   verb + " <number> <" + name + "...>",
			Short: label,
			Args:  cobra.MinimumNArgs(2),
			RunE: withTicket(open, func(cmd *cobra.Command, s *session, number int) (string, error) {
				others, err := parseNumbers(cmd.Flags().Args()[1:])
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("updated #%d", number), write(cmd, s, number, others, add)
			}),
		}
	}
	cmd.AddCommand(build("add", labels[0], true), build("rm", labels[1], false))
	return cmd
}

func newBlockerCommand(open opener) *cobra.Command {
	return relation(open, "blocker", "Manage the tickets blocking this one", [2]string{"Mark a ticket blocked by other tickets", "Remove blockers"},
		func(cmd *cobra.Command, s *session, number int, others []int, add bool) error {
			return s.client.SetBlockers(cmd.Context(), number, others, add)
		})
}

func newSubCommand(open opener) *cobra.Command {
	cmd := relation(open, "sub", "Manage sub-issues", [2]string{"Attach sub-issues", "Detach sub-issues"},
		func(cmd *cobra.Command, s *session, number int, others []int, add bool) error {
			return s.client.SetSubIssues(cmd.Context(), number, others, add)
		})
	for _, child := range cmd.Commands() {
		child.Use = child.Name() + " <parent> <child...>"
	}
	return cmd
}

func newCommentCommand(open opener) *cobra.Command {
	var body, bodyFile string
	add := &cobra.Command{
		Use:   "add <number>",
		Short: "Add a comment",
		Args:  cobra.ExactArgs(1),
		RunE: withTicket(open, func(cmd *cobra.Command, s *session, number int) (string, error) {
			text, err := readBody(cmd, body, bodyFile)
			if err != nil {
				return "", err
			}
			if text == "" {
				return "", errors.New("comment is empty: pass --body or --body-file")
			}
			return fmt.Sprintf("commented on #%d", number), s.client.AddComment(cmd.Context(), number, text)
		}),
	}
	addBodyFlags(add, &body, &bodyFile)
	cmd := &cobra.Command{Use: "comment", Short: "Manage comments"}
	cmd.AddCommand(add)
	return cmd
}

func newLabelCommand(open opener) *cobra.Command {
	build := func(verb, short string, write func(*cobra.Command, *session, int, []string) error) *cobra.Command {
		return &cobra.Command{
			Use:   verb + " <number> <label...>",
			Short: short,
			Args:  cobra.MinimumNArgs(2),
			RunE: withTicket(open, func(cmd *cobra.Command, s *session, number int) (string, error) {
				return fmt.Sprintf("updated labels on #%d", number), write(cmd, s, number, cmd.Flags().Args()[1:])
			}),
		}
	}
	cmd := &cobra.Command{Use: "label", Short: "Manage a ticket's labels"}
	cmd.AddCommand(
		build("add", "Add labels", func(cmd *cobra.Command, s *session, number int, labels []string) error {
			return s.client.AddLabels(cmd.Context(), number, labels)
		}),
		build("rm", "Remove labels", func(cmd *cobra.Command, s *session, number int, labels []string) error {
			return s.client.RemoveLabels(cmd.Context(), number, labels)
		}),
	)
	return cmd
}

func newStatusCommand(open opener) *cobra.Command {
	set := &cobra.Command{
		Use:   "set <number> <status>",
		Short: "Move an unclaimed ticket to Todo or Done",
		Args:  cobra.ExactArgs(2),
		RunE: withTicket(open, func(cmd *cobra.Command, s *session, number int) (string, error) {
			status := cmd.Flags().Arg(1)
			return fmt.Sprintf("set #%d to %s", number, status), s.client.SetStatus(cmd.Context(), number, status)
		}),
	}
	cmd := &cobra.Command{Use: "status", Short: "Manage a ticket's board status"}
	cmd.AddCommand(set)
	return cmd
}
