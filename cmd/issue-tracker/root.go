package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/alexgorbatchev/issue-tracker-cli/internal/agent"
)

var (
	version = "0.1.0"
)

func newRootCommand() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:          "issue-tracker",
		Short:        "CLI for managing projects and tickets in a private GitHub issue tracker",
		Long:         "issue-tracker is a command-line tool designed for CLI for managing projects and tickets in a private GitHub issue tracker.",
		Version:      version,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	rootCmd.SetVersionTemplate("{{.Version}}\n")

	rootCmd.AddCommand(newStatusCommand())
	rootCmd.AddCommand(newSkillCommand())

	setupHelp(rootCmd)

	return rootCmd
}

func newStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Display system and environment status",
		Long:  "Inspects and outputs the current system and runtime status.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if agent.IsAgentMode() {
				fmt.Fprintln(cmd.OutOrStdout(), "status: ready")
				fmt.Fprintln(cmd.OutOrStdout(), "version: "+version)
				return nil
			}

			fmt.Fprintln(cmd.OutOrStdout(), "[OK] System status: ready")
			fmt.Fprintln(cmd.OutOrStdout(), "Version: "+version)
			return nil
		},
	}
	return cmd
}
