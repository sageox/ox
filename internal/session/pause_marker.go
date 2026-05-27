package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// pause_marker.go implements the persistent breadcrumb for an active pause.
//
// Why a marker file alongside RecordingState.SuspendedAt:
//   - .session_paused.<agentID> SURVIVES a /clear boundary (the recording
//     state is finalized and removed at /clear, but the marker stays).
//     New post-/clear sessions inherit the paused state via the marker.
//   - Cross-process discovery: a sibling shell/subagent can detect that
//     this agent's session is paused without loading RecordingState.
//   - Resilient against partial state corruption (.recording.json lost or
//     mid-write); the marker file is small and atomic to write/read.
//
// See ADR-019 (session entity lifecycle) and ADR-020 (pause/resume).

const explicitPauseMarker = ".session_paused"

// pauseMarkerPayload is the JSON shape persisted in .session_paused.<agentID>.
type pauseMarkerPayload struct {
	Seq int       `json:"seq"`
	At  time.Time `json:"at"`
}

// MarkExplicitPause writes a per-agent breadcrumb indicating the user
// explicitly paused recording. Survives /clear so new post-/clear sessions
// can inherit the paused state.
func MarkExplicitPause(projectRoot, agentID string, seq int) error {
	if projectRoot == "" {
		return fmt.Errorf("%w: project root", ErrEmptyPath)
	}
	if agentID == "" {
		return fmt.Errorf("agentID must not be empty")
	}

	dir := resolveSessionsWritePath(projectRoot)
	if dir == "" {
		return fmt.Errorf("could not write pause marker for project=%s agent=%s: no sessions directory", projectRoot, agentID)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create sessions dir for pause marker: %w", err)
	}

	payload := pauseMarkerPayload{Seq: seq, At: time.Now().UTC()}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal pause marker payload: %w", err)
	}

	markerPath := filepath.Join(dir, explicitPauseMarker+"."+agentID)
	if err := os.WriteFile(markerPath, data, 0600); err != nil {
		return fmt.Errorf("write pause marker: %w", err)
	}
	return nil
}

// PeekExplicitPause reads (without removing) the pause marker for the given
// agent. Returns the recorded seq, the wall-clock time of the pause, and
// found=true if the marker exists.
//
// Use PeekExplicitPause for status display and post-/clear inheritance
// detection. Use ConsumeExplicitPause when transitioning out of suspended
// state.
func PeekExplicitPause(projectRoot, agentID string) (seq int, at time.Time, found bool) {
	if projectRoot == "" || agentID == "" {
		return 0, time.Time{}, false
	}
	name := explicitPauseMarker + "." + agentID
	for _, dir := range sessionsSearchPaths(projectRoot) {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var p pauseMarkerPayload
		if err := json.Unmarshal(data, &p); err != nil {
			// legacy marker (or unwritable JSON) — treat as found with seq=0
			return 0, time.Time{}, true
		}
		return p.Seq, p.At, true
	}
	return 0, time.Time{}, false
}

// ConsumeExplicitPause removes the pause marker and returns whether it
// existed. Use when transitioning out of suspended state (resume, stop,
// abort, expiration).
func ConsumeExplicitPause(projectRoot, agentID string) (seq int, found bool) {
	if projectRoot == "" || agentID == "" {
		return 0, false
	}
	name := explicitPauseMarker + "." + agentID
	for _, dir := range sessionsSearchPaths(projectRoot) {
		path := filepath.Join(dir, name)
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var p pauseMarkerPayload
		_ = json.Unmarshal(data, &p) // best-effort; preserve seq=0 on parse failure
		if rmErr := os.Remove(path); rmErr == nil {
			found = true
			seq = p.Seq
		}
	}
	return seq, found
}

// ClearExplicitPause removes the pause marker without returning the
// recorded seq. Convenience wrapper over ConsumeExplicitPause when the
// caller doesn't need the previous value.
func ClearExplicitPause(projectRoot, agentID string) bool {
	_, found := ConsumeExplicitPause(projectRoot, agentID)
	return found
}
