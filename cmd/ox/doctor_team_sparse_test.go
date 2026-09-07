//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMissingSparseTopLevelDirs_CatchesTheManifestOmission reproduces GH #862.
//
// The tracked sync manifest omitted `agents/`, so sparse-checkout never
// materialized it and team rules never reached any client. The existing check
// looked only for the `/*` and `!/*/` root patterns — which were present and
// correct — so it reported everything fine while the content was simply absent.
//
// The only reliable signal is comparing what HEAD contains against what is on
// disk: a top-level directory in the commit but not in the working tree was
// excluded by the sparse spec. That catches an omission nobody anticipated,
// which a pattern check by construction cannot.
func TestMissingSparseTopLevelDirs_CatchesTheManifestOmission(t *testing.T) {
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}

	origin := t.TempDir()
	run(origin, "init", "--bare", "--initial-branch=main")

	seed := t.TempDir()
	run(seed, "init", "--initial-branch=main")
	run(seed, "config", "user.email", "t@example.com")
	run(seed, "config", "user.name", "T")
	for _, rel := range []string{"agents/rules/team.md", "memory/MEMORY.md", "README.md"} {
		p := filepath.Join(seed, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	run(seed, "add", "-A")
	run(seed, "commit", "-q", "-m", "seed")
	run(seed, "remote", "add", "origin", origin)
	run(seed, "push", "-q", "origin", "main")

	// Clone with a sparse spec that includes `memory/` but OMITS `agents/` —
	// exactly the shape of the manifest bug.
	clone := filepath.Join(t.TempDir(), "team-context")
	run(filepath.Dir(clone), "clone", "-q", "--no-checkout", origin, "team-context")
	// --no-cone so the spec is written as raw patterns, which is what the real
	// manifest produces; cone mode rejects leading-slash patterns.
	run(clone, "sparse-checkout", "init", "--no-cone")
	run(clone, "sparse-checkout", "set", "--no-cone", "/*", "!/*/", "/memory/")
	run(clone, "checkout", "-q", "main")

	missing := missingSparseTopLevelDirs(clone)
	joined := strings.Join(missing, ",")
	if !strings.Contains(joined, "agents/") {
		t.Errorf("the omitted agents/ directory was not detected; team rules would silently never materialize. got %v", missing)
	}
	if strings.Contains(joined, "memory/") {
		t.Errorf("an included directory was reported missing: %v", missing)
	}
}

// TestMissingSparseTopLevelDirs_CleanCheckoutReportsNothing is the control:
// without it, a detector that reported every directory as missing would pass the
// test above and flag every healthy team context forever.
func TestMissingSparseTopLevelDirs_CleanCheckoutReportsNothing(t *testing.T) {
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	repo := t.TempDir()
	run(repo, "init", "--initial-branch=main")
	run(repo, "config", "user.email", "t@example.com")
	run(repo, "config", "user.name", "T")
	p := filepath.Join(repo, "agents", "rules", "team.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run(repo, "add", "-A")
	run(repo, "commit", "-q", "-m", "seed")

	if missing := missingSparseTopLevelDirs(repo); len(missing) != 0 {
		t.Errorf("a fully materialized checkout reported missing directories: %v", missing)
	}
}

// TestMissingSparseTopLevelDirs_UntrackedContentDoesNotMaskTheOmission:
// directory PRESENCE is not evidence the content materialized. An excluded
// directory can exist purely because of untracked local files beside it, and a
// presence check would then report everything fine while every tracked file under
// it is still absent — the same silent failure as #862, one layer down.
func TestMissingSparseTopLevelDirs_UntrackedContentDoesNotMaskTheOmission(t *testing.T) {
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}

	origin := t.TempDir()
	run(origin, "init", "--bare", "--initial-branch=main")
	seed := t.TempDir()
	run(seed, "init", "--initial-branch=main")
	run(seed, "config", "user.email", "t@example.com")
	run(seed, "config", "user.name", "T")
	for _, rel := range []string{"agents/rules/team.md", "README.md"} {
		p := filepath.Join(seed, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	run(seed, "add", "-A")
	run(seed, "commit", "-q", "-m", "seed")
	run(seed, "remote", "add", "origin", origin)
	run(seed, "push", "-q", "origin", "main")

	clone := filepath.Join(t.TempDir(), "team-context")
	run(filepath.Dir(clone), "clone", "-q", "--no-checkout", origin, "team-context")
	run(clone, "sparse-checkout", "init", "--no-cone")
	run(clone, "sparse-checkout", "set", "--no-cone", "/*", "!/*/")
	run(clone, "checkout", "-q", "main")

	// The directory exists locally, but only because of an UNTRACKED file.
	if err := os.MkdirAll(filepath.Join(clone, "agents"), 0o755); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(clone, "agents", "local-scratch.md"), []byte("mine\n"), 0o644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}

	missing := missingSparseTopLevelDirs(clone)
	if !strings.Contains(strings.Join(missing, ","), "agents/") {
		t.Errorf("untracked local content masked the omission; team rules would silently never materialize. got %v", missing)
	}
}
