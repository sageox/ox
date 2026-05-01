// Package autofix is the daemon-side periodic auto-fix scheduler
// (ox-0xgx). It hosts a registry of self-contained, auto-fix-safe
// checks the daemon runs on a slow ticker so silent drift in a user's
// workspace gets repaired without anyone having to type `ox doctor`.
//
// What belongs here vs cmd/ox/doctor_*.go:
//
//   - cmd/ox/doctor_*.go is the user-facing diagnostic suite — many
//     checks need CLI-process context (findGitRoot, prompts, output
//     rendering) and shouldn't run unattended.
//   - internal/doctor/autofix is the daemon's home for checks that
//     are (a) self-contained — operate on a path passed in, no CLI
//     globals; (b) idempotent and bounded blast radius — single repo,
//     no network operations beyond the daemon's existing scope; (c)
//     safe to run unattended on a slow cadence.
//
// Migration is incremental. New auto-safe checks land here directly;
// existing cmd/ox/doctor_*.go checks get adapter functions that
// delegate to a shared implementation. See ox-0xgx for the running
// list and ordering.
package autofix

import (
	"context"
	"sync"
	"time"
)

// CheckResult is the structured outcome of a single check invocation.
// Designed to be cheap to emit on every tick, even when nothing
// changed — the daemon's IssueTracker dedupes on (Slug, Repo).
type CheckResult struct {
	Slug    string // matches the cmd/ox/doctor_types.go slugs where applicable
	Status  Status
	Summary string // one-line user-facing message
	Repo    string // workspace identifier when relevant; empty for global checks
}

type Status int

const (
	StatusClean   Status = iota // nothing to do; the workspace is healthy
	StatusFixed                 // detected drift, applied a safe fix
	StatusFound                 // detected drift, no auto-fix applied (e.g., requires user)
	StatusError                 // check itself errored — log and surface; do not retry in tight loop
)

// CheckFunc is the contract every registered check implements.
//
// The function MUST be:
//   - Safe to call on a slow ticker (i.e. cheap when there's nothing
//     to do — the common case).
//   - Concurrency-safe with itself (the scheduler may invoke the same
//     check across N workspaces in parallel).
//   - Self-contained: every input is on the receiver path or
//     environment; no reliance on cmd/ox CLI globals.
//
// `repoPath` is the workspace the check should operate on. Empty
// string when the check is global (rare; prefer per-workspace).
type CheckFunc func(ctx context.Context, repoPath string) CheckResult

// Check is a registry entry. Slug is the canonical identifier and
// matches cmd/ox/doctor_types.go's CheckSlug* constants where the
// daemon-side check shares logic with the CLI doctor check.
type Check struct {
	Slug         string
	Description  string
	Run          CheckFunc
	MinInterval  time.Duration // throttle: don't run more often than this
	BlastRadius  string        // human-readable: "single workspace", "single file", etc. — for ops review
	lastRunMu    sync.Mutex
	lastRunAt    time.Time
}

// Registry holds the set of auto-fix checks the daemon will run.
//
// Intentionally simple: append-only Register, snapshot All for the
// scheduler to iterate. We don't expose a remove or replace because
// the only caller is daemon startup, which builds the registry once.
type Registry struct {
	mu     sync.RWMutex
	checks []*Check
}

// NewRegistry returns an empty registry. The default registry that
// ships with the daemon is built by Default() below.
func NewRegistry() *Registry { return &Registry{} }

// Register adds a check. Slug must be unique; double-registration
// of the same slug overwrites — caller responsibility, useful for
// tests that want to swap a check for a stub.
func (r *Registry) Register(c *Check) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, existing := range r.checks {
		if existing.Slug == c.Slug {
			r.checks[i] = c
			return
		}
	}
	r.checks = append(r.checks, c)
}

// All returns a snapshot of the registered checks. The slice is owned
// by the caller; mutations don't affect the registry.
func (r *Registry) All() []*Check {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Check, len(r.checks))
	copy(out, r.checks)
	return out
}

// shouldRun reports whether enough time has elapsed since the last
// run to honor MinInterval. Updates lastRunAt on success so the next
// caller respects the throttle.
//
// Throttling is intentionally a property of the *check*, not the
// scheduler — different checks have different "expensive enough that
// we shouldn't run them every tick" thresholds, and the registry is
// the right place for that to live.
func (c *Check) shouldRun(now time.Time) bool {
	c.lastRunMu.Lock()
	defer c.lastRunMu.Unlock()
	if c.MinInterval > 0 && !c.lastRunAt.IsZero() && now.Sub(c.lastRunAt) < c.MinInterval {
		return false
	}
	c.lastRunAt = now
	return true
}
