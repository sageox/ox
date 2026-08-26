package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sageox/ox/internal/lfs"
)

// SessionStatus represents the lifecycle state of a session.
//
// See ADR-019 (session entity lifecycle) for the canonical state machine and
// transition rules. ADR-020 adds StatusSuspended for active pause.
//
// Lifecycle:
//
//	           ┌──────────┐
//	           │ recording│◄─────────────┐
//	           └────┬─────┘  resume      │
//	  ┌─────┬──────┼──────┬─────────┐    │
//	  │     │      ▼      ▼         ▼    │ pause
//	  │     │  ┌────────┐ ┌────────┐     │
//	  │     │  │ ghost  │ │ orphan │  ┌──┴──────┐
//	  │     │  └───┬────┘ └───┬────┘  │suspended│
//	  │     │  (cleanup)  (finalize)  └─────────┘
//	  │     ▼               │
//	  │  ┌────────┐         │
//	  │  │ paused │         │
//	  │  └───┬────┘         │
//	  │      ▼              │
//	  │  ┌────────┐         │
//	  └─►│ local  │◄────────┘
//	     └───┬────┘
//	         ▼
//	     ┌──────────┐
//	     │ uploaded │
//	     └──────────┘
//
//	┌──────────┐
//	│ canceled │  (terminal — data discarded)
//	└──────────┘
//
// NOTE: StatusPaused is a legacy name meaning "user stopped, data preserved,
// not yet uploaded" (NOT active pause). StatusSuspended is the active-pause
// state introduced by ADR-020. The legacy constant is preserved verbatim to
// avoid migrating .recording.json files and uploaded ledger metadata.
type SessionStatus string

const (
	StatusRecording SessionStatus = "recording" // actively being recorded, parent process alive
	StatusSuspended SessionStatus = "suspended" // active pause, recording continues locally, upload will exclude paused range (ADR-020)
	StatusPaused    SessionStatus = "paused"    // user explicitly stopped recording (data preserved, not yet uploaded) — LEGACY NAME
	StatusGhost     SessionStatus = "ghost"     // parent dead, no substantive data — safe to delete
	StatusOrphan    SessionStatus = "orphan"    // parent dead, has data — needs recovery/finalization
	StatusLocal     SessionStatus = "local"     // exists locally, not uploaded (may have been recovered from orphan)
	StatusUploaded  SessionStatus = "uploaded"  // committed to ledger
	StatusCanceled  SessionStatus = "canceled"  // user explicitly discarded session (terminal — data deleted)

	// StatusDraft: the ledger holds a meta.json-only placeholder published
	// early so /c/<session_id> resolves for links already circulating in PR
	// bodies. No turn data has been committed. Derived, never persisted — the
	// only persisted signal is lfs.SessionMeta.Draft.
	//
	// This exists because the alternative is worse, not because the vocabulary
	// needed another entry: without it, a draft directory in the ledger reads
	// as StatusUploaded, which reports live recordings as finished and makes
	// `ox session abort <name>` refuse with "already uploaded".
	StatusDraft SessionStatus = "draft"
)

// ghostHeuristicAge is the minimum age before a session with no PID is labeled ghost.
// New sessions start with 0 entries; don't label them until they've been idle long enough.
const ghostHeuristicAge = 5 * time.Minute

// StopReason constants for how a session ended.
const (
	StopReasonStopped       = "stopped"        // user explicitly stopped via /ox-session-stop
	StopReasonCanceled      = "canceled"       // user explicitly canceled via /ox-session-abort
	StopReasonRecovered     = "recovered"      // recovered from orphan by daemon anti-entropy
	StopReasonRateLimited   = "rate_limited"   // adapter detected agent hit a usage / rate limit
	StopReasonQuotaExceeded = "quota_exceeded" // adapter detected agent quota exhausted
	StopReasonTerminalError = "terminal_error" // adapter detected non-recoverable agent error (generic)
)

// stopReasonRank gates StopReason transitions. Higher wins. User-initiated
// reasons (stopped, canceled) take precedence over adapter-detected terminal
// conditions, so a replay of an old rate-limit line can never overwrite a
// reason the user set explicitly. The "recovered" reason is the lowest,
// applied only when nothing else is known.
var stopReasonRank = map[string]int{
	"":                      0,
	StopReasonRecovered:     10,
	StopReasonTerminalError: 40,
	StopReasonRateLimited:   50,
	StopReasonQuotaExceeded: 50,
	StopReasonCanceled:      100,
	StopReasonStopped:       100,
}

// CanTransitionStopReason reports whether next is allowed to overwrite current
// according to the precedence lattice. Equal ranks (e.g. user re-stopping a
// session) are allowed so explicit user actions are always idempotent.
// Unknown reasons are treated as rank 0.
func CanTransitionStopReason(current, next string) bool {
	return stopReasonRank[next] >= stopReasonRank[current]
}

// FormatStopReason renders a SessionInfo's terminal-stop reason for display
// in `ox status` and `ox session list`. Falls back through:
//
//   - parsed absolute reset time (e.g. "rate limit (resets 15:00)")
//   - raw matched reset string (e.g. "rate limit (resets in 3h)")
//   - bare reason text ("rate limit")
//
// Returns "" when there is no terminal stop reason worth surfacing.
func FormatStopReason(info SessionInfo) string {
	label := stopReasonLabel(info.StopReason)
	if label == "" {
		return ""
	}
	switch {
	case info.StopResetsAt != nil && !info.StopResetsAt.IsZero():
		return label + " (resets " + info.StopResetsAt.Local().Format("15:04") + ")"
	case info.StopResetsAtRaw != "":
		return label + " (resets " + info.StopResetsAtRaw + ")"
	default:
		return label
	}
}

func stopReasonLabel(reason string) string {
	switch reason {
	case StopReasonRateLimited:
		return "rate limit"
	case StopReasonQuotaExceeded:
		return "quota exceeded"
	case StopReasonTerminalError:
		return "agent error"
	default:
		return ""
	}
}

// ClassifySession determines the lifecycle status of a session based on its metadata
// and whether it exists in the ledger. This is the single source of truth for session
// status — all display and cleanup code should use this instead of inline logic.
func ClassifySession(info SessionInfo, isUploaded bool) SessionStatus {
	if !info.Recording {
		// check stop reason for terminal states
		if info.StopReason == StopReasonCanceled {
			return StatusCanceled
		}
		// A ledger directory holding only a draft placeholder is NOT an
		// uploaded session — no turn data has been committed. Checked before
		// isUploaded because callers derive isUploaded from "a directory
		// exists in the ledger", which a draft satisfies.
		if info.Draft {
			return StatusDraft
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

	// ADR-020: active pause has its own status. Reported only while the agent
	// is alive — if PID is dead past grace, the existing orphan path handles
	// finalization with the paused range honored at upload.
	if info.SuspendedAt != nil {
		return StatusSuspended
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

// RawKind classifies what a raw.jsonl path actually holds.
//
// The distinction that matters is RawPointerStub vs RawSubstantive.
// A content-store pointer is ~130 bytes over 3 lines, so a naive
// "more than one line means real content" test reports it as a
// full transcript — which is exactly how GH #710 started: the
// summarizer was handed a pointer file, produced an empty title,
// failed validation, and retried forever.
type RawKind int

const (
	// RawMissing: the file does not exist or cannot be read.
	RawMissing RawKind = iota
	// RawHeaderOnly: exists, but holds only the metadata header line —
	// a recording that never captured anything.
	RawHeaderOnly
	// RawSubstantive: real transcript content, safe to summarize.
	RawSubstantive
	// RawPointerStub: an LFS pointer. The transcript is real but lives
	// in the content store; this file is only a reference to it.
	// Ledger clones are dehydrated by design, so this is the NORMAL
	// steady state for a synced session — it is not corruption, and it
	// must never be treated as content.
	RawPointerStub
)

// ClassifyRawFile reports what rawPath holds. Prefer this over
// HasSubstantiveEntries anywhere the pointer case needs distinct
// handling — several callers must treat a stub as "has data elsewhere"
// rather than "has no data", and collapsing the two is destructive.
func ClassifyRawFile(rawPath string) RawKind {
	// Pointer detection first: it is a cheap stat + small read, and a
	// pointer would otherwise be miscounted as multi-line content.
	if lfs.IsPointerFile(rawPath) {
		return RawPointerStub
	}

	f, err := os.Open(rawPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RawMissing
		}
		// Present but unreadable (permission / transient I/O). Fail SAFE — never
		// let a cleanup path delete a raw.jsonl we simply could not open; only a
		// genuinely absent file is RawMissing.
		return RawSubstantive
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	lineCount := 0
	for scanner.Scan() {
		lineCount++
		if lineCount >= 2 {
			return RawSubstantive // at least one line beyond header
		}
	}
	// Fail SAFE on an incomplete read. A line exceeding the 256 KiB scanner
	// buffer (a large paste or tool payload) stops the scan with an error; a
	// too-long line is itself evidence of substantial content, and reporting
	// RawHeaderOnly here would let the cleanup paths os.RemoveAll a real
	// session. Never classify what we could not read as a deletable phantom.
	if scanner.Err() != nil {
		return RawSubstantive
	}
	return RawHeaderOnly
}

// HasUserTurn reports whether a raw.jsonl file contains at least one user turn
// (an entry of type "user") — i.e. a real coworker prompt was recorded. The
// metadata header and machine-generated entries (assistant/tool/system) do NOT
// count: a recording with no user turn never happened from the coworker's point
// of view, and must not be git-committed as a draft nor registered with the
// server. This is the floor that keeps phantom sessions off the ledger and out
// of the team's session count.
//
// A content-store pointer stub reports true: its transcript is real and already
// passed this gate when it was finalized.
func HasUserTurn(rawPath string) bool {
	if lfs.IsPointerFile(rawPath) {
		return true
	}

	f, err := os.Open(rawPath)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		if entry.Type == "user" {
			return true
		}
	}
	// Fail safe on an incomplete read: a user entry larger than the 256 KiB
	// scanner buffer stops the scan with an error, and reporting "no user turn"
	// would suppress registration and draft publication for a real session with
	// a large prompt. When we could not read the file fully, assume a user turn.
	if scanner.Err() != nil {
		return true
	}
	return false
}

// HasSubstantiveEntries returns true if a raw.jsonl file holds at least one
// entry beyond the metadata header line. A header-only file (1 line) has no
// real session content and should not be uploaded or finalized.
//
// A content-store pointer stub is NOT substantive: the bytes present are a
// reference, not a transcript. Callers that need to distinguish "no data"
// from "data lives in the content store" must use ClassifyRawFile.
//
// This is the canonical check — use it everywhere instead of inline line counting.
func HasSubstantiveEntries(rawPath string) bool {
	return ClassifyRawFile(rawPath) == RawSubstantive
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
