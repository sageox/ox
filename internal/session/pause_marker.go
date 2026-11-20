package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/proc"
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
	// PID and PIDStart name the agent process holding a pause it inherited
	// from the agent that launched it (MarkInheritedPause); zero for a pause
	// a person asked for.
	PID      int   `json:"pid,omitempty"`
	PIDStart int64 `json:"pid_start,omitempty"`
}

// MarkExplicitPause writes a per-agent breadcrumb indicating the user
// explicitly paused recording. Survives /clear so new post-/clear sessions
// can inherit the paused state.
func MarkExplicitPause(projectRoot, agentID string, seq int) error {
	return writePauseMarker(projectRoot, agentID, pauseMarkerPayload{Seq: seq, At: time.Now().UTC()})
}

// MarkInheritedPause writes agentID's pause marker for a pause it inherited
// from the paused agent that launched it, naming its own agent process (pid).
// An agent this one launches in turn finds the marker through the
// SAGEOX_AGENT_ID it inherits, and is paused too while pid still runs
// (InheritedPauseHolder): a pause reaches every agent down the launch chain.
func MarkInheritedPause(projectRoot, agentID string, pid int) error {
	start, _ := proc.StartTime(pid)
	return writePauseMarker(projectRoot, agentID, pauseMarkerPayload{At: time.Now().UTC(), PID: pid, PIDStart: start})
}

// InheritedPauseHolder returns the agent process holding agentID's inherited
// pause, when that process still runs. A pause a person asked for names no
// process, and a marker whose process has exited or whose PID now names
// another process holds nothing: both return found=false.
func InheritedPauseHolder(projectRoot, agentID string) (pid int, found bool) {
	p, ok := readPauseMarker(projectRoot, agentID)
	if !ok || p.PID <= 0 || !proc.IsAlive(p.PID) || proc.IsReused(p.PID, p.PIDStart) {
		return 0, false
	}
	return p.PID, true
}

// readPauseMarker reads agentID's pause marker; an unparsable one reads as found
// with a zero payload, as PeekExplicitPause treats a legacy marker.
func readPauseMarker(projectRoot, agentID string) (pauseMarkerPayload, bool) {
	if projectRoot == "" || agentID == "" {
		return pauseMarkerPayload{}, false
	}
	name := explicitPauseMarker + "." + agentID
	for _, dir := range sessionsSearchPaths(projectRoot) {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var p pauseMarkerPayload
		if err := json.Unmarshal(data, &p); err != nil {
			return pauseMarkerPayload{}, true
		}
		return p, true
	}
	return pauseMarkerPayload{}, false
}

func writePauseMarker(projectRoot, agentID string, payload pauseMarkerPayload) error {
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
	// a legacy marker (or unwritable JSON) reads as found with seq=0
	p, found := readPauseMarker(projectRoot, agentID)
	return p.Seq, p.At, found
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
