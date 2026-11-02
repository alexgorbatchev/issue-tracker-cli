package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexgorbatchev/issue-tracker-cli/internal/tracker"
	"github.com/spf13/cobra"
)

// stubGH puts a gh first on PATH that records its arguments and runs script.
func stubGH(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	body := "#!/bin/sh\necho \"$@\" >> " + log + "\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func refreshCommand(stdout *bytes.Buffer) *cobra.Command {
	cmd := newAuthCommand()
	cmd.SetOut(stdout)
	cmd.SetContext(context.Background())
	return cmd
}

func TestAuthRefreshRunsGH(t *testing.T) {
	t.Setenv("AGENT", "0")
	log := stubGH(t, "exit 0")
	var stdout bytes.Buffer
	if err := refreshAuth(refreshCommand(&stdout), true); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(calls)); got != "auth refresh --hostname github.com --scopes project" {
		t.Fatalf("gh arguments = %q", got)
	}
	if stdout.String() != "[OK] project board access granted\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestAuthRefreshReportsGHFailure(t *testing.T) {
	t.Setenv("AGENT", "0")
	stubGH(t, "exit 1")
	var stdout bytes.Buffer
	err := refreshAuth(refreshCommand(&stdout), true)
	if err == nil || !strings.Contains(err.Error(), "refreshing GitHub login") || stdout.Len() != 0 {
		t.Fatalf("err = %v, stdout = %q", err, stdout.String())
	}
}

func TestAuthRefreshNeedsAPerson(t *testing.T) {
	tests := []struct {
		name        string
		agent       string
		interactive bool
	}{
		{"agent at a terminal", "1", true},
		{"no terminal", "0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AGENT", tt.agent)
			log := stubGH(t, "exit 0")
			var stdout bytes.Buffer
			err := refreshAuth(refreshCommand(&stdout), tt.interactive)
			if err == nil || !strings.Contains(err.Error(), "needs a person") {
				t.Fatalf("err = %v", err)
			}
			if _, statErr := os.Stat(log); !os.IsNotExist(statErr) {
				t.Fatal("gh must not start without a person present")
			}
		})
	}
}

func TestAuthRefreshCommandRefusesWithoutATerminal(t *testing.T) {
	t.Setenv("AGENT", "0")
	log := stubGH(t, "exit 0")
	stdin, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	previous := os.Stdin
	os.Stdin = stdin
	t.Cleanup(func() { os.Stdin = previous })
	if _, _, err := execute(t, noSession, "auth", "refresh"); err == nil || !strings.Contains(err.Error(), "needs a person") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("gh must not start without a terminal")
	}
}

func TestRunKeepsWhatFailedAroundTheBoardAccessFix(t *testing.T) {
	access := &tracker.BoardAccessError{Detail: "needs read:project"}
	failure := func(*cobra.Command, []string) error {
		return fmt.Errorf("ticket #50 was created but linking it failed: %w", access)
	}
	prefix := "ticket #50 was created but linking it failed: " + access.Error()
	tests := []struct {
		name, agent, want string
	}{
		{"people get what failed and the fix", "0", "[ERROR] " + prefix + "\n"},
		{"agents also get the cause", "1", "ERR: " + prefix + " (needs read:project)\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AGENT", tt.agent)
			cmd := &cobra.Command{Use: "x", RunE: failure, SilenceUsage: true, SilenceErrors: true}
			var stderr bytes.Buffer
			cmd.SetErr(&stderr)
			cmd.SetArgs(nil)
			if code := run(context.Background(), cmd); code != 1 || stderr.String() != tt.want {
				t.Fatalf("code = %d, stderr = %q", code, stderr.String())
			}
		})
	}
}
