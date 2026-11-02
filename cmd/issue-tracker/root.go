package main

import (
	"github.com/spf13/cobra"
)

func newRootCommand(open opener) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           "issue-tracker",
		Short:         "Manage projects and tickets in one private GitHub repository",
		Long:          "issue-tracker keeps tickets for all your repositories in one private GitHub repository, with boards, atomic claims, and attachments.",
		Version:       currentVersion(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	rootCmd.SetVersionTemplate("{{.Version}}\n")
	rootCmd.AddCommand(newProjectCommand(open), newIssueCommand(open))
	setupHelp(rootCmd)
	return rootCmd
}
