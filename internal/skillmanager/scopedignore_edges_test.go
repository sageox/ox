package skillmanager

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureScopedIgnoreFilesForDirs_ReportsEveryDirectoryItCouldNotProtect.
//
// Apply refuses to materialize into an unprotected directory, so this list is the
// input to that decision. A directory silently omitted from it becomes a
// directory ox fills with reserved-prefix files that git can still see.
func TestEnsureScopedIgnoreFilesForDirs_ReportsEveryDirectoryItCouldNotProtect(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()

	// .claude: the whole agent directory is a symlink out of the repository.
	if err := os.Symlink(outside, filepath.Join(repo, ".claude")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// .agents: the directory is fine, its .gitignore is a symlink out.
	agents := filepath.Join(repo, ".agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	victim := filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, []byte("theirs\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(victim, filepath.Join(agents, ".gitignore")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// .factory: a plain FILE where a directory should be.
	if err := os.WriteFile(filepath.Join(repo, ".factory"), []byte("not a dir\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, unprotected, err := EnsureScopedIgnoreFilesForDirs(repo, nil)
	if err != nil {
		t.Fatalf("EnsureScopedIgnoreFilesForDirs: %v", err)
	}

	got := map[string]bool{}
	for _, d := range unprotected {
		got[d] = true
	}
	for _, want := range []string{".claude", ".agents", ".factory"} {
		if !got[want] {
			t.Errorf("%s was not reported as unprotected; Apply would materialize into it: %v", want, unprotected)
		}
	}

	// And nothing was written through any of those paths.
	if data, err := os.ReadFile(victim); err == nil && string(data) != "theirs\n" {
		t.Errorf("ox wrote through the symlinked .gitignore:\n%s", data)
	}
}

// TestEnsureScopedIgnoreFilesForDirs_ForceCreatesOnlyTheNamedDirectories: Apply
// writes the rule BEFORE the files exist, so it must be able to create the
// directory it is about to fill — and only that one. Creating the rest would add
// vendor footprint for agents the project never selected.
func TestEnsureScopedIgnoreFilesForDirs_ForceCreatesOnlyTheNamedDirectories(t *testing.T) {
	repo := t.TempDir()

	written, unprotected, err := EnsureScopedIgnoreFilesForDirs(repo, map[string]bool{".agents": true})
	if err != nil {
		t.Fatalf("EnsureScopedIgnoreFilesForDirs: %v", err)
	}
	if len(unprotected) != 0 {
		t.Errorf("a directory ox created was reported unprotected: %v", unprotected)
	}
	if len(written) != 1 || written[0].Rel != filepath.Join(".agents", ".gitignore") {
		t.Fatalf("expected exactly .agents/.gitignore, got %v", written)
	}
	for _, dir := range []string{".claude", ".factory"} {
		if _, err := os.Stat(filepath.Join(repo, dir)); err == nil {
			t.Errorf("ox created %s/ for an agent the project never selected", dir)
		}
	}
}

// TestEnsureScopedIgnoreFiles_MissingRepoRootIsAnError: a caller that lost its
// repository must be told, not silently written somewhere else.
func TestEnsureScopedIgnoreFiles_MissingRepoRootIsAnError(t *testing.T) {
	if _, err := EnsureScopedIgnoreFiles(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("a missing repository root was accepted")
	}
}

// TestScopedIgnoreFiles_CoversTheRuleNameWithNoTrailingHyphen pins the miss that
// was silent: "rules/ox-cli-*" does not match "ox-cli.md", so the primary rule
// file needed its own exact pattern. Without it that one file appeared in every
// pull request.
func TestScopedIgnoreFiles_CoversTheRuleNameWithNoTrailingHyphen(t *testing.T) {
	for _, f := range ScopedIgnoreFiles() {
		if f.Dir == ".agents" {
			continue // skills-only projection; it has no rules root
		}
		var exact bool
		for _, e := range f.Entries {
			if e == "rules/"+CLIBase+".md" {
				exact = true
			}
		}
		if !exact {
			t.Errorf("%s has no exact rule for %s.md; the glob alone does not match it: %v", f.Dir, CLIBase, f.Entries)
		}
	}
}
