package ledger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const infoAttrsRel = ".git/info/attributes"

func TestEnsureMergeAttributes_CreatesFileWhenMissing(t *testing.T) {
	dir := t.TempDir()

	changed, err := EnsureMergeAttributes(dir)
	if err != nil {
		t.Fatalf("EnsureMergeAttributes: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true on initial write")
	}

	data, err := os.ReadFile(filepath.Join(dir, infoAttrsRel))
	if err != nil {
		t.Fatalf("read info/attributes: %v", err)
	}
	got := string(data)

	for _, want := range []string{
		mergeAttrsHeader,
		mergeAttrsFooter,
		"AGENTS.md",
		"merge=union",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, got)
		}
	}
}

func TestEnsureMergeAttributes_NotInWorkingTree(t *testing.T) {
	// .git/info/attributes is per-clone metadata; it must NOT appear in
	// the working tree as a tracked file. Verifies the chosen path keeps
	// the user's git-lfs install and any user-authored .gitattributes
	// untouched.
	dir := t.TempDir()
	if _, err := EnsureMergeAttributes(dir); err != nil {
		t.Fatalf("EnsureMergeAttributes: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitattributes")); !os.IsNotExist(err) {
		t.Errorf("EnsureMergeAttributes must NOT create a tracked .gitattributes; got err=%v", err)
	}
}

func TestEnsureMergeAttributes_Idempotent(t *testing.T) {
	dir := t.TempDir()

	if _, err := EnsureMergeAttributes(dir); err != nil {
		t.Fatalf("first call: %v", err)
	}
	changed, err := EnsureMergeAttributes(dir)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if changed {
		t.Errorf("expected changed=false on second call")
	}
}

func TestEnsureMergeAttributes_PreservesExistingContent(t *testing.T) {
	// If a user (or git itself, on rare occasion) wrote into info/attributes
	// already, our managed block must not clobber their lines.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git/info"), 0755); err != nil {
		t.Fatal(err)
	}
	userLine := "*.bin binary\n"
	if err := os.WriteFile(filepath.Join(dir, infoAttrsRel), []byte(userLine), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureMergeAttributes(dir); err != nil {
		t.Fatalf("EnsureMergeAttributes: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, infoAttrsRel))
	got := string(data)
	if !strings.Contains(got, userLine) {
		t.Errorf("user content %q was lost; got:\n%s", userLine, got)
	}
	if !strings.Contains(got, "merge=union") {
		t.Errorf("managed block missing; got:\n%s", got)
	}
}

func TestEnsureMergeAttributes_ReplacesManagedBlock(t *testing.T) {
	// Stale ox-managed content must be rewritten; user content outside
	// the markers must survive.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git/info"), 0755); err != nil {
		t.Fatal(err)
	}
	stale := mergeAttrsHeader + "\nAGENTS.md merge=stale-driver\n" + mergeAttrsFooter + "\n"
	userLine := "*.bin binary\n"
	if err := os.WriteFile(filepath.Join(dir, infoAttrsRel), []byte(userLine+stale), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureMergeAttributes(dir); err != nil {
		t.Fatalf("EnsureMergeAttributes: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, infoAttrsRel))
	got := string(data)
	if strings.Contains(got, "stale-driver") {
		t.Errorf("stale managed content not replaced; got:\n%s", got)
	}
	if !strings.Contains(got, userLine) {
		t.Errorf("user content lost; got:\n%s", got)
	}
	if !strings.Contains(got, "merge=union") {
		t.Errorf("canonical managed block missing; got:\n%s", got)
	}
}

func TestEnsureMergeAttributes_HandlesMissingFooter(t *testing.T) {
	// Truncated managed block (header but no footer) must be replaced
	// rather than infinitely growing on each Ensure call.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git/info"), 0755); err != nil {
		t.Fatal(err)
	}
	corrupted := mergeAttrsHeader + "\nAGENTS.md merge=truncated\n"
	if err := os.WriteFile(filepath.Join(dir, infoAttrsRel), []byte(corrupted), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureMergeAttributes(dir); err != nil {
		t.Fatalf("EnsureMergeAttributes: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, infoAttrsRel))
	got := string(data)
	if strings.Contains(got, "merge=truncated") {
		t.Errorf("truncated managed block not replaced; got:\n%s", got)
	}
	if !strings.Contains(got, mergeAttrsFooter) {
		t.Errorf("footer not restored; got:\n%s", got)
	}
}

func TestEnsureMergeAttributes_EmptyPath(t *testing.T) {
	if _, err := EnsureMergeAttributes(""); err == nil {
		t.Errorf("expected error on empty path")
	}
}
