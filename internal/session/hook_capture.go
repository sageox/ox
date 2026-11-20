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
	// but the live stream could not take the batch, so it was declared lost
	// there (HookCaptureOptions.Spool returned an error).
	HookStatusSpoolLost = "spool-lost"
	// HookStatusRawRefused: raw.jsonl is a symlink or another non-regular
	// file (ErrRawNotRegular), so nothing was recovered, read or written.
	HookStatusRawRefused = "raw-not-regular"
)

// ErrRawNotRegular reports a raw.jsonl that is not a regular file. Rollback
// recovery truncates raw.jsonl and a capture appends to it: through a symlink
// either one would land on whatever file the link names.
var ErrRawNotRegular = errors.New("raw.jsonl is not a regular file")

// CheckRawRegular returns ErrRawNotRegular when rawPath exists as anything but
// a regular file. It uses Lstat, so a symlink is refused whatever it points
// at. A missing raw.jsonl passes: the hook that starts it writes its header.
func CheckRawRegular(rawPath string) error {
	info, err := os.Lstat(rawPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lstat raw.jsonl: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: mode %s", ErrRawNotRegular, info.Mode().Type())
	}
	return nil
}

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
	// runs only after the append succeeded. An error means the stream could
	// not take the batch and declared it lost: the cursor is already past its
	// bytes, so no later capture sends them (HookStatusSpoolLost).
	Spool func(state *RecordingState, batch AppendedBatch) error
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
	// settle. The zero value leaves the read unbounded. A transcript found gone
	// or shrunk is left for a hook to rediscover (HookStatusUnsettled).
	Settled SettledSource
	// Transcript is what the agent named on this hook's stdin; nil for a
	// caller that is not a hook (the daemon). For a Claude Code recording it
	// becomes the recording's bound transcript, the only one the daemon's
	// catch-up reads (bindHookTranscript). Marker fields never bind one: the
	// marker is anyone's to rewrite, the hook's stdin is Claude Code's own.
	Transcript *HookTranscript
}

// HookTranscript is the transcript Claude Code passes every hook on stdin.
type HookTranscript struct {
	// Path is stdin's transcript_path; empty when the agent sent none (an
	// older Claude Code).
	Path string
	// SessionID is stdin's session_id, which names the transcript's file.
	SessionID string
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
	// Checked before anything else touches the recording: a symlinked
	// raw.jsonl turns the rollback below into a truncate of the file it names.
	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	if err := CheckRawRegular(rawPath); err != nil {
		if !errors.Is(err, ErrRawNotRegular) {
			return HookCapture{}, err
		}
		slog.WarnContext(ctx, "session_capture_raw_refused", "session_id", state.SessionID, "agent_id", agentID,
			"session", filepath.Base(state.SessionPath), "error", err)
		return HookCapture{Status: HookStatusRawRefused}, err
	}
	if err := CheckNativeSourceBinding(state); err != nil {
		return HookCapture{}, err
	}
	// Claude Code's ownership checks below must cover every spelling of the
	// adapter that resolves to it ("claude" is an accepted alias).
	claudeCode := adapters.CanonicalAdapterName(state.AdapterName) == "claude-code"
	if claudeCode && opts.Transcript != nil {
		repoRoot := state.WorkspacePath
		if repoRoot == "" {
			repoRoot = projectRoot
		}
		bindHookTranscript(ctx, state, repoRoot, *opts.Transcript)
	}
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
		if opts.Settled.Path != "" {
			// the caller stat'd a transcript that has since gone or shrunk;
			// finding its replacement, and rebinding it, is a hook's job
			result.Status = HookStatusUnsettled
			return result, nil
		}
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
	if claudeCode {
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
	if claudeCode {
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
	batch, appendErr := writer.AppendRecordingBatch(filepath.Join(state.SessionPath, ".recording.json"), sessionEntries, newOffset)
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
	if opts.Spool != nil && opts.Spool(state, batch) != nil {
		status = HookStatusSpoolLost
	}

	markStatus(status)
	return result, nil
}

// rawNeedsHeader reports whether raw.jsonl has no content yet, so its header
// must be written before the first entry. A zero-byte file counts: a crash can
// leave one behind.
// bindHookTranscript binds the transcript Claude Code named on a hook's stdin
// to state's recording, on every hook: a recording active before the binding
// existed, one started by hand with its transcript already set, and one whose
// last bind failed all bind at their next hook. It binds only a transcript
// inside Claude Code's own root that passes Claude Code's ownership check for
// this repository and stdin's session. Failures cost the recording its daemon
// catch-up only, never this capture, so they are logged and the hook goes on.
func bindHookTranscript(ctx context.Context, state *RecordingState, repoRoot string, t HookTranscript) {
	ids := []any{"session_id", state.SessionID, "agent_id", state.AgentID, "session", filepath.Base(state.SessionPath)}
	if t.Path == "" {
		slog.DebugContext(ctx, "session_source_bind_skipped", append(ids, "reason", "no_transcript_path")...)
		return
	}
	entry, registered, err := lookupStartedRecording(state.SessionPath, state.SessionID)
	if err != nil || !registered {
		// not started here (an older ox), or no registry to read: the daemon
		// never catches this recording up, so there is nothing to bind
		slog.DebugContext(ctx, "session_source_bind_skipped", append(ids, "reason", "not_registered", "error", err)...)
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		slog.WarnContext(ctx, "session_source_bind_failed", append(ids, "error", err)...)
		return
	}
	source, err := adapters.SafeSessionFilePath("claude-code", t.Path, home)
	if err != nil {
		slog.WarnContext(ctx, "session_source_bind_refused", append(ids, "transcript", t.Path, "error", err)...)
		return
	}
	if entry.Source == canonicalPath(source) {
		return // bound already: every hook after the first ends here
	}
	if err := claudesource.Validate(source, repoRoot, t.SessionID); err != nil {
		slog.WarnContext(ctx, "session_source_bind_refused", append(ids, "transcript", source, "error", err)...)
		return
	}
	if err := bindStartedRecordingSource(state.SessionPath, state.SessionID, source); err != nil {
		slog.WarnContext(ctx, "session_source_bind_failed", append(ids, "transcript", source, "error", err)...)
		return
	}
	slog.InfoContext(ctx, "session_source_bound", append(ids, "transcript", source, "previous", entry.Source)...)
}

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
