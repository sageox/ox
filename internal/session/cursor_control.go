package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/sageox/ox/internal/session/adapters"
)

// InitializeCursorTurnBoundary runs before the recording marker is published.
// Hook startup expects the current turn after its saved pre-prompt boundary.
func InitializeCursorTurnBoundary(state *RecordingState, sourcePath, generation string) error {
	completed := 0
	snapshot, err := readCursorSourceSnapshot(sourcePath)
	if errors.Is(err, ErrCursorSourcePending) && state.StartOffset == 0 {
		// A fresh conversation has no export yet.
	} else if err != nil {
		return err
	} else {
		if !cursorOffsetBoundary(snapshot.data, state.StartOffset) || cursorSourceHash(snapshot.data[:state.StartOffset]) != state.SourcePrefixSHA256 {
			return ErrCursorSourceChanged
		}
		completed = cursorCompletedTurns(snapshot.data[:state.StartOffset])
	}
	state.CursorExpectedTurns = completed + 1
	if generation != "" {
		state.CursorPromptGenerations = []string{generation}
	}
	return nil
}

// RecordCursorPrompt persists a submission before Cursor exports it. It shares
// capture's append/marker locks, without waiting for the watcher's lifetime lock.
func RecordCursorPrompt(ctx context.Context, projectRoot string, expected *RecordingState, homeDir, generation string) error {
	if generation == "" {
		return ErrCursorBoundaryUnavailable
	}
	return withCursorControlState(ctx, projectRoot, expected, func(state *RecordingState) error {
		if slices.Contains(state.CursorPromptGenerations, generation) {
			return nil
		}
		path, err := ValidateCursorRecordingSource(homeDir, state)
		if err != nil {
			return err
		}
		completed := 0
		snapshot, err := readCursorSourceSnapshot(path)
		if errors.Is(err, ErrCursorSourcePending) && state.SourceOffset == 0 {
			// Fresh pending conversation.
		} else if err != nil {
			return err
		} else {
			if !cursorOffsetBoundary(snapshot.data, state.SourceOffset) || cursorSourceHash(snapshot.data[:state.SourceOffset]) != state.SourcePrefixSHA256 {
				return ErrCursorSourceChanged
			}
			completed = cursorCompletedTurns(snapshot.data)
		}
		// sessionStart can precede the first prompt hook and already reserve
		// that turn. Subsequent unique generations each reserve another turn.
		if len(state.CursorPromptGenerations) != 0 || completed >= state.CursorExpectedTurns {
			state.CursorExpectedTurns = max(completed, state.CursorExpectedTurns) + 1
		}
		state.CursorPromptGenerations = append(state.CursorPromptGenerations, generation)
		return SaveRecordingState(projectRoot, state)
	})
}

// UpdateCursorRecordingControl drains rows and records the lifecycle boundary
// in one append/marker transaction. The daemon may keep its lifetime owner lock.
// Resume waits for the submitted turn to finish: JSONL has no timestamps with
// which to place an unexported paused row on either side of the resume boundary.
func UpdateCursorRecordingControl(ctx context.Context, projectRoot string, expected *RecordingState, homeDir string, reader adapters.IncrementalReader, action LifecycleAction, update func(*RecordingState)) error {
	return withCursorControlState(ctx, projectRoot, expected, func(state *RecordingState) error {
		capture, err := drainCursorSourceLocked(ctx, projectRoot, state.SessionPath, homeDir, reader, false)
		if err != nil {
			if action != LifecycleActionPause || !errors.Is(err, ErrCursorSourcePending) {
				return err
			}
		} else {
			state = capture.State
			if action == LifecycleActionResume && !capture.Complete {
				return ErrCursorFinalDrainPending
			}
		}
		before := *state
		before.Lifecycle = slices.Clone(state.Lifecycle)
		update(state)
		if err := validateCursorLifecycleChange(&before, state); err != nil {
			return err
		}
		return SaveRecordingState(projectRoot, state)
	})
}

func withCursorControlState(ctx context.Context, projectRoot string, expected *RecordingState, update func(*RecordingState) error) error {
	return withRawAppendLock(filepath.Join(expected.SessionPath, "raw.jsonl"), func() error {
		return WithRecordingStateLock(ctx, expected.SessionPath, func() error {
			state, err := loadCursorCaptureCheckpoint(projectRoot, expected.SessionPath, false)
			if err != nil {
				return err
			}
			if state.SessionID != expected.SessionID || state.AgentID != expected.AgentID || state.AgentSessionID != expected.AgentSessionID {
				return fmt.Errorf("identity-conflict: Cursor recording changed during control")
			}
			return update(state)
		})
	})
}

func cursorSourceReady(state *RecordingState, data []byte) bool {
	return cursorSourceTurnComplete(data) && cursorCompletedTurns(data) >= state.CursorExpectedTurns
}

func cursorCompletedTurns(data []byte) int {
	count := 0
	for len(data) != 0 {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			break
		}
		var row struct {
			Type   string `json:"type"`
			Status string `json:"status"`
			Role   string `json:"role"`
		}
		if json.Unmarshal(data[:end], &row) == nil && row.Type == "turn_ended" && row.Status != "" && row.Role == "" {
			count++
		}
		data = data[end+1:]
	}
	return count
}
