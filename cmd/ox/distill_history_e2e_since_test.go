//go:build slow

// distill_history_e2e_since_test.go — Batch E JR-* cases for `ox distill
// history since`. The 3 cases that exercise the since composite (listEntries +
// loadEntries + content / json rendering). Harness, fixtures, and
// recipes live in distill_history_e2e_test.go; this file only adds the
// per-case test funcs.
//
// All 3 cases assert real envelope / stdout behavior against Unit 5
// (Since + ox distill history since, commit d55ce32). Per plan §3.6 and
// distill_history_since.go:
//
//   - --format defaults to content; json and text are also supported.
//   - content marker is the RICH form:
//         <!-- entry: <id> | layer: <layer> | date: <date> -->
//     Differs from show's minimal `<!-- entry: <id> -->` — this
//     distinction is spec §3.6 lines 684-687 and is the primary signal
//     factory/distill/summary.ts uses to route per-entry summarization.
//   - The window is the SAME day-rounded half-open shape as list:
//     effSince = dayFloor(now - dur), effUntil = dayCeil(now). A 24h
//     relative window is therefore at least 48h wide on the wire.
//   - data.bodies is a *[]string pointer with omitempty — since always
//     emits `"bodies": []` even on an empty window, while list and show
//     drop the field entirely. The assertion here only fires for the
//     populated case; empty-window semantics are covered by the unit
//     tests in internal/distill/history/read/since_test.go.
//
// See docs/specs/distill-history-read-test-plan.md §2 rows JR-09, JR-10,
// JR-16.

package main

import (
	"strings"
	"testing"
	"time"
)

// TestDistillHistoryRead_JR09_SinceContentFormat_ChronoOrder — since-24h
// content format emits in-window bodies in chronological order with
// per-entry rich markers. Failure prevented: ordering reversed, window
// off-by-one day, marker is the minimal show form instead of the rich
// since form, or envelope JSON accidentally leaks into the content
// stream that pipes directly to `claude`.
func TestDistillHistoryRead_JR09_SinceContentFormat_ChronoOrder(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	skipIfNearUTCMidnight(t, now)
	e := setupDistillHistoryE2E(t, now)
	fx := recipeThreeDays(t, e.primaryTeam, now)

	out, exit := e.Run(t, "distill", "history", "since", "24h", "--format=content")
	if exit != 0 {
		t.Fatalf("JR-09: exit=%d want 0\nout:\n%s", exit, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatalf("JR-09: stdout empty — content format must emit bodies")
	}
	// Content mode pipes straight to `claude`. Any envelope-shape token
	// leaking into stdout would poison that pipe. Same defense as JR-08.
	if strings.Contains(out, `"success"`) || strings.Contains(out, `"distill_history_since"`) || strings.Contains(out, `"entries"`) {
		t.Fatalf("JR-09: envelope fields leaked into content stdout:\n%s", out)
	}

	// recipeThreeDays specs (see fixtures_test.go):
	//   Dailies[0] = oldest (now-48h, date = now-2d, prefix "Oldest daily...")
	//   Dailies[1] = middle (now-24h, date = now-1d, prefix "Middle daily...")
	//   Dailies[2] = newest (now-6h,  date = now,    prefix "Newest daily...")
	// The 24h query rounds to a window of [dayFloor(now-24h), dayCeil(now)] —
	// that's ~48h wide on the wire and catches middle + newest but excludes
	// oldest whose date is 2 days ago (< dayFloor(now-24h)).
	oldest := fx.Dailies[0]
	middle := fx.Dailies[1]
	newest := fx.Dailies[2]

	if strings.Contains(out, "Oldest daily") {
		t.Fatalf("JR-09: oldest body leaked into 24h window — expected exclusion by date prefix\nout:\n%s", out)
	}
	if !strings.Contains(out, "Middle daily, in-window for a 24h query.") {
		t.Fatalf("JR-09: middle body prefix missing from content stream:\n%s", out)
	}
	if !strings.Contains(out, "Newest daily, always in-window.") {
		t.Fatalf("JR-09: newest body prefix missing from content stream:\n%s", out)
	}

	// Rich markers: each in-window entry must be preceded by
	//   <!-- entry: <id> | layer: daily | date: <date> -->
	// (distill_history_since.go:231-232). This is the contract distinguishing since
	// content from show content — agents use the layer/date segments to
	// route summarization.
	middleMarker := "<!-- entry: " + middle.ID + " | layer: daily | date: " + middle.Date + " -->"
	newestMarker := "<!-- entry: " + newest.ID + " | layer: daily | date: " + newest.Date + " -->"
	if !strings.Contains(out, middleMarker) {
		t.Fatalf("JR-09: middle rich marker missing: want %q\nout:\n%s", middleMarker, out)
	}
	if !strings.Contains(out, newestMarker) {
		t.Fatalf("JR-09: newest rich marker missing: want %q\nout:\n%s", newestMarker, out)
	}
	// A regression to show's minimal marker form would be invisible to the
	// substring checks above (rich is a superset). Explicitly verify the
	// minimal form's unique shape is absent by looking for "<!-- entry: ID -->"
	// as a standalone line for each in-window entry.
	minimalMiddle := "<!-- entry: " + middle.ID + " -->"
	minimalNewest := "<!-- entry: " + newest.ID + " -->"
	for _, ln := range strings.Split(out, "\n") {
		if ln == minimalMiddle || ln == minimalNewest {
			t.Fatalf("JR-09: minimal show-style marker %q leaked into since content — since must use the rich form", ln)
		}
	}

	// Chrono order: middle (date = now-1d) must precede newest (date = now).
	// The marker positions are the stable landmark — the body text between
	// them is recipe-dependent and noisy.
	iMiddle := strings.Index(out, middleMarker)
	iNewest := strings.Index(out, newestMarker)
	if iMiddle < 0 || iNewest < 0 {
		t.Fatalf("JR-09: marker index lookup failed middle=%d newest=%d", iMiddle, iNewest)
	}
	if iMiddle >= iNewest {
		t.Fatalf("JR-09: wrong order — middle marker at %d must precede newest at %d:\n%s",
			iMiddle, iNewest, out)
	}

	// Separator between entries must be the "\n---\n" fence the content
	// renderer emits (distill_history_since.go:237). Exactly one separator for two
	// entries — more would imply an extra stray fence.
	sepCount := strings.Count(out, "\n---\n")
	if sepCount != 1 {
		t.Fatalf("JR-09: separator count=%d want 1 (two entries joined by one \\n---\\n)\nout:\n%s",
			sepCount, out)
	}

	// Defense-in-depth: the oldest entry's ID must not appear anywhere on
	// stdout, not even in a stray marker.
	if strings.Contains(out, oldest.ID) {
		t.Fatalf("JR-09: oldest ID %q appears in content stream — must be excluded by window:\n%s",
			oldest.ID, out)
	}
}

// TestDistillHistoryRead_JR10_SinceJsonFormat_ParallelBodies — since-24h JSON
// format returns entries + parallel bodies array with a day-rounded
// window. Failure prevented: bodies omitted from JSON, raw (now-24h)
// reported instead of dayFloor(now-24h), until reported as raw `now`
// instead of dayCeil(now), or bodies array out of parallel alignment
// with entries.
func TestDistillHistoryRead_JR10_SinceJsonFormat_ParallelBodies(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	skipIfNearUTCMidnight(t, now)
	e := setupDistillHistoryE2E(t, now)
	fx := recipeThreeDays(t, e.primaryTeam, now)

	out, exit := e.Run(t, "distill", "history", "since", "24h", "--format=json")
	if exit != 0 {
		t.Fatalf("JR-10: exit=%d want 0\nout:\n%s", exit, out)
	}
	env, envLine, _ := decodeJournalEnvelope(t, out)
	if !env.Success {
		t.Fatalf("JR-10: success=false want true; err=%+v", env.Error)
	}
	if env.Type != "distill_history_since" {
		t.Fatalf("JR-10: type=%q want %q", env.Type, "distill_history_since")
	}
	if env.Error != nil {
		t.Fatalf("JR-10: unexpected error envelope: %+v", env.Error)
	}
	if env.ElapsedMS < 0 {
		t.Fatalf("JR-10: elapsed_ms=%d want >= 0", env.ElapsedMS)
	}
	if env.Data == nil {
		t.Fatalf("JR-10: data is nil; out=%s", out)
	}
	if got := len(env.Data.Entries); got != 2 {
		t.Fatalf("JR-10: entries len=%d want 2 (middle + newest; oldest excluded by window)\nentries=%+v",
			got, env.Data.Entries)
	}

	// Since ALWAYS emits data.bodies as a non-nil *[]string so the JSON
	// encoder produces `"bodies": [...]` on the wire even for empty windows
	// (distill_history_envelope.go:60-65 rationalizes the pointer indirection).
	// Explicitly verify the field appears on the wire — a struct that
	// dropped to omitempty would pass the Go-level len() check below via
	// a nil decode, but the line-level check would trip.
	if !strings.Contains(envLine, `"bodies":`) {
		t.Fatalf("JR-10: envelope line missing `\"bodies\":` field — since must emit it even when empty\nenvelope line:\n%s", envLine)
	}
	if env.Data.Bodies == nil {
		t.Fatalf("JR-10: data.bodies is nil after decode — since must populate it as a non-nil slice")
	}
	bodies := *env.Data.Bodies
	if len(bodies) != len(env.Data.Entries) {
		t.Fatalf("JR-10: bodies len=%d entries len=%d — arrays must be parallel (distill_history_since.go contract)",
			len(bodies), len(env.Data.Entries))
	}

	middle := fx.Dailies[1]
	newest := fx.Dailies[2]

	got0 := env.Data.Entries[0]
	got1 := env.Data.Entries[1]
	if got0.ID != middle.ID || got1.ID != newest.ID {
		t.Fatalf("JR-10: entry ordering wrong: got [%s, %s] want [%s (middle), %s (newest)]",
			got0.ID, got1.ID, middle.ID, newest.ID)
	}
	if got0.Layer != "daily" || got1.Layer != "daily" {
		t.Fatalf("JR-10: entries must be layer=daily, got [%s, %s]", got0.Layer, got1.Layer)
	}
	if got0.Date != middle.Date || got1.Date != newest.Date {
		t.Fatalf("JR-10: entry dates wrong: got [%s, %s] want [%s, %s]",
			got0.Date, got1.Date, middle.Date, newest.Date)
	}

	// Bodies must align positionally with entries and must not carry the
	// frontmatter fence (parseEntryFile calls StripFrontmatter before
	// handing the body to the caller).
	if !strings.Contains(bodies[0], "Middle daily, in-window for a 24h query.") {
		t.Fatalf("JR-10: bodies[0] missing middle prefix: %q", bodies[0])
	}
	if !strings.Contains(bodies[1], "Newest daily, always in-window.") {
		t.Fatalf("JR-10: bodies[1] missing newest prefix: %q", bodies[1])
	}
	for i, b := range bodies {
		if strings.HasPrefix(b, "---") {
			head := b
			if len(head) > 40 {
				head = head[:40]
			}
			t.Fatalf("JR-10: bodies[%d] still has frontmatter fence: %q", i, head)
		}
		if !strings.Contains(b, "# Daily Memory") {
			t.Fatalf("JR-10: bodies[%d] missing heading — body is empty or truncated: %q", i, b)
		}
	}

	// Window: day-rounded half-open. since = dayFloor(now-24h), until =
	// dayCeil(now). Both RFC3339 Z-suffixed, strictly at 00:00:00Z — the
	// HasSuffix lock-in catches any raw-format regression that would report
	// sub-day granularity.
	if env.Data.Window == nil {
		t.Fatalf("JR-10: window is nil — since must populate window per spec §4.3")
	}
	wantSince := dayFloorUTC(now.Add(-24 * time.Hour)).Format(time.RFC3339)
	wantUntil := dayCeilUTC(now).Format(time.RFC3339)
	if env.Data.Window.Since != wantSince {
		t.Fatalf("JR-10: window.since=%q want %q", env.Data.Window.Since, wantSince)
	}
	if env.Data.Window.Until != wantUntil {
		t.Fatalf("JR-10: window.until=%q want %q", env.Data.Window.Until, wantUntil)
	}
	if !strings.HasSuffix(env.Data.Window.Since, "T00:00:00Z") {
		t.Fatalf("JR-10: window.since=%q does not end in T00:00:00Z — day rounding regressed",
			env.Data.Window.Since)
	}
	if !strings.HasSuffix(env.Data.Window.Until, "T00:00:00Z") {
		t.Fatalf("JR-10: window.until=%q does not end in T00:00:00Z — day rounding regressed",
			env.Data.Window.Until)
	}
	if env.Data.Window.LayerResolved != "daily" {
		t.Fatalf("JR-10: window.layer_resolved=%q want 'daily'", env.Data.Window.LayerResolved)
	}
	if env.Data.Truncated {
		t.Fatalf("JR-10: truncated=true unexpected for a 2-entry window under the 100-row default cap")
	}

	// Per plan §4.3 item 2: tolerate ±few-seconds drift between the test's
	// captured `now` and the subprocess's `time.Now()` because the window
	// rounding happens in the subprocess. dayFloor/dayCeil collapse those
	// drifts unless they straddle midnight — and skipIfNearUTCMidnight
	// already guards against that case, so any mismatch above is a real
	// bug, not a flake.
}

// TestDistillHistoryRead_JR16_SinceAllTeamsContent_ConcatenatedMarkers —
// since + --all-teams + content merges bodies across teams ordered by
// (date, created_at) with rich per-entry markers. Failure prevented:
// merge orders by team slug instead of time, per-entry marker is
// missing, or the marker is stuffed with a team field that would
// change the contract factory/distill/summary.ts parses.
func TestDistillHistoryRead_JR16_SinceAllTeamsContent_ConcatenatedMarkers(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	skipIfNearUTCMidnight(t, now)
	e := setupDistillHistoryE2E(t, now)
	// Discriminator for the slug-order regression: choose a secondary slug
	// that alphabetically sorts BEFORE the primary's "journal-read-e2e"
	// (harness_test.go:195). With the default "journal-read-e2e-t2" slug
	// plus the recipe's (primary -3h, secondary -2h) mtimes, BOTH
	// slug-order and time-order put primary first — so the assertion
	// below would pass regressed code that switched to slug ordering.
	// "alpha-journal-read-e2e" flips slug-order to secondary-first while
	// leaving time-order as primary-first, so the primary-first check
	// below now discriminates the two. JR-14/15 keep their default slug;
	// this change is scoped to JR-16 to avoid rippling their assertions.
	secondary := addSecondaryTeam(t, e, "team_alpha_read_e2e", "Alpha Read E2E", "alpha-journal-read-e2e")
	fx := recipeMultiTeamList(t, e.primaryTeam, secondary, now)

	out, exit := e.Run(t, "distill", "history", "since", "24h", "--all-teams", "--format=content")
	if exit != 0 {
		t.Fatalf("JR-16: exit=%d want 0\nout:\n%s", exit, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatalf("JR-16: stdout empty — content format must emit bodies")
	}
	if strings.Contains(out, `"success"`) || strings.Contains(out, `"distill_history_since"`) || strings.Contains(out, `"entries"`) {
		t.Fatalf("JR-16: envelope fields leaked into content stdout:\n%s", out)
	}

	// recipeMultiTeamList stages:
	//   Dailies[0] = primary team, mtime now-3h, body "Primary team daily."
	//   Dailies[1] = secondary team, mtime now-2h, body "Secondary team daily."
	// Both are today-dated. listEntries merges teams with sortEntries
	// (date asc, created_at asc) per list.go:1-108 — so with identical
	// dates the older mtime (primary, -3h) must come first.
	//
	// Slug-order discriminator: the secondary's slug "alpha-journal-read-e2e"
	// sorts BEFORE the primary's "journal-read-e2e". Under time-order the
	// primary's older mtime wins (primary first); under slug-order the
	// secondary's lexically earlier slug wins (secondary first). So the
	// primary-first assertion below catches a regression that switched the
	// merge key from (date, created_at) to (team_slug, ...).
	primary := fx.Dailies[0]
	secondaryDaily := fx.Dailies[1]

	if !strings.Contains(out, "Primary team daily.") {
		t.Fatalf("JR-16: primary body missing from --all-teams content stream:\n%s", out)
	}
	if !strings.Contains(out, "Secondary team daily.") {
		t.Fatalf("JR-16: secondary body missing from --all-teams content stream:\n%s", out)
	}

	primaryMarker := "<!-- entry: " + primary.ID + " | layer: daily | date: " + primary.Date + " -->"
	secondaryMarker := "<!-- entry: " + secondaryDaily.ID + " | layer: daily | date: " + secondaryDaily.Date + " -->"
	if !strings.Contains(out, primaryMarker) {
		t.Fatalf("JR-16: primary rich marker missing: want %q\nout:\n%s", primaryMarker, out)
	}
	if !strings.Contains(out, secondaryMarker) {
		t.Fatalf("JR-16: secondary rich marker missing: want %q\nout:\n%s", secondaryMarker, out)
	}

	// The spec commits to id/layer/date in the since marker (distill_history_since.go:
	// 231-232 and spec §3.6). It does NOT include team. If a future change
	// adds a "| team: <slug>" segment it would silently break every caller
	// that parses the current contract, so pin the shape here: for each
	// in-window entry, the marker line must not contain "team".
	for _, ln := range strings.Split(out, "\n") {
		if !strings.HasPrefix(ln, "<!-- entry:") {
			continue
		}
		if strings.Contains(ln, "team") {
			t.Fatalf("JR-16: marker line contains 'team' segment — spec §3.6 commits only to id/layer/date:\n%s", ln)
		}
	}

	iPrimary := strings.Index(out, primaryMarker)
	iSecondary := strings.Index(out, secondaryMarker)
	if iPrimary < 0 || iSecondary < 0 {
		t.Fatalf("JR-16: marker index lookup failed primary=%d secondary=%d", iPrimary, iSecondary)
	}
	if iPrimary >= iSecondary {
		t.Fatalf("JR-16: wrong order — primary marker at %d must precede secondary at %d; merge must sort by (date, created_at), not by team slug:\n%s",
			iPrimary, iSecondary, out)
	}

	// Exactly one separator for two entries, same as JR-09.
	sepCount := strings.Count(out, "\n---\n")
	if sepCount != 1 {
		t.Fatalf("JR-16: separator count=%d want 1 (two entries joined by one \\n---\\n)\nout:\n%s",
			sepCount, out)
	}
}
