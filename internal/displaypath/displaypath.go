// Package displaypath formats filesystem paths for terminal output.
package displaypath

import (
	"os"
	"path/filepath"
	"strings"
)

// Fold replaces a leading home directory with ~ for display. Only whole path
// segments match, so a sibling such as /home/developer is left unchanged when
// the home directory is /home/dev. Keep absolute paths for filesystem calls.
func Fold(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return filepath.Join("~", rest)
	}
	return path
}
