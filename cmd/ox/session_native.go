package main

import (
	"errors"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
)

// recordNativeSessionForRecording notes that the agent reported native
// session id nativeID (with SessionStart reason source, or "") for agentID's
// live recording. Called after every start-of-session entry point — the
// SessionStart hook and `ox agent prime` — so the recording's NativeSessions
// list tracks the agent across startup, resume, /clear and compact whether
// or not the recording itself was restarted.
//
// Best-effort by contract: no live recording (recording disabled, manual
// mode, ledger missing) is a silent no-op, and a write failure is logged.
// An empty nativeID is a no-op too — agents without a native id record an
// empty list, never an error.
func recordNativeSessionForRecording(projectRoot, agentID, nativeID, source string) {
	if projectRoot == "" || agentID == "" || nativeID == "" {
		return
	}
	now := time.Now().UTC()
	err := session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		s.RecordNativeSession(nativeID, source, now)
	})
	if err != nil && !errors.Is(err, session.ErrNotRecording) {
		slog.Warn("could not record native session id on recording", "agent_id", agentID, "native_session_id", nativeID, "error", err)
	}
}

// stampRecordingCarrierAtStop appends the recording's native session ids and
// its stop time to raw.jsonl as a footer record. Called by the finalize doors
// that clear .recording.json before the daemon finalizes (SessionEnd, /clear):
// once the state file is gone that record is the only carrier the daemon can
// read those fields from. Appended, never rewritten — the file may still have
// open appenders. Best-effort: a stamp failure is logged, never surfaced into
// the hook, and meta.json still gets whatever the daemon can resolve alone.
func stampRecordingCarrierAtStop(state *session.RecordingState, stoppedAt time.Time) {
	if state == nil || state.SessionPath == "" {
		return
	}
	rawPath := filepath.Join(state.SessionPath, ledgerFileRaw)
	if err := session.StampRawCarrier(rawPath, session.CarrierStamp{
		NativeSessions: state.NativeSessions,
		StoppedAt:      stoppedAt,
	}); err != nil {
		slog.Debug("could not stamp raw.jsonl carrier at stop", "session", state.SessionPath, "error", err)
	}
}

// requestedStopTime is the stop time the explicit-stop door hands to
// session.ResolveStoppedAt: the one carried in the live state when the stop
// was asked for, else the one a prior attempt already wrote into this
// session's meta.json. A retry after a failed LFS upload or push must
// resolve the same instant as the attempt before it — otherwise meta.json
// changes between attempts and every retry needs a fresh commit. Nil when
// neither exists, so the resolver falls through to the recording itself.
func requestedStopTime(state *session.RecordingState, sessionDir string) *time.Time {
	if state != nil && state.StoppedAt != nil && !state.StoppedAt.IsZero() {
		return state.StoppedAt
	}
	if existing, err := lfs.ReadSessionMeta(sessionDir); err == nil && existing != nil && existing.StoppedAt != nil && !existing.StoppedAt.IsZero() {
		return existing.StoppedAt
	}
	return nil
}
