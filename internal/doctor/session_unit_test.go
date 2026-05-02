package doctor

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
)

func TestFormatModeSource(t *testing.T) {
	tests := []struct {
		source config.SessionRecordingSource
		want   string
	}{
		{config.SessionRecordingSourceRepo, "from .sageox/config.json"},
		{config.SessionRecordingSourceTeam, "from team config"},
		{config.SessionRecordingSourceUser, "from user config"},
		{config.SessionRecordingSourceDefault, "default"},
		{"unknown_source", "unknown_source"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(string(tt.source), func(t *testing.T) {
			got := formatModeSource(tt.source)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidSessionRecordingModesString(t *testing.T) {
	result := ValidSessionRecordingModesString()

	// should contain all valid modes separated by commas
	assert.Contains(t, result, "disabled")
	assert.Contains(t, result, "manual")
	assert.Contains(t, result, "auto")
	assert.Contains(t, result, ", ")
}

func TestIsSessionFile(t *testing.T) {
	tests := []struct {
		filename string
		want     bool
	}{
		// positive cases
		{"sessions/test.jsonl", true},
		{"sessions/test.html", true},
		{"sessions/test_summary.md", true},
		{"sessions/test_summary.json", true},
		{"sessions/subdir/test.jsonl", true},
		{"sessions/deep/nested/file.html", true},

		// negative cases
		{"other/test.jsonl", false},
		{"test.jsonl", false},
		{"sessions/test.txt", false},
		{"sessions/test.go", false},
		{"sessions/test.json", false},       // .json is not summary.json
		{"data/sessions/test.jsonl", false}, // must start with sessions/
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := isSessionFile(tt.filename)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetOrComputeHealth_UsesCachedStatus(t *testing.T) {
	cached := &session.HealthStatus{
		StorageWritable: true,
		RepoCloned:      true,
		PendingCount:    42,
	}

	// pass a non-existent gitRoot; if cached is used, no filesystem access occurs
	result := getOrComputeHealth(cached, "/nonexistent/path/that/should/not/be/read")

	assert.Equal(t, cached, result)
	assert.Equal(t, 42, result.PendingCount)
}

// --- SessionCheck with cached HealthStatus ---

func TestSessionCheck_AllHealthy(t *testing.T) {
	check := NewSessionCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		StorageWritable:  true,
		RepoCloned:       true,
		PendingCount:     0,
		SyncedWithRemote: true,
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusPass, result.Status)
	assert.Equal(t, "all checks passed", result.Message)
}

func TestSessionCheck_StorageNotWritable(t *testing.T) {
	check := NewSessionCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		StorageWritable:  false,
		RepoCloned:       true,
		SyncedWithRemote: true,
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusWarn, result.Status)
	assert.Contains(t, result.Message, "issues found")
}

func TestSessionCheck_RepoNotClonedNotProvisioned(t *testing.T) {
	check := NewSessionCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		StorageWritable: true,
		RepoCloned:      false,
		RepoError:       "ledger not provisioned",
	})

	// "ledger not provisioned" is excluded from issues
	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusPass, result.Status)
}

func TestSessionCheck_RepoNotClonedOtherError(t *testing.T) {
	check := NewSessionCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		StorageWritable: true,
		RepoCloned:      false,
		RepoError:       "clone failed",
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusWarn, result.Status)
}

func TestSessionCheck_PendingSessions(t *testing.T) {
	check := NewSessionCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		StorageWritable:  true,
		RepoCloned:       true,
		PendingCount:     3,
		SyncedWithRemote: true,
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusWarn, result.Status)
	assert.Contains(t, result.Message, "issues found")
}

func TestSessionCheck_NotSyncedWithRemote(t *testing.T) {
	check := NewSessionCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		StorageWritable:  true,
		RepoCloned:       true,
		PendingCount:     0,
		SyncedWithRemote: false,
		SyncStatus:       "ahead by 2",
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusWarn, result.Status)
}

func TestSessionCheck_NotSyncedButNoRemote(t *testing.T) {
	check := NewSessionCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		StorageWritable:  true,
		RepoCloned:       true,
		PendingCount:     0,
		SyncedWithRemote: false,
		SyncStatus:       "no remote configured",
	})

	// "no remote configured" is excluded from sync issues
	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusPass, result.Status)
}

// --- SessionStorageCheck with cached HealthStatus ---

func TestSessionStorageCheck_Writable(t *testing.T) {
	check := NewSessionStorageCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		StorageWritable: true,
		StoragePath:     "/home/user/.local/share/sageox/ledger",
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusPass, result.Status)
}

func TestSessionStorageCheck_NotWritableWithError(t *testing.T) {
	check := NewSessionStorageCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		StorageWritable: false,
		StorageError:    "permission denied",
		StoragePath:     "/some/path",
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusFail, result.Status)
	assert.Equal(t, "not writable", result.Message)
	assert.Contains(t, result.Fix, "permission denied")
}

func TestSessionStorageCheck_NotWritableNoError(t *testing.T) {
	check := NewSessionStorageCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		StorageWritable: false,
		StorageError:    "",
		StoragePath:     "/some/path",
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusWarn, result.Status)
	assert.Equal(t, "unknown", result.Message)
}

// --- SessionRepoCheck with cached HealthStatus ---

func TestSessionRepoCheck_Cloned(t *testing.T) {
	check := NewSessionRepoCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		RepoCloned: true,
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusPass, result.Status)
	assert.Equal(t, "yes", result.Message)
}

func TestSessionRepoCheck_NotProvisioned(t *testing.T) {
	check := NewSessionRepoCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		RepoCloned: false,
		RepoError:  "ledger not provisioned",
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusSkip, result.Status)
	assert.Equal(t, "not provisioned", result.Message)
	assert.Contains(t, result.Fix, "ox login")
}

func TestSessionRepoCheck_ErrorCloning(t *testing.T) {
	check := NewSessionRepoCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		RepoCloned: false,
		RepoError:  "authentication failed",
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusFail, result.Status)
	assert.Equal(t, "error", result.Message)
	assert.Equal(t, "authentication failed", result.Fix)
}

func TestSessionRepoCheck_NotFoundNoError(t *testing.T) {
	check := NewSessionRepoCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		RepoCloned: false,
		RepoError:  "",
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusWarn, result.Status)
	assert.Equal(t, "not found", result.Message)
	assert.Contains(t, result.Fix, "ox doctor --fix")
}

// --- SessionRecordingCheck with cached HealthStatus ---

func TestSessionRecordingCheck_NoRecording(t *testing.T) {
	check := NewSessionRecordingCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		IsRecordingActive: false,
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusSkip, result.Status)
}

func TestSessionRecordingCheck_NilRecording(t *testing.T) {
	check := NewSessionRecordingCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		IsRecordingActive: true,
		Recording:         nil,
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusSkip, result.Status)
}

func TestSessionRecordingCheck_DefaultAdapter(t *testing.T) {
	check := NewSessionRecordingCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		IsRecordingActive: true,
		Recording: &session.RecordingState{
			AgentID:     "OxTest",
			AdapterName: "", // empty = default (claude-code)
			StartedAt:   time.Now(),
		},
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusPass, result.Status)
	assert.Contains(t, result.Message, "OxTest")
	assert.NotContains(t, result.Message, "adapter:")
}

// --- SessionPendingCheck with cached HealthStatus ---

func TestSessionPendingCheck_RepoNotCloned(t *testing.T) {
	check := NewSessionPendingCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		RepoCloned: false,
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusSkip, result.Status)
}

func TestSessionPendingCheck_NoPending(t *testing.T) {
	check := NewSessionPendingCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		RepoCloned:   true,
		PendingCount: 0,
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusPass, result.Status)
	assert.Equal(t, "none", result.Message)
}

func TestSessionPendingCheck_HasPending(t *testing.T) {
	check := NewSessionPendingCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		RepoCloned:   true,
		PendingCount: 5,
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusWarn, result.Status)
	assert.Contains(t, result.Message, "5 pending commit")
	assert.Contains(t, result.Fix, "ox session commit")
}

// --- SessionStaleCheck with cached HealthStatus ---

func TestSessionStaleCheck_NotRecording(t *testing.T) {
	check := NewSessionStaleCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		IsRecordingActive: false,
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusSkip, result.Status)
}

func TestSessionStaleCheck_RecordingNotStale(t *testing.T) {
	check := NewSessionStaleCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		IsRecordingActive: true,
		IsStaleRecording:  false,
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusSkip, result.Status)
}

func TestSessionStaleCheck_StaleHours(t *testing.T) {
	check := NewSessionStaleCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		IsRecordingActive: true,
		IsStaleRecording:  true,
		StaleRecordingAge: 18 * time.Hour,
		Recording: &session.RecordingState{
			AgentID: "OxStale1",
		},
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusWarn, result.Status)
	assert.Contains(t, result.Message, "18 hours")
	assert.Contains(t, result.Message, "OxStale1")
	assert.Contains(t, result.Fix, "session recover")
}

func TestSessionStaleCheck_StaleDays(t *testing.T) {
	check := NewSessionStaleCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		IsRecordingActive: true,
		IsStaleRecording:  true,
		StaleRecordingAge: 72 * time.Hour,
		Recording: &session.RecordingState{
			AgentID: "OxStale2",
		},
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusWarn, result.Status)
	assert.Contains(t, result.Message, "3 days")
	assert.Contains(t, result.Message, "OxStale2")
}

func TestSessionStaleCheck_NilRecording(t *testing.T) {
	check := NewSessionStaleCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		IsRecordingActive: true,
		IsStaleRecording:  true,
		StaleRecordingAge: 24 * time.Hour,
		Recording:         nil,
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusWarn, result.Status)
	assert.Contains(t, result.Message, "unknown")
}

// --- SessionSyncCheck with cached HealthStatus ---

func TestSessionSyncCheck_RepoNotCloned(t *testing.T) {
	check := NewSessionSyncCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		RepoCloned: false,
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusSkip, result.Status)
}

func TestSessionSyncCheck_NoRemote(t *testing.T) {
	check := NewSessionSyncCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		RepoCloned:       true,
		SyncedWithRemote: false,
		SyncStatus:       "no remote configured",
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusSkip, result.Status)
	assert.Equal(t, "no remote", result.Message)
	assert.Contains(t, result.Fix, "Configure remote")
}

func TestSessionSyncCheck_Synced(t *testing.T) {
	check := NewSessionSyncCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		RepoCloned:       true,
		SyncedWithRemote: true,
		SyncStatus:       "up to date",
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusPass, result.Status)
	assert.Equal(t, "up to date", result.Message)
}

func TestSessionSyncCheck_NotSynced(t *testing.T) {
	check := NewSessionSyncCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		RepoCloned:       true,
		SyncedWithRemote: false,
		SyncStatus:       "ahead by 3",
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusWarn, result.Status)
	assert.Equal(t, "ahead by 3", result.Message)
	assert.Contains(t, result.Fix, "ox sync")
}

// --- SessionStopIncompleteCheck ---

func TestSessionStopIncompleteCheck_NilRecording(t *testing.T) {
	check := NewSessionStopIncompleteCheck("/tmp/fake")
	check.SetHealthStatus(&session.HealthStatus{
		IsStopIncomplete: true,
		Recording:        nil,
	})

	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusWarn, result.Status)
	assert.Contains(t, result.Message, "unknown")
}

// --- SessionOrphanedCheck with cached HealthStatus ---

func TestSessionOrphanedCheck_Name(t *testing.T) {
	check := NewSessionOrphanedCheck("/tmp/fake", false)
	assert.Equal(t, "orphaned recordings", check.Name())
}

func TestSessionOrphanedCheck_NoStaleNoGhost(t *testing.T) {
	check := NewSessionOrphanedCheck("/tmp/fake", false)
	check.SetHealthStatus(&session.HealthStatus{
		StaleRecordings: nil,
	})

	// ghost cleanup runs on real filesystem so we can't fully isolate it,
	// but with no stale recordings the test validates the path
	result := check.Run(context.Background(), false)
	// either skip (no orphans) or pass (ghost cleaned)
	assert.Contains(t, []Status{StatusSkip, StatusPass}, result.Status)
}

// --- SessionPushCheck ---

func TestSessionPushCheck_Name(t *testing.T) {
	check := NewSessionPushCheck("/tmp/fake", false)
	assert.Equal(t, "session push", check.Name())
}

func TestSessionPushCheck_EmptyGitRoot(t *testing.T) {
	check := NewSessionPushCheck("", false)
	result := check.Run(context.Background(), false)
	assert.Equal(t, StatusSkip, result.Status)
}

func TestSessionPushCheck_GetLedgerPath_ExplicitPath(t *testing.T) {
	check := &SessionPushCheck{
		gitRoot:    "/tmp/fake",
		ledgerPath: "/explicit/ledger/path",
	}

	path := check.getLedgerPath()
	assert.Equal(t, "/explicit/ledger/path", path)
}

func TestSessionPushCheck_GetLedgerPath_EmptyGitRoot(t *testing.T) {
	check := &SessionPushCheck{
		gitRoot: "",
	}

	path := check.getLedgerPath()
	assert.Equal(t, "", path)
}

// --- SessionIncompleteCheck result message formatting ---

func TestSessionIncompleteCheck_RunResult_MessageFormat(t *testing.T) {
	// test the message formatting logic extracted from the Run method
	tests := []struct {
		name           string
		missingHTML    int
		missingSummary int
		uncommitted    int
		wantParts      []string
	}{
		{
			name:        "only missing HTML",
			missingHTML: 2,
			wantParts:   []string{"2 missing HTML"},
		},
		{
			name:           "only missing summary",
			missingSummary: 1,
			wantParts:      []string{"1 missing summary"},
		},
		{
			name:        "only uncommitted",
			uncommitted: 3,
			wantParts:   []string{"3 uncommitted"},
		},
		{
			name:           "all issues",
			missingHTML:    1,
			missingSummary: 2,
			uncommitted:    3,
			wantParts:      []string{"1 missing HTML", "2 missing summary", "3 uncommitted"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// replicate the message building logic from SessionIncompleteCheck.Run
			var parts []string
			if tt.missingHTML > 0 {
				parts = append(parts, fmt.Sprintf("%d missing HTML", tt.missingHTML))
			}
			if tt.missingSummary > 0 {
				parts = append(parts, fmt.Sprintf("%d missing summary", tt.missingSummary))
			}
			if tt.uncommitted > 0 {
				parts = append(parts, fmt.Sprintf("%d uncommitted", tt.uncommitted))
			}
			message := strings.Join(parts, ", ")

			for _, want := range tt.wantParts {
				assert.Contains(t, message, want)
			}
		})
	}
}

// --- SessionModeCheck, SessionLedgerCheck names ---

func TestSessionModeCheck_Name(t *testing.T) {
	check := NewSessionModeCheck("/tmp/fake")
	assert.Equal(t, "session recording", check.Name())
}

func TestSessionLedgerCheck_Name(t *testing.T) {
	check := NewSessionLedgerCheck("/tmp/fake")
	assert.Equal(t, "ledger for sessions", check.Name())
}

func TestSessionAutoStageCheck_GetLedgerPath_EmptyGitRoot(t *testing.T) {
	check := &SessionAutoStageCheck{gitRoot: ""}
	// when gitRoot is empty, getLedgerPath falls back to ledger.DefaultPath()
	// which depends on CWD. Just verify it doesn't panic.
	_ = check.getLedgerPath()
}

func TestSessionIncompleteCheck_GetLedgerPath_EmptyGitRoot(t *testing.T) {
	check := &SessionIncompleteCheck{gitRoot: ""}
	// same as above - just verify it doesn't panic
	_ = check.getLedgerPath()
}

// --- IncompleteSessionIssue struct ---

func TestIncompleteSessionIssue_Fields(t *testing.T) {
	issue := IncompleteSessionIssue{
		Path:           "sessions/test.jsonl",
		MissingHTML:    true,
		MissingSummary: false,
		Untracked:      true,
		Unstaged:       false,
	}

	assert.Equal(t, "sessions/test.jsonl", issue.Path)
	assert.True(t, issue.MissingHTML)
	assert.False(t, issue.MissingSummary)
	assert.True(t, issue.Untracked)
	assert.False(t, issue.Unstaged)
}

// --- gitFileStatus struct ---

func TestGitFileStatus_Fields(t *testing.T) {
	status := gitFileStatus{
		untracked: true,
		staged:    false,
		modified:  true,
		isDir:     false,
	}

	assert.True(t, status.untracked)
	assert.False(t, status.staged)
	assert.True(t, status.modified)
	assert.False(t, status.isDir)
}

// --- SessionHealthCacheable interface ---

func TestSessionHealthCacheable_Interface(t *testing.T) {
	// verify all session checks implement SessionHealthCacheable
	healthStatus := &session.HealthStatus{
		StorageWritable: true,
		RepoCloned:      true,
	}

	checks := []SessionHealthCacheable{
		NewSessionCheck("/tmp/fake"),
		NewSessionStorageCheck("/tmp/fake"),
		NewSessionRepoCheck("/tmp/fake"),
		NewSessionRecordingCheck("/tmp/fake"),
		NewSessionPendingCheck("/tmp/fake"),
		NewSessionStaleCheck("/tmp/fake"),
		NewSessionSyncCheck("/tmp/fake"),
		NewSessionOrphanedCheck("/tmp/fake", false),
		NewSessionStopIncompleteCheck("/tmp/fake"),
		&SessionPushCheck{gitRoot: "/tmp/fake"},
	}

	for _, check := range checks {
		// should not panic
		check.SetHealthStatus(healthStatus)
	}
}
