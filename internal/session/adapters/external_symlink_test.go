package adapters

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. SessionLookup validation ---

// TestSessionLookup_Validate_RejectsRelativePath verifies that "." and other
// relative paths are rejected with a clear error.
// Failure prevented: adapter receives "." as --repo-root, hashes it to "-",
// looks in nonexistent ~/.claude/projects/-, silently fails.
func TestSessionLookup_Validate_RejectsRelativePath(t *testing.T) {
	lookup := SessionLookup{
		RepoRoot: ".",
		AgentID:  "OxTest",
		Since:    time.Now(),
	}
	err := lookup.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}

// TestSessionLookup_Validate_RejectsEmpty verifies empty repoRoot is rejected.
// Failure prevented: empty string passed as --repo-root to adapter subprocess.
func TestSessionLookup_Validate_RejectsEmpty(t *testing.T) {
	lookup := SessionLookup{
		RepoRoot: "",
		AgentID:  "OxTest",
		Since:    time.Now(),
	}
	err := lookup.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}

// TestSessionLookup_Validate_RejectsNoSageox verifies that a valid absolute
// path without .sageox/ is rejected.
// Failure prevented: adapter runs against a directory that isn't an ox project.
func TestSessionLookup_Validate_RejectsNoSageox(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)

	lookup := SessionLookup{
		RepoRoot: tmpDir,
		AgentID:  "OxTest",
		Since:    time.Now(),
	}
	err := lookup.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".sageox/")
}

// TestSessionLookup_Validate_RejectsEmptyAgentID verifies agentID is required.
func TestSessionLookup_Validate_RejectsEmptyAgentID(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".sageox"), 0o755))

	lookup := SessionLookup{
		RepoRoot: tmpDir,
		AgentID:  "",
		Since:    time.Now(),
	}
	err := lookup.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agentID")
}

// TestSessionLookup_Validate_AcceptsValid verifies a well-formed lookup passes.
func TestSessionLookup_Validate_AcceptsValid(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".sageox"), 0o755))

	lookup := SessionLookup{
		RepoRoot: tmpDir,
		AgentID:  "OxTest",
		Since:    time.Now(),
	}
	err := lookup.Validate()
	require.NoError(t, err)
}

// --- B. execOneShot pre-flight validation (validateRepoRootArg) ---

// TestValidateRepoRootArg_RejectsDot verifies "." is caught before subprocess spawn.
// Failure prevented: adapter subprocess receives "." as --repo-root, silently fails.
func TestValidateRepoRootArg_RejectsDot(t *testing.T) {
	err := validateRepoRootArg([]string{"--repo-root", "."}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}

// TestValidateRepoRootArg_RejectsEmpty verifies empty value is caught.
func TestValidateRepoRootArg_RejectsEmpty(t *testing.T) {
	err := validateRepoRootArg([]string{"--repo-root", ""}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}

// TestValidateRepoRootArg_RejectsNoSageox verifies absolute path without .sageox/ is caught.
func TestValidateRepoRootArg_RejectsNoSageox(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)

	err := validateRepoRootArg([]string{"--repo-root", tmpDir}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".sageox/")
}

// TestValidateRepoRootArg_AcceptsValid verifies a proper ox project root passes.
func TestValidateRepoRootArg_AcceptsValid(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".sageox"), 0o755))

	err := validateRepoRootArg([]string{"--repo-root", tmpDir}, false)
	require.NoError(t, err)
}

// TestValidateRepoRootArg_NoRepoRootArg verifies no --repo-root arg passes when not required.
func TestValidateRepoRootArg_NoRepoRootArg(t *testing.T) {
	err := validateRepoRootArg([]string{"--agent-id", "test"}, false)
	require.NoError(t, err)
}

// --- C. SessionLookup edge cases ---

// TestSessionLookup_Validate_PathWithSpaces verifies paths containing spaces are accepted.
// Failure prevented: paths with spaces silently break session file hashing.
func TestSessionLookup_Validate_PathWithSpaces(t *testing.T) {
	tmpDir := filepath.Join(t.TempDir(), "my project repo")
	tmpDir, _ = filepath.EvalSymlinks(filepath.Dir(tmpDir))
	tmpDir = filepath.Join(tmpDir, "my project repo")
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".sageox"), 0o755))

	lookup := SessionLookup{RepoRoot: tmpDir, AgentID: "OxTest", Since: time.Now()}
	require.NoError(t, lookup.Validate())
}

// TestSessionLookup_Validate_TrailingSlash verifies trailing slash is accepted.
func TestSessionLookup_Validate_TrailingSlash(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".sageox"), 0o755))

	lookup := SessionLookup{RepoRoot: tmpDir + "/", AgentID: "OxTest", Since: time.Now()}
	require.NoError(t, lookup.Validate())
}

// TestSessionLookup_Validate_SymlinkedSageox verifies .sageox as a symlink to a real dir.
// Failure prevented: projects using symlinked .sageox/ (e.g., shared config) rejected.
func TestSessionLookup_Validate_SymlinkedSageox(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)

	realDir := filepath.Join(tmpDir, "real-sageox")
	require.NoError(t, os.MkdirAll(realDir, 0o755))
	require.NoError(t, os.Symlink(realDir, filepath.Join(tmpDir, ".sageox")))

	lookup := SessionLookup{RepoRoot: tmpDir, AgentID: "OxTest", Since: time.Now()}
	require.NoError(t, lookup.Validate())
}

// TestSessionLookup_Validate_SageoxIsFile verifies .sageox as a file (not dir) is rejected.
// Failure prevented: stale .sageox file from interrupted init passes validation.
func TestSessionLookup_Validate_SageoxIsFile(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".sageox"), []byte("not a dir"), 0o644))

	lookup := SessionLookup{RepoRoot: tmpDir, AgentID: "OxTest", Since: time.Now()}
	err := lookup.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".sageox/")
}

// TestSessionLookup_Validate_ZeroSinceAllowed verifies zero Since time passes.
// Since is not validated by Validate() — it's the caller's responsibility.
func TestSessionLookup_Validate_ZeroSinceAllowed(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".sageox"), 0o755))

	lookup := SessionLookup{RepoRoot: tmpDir, AgentID: "OxTest", Since: time.Time{}}
	require.NoError(t, lookup.Validate())
}

// TestSessionLookup_Validate_AgentSessionIDOptional verifies empty AgentSessionID passes.
func TestSessionLookup_Validate_AgentSessionIDOptional(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".sageox"), 0o755))

	lookup := SessionLookup{RepoRoot: tmpDir, AgentID: "OxTest", Since: time.Now(), AgentSessionID: ""}
	require.NoError(t, lookup.Validate())
}

// TestSessionLookup_Validate_NonexistentPath verifies nonexistent path is rejected.
func TestSessionLookup_Validate_NonexistentPath(t *testing.T) {
	lookup := SessionLookup{RepoRoot: "/nonexistent/path/that/does/not/exist", AgentID: "OxTest", Since: time.Now()}
	err := lookup.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".sageox/")
}

// TestSessionLookup_Validate_TildePath verifies ~ is rejected (not absolute after expansion).
func TestSessionLookup_Validate_TildePath(t *testing.T) {
	lookup := SessionLookup{RepoRoot: "~/repo", AgentID: "OxTest", Since: time.Now()}
	err := lookup.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}

// --- D. CanonicalAdapterName ---

// TestCanonicalAdapterName verifies alias resolution for all known adapters.
// Failure prevented: agent_type drift causes "claude" vs "claude-code" in metadata,
// breaking session lookup and downstream analytics.
func TestCanonicalAdapterName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// claude-code aliases
		{"claude code", "claude-code"},
		{"Claude Code", "claude-code"},
		{"claude", "claude-code"},
		{"CLAUDE", "claude-code"},

		// gemini aliases
		{"gemini", "gemini"},
		{"gemini-cli", "gemini"},
		{"Gemini CLI", "gemini"},
		{"Gemini", "gemini"},

		// generic fallbacks
		{"codex", "codex"},
		{"Codex", "codex"},
		{"amp", "amp"},
		{"cursor", "generic"},
		{"windsurf", "generic"},
		{"copilot", "generic"},
		{"aider", "aider"},
		{"cody", "generic"},
		{"continue", "generic"},
		{"cline", "generic"},
		{"goose", "goose"},
		{"kiro", "generic"},
		{"opencode", "opencode"},
		{"pi", "pi"},
		{"pi-coding-agent", "pi"},
		{"droid", "droid"},

		// already canonical — passthrough
		{"claude-code", "claude-code"},
		{"generic", "generic"},

		// unknown — passthrough unchanged
		{"MyCustomAdapter", "MyCustomAdapter"},
		{"unknown-agent", "unknown-agent"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := CanonicalAdapterName(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- E. validateRepoRootArg edge cases ---

// TestValidateRepoRootArg_LastArgNoValue verifies --repo-root as the last arg (no value)
// passes through. The loop condition `i < len(args)-1` skips it intentionally —
// the adapter subprocess will reject the missing value.
func TestValidateRepoRootArg_LastArgNoValue(t *testing.T) {
	err := validateRepoRootArg([]string{"--repo-root"}, false)
	require.NoError(t, err, "trailing --repo-root without value should pass through to subprocess")
}

// TestValidateRepoRootArg_FirstOfDuplicates verifies only the first --repo-root is validated.
// Failure prevented: second occurrence could bypass validation of the first.
func TestValidateRepoRootArg_FirstOfDuplicates(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".sageox"), 0o755))

	// first is valid, second is bad — should pass because first is checked and returned
	err := validateRepoRootArg([]string{"--repo-root", tmpDir, "--repo-root", "."}, false)
	require.NoError(t, err)

	// first is bad — should fail even though second is valid
	err = validateRepoRootArg([]string{"--repo-root", ".", "--repo-root", tmpDir}, false)
	require.Error(t, err)
}

// TestValidateRepoRootArg_EqualsSyntaxPassthrough verifies --repo-root=/path is not matched.
// This documents a known gap — the function only matches `--repo-root <value>` (space-separated).
// The adapter subprocess handles --repo-root=value parsing itself.
func TestValidateRepoRootArg_EqualsSyntaxPassthrough(t *testing.T) {
	err := validateRepoRootArg([]string{"--repo-root=."}, false)
	require.NoError(t, err, "--repo-root=value syntax is not matched by pre-flight check")
}

// TestValidateRepoRootArg_MixedArgs verifies --repo-root is found among other args.
func TestValidateRepoRootArg_MixedArgs(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".sageox"), 0o755))

	err := validateRepoRootArg([]string{
		"--agent-id", "OxTest",
		"--repo-root", tmpDir,
		"--since", "2026-01-01T00:00:00Z",
	}, false)
	require.NoError(t, err)
}

// TestValidateRepoRootArg_PathWithSpaces verifies paths with spaces work.
func TestValidateRepoRootArg_PathWithSpaces(t *testing.T) {
	tmpDir := filepath.Join(t.TempDir(), "my project")
	tmpDir, _ = filepath.EvalSymlinks(filepath.Dir(tmpDir))
	tmpDir = filepath.Join(tmpDir, "my project")
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".sageox"), 0o755))

	err := validateRepoRootArg([]string{"--repo-root", tmpDir}, false)
	require.NoError(t, err)
}

// TestValidateRepoRootArg_NilArgs verifies nil slice is safe.
func TestValidateRepoRootArg_NilArgs(t *testing.T) {
	require.NoError(t, validateRepoRootArg(nil, false))
}

// TestValidateRepoRootArg_EmptySlice verifies empty slice is safe.
func TestValidateRepoRootArg_EmptySlice(t *testing.T) {
	require.NoError(t, validateRepoRootArg([]string{}, false))
}

// --- F. validateRepoRootArg fail-closed for find-session ---

// TestValidateRepoRootArg_RequireRepoRoot_MissingFails verifies that when
// requireRepoRoot is true, missing --repo-root is an error (fail closed).
// Failure prevented: find-session silently falls back to cwd when --repo-root omitted.
func TestValidateRepoRootArg_RequireRepoRoot_MissingFails(t *testing.T) {
	err := validateRepoRootArg([]string{"--agent-id", "test"}, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required for find-session")
}

// TestValidateRepoRootArg_RequireRepoRoot_EmptyArgsFails verifies empty args
// with requireRepoRoot is an error.
func TestValidateRepoRootArg_RequireRepoRoot_EmptyArgsFails(t *testing.T) {
	require.Error(t, validateRepoRootArg(nil, true))
	require.Error(t, validateRepoRootArg([]string{}, true))
}

// TestValidateRepoRootArg_RequireRepoRoot_ValidPasses verifies that when
// requireRepoRoot is true and --repo-root is valid, no error.
func TestValidateRepoRootArg_RequireRepoRoot_ValidPasses(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".sageox"), 0o755))

	err := validateRepoRootArg([]string{"--repo-root", tmpDir}, true)
	require.NoError(t, err)
}
