package sageoxignore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

var oxEntries = []string{"skills/ox-cli-*/", "rules/ox-cli-*", "skills/sageox-team-*/"}

// TestEnsureBlock_RepeatedCallsAreByteIdentical is the regression that motivated
// a marked block instead of line appends.
//
// EnsureEntry skips comment lines when deciding whether an entry is present, so a
// header comment reports "missing" on every call and gets appended again. This
// file is COMMITTED and the writer runs at prime, at doctor, and on the daemon's
// 30-minute tick — so the ignore file would grow a line per session and reintroduce
// exactly the pull-request churn the whole mechanism exists to remove.
//
// Three runs, not two: an off-by-one in block replacement typically stabilizes
// after the first rewrite and only diverges on the next one.
func TestEnsureBlock_RepeatedCallsAreByteIdentical(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitignore")

	changed, created, err := EnsureBlock(path, oxEntries)
	if err != nil {
		t.Fatalf("first EnsureBlock: %v", err)
	}
	if !changed || !created {
		t.Fatalf("first call should create and change: changed=%v created=%v", changed, created)
	}
	first := read(t, path)

	for i := 2; i <= 3; i++ {
		changed, _, err := EnsureBlock(path, oxEntries)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if changed {
			t.Errorf("run %d reported a change for identical entries; the committed file would grow every session", i)
		}
		if got := read(t, path); got != first {
			t.Errorf("run %d changed the file:\n--- first ---\n%s\n--- run %d ---\n%s", i, first, i, got)
		}
	}
	if n := strings.Count(first, BlockBegin); n != 1 {
		t.Errorf("expected exactly one managed block, got %d", n)
	}
}

// TestEnsureBlock_NeverTouchesUserRules is the data-safety guarantee. Ordering in
// a .gitignore is semantically significant (a later negation overrides an earlier
// ignore), so reordering a user's rules can silently change which files git
// tracks — a far worse outcome than a duplicated line.
func TestEnsureBlock_NeverTouchesUserRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitignore")
	user := "# my rules\nnode_modules/\n*.log\n!important.log\nbuild/\n"
	write(t, path, user)

	if _, _, err := EnsureBlock(path, oxEntries); err != nil {
		t.Fatalf("EnsureBlock: %v", err)
	}
	got := read(t, path)

	if !strings.HasPrefix(got, user) {
		t.Errorf("user rules were modified or reordered.\n--- want prefix ---\n%s\n--- got ---\n%s", user, got)
	}
	// The negation must still follow its ignore rule, in the original order.
	if strings.Index(got, "*.log") > strings.Index(got, "!important.log") {
		t.Error("rule order inverted; a negation moved relative to its ignore rule")
	}
}

// TestEnsureBlock_UpdatesInPlaceWhenEntriesChange proves a later release can
// change the entry set without appending a second block or disturbing user rules.
func TestEnsureBlock_UpdatesInPlaceWhenEntriesChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitignore")
	write(t, path, "node_modules/\n")

	if _, _, err := EnsureBlock(path, []string{"skills/ox-cli-*/"}); err != nil {
		t.Fatalf("initial: %v", err)
	}
	changed, _, err := EnsureBlock(path, oxEntries)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !changed {
		t.Error("changing the entry set should report a change")
	}

	got := read(t, path)
	if n := strings.Count(got, BlockBegin); n != 1 {
		t.Errorf("entry-set change appended a second block (got %d)", n)
	}
	for _, e := range oxEntries {
		if !strings.Contains(got, e) {
			t.Errorf("missing entry %q after update", e)
		}
	}
	if !strings.HasPrefix(got, "node_modules/\n") {
		t.Error("user rule lost during in-place update")
	}
}

// TestEnsureBlock_AppendsCleanlyWithoutTrailingNewline: a file whose last rule
// lacks a trailing newline must not have that rule merged with our first line —
// which would silently destroy one of the user's ignore rules and create a
// nonsense one.
func TestEnsureBlock_AppendsCleanlyWithoutTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitignore")
	write(t, path, "build/")

	if _, _, err := EnsureBlock(path, oxEntries); err != nil {
		t.Fatalf("EnsureBlock: %v", err)
	}
	got := read(t, path)

	if !strings.Contains(got, "build/\n") {
		t.Errorf("existing rule was corrupted by the append: %q", got)
	}
	if strings.Contains(got, "build/#") {
		t.Errorf("rule merged with the block header: %q", got)
	}
}

// TestEnsureBlock_TruncatedBlockIsNotSpliced covers a hand-mangled file: a begin
// marker with no end marker. Guessing where the block ended risks eating user
// rules that follow it, so the writer appends a fresh complete block and leaves
// the damaged text visible instead.
func TestEnsureBlock_TruncatedBlockIsNotSpliced(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitignore")
	write(t, path, BlockBegin+"\nskills/ox-cli-*/\n# user deleted the end marker\nmy-secret/\n")

	if _, _, err := EnsureBlock(path, oxEntries); err != nil {
		t.Fatalf("EnsureBlock: %v", err)
	}
	got := read(t, path)

	if !strings.Contains(got, "my-secret/") {
		t.Error("a user rule after a truncated block was destroyed")
	}
	if !strings.Contains(got, BlockEnd) {
		t.Error("a complete, terminated block should have been appended")
	}
}

// TestRemoveBlock_LeavesUserRulesByteIdentical is the uninstall contract: ox
// removes what it wrote and nothing else.
func TestRemoveBlock_LeavesUserRulesByteIdentical(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitignore")
	user := "# mine\nnode_modules/\n!keep.log\n"
	write(t, path, user)

	if _, _, err := EnsureBlock(path, oxEntries); err != nil {
		t.Fatalf("EnsureBlock: %v", err)
	}
	removed, err := RemoveBlock(path)
	if err != nil {
		t.Fatalf("RemoveBlock: %v", err)
	}
	if !removed {
		t.Fatal("RemoveBlock reported nothing removed")
	}
	if got := read(t, path); got != user {
		t.Errorf("uninstall did not restore the file byte-for-byte.\n--- want ---\n%q\n--- got ---\n%q", user, got)
	}
}

// TestEnsureBlock_TruncatedBlockSurvivesRepeatedCalls is the second-call bug.
//
// A file with an orphaned begin marker gets a fresh complete block appended. On
// the NEXT call a naive "first begin, first end" search pairs the ORPHAN with the
// new block's end marker and replaces everything between them — silently deleting
// every user rule that sat after the damaged marker. The writer runs at init, at
// doctor, and on the daemon tick, so a second call is guaranteed.
func TestEnsureBlock_TruncatedBlockSurvivesRepeatedCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitignore")
	write(t, path, BlockBegin+"\nskills/ox-cli-*/\n# user deleted the end marker\nmy-secret/\nbuild/\n")

	for i := 1; i <= 3; i++ {
		if _, _, err := EnsureBlock(path, oxEntries); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		got := read(t, path)
		if !strings.Contains(got, "my-secret/") {
			t.Fatalf("run %d deleted a user rule that followed the damaged marker:\n%s", i, got)
		}
		if !strings.Contains(got, "build/") {
			t.Fatalf("run %d deleted a user rule that followed the damaged marker:\n%s", i, got)
		}
	}
}
