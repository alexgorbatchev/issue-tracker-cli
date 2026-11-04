package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	cobrahelptree "github.com/alexgorbatchev/cobra-help-tree/v4"
	"github.com/alexgorbatchev/issue-tracker-cli/internal/config"
	"github.com/alexgorbatchev/issue-tracker-cli/internal/tracker"
	"github.com/spf13/cobra"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, newRootCommand(openTracker))
	stop()
	os.Exit(code)
}

func run(ctx context.Context, cmd *cobra.Command) int {
	err := cmd.ExecuteContext(ctx)
	if err == nil {
		err = helpWriteError
	}
	if err != nil {
		if cobrahelptree.IsAgentMode() {
			fmt.Fprintf(cmd.ErrOrStderr(), "ERR: %s\n", forAgents(err))
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "[ERROR] %v\n", err)
		}
		return 1
	}
	return 0
}

// forAgents adds GitHub's explanation of a missing scope, which people do not need.
func forAgents(err error) string {
	var access *tracker.BoardAccessError
	if errors.As(err, &access) {
		return err.Error() + " (" + access.Detail + ")"
	}
	return err.Error()
}

// session is the configured client for one command.
type session struct {
	client    *tracker.Client
	cfg       config.Config
	cacheRoot string
}

// opener builds a session for a command on first use, so help and version need no
// configuration.
type opener func(*cobra.Command) (*session, error)

// ownerAnnotation marks a command that changes what only the tracker's owner can:
// boards under the owner, labels, and issues moved between the owner's
// repositories. An agent account lacks those rights, so such a command acts as the
// account setting instead.
const ownerAnnotation = "owner-only"

// ownerOnly marks cmd as acting as the account setting even in agent mode, and says
// so in its help.
func ownerOnly(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[ownerAnnotation] = "true"
	note := "Runs as the account setting, or else the default GitHub login (GH_TOKEN, or the GitHub CLI's active account), even when agent_account applies, because only the tracker's owner can make these changes."
	cmd.Long = strings.TrimSpace(cmd.Long + " " + note)
	return cmd
}

// login picks the account for cmd, and warns when an owner-only command sets aside
// the agent account.
func login(cmd *cobra.Command, cfg config.Config) (string, error) {
	agent := cobrahelptree.IsAgentMode()
	chosen := cfg.Login(agent)
	owner := cfg.Login(false)
	if !agent || cmd.Annotations[ownerAnnotation] == "" || chosen == owner {
		return chosen, nil
	}
	return owner, warn(cmd.ErrOrStderr(), "%s runs as %s, not agent account %s, because only the tracker's owner can make its changes", cmd.CommandPath(), describeLogin(owner), chosen)
}

// describeLogin names the credential an empty account falls back to, in the order
// go-gh reads them.
func describeLogin(account string) string {
	switch {
	case account != "":
		return account
	case os.Getenv("GH_TOKEN") != "":
		return "the GH_TOKEN login"
	case os.Getenv("GITHUB_TOKEN") != "":
		return "the GITHUB_TOKEN login"
	}
	return "the GitHub CLI's active account"
}

func openTracker(cmd *cobra.Command) (*session, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if cfg.Repo == "" {
		return nil, fmt.Errorf("no tracker repository: set repo in config.toml or %s", config.RepoEnv)
	}
	repo, err := tracker.ParseRepo(cfg.Repo)
	if err != nil {
		return nil, err
	}
	cacheRoot, err := config.CacheDir()
	if err != nil {
		return nil, err
	}
	account, err := login(cmd, cfg)
	if err != nil {
		return nil, err
	}
	client, err := tracker.New(tracker.Options{Repo: repo, CacheDir: cacheRoot, RequirePrivate: cfg.RequirePrivate, Account: account})
	if err != nil {
		return nil, err
	}
	return &session{client: client, cfg: cfg, cacheRoot: cacheRoot}, nil
}
