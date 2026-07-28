//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionMarkerDir(t *testing.T) {
	dir := SessionMarkerDir()
	assert.Contains(t, dir, "sageox")
	assert.True(t, strings.HasSuffix(dir, "sessions"))
}

func TestMarkerPath(t *testing.T) {
	t.Run("simple session ID", func(t *testing.T) {
		path := markerPath("abc123")
		assert.Equal(t, filepath.Join(SessionMarkerDir(), "abc123.json"), path)
	})

	t.Run("sanitizes path traversal", func(t *testing.T) {
		path := markerPath("../../../etc/passwd")
		assert.True(t, strings.HasPrefix(path, SessionMarkerDir()))
		assert.NotContains(t, path, "..")
	})

	t.Run("sanitizes slashes", func(t *testing.T) {
		path := markerPath("path/to/session")
		assert.NotContains(t, path, "/to/")
	})
}

func TestSessionMarkerReadWrite(t *testing.T) {
	t.Run("write and read marker", func(t *testing.T) {
		sessionID := "test_" + time.Now().Format("20060102150405.000")
		marker := &SessionMarker{
			AgentID:        "OxTest",
			SessionID:      "oxsid_test123",
			AgentSessionID: sessionID,
			PrimedAt:       time.Now().Truncate(time.Second),
		}

		// write
		err := WriteSessionMarker(marker)
		require.NoError(t, err)
		t.Cleanup(func() {
			DeleteSessionMarker(sessionID)
		})

		// read back
		read, err := ReadSessionMarker(sessionID)
		require.NoError(t, err)
		require.NotNil(t, read)

		assert.Equal(t, marker.AgentID, read.AgentID)
		assert.Equal(t, marker.SessionID, read.SessionID)
		assert.Equal(t, marker.AgentSessionID, read.AgentSessionID)
		assert.Equal(t, marker.PrimedAt.Unix(), read.PrimedAt.Unix())
	})

	t.Run("read non-existent marker returns nil", func(t *testing.T) {
		read, err := ReadSessionMarker("nonexistent_session_id")
		assert.NoError(t, err)
		assert.Nil(t, read)
	})

	t.Run("read empty session ID returns nil", func(t *testing.T) {
		read, err := ReadSessionMarker("")
		assert.NoError(t, err)
		assert.Nil(t, read)
	})

	t.Run("delete marker", func(t *testing.T) {
		sessionID := "test_delete_" + time.Now().Format("20060102150405.000")
		marker := &SessionMarker{
			AgentID:        "OxDel",
			AgentSessionID: sessionID,
			PrimedAt:       time.Now(),
		}

		// write
		err := WriteSessionMarker(marker)
		require.NoError(t, err)

		// verify exists
		read, err := ReadSessionMarker(sessionID)
		require.NoError(t, err)
		require.NotNil(t, read)

		// delete
		err = DeleteSessionMarker(sessionID)
		require.NoError(t, err)

		// verify gone
		read, err = ReadSessionMarker(sessionID)
		require.NoError(t, err)
		assert.Nil(t, read)
	})

	t.Run("delete non-existent marker is no-op", func(t *testing.T) {
		err := DeleteSessionMarker("nonexistent_delete_test")
		assert.NoError(t, err)
	})

	t.Run("delete empty session ID is no-op", func(t *testing.T) {
		err := DeleteSessionMarker("")
		assert.NoError(t, err)
	})
}

func TestSessionMarkerJSONFormat(t *testing.T) {
	// verify the marker file is in JSON format
	sessionID := "test_format_" + time.Now().Format("20060102150405.000")
	marker := &SessionMarker{
		AgentID:        "OxFmt",
		SessionID:      "oxsid_format123",
		AgentSessionID: sessionID,
		PrimedAt:       time.Unix(1700000000, 0),
	}

	err := WriteSessionMarker(marker)
	require.NoError(t, err)
	t.Cleanup(func() {
		DeleteSessionMarker(sessionID)
	})

	// read raw file content
	path := markerPath(sessionID)
	content, err := os.ReadFile(path)
	require.NoError(t, err)

	// verify JSON format
	assert.Contains(t, string(content), `"agent_id": "OxFmt"`)
	assert.Contains(t, string(content), `"session_id": "oxsid_format123"`)
	assert.Contains(t, string(content), `"agent_session_id"`)
}

func TestIsAgentHookContext(t *testing.T) {
	// save original env
	origProjectDir := os.Getenv("CLAUDE_PROJECT_DIR")
	origClaudeCode := os.Getenv("CLAUDECODE")
	origEntrypoint := os.Getenv("CLAUDE_CODE_ENTRYPOINT")
	t.Cleanup(func() {
		os.Setenv("CLAUDE_PROJECT_DIR", origProjectDir)
		os.Setenv("CLAUDECODE", origClaudeCode)
		os.Setenv("CLAUDE_CODE_ENTRYPOINT", origEntrypoint)
	})

	t.Run("detects CLAUDE_PROJECT_DIR", func(t *testing.T) {
		os.Setenv("CLAUDE_PROJECT_DIR", "/some/project")
		os.Unsetenv("CLAUDECODE")
		os.Unsetenv("CLAUDE_CODE_ENTRYPOINT")
		assert.True(t, IsAgentHookContext())
	})

	t.Run("detects CLAUDECODE=1", func(t *testing.T) {
		os.Unsetenv("CLAUDE_PROJECT_DIR")
		os.Setenv("CLAUDECODE", "1")
		os.Unsetenv("CLAUDE_CODE_ENTRYPOINT")
		assert.True(t, IsAgentHookContext())
	})

	t.Run("detects CLAUDE_CODE_ENTRYPOINT", func(t *testing.T) {
		os.Unsetenv("CLAUDE_PROJECT_DIR")
		os.Unsetenv("CLAUDECODE")
		os.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")
		assert.True(t, IsAgentHookContext())
	})

	t.Run("returns false when no agent env vars", func(t *testing.T) {
		os.Unsetenv("CLAUDE_PROJECT_DIR")
		os.Unsetenv("CLAUDECODE")
		os.Unsetenv("CLAUDE_CODE_ENTRYPOINT")
		assert.False(t, IsAgentHookContext())
	})
}

func TestWriteToAgentEnvFile(t *testing.T) {
	t.Run("no-op when CLAUDE_ENV_FILE not set", func(t *testing.T) {
		origEnv := os.Getenv("CLAUDE_ENV_FILE")
		os.Unsetenv("CLAUDE_ENV_FILE")
		t.Cleanup(func() {
			if origEnv != "" {
				os.Setenv("CLAUDE_ENV_FILE", origEnv)
			}
		})

		err := WriteToAgentEnvFile(map[string]string{"KEY": "value"})
		assert.NoError(t, err)
	})

	t.Run("writes to env file when set", func(t *testing.T) {
		tmpDir := t.TempDir()
		envFile := filepath.Join(tmpDir, "env")

		origEnv := os.Getenv("CLAUDE_ENV_FILE")
		os.Setenv("CLAUDE_ENV_FILE", envFile)
		t.Cleanup(func() {
			if origEnv != "" {
				os.Setenv("CLAUDE_ENV_FILE", origEnv)
			} else {
				os.Unsetenv("CLAUDE_ENV_FILE")
			}
		})

		err := WriteToAgentEnvFile(map[string]string{
			"AGENT_ENV":       "claude-code",
			"SAGEOX_AGENT_ID": "OxEnv1",
		})
		require.NoError(t, err)

		// verify content
		content, err := os.ReadFile(envFile)
		require.NoError(t, err)
		assert.Contains(t, string(content), `export AGENT_ENV="claude-code"`)
		assert.Contains(t, string(content), `export SAGEOX_AGENT_ID="OxEnv1"`)
	})
}

// TestWriteToAgentEnvFile_WritesAgentIDWithoutSessionID is the regression test for
// #258: env file must be written even when SAGEOX_SESSION_ID is empty (no hook stdin,
// no session ID). The agent ID must always reach the env file so /clear in Claude
// Code inherits the correct agent ID.
func TestWriteToAgentEnvFile_WritesAgentIDWithoutSessionID(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "env")

	origEnv := os.Getenv("CLAUDE_ENV_FILE")
	os.Setenv("CLAUDE_ENV_FILE", envFile)
	t.Cleanup(func() {
		if origEnv != "" {
			os.Setenv("CLAUDE_ENV_FILE", origEnv)
		} else {
			os.Unsetenv("CLAUDE_ENV_FILE")
		}
	})

	// simulate what the fixed prime code does: write both vars unconditionally,
	// even when session ID is empty (no hook context)
	err := WriteToAgentEnvFile(map[string]string{
		"SAGEOX_AGENT_ID":   "OxTest",
		"SAGEOX_SESSION_ID": "", // empty — no session from hook stdin
	})
	require.NoError(t, err)

	content, err := os.ReadFile(envFile)
	require.NoError(t, err)
	// agent ID must always be written regardless of empty session ID
	assert.Contains(t, string(content), `export SAGEOX_AGENT_ID="OxTest"`,
		"SAGEOX_AGENT_ID must be written to env file even when session ID is empty")
}

// TestWriteToAgentEnvFile_Idempotent verifies upsert semantics: a second
// write of the same key replaces the first, leaving exactly one definition
// in the file rather than stacking duplicates.
//
// Failure prevented: #527/#529 cross-session AGENT_ENV poisoning. Without
// upsert, a second prime that mis-claims AGENT_ENV=pi after an earlier
// AGENT_ENV=claude-code would leave both lines in the file and any later
// subprocess (re-sourcing the file) could inherit the wrong value
// depending on ordering and shell semantics.
func TestWriteToAgentEnvFile_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "env")

	origEnv := os.Getenv("CLAUDE_ENV_FILE")
	os.Setenv("CLAUDE_ENV_FILE", envFile)
	t.Cleanup(func() {
		if origEnv != "" {
			os.Setenv("CLAUDE_ENV_FILE", origEnv)
		} else {
			os.Unsetenv("CLAUDE_ENV_FILE")
		}
	})

	// first prime call
	require.NoError(t, WriteToAgentEnvFile(map[string]string{
		"SAGEOX_AGENT_ID": "OxFirst",
	}))

	// second prime call (after /clear) with a different agent ID
	require.NoError(t, WriteToAgentEnvFile(map[string]string{
		"SAGEOX_AGENT_ID": "OxSecond",
	}))

	content, err := os.ReadFile(envFile)
	require.NoError(t, err)
	// second write wins; first is not retained
	assert.Contains(t, string(content), `export SAGEOX_AGENT_ID="OxSecond"`)
	assert.NotContains(t, string(content), `export SAGEOX_AGENT_ID="OxFirst"`,
		"first write must be replaced, not stacked — append-and-stack is the #527/#529 bug class")
	// exactly one export line for this key
	assert.Equal(t, 1, strings.Count(string(content), `export SAGEOX_AGENT_ID=`),
		"only one export line per key should remain after upsert")
}

// TestWriteToAgentEnvFile_AgentEnvUpsertAfterAdapterMismatch is a direct
// reproducer for #527: the first prime (from the correct SessionStart hook)
// sets AGENT_ENV=claude-code; a second prime driven by a tainted CLAUDE.md
// block would have injected AGENT_ENV=pi. After upsert, only the latest
// write should remain — and this test pins that contract.
func TestWriteToAgentEnvFile_AgentEnvUpsertAfterAdapterMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "env")

	origEnv := os.Getenv("CLAUDE_ENV_FILE")
	os.Setenv("CLAUDE_ENV_FILE", envFile)
	t.Cleanup(func() {
		if origEnv != "" {
			os.Setenv("CLAUDE_ENV_FILE", origEnv)
		} else {
			os.Unsetenv("CLAUDE_ENV_FILE")
		}
	})

	// hook-driven prime: correct agent type
	require.NoError(t, WriteToAgentEnvFile(map[string]string{
		"AGENT_ENV": "claude-code",
	}))

	// CLAUDE.md-driven re-prime that wrongly claims pi (pre-fix #527 shape)
	require.NoError(t, WriteToAgentEnvFile(map[string]string{
		"AGENT_ENV": "pi",
	}))

	content, err := os.ReadFile(envFile)
	require.NoError(t, err)
	// only the last write should remain in the file, and it must be "pi"
	// — a buggy implementation that kept "claude-code" and silently
	// dropped the second write would satisfy a bare count check
	// (CodeRabbit review on #543).
	assert.Contains(t, string(content), `export AGENT_ENV="pi"`,
		"last write value must survive")
	assert.NotContains(t, string(content), `export AGENT_ENV="claude-code"`,
		"earlier write must be replaced, not retained")
	assert.Equal(t, 1, strings.Count(string(content), `export AGENT_ENV=`),
		"AGENT_ENV must not stack across primes")
}

// TestWriteToAgentEnvFile_PreservesUnrelatedExportsVerbatim confirms the
// surgical-upsert contract: lines unrelated to the keys being written
// (including unrelated exports with shell-expansion values, comments,
// blanks) must pass through byte-for-byte — never reformatted through
// Go's %q, which would change quoting on constructs like
// `export PATH="$HOME/bin:$PATH"` and silently break the caller's shell.
//
// Failure prevented: CodeRabbit review on #543 flagged this as a
// privacy + correctness regression in the earlier parseEnvFile-based
// approach. The env file can carry any export a parent shell injected.
func TestWriteToAgentEnvFile_PreservesUnrelatedExportsVerbatim(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "env")

	// seed the file with content ox did not write: a comment, an
	// unrelated export with a shell-expansion value, and a blank line.
	seed := "# caller's settings\nexport PATH=\"$HOME/bin:$PATH\"\n\nexport UNRELATED='don'\\''t touch'\n"
	require.NoError(t, os.WriteFile(envFile, []byte(seed), 0600))

	t.Setenv("CLAUDE_ENV_FILE", envFile)

	require.NoError(t, WriteToAgentEnvFile(map[string]string{
		"SAGEOX_AGENT_ID": "OxNew",
	}))

	got, err := os.ReadFile(envFile)
	require.NoError(t, err)
	gotStr := string(got)

	// Every seeded line must be present byte-for-byte.
	for _, lit := range []string{
		"# caller's settings",
		`export PATH="$HOME/bin:$PATH"`,
		`export UNRELATED='don'\''t touch'`,
	} {
		assert.Contains(t, gotStr, lit,
			"caller's line %q must be preserved verbatim", lit)
	}
	// The new SageOx-owned key must be appended exactly once.
	assert.Equal(t, 1, strings.Count(gotStr, "export SAGEOX_AGENT_ID="))
	assert.Contains(t, gotStr, `export SAGEOX_AGENT_ID="OxNew"`)
}

// TestWriteToAgentEnvFile_NeverLoosensPermissions confirms the env
// file gets written with no looser than 0600 — and if the existing
// file was already stricter, the stricter mode is preserved.
// Failure prevented: 0644 exposure of values the caller considers
// private. CodeRabbit review on #543.
func TestWriteToAgentEnvFile_NeverLoosensPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "env")

	t.Setenv("CLAUDE_ENV_FILE", envFile)

	t.Run("new file defaults to 0600", func(t *testing.T) {
		require.NoError(t, WriteToAgentEnvFile(map[string]string{"AGENT_ENV": "claude-code"}))
		info, err := os.Stat(envFile)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm(),
			"default permissions should cap at 0600, not 0644")
		require.NoError(t, os.Remove(envFile))
	})

	t.Run("preserves stricter existing mode", func(t *testing.T) {
		require.NoError(t, os.WriteFile(envFile, []byte(""), 0400))
		require.NoError(t, WriteToAgentEnvFile(map[string]string{"AGENT_ENV": "claude-code"}))
		info, err := os.Stat(envFile)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0400), info.Mode().Perm(),
			"existing stricter mode (0400) must not be loosened")
	})
}

// isolateSessionMarkerDir gives the calling test a private SessionMarkerDir.
//
// SessionMarkerDir() is paths.TempDir()/sessions — ONE global directory
// shared by the whole package (and with the developer's real ox install).
// FindSessionMarkerByPID scans it and returns the FIRST marker matching the
// queried PID, in os.ReadDir order. Every test that writes a marker with
// ParentPID: os.Getpid() therefore competes for the same key, and which one
// a PID query resolves to comes down to filename ordering — several test
// files do exactly that (agent_hook_test.go, session_force_stop_test.go,
// agent_prime_id_reuse_test.go).
//
// Failure prevented: a PID-query test intermittently asserting against
// another test's marker. Reproduced deterministically by planting a
// same-PID marker whose session ID sorts earlier — the query then returns
// the competitor rather than the marker under test.
//
// paths.TempDir() derives the directory from $USER (then $USERNAME on
// Windows), so overriding those per test isolates the namespace without
// touching production code. t.Setenv restores them automatically and marks
// the test non-parallel, which is required here anyway. Any future test
// that queries markers by PID should call this rather than hope it wins the
// ordering race.
// t.Name() alone is NOT enough: two concurrent `go test` processes run the
// same test name, would derive the same directory, and the Cleanup below
// would delete the other process's markers — reintroducing the shared-key
// collision at process scope instead of test scope. t.TempDir()'s basename
// is unique per test AND per process, so it closes that last gap.
func isolateSessionMarkerDir(t *testing.T) {
	t.Helper()
	unique := "oxtest-" + filepath.Base(t.TempDir()) + "-" + strings.ReplaceAll(t.Name(), "/", "_")
	t.Setenv("USER", unique)
	t.Setenv("USERNAME", unique)
	require.NoError(t, os.MkdirAll(SessionMarkerDir(), 0700))
	t.Cleanup(func() { _ = os.RemoveAll(SessionMarkerDir()) })
}

// TestFindSessionMarkerByPID_MatchesParentPID is the #527/#529 regression
// guard for PID-based marker fallback: a second prime call that lacks a
// native agent_session_id (e.g. invoked from a CLAUDE.md BLOCKING
// instruction with no hook stdin) must still locate the marker the
// hook-driven prime wrote earlier in the same agent process. Without this,
// the second prime falls through to fresh-prime, appending a duplicate row
// to agent_instances.jsonl.
//
// Uses the current test process PID so the proc.IsAlive liveness gate
// passes — a recycled/dead PID is specifically what the gate filters out.
// That PID is shared with every other test in the package, so the marker
// namespace is isolated first; see isolateSessionMarkerDir.
func TestFindSessionMarkerByPID_MatchesParentPID(t *testing.T) {
	isolateSessionMarkerDir(t)
	agentPID := os.Getpid()
	sessionID := "findbyPIDtest_" + time.Now().Format("20060102150405.000")

	marker := &SessionMarker{
		AgentID:        "OxFindByPID",
		AgentSessionID: sessionID,
		PrimedAt:       time.Now().Truncate(time.Second),
		ParentPID:      agentPID,
	}
	require.NoError(t, WriteSessionMarker(marker))
	t.Cleanup(func() { DeleteSessionMarker(sessionID) })

	found := FindSessionMarkerByPID(agentPID)
	require.NotNil(t, found, "marker with matching ParentPID must be found")
	assert.Equal(t, "OxFindByPID", found.AgentID)
	assert.Equal(t, sessionID, found.AgentSessionID)
}

// TestFindSessionMarkerByPID_IgnoresDeadParentPID is the liveness regression
// guard: markers whose ParentPID no longer corresponds to a running process
// must not be returned, even if their PID field matches the query. Stale
// markers from crashed sessions are a primary source of cross-session
// identity bleed — see code-reviewer round 2 SUGGESTION on #527 PID fallback.
//
// Spawns a short-lived child process and records its PID, then Wait()s to
// reap it so we have a deterministically-dead PID to assert against —
// strictly safer than a "probably unused" integer which could flake on
// machines with long-running processes holding that PID.
func TestFindSessionMarkerByPID_IgnoresDeadParentPID(t *testing.T) {
	// exec.Command("true") is POSIX-only. Windows has no /usr/bin/true,
	// so on that platform skip rather than fabricate a fake PID that
	// would defeat the liveness gate under test. The live-PID happy
	// path is already covered by TestFindSessionMarkerByPID_MatchesParentPID.
	if runtime.GOOS == "windows" {
		t.Skip("test requires POSIX `true` to spawn-and-reap a guaranteed-dead PID")
	}
	isolateSessionMarkerDir(t)

	cmd := exec.Command("true")
	require.NoError(t, cmd.Start())
	deadPID := cmd.Process.Pid
	require.NoError(t, cmd.Wait()) // reap — PID is now guaranteed dead

	sessionID := "findbyPIDdead_" + time.Now().Format("20060102150405.000")
	marker := &SessionMarker{
		AgentID:        "OxDead",
		AgentSessionID: sessionID,
		PrimedAt:       time.Now().Truncate(time.Second),
		ParentPID:      deadPID,
	}
	require.NoError(t, WriteSessionMarker(marker))
	t.Cleanup(func() { DeleteSessionMarker(sessionID) })

	got := FindSessionMarkerByPID(deadPID)
	assert.Nil(t, got, "marker whose ParentPID is not alive must be rejected")
}

// TestFindSessionMarkerByPID_IgnoresNonMatchingPID confirms the scan is
// strict: unrelated markers on the system must not be returned for a PID
// they don't reference.
func TestFindSessionMarkerByPID_IgnoresNonMatchingPID(t *testing.T) {
	isolateSessionMarkerDir(t)
	sessionID := "findbyPIDnomatch_" + time.Now().Format("20060102150405.000")
	marker := &SessionMarker{
		AgentID:        "OxOther",
		AgentSessionID: sessionID,
		PrimedAt:       time.Now().Truncate(time.Second),
		ParentPID:      111111,
	}
	require.NoError(t, WriteSessionMarker(marker))
	t.Cleanup(func() { DeleteSessionMarker(sessionID) })

	// query for a PID that no marker references
	got := FindSessionMarkerByPID(222222)
	assert.Nil(t, got, "must not return a marker whose ParentPID differs")
}

// TestFindSessionMarkerByPID_RejectsInvalidPID guards against matching
// placeholder / unset PID values (0, negative) against a stored marker
// that happens to record PID 0 from an earlier buggy write.
func TestFindSessionMarkerByPID_RejectsInvalidPID(t *testing.T) {
	isolateSessionMarkerDir(t)
	assert.Nil(t, FindSessionMarkerByPID(0))
	assert.Nil(t, FindSessionMarkerByPID(-1))
}

// TestFindSessionMarkerByPID_IgnoresMarkerFromAnotherTestsDir is the
// regression guard for isolateSessionMarkerDir itself. It reconstructs the
// exact conditions that made this suite flaky: a marker sitting in the
// SHARED global dir carrying the same ParentPID (os.Getpid(), which every
// test in the package shares) under a session ID that sorts EARLIER, so an
// un-isolated os.ReadDir scan reaches it first.
//
// Failure prevented: the flake returning. Remove the isolateSessionMarkerDir
// call below and this fails deterministically with AgentID "OxForeign" —
// which is how the original intermittent failure was diagnosed. Note that a
// test which merely writes one marker and reads it back would keep passing
// throughout, which is why the flake survived this long.
func TestFindSessionMarkerByPID_IgnoresMarkerFromAnotherTestsDir(t *testing.T) {
	agentPID := os.Getpid()

	// Plant BEFORE isolating, so it lands in the shared dir that other tests
	// in this package really do write to. "aaa" sorts ahead of "own".
	foreignID := "aaa_foreign_marker_from_another_test"
	require.NoError(t, WriteSessionMarker(&SessionMarker{
		AgentID:        "OxForeign",
		AgentSessionID: foreignID,
		PrimedAt:       time.Now().Truncate(time.Second),
		ParentPID:      agentPID,
	}))
	// capture the absolute path now: once USER is overridden below,
	// markerPath resolves somewhere else entirely and could not clean this up
	foreignPath := markerPath(foreignID)
	t.Cleanup(func() { _ = os.Remove(foreignPath) })

	isolateSessionMarkerDir(t)

	ownID := "own_marker_under_test"
	require.NoError(t, WriteSessionMarker(&SessionMarker{
		AgentID:        "OxOwn",
		AgentSessionID: ownID,
		PrimedAt:       time.Now().Truncate(time.Second),
		ParentPID:      agentPID,
	}))
	t.Cleanup(func() { DeleteSessionMarker(ownID) })

	found := FindSessionMarkerByPID(agentPID)
	require.NotNil(t, found, "the isolated dir's own marker must still be found")
	assert.Equal(t, "OxOwn", found.AgentID,
		"a same-PID marker in the shared global dir must not leak into an isolated test")
}
