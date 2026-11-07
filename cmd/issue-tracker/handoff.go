package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	cobrahelptree "github.com/alexgorbatchev/cobra-help-tree/v4"
	"github.com/alexgorbatchev/issue-tracker-cli/internal/tracker"
	"github.com/spf13/cobra"
)

func newHandoffCommand(open opener) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "handoff",
		Short: "Hand work to the next agent, one open handoff per project",
		Long:  "A handoff is a ticket that records the branch and commit where work stopped. A project holds at most one open handoff; the next agent takes it, which claims and closes it at once.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newHandoffCreateCommand(open), newHandoffShowCommand(open), newHandoffTakeCommand(open), newHandoffDropCommand(open))
	return cmd
}

// handoffRemedy ends the error of a handoff command run outside a registered repository.
const handoffRemedy = "pass --project <name>"

func newHandoffCreateCommand(open opener) *cobra.Command {
	var opts tracker.HandoffOptions
	var bodyFile string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Open the project's handoff at a branch and commit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open(cmd)
			if err != nil {
				return err
			}
			if opts.Text, err = readBody(cmd, opts.Text, bodyFile); err != nil {
				return err
			}
			project, err := s.requireProject(cmd, opts.Project, handoffRemedy)
			if err != nil {
				return err
			}
			if opts.Branch == "" || opts.SHA == "" {
				if err := checkoutDefaults(project, &opts); err != nil {
					return err
				}
			}
			opts.Project = project.Name
			handoff, err := s.client.CreateHandoff(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return report(cmd.OutOrStdout(), "created handoff #%d in %s at %s %s: %s", handoff.Ticket.Number, handoff.Ticket.Project, handoff.Branch, handoff.SHA, handoff.Ticket.URL)
		},
	}
	cmd.Flags().StringVar(&opts.Title, "title", "", "Handoff title")
	if err := cmd.MarkFlagRequired("title"); err != nil {
		panic(err) // the flag is defined on the line above
	}
	addBodyFlags(cmd, &opts.Text, &bodyFile)
	cmd.Flags().StringVar(&opts.Project, "project", "", "Project to hand off; defaults to the current repository's project")
	cmd.Flags().StringVar(&opts.Branch, "branch", "", "Branch the work is on; defaults to the current checkout's branch")
	cmd.Flags().StringVar(&opts.SHA, "sha", "", "Commit the work stopped at, full or abbreviated; defaults to the current checkout's HEAD")
	cmd.Flags().IntVar(&opts.Related, "ticket", 0, "Ticket the work belongs to; taking the handoff does not claim it")
	return cmd
}

// checkoutDefaults fills a missing branch or commit from the current checkout, which
// has to be a checkout of the project's repository.
func checkoutDefaults(project tracker.Project, opts *tracker.HandoffOptions) error {
	missing := "pass --branch and --sha, or run inside a checkout of " + project.Repo
	repo, err := tracker.CurrentRepo()
	if err != nil {
		return fmt.Errorf("no branch or commit to hand off: %w: %s", err, missing)
	}
	if !strings.EqualFold(repo.String(), project.Repo) {
		return fmt.Errorf("no branch or commit to hand off: this checkout is %s, not %s: %s", repo, project.Repo, missing)
	}
	if opts.Branch == "" {
		branch, err := git("symbolic-ref", "--quiet", "--short", "HEAD")
		if err != nil {
			return fmt.Errorf("no branch to hand off: the checkout has no current branch: pass --branch")
		}
		opts.Branch = branch
	}
	if opts.SHA == "" {
		sha, err := git("rev-parse", "--verify", "HEAD")
		if err != nil {
			return fmt.Errorf("no commit to hand off: the checkout has no commits: pass --sha")
		}
		opts.SHA = sha
	}
	return nil
}

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(out)), err
}

func newHandoffShowCommand(open opener) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show the project's open handoff without taking it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, name, err := handoffScope(cmd, open, project)
			if err != nil {
				return err
			}
			handoff, err := s.client.ShowHandoff(cmd.Context(), name)
			if errors.Is(err, tracker.ErrNoHandoff) {
				return report(cmd.OutOrStdout(), "%s", err)
			}
			if err != nil {
				return err
			}
			return printHandoff(cmd, s, handoff, time.Now())
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project whose handoff to show; defaults to the current repository's project")
	return cmd
}

func newHandoffTakeCommand(open opener) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "take",
		Short: "Pick up the project's open handoff: claim and close it, and print where to resume",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, name, err := handoffScope(cmd, open, project)
			if err != nil {
				return err
			}
			taken, err := s.client.TakeHandoff(cmd.Context(), name)
			if err != nil {
				return err
			}
			if taken.Moved {
				if err := warn(cmd.ErrOrStderr(), "branch %s no longer contains commit %s: check where the work went before resuming", taken.Branch, taken.SHA); err != nil {
					return err
				}
			}
			return printHandoff(cmd, s, taken.Handoff, time.Now())
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project whose handoff to take; defaults to the current repository's project")
	return cmd
}

func newHandoffDropCommand(open opener) *cobra.Command {
	var project, reason string
	cmd := &cobra.Command{
		Use:   "drop",
		Short: "Close the project's open handoff without taking it, freeing the slot",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, name, err := handoffScope(cmd, open, project)
			if err != nil {
				return err
			}
			handoff, ok, err := s.client.DropHandoff(cmd.Context(), name, reason)
			if err != nil {
				return err
			}
			if !ok {
				return report(cmd.OutOrStdout(), "freed the handoff slot of %s, which held no open handoff", name)
			}
			return report(cmd.OutOrStdout(), "dropped handoff #%d in %s: %s", handoff.Ticket.Number, handoff.Ticket.Project, handoff.Ticket.Title)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project whose handoff to drop; defaults to the current repository's project")
	cmd.Flags().StringVar(&reason, "reason", "", "Why the handoff is dropped, posted as a comment")
	return cmd
}

// handoffScope opens a session and names the project a handoff command acts on.
func handoffScope(cmd *cobra.Command, open opener, name string) (*session, string, error) {
	s, err := open(cmd)
	if err != nil {
		return nil, "", err
	}
	project, err := s.requireProject(cmd, name, handoffRemedy)
	if err != nil {
		return nil, "", err
	}
	return s, project.Name, nil
}

// printHandoff prints where a handoff's work stopped, then its text. Agents get one
// OK: line with the number, project, branch, and commit tab-separated.
func printHandoff(cmd *cobra.Command, s *session, handoff tracker.Handoff, now time.Time) error {
	w := cmd.OutOrStdout()
	ticket := handoff.Ticket
	if project, err := s.client.ResolveProject(cmd.Context(), ticket.Project); err == nil && project.Public {
		if err := warnPublic(w, project.Name); err != nil {
			return err
		}
	}
	if cobrahelptree.IsAgentMode() {
		if _, err := fmt.Fprintf(w, "OK: %d\t%s\t%s\t%s\n", ticket.Number, ticket.Project, handoff.Branch, handoff.SHA); err != nil {
			return err
		}
	} else {
		lines := []string{
			fmt.Sprintf("handoff: #%d %s", ticket.Number, ticket.Title),
			"project: " + ticket.Project,
			"branch: " + handoff.Branch,
			"commit: " + handoff.SHA,
		}
		if handoff.Related != 0 {
			lines = append(lines, "ticket: #"+strconv.Itoa(handoff.Related))
		}
		lines = append(lines, "age: "+age(now.Sub(ticket.Created())), "url: "+ticket.URL)
		if _, err := fmt.Fprintln(w, strings.Join(lines, "\n")); err != nil {
			return err
		}
	}
	if handoff.Text == "" {
		return nil
	}
	_, err := fmt.Fprintf(w, "\n%s\n", handoff.Text)
	return err
}

// warnHandoffs points at open handoffs that claim --next passed over, because they
// may hold work to review before starting something new.
func warnHandoffs(cmd *cobra.Command, handoffs []tracker.Ticket, now time.Time) error {
	for _, ticket := range handoffs {
		if err := warn(cmd.ErrOrStderr(), "open handoff #%d in %s, %s old: review it with issue-tracker handoff show --project %s",
			ticket.Number, ticket.Project, age(now.Sub(ticket.Created())), ticket.Project); err != nil {
			return err
		}
	}
	return nil
}

// age rounds a duration down to whole minutes, hours, or days.
func age(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}
