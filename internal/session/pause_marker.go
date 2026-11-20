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
	// PID, PIDStart and RootAgentID are the anchors of a pause inherited from
	// the agent that launched this one (MarkInheritedPause, PauseAnchor); all
	// empty for a pause a person asked for.
	PID         int    `json:"pid,omitempty"`
	PIDStart    int64  `json:"pid_start,omitempty"`
	RootAgentID string `json:"root_agent_id,omitempty"`
}

// inherited reports whether the marker records a pause inherited down a
// launch chain rather than one a person asked for.
func (p pauseMarkerPayload) inherited() bool { return p.PID > 0 || p.RootAgentID != "" }

func (p pauseMarkerPayload) anchor() PauseAnchor {
	return PauseAnchor{PID: p.PID, Start: p.PIDStart, RootAgentID: p.RootAgentID}
}

// MarkExplicitPause writes a per-agent breadcrumb indicating the user
// explicitly paused recording. Survives /clear so new post-/clear sessions
// can inherit the paused state.
func MarkExplicitPause(projectRoot, agentID string, seq int) error {
	return writePauseMarker(projectRoot, agentID, pauseMarkerPayload{Seq: seq, At: time.Now().UTC()})
}

// PauseAnchor is what a pause inherited down a launch chain lasts for. A
// root anchor, when present, decides alone: the pause holds exactly while the
// root's does, so resuming the root, or the root ending, releases the whole
// chain. The process anchor only decides for a marker with no root (a legacy
// marker). A chain whose anchor has ended never blocks a later agent.
type PauseAnchor struct {
	// PID and Start: the nearest agent process up the chain that is known.
	// Without a root, it holds while that process runs and its PID was not
	// reused. Start is proc.StartTime(PID); 0 when unknown, which leaves
	// liveness on the PID alone. 0 PID when no agent in the chain names one
	// (Cursor records none: its GUI process is shared by every conversation).
	PID   int
	Start int64
	// RootAgentID: the agent whose recording a person paused. It holds while
	// that recording exists and its person-set pause is still in place.
	RootAgentID string
}

// ProcessAnchorOf is a process anchor on pid, with its start time when the
// platform can tell it (Linux and macOS).
func ProcessAnchorOf(pid int) PauseAnchor {
	if pid <= 0 {
		return PauseAnchor{}
	}
	start, _ := proc.StartTime(pid)
	return PauseAnchor{PID: pid, Start: start}
}

// holds reports whether the pause still holds: by its root when it names
// one, else by its process. A process anchor on self is this agent's own
// process, not a launcher's, and does not count.
func (a PauseAnchor) holds(projectRoot string, self int) bool {
	if a.RootAgentID != "" {
		return personPausedRecording(projectRoot, a.RootAgentID)
	}
	return a.PID > 0 && a.PID != self && proc.IsAlive(a.PID) && !proc.IsReused(a.PID, a.Start)
}

// personPausedRecording reports whether agentID's recording exists and a
// person's pause on it (not an inherited one) is still in place.
func personPausedRecording(projectRoot, agentID string) bool {
	p, found := readPauseMarker(projectRoot, agentID)
	if !found || p.inherited() {
		return false
	}
	state, err := LoadRecordingStateForAgent(projectRoot, agentID)
	return err == nil && state != nil && state.IsAgentAlive()
}

// LaunchedPauseAnchor is the anchor a pause on launcher passes to an agent it
// launched: launcher's own agent process (processPID, its recording's, when
// known) or else the process its own inherited pause names; and the root its
// inherited pause names, or launcher itself when a person paused it.
func LaunchedPauseAnchor(projectRoot, launcher string, processPID int) PauseAnchor {
	anchor := PauseAnchor{RootAgentID: launcher}
	if p, found := readPauseMarker(projectRoot, launcher); found && p.inherited() {
		anchor = p.anchor()
	}
	if own := ProcessAnchorOf(processPID); own.PID > 0 {
		anchor.PID, anchor.Start = own.PID, own.Start
	}
	return anchor
}

// MarkInheritedPause writes agentID's pause marker for a pause it inherited
// from the paused agent that launched it. An agent this one launches in turn
// finds the marker through the SAGEOX_AGENT_ID it inherits, and is paused too
// while the anchor holds (InheritedPauseHeld): a pause reaches every agent down
// the launch chain. An anchor with neither a process nor a root is refused,
// because it would read as a pause a person asked for.
func MarkInheritedPause(projectRoot, agentID string, anchor PauseAnchor) error {
	if anchor.PID <= 0 && anchor.RootAgentID == "" {
		return fmt.Errorf("inherited pause for agent=%s names no anchor", agentID)
	}
	return writePauseMarker(projectRoot, agentID, pauseMarkerPayload{
		At: time.Now().UTC(), PID: anchor.PID, PIDStart: anchor.Start, RootAgentID: anchor.RootAgentID,
	})
}

// InheritedPauseHeld reports whether agentID holds a pause it inherited down a
// launch chain that still holds for an agent running as process self. A pause
// a person asked for, and an inherited one whose anchors have ended, report
// false: an ended chain never blocks a later agent.
func InheritedPauseHeld(projectRoot, agentID string, self int) bool {
	p, found := readPauseMarker(projectRoot, agentID)
	return found && p.inherited() && p.anchor().holds(projectRoot, self)
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
