package agentwork

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
)

// hookCatchUpInterval is how often the daemon looks for transcript bytes no
// hook captured. A hook-mode recording reads its transcript only when a hook
// fires, and Claude Code can fire its last Stop before the final answer is on
// disk; with no hook to follow, that answer was never read. Shorter delivers
// it sooner, but every sweep scans the session folders and stats each active
// transcript (an adapter read runs only when one grew). 5 s keeps a missed
// answer well inside ADR-247 D6's 60 s from write to visible.
const hookCatchUpInterval = 5 * time.Second

// hookCatchUpSettle is how long transcript bytes must sit unread before the
// daemon takes them. A hook fires within milliseconds of the write it is
// about, and only the hook has its stdin's tool error to attach to the batch,
// so the daemon reads only what the hooks left behind. It also keeps the
// daemon off the raw.jsonl lock while hooks are busy, and away from a record
// the agent is still writing.
const hookCatchUpSettle = 2 * time.Second

// hookCatchUpRetry is how long an unchanged transcript waits after a failed
// catch-up before it is read again: a failure that persists costs one adapter
// run and one warning per retry, not one per sweep.
const hookCatchUpRetry = 30 * time.Second

// hookCatchUpMark remembers a catch-up that consumed nothing. Claude Code
// writes records its adapter turns into no entries, and the cursor stays short
// of them until a real entry follows, so "the transcript is past the cursor"
// can stay true while idle. An unchanged cursor and size are not read again.
// A refused mark only records that the refusal was already logged.
type hookCatchUpMark struct {
	offset, size int64
	failed       bool
	refused      bool
	at           time.Time
}

// RunHookCatchUp catches up the hook-mode recordings of projectRoot's
// repository every hookCatchUpInterval until ctx ends.
func (m *SessionWatcherManager) RunHookCatchUp(ctx context.Context, projectRoot string) {
	ticker := time.NewTicker(hookCatchUpInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		m.CatchUpHookRecordings(ctx, projectRoot)
	}
}

// CatchUpHookRecordings runs one catch-up sweep and returns how many
// recordings it captured bytes for. Each capture is the hooks' own
// transaction (session.CaptureHookEntries) under the same raw.jsonl lock, so
// it redacts, appends, moves the cursor and spools exactly as a hook would,
// and a hook that ran first leaves it nothing to read twice.
func (m *SessionWatcherManager) CatchUpHookRecordings(ctx context.Context, projectRoot string) int {
	if projectRoot == "" {
		return 0
	}
	states, err := session.LoadAllRecordingStates(projectRoot)
	if err != nil {
		m.logger.DebugContext(ctx, "session_capture_catchup_skipped", "reason", "cannot_list_recordings", "error", err)
		return 0
	}
	active := make(map[string]bool, len(states))
	caught := 0
	for _, st := range states {
		if ctx.Err() != nil {
			break
		}
		if !hookCatchUpCandidate(st) {
			continue
		}
		active[st.SessionPath] = true
		if m.catchUpHookRecording(ctx, st) {
			caught++
		}
	}
	m.pruneHookMarks(active)
	return caught
}

// hookCatchUpCandidate is the cheap part of the eligibility check, made
// without the lock. CaptureHookEntries repeats the hook path's checks
// (explicit stop, StoppedAt, a changed or quarantined recording) under it.
// A held recording is skipped outright: held means kept as it is.
func hookCatchUpCandidate(st *session.RecordingState) bool {
	return st.WatchMode == "hook" && st.StoppedAt == nil && !st.SourceRejected &&
		st.AgentID != "" && st.SessionPath != "" && st.WorkspacePath != "" &&
		// only a file-backed transcript can be checked for growth with a stat;
		// an undiscovered one is left to the next hook, which discovers it
		filepath.IsAbs(st.SessionFile) &&
		!session.HasExplicitStop(st.WorkspacePath, st.AgentID) &&
		!session.IsHeld(st.SessionPath)
}

// catchUpHookRecording captures what st's transcript holds past its cursor,
// if it has settled and no hook is capturing it right now, and reports
// whether bytes were captured.
func (m *SessionWatcherManager) catchUpHookRecording(ctx context.Context, st *session.RecordingState) bool {
	ids := []any{"session_id", st.SessionID, "agent_id", st.AgentID, "session", filepath.Base(st.SessionPath)}
	// The swept markers include the Ledger checkout's and legacy sessions/
	// folders', which this daemon never wrote: read only a transcript the tail
	// watcher would accept (startWatchAt).
	source, err := adapters.SafeSessionFilePath(st.AdapterName, st.SessionFile, m.homeDir())
	if err != nil {
		m.refuseHookCatchUp(ctx, st.SessionPath, ids, err)
		return false
	}
	info, err := os.Stat(source)
	if err != nil {
		return false // gone or replaced: the hook path rediscovers it
	}
	offset := max(st.SourceOffset, st.StartOffset)
	if info.Size() <= offset || time.Since(info.ModTime()) < hookCatchUpSettle || m.hookMarkCurrent(st.SessionPath, offset, info.Size()) {
		return false
	}

	var capture session.HookCapture
	acquired, err := fileutil.TryWithFileLock(filepath.Join(st.SessionPath, "raw.jsonl"), func() error {
		var captureErr error
		capture, captureErr = session.CaptureHookEntries(ctx, st.WorkspacePath, st.AgentID, st.SessionPath, st.SessionID, session.HookCaptureOptions{
			// the lock reloads the recording, and a hook may have rediscovered its
			// transcript since: confine whatever the capture ends up reading
			SourceHome: m.homeDir(),
			// only bytes that sat unread since the stat above are the daemon's
			Settled: session.SettledSource{Path: source, Size: info.Size()},
			Spool: func(state *session.RecordingState, batch session.AppendedBatch) {
				m.forwardLive(&activeWatcher{cachePath: state.SessionPath, projectRoot: state.WorkspacePath, sessionName: filepath.Base(state.SessionPath)}, batch)
			},
			SpoolLost: func(state *session.RecordingState, n int64, cause error) {
				m.declareLiveLoss(ctx, state, n, cause)
			},
		})
		return captureErr
	})
	if err == nil && !acquired {
		return false // a hook or the stop holds it; never wait on either
	}

	switch {
	case capture.Status == session.HookStatusSourceRefused:
		m.refuseHookCatchUp(ctx, st.SessionPath, ids, err)
		return false
	case err != nil || capture.Failed():
		m.markHook(st.SessionPath, hookCatchUpMark{offset: offset, size: info.Size(), failed: true, at: time.Now()})
		m.logger.WarnContext(ctx, "session_capture_catchup_failed", append(ids, "status", capture.Status, "offset", offset, "error", err)...)
		return false
	case capture.Status == session.HookStatusNotReady || capture.Status == session.HookStatusUnsettled:
		// not idle: raw.jsonl waits for a hook to write its header, or the
		// transcript moved after the stat. Unmarked, the next sweep looks again.
		m.logger.DebugContext(ctx, "session_capture_catchup_deferred", append(ids, "status", capture.Status)...)
		return false
	case capture.ToOffset <= capture.FromOffset:
		m.markHook(st.SessionPath, hookCatchUpMark{offset: offset, size: info.Size(), at: time.Now()})
		return false
	}
	m.logger.InfoContext(ctx, "session_capture_catchup", append(ids,
		"bytes", capture.ToOffset-capture.FromOffset, "from_offset", capture.FromOffset, "to_offset", capture.ToOffset, "entries", capture.Entries)...)
	return true
}

// refuseHookCatchUp skips a recording whose transcript is outside its
// adapter's allowed roots, warning once per recording rather than every sweep.
func (m *SessionWatcherManager) refuseHookCatchUp(ctx context.Context, sessionPath string, ids []any, reason error) {
	m.mu.Lock()
	warned := m.hookMarks[sessionPath].refused
	if !warned {
		if m.hookMarks == nil {
			m.hookMarks = make(map[string]hookCatchUpMark)
		}
		m.hookMarks[sessionPath] = hookCatchUpMark{refused: true, at: time.Now()}
	}
	m.mu.Unlock()
	if !warned {
		m.logger.WarnContext(ctx, "session_capture_catchup_refused", append(ids, "reason", reason)...)
	}
}

// declareLiveLoss declares a batch the catch-up committed to raw.jsonl but
// could not hand to the live stream, under forwardLive's refusals: a batch it
// would not have spooled is not a loss.
func (m *SessionWatcherManager) declareLiveLoss(ctx context.Context, state *session.RecordingState, n int64, cause error) {
	m.mu.Lock()
	streams := m.liveSpool != nil
	m.mu.Unlock()
	if !streams || liveSpoolRefusal(state.SessionPath, state.WorkspacePath) != "" {
		return
	}
	// DeclareLost logs the loss, and any failure to record it, at Error itself
	_ = livesession.DeclareLost(ctx, state.SessionPath, n, cause)
}

// hookMarkCurrent reports whether the last catch-up of sessionPath already
// found nothing at this cursor and size (a failure only until it may retry).
func (m *SessionWatcherManager) hookMarkCurrent(sessionPath string, offset, size int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	mark, ok := m.hookMarks[sessionPath]
	return ok && !mark.refused && mark.offset == offset && mark.size == size &&
		(!mark.failed || time.Since(mark.at) < hookCatchUpRetry)
}

func (m *SessionWatcherManager) markHook(sessionPath string, mark hookCatchUpMark) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hookMarks == nil {
		m.hookMarks = make(map[string]hookCatchUpMark)
	}
	m.hookMarks[sessionPath] = mark
}

// pruneHookMarks forgets recordings that are no longer active.
func (m *SessionWatcherManager) pruneHookMarks(active map[string]bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for path := range m.hookMarks {
		if !active[path] {
			delete(m.hookMarks, path)
		}
	}
}
