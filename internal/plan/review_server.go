package plan

// review_server.go — persisted identity for the `ox plan review` localhost
// server, so a review session SURVIVES the server process dying.
//
// The problem this solves: the review page's unsent marks live in the
// browser's localStorage, which is scoped to the ORIGIN (scheme+host+port).
// If a restarted server binds a fresh ephemeral port, the old tab's marks are
// stranded on an origin that will never be served again, and the old tab can
// never reconnect (its EventSource dies permanently on the new server's 403).
// Review feedback is sacred-tier data — so the server's port and token are
// made stable per plan:
//
//   - Port: derived deterministically from the plan's dated dir name, with the
//     last successfully-used port persisted and preferred. Restart → same
//     origin → the open tab reconnects and its localStorage marks survive.
//   - Token: persisted alongside, so the tab's retrying EventSource and queued
//     POSTs authenticate against the restarted server without a reload.
//
// The state lives in the ledger's `.sageox/cache/` (local-only derived data,
// never committed — see .claude/rules/ledger-cache.md), file mode 0600. Threat
// model unchanged from the per-run token: the server binds 127.0.0.1 only, so
// the token only ever gates same-machine callers, same trust domain as the
// cache file itself.

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/fileutil"
)

// Stable review ports live in 42600–42999: outside the OS ephemeral ranges
// (32768+ on Linux picks high; macOS uses 49152–65535) so an OS-assigned
// socket rarely squats a slot, and 400 slots keeps same-machine collisions
// between plans unlikely. Collisions are handled by probing anyway.
const (
	reviewPortBase  = 42600
	reviewPortSlots = 400
)

// reviewServerCacheDir is the ledger-cache subdir holding per-plan state.
const reviewServerCacheDir = "plan-review"

// ReviewServerState is the persisted identity of a plan's review server.
type ReviewServerState struct {
	Port  int    `json:"port"`  // last port actually served on
	Token string `json:"token"` // review token the served pages carry
}

// StableReviewPort maps a plan's dated dir name to its deterministic default
// port. Same plan → same port on every run, on every machine.
func StableReviewPort(planDirName string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(planDirName))
	return reviewPortBase + int(h.Sum32()%reviewPortSlots)
}

// reviewServerStatePath resolves the state file for a plan. Empty when the
// project has no ledger (fail-open — callers fall back to ephemeral behavior).
func reviewServerStatePath(gitRoot, planDirName string) string {
	ledger := ledgerPathFor(gitRoot)
	if ledger == "" || planDirName == "" || planDirName != filepath.Base(planDirName) {
		return ""
	}
	return filepath.Join(ledger, ".sageox", "cache", reviewServerCacheDir, planDirName+".json")
}

// LoadReviewServerState reads a plan's persisted server identity. ok is false
// when there is none (first review, no ledger, or unreadable state).
func LoadReviewServerState(gitRoot, planDirName string) (ReviewServerState, bool) {
	path := reviewServerStatePath(gitRoot, planDirName)
	if path == "" {
		return ReviewServerState{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ReviewServerState{}, false
	}
	var st ReviewServerState
	if err := json.Unmarshal(b, &st); err != nil || st.Token == "" {
		return ReviewServerState{}, false
	}
	return st, true
}

// SaveReviewServerState persists a plan's server identity (0600 — it carries
// the review token). Best-effort callers may ignore the error: without state
// the next run simply mints a fresh identity, which is the pre-existing
// behavior, not a data loss.
func SaveReviewServerState(gitRoot, planDirName string, st ReviewServerState) error {
	path := reviewServerStatePath(gitRoot, planDirName)
	if path == "" {
		return fmt.Errorf("no ledger configured: cannot persist review server state")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create review state dir: %w", err)
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode review state: %w", err)
	}
	return fileutil.AtomicWriteBytes(path, b, 0o600)
}
