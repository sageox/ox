package doctor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Category() coverage for all check types ---
// 24 Category() methods sit at 0% coverage. Covering them catches
// regressions if category grouping changes.

func TestAllDaemonCheckCategories(t *testing.T) {
	checks := []struct {
		name  string
		check Check
	}{
		{"DaemonRunningCheck", NewDaemonRunningCheck()},
		{"DaemonResponsiveCheck", NewDaemonResponsiveCheck()},
		{"DaemonSyncStatusCheck", NewDaemonSyncStatusCheck()},
		{"DaemonUptimeCheck", NewDaemonUptimeCheck()},
		{"DaemonSyncErrorsCheck", NewDaemonSyncErrorsCheck()},
		{"DaemonDirtyTeamContextCheck", NewDaemonDirtyTeamContextCheck()},
		{"DaemonHeartbeatCheck", NewDaemonHeartbeatCheck("workspace", "repo_ws", "test", "ep")},
	}

	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, "Daemon", tc.check.Category())
		})
	}
}

func TestAllSessionCheckCategories(t *testing.T) {
	checks := []struct {
		name  string
		check Check
	}{
		{"SessionCheck", NewSessionCheck("/tmp/fake")},
		{"SessionStorageCheck", NewSessionStorageCheck("/tmp/fake")},
		{"SessionRepoCheck", NewSessionRepoCheck("/tmp/fake")},
		{"SessionRecordingCheck", NewSessionRecordingCheck("/tmp/fake")},
		{"SessionPendingCheck", NewSessionPendingCheck("/tmp/fake")},
		{"SessionStaleCheck", NewSessionStaleCheck("/tmp/fake")},
		{"SessionSyncCheck", NewSessionSyncCheck("/tmp/fake")},
		{"SessionModeCheck", NewSessionModeCheck("/tmp/fake")},
		{"SessionLedgerCheck", NewSessionLedgerCheck("/tmp/fake")},
		{"SessionOrphanedCheck", NewSessionOrphanedCheck("/tmp/fake", false)},
		{"SessionStopIncompleteCheck", NewSessionStopIncompleteCheck("/tmp/fake")},
		{"SessionIncompleteCheck", NewSessionIncompleteCheck("/tmp/fake")},
		{"SessionAutoStageCheck", NewSessionAutoStageCheck("/tmp/fake")},
		{"SessionPushCheck", NewSessionPushCheck("/tmp/fake", false)},
	}

	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, "Sessions", tc.check.Category())
		})
	}
}

// --- SessionStopIncompleteCheck ---

func TestSessionStopIncompleteCheck_NotIncomplete(t *testing.T) {
	check := NewSessionStopIncompleteCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		IsStopIncomplete: false,
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusSkip, result.Status)
}

func TestSessionStopIncompleteCheck_WithRecording(t *testing.T) {
	check := NewSessionStopIncompleteCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		IsStopIncomplete: true,
		Recording: &session.RecordingState{
			AgentID: "OxAgent42",
		},
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusWarn, result.Status)
	assert.Contains(t, result.Message, "OxAgent42")
	assert.Contains(t, result.Fix, "OxAgent42")
}

// --- SessionRecordingCheck: adapter display logic ---

func TestSessionRecordingCheck_CustomAdapter(t *testing.T) {
	check := NewSessionRecordingCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		IsRecordingActive: true,
		Recording: &session.RecordingState{
			AgentID:     "OxCustom",
			AdapterName: "gemini-cli",
			StartedAt:   time.Now(),
		},
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusPass, result.Status)
	assert.Contains(t, result.Message, "adapter: gemini-cli")
	assert.Contains(t, result.Message, "OxCustom")
}

func TestSessionRecordingCheck_ClaudeCodeAdapter(t *testing.T) {
	// claude-code is the default adapter and should NOT show "adapter:" in output
	check := NewSessionRecordingCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		IsRecordingActive: true,
		Recording: &session.RecordingState{
			AgentID:     "OxDefault",
			AdapterName: "claude-code",
			StartedAt:   time.Now(),
		},
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusPass, result.Status)
	assert.NotContains(t, result.Message, "adapter:")
}

// --- SessionCheck: multiple simultaneous issues ---

func TestSessionCheck_MultipleIssues(t *testing.T) {
	check := NewSessionCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		StorageWritable:  false,
		RepoCloned:       false,
		RepoError:        "clone failed",
		PendingCount:     2,
		SyncedWithRemote: false,
		SyncStatus:       "behind by 5",
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusWarn, result.Status)
	assert.Contains(t, result.Message, "issues found")
}

// --- checkSessionFile: filesystem-based tests for real edge cases ---

func TestCheckSessionFile_AllCompanionsPresent(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	jsonlPath := filepath.Join(sessionsDir, "test-session.jsonl")
	require.NoError(t, os.WriteFile(jsonlPath, []byte(`{"type":"user"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "test-session.html"), []byte("<html></html>"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "test-session_summary.md"), []byte("# Summary"), 0o644))

	check := &SessionIncompleteCheck{gitRoot: tmpDir}
	issue := check.checkSessionFile(jsonlPath, tmpDir, map[string]gitFileStatus{}, nil)
	assert.Nil(t, issue)
}

func TestCheckSessionFile_MissingHTML(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	jsonlPath := filepath.Join(sessionsDir, "test-session.jsonl")
	require.NoError(t, os.WriteFile(jsonlPath, []byte(`{"type":"user"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "test-session_summary.md"), []byte("# Summary"), 0o644))

	check := &SessionIncompleteCheck{gitRoot: tmpDir}
	issue := check.checkSessionFile(jsonlPath, tmpDir, map[string]gitFileStatus{}, nil)

	require.NotNil(t, issue)
	assert.True(t, issue.MissingHTML)
	assert.False(t, issue.MissingSummary)
}

func TestCheckSessionFile_MissingSummary(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	jsonlPath := filepath.Join(sessionsDir, "test-session.jsonl")
	require.NoError(t, os.WriteFile(jsonlPath, []byte(`{"type":"user"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "test-session.html"), []byte("<html></html>"), 0o644))

	check := &SessionIncompleteCheck{gitRoot: tmpDir}
	issue := check.checkSessionFile(jsonlPath, tmpDir, map[string]gitFileStatus{}, nil)

	require.NotNil(t, issue)
	assert.False(t, issue.MissingHTML)
	assert.True(t, issue.MissingSummary)
}

func TestCheckSessionFile_BothCompanionsMissing(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	jsonlPath := filepath.Join(sessionsDir, "test-session.jsonl")
	require.NoError(t, os.WriteFile(jsonlPath, []byte(`{"type":"user"}`), 0o644))

	check := &SessionIncompleteCheck{gitRoot: tmpDir}
	issue := check.checkSessionFile(jsonlPath, tmpDir, map[string]gitFileStatus{}, nil)

	require.NotNil(t, issue)
	assert.True(t, issue.MissingHTML)
	assert.True(t, issue.MissingSummary)
}

func TestCheckSessionFile_UntrackedFile(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	jsonlPath := filepath.Join(sessionsDir, "test-session.jsonl")
	require.NoError(t, os.WriteFile(jsonlPath, []byte(`{"type":"user"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "test-session.html"), []byte("<html></html>"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "test-session_summary.md"), []byte("# Summary"), 0o644))

	statuses := map[string]gitFileStatus{
		"sessions/test-session.jsonl": {untracked: true},
	}

	check := &SessionIncompleteCheck{gitRoot: tmpDir}
	issue := check.checkSessionFile(jsonlPath, tmpDir, statuses, nil)

	require.NotNil(t, issue)
	assert.True(t, issue.Untracked)
	assert.False(t, issue.Unstaged)
}

func TestCheckSessionFile_UnstagedModified(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	jsonlPath := filepath.Join(sessionsDir, "test-session.jsonl")
	require.NoError(t, os.WriteFile(jsonlPath, []byte(`{"type":"user"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "test-session.html"), []byte("<html></html>"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "test-session_summary.md"), []byte("# Summary"), 0o644))

	statuses := map[string]gitFileStatus{
		"sessions/test-session.jsonl": {modified: true, staged: false},
	}

	check := &SessionIncompleteCheck{gitRoot: tmpDir}
	issue := check.checkSessionFile(jsonlPath, tmpDir, statuses, nil)

	require.NotNil(t, issue)
	assert.False(t, issue.Untracked)
	assert.True(t, issue.Unstaged)
}

func TestCheckSessionFile_StagedFileNoIssue(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	jsonlPath := filepath.Join(sessionsDir, "test-session.jsonl")
	require.NoError(t, os.WriteFile(jsonlPath, []byte(`{"type":"user"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "test-session.html"), []byte("<html></html>"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "test-session_summary.md"), []byte("# Summary"), 0o644))

	statuses := map[string]gitFileStatus{
		"sessions/test-session.jsonl": {modified: true, staged: true},
	}

	check := &SessionIncompleteCheck{gitRoot: tmpDir}
	issue := check.checkSessionFile(jsonlPath, tmpDir, statuses, nil)
	assert.Nil(t, issue)
}

func TestCheckSessionFile_UntrackedDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	jsonlPath := filepath.Join(sessionsDir, "test-session.jsonl")
	require.NoError(t, os.WriteFile(jsonlPath, []byte(`{"type":"user"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "test-session.html"), []byte("<html></html>"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "test-session_summary.md"), []byte("# Summary"), 0o644))

	check := &SessionIncompleteCheck{gitRoot: tmpDir}
	issue := check.checkSessionFile(jsonlPath, tmpDir, map[string]gitFileStatus{}, []string{"sessions/"})

	require.NotNil(t, issue)
	assert.True(t, issue.Untracked)
}

// --- findIncompleteSessionsInLedger ---

func TestFindIncompleteSessionsInLedger_NoSessionsDir(t *testing.T) {
	tmpDir := t.TempDir()
	check := &SessionIncompleteCheck{gitRoot: tmpDir}
	issues := check.findIncompleteSessionsInLedger(tmpDir)
	assert.Nil(t, issues)
}

// --- hasRemote and countCommitsAhead with real git repos ---

func TestHasRemote_NoRemote(t *testing.T) {
	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)

	check := &SessionPushCheck{gitRoot: tmpDir}
	assert.False(t, check.hasRemote(tmpDir))
}

func TestHasRemote_WithRemote(t *testing.T) {
	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)

	runTestGit(t, tmpDir, "remote", "add", "origin", "https://example.com/repo.git")
	assert.True(t, (&SessionPushCheck{}).hasRemote(tmpDir))
}

func TestCountCommitsAhead_NoRemote(t *testing.T) {
	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	makeTestCommit(t, tmpDir, "initial-commit")

	check := &SessionPushCheck{}
	assert.Equal(t, 0, check.countCommitsAhead(tmpDir))
}

func TestCountCommitsAhead_DetachedHead(t *testing.T) {
	tmpDir := t.TempDir()
	initTestGitRepo(t, tmpDir)
	makeTestCommit(t, tmpDir, "initial")

	runTestGit(t, tmpDir, "checkout", "--detach")

	check := &SessionPushCheck{}
	assert.Equal(t, 0, check.countCommitsAhead(tmpDir))
}

// --- SessionPushCheck.getLedgerPath with cached status ---

func TestSessionPushCheck_GetLedgerPath_FromCachedStatus(t *testing.T) {
	check := &SessionPushCheck{
		gitRoot: "/tmp/fake",
		cachedStatus: &session.HealthStatus{
			RepoCloned: true,
			RepoPath:   "/some/ledger/path",
		},
	}

	path := check.getLedgerPath()
	assert.Equal(t, "/some/ledger/path", path)
}

func TestSessionPushCheck_GetLedgerPath_RepoNotCloned(t *testing.T) {
	check := &SessionPushCheck{
		gitRoot: "/tmp/fake",
		cachedStatus: &session.HealthStatus{
			RepoCloned: false,
		},
	}

	path := check.getLedgerPath()
	assert.Equal(t, "", path)
}

// --- Runner ---

func TestRunner_MultipleCategories(t *testing.T) {
	runner := NewRunner()
	runner.Register(&mockCheck{
		name:     "check1",
		category: "Alpha",
		result:   CheckResult{Name: "check1", Status: StatusPass, Message: "ok"},
	})
	runner.Register(&mockCheck{
		name:     "check2",
		category: "Beta",
		result:   CheckResult{Name: "check2", Status: StatusFail, Message: "bad"},
	})

	results := runner.RunAll(context.Background(), false)
	assert.Len(t, results, 2)
	assert.Equal(t, StatusPass, results[0].Status)
	assert.Equal(t, StatusFail, results[1].Status)
}

func TestRunner_Empty(t *testing.T) {
	runner := NewRunner()
	results := runner.RunAll(context.Background(), true)
	assert.Empty(t, results)
}

// --- SessionOrphanedCheck: stale recordings path ---

func TestSessionOrphanedCheck_StaleWithContentNoFix(t *testing.T) {
	check := NewSessionOrphanedCheck("/tmp/fake", false)
	check.SetHealthStatus(&session.HealthStatus{
		StaleRecordings:   []*session.RecordingState{{AgentID: "agent1"}, {AgentID: "agent2"}},
		EmptyStaleCount:   1,
		ContentStaleCount: 1,
	})

	result := check.Run(context.Background(), false)
	assert.Contains(t, []Status{StatusWarn, StatusSkip, StatusPass}, result.Status)
}

// --- SessionModeCheck.Run: exercises config.ResolveSessionRecording ---

func TestSessionModeCheck_Run_DefaultMode(t *testing.T) {
	// temp dir with no .sageox/ -- ResolveSessionRecording returns manual/default
	tmpDir := t.TempDir()
	check := NewSessionModeCheck(tmpDir)
	result := check.Run(context.Background(), false)

	assert.Equal(t, StatusPass, result.Status)
	assert.NotEmpty(t, result.Message)
}

// --- SessionOrphanedCheck.Run with fix=true: filesystem cleanup ---

func TestSessionOrphanedCheck_FixEmptyStaleRecording(t *testing.T) {
	tmpDir := t.TempDir()

	// empty stub older than 48h threshold
	sessionDir := filepath.Join(tmpDir, "sessions", "stale-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	recPath := filepath.Join(sessionDir, ".recording.json")
	require.NoError(t, os.WriteFile(recPath, []byte(`{"agent_id":"test"}`), 0o644))

	staleTime := time.Now().Add(-72 * time.Hour)

	check := NewSessionOrphanedCheck(tmpDir, true)
	check.SetHealthStatus(&session.HealthStatus{
		StaleRecordings: []*session.RecordingState{
			{
				AgentID:     "test-agent",
				SessionPath: sessionDir,
				StartedAt:   staleTime,
			},
		},
		EmptyStaleCount:   1,
		ContentStaleCount: 0,
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusPass, result.Status)
	assert.Contains(t, result.Message, "cleaned")

	_, err := os.Stat(recPath)
	assert.True(t, os.IsNotExist(err), ".recording.json should be removed")
}

func TestSessionOrphanedCheck_FixContentRecording(t *testing.T) {
	tmpDir := t.TempDir()

	sessionDir := filepath.Join(tmpDir, "sessions", "content-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	recPath := filepath.Join(sessionDir, ".recording.json")
	rawPath := filepath.Join(sessionDir, "raw.jsonl")
	require.NoError(t, os.WriteFile(recPath, []byte(`{"agent_id":"test"}`), 0o644))
	require.NoError(t, os.WriteFile(rawPath, []byte(`{"type":"user","content":"hello"}`), 0o644))

	check := NewSessionOrphanedCheck(tmpDir, true)
	check.SetHealthStatus(&session.HealthStatus{
		StaleRecordings: []*session.RecordingState{
			{
				AgentID:     "test-agent",
				SessionPath: sessionDir,
				StartedAt:   time.Now().Add(-24 * time.Hour),
			},
		},
		EmptyStaleCount:   0,
		ContentStaleCount: 1,
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusPass, result.Status)
	assert.Contains(t, result.Message, "with content cleared")

	// .recording.json removed but raw.jsonl preserved for recovery
	_, err := os.Stat(recPath)
	assert.True(t, os.IsNotExist(err), ".recording.json should be removed")
	_, err = os.Stat(rawPath)
	assert.False(t, os.IsNotExist(err), "raw.jsonl should be preserved")
}

func TestSessionOrphanedCheck_FixEmptyStubTooRecent(t *testing.T) {
	tmpDir := t.TempDir()

	sessionDir := filepath.Join(tmpDir, "sessions", "recent-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	recPath := filepath.Join(sessionDir, ".recording.json")
	require.NoError(t, os.WriteFile(recPath, []byte(`{"agent_id":"test"}`), 0o644))

	check := NewSessionOrphanedCheck(tmpDir, true)
	check.SetHealthStatus(&session.HealthStatus{
		StaleRecordings: []*session.RecordingState{
			{
				AgentID:     "test-agent",
				SessionPath: sessionDir,
				StartedAt:   time.Now().Add(-12 * time.Hour), // too recent for 48h threshold
			},
		},
		EmptyStaleCount:   1,
		ContentStaleCount: 0,
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusPass, result.Status)

	// should NOT be cleaned (too recent)
	_, err := os.Stat(recPath)
	assert.False(t, os.IsNotExist(err), ".recording.json should NOT be removed for recent stubs")
}

func TestSessionOrphanedCheck_FixEmptySessionPath(t *testing.T) {
	tmpDir := t.TempDir()

	check := NewSessionOrphanedCheck(tmpDir, true)
	check.SetHealthStatus(&session.HealthStatus{
		StaleRecordings: []*session.RecordingState{
			{
				AgentID:     "test-agent",
				SessionPath: "", // empty path -- should be skipped
				StartedAt:   time.Now().Add(-72 * time.Hour),
			},
		},
		EmptyStaleCount:   1,
		ContentStaleCount: 0,
	})

	// should not panic
	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusPass, result.Status)
}

// --- SessionIncompleteCheck.getLedgerPath ---

func TestSessionIncompleteCheck_GetLedgerPath_WithGitRoot(t *testing.T) {
	tmpDir := t.TempDir()
	check := &SessionIncompleteCheck{gitRoot: tmpDir}
	path := check.getLedgerPath()
	// no .sageox/config.json, so ledgerPathFromProject returns ""
	assert.Equal(t, "", path)
}

// --- DaemonBootstrapGrace ---

func TestDaemonBootstrapGrace(t *testing.T) {
	assert.Equal(t, 3*time.Minute, DaemonBootstrapGrace)
}

// --- helper functions for git-based tests ---

func initTestGitRepo(t *testing.T, dir string) {
	t.Helper()
	runTestGit(t, dir, "init")
	runTestGit(t, dir, "config", "user.email", "test@example.com")
	runTestGit(t, dir, "config", "user.name", "Test User")
}

func makeTestCommit(t *testing.T, dir, msg string) {
	t.Helper()
	f := filepath.Join(dir, msg+".txt")
	require.NoError(t, os.WriteFile(f, []byte(msg), 0o644))
	runTestGit(t, dir, "add", f)
	runTestGit(t, dir, "commit", "-m", msg)
}

func runTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}
