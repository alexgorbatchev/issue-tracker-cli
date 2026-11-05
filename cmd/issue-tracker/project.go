package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/alexgorbatchev/issue-tracker-cli/internal/tracker"
	"github.com/spf13/cobra"
)

func newProjectCommand(open opener) *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "Manage the repositories registered with the tracker"}
	cmd.AddCommand(ownerOnly(newProjectAddCommand(open)), newProjectInspectCommand(open), newProjectListCommand(open),
		ownerOnly(newProjectRmCommand(open)), ownerOnly(newProjectSyncCommand(open)), ownerOnly(newProjectScanCommand(open)),
		ownerOnly(newProjectImportCommand(open)))
	return cmd
}

func newProjectAddCommand(open opener) *cobra.Command {
	var dir, name string
	cmd := &cobra.Command{
		Use:   "add [owner/repo]",
		Short: "Register a repository, or a directory of one, and create its label and board",
		Long:  "Registers a repository with its own label and board. With --path, registers a directory of an already registered repository as a component: a project with its own label on the repository's board.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if name != "" && dir == "" {
				return errors.New("--name names a component: pass --path too")
			}
			s, err := open(cmd)
			if err != nil {
				return err
			}
			repo, err := addTarget(args)
			if err != nil {
				return err
			}
			var project tracker.Project
			if dir == "" {
				project, err = s.client.AddProject(cmd.Context(), repo)
			} else {
				project, err = s.client.AddComponent(cmd.Context(), repo, dir, name)
			}
			if err != nil {
				return err
			}
			if project.Component() {
				return report(cmd.OutOrStdout(), "registered project %s for %s in %s", project.Name, project.Path, project.Repo)
			}
			return report(cmd.OutOrStdout(), "registered project %s for %s", project.Name, project.Repo)
		},
	}
	cmd.Flags().StringVar(&dir, "path", "", "Register this directory, relative to the top of the repository, as a component")
	cmd.Flags().StringVar(&name, "name", "", "Component name after the repository's; defaults to the last element of --path")
	return cmd
}

func addTarget(args []string) (tracker.Repo, error) {
	if len(args) == 1 {
		return tracker.ParseRepo(args[0])
	}
	repo, err := tracker.CurrentRepo()
	if err != nil {
		return tracker.Repo{}, fmt.Errorf("not in a GitHub repository: %w: pass owner/repo", err)
	}
	return repo, nil
}

func newProjectInspectCommand(open opener) *cobra.Command {
	return &cobra.Command{
		Use:   "inspect [project]",
		Short: "Show a project's repository, board, and ticket counts",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open(cmd)
			if err != nil {
				return err
			}
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			project, err := s.requireProject(cmd, name, "name a project")
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
			s, err := open(cmd)
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
		Short: "Unregister a project with no open tickets and delete a repository's board",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirmed {
				return errors.New("this unregisters the project and deletes a repository's board: pass --yes to confirm")
			}
			s, err := open(cmd)
			if err != nil {
				return err
			}
			if err := s.client.RemoveProject(cmd.Context(), args[0]); err != nil {
				return err
			}
			return report(cmd.OutOrStdout(), "removed project %s", args[0])
		},
	}
	cmd.Flags().BoolVar(&confirmed, "yes", false, "Confirm the removal")
	return cmd
}

func newProjectSyncCommand(open opener) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "sync [project]",
		Short: "Copy a repository's description and link onto its project board",
		Long:  "Sets a project board's description to its repository's description, followed by a blank line and the repository's URL, and leaves boards that already match unchanged. A component shares its repository's board.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all && len(args) == 1 {
				return errors.New("name a project or pass --all-projects, not both")
			}
			s, err := open(cmd)
			if err != nil {
				return err
			}
			var projects []tracker.Project
			if all {
				projects, err = s.client.Projects(cmd.Context())
			} else {
				name := ""
				if len(args) == 1 {
					name = args[0]
				}
				var project tracker.Project
				project, err = s.requireProject(cmd, name, "name a project, or pass --all-projects for every project")
				projects = []tracker.Project{project}
			}
			if err != nil {
				return err
			}
			result, err := s.client.SyncDescriptions(cmd.Context(), projects)
			if err != nil {
				return err
			}
			if err := report(cmd.OutOrStdout(), "updated %d of %d board descriptions", len(result.Updated), result.Boards); err != nil {
				return err
			}
			if len(result.Problems) > 0 {
				return fmt.Errorf("could not sync %d projects: %s", len(result.Problems), strings.Join(result.Problems, "; "))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all-projects", false, "Sync the board of every project")
	return cmd
}

func newProjectScanCommand(open opener) *cobra.Command {
	var authors []string
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Count open issues across your repositories by who wrote them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open(cmd)
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
			s, err := open(cmd)
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

// requireProject resolves an explicit project name or the current repository's
// project. Remedy names how the command takes a project.
func (s *session) requireProject(cmd *cobra.Command, name, remedy string) (tracker.Project, error) {
	if name != "" {
		return s.client.ResolveProject(cmd.Context(), name)
	}
	return s.currentProject(cmd, remedy)
}
