package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	if content == "" {
		return
	}
	dir := filepath.Join(base, appName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{RepoEnv, StaleDaysEnv, RequirePrivateEnv, AccountEnv, AgentAccountEnv} {
		t.Setenv(name, "")
	}
}

func TestLoadPrecedence(t *testing.T) {
	tests := []struct {
		name string
		file string
		env  map[string]string
		want Config
	}{
		{"defaults", "", nil, Config{StaleDays: 3}},
		{"file", "repo = \"o/file\"\nstale_days = 5\nrequire_private = true\n", nil, Config{Repo: "o/file", StaleDays: 5, RequirePrivate: true}},
		{
			"environment wins", "repo = \"o/file\"\nstale_days = 5\nrequire_private = true\naccount = \"me\"\nagent_account = \"bot\"\n",
			map[string]string{RepoEnv: "o/env", StaleDaysEnv: "9", RequirePrivateEnv: "false", AccountEnv: "env-me", AgentAccountEnv: "env-bot"},
			Config{Repo: "o/env", StaleDays: 9, Account: "env-me", AgentAccount: "env-bot"},
		},
		{"accounts from the file", "account = \"me\"\nagent_account = \"bot\"\n", nil, Config{StaleDays: 3, Account: "me", AgentAccount: "bot"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			writeConfig(t, tt.file)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			got, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("Load() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		file string
		env  map[string]string
		want string
	}{
		{"malformed toml", "repo = ", nil, "parsing config"},
		{"zero stale days", "stale_days = 0", nil, "at least 1"},
		{"stale days env", "", map[string]string{StaleDaysEnv: "soon"}, StaleDaysEnv},
		{"require private env", "", map[string]string{RequirePrivateEnv: "maybe"}, RequirePrivateEnv},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			writeConfig(t, tt.file)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadReportsUnreadableConfig(t *testing.T) {
	clearEnv(t)
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	if err := os.MkdirAll(filepath.Join(base, appName, "config.toml"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "reading config") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", "")
	got, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".cache", appName); got != want {
		t.Fatalf("CacheDir() = %q, want %q", got, want)
	}
	t.Setenv("XDG_CACHE_HOME", "/xdg")
	if got, _ := CacheDir(); got != filepath.Join("/xdg", appName) {
		t.Fatalf("CacheDir() = %q", got)
	}
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() without a home directory must fail")
	}
}

func TestLoginPicksTheAgentAccountOnlyForAgents(t *testing.T) {
	tests := []struct {
		name  string
		cfg   Config
		agent bool
		want  string
	}{
		{"neither", Config{}, true, ""},
		{"account for people", Config{Account: "me", AgentAccount: "bot"}, false, "me"},
		{"agent account for agents", Config{Account: "me", AgentAccount: "bot"}, true, "bot"},
		{"account for agents without an agent account", Config{Account: "me"}, true, "me"},
		{"agent account alone leaves people on gh's choice", Config{AgentAccount: "bot"}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.Login(tt.agent); got != tt.want {
				t.Fatalf("Login(%v) = %q, want %q", tt.agent, got, tt.want)
			}
		})
	}
}
