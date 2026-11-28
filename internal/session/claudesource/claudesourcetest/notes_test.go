package claudesourcetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSharedNotesFolderStaysInsideTheTestsOwnConfigDir: the helper must never
// hand a test a folder under the developer's real app data, whatever the
// process environment says on the way in.
func TestSharedNotesFolderStaysInsideTheTestsOwnConfigDir(t *testing.T) {
	tests := []struct {
		name string
		xdg  func(t *testing.T) string
		kept bool
	}{
		{"a developer's own config dir is replaced", func(*testing.T) string { return filepath.Join(string(os.PathSeparator), "home", "dev", ".config") }, false},
		{"an unset config dir is replaced", func(*testing.T) string { return "" }, false},
		{"one a fixture already moved into the temp area stays", func(t *testing.T) string { return t.TempDir() }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			xdg := tt.xdg(t)
			t.Setenv("XDG_CONFIG_HOME", xdg)

			notes := SharedNotesFolder(t)

			if info, err := os.Stat(notes); err != nil || !info.IsDir() {
				t.Fatalf("SharedNotesFolder() = %q, want an existing directory (err %v)", notes, err)
			}
			if !insideTempDir(notes) {
				t.Fatalf("SharedNotesFolder() = %q, want a folder inside the temp area", notes)
			}
			config, err := os.UserConfigDir()
			if err != nil {
				t.Fatal(err)
			}
			if config, err = filepath.EvalSymlinks(config); err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(config, "Claude", "side-session-notes", "local_1")
			if notes != want {
				t.Fatalf("SharedNotesFolder() = %q, want %q: the folder must be where the app would put it", notes, want)
			}
			if got := os.Getenv("XDG_CONFIG_HOME"); (got == xdg) != tt.kept {
				t.Fatalf("XDG_CONFIG_HOME = %q after the call (was %q), kept = %v, want %v", got, xdg, got == xdg, tt.kept)
			}
			if home := os.Getenv("HOME"); !insideTempDir(home) {
				t.Fatalf("HOME = %q after the call, want a throwaway folder", home)
			}
		})
	}
}

func TestInsideTempDir(t *testing.T) {
	inside := t.TempDir()
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"empty", "", false},
		{"the temp dir itself", os.TempDir(), false},
		{"its parent", filepath.Dir(filepath.Clean(os.TempDir())), false},
		{"a folder under it", inside, true},
		{"a sibling that only shares its prefix", strings.TrimSuffix(filepath.Clean(os.TempDir()), string(os.PathSeparator)) + "-other", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := insideTempDir(tt.path); got != tt.want {
				t.Fatalf("insideTempDir(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
