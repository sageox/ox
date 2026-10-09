package session

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/cursorpaths"
)

var (
	ErrCursorSourcePending       = errors.New("source-not-found: Cursor session export is pending")
	ErrCursorSourceChanged       = errors.New("source-changed: Cursor session export changed; checkpoint preserved")
	ErrCursorBoundaryUnavailable = errors.New("boundary-unavailable: Cursor recording needs a saved source boundary")
	ErrCursorFinalDrainPending   = errors.New("final-drain-pending: waiting for Cursor to finish exporting this turn")
)

const cursorCaptureMaxBytes int64 = 64 << 20

// CursorCaptureResult describes only durably acknowledged capture. Complete is
// an observation of the current export, not a promise that Cursor will never
// append another turn. A native stop hook must leave its watcher alive.
type CursorCaptureResult struct {
	State    *RecordingState
	Entries  int
	Complete bool
}

// ValidateCursorRecordingSource applies the exact conversation/worktree rule
// on top of the shared native-root allowlist. Missing files remain pending;
// discovery never changes the already-persisted byte boundary.
func ValidateCursorRecordingSource(homeDir string, state *RecordingState) (string, error) {
	if state == nil || state.AdapterName != "cursor" || state.WorkspacePath == "" {
		return "", ErrCursorBoundaryUnavailable
	}
	if !state.StartOffsetKnown || state.StartOffset < 0 || state.SourceOffset < state.StartOffset || state.EntryCount < 0 {
		return "", ErrCursorBoundaryUnavailable
	}
	hash, err := hex.DecodeString(state.SourcePrefixSHA256)
	if err != nil || len(hash) != sha256.Size {
		return "", ErrCursorBoundaryUnavailable
	}
	homeDir, err = filepath.EvalSymlinks(homeDir)
	if err != nil {
		return "", fmt.Errorf("invalid-source-path: Cursor home directory is unavailable")
	}
	path, err := cursorpaths.ValidateSource(homeDir, state.WorkspacePath, state.AgentSessionID, state.SessionFile)
	if err != nil {
		return "", fmt.Errorf("invalid-source-path: Cursor recording source is not authorized")
	}
	// The generic helper requires an existing immediate parent. Cursor's
	// stricter component walk also proves a fresh, not-yet-created chat tree.
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) && adapters.IsSessionFileAllowed("cursor", path, homeDir) {
		return path, nil
	}
	if _, err := adapters.SafeSessionFilePath("cursor", path, homeDir); err != nil {
		return "", fmt.Errorf("invalid-source-path: Cursor recording source is not authorized")
	}
	return path, nil
}

// DrainCursorSource is the single Cursor capture transaction for daemon polls,
// hook fallback and finalization. The caller MUST hold the raw.jsonl owner
// lock. Lock order is raw owner, append, then recording marker. Holding the marker
// across append and checkpoint gives pause/resume an exact saved-entry boundary.
// Failed checkpoints are recovered with the same redacted-prefix proof as all
// other capture paths; source offsets are never reset or inferred from raw rows.
func DrainCursorSource(ctx context.Context, projectRoot, sessionPath, homeDir string, reader adapters.IncrementalReader, final bool) (*CursorCaptureResult, error) {
	var result *CursorCaptureResult
	err := withRawAppendLock(filepath.Join(sessionPath, "raw.jsonl"), func() error {
		return WithRecordingStateLock(ctx, sessionPath, func() error {
			var err error
			result, err = drainCursorSourceLocked(ctx, projectRoot, sessionPath, homeDir, reader, final)
			return err
		})
	})
	return result, err
}

func drainCursorSourceLocked(ctx context.Context, projectRoot, sessionPath, homeDir string, reader adapters.IncrementalReader, final bool) (*CursorCaptureResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(recordingStatePath(sessionPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotRecording
	}
	if err != nil {
		return nil, fmt.Errorf("read Cursor recording checkpoint: %w", err)
	}
	state, _, err := decodeRecordingState(data)
	if err != nil || state == nil {
		return nil, fmt.Errorf("invalid Cursor recording checkpoint")
	}
	if state.SourceRejected {
		return nil, fmt.Errorf("%w: Cursor recording is held for ownership review", ErrNotRecording)
	}
	root, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("workspace-mismatch: Cursor workspace is unavailable")
	}
	workspace, err := filepath.EvalSymlinks(state.WorkspacePath)
	if err != nil || root != workspace || filepath.Clean(state.SessionPath) != filepath.Clean(sessionPath) {
		return nil, fmt.Errorf("workspace-mismatch: Cursor recording belongs to another workspace")
	}
	if !final && (state.StoppedAt != nil || HasExplicitStop(projectRoot, state.AgentID)) {
		return nil, ErrNotRecording
	}
	if _, finalized, err := cursorCaptureHeader(filepath.Join(sessionPath, "raw.jsonl")); err != nil {
		return nil, err
	} else if finalized {
		return nil, ErrNotRecording
	}
	path, err := ValidateCursorRecordingSource(homeDir, state)
	if err != nil {
		return nil, err
	}
	before, err := readCursorSourceSnapshot(path)
	if err != nil {
		return nil, err
	}
	if !cursorOffsetBoundary(before.data, state.SourceOffset) || cursorSourceHash(before.data[:state.SourceOffset]) != state.SourcePrefixSHA256 {
		return nil, ErrCursorSourceChanged
	}
	if reader == nil {
		return nil, fmt.Errorf("adapter-missing: Cursor incremental reader is unavailable")
	}
	entries, nextOffset, err := reader.ReadFromOffset(path, state.SourceOffset)
	if err != nil {
		// Adapter errors can include arbitrary source paths or content. The
		// adapter's own diagnostic retains the detailed category; this shared
		// host boundary never logs its untrusted body.
		return nil, fmt.Errorf("source-read-error: Cursor export could not be read; checkpoint preserved")
	}
	if nextOffset < state.SourceOffset || !cursorOffsetBoundary(before.data, nextOffset) || (len(entries) > 0 && nextOffset == state.SourceOffset) {
		return nil, fmt.Errorf("invalid-offset: Cursor reader returned an invalid checkpoint")
	}
	if _, err := ValidateCursorRecordingSource(homeDir, state); err != nil {
		return nil, ErrCursorSourceChanged
	}
	after, err := readCursorSourceSnapshot(path)
	if err != nil || !os.SameFile(before.info, after.info) || !before.info.ModTime().Equal(after.info.ModTime()) || !bytes.Equal(before.data, after.data) {
		return nil, ErrCursorSourceChanged
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	converted := ConvertRawEntries(entries)
	rawPath := filepath.Join(sessionPath, "raw.jsonl")
	matched, err := ReconcileRawPrefix(rawPath, state.EntryCount, converted, projectRoot)
	if err != nil {
		return nil, fmt.Errorf("reconcile Cursor capture: %w", err)
	}
	writer, err := NewRawWriter(rawPath, projectRoot)
	if err != nil {
		return nil, err
	}
	// The append lock covers the transaction; do not acquire it recursively.
	for i := matched; i < len(converted); i++ {
		if err := writer.writeEntry(&converted[i]); err != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("append Cursor capture: %w", err)
		}
	}
	if err := writer.CloseAndSync(); err != nil {
		return nil, fmt.Errorf("sync Cursor capture: %w", err)
	}
	changed := state.SessionFile != path || nextOffset != state.SourceOffset
	state.SessionFile = path
	state.SourceOffset = nextOffset
	state.SourcePrefixSHA256 = cursorSourceHash(before.data[:nextOffset])
	state.EntryCount += len(converted)
	if changed {
		// Save is atomic but deliberately does not acquire this already-held
		// marker lock. A failure leaves the old checkpoint for reconciliation.
		if err := SaveRecordingState(projectRoot, state); err != nil {
			return nil, fmt.Errorf("checkpoint Cursor capture: %w", err)
		}
	}
	return &CursorCaptureResult{
		State: state, Entries: len(converted),
		Complete: nextOffset == int64(len(before.data)) && cursorSourceTurnComplete(before.data),
	}, nil
}

// FinalizeCursorCapture drains the current complete native turn, then applies
// pause ranges in an atomic RawWriter rewrite. The caller holds raw ownership.
// The finalized flag travels in that same raw-file replacement, so a crash
// cannot make a retry apply the mask twice or replay the source into masked raw.
func FinalizeCursorCapture(ctx context.Context, projectRoot, sessionPath, homeDir string, reader adapters.IncrementalReader) (*RecordingState, error) {
	var state *RecordingState
	err := withRawAppendLock(filepath.Join(sessionPath, "raw.jsonl"), func() error {
		return WithRecordingStateLock(ctx, sessionPath, func() error {
			rawPath := filepath.Join(sessionPath, "raw.jsonl")
			header, finalized, err := cursorCaptureHeader(rawPath)
			if err != nil {
				return err
			}
			if finalized {
				data, err := os.ReadFile(recordingStatePath(sessionPath))
				if err != nil {
					return err
				}
				decoded, _, decodeErr := decodeRecordingState(data)
				if decodeErr != nil || decoded == nil || decoded.AdapterName != "cursor" || decoded.SessionPath != sessionPath {
					return fmt.Errorf("invalid finalized Cursor recording checkpoint")
				}
				if decoded.SourceRejected {
					return fmt.Errorf("%w: Cursor recording is held for ownership review", ErrNotRecording)
				}
				state = decoded
				return nil
			}
			capture, err := drainCursorSourceLocked(ctx, projectRoot, sessionPath, homeDir, reader, true)
			if err != nil {
				return err
			}
			state = capture.State
			if !capture.Complete {
				return ErrCursorFinalDrainPending
			}
			data, err := os.ReadFile(rawPath)
			if err != nil {
				return err
			}
			entries, completeBytes, err := completeRawRecords(data)
			if err != nil || completeBytes != len(data) || len(entries) != state.EntryCount {
				return fmt.Errorf("invalid-checkpoint: Cursor final capture does not match its durable checkpoint")
			}
			if header == nil {
				header = map[string]any{"_meta": map[string]any{
					"schema_version": "1", "agent_id": state.AgentID, "agent_type": "cursor",
					"session_id": state.SessionID, "created_at": state.StartedAt,
				}}
			}
			meta, ok := header["_meta"].(map[string]any)
			if header["type"] == "header" {
				meta, ok = header["metadata"].(map[string]any)
			}
			if !ok {
				return fmt.Errorf("invalid Cursor capture metadata header")
			}
			meta["cursor_finalized"] = true
			meta["finalized_at"] = time.Now().UTC()
			tmp, err := os.CreateTemp(sessionPath, ".cursor-finalize-*")
			if err != nil {
				return err
			}
			tmpPath := tmp.Name()
			defer os.Remove(tmpPath)
			if err := tmp.Close(); err != nil {
				return err
			}
			writer, err := NewRawWriterTruncate(tmpPath, projectRoot)
			if err != nil {
				return err
			}
			defer writer.Close()
			if err := writer.WriteRaw(header); err != nil {
				return err
			}
			ranges := BuildSegmentRanges(state.Lifecycle)
			for i, entry := range entries {
				if !IsSeqExcluded(i, ranges) {
					if err := writer.WriteRaw(entry); err != nil {
						return err
					}
				}
			}
			// Carrier footers belong to the recording, not to its masked turns.
			// Preserve their order so newer provenance and stop stamps still win.
			for _, line := range bytes.Split(data, []byte{'\n'}) {
				var row map[string]any
				if json.Unmarshal(line, &row) == nil && row["type"] == "footer" {
					if err := writer.WriteRaw(row); err != nil {
						return err
					}
				}
			}
			if err := writer.CloseAndSync(); err != nil {
				return err
			}
			if err := os.Rename(tmpPath, rawPath); err != nil {
				return err
			}
			dir, err := os.Open(sessionPath)
			if err != nil {
				return err
			}
			defer dir.Close()
			return dir.Sync()
		})
	})
	return state, err
}

func cursorCaptureHeader(rawPath string) (map[string]any, bool, error) {
	file, err := os.Open(rawPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	line, err := bufio.NewReader(io.LimitReader(file, 1<<20)).ReadBytes('\n')
	if errors.Is(err, io.EOF) && len(line) == 0 {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, nil // reconciliation must prove any torn first entry
	}
	var row map[string]any
	if err := json.Unmarshal(line, &row); err != nil {
		return nil, false, nil // complete malformed records fail reconciliation
	}
	meta, header := row["_meta"].(map[string]any)
	if row["type"] == "header" {
		meta, header = row["metadata"].(map[string]any)
	}
	if !header {
		return nil, false, nil // header-less crash recovery is supported
	}
	finalized, _ := meta["cursor_finalized"].(bool)
	return row, finalized, nil
}

type cursorSourceSnapshot struct {
	info os.FileInfo
	data []byte
}

func readCursorSourceSnapshot(path string) (*cursorSourceSnapshot, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrCursorSourcePending
	}
	if err != nil {
		return nil, fmt.Errorf("source-unreadable: Cursor export could not be opened")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("source-unreadable: Cursor export is not a regular file")
	}
	if before.Size() > cursorCaptureMaxBytes {
		return nil, fmt.Errorf("source-too-large: Cursor export exceeds the 64 MiB capture limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, cursorCaptureMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("source-unreadable: Cursor export could not be read")
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || int64(len(data)) != before.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, ErrCursorSourceChanged
	}
	return &cursorSourceSnapshot{info: before, data: data}, nil
}

func cursorOffsetBoundary(data []byte, offset int64) bool {
	return offset >= 0 && offset <= int64(len(data)) && (offset == 0 || data[offset-1] == '\n')
}

func cursorSourceHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func cursorSourceTurnComplete(data []byte) bool {
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return false
	}
	last := bytes.TrimSpace(data)
	if i := bytes.LastIndexByte(last, '\n'); i >= 0 {
		last = last[i+1:]
	}
	var row struct {
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	return json.Unmarshal(last, &row) == nil && row.Type == "turn_ended" && row.Status != ""
}
