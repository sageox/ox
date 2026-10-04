package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionRecoverFromCachePreservesOriginalSessionName(t *testing.T) {
	projectRoot := t.TempDir()
	runGit(t, projectRoot, "init")
	t.Chdir(projectRoot)
	t.Setenv("OX_PROJECT_ROOT", projectRoot)
	t.Setenv("SAGEOX_ENDPOINT", "https://test-only-no-creds.invalid")

	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0o755))

	const originalName = "2001-02-03T04-05-historic-OxRcvr"
	cacheRoot := filepath.Join(projectRoot, "sessions")
	sessionPath := filepath.Join(cacheRoot, originalName)
	require.NoError(t, os.MkdirAll(sessionPath, 0o755))
	raw := []byte(`{"type":"header","metadata":{"agent_id":"OxRcvr","session_id":"ses_019d0000-0000-7000-8000-000000000916","created_at":"2001-02-03T04:05:00Z"}}` + "\n" +
		`{"type":"user","content":"please recover the original name"}` + "\n" +
		`{"type":"assistant","content":"recovered"}` + "\n" +
		`{"type":"footer","entry_count":2}` + "\n")
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, ledgerFileRaw), raw, 0o600))

	startedAt := time.Date(2001, 2, 3, 4, 5, 0, 0, time.UTC)
	require.NoError(t, session.SaveRecordingState(projectRoot, &session.RecordingState{
		AgentID:     "OxRcvr",
		AdapterName: "claude-code",
		SessionID:   "ses_019d0000-0000-7000-8000-000000000916",
		SessionPath: sessionPath,
		StartedAt:   startedAt,
	}))

	var recoverErr error
	out := captureRealStdout(t, func() {
		recoverErr = runAgentSessionRecover(&agentinstance.Instance{AgentID: "OxRcvr"})
	})
	require.NoError(t, recoverErr)

	var got sessionRecoverOutput
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, originalName, got.SessionName,
		"recovery must report the original cache directory name, not a fresh now()-based identity")
	assert.Equal(t, filepath.Join(sessionPath, ledgerFileRaw), got.RawPath)
	assert.False(t, got.Uploaded, "precondition: no ledger is configured, so the test isolates recovery naming")

	entries, readErr := os.ReadDir(cacheRoot)
	require.NoError(t, readErr)
	require.Len(t, entries, 1, "recovery must not create a second date-based cache identity")
	assert.Equal(t, originalName, entries[0].Name())
	assert.NoFileExists(t, filepath.Join(sessionPath, ".recording.json"), "recovery still clears stale recording state")
}

// recoverCollisionFixture is a project whose Ledger may already hold a session
// under the cache directory's name -- two sessions started in one minute share it.
type recoverCollisionFixture struct {
	projectRoot      string
	ledgerPath       string
	sessionPath      string
	ledgerSessionDir string
	recoveredRaw     []byte
}

const (
	collisionSessionName = "2001-02-03T04-05-historic-OxRcvr"
	collisionRecoveredID = "ses_019d0000-0000-7000-8000-000000000916"
	collisionOtherID     = "ses_019d0000-0000-7000-8000-000000000999"
	collisionExistingRaw = "existing session raw sentinel\n"
	collisionSidecarName = "sentinel.txt"
	collisionSidecar     = "existing session sidecar sentinel\n"
)

// newRecoverCollisionFixture stages a recoverable cache recording. recordingID
// is carried by BOTH the recording state and the raw header ("" is a recording
// that predates session IDs). destination, when non-nil, is committed to the
// Ledger under the same session name.
func newRecoverCollisionFixture(t *testing.T, recordingID string, destination *lfs.SessionMeta) *recoverCollisionFixture {
	t.Helper()
	projectRoot, ledgerPath := setupLedgerProject(t)
	runGit(t, ledgerPath, "init")
	runGit(t, ledgerPath, "config", "user.email", "test@example.com")
	runGit(t, ledgerPath, "config", "user.name", "Test")
	t.Chdir(projectRoot)
	t.Setenv("OX_PROJECT_ROOT", projectRoot)
	// loopback and discard port: nothing here may reach a real host
	t.Setenv("SAGEOX_ENDPOINT", "http://127.0.0.1:9")

	f := &recoverCollisionFixture{
		projectRoot:      projectRoot,
		ledgerPath:       ledgerPath,
		sessionPath:      filepath.Join(projectRoot, "sessions", collisionSessionName),
		ledgerSessionDir: filepath.Join(ledgerPath, "sessions", collisionSessionName),
	}

	// the Ledger needs a commit to compare HEAD against, with or without a destination
	require.NoError(t, os.WriteFile(filepath.Join(ledgerPath, "README.md"), []byte("ledger\n"), 0o600))
	if destination != nil {
		require.NoError(t, os.MkdirAll(f.ledgerSessionDir, 0o755))
		destination.SessionName = collisionSessionName
		if !destination.IsDraft() {
			// a draft directory holds only meta.json; a finalized one holds artifacts
			destination.Files = map[string]lfs.FileRef{ledgerFileRaw: lfs.NewGitFileRef(int64(len(collisionExistingRaw)))}
		}
		require.NoError(t, lfs.WriteSessionMetaOnly(f.ledgerSessionDir, destination))
		if !destination.IsDraft() {
			require.NoError(t, os.WriteFile(filepath.Join(f.ledgerSessionDir, ledgerFileRaw), []byte(collisionExistingRaw), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(f.ledgerSessionDir, collisionSidecarName), []byte(collisionSidecar), 0o600))
		}
	}
	runGit(t, ledgerPath, "add", ".")
	runGit(t, ledgerPath, "commit", "--no-verify", "-m", "ledger before recovery")

	headerID := ""
	if recordingID != "" {
		headerID = fmt.Sprintf(`"session_id":%q,`, recordingID)
	}
	f.recoveredRaw = []byte(`{"type":"header","metadata":{"agent_id":"OxRcvr",` + headerID + `"created_at":"2001-02-03T04:05:00Z"}}` + "\n" +
		`{"type":"user","content":"please recover the original name"}` + "\n" +
		`{"type":"assistant","content":"recovered"}` + "\n" +
		`{"type":"footer","entry_count":2}` + "\n")
	require.NoError(t, os.MkdirAll(f.sessionPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.sessionPath, ledgerFileRaw), f.recoveredRaw, 0o600))
	require.NoError(t, session.SaveRecordingState(projectRoot, &session.RecordingState{
		AgentID:     "OxRcvr",
		AdapterName: "claude-code",
		SessionID:   recordingID,
		SessionPath: f.sessionPath,
		StartedAt:   time.Date(2001, 2, 3, 4, 5, 0, 0, time.UTC),
	}))
	return f
}

// snapshotDestination reads every file under the Ledger session directory so a
// refused recovery can prove it left each byte alone.
func snapshotDestination(t *testing.T, dir string) map[string]string {
	t.Helper()
	got := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if errors.Is(walkErr, fs.ErrNotExist) {
			return nil // nothing there yet
		}
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		rel, err := filepath.Rel(dir, path)
		require.NoError(t, err)
		got[rel] = string(data)
		return nil
	}))
	return got
}

// TestSessionRecoverFromCacheGuardsExistingLedgerSession is the customer promise:
// recovering an interrupted session never overwrites a different finalized
// session that shares its name -- and never guesses when ownership is unprovable.
// Failure prevented: a coworker's finished session, already in the Ledger,
// replaced by an unrelated recording and reported as a successful recovery.
// The observable difference: the Ledger directory is byte-identical, git is
// untouched, the recoverable state survives, and doctor is flagged.
func TestSessionRecoverFromCacheGuardsExistingLedgerSession(t *testing.T) {
	finalized := func(id string) *lfs.SessionMeta {
		return &lfs.SessionMeta{
			SessionID:  id,
			AgentID:    "OxOther",
			AgentType:  "claude-code",
			CreatedAt:  time.Date(2001, 2, 3, 4, 5, 0, 0, time.UTC),
			Title:      "existing finalized session",
			EntryCount: 1,
		}
	}
	draft := func(id string) *lfs.SessionMeta {
		meta := finalized(id)
		meta.Draft = true
		return meta
	}

	tests := []struct {
		name        string
		destination *lfs.SessionMeta // nil: nothing in the Ledger under this name
		recordingID string           // "" is a recording that predates session IDs
		wantRefused bool
	}{
		{name: "no destination", destination: nil, recordingID: collisionRecoveredID},
		{name: "no destination, recording without ID", destination: nil, recordingID: ""},
		{name: "matching IDs retry an interrupted recovery", destination: finalized(collisionRecoveredID), recordingID: collisionRecoveredID},
		{name: "draft placeholder with the same ID", destination: draft(collisionRecoveredID), recordingID: collisionRecoveredID},
		{name: "draft placeholder carries the ID for a recording without one", destination: draft(collisionRecoveredID), recordingID: ""},
		{name: "recovered ID empty vs finalized with ID", destination: finalized(collisionOtherID), recordingID: "", wantRefused: true},
		{name: "legacy metadata without ID vs recovered ID", destination: finalized(""), recordingID: collisionRecoveredID, wantRefused: true},
		{name: "legacy metadata without ID vs recording without ID", destination: finalized(""), recordingID: "", wantRefused: true},
		{name: "different IDs", destination: finalized(collisionOtherID), recordingID: collisionRecoveredID, wantRefused: true},
		{name: "draft placeholder of a different session", destination: draft(collisionOtherID), recordingID: collisionRecoveredID, wantRefused: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRecoverCollisionFixture(t, tt.recordingID, tt.destination)
			beforeHead := runGit(t, f.ledgerPath, "rev-parse", "HEAD")
			beforeDestination := snapshotDestination(t, filepath.Join(f.ledgerPath, "sessions"))

			var recoverErr error
			out := captureRealStdout(t, func() {
				recoverErr = runAgentSessionRecover(&agentinstance.Instance{AgentID: "OxRcvr"})
			})

			statePath := filepath.Join(f.sessionPath, ".recording.json")
			afterDestination := snapshotDestination(t, filepath.Join(f.ledgerPath, "sessions"))

			if tt.wantRefused {
				require.ErrorIs(t, recoverErr, errRecoveryRefused)
				assert.Empty(t, out, "a refused recovery must not emit success JSON")
				assert.Equal(t, beforeDestination, afterDestination, "recovery must not touch the existing Ledger session")
				assert.Equal(t, beforeHead, runGit(t, f.ledgerPath, "rev-parse", "HEAD"), "recovery must not commit")
				assert.Empty(t, runGit(t, f.ledgerPath, "status", "--porcelain"), "recovery must not stage or leave files in the Ledger")
				assert.FileExists(t, statePath, "a refused recovery must keep the state for a safe retry")
				assert.True(t, doctor.NeedsDoctorAgent(f.projectRoot), "a refused recovery must flag the project for doctor")
				return
			}

			require.NoError(t, recoverErr)
			var got sessionRecoverOutput
			require.NoError(t, json.Unmarshal(out, &got))
			assert.Equal(t, collisionSessionName, got.SessionName)
			recovered, err := os.ReadFile(filepath.Join(f.ledgerSessionDir, ledgerFileRaw))
			require.NoError(t, err)
			assert.Equal(t, string(f.recoveredRaw), string(recovered), "an allowed recovery writes the recording under its original name")
			assert.NoFileExists(t, statePath, "a completed recovery retires the recording state")
		})
	}
}

// TestSessionRecoverFromCacheRefusesNonPlainSessionName covers the trust
// boundary the PR introduces: the Ledger key now comes from the session_path a
// .recording.json reports, not from a generated name.
// Failure prevented: a state whose path ends in ".." addressing the Ledger root
// (a draft purge RemoveAlls whatever the name resolves to).
func TestSessionRecoverFromCacheRefusesNonPlainSessionName(t *testing.T) {
	f := newRecoverCollisionFixture(t, collisionRecoveredID, nil)
	beforeHead := runGit(t, f.ledgerPath, "rev-parse", "HEAD")

	// "<folder>/work/.." is the folder itself, so the state and raw.jsonl are
	// found where recovery looks, but the path's base name is ".."
	folder := filepath.Join(f.projectRoot, "sessions", "traversal")
	require.NoError(t, os.MkdirAll(filepath.Join(folder, "work"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(folder, ledgerFileRaw), f.recoveredRaw, 0o600))
	require.NoError(t, session.SaveRecordingState(f.projectRoot, &session.RecordingState{
		AgentID:     "OxTrav",
		AdapterName: "claude-code",
		SessionID:   collisionRecoveredID,
		SessionPath: folder + string(filepath.Separator) + "work" + string(filepath.Separator) + "..",
		StartedAt:   time.Date(2001, 2, 3, 4, 5, 0, 0, time.UTC),
	}))

	var recoverErr error
	out := captureRealStdout(t, func() {
		recoverErr = runAgentSessionRecover(&agentinstance.Instance{AgentID: "OxTrav"})
	})

	require.ErrorIs(t, recoverErr, errRecoveryRefused)
	assert.Empty(t, out)
	assert.Equal(t, beforeHead, runGit(t, f.ledgerPath, "rev-parse", "HEAD"))
	assert.NoFileExists(t, filepath.Join(f.ledgerPath, ledgerFileRaw), "nothing may be written at the Ledger root")
	assert.FileExists(t, filepath.Join(folder, ".recording.json"), "a refused recovery keeps the state")
	assert.True(t, doctor.NeedsDoctorAgent(f.projectRoot))
}

// TestSessionRecoverFromCacheRefusalSanitizesUntrustedMetadata covers the
// refusal text for a meta.json a teammate pushed: its parse errors echo its own
// content, and recovery's error is printed to a terminal.
// Failure prevented: an escape sequence in a Ledger meta.json reaching the
// recovering coworker's terminal through the refusal message.
func TestSessionRecoverFromCacheRefusalSanitizesUntrustedMetadata(t *testing.T) {
	f := newRecoverCollisionFixture(t, collisionRecoveredID, nil)

	// a files key that is an absolute path, so ReadSessionMeta rejects it and
	// quotes the key unescaped; the key carries a screen-clear escape
	require.NoError(t, os.MkdirAll(f.ledgerSessionDir, 0o755))
	hostile := `{"session_name":"` + collisionSessionName + `","session_id":"` + collisionOtherID + `","files":{"/\u001b[2Jpwned":{"storage":"git","size":1}}}`
	require.NoError(t, os.WriteFile(filepath.Join(f.ledgerSessionDir, "meta.json"), []byte(hostile), 0o644))
	before := snapshotDestination(t, f.ledgerSessionDir)

	var recoverErr error
	out := captureRealStdout(t, func() {
		recoverErr = runAgentSessionRecover(&agentinstance.Instance{AgentID: "OxRcvr"})
	})

	require.ErrorIs(t, recoverErr, errRecoveryRefused)
	assert.NotContains(t, recoverErr.Error(), "\x1b", "the refusal must not carry a raw escape byte")
	assert.Contains(t, recoverErr.Error(), "pwned", "the sanitized message should still name the offending key")
	assert.Empty(t, out)
	assert.Equal(t, before, snapshotDestination(t, f.ledgerSessionDir), "recovery must not touch an unreadable Ledger session")
	assert.FileExists(t, filepath.Join(f.sessionPath, ".recording.json"))
	assert.True(t, doctor.NeedsDoctorAgent(f.projectRoot))
}

// TestCheckRecoveryDestinationSessionName pins which cache directory names may
// become a Ledger path. Only a name that addresses something other than one
// session is refused; the destination does not exist in any row.
func TestCheckRecoveryDestinationSessionName(t *testing.T) {
	tests := []struct {
		name        string
		sessionName string
		wantRefused bool
	}{
		{name: "generated name", sessionName: collisionSessionName},
		{name: "dots inside a username are still one session", sessionName: "2001-02-03T04-05-first..last-OxRcvr"},
		{name: "empty", sessionName: "", wantRefused: true},
		{name: "current directory", sessionName: ".", wantRefused: true},
		{name: "parent directory", sessionName: "..", wantRefused: true},
		{name: "forward slash", sessionName: "a/b", wantRefused: true},
		{name: "backslash", sessionName: `a\b`, wantRefused: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkRecoveryDestination(filepath.Join(t.TempDir(), "sessions", "absent"), tt.sessionName, collisionRecoveredID)
			if tt.wantRefused {
				require.ErrorIs(t, err, errRecoveryRefused)
				return
			}
			require.NoError(t, err)
		})
	}
}
