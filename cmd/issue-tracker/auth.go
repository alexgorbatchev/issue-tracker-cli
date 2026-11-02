package main

import (
	"errors"
	"fmt"
	"os"

	cobrahelptree "github.com/alexgorbatchev/cobra-help-tree/v4"
	"github.com/cli/go-gh/v2"
	"github.com/cli/go-gh/v2/pkg/term"
	"github.com/spf13/cobra"
)

func newAuthCommand() *cobra.Command {
	refresh := &cobra.Command{
		Use:   "refresh",
		Short: "Give your GitHub login access to project boards",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return refreshAuth(cmd, term.IsTerminal(os.Stdin))
		},
	}
	cmd := &cobra.Command{Use: "auth", Short: "Manage GitHub access"}
	cmd.AddCommand(refresh)
	return cmd
}

// refreshAuth refuses without a person present, because gh would otherwise wait
// minutes for a browser approval that nobody can give.
func refreshAuth(cmd *cobra.Command, interactive bool) error {
	if cobrahelptree.IsAgentMode() || !interactive {
		return errors.New("auth refresh needs a person at a terminal to approve the login in a browser")
	}
	if err := gh.ExecInteractive(cmd.Context(), "auth", "refresh", "--hostname", "github.com", "--scopes", "project"); err != nil {
		return fmt.Errorf("refreshing GitHub login: %w", err)
	}
	return report(cmd.OutOrStdout(), "project board access granted")
}
