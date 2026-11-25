// Package claudesourcetest gives tests the folders claudesource treats specially.
package claudesourcetest

import (
	"os"
	"path/filepath"
	"testing"
)

// SharedNotesFolder creates and returns one session's shared notes folder, as
// the Claude desktop app lays it out, under a throwaway per-user config
// directory. The process environment points there for the rest of the test, so
// nothing reads or writes the real app's data folder.
func SharedNotesFolder(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// os.UserConfigDir reads a different variable on each OS
	t.Setenv("HOME", home)
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
	if os.Getenv("XDG_CONFIG_HOME") == "" {
		// a fixture that already moved it keeps its own; Linux reads it first
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	}
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(config, "Claude", "side-session-notes", "local_1")
	if err := os.MkdirAll(notes, 0o755); err != nil {
		t.Fatal(err)
	}
	if notes, err = filepath.EvalSymlinks(notes); err != nil {
		t.Fatal(err)
	}
	return notes
}
