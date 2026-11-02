package main

import (
	"errors"

	"github.com/alexgorbatchev/issue-tracker-cli/internal/tracker"
	"github.com/spf13/cobra"
)

func newProjectCommand(open opener) *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "Manage the repositories registered with the tracker"}
	cmd.AddCommand(newProjectAddCommand(open), newProjectInspectCommand(open), newProjectListCommand(open), newProjectRmCommand(open), newProjectScanCommand(open), newProjectImportCommand(open))
	return cmd
}

func newProjectAddCommand(open opener) *cobra.Command {
	return &cobra.Command{
		Use:   "add [owner/repo]",
		Short: "Register a repository and create its label and board",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open()
			if err != nil {
				return err
			}
			repo, err := addTarget(args)
			if err != nil {
				return err
			}
			project, err := s.client.AddProject(cmd.Context(), repo)
			if err != nil {
				return err
			}
			return report(cmd.OutOrStdout(), "registered project %s for %s", project.Name, project.Repo)
		},
	}
}

func addTarget(args []string) (tracker.Repo, error) {
	if len(args) == 1 {
		return tracker.ParseRepo(args[0])
	}
	repo, ok := tracker.CurrentRepo()
	if !ok {
		return tracker.Repo{}, errors.New("not in a GitHub repository: pass owner/repo")
	}
	return repo, nil
}

func newProjectInspectCommand(open opener) *cobra.Command {
	return &cobra.Command{
		Use:   "inspect [project]",
		Short: "Show a project's repository, board, and ticket counts",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open()
			if err != nil {
				return err
			}
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			project, err := s.requireProject(cmd, name)
			if err != nil {
				return err
			}
			summary, err := s.client.ProjectSummary(cmd.Context(), project)
			if err != nil {
				return err
			}
			return printSummary(cmd.OutOrStdout(), summary)
		},
	}
}

func newProjectListCommand(open opener) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered projects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open()
			if err != nil {
				return err
			}
			projects, err := s.client.Projects(cmd.Context())
			if err != nil {
				return err
			}
			return printProjects(cmd.OutOrStdout(), projects)
		},
	}
}

func newProjectRmCommand(open opener) *cobra.Command {
	var confirmed bool
	cmd := &cobra.Command{
		Use:   "rm <project>",
		Short: "Unregister a project with no open tickets and delete its board",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirmed {
				return errors.New("this deletes the project's board: pass --yes to confirm")
			}
			s, err := open()
			if err != nil {
				return err
			}
			if err := s.client.RemoveProject(cmd.Context(), args[0]); err != nil {
				return err
			}
			return report(cmd.OutOrStdout(), "removed project %s", args[0])
		},
	}
	cmd.Flags().BoolVar(&confirmed, "yes", false, "Confirm deleting the project's board")
	return cmd
}

func newProjectScanCommand(open opener) *cobra.Command {
	var authors []string
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Count open issues across your repositories by who wrote them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open()
			if err != nil {
				return err
			}
			scans, err := s.client.ScanRepos(cmd.Context(), authors)
			if err != nil {
				return err
			}
			return printScans(cmd.OutOrStdout(), scans)
		},
	}
	cmd.Flags().StringArrayVar(&authors, "author", nil, "Also trust text from this login. Repeatable")
	return cmd
}

func newProjectImportCommand(open opener) *cobra.Command {
	opts := tracker.ImportOptions{}
	cmd := &cobra.Command{
		Use:   "import <owner/repo>",
		Short: "Move a repository's open issues into the tracker",
		Long:  "Moves every open issue of a private repository, and each issue of a public repository written entirely by trusted authors. A public issue cannot be moved back.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := tracker.ParseRepo(args[0])
			if err != nil {
				return err
			}
			s, err := open()
			if err != nil {
				return err
			}
			opts.Repo = repo
			opts.OnMove = func(source, ticket int) error {
				return report(cmd.OutOrStdout(), "moved %s#%d to #%d", repo, source, ticket)
			}
			result, err := s.client.ImportIssues(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return report(cmd.OutOrStdout(), "%s: moved %d, %d left to move, skipped %d with text from other people", repo, result.Moved, result.Remaining, result.Skipped)
		},
	}
	cmd.Flags().StringArrayVar(&opts.Authors, "author", nil, "Also trust text from this login. Repeatable")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Report what would move without changing anything")
	cmd.Flags().IntVar(&opts.Limit, "limit", 0, "Maximum issues to move in this run. 0 moves all")
	return cmd
}

// requireProject resolves an explicit project name or the current repository's project.
func (s *session) requireProject(cmd *cobra.Command, name string) (tracker.Project, error) {
	if name != "" {
		return s.client.ResolveProject(cmd.Context(), name)
	}
	project, ok, err := s.client.CurrentProject(cmd.Context(), true)
	if err != nil {
		return tracker.Project{}, err
	}
	if !ok {
		return tracker.Project{}, errors.New("not in a registered repository: pass a project")
	}
	return project, nil
}
