// Package claudesourcetest gives tests the folders claudesource treats specially.
package claudesourcetest

import (
	"os"
	"path/filepath"
	"strings"
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
	// Linux reads XDG_CONFIG_HOME first. One a fixture already moved into the
	// temp area is throwaway and stays; any other value is the developer's own.
	if xdg := os.Getenv("XDG_CONFIG_HOME"); !insideTempDir(xdg) {
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

func insideTempDir(path string) bool {
	if path == "" {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	tmp := os.TempDir()
	if resolved, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = resolved
	}
	rel, err := filepath.Rel(tmp, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
