// Package config resolves issue-tracker settings from the XDG config file and
// environment variables, with the environment taking precedence.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/pelletier/go-toml/v2"
)

const (
	appName          = "issue-tracker"
	defaultStaleDays = 3

	// RepoEnv names the tracker repository as owner/name.
	RepoEnv = "ISSUE_TRACKER_REPO"
	// StaleDaysEnv sets the days without activity before Doing tickets are stale.
	StaleDaysEnv = "ISSUE_TRACKER_STALE_DAYS"
	// RequirePrivateEnv makes commands refuse a tracker that is not private.
	RequirePrivateEnv = "ISSUE_TRACKER_REQUIRE_PRIVATE"
)

// Config holds resolved settings.
type Config struct {
	Repo           string `toml:"repo"`
	StaleDays      int    `toml:"stale_days"`
	RequirePrivate bool   `toml:"require_private"`
}

// Load reads the config file when it exists, then applies the environment.
func Load() (Config, error) {
	cfg := Config{StaleDays: defaultStaleDays}
	dir, err := configDir()
	if err != nil {
		return Config{}, err
	}
	if err := readFile(filepath.Join(dir, "config.toml"), &cfg); err != nil {
		return Config{}, err
	}
	if err := applyEnv(&cfg); err != nil {
		return Config{}, err
	}
	if cfg.StaleDays < 1 {
		return Config{}, fmt.Errorf("stale_days must be at least 1, got %d", cfg.StaleDays)
	}
	return cfg, nil
}

func readFile(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}
	return nil
}

func applyEnv(cfg *Config) error {
	if value := os.Getenv(RepoEnv); value != "" {
		cfg.Repo = value
	}
	if value := os.Getenv(StaleDaysEnv); value != "" {
		days, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s must be a whole number of days: %w", StaleDaysEnv, err)
		}
		cfg.StaleDays = days
	}
	if value := os.Getenv(RequirePrivateEnv); value != "" {
		required, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("%s must be true or false: %w", RequirePrivateEnv, err)
		}
		cfg.RequirePrivate = required
	}
	return nil
}

// CacheDir returns the directory for the metadata cache and pulled attachments.
// It uses ~/.cache rather than os.UserCacheDir so every platform shares one path.
func CacheDir() (string, error) {
	return xdgDir("XDG_CACHE_HOME", ".cache")
}

func configDir() (string, error) {
	return xdgDir("XDG_CONFIG_HOME", ".config")
}

func xdgDir(env, fallback string) (string, error) {
	if base := os.Getenv(env); base != "" {
		return filepath.Join(base, appName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding home directory: %w", err)
	}
	return filepath.Join(home, fallback, appName), nil
}
