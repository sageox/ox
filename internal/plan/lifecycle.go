package plan

// lifecycle.go is the engine behind the `ox plan` lifecycle verbs — approve,
// work, realize, abandon, supersede (cmd/ox/plan_lifecycle.go) — and the
// review server's /approve endpoint (cmd/ox/plan_review.go). Every one of
// them funnels through AppendPlanEvent, so there is exactly one mechanism
// that ever appends a lifecycle event: browser Approve and `ox plan approve`
// are the same call.
//
// AppendPlanEvent does NOT call the exported AppendEvent (events.go) from
// inside its own critical section: AppendEvent acquires
// fileutil.WithFileLock itself, and that lock's in-process half
// (flock_inprocess.go) is a plain sync.Mutex — NOT reentrant. A nested
// acquire on the same lock path, even from the same goroutine, blocks
// forever waiting for a release that can never happen. Instead
// AppendPlanEvent takes the lock once and inlines the same
// read-marshal-append-atomic-write sequence AppendEvent uses — the same
// pattern feedback.go's AppendResolution already follows, for the identical
// reason (see its comment).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/sageox/ox/internal/fileutil"
)

// PlanEventFields carries the caller-supplied data for one lifecycle event:
// provenance resolved ONCE by the caller (cmd/ox's resolvePlanProvenance —
// never re-derived here) plus verb-specific payload. AppendPlanEvent derives
// Kind and Status itself from the kind argument; callers never set them.
type PlanEventFields struct {
	SessionID    string
	SessionName  string
	AgentID      string
	AgentEnv     string
	Model        string
	Author       string
	Produced     string // realize: what shipped (a PR URL, commit, etc.)
	Reason       string // abandon: why
	SupersededBy string // supersede: the successor plan's slug (already resolved + validated by the caller)
}

// lifecycleKindStatus maps a lifecycle-verb EventKind to the PlanStatus it
// carries, when it is status-bearing. EventWorked is intentionally absent:
// it is edge-only (recording that a session touched the plan) and must never
// set Status.
func lifecycleKindStatus(kind EventKind) (PlanStatus, bool) {
	switch kind {
	case EventApproved:
		return PlanStatusApproved, true
	case EventRealized:
		return PlanStatusRealized, true
	case EventAbandoned:
		return PlanStatusAbandoned, true
	case EventSuperseded:
		return PlanStatusSuperseded, true
	}
	return "", false
}

// isLifecycleVerbKind restricts AppendPlanEvent to the 5 verb kinds it
// actually models. EventCreated/EventRevised belong to Save() (a different
// shape entirely: first-save id-minting, not this idempotency check) and
// EventVisibilitySet has no verb yet — keeping AppendPlanEvent closed over a
// known kind set means a future kind added to events.go can't silently fall
// through here with the wrong Status/idempotency behavior.
func isLifecycleVerbKind(kind EventKind) bool {
	switch kind {
	case EventApproved, EventWorked, EventRealized, EventAbandoned, EventSuperseded:
		return true
	}
	return false
}

// AppendPlanEvent appends one lifecycle event to a plan's events.jsonl under
// the same advisory flock AppendEvent uses (see the package comment above),
// after checking — inside that same critical section — whether doing so
// would actually change the plan's projected state. Re-running an
// already-applied verb (e.g. approving an already-approved plan) is then a
// safe no-op: changed=false, nothing written to either events.jsonl or
// meta.json.
//
// Status-bearing kinds (approved/realized/abandoned/superseded) ALSO update
// meta.json via MutatePlanMeta — additive dual-write, exactly like Save's own
// meta.json/events.jsonl pair: events.jsonl remains the source of truth a
// reader folds to reconstruct state, meta.json stays a cheap-read snapshot.
// EventRealized is both status-bearing AND session-attributed, so a single
// call here updates both axes with the one appended event — no special-case
// branch. The meta.json write is best-effort (logged, not returned): a
// dual-write hiccup must never undo the events.jsonl append that already
// succeeded, the same treatment Save() gives its own secondary write.
func AppendPlanEvent(ctx context.Context, planDir string, kind EventKind, fields PlanEventFields) (changed bool, err error) {
	if !isLifecycleVerbKind(kind) {
		return false, fmt.Errorf("append plan event: %q is not a lifecycle-verb kind", kind)
	}
	if kind == EventSuperseded && fields.SupersededBy == "" {
		return false, fmt.Errorf("append plan event: superseded requires SupersededBy")
	}

	eventsPath := filepath.Join(planDir, eventsFile)
	err = fileutil.WithFileLock(ctx, eventsPath, func() error {
		before, loadErr := LoadEvents(planDir)
		if loadErr != nil {
			return fmt.Errorf("load existing events: %w", loadErr)
		}
		if len(before) == 0 {
			return fmt.Errorf("no plan history at %s — save the plan before recording a lifecycle event", planDir)
		}

		candidate := Event{
			Timestamp:    time.Now().UTC(),
			PlanID:       before[0].PlanID,
			Kind:         kind,
			SessionID:    fields.SessionID,
			SessionName:  fields.SessionName,
			AgentID:      fields.AgentID,
			AgentEnv:     fields.AgentEnv,
			Model:        fields.Model,
			Author:       fields.Author,
			Produced:     fields.Produced,
			Reason:       fields.Reason,
			SupersededBy: fields.SupersededBy,
		}
		if status, ok := lifecycleKindStatus(kind); ok {
			candidate.Status = status
		}

		if !candidateChangesState(before, candidate) {
			return nil // no-op: changed stays false, nothing written
		}

		line, mErr := json.Marshal(candidate)
		if mErr != nil {
			return fmt.Errorf("encode event: %w", mErr)
		}
		existing, rErr := os.ReadFile(eventsPath)
		if rErr != nil {
			return fmt.Errorf("read events: %w", rErr)
		}
		// Plain append (no pre-sized make with len arithmetic) mirrors
		// AppendEvent in events.go — avoids the CodeQL allocation-size-overflow
		// pattern; event-log writes are small so pre-sizing buys nothing.
		var out []byte
		out = append(out, existing...)
		out = append(out, line...)
		out = append(out, '\n')
		if wErr := fileutil.AtomicWriteBytes(eventsPath, out, 0o644); wErr != nil {
			return fmt.Errorf("write events: %w", wErr)
		}
		changed = true

		if status, ok := lifecycleKindStatus(kind); ok {
			if dErr := MutatePlanMeta(ctx, planDir, func(m *Meta) (*Meta, error) {
				if m == nil {
					return nil, nil
				}
				m.Status = status
				return m, nil
			}); dErr != nil {
				slog.Warn("plan lifecycle: meta.json dual-write failed", "error", dErr, "dir", planDir, "kind", kind)
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// candidateChangesState reports whether appending candidate to before would
// actually change the plan's projected state — the check AppendPlanEvent
// makes, under lock, to decide whether to append at all.
//
// "Projected state" here is narrower than a byte-for-byte FoldedState
// comparison: Fold always advances CreatedAt/UpdatedAt and a session's
// LastAt on every event by construction, so comparing those would defeat
// idempotency (a no-op re-run's candidate always carries a newer timestamp
// than anything already on disk). Only SEMANTIC fields are compared: Status,
// Reason, SupersededBy, and — for the two session-edge kinds (Worked /
// Realized) — whether the acting session's set of distinct edge kinds gains
// a new one.
func candidateChangesState(before []Event, candidate Event) bool {
	p0 := Fold(before)
	after := append(slices.Clone(before), candidate)
	p1 := Fold(after)

	if p0.Status != p1.Status {
		return true
	}
	if latestReason(before) != latestReason(after) {
		return true
	}
	if latestSupersededBy(before) != latestSupersededBy(after) {
		return true
	}
	if candidate.Kind == EventWorked || candidate.Kind == EventRealized {
		// An unattributed edge event carries no session key, so Fold's own
		// Sessions aggregation can't dedupe it against history (a
		// session-less event contributes nothing to Sessions — see Fold) —
		// always record it rather than silently drop the only evidence
		// anything happened. An attributed one dedupes correctly: p1's
		// Sessions only differs from p0's when this session gains a
		// genuinely new distinct kind.
		if candidate.SessionName == "" && candidate.SessionID == "" {
			return true
		}
		if !sessionsEqual(p0.Sessions, p1.Sessions) {
			return true
		}
	}
	return false
}

// latestReason and latestSupersededBy mirror Fold's own "latest non-empty
// wins" derivation (see Fold's Status/Topic/Visibility handling) for the two
// fields FoldedState doesn't expose — reading the raw Event slice directly
// rather than widening the exported projection for an idempotency-only need.
func latestReason(events []Event) string {
	var v string
	for _, ev := range events {
		if ev.Reason != "" {
			v = ev.Reason
		}
	}
	return v
}

func latestSupersededBy(events []Event) string {
	var v string
	for _, ev := range events {
		if ev.SupersededBy != "" {
			v = ev.SupersededBy
		}
	}
	return v
}

// sessionsEqual reports whether two folded session lists carry the same
// identity + distinct-kinds content, ignoring FirstAt/LastAt (which advance
// on every event by construction — see candidateChangesState).
func sessionsEqual(a, b []SessionRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].SessionName != b[i].SessionName ||
			a[i].SessionID != b[i].SessionID ||
			a[i].Author != b[i].Author ||
			!slices.Equal(a[i].Kinds, b[i].Kinds) {
			return false
		}
	}
	return true
}
