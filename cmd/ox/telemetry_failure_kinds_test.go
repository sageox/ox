package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/agentx"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/errkind"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSetupFailures_CarryAKindAndDetail drives each setup failure ox hits
// before a repository, Ledger, login, or AI coworker session is ready.
//
// Failure prevented: these failures reaching usage telemetry as
// error_kind=other with an empty error_detail, so the dashboard cannot tell a
// missing setup step from an ox bug.
func TestSetupFailures_CarryAKindAndDetail(t *testing.T) {
	// Every variable agentx v0.1.14 reads to detect a coding agent, plus ox's
	// own: run from inside any AI coworker, the dispatcher would otherwise find
	// that session and take a different branch.
	for _, v := range []string{"SAGEOX_AGENT_ID", "AGENT_ENV", "_",
		"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION_ID",
		"CODEX_CI", "CODEX_SANDBOX", "CODEX_THREAD_ID",
		"PI_CODING_AGENT", "PI_CODING_AGENT_DIR", "PI_SESSION_ID", "OMP_SESSION_ID",
		"AIDER", "AIDER_AGENT", "AMP", "AMP_AGENT", "AMP_THREAD_URL", "CLINE", "CLINE_AGENT",
		"CODEIUM_AGENT", "CODE_PUPPY", "CODE_PUPPY_AGENT", "CODY_AGENT", "CONTINUE_AGENT",
		"COPILOT_AGENT", "CURSOR_AGENT", "DROID", "DROID_AGENT", "FACTORY_DROID",
		"GEMINI", "GEMINI_AGENT", "GOOSE", "GOOSE_AGENT", "KIRO", "KIRO_AGENT",
		"OPENCODE", "OPENCODE_AGENT", "WINDSURF_AGENT"} {
		// Unset, not empty: some detectors test presence (Pi reads
		// PI_CODING_AGENT_DIR with LookupEnv). t.Setenv restores it after.
		t.Setenv(v, "")
		require.NoError(t, os.Unsetenv(v))
	}
	t.Chdir(t.TempDir()) // no .codex/ or other agent directory to fall back to
	require.Nil(t, agentx.CurrentAgent(), "isolation: no coding agent may be detected")

	t.Run("not in a SageOx project", func(t *testing.T) {
		t.Chdir(t.TempDir())
		_, err := requireProjectRoot()
		assertFailureKind(t, err, errkind.NotInitialized, "not in a SageOx project (no .sageox directory found)")
	})

	t.Run("murmur to a team with no Team Context", func(t *testing.T) {
		_, err := resolveMurmurTarget(t.TempDir(), "team")
		assertFailureKind(t, err, errkind.NotInitialized, "no team context configured — run 'ox init' first")
	})

	t.Run("murmur to a Ledger that was never cloned", func(t *testing.T) {
		repo := t.TempDir()
		runGit(t, repo, "init", "-q")
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".sageox"), 0o755))
		require.NoError(t, config.SaveLocalConfig(repo, &config.LocalConfig{
			Ledger: &config.LedgerConfig{Path: filepath.Join(repo, "missing-ledger")},
		}))
		t.Chdir(repo)
		_, err := resolveMurmurTarget(repo, "ledger")
		assertFailureKind(t, err, errkind.NotInitialized, "ledger not found at %s — run 'ox doctor --fix'")
	})

	t.Run("session score outside an AI coworker session", func(t *testing.T) {
		err := runSessionScore(sessionScoreCmd, nil)
		assertFailureKind(t, err, errkind.Other, "SAGEOX_AGENT_ID not set -- run 'ox agent prime' first")
	})

	t.Run("agent session with no agent ID", func(t *testing.T) {
		err := runAgentDispatcher(agentCmd, []string{"session", "start"})
		assertFailureKind(t, err, errkind.Other, "no agent ID: %q requires an agent ID (run 'ox agent prime' first)")
	})

	t.Run("git credentials with no token", func(t *testing.T) {
		ep := "http://127.0.0.1:9"
		t.Setenv("SAGEOX_ENDPOINT", ep)
		// a file in a temp dir, never the developer's OS keychain
		prevForce := gitserver.TestSetForceFileStorage(true)
		prevDir := gitserver.TestSetConfigDirOverride(t.TempDir())
		t.Cleanup(func() {
			gitserver.TestSetForceFileStorage(prevForce)
			gitserver.TestSetConfigDirOverride(prevDir)
		})
		require.NoError(t, gitserver.SaveCredentialsForEndpoint(ep, gitserver.GitCredentials{
			Username: "testuser", ServerURL: ep, ExpiresAt: time.Now().Add(time.Hour),
		}))
		_, err := getLFSClient(t.TempDir())
		assertFailureKind(t, err, errkind.NotLoggedIn, "git credentials have empty token")
	})
}

func assertFailureKind(t *testing.T, err error, kind errkind.Kind, detail string) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, kind, errkind.Of(err))
	assert.Equal(t, detail, errkind.DetailOf(err))
}
