package session

import (
	"bufio"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// SessionStatus represents the lifecycle state of a session.
//
// Lifecycle:
//
//	           ┌──────────┐
//	           │ recording│
//	           └────┬─────┘
//	     ┌──────────┼──────────┐
//	     ▼          ▼          ▼
//	┌────────┐ ┌────────┐ ┌────────┐
//	│ paused │ │ ghost  │ │ orphan │
//	└───┬────┘ └───┬────┘ └───┬────┘
//	    │       (cleanup)  (finalize)
//	    ▼                      │
//	┌────────┐                 │
//	│ local  │◄────────────────┘
//	└───┬────┘
//	    ▼
//	┌──────────┐
//	│ uploaded  │
//	└──────────┘
//
//	┌──────────┐
//	│ canceled  │  (terminal — data discarded)
//	└──────────┘
type SessionStatus string

const (
	StatusRecording SessionStatus = "recording" // actively being recorded, parent process alive
	StatusPaused    SessionStatus = "paused"    // user explicitly stopped recording (data preserved, not yet uploaded)
	StatusGhost     SessionStatus = "ghost"     // parent dead, no substantive data — safe to delete
	StatusOrphan    SessionStatus = "orphan"    // parent dead, has data — needs recovery/finalization
	StatusLocal     SessionStatus = "local"     // exists locally, not uploaded (may have been recovered from orphan)
	StatusUploaded  SessionStatus = "uploaded"  // committed to ledger
	StatusCanceled  SessionStatus = "canceled"  // user explicitly discarded session (terminal — data deleted)
)

// ghostHeuristicAge is the minimum age before a session with no PID is labeled ghost.
// New sessions start with 0 entries; don't label them until they've been idle long enough.
const ghostHeuristicAge = 5 * time.Minute

// StopReason constants for how a session ended.
const (
	StopReasonStopped   = "stopped"   // user explicitly stopped via /ox-session-stop
	StopReasonCanceled  = "canceled"  // user explicitly canceled via /ox-session-abort
	StopReasonRecovered = "recovered" // recovered from orphan by daemon anti-entropy
)

// ClassifySession determines the lifecycle status of a session based on its metadata
// and whether it exists in the ledger. This is the single source of truth for session
// status — all display and cleanup code should use this instead of inline logic.
func ClassifySession(info SessionInfo, isUploaded bool) SessionStatus {
	if !info.Recording {
		// check stop reason for terminal states
		if info.StopReason == StopReasonCanceled {
			return StatusCanceled
		}
		if isUploaded {
			return StatusUploaded
		}
		// "paused" = user explicitly stopped, data preserved locally, not yet uploaded
		if info.StopReason == StopReasonStopped {
			return StatusPaused
		}
		return StatusLocal
	}

	// recording is active — check if the parent process is still alive
	if isAbandoned(info.ParentPID, info.CreatedAt) {
		if info.HasRawData || info.EntryCount > 0 {
			return StatusOrphan
		}
		return StatusGhost
	}

	return StatusRecording
}

// isAbandoned checks whether a recording session's parent process is dead.
// If PID is known, uses kill(pid, 0) for instant liveness detection.
// If PID is unknown, falls back to age heuristic.
func isAbandoned(parentPID int, createdAt time.Time) bool {
	if parentPID > 0 {
		return !isPIDAlive(parentPID)
	}
	// no PID recorded — fall back to heuristic: old enough to be suspicious
	return time.Since(createdAt) > ghostHeuristicAge
}

// isPIDAlive checks if a process with the given PID is still running.
// Uses kill(pid, 0) which checks existence without sending a signal.
func isPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// HasSubstantiveEntries returns true if a raw.jsonl file has at least one entry
// beyond the metadata header line. A header-only file (1 line) has no real session
// content and should not be uploaded or finalized.
//
// This is the canonical check — use it everywhere instead of inline line counting.
func HasSubstantiveEntries(rawPath string) bool {
	f, err := os.Open(rawPath)
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	lineCount := 0
	for scanner.Scan() {
		lineCount++
		if lineCount >= 2 {
			return true // at least one line beyond header
		}
	}
	return false
}

// CountSubstantiveEntries counts lines in a raw.jsonl that are actual session
// entries, excluding the metadata header (first line). Returns 0 for header-only
// files or files that don't exist.
func CountSubstantiveEntries(rawPath string) int {
	f, err := os.Open(rawPath)
	if err != nil {
		return 0
	}
	defer f.Close()

	count := 0
	isFirst := true
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		if isFirst {
			isFirst = false
			continue // skip metadata header
		}
		count++
	}
	return count
}

// RawJSONLHasData checks if a raw.jsonl file exists on disk and has content.
// This is a filesystem-level check (size > 0), not a line-level check.
func RawJSONLHasData(sessionPath string) bool {
	rawPath := filepath.Join(sessionPath, "raw.jsonl")
	info, err := os.Stat(rawPath)
	if err != nil {
		return false
	}
	return info.Size() > 0
}
