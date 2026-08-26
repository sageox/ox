package manifest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeSparseSet_DenyDataExcludesData(t *testing.T) {
	cfg := &ManifestConfig{
		Includes: []string{"memory/", ".sageox/"},
		Denies:   []string{"data/"},
	}

	result := ComputeSparseSet(cfg)

	assert.Contains(t, result, "/memory/")
	assert.Contains(t, result, "/.sageox/")
	assert.NotContains(t, result, "/data/")
}

func TestComputeSparseSet_IncludesRootFiles(t *testing.T) {
	cfg := &ManifestConfig{
		Includes: []string{"memory/", ".sageox/"},
		Denies:   []string{"data/"},
	}

	result := ComputeSparseSet(cfg)

	// /* and !/*/ must be the first two entries: include root-level files,
	// exclude root-level directories (re-included explicitly by includes)
	require.Len(t, result, 4, "expected /* + !/*/ + 2 includes")
	assert.Equal(t, "/*", result[0], "first entry should be /* to include root files")
	assert.Equal(t, "!/*/", result[1], "second entry should be !/*/ to exclude root dirs")
}

func TestComputeSparseSet_DenyDataBlocksDataSubdirs(t *testing.T) {
	cfg := &ManifestConfig{
		Includes: []string{"data/slack/"},
		Denies:   []string{"data/"},
	}

	result := ComputeSparseSet(cfg)

	// only root-file patterns remain — no include dirs survived the deny
	assert.Equal(t, []string{"/*", "!/*/"}, result, "deny on parent data/ should block child data/slack/")
}

func TestComputeSparseSet_FallbackConfigExcludesData(t *testing.T) {
	cfg := FallbackConfigFor(RepoKindTeamContext)
	result := ComputeSparseSet(cfg)

	for _, path := range result {
		// patterns are root-anchored ("/data/"), so compare on the unanchored form
		bare := strings.TrimPrefix(path, "/")
		assert.NotEqual(t, "data/", bare, "fallback sparse set should not include data/")
		assert.False(t,
			strings.HasPrefix(bare, "data/"),
			"fallback sparse set should not include data/ subdirectories, got: %s", path,
		)
	}

	// verify known fallback paths are present
	assert.Contains(t, result, "/memory/")
	assert.Contains(t, result, "/agents/")
	assert.Contains(t, result, "/.sageox/")
}

// gitEnv returns environment variables that provide git identity
// so tests don't depend on global git config.
func gitEnv() []string {
	return append(os.Environ(), // safe: git subprocess in temp dir, not ox
		"GIT_AUTHOR_NAME=Test User",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
}

// runGit runs a git command in the given directory with test-safe env vars.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(out))
}

// initGitRepo creates a git repo at dir with an initial commit containing
// the provided files. fileContents maps relative paths to file content.
func initGitRepo(t *testing.T, dir string, fileContents map[string]string) {
	t.Helper()

	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")

	for relPath, content := range fileContents {
		fullPath := filepath.Join(dir, relPath)
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0o755))
		require.NoError(t, os.WriteFile(fullPath, []byte(content), 0o644))
	}

	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial commit")
}

func TestSparseCheckout_DataExcludedFromWorkingTree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()

	// create repo with files in data/, memory/, .sageox/, and root-level files
	initGitRepo(t, dir, map[string]string{
		"data/raw.txt":               "raw data content",
		"memory/daily/2026-01-01.md": "daily memory",
		".sageox/config.json":        `{"version": 1}`,
		// Plain root-level file to verify /* pattern includes root files.
		// Intentionally NOT a filter=lfs entry — ox never writes LFS filters
		// (see .claude/rules/lfs-no-git-lfs-binary.md). An LFS filter here
		// would trip git's smudge filter on `git add` on machines without
		// git-lfs installed.
		".gitattributes": "* text=auto\n",
	})

	// compute sparse set from a manifest that denies data/
	cfg := &ManifestConfig{
		Includes: []string{"memory/", ".sageox/"},
		Denies:   []string{"data/"},
	}
	sparseSet := ComputeSparseSet(cfg)
	require.NotEmpty(t, sparseSet)

	// enable sparse checkout in --no-cone mode (matches TwoPhaseClone)
	runGit(t, dir, append([]string{"sparse-checkout", "set", "--no-cone"}, sparseSet...)...)

	// data/raw.txt should NOT exist in the working tree
	_, err := os.Stat(filepath.Join(dir, "data", "raw.txt"))
	assert.True(t, os.IsNotExist(err), "data/raw.txt should not exist in sparse working tree")

	// memory/ files should exist
	_, err = os.Stat(filepath.Join(dir, "memory", "daily", "2026-01-01.md"))
	assert.NoError(t, err, "memory/daily/2026-01-01.md should exist in sparse working tree")

	// .sageox/ files should exist
	_, err = os.Stat(filepath.Join(dir, ".sageox", "config.json"))
	assert.NoError(t, err, ".sageox/config.json should exist in sparse working tree")

	// root-level files (like .gitattributes) should exist via /* pattern
	_, err = os.Stat(filepath.Join(dir, ".gitattributes"))
	assert.NoError(t, err, ".gitattributes should exist in sparse working tree (root-level files included)")
}

func TestSparseCheckout_FreshCloneExcludesData(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// create a bare repo to serve as the remote
	bareDir := t.TempDir()
	runGit(t, bareDir, "init", "--bare")

	// create a working repo, add files, push to bare
	srcDir := t.TempDir()
	initGitRepo(t, srcDir, map[string]string{
		"data/file.txt":    "should be excluded",
		"memory/obs.jsonl": `{"observation": "test"}`,
		".sageox/soul.md":  "team soul",
		// Plain root-level file to verify /* pattern includes root files.
		// See note in TestSparseCheckout_DataExcludedFromWorkingTree — no
		// filter=lfs entries in ox test fixtures.
		".gitattributes": "* text=auto\n",
	})
	runGit(t, srcDir, "remote", "add", "origin", bareDir)
	runGit(t, srcDir, "push", "origin", "HEAD:main")

	// clone with --sparse from the bare repo
	cloneDir := filepath.Join(t.TempDir(), "clone")
	cmd := exec.Command("git", "clone", "--sparse", "--branch", "main", bareDir, cloneDir)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git clone --sparse failed: %s", string(out))

	// compute sparse set and apply
	cfg := &ManifestConfig{
		Includes: []string{"memory/", ".sageox/"},
		Denies:   []string{"data/"},
	}
	sparseSet := ComputeSparseSet(cfg)
	require.NotEmpty(t, sparseSet)

	runGit(t, cloneDir, append([]string{"sparse-checkout", "set", "--no-cone"}, sparseSet...)...)

	// data/ should not be in the working tree
	_, err = os.Stat(filepath.Join(cloneDir, "data"))
	assert.True(t, os.IsNotExist(err), "data/ directory should not exist in sparse clone")

	// memory/ should be present
	_, err = os.Stat(filepath.Join(cloneDir, "memory", "obs.jsonl"))
	assert.NoError(t, err, "memory/obs.jsonl should exist in sparse clone")

	// root-level files should be present via /* pattern
	_, err = os.Stat(filepath.Join(cloneDir, ".gitattributes"))
	assert.NoError(t, err, ".gitattributes should exist in sparse clone (root-level files included)")
}

// TestSparseCheckout_BareFilePatternDoesNotLeakNestedMatches drives real git
// to prove the root-anchoring fix, rather than only asserting on the pattern
// string that ComputeSparseSet emits.
//
// gitignore semantics (which --no-cone sparse-checkout uses) make a pattern
// with no interior slash match at ANY depth. An include of "AGENTS.md" for the
// repo's root entry point therefore also matched knowledge/agents.md, pulling
// one file out of a directory that was otherwise entirely excluded. On macOS
// core.ignorecase=true widened it further to any case variant.
//
// The class of failure is "a root-level manifest entry silently materializes
// same-named files nested elsewhere", so this covers the exact-case nested
// match, the case-variant nested match, and confirms the intended root file
// still lands.
//
// Failure prevented: a sparse set that is wrong for an entire directory looks
// partially correct, hiding the real breakage during triage.
func TestSparseCheckout_BareFilePatternDoesNotLeakNestedMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	initGitRepo(t, dir, map[string]string{
		"AGENTS.md":            "root entry point",
		"knowledge/agents.md":  "nested, case-variant",
		"knowledge/AGENTS.md":  "nested, exact case",
		"knowledge/quality.md": "nested, unrelated name",
		".sageox/kb.yaml":      "kb_type: custom\n",
	})

	// manifest includes the root file but NOT knowledge/
	sparseSet := ComputeSparseSet(&ManifestConfig{Includes: []string{".sageox/", "AGENTS.md"}})
	runGit(t, dir, append([]string{"sparse-checkout", "set", "--no-cone"}, sparseSet...)...)

	assert.FileExists(t, filepath.Join(dir, "AGENTS.md"),
		"the root AGENTS.md is what the manifest actually asked for")

	for _, leaked := range []string{"knowledge/agents.md", "knowledge/AGENTS.md", "knowledge/quality.md"} {
		_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(leaked)))
		assert.True(t, os.IsNotExist(err),
			"%s must not materialize: knowledge/ is not in the include set", leaked)
	}
}
