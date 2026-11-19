package session

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/claudesource"
)

// HookStatusOK is the status of a capture that appended entries to raw.jsonl.
const HookStatusOK = "ok"

const (
	// HookStatusNotReady: raw.jsonl has no header yet and the caller has no
	// WriteHeader to start it with, so nothing was read. The next hook starts
	// it; the transcript is not idle.
	HookStatusNotReady = "raw-not-ready"
	// HookStatusUnsettled: the transcript changed after the caller saw it
	// settle (HookCaptureOptions.Settled), so nothing was committed.
	HookStatusUnsettled = "source-unsettled"
	// HookStatusSourceRefused: the transcript is outside its adapter's allowed
	// roots (HookCaptureOptions.SourceHome), so nothing was read.
	HookStatusSourceRefused = "source-not-allowed"
	// HookStatusSpoolLost: the entries reached raw.jsonl and the cursor moved,
	// but the batch could not be read back for the live stream, so it was
	// declared lost there (HookCaptureOptions.SpoolLost).
	HookStatusSpoolLost = "spool-lost"
)

// HookCaptureOptions holds what differs between the callers of
// CaptureHookEntries: a hook, which has the agent's stdin, and the daemon's
// catch-up, which has only the transcript.
type HookCaptureOptions struct {
	// ToolError is the failure a PostToolUse hook's stdin reported for the call
	// that just ended. The transcript does not carry it.
	ToolError string
	// WriteHeader writes raw.jsonl's header line when the file does not exist
	// yet. nil leaves such a recording for the next hook: an entry appended
	// first would leave raw.jsonl without its header for good.
	WriteHeader func(state *RecordingState) error
	// Spool hands a batch just committed to raw.jsonl to the live stream. It
	// runs only after the append succeeded.
	Spool func(state *RecordingState, batch AppendedBatch)
	// SpoolLost declares a batch that was committed to raw.jsonl but never
	// reached Spool, because it could not be read back, as a live-stream loss.
	// The cursor is already past its bytes, so no later capture sends them.
	// Set it alongside Spool.
	SpoolLost func(state *RecordingState, bytes int64, cause error)
	// MarkStatus records how the capture ended on the recording, inside the
	// state update the capture makes anyway. nil records nothing: the counters
	// `ox session status` shows describe hooks, not the daemon's reads.
	MarkStatus func(state *RecordingState, status string)
	// SourceHome, when set, confines the capture to a transcript under its
	// adapter's allowed roots in this home directory, symlinks resolved
	// (adapters.SafeSessionFilePath), as the tail watcher is confined. The
	// daemon sets it: the recordings it sweeps include markers it did not
	// write. A hook reads the transcript of the agent that ran it.
	SourceHome string
	// Settled bounds the capture to the transcript bytes the caller saw
	// settle. The zero value leaves the read unbounded.
	Settled SettledSource
}

// SettledSource is a transcript as a caller saw it settle. Adapters read to the
// end of the file, so the bound is enforced after the read: a read that ended
// past Size, or of a transcript other than Path, commits nothing
// (HookStatusUnsettled) and is left whole for the next capture.
type SettledSource struct {
	// Path is the transcript the caller stat'd, resolved as SourceHome resolves it.
	Path string
	// Size is the transcript's size then.
	Size int64
}

// HookCapture is what one CaptureHookEntries call did.
type HookCapture struct {
	// Status is the status the capture ended with (HookStatusOK,
	// "no-new-entries", "read-error", ...), or "" when it stopped before
	// reading: the recording stopped, changed, or has no raw.jsonl yet.
	Status string
	// FromOffset and ToOffset bound the transcript bytes the capture consumed;
	// they are equal when it consumed none.
	FromOffset, ToOffset int64
	// Entries is how many entries reached raw.jsonl.
	Entries int
}

// Failed reports a capture that could not read the transcript or append what
// it read. The cursor did not move, so the same bytes are read again later.
func (c HookCapture) Failed() bool {
	switch c.Status {
	case "", HookStatusOK, "no-new-entries", "no-entries-after-filter",
		HookStatusNotReady, HookStatusUnsettled, HookStatusSpoolLost:
		return false
	}
	return true
}

// CaptureHookEntries is the capture transaction of a hook-mode recording: it
// appends what the agent wrote to its transcript since SourceOffset to
// raw.jsonl, moves SourceOffset past it, and hands the batch to the live
// stream. Hooks run it after every tool call and turn; the daemon runs it to
// catch up on what the agent wrote after its last hook. Both go through this
// one function so redaction, the cursor and the spool cannot drift apart.
//
// The caller MUST hold the raw.jsonl lock (fileutil.WithFileLock on
// expectedSessionPath/raw.jsonl). State is reloaded under it, so two captures
// of one recording never read the same bytes twice.
//
// A stop/start cycle for this agent can swap in a new SessionPath between the
// caller's read and this reload (StartRecording can immediately mint a fresh
// session after a stale, incomplete stop). If that happens, the lock held is
// for the wrong raw.jsonl -- writing here would race whatever legitimately
// holds the new session's lock. Skip this invocation instead; the next call
// reloads state and locks the current session correctly.
//
// SessionPath alone is not a reliable generation check: it's minute-granular
// (GenerateSessionName), so a stop immediately followed by a restart for the
// same agent within the same minute mints an IDENTICAL path. expectedSessionID
// is the durable per-recording identity minted once at StartRecording, so it
// still distinguishes the two generations when the path collides; fall back
// to the path comparison only for a pre-SessionID recording (empty on both
// sides).
func CaptureHookEntries(ctx context.Context, projectRoot, agentID, expectedSessionPath, expectedSessionID string, opts HookCaptureOptions) (HookCapture, error) {
	if err := ctx.Err(); err != nil {
		return HookCapture{}, err
	}
	// The stop command sets this before waiting for the raw lock. A capture
	// queued behind its final drain must never append to finalized content.
	if HasExplicitStop(projectRoot, agentID) {
		return HookCapture{}, nil
	}
	state, err := LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil || state == nil || state.StoppedAt != nil {
		return HookCapture{}, err
	}
	changed := state.SessionPath != expectedSessionPath
	if expectedSessionID != "" || state.SessionID != "" {
		changed = state.SessionID != expectedSessionID
	}
	if changed {
		slog.Debug("hook: afterTool session changed since lock acquired, skipping", "agentID", agentID, "lockedPath", expectedSessionPath, "currentPath", state.SessionPath, "lockedSessionID", expectedSessionID, "currentSessionID", state.SessionID)
		return HookCapture{}, nil
	}
	if err := CheckNativeSourceBinding(state); err != nil {
		return HookCapture{}, err
	}
	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	if err := RecoverRawAppend(rawPath, state.SourceOffset); err != nil {
		return HookCapture{}, err
	}
	// Only a caller with WriteHeader can start a headerless raw.jsonl. Without
	// one, read nothing: the adapter run would be wasted, and an empty pass
	// would look like an idle transcript.
	if opts.WriteHeader == nil {
		needHeader, err := rawNeedsHeader(rawPath)
		if err != nil {
			return HookCapture{}, err
		}
		if needHeader {
			return HookCapture{Status: HookStatusNotReady}, nil
		}
	}

	// Track every invocation + its terminal reason so `ox session status` can
	// distinguish a healthy idle session (status=ok) from a broken recording
	// (status=session-file-not-found, adapter-missing, etc.) when EntryCount=0.
	result := HookCapture{}
	markStatus := func(status string) {
		result.Status = status
		if opts.MarkStatus == nil {
			return
		}
		_ = UpdateRecordingStateForAgent(projectRoot, agentID, func(s *RecordingState) {
			opts.MarkStatus(s, status)
		})
	}

	adapter, adapterErr := adapters.GetAdapter(state.AdapterName)
	if adapterErr != nil {
		slog.Info("hook: afterTool adapter not found", "agentID", agentID, "adapter", state.AdapterName, "err", adapterErr)
		markStatus("adapter-not-found")
		return result, nil
	}
	reader, ok := adapter.(adapters.IncrementalReader)
	if !ok {
		markStatus("adapter-no-incremental-reader")
		return result, nil // adapter doesn't support incremental reads
	}

	if state.SessionFile == "" {
		// discover session file on first hook call (Claude Code JSONL may not exist at prime time)
		repoRoot := state.WorkspacePath
		if repoRoot == "" {
			repoRoot = projectRoot
		}
		sf, findErr := adapter.FindSessionFile(adapters.SessionLookup{
			RepoRoot:       repoRoot,
			AgentID:        agentID,
			AgentSessionID: state.AgentSessionID,
			Since:          state.StartedAt.Add(-5 * time.Minute),
		})
		if findErr != nil || sf == "" {
			slog.Info("hook: session file not found", "agentID", agentID, "adapter", state.AdapterName, "repo", repoRoot, "err", findErr)
			markStatus("session-file-not-found")
			return result, nil // session file not available yet
		}
		state.SessionFile = sf
		if err := CheckNativeSourceBinding(state); err != nil {
			return result, err
		}
		_ = UpdateRecordingStateForAgent(projectRoot, agentID, func(s *RecordingState) {
			s.SessionFile = sf
		})
		slog.Debug("hook: discovered session file", "file", sf)
	}
	if opts.SourceHome != "" {
		safe, err := adapters.SafeSessionFilePath(state.AdapterName, state.SessionFile, opts.SourceHome)
		if err != nil {
			result.Status = HookStatusSourceRefused
			return result, fmt.Errorf("transcript %q: %w", state.SessionFile, err)
		}
		state.SessionFile = safe
	}

	// staleness check: if the source file disappeared or shrank (e.g., Claude Code
	// created a new file after compaction), try to rediscover it
	if fi, statErr := os.Stat(state.SessionFile); statErr != nil || fi.Size() < state.SourceOffset {
		if statErr != nil {
			slog.Info("hook: session file missing, attempting rediscovery", "file", state.SessionFile)
		} else {
			slog.Info("hook: session file shrank, attempting rediscovery", "file", state.SessionFile, "size", fi.Size(), "offset", state.SourceOffset)
		}
		repoRoot := state.WorkspacePath
		if repoRoot == "" {
			repoRoot = projectRoot
		}
		sf, findErr := adapter.FindSessionFile(adapters.SessionLookup{
			RepoRoot:       repoRoot,
			AgentID:        agentID,
			AgentSessionID: state.AgentSessionID,
			Since:          state.StartedAt.Add(-5 * time.Minute),
		})
		if findErr == nil && sf != "" && opts.SourceHome != "" {
			// checked before it replaces the source, so a refused path is never persisted
			safe, err := adapters.SafeSessionFilePath(state.AdapterName, sf, opts.SourceHome)
			if err != nil {
				result.Status = HookStatusSourceRefused
				return result, fmt.Errorf("rediscovered transcript %q: %w", sf, err)
			}
			sf = safe
		}
		if findErr == nil && sf != "" && sf != state.SessionFile {
			slog.Info("hook: rediscovered session file", "old", state.SessionFile, "new", sf)
			state.SessionFile = sf
			if err := CheckNativeSourceBinding(state); err != nil {
				return result, err
			}
			// both offsets belong to the old file: a StartOffset left behind would
			// make every read skip the first bytes of the new transcript, and
			// every ownership check start mid-record. Unpersisted, the append
			// below would commit the new file's cursor against the old file.
			if err := UpdateRecordingStateForAgent(projectRoot, agentID, func(s *RecordingState) {
				s.SessionFile = sf
				s.SourceOffset = 0 // reset offset for new file
				s.StartOffset = 0
			}); err != nil {
				result.Status = "checkpoint-failed"
				return result, fmt.Errorf("persist rediscovered transcript: %w", err)
			}
			state.SourceOffset = 0
			state.StartOffset = 0
		}
	}

	// use StartOffset as minimum read position to skip pre-session content
	readOffset := state.SourceOffset
	if state.StartOffset > 0 && readOffset < state.StartOffset {
		readOffset = state.StartOffset
	}
	result.FromOffset, result.ToOffset = readOffset, readOffset
	settled := opts.Settled.Path != ""
	if settled && opts.Settled.Path != state.SessionFile {
		result.Status = HookStatusUnsettled
		return result, nil
	}

	var sourceSnapshot os.FileInfo
	if state.AdapterName == "claude-code" {
		var snapshotErr error
		sourceSnapshot, snapshotErr = claudesource.Snapshot(state.SessionFile)
		if snapshotErr != nil {
			markStatus("read-error")
			return result, nil
		}
	}
	entries, newOffset, readErr := reader.ReadFromOffset(state.SessionFile, readOffset)
	if readErr != nil {
		slog.Info("hook: incremental read failed", "agentID", agentID, "adapter", state.AdapterName, "file", state.SessionFile, "offset", readOffset, "error", readErr)
		markStatus("read-error")
		return result, nil // non-fatal, will catch up at stop
	}
	if settled && newOffset > opts.Settled.Size {
		// The read took bytes written after the caller's settle check: a tool
		// record whose PostToolUse hook has not run would be committed without
		// that hook's error. Commit none of it; the next capture sees it settle.
		result.Status = HookStatusUnsettled
		return result, nil
	}
	if err := CheckNativeSourceBinding(state); err != nil {
		return result, err
	}
	if len(entries) == 0 {
		markStatus("no-new-entries")
		return result, nil
	}
	if state.AdapterName == "claude-code" {
		repoRoot := state.WorkspacePath
		if repoRoot == "" {
			repoRoot = projectRoot
		}
		// Only the newly read records can be appended by this capture. Require
		// ownership on each captured turn; finalization rechecks the whole file.
		if err := claudesource.ValidateRead(state.SessionFile, repoRoot, state.AgentSessionID, readOffset, false, sourceSnapshot); err != nil {
			slog.Info("hook: Claude source crossed repository boundary", "agentID", agentID, "error", err)
			// record the status first: once quarantined the recording leaves the
			// agent's active slot, which markStatus looks it up by
			markStatus("source-repo-mismatch")
			if errors.Is(err, claudesource.ErrUntrustedSource) {
				if markErr := SetSourceRejectedAt(state.SessionPath, state.SessionID, true); markErr != nil {
					slog.Warn("hook: failed to quarantine source", "agentID", agentID, "error", markErr)
				}
			}
			return result, nil
		}
	}

	// filter entries by timestamp — strict After() to prevent boundary leaks
	// for legacy states (StartOffset=0) where offset-based filtering isn't available
	if !state.StartedAt.IsZero() {
		filtered := make([]adapters.RawEntry, 0, len(entries))
		for _, e := range entries {
			if e.Timestamp.After(state.StartedAt) {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	if len(entries) == 0 {
		// update offset even if no entries passed filter; the bytes count as
		// consumed only once that checkpoint is durable
		if err := UpdateRecordingStateForAgent(projectRoot, agentID, func(s *RecordingState) {
			s.SourceOffset = newOffset
			if opts.MarkStatus != nil {
				opts.MarkStatus(s, "no-entries-after-filter")
			}
		}); err != nil {
			result.Status = "checkpoint-failed"
			return result, fmt.Errorf("persist cursor past filtered entries: %w", err)
		}
		result.Status, result.ToOffset = "no-entries-after-filter", newOffset
		return result, nil
	}

	// The header carries the session's identity and must be raw.jsonl's first
	// line: an entry appended ahead of it leaves the file headerless for good.
	needHeader, err := rawNeedsHeader(rawPath)
	if err != nil {
		return result, err
	}
	if needHeader {
		if opts.WriteHeader == nil { // checked before the read; kept so a nil writer is never called
			result.Status = HookStatusNotReady
			return result, nil
		}
		if err := opts.WriteHeader(state); err != nil {
			markStatus("header-write-failed")
			return result, fmt.Errorf("write raw.jsonl header: %w", err)
		}
	}

	redactor, _ := NewRedactorWithCustomRules(projectRoot)

	sessionEntries := ConvertRawEntries(entries)

	// enrich the last tool entry with error data from hook stdin.
	// hook stdin provides real-time error info that isn't in the JSONL file.
	if opts.ToolError != "" {
		for i := len(sessionEntries) - 1; i >= 0; i-- {
			if sessionEntries[i].Type == EntryTypeTool {
				sessionEntries[i].IsError = true
				if sessionEntries[i].ToolOutput == "" {
					sessionEntries[i].ToolOutput = opts.ToolError
				}
				break
			}
		}
	}

	redactor.RedactEntries(sessionEntries)

	writer, writeErr := NewRawWriter(rawPath, projectRoot)
	if writeErr != nil {
		return result, writeErr
	}
	appendErr := writer.AppendRecordingBatch(filepath.Join(state.SessionPath, ".recording.json"), sessionEntries, newOffset)
	closeErr := writer.Close()
	if appendErr == nil {
		appendErr = closeErr
	}
	if appendErr != nil {
		slog.Info("hook: append entries failed", "agentID", agentID, "path", rawPath, "error", appendErr)
		markStatus("append-failed")
		return result, nil // non-fatal
	}
	result.ToOffset, result.Entries = newOffset, len(sessionEntries)
	status := HookStatusOK
	if opts.Spool != nil {
		// spool exactly what the append just committed; reading it back is safe
		// after Close because raw.jsonl is append-only (see LastAppended)
		batch, ok, err := writer.LastAppended()
		switch {
		case err != nil && writer.lastSuspended:
			// committed while paused: the live stream deliberately excludes these
			// bytes (Spool skips paused batches), so failing to read them back
			// loses nothing from it. Declaring them would report a false gap.
			slog.WarnContext(ctx, "live_session_batch_read_failed", "session_id", state.SessionID, "agent_id", agentID,
				"session", filepath.Base(state.SessionPath), "bytes", writer.lastTo-writer.lastFrom, "paused", true, "declared", false, "error", err)
		case err != nil:
			// committed, but the live stream will never see it: the cursor is
			// past these bytes. Declared, the stream shows a gap in their place.
			status = HookStatusSpoolLost
			lost := writer.lastTo - writer.lastFrom // the span LastAppended failed to read
			slog.ErrorContext(ctx, "live_session_batch_read_failed", "session_id", state.SessionID, "agent_id", agentID,
				"session", filepath.Base(state.SessionPath), "bytes", lost, "declared", opts.SpoolLost != nil, "error", err)
			if opts.SpoolLost != nil {
				opts.SpoolLost(state, lost, err)
			}
		case ok:
			opts.Spool(state, batch)
		}
	}

	markStatus(status)
	return result, nil
}

// rawNeedsHeader reports whether raw.jsonl has no content yet, so its header
// must be written before the first entry. A zero-byte file counts: a crash can
// leave one behind.
func rawNeedsHeader(rawPath string) (bool, error) {
	info, err := os.Stat(rawPath)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat raw.jsonl: %w", err)
	}
	return info.Size() == 0, nil
}
