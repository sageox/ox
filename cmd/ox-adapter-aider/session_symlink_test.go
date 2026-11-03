package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterruntime"
)

func TestAiderHistoryRejectsEscapingSymlinks(t *testing.T) {
	for _, kind := range []string{"absolute", "relative", "intermediate"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			repo := filepath.Join(parent, "repo")
			// A sibling with a shared path prefix must not count as contained.
			outside := filepath.Join(parent, "repo-private")
			for _, dir := range []string{filepath.Join(repo, ".sageox"), outside} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			const privateText = "private local notes without an Aider header"
			target := filepath.Join(outside, "notes.md")
			if err := os.WriteFile(target, []byte(privateText), 0o600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "relative":
				target = filepath.Join("..", "repo-private", "notes.md")
			case "intermediate":
				if err := os.Symlink(outside, filepath.Join(repo, "linked-dir")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				target = filepath.Join("linked-dir", "notes.md")
			}
			history := filepath.Join(repo, aiderHistoryFile)
			if err := os.Symlink(target, history); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}

			// The external file is recent enough for stop-time discovery. Exercise
			// the real dispatch table, including the full read used by CLI stop.
			since := time.Now().Add(-5 * time.Minute).Format(time.RFC3339)
			for _, args := range [][]string{
				{"find-session", "--repo-root", repo, "--since", since},
				{"read", "--session-file", history},
				{"read-from-offset", "--session-file", history, "--offset", "0"},
				{"import-session", "--repo-root", repo, "--session-id", "2024-01-15 14:30:45"},
			} {
				t.Run(args[0], func(t *testing.T) {
					var out bytes.Buffer
					if err := adapterruntime.RunWithArgs(newConfig(), args, nil, &out); err == nil {
						t.Errorf("accepted escaping history symlink: %s", out.String())
					}
					if strings.Contains(out.String(), privateText) {
						t.Error("external content escaped into adapter output")
					}
				})
			}
			// Timestamp recovery also opens the history file independently.
			if err := os.WriteFile(filepath.Join(outside, "notes.md"), []byte("# aider chat started at 2024-01-15 14:30:45\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if ts := resolveLatestSessionTS(history, 0); !ts.IsZero() {
				t.Errorf("read a timestamp outside the workspace: %v", ts)
			}
		})
	}
}

func TestAiderHistoryAllowsContainedSymlinkAndLinkedWorkspace(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "history.md"), []byte("workspace text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("history.md", filepath.Join(repo, aiderHistoryFile)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	linkedRepo := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(repo, linkedRepo); err != nil {
		t.Fatal(err)
	}
	path, err := findAiderSession(linkedRepo, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(linkedRepo, aiderHistoryFile) {
		t.Fatalf("discovery must preserve the workspace-rooted path, got %q", path)
	}
	entries, err := readAiderFile(path)
	if err != nil || len(entries) != 1 || entries[0].Content != "workspace text" {
		t.Fatalf("contained history was not read: entries=%v, error=%v", entries, err)
	}
}

func TestAiderHistoryReadKeepsOpenedFile(t *testing.T) {
	repo := t.TempDir()
	history := filepath.Join(repo, aiderHistoryFile)
	if err := os.WriteFile(history, []byte("workspace text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := openAiderFile(history)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	outside := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(outside, []byte("private local notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(history, filepath.Join(repo, "old-history.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, history); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	entries, err := parseAiderMarkdown(bufio.NewScanner(f), time.Time{})
	if err != nil || len(entries) != 1 || entries[0].Content != "workspace text" {
		t.Fatalf("read did not retain the validated handle: entries=%v, error=%v", entries, err)
	}
	if entries, err := readAiderFile(history); err == nil || len(entries) != 0 {
		t.Fatalf("reopening a replaced path must fail: entries=%v, error=%v", entries, err)
	}
}
