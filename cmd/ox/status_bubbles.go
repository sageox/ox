package main

// status_bubbles.go — `ox status` knowledge-bubbles summary line.
//
// Replaces the historical "Team contexts: 3 / Ledger: synced" pair with a
// single scannable line sourced from the F3 three-source merger
// (internal/kb.Merger). Per the plan ("ox status" bullet, Knowledge
// Bubbles plan): one line, scannable, deprecated team_contexts/ledger
// JSON mirrors retained for one release.
//
// Format:
//
//	Knowledge bubbles: <total> (<n> personal, <n> profile, <n> team, <n> repo[, <n> custom][, <n> unknown])
//
// Zero buckets are omitted. Total=0 collapses to `Knowledge bubbles: 0`
// (no parens). Merger errors degrade gracefully to `(unavailable)` so the
// rest of `ox status` still renders.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/kb"
	"github.com/sageox/ox/internal/status"
)

// statusBubblesMerger is the seam between status rendering and the F3
// three-source merger. Mirrors kbListMerger so the same fakes work in tests.
type statusBubblesMerger interface {
	Merge(ctx context.Context) (kb.MergeResult, error)
}

// statusBubblesSummary is the small intermediate value that renderers and
// JSON emitters both consume. Keeps presentation logic out of the merger.
type statusBubblesSummary struct {
	// Total is the sum of all per-type counts.
	Total int

	// ByType is keyed by kb_type slug ("personal", "profile", ...). Only
	// non-zero buckets appear so order-of-iteration callers can skip the
	// is-zero check.
	ByType map[string]int

	// Warnings are non-fatal per-source errors from the merger.
	Warnings []kb.SourceWarning

	// Unavailable reports that the merger itself failed (not a per-source
	// warning). Renderers show "(unavailable)" and skip the breakdown.
	Unavailable bool
}

// statusBubblesTypeOrder is the canonical render order for the by-type
// breakdown. Matches kb_list.go's kbTypePriority so `ox status` and
// `ox kb list` agree on which bucket appears first.
var statusBubblesTypeOrder = []api.KBType{
	api.KBTypePersonal,
	api.KBTypeProfile,
	api.KBTypeTeam,
	api.KBTypeRepo,
	api.KBTypeCustom,
	api.KBType("channel"),
	api.KBTypeUnknown,
}

// summarizeBubbles tallies per-type counts from a MergeResult. Empty/unknown
// types collapse to "unknown" so a forward-compat row never silently
// vanishes from the count.
func summarizeBubbles(res kb.MergeResult) statusBubblesSummary {
	by := make(map[string]int)
	for _, b := range res.Bubbles {
		key := string(b.Type)
		if b.Type == "" || b.Type == api.KBTypeUnknown {
			key = string(api.KBTypeUnknown)
		}
		by[key]++
	}
	// drop zero entries (defensive — should never happen since we only
	// increment, but keeps the shape predictable for JSON consumers)
	for k, v := range by {
		if v == 0 {
			delete(by, k)
		}
	}
	return statusBubblesSummary{
		Total:    len(res.Bubbles),
		ByType:   by,
		Warnings: res.Warnings,
	}
}

// formatBubblesLine builds the human-readable summary string with no
// styling applied. Splitting style off makes this trivially testable:
// the assertion is the literal string the user sees.
func formatBubblesLine(s statusBubblesSummary) string {
	if s.Unavailable {
		return "Knowledge bubbles: (unavailable)"
	}
	if s.Total == 0 {
		return "Knowledge bubbles: 0"
	}
	parts := make([]string, 0, len(s.ByType))
	for _, t := range statusBubblesTypeOrder {
		k := string(t)
		if n, ok := s.ByType[k]; ok && n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, k))
		}
	}
	// stable order for any custom keys not in statusBubblesTypeOrder
	// (forward-compat: server adds a new type before the CLI knows about
	// it). Sort by key so output stays deterministic.
	if len(parts) < nonZeroBuckets(s.ByType) {
		extras := make([]string, 0)
		known := knownTypeSet()
		for k, n := range s.ByType {
			if _, ok := known[k]; ok {
				continue
			}
			if n > 0 {
				extras = append(extras, fmt.Sprintf("%d %s", n, k))
			}
		}
		sort.Strings(extras)
		parts = append(parts, extras...)
	}
	return fmt.Sprintf("Knowledge bubbles: %d (%s)", s.Total, strings.Join(parts, ", "))
}

// nonZeroBuckets counts entries with value > 0. Used to detect when
// statusBubblesTypeOrder didn't cover every key.
func nonZeroBuckets(m map[string]int) int {
	n := 0
	for _, v := range m {
		if v > 0 {
			n++
		}
	}
	return n
}

// knownTypeSet returns the set of type slugs the CLI knows about, used to
// detect forward-compat extras in formatBubblesLine.
func knownTypeSet() map[string]struct{} {
	out := make(map[string]struct{}, len(statusBubblesTypeOrder))
	for _, t := range statusBubblesTypeOrder {
		out[string(t)] = struct{}{}
	}
	return out
}

// renderBubblesLine produces the styled `ox status` block: one main line
// plus an optional warnings hint. Returns an empty string only if the
// caller passed a zero-value summary (defensive).
func renderBubblesLine(s statusBubblesSummary) string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(statusLabelStyle.Render("Knowledge bubbles"))
	main := formatBubblesLine(s)
	// strip the "Knowledge bubbles: " prefix — the label column already
	// shows the field name, so the value column shouldn't repeat it.
	value := strings.TrimPrefix(main, "Knowledge bubbles: ")
	if s.Unavailable {
		b.WriteString(statusMutedStyle.Render(value))
	} else {
		b.WriteString(statusValueStyle.Render(value))
	}
	if len(s.Warnings) > 0 && !s.Unavailable {
		b.WriteString(" ")
		b.WriteString(statusWarningStyle.Render("(warnings: see ox doctor)"))
	}
	b.WriteString("\n")
	return b.String()
}

// buildBubblesJSON converts a summary into the BubblesJSON payload. Uses
// status.BubblesJSON directly so the shape stays in sync with the type
// declared in internal/status/types.go.
func buildBubblesJSON(s statusBubblesSummary) *status.BubblesJSON {
	if s.Unavailable {
		// surface unavailability as zero counts + a synthetic warning
		// rather than omitting the field — JSON consumers should see
		// "the merger ran, it produced nothing" explicitly.
		return &status.BubblesJSON{
			Total: 0,
			Warnings: []status.BubbleWarningJSON{
				{Source: "merger", Error: "merger unavailable"},
			},
		}
	}
	out := &status.BubblesJSON{
		Total: s.Total,
	}
	if len(s.ByType) > 0 {
		out.ByType = make(map[string]int, len(s.ByType))
		for k, v := range s.ByType {
			out.ByType[k] = v
		}
	}
	if len(s.Warnings) > 0 {
		out.Warnings = make([]status.BubbleWarningJSON, 0, len(s.Warnings))
		for _, w := range s.Warnings {
			out.Warnings = append(out.Warnings, status.BubbleWarningJSON{
				Source: string(w.Source),
				Error:  w.Err,
			})
		}
	}
	return out
}

// collectBubblesSummary runs the merger with a short timeout and returns
// a summary. Failure of the merger never propagates upward — `ox status`
// must keep rendering the rest of the report.
func collectBubblesSummary(merger statusBubblesMerger) statusBubblesSummary {
	if merger == nil {
		return statusBubblesSummary{Unavailable: true}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := merger.Merge(ctx)
	if err != nil {
		return statusBubblesSummary{Unavailable: true}
	}
	return summarizeBubbles(res)
}

// newDefaultStatusBubblesMerger wires up the production three-source merger
// for `ox status`. Mirrors newDefaultKBListMerger in kb_list.go — both
// commands fan out to the same three sources, so the wiring stays
// identical.
func newDefaultStatusBubblesMerger(projectRoot string) statusBubblesMerger {
	ep := endpoint.Get()
	if projectRoot != "" {
		ep = endpoint.GetForProject(projectRoot)
	}

	kbClient := api.NewKBClientWithEndpoint(ep)
	if token, err := auth.GetTokenForEndpoint(ep); err == nil && token != nil && token.AccessToken != "" {
		kbClient = kbClient.WithAuthToken(token.AccessToken)
	}

	teams := &kbListTeamSource{projectRoot: projectRoot, endpoint: ep}
	ledger := &kbListLedgerSource{projectRoot: projectRoot}

	return kb.NewMerger(kbClient, teams, ledger)
}

// statusBubblesMergerForRoot is a small indirection used by status.go so
// tests can swap the merger without touching auth/endpoint plumbing.
// Production calls newDefaultStatusBubblesMerger; tests assign a fake.
var statusBubblesMergerForRoot = func(projectRoot string) statusBubblesMerger {
	return newDefaultStatusBubblesMerger(projectRoot)
}
