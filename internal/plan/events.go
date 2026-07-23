package plan

// events.go is the append-only lifecycle+provenance log living beside a
// captured plan: <planDir>/events.jsonl, one JSON object per line, oldest
// first. It is the SINGLE source of truth for a plan's lifecycle
// (created/revised/approved/worked/realized/abandoned/superseded) and every
// session that touched it — meta.json's Status/Provenance fields remain a
// convenience snapshot for cheap reads, but events.jsonl is what a reader
// replays to reconstruct current state (see Fold) and history.
//
// Why an append-only log rather than mutating meta.json further: meta.json
// already answers "what is true now"; it cannot answer "what happened, in
// what order, across how many sessions" without layering on more mutable
// fields that a concurrent writer could clobber. An append-only line per
// event sidesteps that — nothing already on disk is ever rewritten, so two
// processes recording events for the same plan can never lose each other's
// history, only interleave it (and interleaving is fine: Fold is order-
// tolerant for everything except relying on file order for CreatedAt/
// UpdatedAt, which matches how AppendEvent actually writes).
//
// Locking mirrors AppendResolution in feedback.go exactly: the file is a
// shared resource across processes (CLI, daemon, a future reconciler), so
// the read-modify-write that appends a line runs under fileutil.WithFileLock
// rather than a bare os.OpenFile(O_APPEND) — two concurrent appends without
// a lock can both read the same "existing" bytes and each write back a
// truncated file containing only their own line, silently dropping the
// other's event.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/fileutil"
)

// eventsFile is the append-only events log filename, stored alongside
// plan.md/meta.json inside a captured plan's directory.
const eventsFile = "events.jsonl"

// PlanStatusRealized is the artifact-agnostic terminal status: the plan's
// intent was carried out by some means, not necessarily "implemented" (code
// shipped) — e.g. a decision plan whose realized outcome was choosing not to
// build. Defined here (rather than alongside the other PlanStatus* constants
// in store.go) to keep the event log an additive layer with zero edits to
// existing plan-status logic.
const PlanStatusRealized PlanStatus = "realized"

// EventKind is the lifecycle or provenance action one events.jsonl line
// records.
type EventKind string

const (
	EventCreated       EventKind = "created"
	EventRevised       EventKind = "revised"
	EventApproved      EventKind = "approved"
	EventWorked        EventKind = "worked"
	EventRealized      EventKind = "realized"
	EventAbandoned     EventKind = "abandoned"
	EventSuperseded    EventKind = "superseded"
	EventVisibilitySet EventKind = "visibility-set"
)

// validEventKind guards AppendEvent against a typo'd/unknown kind reaching
// disk, mirroring feedback.go's validStatus/validResolutionState checks.
func validEventKind(k EventKind) bool {
	switch k {
	case EventCreated, EventRevised, EventApproved, EventWorked, EventRealized, EventAbandoned, EventSuperseded, EventVisibilitySet:
		return true
	}
	return false
}

// Event is one line of a plan's events.jsonl. PlanID is carried on every
// line (not just implied by the containing directory) so a line remains
// self-describing if ever copied, replayed, or ingested elsewhere.
// SessionID/SessionName are two-phase like Provenance's (see store.go):
// SessionName is the durable join key available while a recording is live;
// SessionID is the canonical ses_<UUIDv7> backfilled once known, typically
// via a later event on the same plan rather than an edit to this one.
type Event struct {
	Timestamp   time.Time  `json:"ts"`
	PlanID      string     `json:"plan_id"`
	Kind        EventKind  `json:"event"`
	SessionID   string     `json:"session_id,omitempty"`
	SessionName string     `json:"session_name,omitempty"`
	AgentID     string     `json:"agent_id,omitempty"`
	AgentEnv    string     `json:"agent_env,omitempty"`
	Model       string     `json:"model,omitempty"`
	Author      string     `json:"author,omitempty"`
	Status      PlanStatus `json:"status,omitempty"`
	Produced    string     `json:"produced,omitempty"`
	// Reason and SupersededBy are set by the lifecycle-verb engine (see
	// lifecycle.go): Reason on an abandoned event (the --reason text),
	// SupersededBy on a superseded event (the successor plan's slug). No
	// other event kind sets either.
	Reason       string `json:"reason,omitempty"`
	SupersededBy string `json:"superseded_by,omitempty"`
	Visibility   string `json:"visibility,omitempty"`
	// Topic carries the plan's topic on created/revised events, so Fold can
	// recover it without also reading meta.json.
	Topic string `json:"topic,omitempty"`
}

// AppendEvent adds one line to a plan's events.jsonl under an advisory
// flock — the exact locking idiom AppendResolution uses in feedback.go.
// Multiple processes (the CLI, the daemon, a future reconciler) can record
// events for the same plan concurrently; the flocked read-modify-write
// ensures one append can never silently overwrite another.
//
// ev.Timestamp defaults to time.Now().UTC() when zero, so most callers can
// omit it; tests that need deterministic ordering should set it explicitly.
func AppendEvent(ctx context.Context, planDir string, ev Event) error {
	if ev.PlanID == "" {
		return fmt.Errorf("append event: empty plan_id")
	}
	if !validEventKind(ev.Kind) {
		return fmt.Errorf("append event: invalid kind %q", ev.Kind)
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		return fmt.Errorf("create plan dir: %w", err)
	}

	path := filepath.Join(planDir, eventsFile)
	return fileutil.WithFileLock(ctx, path, func() error {
		existing, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read events: %w", err)
		}
		line, err := json.Marshal(ev)
		if err != nil {
			return fmt.Errorf("encode event: %w", err)
		}
		var out []byte
		out = append(out, existing...)
		out = append(out, line...)
		out = append(out, '\n')
		return fileutil.AtomicWriteBytes(path, out, 0o644)
	})
}

// LoadEvents reads a plan's events.jsonl, oldest first (append order). A
// missing file is not an error — a plan with no lifecycle events recorded
// yet simply has none. Unlike feedback.go's per-round LoadAllFeedback (which
// skips a malformed round because rounds are browser-authored, untrusted
// input), events.jsonl is written only by AppendEvent, so a line that fails
// to parse indicates corruption worth surfacing rather than silently
// dropping lifecycle history.
func LoadEvents(planDir string) ([]Event, error) {
	path := filepath.Join(planDir, eventsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read events: %w", err)
	}

	var events []Event
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue // tolerate the trailing newline every append writes, and any blank line
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("parse events line: %w", err)
		}
		events = append(events, ev)
	}
	return events, nil
}

// BackfillSessionID rewrites events.jsonl lines whose SessionName matches
// sessionName and whose SessionID is still empty, setting SessionID —
// the same one-time id-reconciliation ReconcileSessionOutcome already
// performs on meta.json's Provenance at session-stop (see store.go),
// applied here to the append-only event log. events.jsonl is otherwise
// immutable by design (see the package comment above): this is the sole,
// narrowly-scoped exception, and it only ever WIDENS a line (fills a
// previously-empty field) — it never changes a value a reader has already
// observed, and only ever targets the calling session's own lines (matched
// by SessionName, the durable pre-canonical join key). Runs under the same
// flock AppendEvent/AppendPlanEvent use, so it can never race a concurrent
// append. Idempotent: a line already carrying a SessionID (this session's or
// any other's) is left untouched, so re-running after a successful backfill
// updates nothing. Returns the number of lines changed; sessionName == "" or
// sessionID == "" is a no-op, not an error (nothing to key off of / nothing
// to write).
func BackfillSessionID(ctx context.Context, planDir, sessionName, sessionID string) (int, error) {
	if sessionName == "" || sessionID == "" {
		return 0, nil
	}

	path := filepath.Join(planDir, eventsFile)
	count := 0
	err := fileutil.WithFileLock(ctx, path, func() error {
		events, loadErr := LoadEvents(planDir)
		if loadErr != nil {
			return fmt.Errorf("load events: %w", loadErr)
		}
		if len(events) == 0 {
			return nil // no events.jsonl yet (or empty) — nothing to backfill
		}

		for i := range events {
			if events[i].SessionName == sessionName && events[i].SessionID == "" {
				events[i].SessionID = sessionID
				count++
			}
		}
		if count == 0 {
			return nil
		}

		var buf bytes.Buffer
		for _, ev := range events {
			line, mErr := json.Marshal(ev)
			if mErr != nil {
				return fmt.Errorf("encode event: %w", mErr)
			}
			buf.Write(line)
			buf.WriteByte('\n')
		}
		return fileutil.AtomicWriteBytes(path, buf.Bytes(), 0o644)
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// FoldedState is the current-state projection of a plan's events.jsonl,
// derived entirely by replaying the log — never itself stored. Two callers
// folding the same event slice always get identical output.
type FoldedState struct {
	PlanID     string
	Topic      string
	Status     PlanStatus
	Authors    []string // distinct Author values, first-seen order
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Visibility string
	Sessions   []SessionRef // distinct sessions, first-seen order
}

// SessionRef is one distinct session that touched a plan, folded from every
// event it produced — the unit a "related sessions" UI lists per plan.
// Sessions are deduplicated by SessionName (falling back to SessionID when
// SessionName wasn't set on that event); Kinds/FirstAt/LastAt accumulate
// across every event sharing that key.
type SessionRef struct {
	SessionName string
	SessionID   string
	Author      string
	Kinds       []EventKind // distinct kinds this session produced, first-seen order
	FirstAt     time.Time
	LastAt      time.Time
}

// defaultVisibility is what a plan is treated as before any visibility-set
// event has ever been recorded — private until a human explicitly shares it.
const defaultVisibility = "private"

// Fold replays a plan's event log into its current state. Pure and
// deterministic — no IO, and the same input slice always yields the same
// FoldedState. An empty/nil slice folds to the same defaults a never-
// touched plan would have (draft status, private visibility, no sessions).
//
// Field-by-field derivation:
//   - Topic: Topic of the latest created/revised event that carries one.
//   - Status: Status of the latest status-bearing event; defaults to draft.
//   - Visibility: Visibility of the latest event that carries one; defaults
//     to private.
//   - CreatedAt: timestamp of the first "created" event; falls back to the
//     first event overall if the log has no created event (e.g. one
//     backfilled starting mid-lifecycle).
//   - UpdatedAt: timestamp of the last event in the slice.
func Fold(events []Event) FoldedState {
	state := FoldedState{
		Status:     PlanStatusDraft,
		Visibility: defaultVisibility,
	}
	if len(events) == 0 {
		return state
	}

	state.PlanID = events[0].PlanID
	state.CreatedAt = events[0].Timestamp
	state.UpdatedAt = events[len(events)-1].Timestamp

	seenAuthor := make(map[string]bool, len(events))
	sessionsByKey := make(map[string]*SessionRef, len(events))
	var sessionOrder []string
	haveCreated := false

	for _, ev := range events {
		if ev.Kind == EventCreated && !haveCreated {
			state.CreatedAt = ev.Timestamp
			haveCreated = true
		}
		if ev.Timestamp.After(state.UpdatedAt) {
			state.UpdatedAt = ev.Timestamp // defensive: tolerate a slice not already in file/append order
		}

		if (ev.Kind == EventCreated || ev.Kind == EventRevised) && ev.Topic != "" {
			state.Topic = ev.Topic
		}
		if ev.Status != "" {
			state.Status = ev.Status
		}
		if ev.Visibility != "" {
			state.Visibility = ev.Visibility
		}
		if ev.Author != "" && !seenAuthor[ev.Author] {
			seenAuthor[ev.Author] = true
			state.Authors = append(state.Authors, ev.Author)
		}

		key := ev.SessionName
		if key == "" {
			key = ev.SessionID
		}
		if key == "" {
			continue // this event isn't attributable to a specific session (e.g. a system reconciler)
		}
		ref, ok := sessionsByKey[key]
		if !ok {
			ref = &SessionRef{SessionName: ev.SessionName, SessionID: ev.SessionID, Author: ev.Author, FirstAt: ev.Timestamp, LastAt: ev.Timestamp}
			sessionsByKey[key] = ref
			sessionOrder = append(sessionOrder, key)
		} else {
			// progressive enrichment: a later event on the same session may
			// carry the SessionID backfilled at stop, or an Author the
			// earliest event didn't have set.
			if ref.SessionName == "" {
				ref.SessionName = ev.SessionName
			}
			if ref.SessionID == "" {
				ref.SessionID = ev.SessionID
			}
			if ref.Author == "" {
				ref.Author = ev.Author
			}
			if ev.Timestamp.Before(ref.FirstAt) {
				ref.FirstAt = ev.Timestamp
			}
			if ev.Timestamp.After(ref.LastAt) {
				ref.LastAt = ev.Timestamp
			}
		}
		if !containsKind(ref.Kinds, ev.Kind) {
			ref.Kinds = append(ref.Kinds, ev.Kind)
		}
	}

	if len(sessionOrder) > 0 {
		state.Sessions = make([]SessionRef, 0, len(sessionOrder))
		for _, key := range sessionOrder {
			state.Sessions = append(state.Sessions, *sessionsByKey[key])
		}
	}
	return state
}

func containsKind(kinds []EventKind, k EventKind) bool {
	for _, existing := range kinds {
		if existing == k {
			return true
		}
	}
	return false
}
