package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
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

// opener builds a session on first use, so help and version need no configuration.
type opener func() (*session, error)

func openTracker() (*session, error) {
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
	client, err := tracker.New(tracker.Options{Repo: repo, CacheDir: cacheRoot, RequirePrivate: cfg.RequirePrivate})
	if err != nil {
		return nil, err
	}
	return &session{client: client, cfg: cfg, cacheRoot: cacheRoot}, nil
}
