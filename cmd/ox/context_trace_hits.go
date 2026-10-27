package main

import (
	"log/slog"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
)

// Surfaced-item trace: every ox command that puts a specific team item in
// front of the AI coworker (a query hit, a conversation, a plan, a session)
// records one `provided` event per item, so the item becomes something the
// coworker can later cite and report using. The event carries the item's
// ref, type, title and link — never its content (ADR-027 rule 4).

// maxProvidedHitsPerCall bounds how many items one command call surfaces into
// the trace. Search-shaped commands return more; the top few are what an AI
// coworker plausibly acts on, and the trace must stay small.
const maxProvidedHitsPerCall = 5

// maxProvidedTitleRunes caps a surfaced item's title. A title is a display
// label, not content; the cap keeps a pathological title from turning the
// trace into a copy of the item.
const maxProvidedTitleRunes = 120

// providedHit is one surfaced item plus its retrieval score (0 = none).
// Score lives here, not on contextuse.SurfacedRef, because it describes the
// retrieval that surfaced the item, not the item.
type providedHit struct {
	contextuse.SurfacedRef
	Score float64
}

// traceProvidedHits appends one `provided` event per hit to the AI coworker's
// live session trace. It is a no-op without an agent ID or a live recording
// (a surfaced item only matters inside a session it could be used in), and
// fail-open: nothing here may break the command that surfaced the items.
func traceProvidedHits(projectRoot, agentID string, source contexttrace.SourceType, mechanism contexttrace.Mechanism, hits []providedHit) {
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("provided-hits trace: recovered", "panic", r)
		}
	}()
	if projectRoot == "" || agentID == "" || len(hits) == 0 {
		return
	}
	if !config.TraceConsultationsEnabled() {
		return
	}
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil || state == nil || state.SessionPath == "" {
		if err != nil {
			slog.Debug("provided-hits trace: recording state", "agent_id", agentID, "err", err)
		}
		return
	}
	w := contexttrace.NewWriter(state.SessionPath)
	existing, err := contexttrace.ReadEvents(w.Path())
	if err != nil {
		slog.Debug("provided-hits trace: read existing", "err", err)
	}
	events := providedHitEvents(existing, source, mechanism, state.EntryCount, hits)
	if len(events) == 0 {
		return
	}
	if err := w.AppendAll(events); err != nil {
		slog.Debug("provided-hits trace: append", "agent_id", agentID, "err", err)
	}
}

// providedHitEvents builds the events traceProvidedHits writes: refless hits
// dropped, duplicates (by ref at this seq, within the call or already in the
// trace) skipped, at most maxProvidedHitsPerCall kept.
func providedHitEvents(existing []contexttrace.Event, source contexttrace.SourceType, mechanism contexttrace.Mechanism, seq int, hits []providedHit) []contexttrace.Event {
	seen := map[string]bool{}
	for _, ev := range existing {
		if ev.Type == contexttrace.EventProvided && ev.Ref != "" && ev.Seq == seq {
			seen[ev.Ref] = true
		}
	}
	ts := contexttrace.Now()
	var out []contexttrace.Event
	for _, h := range hits {
		if len(out) >= maxProvidedHitsPerCall {
			break
		}
		ref := strings.TrimSpace(h.Ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, contexttrace.Event{
			Type:      contexttrace.EventProvided,
			Timestamp: ts,
			Source:    source,
			Mechanism: mechanism,
			Seq:       seq,
			Ref:       ref,
			RefType:   h.RefType,
			Title:     capRunes(strings.TrimSpace(h.Title), maxProvidedTitleRunes),
			URL:       h.URL,
			FromName:  h.FromName,
			Score:     h.Score,
			Aliases:   h.Aliases,
		})
	}
	return out
}

// hitAliases returns the other names a surfaced item goes by, in order:
// empties, "." (filepath.Base of an empty path), ref itself, and repeats
// dropped; nil when none remain.
func hitAliases(ref string, names ...string) []string {
	var out []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || n == "." || n == ref || slices.Contains(out, n) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// traceAgentIDFromEnv returns the AI coworker's agent ID for commands invoked
// directly (not via `ox agent <id> …`): SAGEOX_AGENT_ID when it is a real one.
func traceAgentIDFromEnv() string {
	if id := os.Getenv("SAGEOX_AGENT_ID"); agentinstance.IsValidAgentID(id) {
		return id
	}
	return ""
}

// capRunes truncates s to at most n runes, marking the cut with "…".
func capRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
