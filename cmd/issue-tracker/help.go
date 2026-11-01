package main

import (
	"fmt"

	cobrahelptree "github.com/alexgorbatchev/cobra-help-tree/v2"
	"github.com/spf13/cobra"
	"github.com/alexgorbatchev/issue-tracker-cli/internal/agent"
)

var techCatalog = cobrahelptree.TechCatalog{
	"issue-tracker": {
		Summary:     "CLI for managing projects and tickets in a private GitHub issue tracker",
		Description: "CLI for managing projects and tickets in a private GitHub issue tracker - command-line interface.",
	},
	"issue-tracker status": {
		Summary:     "Display system and environment status",
		Description: "Outputs current runtime readiness and version information.",
	},
}

func setupHelp(cmd *cobra.Command) {
	_ = cobrahelptree.SetupWithOptions(cmd, cobrahelptree.HelpOptions{
		Catalog: techCatalog,
	})
	help := cmd.HelpFunc()
	cmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		if agent.IsAgentMode() {
			if _, err := fmt.Fprintln(c.OutOrStdout(), "ALERT: Agents must read `AGENT=1 issue-tracker skill` before using this tool."); err != nil {
				c.PrintErrln(err)
				return
			}
		}
		help(c, args)
	})
}
