package agentwork

import (
	"context"
	"errors"
	"fmt"
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
	// skipped is the reason a skip was last logged for (skipCatchUp): a
	// recording waiting on a hook is logged once per reason, not every sweep.
	skipped string
	at      time.Time
}

// hookTouch names a recording a local hook reported: its folder, durable
// identity and agent, as the marker the hook captures with records them.
type hookTouch struct {
	sessionPath, sessionID, agentID string
	// refused keys the one warning logged for a marker ox did not start here
	// (refuseUnstarted), not an armed recording. Its sessionPath is the folder
	// the marker was found in: a forged marker can claim a real one's.
	refused bool
}

func touchOf(st *session.RecordingState) hookTouch {
	return hookTouch{sessionPath: st.SessionPath, sessionID: st.SessionID, agentID: st.AgentID}
}

func refusedTouchOf(mk session.RecordingMarker) hookTouch {
	return hookTouch{sessionPath: mk.Dir, sessionID: mk.State.SessionID, agentID: mk.State.AgentID, refused: true}
}

// NoteHook arms the catch-up for agentID's recording in projectRoot. The
// daemon calls it for every agent heartbeat, which every ox hook sends, and
// so does any other ox command: a heartbeat says the agent is alive, not that
// a hook ran.
//
// What makes a recording safe to catch up is where it came from. Markers
// live in places a repository writer controls (the Ledger checkout, the
// legacy sessions/ folder), and one can reuse an active agent's ID. Only a
// recording ox started on this machine, in the folder it was started in, is
// armed (session.LoadRecordingMarkers). Any other marker naming agentID is
// left alone with one warning (refuseUnstarted), and so is every recording an
// older ox started.
//
// The catch-up acts on armed recordings only (HookNoted). The set lives in
// memory: after a daemon restart a recording is caught up again once its next
// heartbeat arrives.
func (m *SessionWatcherManager) NoteHook(ctx context.Context, projectRoot, agentID string) {
	if projectRoot == "" || agentID == "" {
		return
	}
	markers, err := session.LoadRecordingMarkers(projectRoot)
	if err != nil {
		m.logger.DebugContext(ctx, "session_capture_catchup_skipped", "reason", "cannot_list_recordings", "agent_id", agentID, "error", err)
		return
	}
	for _, mk := range markers {
		st := mk.State
		if st.AgentID != agentID || st.SourceRejected || st.SessionPath == "" {
			continue
		}
		if !mk.Started {
			if hookCatchUpCandidate(st) {
				m.refuseUnstarted(ctx, mk)
			}
			continue
		}
		touch := touchOf(st)
		m.mu.Lock()
		if m.hookTouched == nil {
			m.hookTouched = make(map[hookTouch]time.Time)
		}
		_, armed := m.hookTouched[touch]
		m.hookTouched[touch] = time.Now()
		m.mu.Unlock()
		if !armed {
			m.logger.DebugContext(ctx, "session_capture_catchup_armed", "session_id", st.SessionID, "agent_id", agentID, "session", filepath.Base(st.SessionPath))
		}
	}
}

// HookNoted reports whether a local hook has reported st since the daemon
// started (NoteHook).
func (m *SessionWatcherManager) HookNoted(st *session.RecordingState) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.hookTouched[touchOf(st)]
	return ok
}

// pruneHookTouches forgets recordings, and refusals, whose marker is gone. One
// noted after the sweep listed the markers is kept: its marker may postdate
// the list.
func (m *SessionWatcherManager) pruneHookTouches(present map[hookTouch]bool, listedAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for touch, at := range m.hookTouched {
		if !present[touch] && at.Before(listedAt) {
			delete(m.hookTouched, touch)
		}
	}
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
	listedAt := time.Now()
	markers, err := session.LoadRecordingMarkers(projectRoot)
	if err != nil {
		m.logger.DebugContext(ctx, "session_capture_catchup_skipped", "reason", "cannot_list_recordings", "error", err)
		return 0
	}
	present := make(map[hookTouch]bool, len(markers))
	for _, mk := range markers {
		if mk.Started {
			present[touchOf(mk.State)] = true
		} else {
			present[refusedTouchOf(mk)] = true
		}
	}
	m.pruneHookTouches(present, listedAt)
	active := make(map[string]bool, len(markers))
	caught := 0
	for _, mk := range markers {
		if ctx.Err() != nil {
			break
		}
		st := mk.State
		if !hookCatchUpCandidate(st) {
			continue
		}
		if !mk.Started {
			// checked before anything is read or locked: the marker, its
			// session_path included, is whoever wrote it
			m.refuseUnstarted(ctx, mk)
			continue
		}
		active[st.SessionPath] = true
		if m.catchUpHookRecording(ctx, st, mk.Source) {
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
//
// Only Claude Code, whatever alias names it, is caught up: CaptureHookEntries
// runs its ownership checks and no other adapter's. Cursor has its own drain
// (session.DrainCursorSource) with its own source validation.
func hookCatchUpCandidate(st *session.RecordingState) bool {
	return st.WatchMode == "hook" && st.StoppedAt == nil && !st.SourceRejected &&
		adapters.CanonicalAdapterName(st.AdapterName) == "claude-code" &&
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
//
// bound is the transcript a local hook found for the recording
// (session.RecordingMarker.Source); only that one is read.
func (m *SessionWatcherManager) catchUpHookRecording(ctx context.Context, st *session.RecordingState, bound string) bool {
	ids := []any{"session_id", st.SessionID, "agent_id", st.AgentID, "session", filepath.Base(st.SessionPath)}
	if !m.HookNoted(st) {
		m.skipCatchUp(ctx, st.SessionPath, ids, "no_local_hook")
		return false
	}
	// The swept markers include the Ledger checkout's and legacy sessions/
	// folders', which this daemon never wrote: read only a transcript the tail
	// watcher would accept (startWatchAt).
	source, err := adapters.SafeSessionFilePath(st.AdapterName, st.SessionFile, m.homeDir())
	if err != nil {
		m.refuseHookCatchUp(ctx, st.SessionPath, ids, err)
		return false
	}
	// Even this recording's marker can be rewritten, and another Claude Code
	// transcript under the home directory passes the check above. Read only
	// the one a local hook found for it. Under the lock the capture reads the
	// marker again, and Settled refuses any transcript other than this one.
	if bound == "" {
		m.skipCatchUp(ctx, st.SessionPath, ids, "no_transcript_bound")
		return false // its next hook discovers and binds one
	}
	if source != bound {
		m.refuseHookCatchUp(ctx, st.SessionPath, ids, fmt.Errorf("%w: marker names %s, a hook found %s", errSourceNotBound, source, bound))
		return false
	}
	// CaptureHookEntries refuses this too, under the lock. Checked here first,
	// the refusal is warned once rather than on every sweep.
	rawPath := filepath.Join(st.SessionPath, "raw.jsonl")
	if err := session.CheckRawRegular(rawPath); err != nil {
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
	acquired, err := fileutil.TryWithFileLock(rawPath, func() error {
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
	case capture.Status == session.HookStatusSourceRefused || capture.Status == session.HookStatusRawRefused:
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

// errSourceNotBound refuses a marker whose transcript is not the one a local
// hook found for its recording.
var errSourceNotBound = errors.New("transcript is not the one a local hook found for this recording")

// skipCatchUp leaves a recording waiting on its next hook: none has reported
// it since the daemon started (NoteHook), or none has found its transcript
// yet. Logged once per recording and reason, at Debug: right after a daemon
// restart every active recording waits for its next hook, which is normal.
func (m *SessionWatcherManager) skipCatchUp(ctx context.Context, sessionPath string, ids []any, reason string) {
	m.mu.Lock()
	logged := m.hookMarks[sessionPath].skipped == reason
	if !logged {
		if m.hookMarks == nil {
			m.hookMarks = make(map[string]hookCatchUpMark)
		}
		m.hookMarks[sessionPath] = hookCatchUpMark{skipped: reason, at: time.Now()}
	}
	m.mu.Unlock()
	if !logged {
		m.logger.DebugContext(ctx, "session_capture_catchup_skipped", append(ids, "reason", reason)...)
	}
}

// refuseUnstarted leaves a marker ox did not start in the folder it is in:
// planted, or a recording an older ox started. It warns once per marker, not
// every sweep or heartbeat. Nothing of it is read, locked or written.
func (m *SessionWatcherManager) refuseUnstarted(ctx context.Context, mk session.RecordingMarker) {
	touch := refusedTouchOf(mk)
	m.mu.Lock()
	if m.hookTouched == nil {
		m.hookTouched = make(map[hookTouch]time.Time)
	}
	_, warned := m.hookTouched[touch]
	m.hookTouched[touch] = time.Now()
	m.mu.Unlock()
	if !warned {
		m.logger.WarnContext(ctx, "session_capture_catchup_refused", "reason", "not_started_here",
			"session_id", mk.State.SessionID, "agent_id", mk.State.AgentID, "marker_dir", mk.Dir)
	}
}

// refuseHookCatchUp skips a recording whose transcript is outside its
// adapter's allowed roots, or whose raw.jsonl is not a regular file, warning
// once per recording rather than every sweep.
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
	return ok && !mark.refused && mark.skipped == "" && mark.offset == offset && mark.size == size &&
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
