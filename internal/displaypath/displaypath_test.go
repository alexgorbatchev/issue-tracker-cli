package displaypath

import (
	"path/filepath"
	"testing"
)

func TestFold(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "dev")
	t.Setenv("HOME", home)
	tests := []struct {
		name string
		path string
		want string
	}{
		{"home itself", home, "~"},
		{"nested", filepath.Join(home, "projects", "app"), filepath.Join("~", "projects", "app")},
		{"sibling with shared prefix", home + "eloper/app", home + "eloper/app"},
		{"outside home", "/var/tmp/file", "/var/tmp/file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Fold(tt.path); got != tt.want {
				t.Fatalf("Fold(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestFoldWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	if got := Fold("/some/path"); got != "/some/path" {
		t.Fatalf("Fold = %q", got)
	}
}
