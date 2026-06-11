// Package agenttask implements a shared, project-local queue of work items
// scheduled by internal producers (the daemon, doctor, scripts) for the next
// available AI coworker to execute — typically as a fresh-context subagent.
//
// It is deliberately NOT a beads (bd) replacement: bd tracks human-facing
// project work, agenttask tracks ephemeral, machine-scheduled chores run on
// behalf of the developer's live session. See
// docs/specs/agent-task-scheduling.md for the full design.
package agenttask

import "time"

// Status is the lifecycle state of a task.
type Status string

const (
	// StatusReady is a task waiting to be claimed by an agent.
	StatusReady Status = "ready"
	// StatusInProgress is a task claimed by an agent and currently leased.
	StatusInProgress Status = "in_progress"
	// StatusCompleted is a task an agent finished successfully (terminal).
	StatusCompleted Status = "completed"
	// StatusCanceled is a task abandoned without completion (terminal).
	StatusCanceled Status = "canceled"
)

// Known task kinds. The vocabulary is closed on purpose: the surfacing/guidance
// layer maps each kind to a FIXED ox action (a playbook), so the agent never
// derives what to run from free-form task text. An unknown kind has no playbook
// and must not be auto-executed.
const (
	KindDoctor          = "doctor"
	KindSessionFinalize = "session-finalize"
	KindAntiEntropy     = "anti-entropy"
	KindCustom          = "custom"
)

var knownKinds = map[string]bool{
	KindDoctor:          true,
	KindSessionFinalize: true,
	KindAntiEntropy:     true,
	KindCustom:          true,
}

// ValidKind reports whether a kind is in the closed vocabulary. Empty is allowed
// (treated as unclassified) so producers may omit it.
func ValidKind(kind string) bool {
	return kind == "" || knownKinds[kind]
}

// Size limits bound a single task so a hostile or buggy producer cannot flood
// the agent's context or wedge the queue with an oversized row.
const (
	MaxTitleLen   = 200
	MaxBodyLen    = 4096
	MaxPayloadLen = 8192 // total bytes across payload keys+values
)

// DefaultLease is how long a claimed task stays in_progress before the store
// reconciles it back to ready (assuming the claimer did not complete or extend
// it). Picked to be long enough for a subagent to summarize a session but
// short enough that a crashed agent's work is rescheduled promptly.
const DefaultLease = 15 * time.Minute

// Task is a single unit of scheduled agent work.
//
// Field tags mirror the JSONL on-disk format. Optional fields are omitempty so
// the ledger stays compact and so that older rows (missing newer fields)
// round-trip cleanly through last-write-wins reads.
type Task struct {
	ID          string `json:"id"`                     // UUIDv7 (time-sortable)
	Title       string `json:"title"`                  // short summary shown to the agent
	Body        string `json:"body,omitempty"`         // fuller instruction for the executor
	Kind        string `json:"kind,omitempty"`         // category: doctor, session-finalize, anti-entropy, custom
	Priority    int    `json:"priority"`               // lower = higher priority (matches agentwork.WorkItem)
	Status      Status `json:"status"`                 // ready | in_progress | completed | canceled
	Source      string `json:"source,omitempty"`       // producer: daemon, doctor, cli
	TargetAgent string `json:"target_agent,omitempty"` // restrict to an agent type; "" = any
	DedupKey    string `json:"dedup_key,omitempty"`    // at most one active (non-terminal) task per key

	Payload map[string]string `json:"payload,omitempty"` // optional structured data for the executor

	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at,omitempty"` // zero = never; dropped once past

	// Lease fields — populated only while Status == in_progress.
	ClaimedByAgentID string    `json:"claimed_by_agent_id,omitempty"` // ox internal agent id (e.g. Oxa7b3)
	ClaimedByPID     int       `json:"claimed_by_pid,omitempty"`      // PID of claiming process (host-local liveness)
	ClaimedHost      string    `json:"claimed_host,omitempty"`        // hostname; PID only meaningful on the same host
	ClaimedAt        time.Time `json:"claimed_at,omitempty"`
	LeaseExpiresAt   time.Time `json:"lease_expires_at,omitempty"` // reverts to ready if not completed by this time
	Attempts         int       `json:"attempts,omitempty"`         // incremented each time the task is (re)claimed

	// Terminal fields.
	CompletedAt time.Time `json:"completed_at,omitempty"` // when it reached a terminal state
	Result      string    `json:"result,omitempty"`       // optional note on completion/cancellation
}

// IsTerminal reports whether the task has reached a terminal state.
func (t *Task) IsTerminal() bool {
	return t.Status == StatusCompleted || t.Status == StatusCanceled
}

// IsExpired reports whether the task has an expiry that has passed.
// Tasks without an ExpiresAt never expire.
func (t *Task) IsExpired() bool {
	return !t.ExpiresAt.IsZero() && time.Now().After(t.ExpiresAt)
}

// Lease reclaim (lease-expired and same-host dead-claimer) is enforced by the
// store directly in SQL — see Store.reconcile / reclaimDeadClaimers. The
// same-host PID guard lives there so an empty/foreign claimed_host is never
// PID-checked against an unrelated local process.

// ClaimableBy reports whether a task targeted at a particular agent type may be
// claimed/surfaced to the given agent type. Exported wrapper over the internal
// match so callers outside the package (surfacing) use identical semantics.
func (t *Task) ClaimableBy(agentType string) bool {
	return t.matchesAgentType(agentType)
}

// matchesAgentType reports whether a ready task may be claimed by the given
// agent type. An empty TargetAgent matches any agent; otherwise the normalized
// types must be equal. An empty agentType (caller could not detect) only
// matches untargeted tasks.
func (t *Task) matchesAgentType(agentType string) bool {
	if t.TargetAgent == "" {
		return true
	}
	return NormalizeAgentType(t.TargetAgent) == NormalizeAgentType(agentType)
}

// NormalizeAgentType folds known agent-type aliases to their canonical slug so
// "claude-code" and "claude" target the same tasks. Unknown values are
// returned unchanged.
func NormalizeAgentType(s string) string {
	switch s {
	case "claude-code", "claudecode", "claude":
		return "claude"
	default:
		return s
	}
}
