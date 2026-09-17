package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/manifest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplySparseFromManifest_KeepsAgentsWhenManifestOmitsIt is the regression
// test for the bug that made every team rule vanish ~15 seconds after clone.
//
// The GH #862 floor was applied when the team context was cloned and when
// `ox doctor --fix` repaired it, but NOT on the daemon's every-tick re-apply.
// So on any team whose server-generated manifest omitted `agents/`, the clone
// materialized the directory and the next sync tick deleted it — taking every
// team rule and team skill with it, on every client, permanently, with no error
// anywhere. The two states are indistinguishable from the repository: an
// un-materialized directory and a team that published nothing look identical.
//
// Drive the real function against a real git repo, because the deletion is
// something git does in response to the spec we write — asserting on the
// computed path list alone would not have caught it.
func TestApplySparseFromManifest_KeepsAgentsWhenManifestOmitsIt(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo // never the developer's own repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(repo, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
	}

	git("init", "-q", "-b", "main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	git("config", "commit.gpgsign", "false") // the developer's global config may sign

	// A manifest that lists everything EXCEPT agents/ — the #862 shape.
	write(".sageox/sync.manifest", "version 1\ninclude .sageox/\ninclude docs/\n")
	write("docs/architecture.md", "# arch\n")
	write("agents/rules/testing.md", "---\nname: testing\ndescription: d\n---\nUse real databases.\n")
	write("agents/skills/deploy/SKILL.md", "---\nname: deploy\ndescription: d\n---\nSteps.\n")
	git("add", ".")
	git("commit", "-q", "-m", "team context with rules and skills")

	cfg := manifest.ParseFile(filepath.Join(repo, ".sageox", "sync.manifest"), manifest.RepoKindTeamContext)
	require.NotContains(t, cfg.Includes, "agents/", "fixture must omit agents/ for this test to mean anything")

	require.NoError(t, applySparseFromManifest(context.Background(), repo, cfg, manifest.RepoKindTeamContext, nil))

	// The claim a customer cares about: their rules and skills are still there.
	assert.FileExists(t, filepath.Join(repo, "agents", "rules", "testing.md"),
		"team rules were deleted by the sparse re-apply")
	assert.FileExists(t, filepath.Join(repo, "agents", "skills", "deploy", "SKILL.md"),
		"team skills were deleted by the sparse re-apply")
	assert.FileExists(t, filepath.Join(repo, "docs", "architecture.md"),
		"the manifest's own includes must still materialize")
}

// TestSparseSetForIsTheOnlyWayToComputeASparseSet guards the shape of the fix.
//
// The bug was not that one call site was wrong — it was that three call sites
// each open-coded the same three steps and only two remembered the floor. A
// fourth writer would have had the same coin flip. Production code outside the
// manifest package must go through SparseSetFor.
func TestSparseSetForIsTheOnlyWayToComputeASparseSet(t *testing.T) {
	root := ".."
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		slash := filepath.ToSlash(path)
		switch {
		case filepath.Base(path) == "parser.go" && filepath.Base(filepath.Dir(path)) == "manifest":
			return nil // the implementation itself
		case strings.HasSuffix(path, "_test.go"):
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, banned := range []string{"ComputeSparseSet(", "EnsureRequiredIncludes(", "EnsureSageoxInclude("} {
			if idx := indexOfCall(string(body), banned); idx {
				offenders = append(offenders, slash+" calls "+banned)
			}
		}
		return nil
	})
	require.NoError(t, err)
	assert.Empty(t, offenders,
		"compute the sparse set with manifest.SparseSetFor(cfg, kind); open-coding the steps is how the agents/ floor was lost")
}

// indexOfCall reports whether the source calls name outside of a comment line.
// Comments are skipped so the parser's own explanatory prose does not trip it.
func indexOfCall(src, name string) bool {
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.Contains(trimmed, name) {
			return true
		}
	}
	return false
}
