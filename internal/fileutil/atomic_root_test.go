//go:build !windows

package fileutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteBytesInRoot_RejectsSymlinkRedirects(t *testing.T) {
	for _, parentLink := range []bool{false, true} {
		for _, relative := range []bool{false, true} {
			t.Run("redirect", func(t *testing.T) {
				dir, outside := t.TempDir(), t.TempDir()
				target := filepath.Join(outside, "settings.json")
				if err := os.WriteFile(target, []byte("private"), 0600); err != nil {
					t.Fatal(err)
				}
				root, err := os.OpenRoot(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				link, linkTarget, name := "settings.json", target, "settings.json"
				if parentLink {
					link, linkTarget, name = ".claude", outside, ".claude/settings.json"
					// Simulate a safe parent replaced after a caller's read/check.
					if err := root.Mkdir(".claude", 0700); err != nil {
						t.Fatal(err)
					}
					if err := root.Rename(".claude", "original"); err != nil {
						t.Fatal(err)
					}
				}
				if relative {
					linkTarget, err = filepath.Rel(dir, linkTarget)
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(linkTarget, filepath.Join(dir, link)); err != nil {
					t.Fatal(err)
				}
				if err := AtomicWriteBytesInRoot(root, name, []byte("modified"), 0644); err == nil {
					t.Fatal("expected symlink escape to be rejected")
				}
				data, err := os.ReadFile(target)
				if err != nil || string(data) != "private" {
					t.Fatalf("external content changed: %q, %v", data, err)
				}
				info, err := os.Stat(target)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("external permissions changed: %v, %v", info, err)
				}
				entries, err := os.ReadDir(outside)
				if err != nil || len(entries) != 1 {
					t.Fatalf("unexpected external files: %v, %v", entries, err)
				}
			})
		}
	}
}

func TestAtomicWriteBytesInRoot_InternalLinkPreservesMode(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.WriteFile("AGENTS.md", []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink("AGENTS.md", "CLAUDE.md"); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteBytesInRoot(root, "CLAUDE.md", []byte("updated"), 0644); err != nil {
		t.Fatal(err)
	}
	data, err := root.ReadFile("AGENTS.md")
	if err != nil || string(data) != "updated" {
		t.Fatalf("target not updated: %q, %v", data, err)
	}
	info, err := root.Stat("AGENTS.md")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("permissions not preserved: %v, %v", info, err)
	}
	link, err := root.Readlink("CLAUDE.md")
	if err != nil || link != "AGENTS.md" {
		t.Fatalf("link not preserved: %q, %v", link, err)
	}
}
