package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHandleDetectRecognizesExplicitCursorEnvironment(t *testing.T) {
	t.Setenv("AGENT_ENV", "cursor")
	got, err := handleDetect()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Detected || got.Reason != "AGENT_ENV=cursor" {
		t.Fatalf("detect = %#v, want explicit Cursor detection", got)
	}
}

func TestHandleDetectRecognizesNativeRootAndDesktopCLI(t *testing.T) {
	origHome, origLookPath, origApps := cursorUserHomeDir, cursorLookPath, cursorAppPaths
	t.Cleanup(func() { cursorUserHomeDir, cursorLookPath, cursorAppPaths = origHome, origLookPath, origApps })
	t.Setenv("AGENT_ENV", "")
	home := t.TempDir()
	cursorUserHomeDir = func() (string, error) { return home, nil }
	cursorAppPaths = func(string) []string { return nil }
	cursorLookPath = func(string) (string, error) { return "", errors.New("not found") }

	if err := os.MkdirAll(filepath.Join(home, ".cursor", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := handleDetect()
	if err != nil || !got.Detected || got.Reason != "found ~/.cursor/projects/" {
		t.Fatalf("native-root detection = %#v, %v", got, err)
	}

	if err := os.RemoveAll(filepath.Join(home, ".cursor")); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(t.TempDir(), "Cursor.app")
	if err := os.Mkdir(app, 0o755); err != nil {
		t.Fatal(err)
	}
	cursorAppPaths = func(string) []string { return []string{app} }
	got, err = handleDetect()
	if err != nil || !got.Detected || got.Reason != "found Cursor desktop application" {
		t.Fatalf("desktop detection = %#v, %v", got, err)
	}

	cursorAppPaths = func(string) []string { return nil }
	cursorLookPath = func(name string) (string, error) { return "/tmp/cursor", nil }
	got, err = handleDetect()
	if err != nil || !got.Detected || got.Reason != "cursor binary found in PATH" {
		t.Fatalf("CLI detection = %#v, %v", got, err)
	}
}
