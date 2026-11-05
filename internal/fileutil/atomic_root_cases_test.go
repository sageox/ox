//go:build !windows

package fileutil

import (
	"os"
	"strings"
	"syscall"
	"testing"
)

func openTestRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root, dir
}

func TestAtomicWriteBytesInRoot_CreatesNewFileWithPermAndNoTempLeftovers(t *testing.T) {
	root, _ := openTestRoot(t)
	if err := root.Mkdir(".claude", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteBytesInRoot(root, ".claude/settings.json", []byte("{}"), 0o640); err != nil {
		t.Fatal(err)
	}
	data, err := root.ReadFile(".claude/settings.json")
	if err != nil || string(data) != "{}" {
		t.Fatalf("new file: %q, %v", data, err)
	}
	if info, err := root.Stat(".claude/settings.json"); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("new file mode: %v, %v", info, err)
	}
	entries, err := os.ReadDir(root.Name() + "/.claude")
	if err != nil || len(entries) != 1 {
		t.Fatalf("leftover temp files: %v, %v", entries, err)
	}
}

// A relative link chain that stays inside root is followed to its final
// target; a chain that leaves root at any hop fails closed.
func TestAtomicWriteBytesInRoot_FollowsInRootChainsOnly(t *testing.T) {
	root, _ := openTestRoot(t)
	if err := root.Mkdir("docs", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile("docs/AGENTS.md", []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"CLAUDE.md": "GEMINI.md", "GEMINI.md": "docs/AGENTS.md", "ESCAPE.md": "docs/../../outside.md"} {
		if err := root.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	if err := AtomicWriteBytesInRoot(root, "CLAUDE.md", []byte("updated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, _ := root.ReadFile("docs/AGENTS.md"); string(data) != "updated" {
		t.Fatalf("chain target not updated: %q", data)
	}
	if err := AtomicWriteBytesInRoot(root, "ESCAPE.md", []byte("x"), 0o600); err == nil {
		t.Fatal("expected a link escaping root to be rejected")
	}
}

func TestAtomicWriteBytesInRoot_RejectsSymlinkLoops(t *testing.T) {
	root, _ := openTestRoot(t)
	if err := root.Symlink("b", "a"); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink("a", "b"); err != nil {
		t.Fatal(err)
	}
	err := AtomicWriteBytesInRoot(root, "a", []byte("x"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "too many symlinks") {
		t.Fatalf("expected symlink loop refusal, got %v", err)
	}
}

func TestAtomicWriteBytesInRoot_RefusesReadOnlyAndNonRegularTargets(t *testing.T) {
	root, dir := openTestRoot(t)
	if err := root.WriteFile("locked.md", []byte("keep"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := root.Mkdir("adir", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(dir+"/pipe", 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"locked.md", "adir", "pipe"} {
		if err := AtomicWriteBytesInRoot(root, name, []byte("x"), 0o644); err == nil {
			t.Errorf("%s: expected refusal", name)
		}
	}
	if data, _ := root.ReadFile("locked.md"); string(data) != "keep" {
		t.Fatalf("read-only file replaced: %q", data)
	}
}

func TestAtomicWriteBytesInRoot_MissingParentFails(t *testing.T) {
	root, _ := openTestRoot(t)
	if err := AtomicWriteBytesInRoot(root, "missing/settings.json", []byte("x"), 0o644); err == nil {
		t.Fatal("expected a missing parent to fail")
	}
}
