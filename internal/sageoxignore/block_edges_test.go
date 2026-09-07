package sageoxignore

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeIgnore(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".gitignore")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

// TestEnsureBlock_TruncatedBlockIsLeftVisibleNotSpliced.
//
// A begin marker with no end marker means the block was truncated or hand-edited.
// Guessing where it ended and splicing there would silently eat whatever the user
// wrote after it. Appending a fresh, complete block is recoverable and leaves the
// damage visible for a human to resolve.
func TestEnsureBlock_TruncatedBlockIsLeftVisibleNotSpliced(t *testing.T) {
	userRule := "keepme.txt\n"
	p := writeIgnore(t, BlockBegin+"\nskills/old-*/\n"+userRule)

	changed, _, err := EnsureBlock(p, []string{"skills/ox-cli-*/"})
	if err != nil {
		t.Fatalf("EnsureBlock: %v", err)
	}
	if !changed {
		t.Fatal("a damaged block was treated as current")
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(got), userRule) {
		t.Errorf("ox ate a user rule that followed a truncated block:\n%s", got)
	}
	if !strings.Contains(string(got), "skills/ox-cli-*/") {
		t.Errorf("no complete block was appended:\n%s", got)
	}
}

// TestEnsureBlock_RepairsAMissingTrailingNewline: without this, the user's last
// rule and ox's first comment merge into one line, silently changing what that
// rule matches.
func TestEnsureBlock_RepairsAMissingTrailingNewline(t *testing.T) {
	p := writeIgnore(t, "no-trailing-newline.txt")

	if _, _, err := EnsureBlock(p, []string{"skills/ox-cli-*/"}); err != nil {
		t.Fatalf("EnsureBlock: %v", err)
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(got), "no-trailing-newline.txt"+BlockBegin) {
		t.Errorf("the user's last rule was merged with ox's block header:\n%s", got)
	}
	if !strings.HasPrefix(string(got), "no-trailing-newline.txt\n") {
		t.Errorf("the user's rule was altered:\n%s", got)
	}
}

// TestEnsureBlock_RewritesOnlyBetweenTheMarkers: gitignore ordering is
// semantically significant, so everything outside the markers must survive
// byte-identical, in place.
func TestEnsureBlock_RewritesOnlyBetweenTheMarkers(t *testing.T) {
	before := "before.txt\n!keep/\n\n"
	after := "\nafter.txt\n"
	p := writeIgnore(t, before+BlockBegin+"\nstale-entry\n"+BlockEnd+"\n"+after)

	if _, _, err := EnsureBlock(p, []string{"skills/ox-cli-*/"}); err != nil {
		t.Fatalf("EnsureBlock: %v", err)
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(got)
	if !strings.HasPrefix(s, before) {
		t.Errorf("content before the block changed:\n%s", s)
	}
	if !strings.HasSuffix(s, after) {
		t.Errorf("content after the block changed:\n%s", s)
	}
	if strings.Contains(s, "stale-entry") {
		t.Errorf("the stale entry inside the block survived:\n%s", s)
	}
}

// TestEnsureBlock_CreatedFlagDistinguishesNewFromExisting drives the init
// rollback decision: a file ox created is deleted on rollback, one that already
// existed is restored. Getting this backwards deletes the user's rules.
func TestEnsureBlock_CreatedFlagDistinguishesNewFromExisting(t *testing.T) {
	fresh := filepath.Join(t.TempDir(), ".gitignore")
	_, created, err := EnsureBlock(fresh, []string{"skills/ox-cli-*/"})
	if err != nil {
		t.Fatalf("EnsureBlock: %v", err)
	}
	if !created {
		t.Error("a file that did not exist was not reported as created")
	}

	p := writeIgnore(t, "mine.txt\n")
	_, created, err = EnsureBlock(p, []string{"skills/ox-cli-*/"})
	if err != nil {
		t.Fatalf("EnsureBlock: %v", err)
	}
	if created {
		t.Error("an existing file was reported as created; rollback would delete the user's rules")
	}
}

// TestEnsureBlock_UnreadableFileIsAnErrorNotASilentOverwrite: treating a read
// failure as "empty" would rewrite the file with only ox's block, destroying
// every rule in it.
func TestEnsureBlock_UnreadableFileIsAnErrorNotASilentOverwrite(t *testing.T) {
	// Windows: Go's Chmod maps only the read-only bit, so 0o000 does NOT make a
	// file unreadable and the read below would succeed — this test would fail
	// there while asserting nothing. os.Geteuid also returns -1 rather than 0 on
	// Windows, so the root check alone never caught it.
	if runtime.GOOS == "windows" {
		t.Skip("windows: chmod cannot make a file unreadable")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not apply")
	}
	p := writeIgnore(t, "mine.txt\n")
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })

	if _, _, err := EnsureBlock(p, []string{"skills/ox-cli-*/"}); err == nil {
		t.Fatal("an unreadable ignore file was not reported as an error")
	}

	_ = os.Chmod(p, 0o644)
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "mine.txt\n" {
		t.Errorf("ox overwrote a file it could not read:\n%s", got)
	}
}

// TestRemoveBlock_LeavesEverythingElseByteIdentical is the uninstall contract.
func TestRemoveBlock_LeavesEverythingElseByteIdentical(t *testing.T) {
	original := "a.txt\n!keep/\nb/\n"
	p := writeIgnore(t, original)
	if _, _, err := EnsureBlock(p, []string{"skills/ox-cli-*/"}); err != nil {
		t.Fatalf("EnsureBlock: %v", err)
	}

	removed, err := RemoveBlock(p)
	if err != nil {
		t.Fatalf("RemoveBlock: %v", err)
	}
	if !removed {
		t.Fatal("RemoveBlock reported no block to remove")
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != original {
		t.Errorf("uninstall did not restore the file byte-for-byte:\ngot  %q\nwant %q", got, original)
	}
}

// TestRemoveBlock_MissingFileAndMissingBlockAreNotErrors: uninstall runs on
// repositories ox may never have touched.
func TestRemoveBlock_MissingFileAndMissingBlockAreNotErrors(t *testing.T) {
	absent := filepath.Join(t.TempDir(), ".gitignore")
	removed, err := RemoveBlock(absent)
	if err != nil || removed {
		t.Errorf("missing file: removed=%v err=%v", removed, err)
	}

	p := writeIgnore(t, "only-user-rules.txt\n")
	removed, err = RemoveBlock(p)
	if err != nil || removed {
		t.Errorf("no ox block: removed=%v err=%v", removed, err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "only-user-rules.txt\n" {
		t.Errorf("uninstall modified a file with no ox block:\n%s", got)
	}
}

// IsManagedOnly is used as OWNERSHIP PROOF before the migration adopts an
// otherwise untracked .gitignore into a commit it makes on the user's behalf. It
// has to be stricter than "contains a valid block": a single line of theirs
// outside the markers means those bytes are not ox's to commit.
func TestIsManagedOnly_RequiresNothingOutsideTheMarkers(t *testing.T) {
	entries := []string{"skills/ox-cli-*/", "rules/ox-cli.md"}

	p := filepath.Join(t.TempDir(), ".gitignore")
	if _, _, err := EnsureBlock(p, entries); err != nil {
		t.Fatalf("EnsureBlock: %v", err)
	}
	generated, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !IsManagedOnly(generated, entries) {
		t.Fatal("freshly generated content was not recognized as managed-only")
	}

	for name, content := range map[string][]byte{
		"a user rule before the block": append([]byte("mine.txt\n"), generated...),
		"a user rule after the block":  append(append([]byte{}, generated...), []byte("mine.txt\n")...),
		"different entries":            []byte(string(generated) + ""),
		"empty":                        nil,
		"no block at all":              []byte("just.txt\n"),
	} {
		if name == "different entries" {
			if !IsManagedOnly(content, entries) {
				t.Errorf("%s: identical content was rejected", name)
			}
			continue
		}
		if IsManagedOnly(content, entries) {
			t.Errorf("%s: would be adopted into a commit on the user's behalf", name)
		}
	}

	// The same block rendered for a DIFFERENT entry set is not ours either.
	if IsManagedOnly(generated, []string{"skills/other-*/"}) {
		t.Error("a block for different entries was accepted as managed-only")
	}
}

// TestEnsureBlock_CRLFFileConvergesInsteadOfRewritingForever.
//
// On Windows with core.autocrlf=true every checkout rewrites the committed
// .gitignore with CRLF line endings. If EnsureBlock compared against its own LF
// rendering and rewrote on every mismatch, prime would modify a TRACKED file at
// every session start on those machines — permanent, invisible churn in exactly
// the file this design added to stop churn.
func TestEnsureBlock_CRLFFileConvergesInsteadOfRewritingForever(t *testing.T) {
	entries := []string{"skills/ox-cli-*/", "rules/ox-cli.md"}
	p := writeIgnore(t, "mine.txt\n")
	if _, _, err := EnsureBlock(p, entries); err != nil {
		t.Fatalf("EnsureBlock: %v", err)
	}
	lf, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// Simulate the checkout: every newline becomes CRLF.
	crlf := strings.ReplaceAll(string(lf), "\n", "\r\n")
	if err := os.WriteFile(p, []byte(crlf), 0o644); err != nil {
		t.Fatalf("write crlf: %v", err)
	}

	first, _, err := EnsureBlock(p, entries)
	if err != nil {
		t.Fatalf("EnsureBlock on CRLF: %v", err)
	}
	afterFirst, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// Whatever it decided the first time, a SECOND call must be a no-op. A writer
	// that keeps disagreeing with its own output never settles.
	second, _, err := EnsureBlock(p, entries)
	if err != nil {
		t.Fatalf("EnsureBlock second: %v", err)
	}
	afterSecond, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if second {
		t.Errorf("the writer still reported a change on the second pass after a CRLF checkout (first=%v); "+
			"every session start would modify a tracked file", first)
	}
	if string(afterFirst) != string(afterSecond) {
		t.Error("the file changed again on the second pass; the writer does not converge")
	}
	if !strings.Contains(string(afterSecond), "mine.txt") {
		t.Errorf("the user's rule was lost through the CRLF round trip:\n%q", afterSecond)
	}
}

// TestEnsureBlock_TwoBlocksFromABadMergeStillConverge.
//
// A merge that takes both sides of a conflicted .gitignore leaves two complete ox
// blocks. git honors duplicate ignore rules, so nothing looks broken — but the
// writer must still settle rather than fight itself forever, and it must never
// delete the user's rules while sorting it out.
func TestEnsureBlock_TwoBlocksFromABadMergeStillConverge(t *testing.T) {
	entries := []string{"skills/ox-cli-*/"}
	one := renderBlock(entries)
	p := writeIgnore(t, "mine.txt\n\n"+one+"\n"+one)

	if _, _, err := EnsureBlock(p, entries); err != nil {
		t.Fatalf("EnsureBlock: %v", err)
	}
	changed, _, err := EnsureBlock(p, entries)
	if err != nil {
		t.Fatalf("EnsureBlock second: %v", err)
	}
	if changed {
		t.Error("the writer never settles on a file carrying two ox blocks")
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(got), "mine.txt") {
		t.Errorf("the user's rule was lost while reconciling duplicate blocks:\n%s", got)
	}
}
