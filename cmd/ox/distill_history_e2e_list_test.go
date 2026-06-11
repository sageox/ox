//go:build slow

// distill_history_e2e_list_test.go — JR-* skeletons for `ox distill history list`.
// All 14 cases that exercise the list command. Harness, fixtures, and
// recipes live in distill_history_e2e_test.go; this file only adds the
// per-case test funcs.
//
// Each skeleton stages its fixture and invokes the real ox subprocess
// BEFORE calling t.Skip. The Skip guard is narrow by design: it covers
// only the assertion block. That way a regression in the harness
// (binary build, XDG reroute, recipe staging) still fails this file
// even while the assertion block is skip-gated. Once the implementer
// lands Unit 3 (ox distill history list + distill_history_time.go), replace the Skip
// with real assertions on the JSON envelope.
//
// See docs/specs/distill-history-read-test-plan.md §2 rows JR-01..JR-03,
// JR-11..JR-15, JR-19..JR-24.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// decodeJournalEnvelope parses the first JSON object line out of a
// combined stdout+stderr blob and returns three slices of that blob:
//
//   - env         — the decoded envelope struct (never nil; test-fatal on miss)
//   - envelopeLine — the raw matched JSON line exactly as it appeared on
//     the wire (post-TrimSpace), so tests can make byte-level assertions
//     (e.g. literal `"entries":[]`) without accidentally matching warning
//     lines that follow.
//   - stderrTail  — every OTHER line from the blob (both before and after
//     the envelope), joined by "\n". This is where `emitDistillHistoryWarnings`
//     writes its `warning: <code>: <msg>` lines, plus any diagnostic
//     noise tests want to inspect.
//
// testguard.RunOx uses cmd.CombinedOutput, which merges stdout+stderr
// into one byte stream with no ordering guarantee between the two fds.
// Tests cannot assume the envelope comes first: stderr is typically
// unbuffered and can beat the stdout envelope into the pipe. The scan
// finds the envelope line wherever it lands and concatenates everything
// else into stderrTail so warning assertions work regardless of order.
func decodeJournalEnvelope(t *testing.T, out string) (*distillHistoryEnvelope, string, string) {
	t.Helper()
	lines := strings.Split(out, "\n")
	envIdx := -1
	var env distillHistoryEnvelope
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
			continue
		}
		if err := json.Unmarshal([]byte(trimmed), &env); err == nil {
			envIdx = i
			break
		}
	}
	if envIdx < 0 {
		t.Fatalf("no JSON envelope found in output:\n%s", out)
		return nil, "", ""
	}
	envelopeLine := strings.TrimSpace(lines[envIdx])
	tail := make([]string, 0, len(lines)-1)
	for i, line := range lines {
		if i == envIdx {
			continue
		}
		tail = append(tail, line)
	}
	return &env, envelopeLine, strings.Join(tail, "\n")
}

// dayFloorUTC returns the UTC midnight at or before t. It mirrors
// list.go:dayFloor (unexported) so JR-* tests can compute the expected
// day-rounded window bound from the spec without reaching into the
// reader package. The spec is the source of truth — if list.go's rounding
// ever diverges from this helper, that is itself a bug.
func dayFloorUTC(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// dayCeilUTC returns the exclusive UTC-midnight upper bound for a
// half-open day window containing t.
func dayCeilUTC(t time.Time) time.Time {
	return dayFloorUTC(t).Add(24 * time.Hour)
}

// skipIfNearUTCMidnight lives in distill_history_test_helpers_test.go (untagged)
// so both this slow-tagged file and the fast in-proc distill history tests share
// one source of truth.

// TestDistillHistoryRead_JR01_MinimalDaily_List — one in-window daily.
// Failure prevented: reader cannot locate memory/daily/ under the
// staged team context at all.
func TestDistillHistoryRead_JR01_MinimalDaily_List(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	skipIfNearUTCMidnight(t, now)
	e := setupDistillHistoryE2E(t, now)
	fx := recipeMinimalDaily(t, e.primaryTeam, now)

	out, exit := e.Run(t, "distill", "history", "list", "--since=24h", "--format=json")
	if exit != 0 {
		t.Fatalf("JR-01: exit=%d want 0\nout:\n%s", exit, out)
	}
	env, _, _ := decodeJournalEnvelope(t, out)
	if !env.Success {
		t.Fatalf("JR-01: success=false want true; out=%s", out)
	}
	if env.Type != "distill_history_list" {
		t.Fatalf("JR-01: type=%q want %q", env.Type, "distill_history_list")
	}
	if env.Error != nil {
		t.Fatalf("JR-01: unexpected error envelope: %+v", env.Error)
	}
	if env.Data == nil {
		t.Fatalf("JR-01: data is nil; out=%s", out)
	}
	if got := len(env.Data.Entries); got != 1 {
		t.Fatalf("JR-01: entries len=%d want 1; entries=%+v", got, env.Data.Entries)
	}

	want := fx.Dailies[0]
	got := env.Data.Entries[0]
	if got.ID != want.ID {
		t.Fatalf("JR-01: entry.id=%q want %q", got.ID, want.ID)
	}
	if got.Layer != "daily" {
		t.Fatalf("JR-01: entry.layer=%q want %q", got.Layer, "daily")
	}
	if got.Date != want.Date {
		t.Fatalf("JR-01: entry.date=%q want %q", got.Date, want.Date)
	}
	if got.Team != e.primaryTeam.slug {
		t.Fatalf("JR-01: entry.team=%q want %q (slug, not id)", got.Team, e.primaryTeam.slug)
	}
	if got.Path != want.Rel {
		t.Fatalf("JR-01: entry.path=%q want %q", got.Path, want.Rel)
	}
	if got.FactCount != 0 {
		t.Fatalf("JR-01: entry.fact_count=%d want 0 (reader does not populate fact_count yet)", got.FactCount)
	}
	if got.CitationCount != 1 {
		t.Fatalf("JR-01: entry.citation_count=%d want 1 (body has one numbered ref)", got.CitationCount)
	}
	if len(got.SourceFiles) != 1 || got.SourceFiles[0] != want.Sources[0] {
		t.Fatalf("JR-01: entry.source_files=%v want %v", got.SourceFiles, want.Sources)
	}
	createdAt, err := time.Parse(time.RFC3339, got.CreatedAt)
	if err != nil {
		t.Fatalf("JR-01: parse created_at=%q: %v", got.CreatedAt, err)
	}
	if delta := createdAt.Sub(want.Mtime.UTC()); delta < -2*time.Second || delta > 2*time.Second {
		t.Fatalf("JR-01: created_at=%s drifts from mtime=%s by %s (>2s)",
			createdAt.Format(time.RFC3339), want.Mtime.UTC().Format(time.RFC3339), delta)
	}

	if env.Data.Window == nil {
		t.Fatalf("JR-01: window is nil")
	}
	wantSince := dayFloorUTC(now.Add(-24 * time.Hour)).Format(time.RFC3339)
	wantUntil := dayCeilUTC(now).Format(time.RFC3339)
	if env.Data.Window.Since != wantSince {
		t.Fatalf("JR-01: window.since=%q want %q", env.Data.Window.Since, wantSince)
	}
	if env.Data.Window.Until != wantUntil {
		t.Fatalf("JR-01: window.until=%q want %q", env.Data.Window.Until, wantUntil)
	}
	if env.Data.Window.LayerResolved != "daily" {
		t.Fatalf("JR-01: window.layer_resolved=%q want %q", env.Data.Window.LayerResolved, "daily")
	}
	if env.Data.Truncated {
		t.Fatalf("JR-01: truncated=true unexpected for a single-row window")
	}
}

// TestDistillHistoryRead_JR02_EmptyWindow_YesterdayOnly — 1h window over a
// team that only has yesterday's daily. Failure prevented: day-rounding
// rule wrong, or reader errors on empty windows.
func TestDistillHistoryRead_JR02_EmptyWindow_YesterdayOnly(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	skipIfNearUTCMidnight(t, now)
	// Within the first UTC hour, --since=1h rounds back to yesterday and
	// the staged yesterday file would (correctly) be in window — the
	// recipe's intent is to exercise an empty window, so we skip.
	if now.Hour() == 0 {
		t.Skip("first UTC hour: --since=1h crosses back to yesterday; the yesterday-only fixture would be in window")
	}
	e := setupDistillHistoryE2E(t, now)
	_ = recipeYesterdayOnly(t, e.primaryTeam, now)

	out, exit := e.Run(t, "distill", "history", "list", "--since=1h", "--format=json")
	if exit != 0 {
		t.Fatalf("JR-02: exit=%d want 0\nout:\n%s", exit, out)
	}
	env, envLine, _ := decodeJournalEnvelope(t, out)
	if !env.Success {
		t.Fatalf("JR-02: success=false want true")
	}
	if env.Error != nil {
		t.Fatalf("JR-02: unexpected error envelope: %+v", env.Error)
	}
	if env.Data == nil {
		t.Fatalf("JR-02: data is nil")
	}
	if got := len(env.Data.Entries); got != 0 {
		t.Fatalf("JR-02: entries len=%d want 0 (yesterday file must be out of 1h window); entries=%+v",
			got, env.Data.Entries)
	}
	// The reader must serialize an empty `entries` array, not null. Spec
	// §4.3 says the field is always present; pairing that with our
	// nonNilStringSlice / nonNilSlice discipline means the envelope line
	// itself (not the whole stdout blob) contains the literal
	// `"entries":[]`. Scanning envLine instead of out prevents any stray
	// warning-line substring match from vacuously passing the check.
	if !strings.Contains(envLine, `"entries":[]`) {
		t.Fatalf("JR-02: expected entries to serialize as [] on envelope line, got line=%s", envLine)
	}
	if env.Data.Truncated {
		t.Fatalf("JR-02: truncated=true unexpected")
	}
	if env.Data.Window == nil {
		t.Fatalf("JR-02: window is nil")
	}
	wantSince := dayFloorUTC(now.Add(-1 * time.Hour)).Format(time.RFC3339)
	wantUntil := dayCeilUTC(now).Format(time.RFC3339)
	if env.Data.Window.Since != wantSince {
		t.Fatalf("JR-02: window.since=%q want %q", env.Data.Window.Since, wantSince)
	}
	if env.Data.Window.Until != wantUntil {
		t.Fatalf("JR-02: window.until=%q want %q", env.Data.Window.Until, wantUntil)
	}
}

// TestDistillHistoryRead_JR03_MultiSnapshotSameDay_ListOrdering — two
// snapshots same day, older mtime first. Failure prevented: reader
// dedupes by date and hides one, or ordering is wrong (spec §5.c).
func TestDistillHistoryRead_JR03_MultiSnapshotSameDay_ListOrdering(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	skipIfNearUTCMidnight(t, now)
	e := setupDistillHistoryE2E(t, now)
	fx := recipeMultiSnapshotSameDay(t, e.primaryTeam, now)

	out, exit := e.Run(t, "distill", "history", "list", "--since=24h", "--format=json")
	if exit != 0 {
		t.Fatalf("JR-03: exit=%d want 0\nout:\n%s", exit, out)
	}
	env, _, _ := decodeJournalEnvelope(t, out)
	if env.Data == nil || len(env.Data.Entries) != 2 {
		t.Fatalf("JR-03: want 2 entries (spec §5.c multi-snapshot per day), got data=%+v", env.Data)
	}

	// Recipe stages: fx.Dailies[0]=older (mtime -4h), fx.Dailies[1]=newer (mtime -2h).
	// Reader sort order is (date asc, created_at asc) per list.go:sortEntries,
	// so the older snapshot must precede the newer one.
	want0 := fx.Dailies[0]
	want1 := fx.Dailies[1]
	got0 := env.Data.Entries[0]
	got1 := env.Data.Entries[1]
	if got0.ID != want0.ID || got1.ID != want1.ID {
		t.Fatalf("JR-03: ordering wrong: got [%s, %s] want [%s, %s] (older mtime first)",
			got0.ID, got1.ID, want0.ID, want1.ID)
	}
	if got0.Date != want0.Date || got1.Date != want1.Date {
		t.Fatalf("JR-03: dates wrong: [%s, %s] want [%s, %s] (both = today)",
			got0.Date, got1.Date, want0.Date, want1.Date)
	}
	ca0, err := time.Parse(time.RFC3339, got0.CreatedAt)
	if err != nil {
		t.Fatalf("JR-03: parse created_at[0]=%q: %v", got0.CreatedAt, err)
	}
	ca1, err := time.Parse(time.RFC3339, got1.CreatedAt)
	if err != nil {
		t.Fatalf("JR-03: parse created_at[1]=%q: %v", got1.CreatedAt, err)
	}
	if !ca0.Before(ca1) {
		t.Fatalf("JR-03: expected created_at ascending, got [0]=%s [1]=%s",
			ca0.Format(time.RFC3339), ca1.Format(time.RFC3339))
	}
}

// TestDistillHistoryRead_JR11_MalformedFilename_ListWarnsNotCrash — a stray
// non-date file alongside one valid daily. Failure prevented: reader
// crashes/fails the whole call on a single malformed filename.
func TestDistillHistoryRead_JR11_MalformedFilename_ListWarnsNotCrash(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	skipIfNearUTCMidnight(t, now)
	e := setupDistillHistoryE2E(t, now)
	fx := recipeMalformedFilename(t, e.primaryTeam, now)

	out, exit := e.Run(t, "distill", "history", "list", "--since=24h", "--format=json")
	if exit != 0 {
		t.Fatalf("JR-11: exit=%d want 0 (one bad neighbor must not fail the call)\nout:\n%s", exit, out)
	}
	env, _, stderrTail := decodeJournalEnvelope(t, out)
	if !env.Success {
		t.Fatalf("JR-11: success=false want true; err=%+v", env.Error)
	}
	if env.Error != nil {
		t.Fatalf("JR-11: unexpected error envelope: %+v", env.Error)
	}
	if env.Data == nil || len(env.Data.Entries) != 1 {
		t.Fatalf("JR-11: want exactly 1 entry (the valid daily), got data=%+v", env.Data)
	}
	want := fx.Dailies[0]
	if got := env.Data.Entries[0]; got.ID != want.ID {
		t.Fatalf("JR-11: entry.id=%q want %q (the bad neighbor must not be surfaced)", got.ID, want.ID)
	}
	// Assert against the reader's ACTUAL emitter format: distill_history_list.go
	// emitDistillHistoryWarnings writes `"warning: <code>: <message>\n"` — it
	// deliberately drops Warning.Path. So we match the prefix only; the
	// spec's aspirational form (which named the offending file) is not
	// what the command layer emits today. If the emitter grows a Path
	// column, tighten this assertion.
	if !strings.Contains(stderrTail, "warning: malformed_filename:") {
		t.Fatalf("JR-11: expected stderr tail to contain 'warning: malformed_filename:' — got:\n%s", stderrTail)
	}
}

// TestDistillHistoryRead_JR12_EmptyMarkerOnly_ListIgnoresFactDirs — no
// dailies; one fact file. Failure prevented: daily reader walks into
// memory/.github-facts/ and surfaces fact files as daily entries.
func TestDistillHistoryRead_JR12_EmptyMarkerOnly_ListIgnoresFactDirs(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	skipIfNearUTCMidnight(t, now)
	e := setupDistillHistoryE2E(t, now)
	_ = recipeEmptyMarkerOnly(t, e.primaryTeam, now)

	out, exit := e.Run(t, "distill", "history", "list", "--since=24h", "--format=json")
	if exit != 0 {
		t.Fatalf("JR-12: exit=%d want 0\nout:\n%s", exit, out)
	}
	env, _, stderrTail := decodeJournalEnvelope(t, out)
	if !env.Success {
		t.Fatalf("JR-12: success=false want true; err=%+v", env.Error)
	}
	if env.Error != nil {
		t.Fatalf("JR-12: unexpected error envelope: %+v", env.Error)
	}
	if env.Data == nil {
		t.Fatalf("JR-12: data is nil")
	}
	if got := len(env.Data.Entries); got != 0 {
		t.Fatalf("JR-12: entries len=%d want 0 (facts under .github-facts must not surface as dailies); entries=%+v",
			got, env.Data.Entries)
	}
	// The daily reader must not walk sibling subtrees — there is no fact
	// file to warn about at all, so stderr must be silent. Any "warning:"
	// line here means the reader is either descending into .github-facts
	// or failing to filter by directory.
	if strings.Contains(stderrTail, "warning:") {
		t.Fatalf("JR-12: unexpected stderr warning — reader may be walking fact subtrees; stderr:\n%s", stderrTail)
	}
}

// TestDistillHistoryRead_JR13_MixedTzAndUtcLegacy_TrustsPrefix — legacy +
// modern daily files. Failure prevented: reader re-derives date from
// UUID7 instead of trusting the filename prefix (spec §5.b option a);
// stderr carries a spurious legacy warning despite the silent policy.
func TestDistillHistoryRead_JR13_MixedTzAndUtcLegacy_TrustsPrefix(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	skipIfNearUTCMidnight(t, now)
	e := setupDistillHistoryE2E(t, now)
	fx := recipeMixedTzAndUtcLegacy(t, e.primaryTeam, now)

	out, exit := e.Run(t, "distill", "history", "list", "--since=48h", "--format=json")
	if exit != 0 {
		t.Fatalf("JR-13: exit=%d want 0\nout:\n%s", exit, out)
	}
	env, _, stderrTail := decodeJournalEnvelope(t, out)
	if env.Data == nil || len(env.Data.Entries) != 2 {
		t.Fatalf("JR-13: want 2 entries (legacy + modern), got data=%+v", env.Data)
	}
	// Recipe: fx.Dailies[0]=legacy(yesterday prefix), fx.Dailies[1]=modern(today prefix).
	// sortEntries orders by (date asc, created_at asc), so legacy (yesterday)
	// must come before modern (today) regardless of the narrative UUID7
	// encoding. This is the lock-in that proves the reader trusts the
	// filename prefix per spec §5.b item a.
	wantLegacy := fx.Dailies[0]
	wantModern := fx.Dailies[1]
	got0 := env.Data.Entries[0]
	got1 := env.Data.Entries[1]
	if got0.ID != wantLegacy.ID || got1.ID != wantModern.ID {
		t.Fatalf("JR-13: ordering wrong: got [%s, %s] want [%s, %s] (legacy yesterday first)",
			got0.ID, got1.ID, wantLegacy.ID, wantModern.ID)
	}
	if got0.Date != wantLegacy.Date {
		t.Fatalf("JR-13: legacy entry date=%q want %q (filename prefix, not UUID7 narrative)",
			got0.Date, wantLegacy.Date)
	}
	if got1.Date != wantModern.Date {
		t.Fatalf("JR-13: modern entry date=%q want %q", got1.Date, wantModern.Date)
	}
	// Silent-legacy policy: legacy files carry no stderr warning at all.
	// Any "warning:" line here breaks that policy — the reader must treat
	// the filename prefix as authoritative without surfacing a hint that
	// the UUID7 disagrees.
	if strings.Contains(stderrTail, "warning:") {
		t.Fatalf("JR-13: unexpected stderr warning on a legacy/modern mix (silent-legacy policy); stderr:\n%s", stderrTail)
	}
}

// TestDistillHistoryRead_JR14_MultiTeamDefault_SingleTeam — default list only
// returns the active team. Failure prevented: reader defaults to
// all-teams and leaks the secondary team's entry; or team field missing.
func TestDistillHistoryRead_JR14_MultiTeamDefault_SingleTeam(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	skipIfNearUTCMidnight(t, now)
	e := setupDistillHistoryE2E(t, now)
	secondary := addSecondaryTeam(t, e, "team_read_e2e_t2", "Journal Read E2E T2", "journal-read-e2e-t2")
	fx := recipeMultiTeamList(t, e.primaryTeam, secondary, now)

	out, exit := e.Run(t, "distill", "history", "list", "--since=24h", "--format=json")
	if exit != 0 {
		t.Fatalf("JR-14: exit=%d want 0\nout:\n%s", exit, out)
	}
	env, _, _ := decodeJournalEnvelope(t, out)
	if env.Data == nil || len(env.Data.Entries) != 1 {
		t.Fatalf("JR-14: want 1 entry (primary only), got data=%+v", env.Data)
	}
	got := env.Data.Entries[0]
	wantPrimary := fx.Dailies[0]
	wantSecondary := fx.Dailies[1]
	if got.ID != wantPrimary.ID {
		t.Fatalf("JR-14: entry.id=%q want %q (primary daily, secondary must be excluded)",
			got.ID, wantPrimary.ID)
	}
	// Team field is the slug resolved via teamSlugOrID (distill_history_list.go),
	// not the raw team_id. The TODO left on this skeleton was wrong; guard
	// against a regression that starts emitting team_id.
	// Team field is the slug resolved via teamSlugOrID (distill_history_list.go),
	// not the raw team_id. The TODO left on this skeleton was wrong; the
	// slug equality check below is the standing guard against a regression
	// that starts emitting team_id. (The harness guarantees id != slug, so
	// a separate "team != id" assertion would be dead code.)
	if got.Team != e.primaryTeam.slug {
		t.Fatalf("JR-14: entry.team=%q want slug %q (teamSlugOrID picks Slug over TeamID)",
			got.Team, e.primaryTeam.slug)
	}
	// Defensive: the secondary daily must not appear anywhere in entries.
	if got.ID == wantSecondary.ID {
		t.Fatalf("JR-14: secondary daily %q leaked into default list", got.ID)
	}
}

// TestDistillHistoryRead_JR15_MultiTeamAllTeams_Merged — --all-teams merges
// both teams with stable ordering. Failure prevented: cross-team merge
// fails or ordering collapses to insertion order (spec §5.g).
func TestDistillHistoryRead_JR15_MultiTeamAllTeams_Merged(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	skipIfNearUTCMidnight(t, now)
	e := setupDistillHistoryE2E(t, now)
	secondary := addSecondaryTeam(t, e, "team_read_e2e_t2", "Journal Read E2E T2", "journal-read-e2e-t2")
	fx := recipeMultiTeamList(t, e.primaryTeam, secondary, now)

	out, exit := e.Run(t, "distill", "history", "list", "--since=24h", "--all-teams", "--format=json")
	if exit != 0 {
		t.Fatalf("JR-15: exit=%d want 0\nout:\n%s", exit, out)
	}
	env, _, _ := decodeJournalEnvelope(t, out)
	if env.Data == nil || len(env.Data.Entries) != 2 {
		t.Fatalf("JR-15: want 2 entries (merged across teams), got data=%+v", env.Data)
	}
	// Recipe stages primary daily at mtime=-3h and secondary daily at
	// mtime=-2h. Both share date=today. list.go:sortEntries orders by
	// (date asc, created_at asc) — so primary (older) comes first.
	want0 := fx.Dailies[0] // primary
	want1 := fx.Dailies[1] // secondary
	got0 := env.Data.Entries[0]
	got1 := env.Data.Entries[1]
	if got0.ID != want0.ID || got1.ID != want1.ID {
		t.Fatalf("JR-15: ordering wrong: got [%s, %s] want [%s, %s] (created_at asc: primary -3h < secondary -2h)",
			got0.ID, got1.ID, want0.ID, want1.ID)
	}
	if got0.Team != e.primaryTeam.slug {
		t.Fatalf("JR-15: entry[0].team=%q want %q (primary slug)", got0.Team, e.primaryTeam.slug)
	}
	if got1.Team != secondary.slug {
		t.Fatalf("JR-15: entry[1].team=%q want %q (secondary slug)", got1.Team, secondary.slug)
	}
	ca0, err := time.Parse(time.RFC3339, got0.CreatedAt)
	if err != nil {
		t.Fatalf("JR-15: parse created_at[0]=%q: %v", got0.CreatedAt, err)
	}
	ca1, err := time.Parse(time.RFC3339, got1.CreatedAt)
	if err != nil {
		t.Fatalf("JR-15: parse created_at[1]=%q: %v", got1.CreatedAt, err)
	}
	if !ca0.Before(ca1) {
		t.Fatalf("JR-15: expected created_at ascending across merged teams, got [0]=%s [1]=%s",
			ca0.Format(time.RFC3339), ca1.Format(time.RFC3339))
	}
}

// TestDistillHistoryRead_JR19_TeamNotFound_ErrorEnvelope — no team context
// registered. Failure prevented: reader panics on missing team, or
// returns empty-success (which hides misconfiguration from agents).
func TestDistillHistoryRead_JR19_TeamNotFound_ErrorEnvelope(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupDistillHistoryE2E(t, now)

	// Wipe the team context: empty config.local.toml and remove the
	// primary team root so nothing the reader walks finds a registered team.
	// resolveDistillHistoryTeams calls config.FindRepoTeamContext which returns nil
	// on an empty config, so the command layer funnels through
	// writeJournalRuntimeError with code "team_not_found".
	emptyCfg := filepath.Join(e.workspace, ".sageox", "config.local.toml")
	if err := os.WriteFile(emptyCfg, []byte(""), 0o600); err != nil {
		t.Fatalf("wipe config.local.toml: %v", err)
	}
	if err := os.RemoveAll(e.primaryTeam.path); err != nil {
		t.Fatalf("remove primary team path: %v", err)
	}

	out, exit := e.Run(t, "distill", "history", "list", "--since=24h", "--format=json")
	if exit != 1 {
		t.Fatalf("JR-19: exit=%d want 1 (runtime error, not usage error)\nout:\n%s", exit, out)
	}
	env, _, _ := decodeJournalEnvelope(t, out)
	if env.Success {
		t.Fatalf("JR-19: success=true want false")
	}
	if env.Error == nil {
		t.Fatalf("JR-19: error envelope is nil")
	}
	if env.Error.Code != "team_not_found" {
		t.Fatalf("JR-19: error.code=%q want %q", env.Error.Code, "team_not_found")
	}
	if env.Error.Retryable {
		t.Fatalf("JR-19: error.retryable=true want false (missing config is not transient)")
	}
	if env.Error.Message == "" {
		t.Fatalf("JR-19: error.message is empty — must carry a human-readable explanation")
	}
	// Defensive: no data block on an error envelope. If this fires, the
	// command is leaking partial state back to the agent alongside the error.
	if env.Data != nil {
		t.Fatalf("JR-19: data=%+v want nil on error envelope", env.Data)
	}
}

// TestDistillHistoryRead_JR20_UsageError_InvalidSince — bad duration flag.
// Failure prevented: usage errors land on exit 1 instead of exit 2,
// collapsing "bad flag" and "real failure" for agents.
func TestDistillHistoryRead_JR20_UsageError_InvalidSince(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupDistillHistoryE2E(t, now)
	// Stage a valid daily so we can prove the usage error fires BEFORE
	// the reader walks files — a non-zero len(entries) here would mean the
	// reader silently fell back to now() instead of rejecting the bad flag.
	_ = recipeMinimalDaily(t, e.primaryTeam, now)

	out, exit := e.Run(t, "distill", "history", "list", "--since=not-a-duration", "--format=json")
	if exit != 2 {
		t.Fatalf("JR-20: exit=%d want 2 (usage error, distinct from runtime exit 1)\nout:\n%s", exit, out)
	}
	env, _, _ := decodeJournalEnvelope(t, out)
	if env.Success {
		t.Fatalf("JR-20: success=true want false")
	}
	if env.Error == nil {
		t.Fatalf("JR-20: error envelope is nil")
	}
	if env.Error.Code != "usage_error" {
		t.Fatalf("JR-20: error.code=%q want %q", env.Error.Code, "usage_error")
	}
	if env.Error.Retryable {
		t.Fatalf("JR-20: error.retryable=true want false (bad flag is not transient)")
	}
	// The daily that we staged must not leak into the error envelope —
	// the reader is expected to refuse to run at all on a bad --since.
	if env.Data != nil {
		t.Fatalf("JR-20: data=%+v want nil — usage errors must not carry partial results", env.Data)
	}
}

// TestDistillHistoryRead_JR21_AbsoluteTzRoundTrip — LA-local day window that
// spans UTC midnight. Failure prevented: --tz ignored, offset applied
// wrong direction, or rounding computed before conversion (spec §3.4).
func TestDistillHistoryRead_JR21_AbsoluteTzRoundTrip(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	skipIfNearUTCMidnight(t, now)
	e := setupDistillHistoryE2E(t, now)
	fx := recipeTwoConsecutiveUtcDays(t, e.primaryTeam, now)

	// Absolute window derived from the captured `now`: "yesterday in LA,
	// the whole day". Once converted to UTC this straddles UTC midnight
	// and, after outward day-rounding, covers both yesterday(now) and
	// today(now) UTC days — matching the two files the recipe staged.
	yesterday := dayString(daysBack(now, 1))
	since := fmt.Sprintf("%sT00:00", yesterday)
	until := fmt.Sprintf("%sT23:59", yesterday)

	out, exit := e.Run(t, "distill", "history", "list",
		"--since="+since,
		"--until="+until,
		"--tz=America/Los_Angeles",
		"--format=json")
	if exit != 0 {
		t.Fatalf("JR-21: exit=%d want 0\nout:\n%s", exit, out)
	}
	env, _, _ := decodeJournalEnvelope(t, out)
	if env.Data == nil || len(env.Data.Entries) != 2 {
		t.Fatalf("JR-21: want 2 entries (yesterday+today UTC after LA→UTC rounding), got data=%+v", env.Data)
	}
	// Recipe stages fx.Dailies[0]=yesterday(UTC prefix), fx.Dailies[1]=today(UTC prefix).
	// sortEntries orders by (date asc) so yesterday must precede today.
	wantYesterday := fx.Dailies[0]
	wantToday := fx.Dailies[1]
	got0 := env.Data.Entries[0]
	got1 := env.Data.Entries[1]
	if got0.ID != wantYesterday.ID || got1.ID != wantToday.ID {
		t.Fatalf("JR-21: ordering wrong: got [%s, %s] want [%s, %s] (date asc)",
			got0.ID, got1.ID, wantYesterday.ID, wantToday.ID)
	}
	if got0.Date != wantYesterday.Date {
		t.Fatalf("JR-21: entry[0].date=%q want %q", got0.Date, wantYesterday.Date)
	}
	if got1.Date != wantToday.Date {
		t.Fatalf("JR-21: entry[1].date=%q want %q", got1.Date, wantToday.Date)
	}
	// Window assertions: the LA-local window [yesterday 00:00 LA, yesterday
	// 23:59 LA] converts to UTC as [yesterday 07:00Z, today 06:59Z] in PDT
	// (or 08:00/07:59 in PST). dayFloor/dayCeil then round outward to
	// [yesterday 00:00Z, tomorrow 00:00Z) — a two-day window containing
	// both staged files. The specific expected strings are derived from
	// the captured `now` so the test is agnostic to DST.
	if env.Data.Window == nil {
		t.Fatalf("JR-21: window is nil")
	}
	wantSince := dayFloorUTC(daysBack(now, 1)).Format(time.RFC3339)
	wantUntil := dayCeilUTC(now).Format(time.RFC3339)
	if env.Data.Window.Since != wantSince {
		t.Fatalf("JR-21: window.since=%q want %q (outward day-round of LA→UTC)",
			env.Data.Window.Since, wantSince)
	}
	if env.Data.Window.Until != wantUntil {
		t.Fatalf("JR-21: window.until=%q want %q (outward day-round of LA→UTC)",
			env.Data.Window.Until, wantUntil)
	}
}

// TestDistillHistoryRead_JR22_TzConflictAndInvalid — two usage subtests on
// malformed --tz combinations. Failure prevented: conflict silently
// honored, invalid zone defaults to UTC or panics.
func TestDistillHistoryRead_JR22_TzConflictAndInvalid(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupDistillHistoryE2E(t, now)

	// Both subtests share the same harness/binary — neither mutates disk
	// state and neither cares about fixtures. They only differ in flags.

	t.Run("A_conflicting_tz", func(t *testing.T) {
		out, exit := e.Run(t, "distill", "history", "list",
			"--since=2026-04-12T09:00-07:00",
			"--tz=America/Los_Angeles",
			"--format=json")
		if exit != 2 {
			t.Fatalf("JR-22A: exit=%d want 2 (conflicting tz is a usage error)\nout:\n%s", exit, out)
		}
		env, _, _ := decodeJournalEnvelope(t, out)
		if env.Success {
			t.Fatalf("JR-22A: success=true want false")
		}
		if env.Error == nil {
			t.Fatalf("JR-22A: error envelope is nil")
		}
		if env.Error.Code != "usage_error" {
			t.Fatalf("JR-22A: error.code=%q want %q", env.Error.Code, "usage_error")
		}
		// parseJournalAbsoluteTimestamp emits "conflicting timezone
		// specification on --since: ..." — match on the "conflicting
		// timezone" substring so a future message rewording doesn't
		// accidentally break the guard.
		if !strings.Contains(env.Error.Message, "conflicting timezone") {
			t.Fatalf("JR-22A: error.message=%q must mention 'conflicting timezone'", env.Error.Message)
		}
		if env.Data != nil {
			t.Fatalf("JR-22A: data=%+v want nil on a usage-error envelope", env.Data)
		}
	})

	t.Run("B_invalid_zone", func(t *testing.T) {
		out, exit := e.Run(t, "distill", "history", "list",
			"--since=2026-04-12T09:00",
			"--tz=Not/A/Real/Zone",
			"--format=json")
		if exit != 2 {
			t.Fatalf("JR-22B: exit=%d want 2 (invalid zone is a usage error)\nout:\n%s", exit, out)
		}
		env, _, _ := decodeJournalEnvelope(t, out)
		if env.Success {
			t.Fatalf("JR-22B: success=true want false")
		}
		if env.Error == nil {
			t.Fatalf("JR-22B: error envelope is nil")
		}
		if env.Error.Code != "usage_error" {
			t.Fatalf("JR-22B: error.code=%q want %q", env.Error.Code, "usage_error")
		}
		// resolveDistillHistoryWindow emits "invalid timezone %q: <LoadLocation err>".
		if !strings.Contains(env.Error.Message, "invalid timezone") {
			t.Fatalf("JR-22B: error.message=%q must mention 'invalid timezone'", env.Error.Message)
		}
		if env.Data != nil {
			t.Fatalf("JR-22B: data=%+v want nil on a usage-error envelope", env.Data)
		}
	})
}

// TestDistillHistoryRead_JR23_EffectiveWindowRounding — envelope reports
// day-rounded boundaries, not raw --since/--until. Failure prevented:
// CLI leaks raw input instants, silently defeating caller's ability to
// detect rounding.
func TestDistillHistoryRead_JR23_EffectiveWindowRounding(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	skipIfNearUTCMidnight(t, now)
	e := setupDistillHistoryE2E(t, now)
	fx := recipeMinimalDaily(t, e.primaryTeam, now)

	out, exit := e.Run(t, "distill", "history", "list", "--since=6h", "--format=json")
	if exit != 0 {
		t.Fatalf("JR-23: exit=%d want 0\nout:\n%s", exit, out)
	}
	env, _, _ := decodeJournalEnvelope(t, out)
	if env.Data == nil || len(env.Data.Entries) != 1 {
		t.Fatalf("JR-23: want 1 entry (the minimal daily), got data=%+v", env.Data)
	}
	if got, want := env.Data.Entries[0].ID, fx.Dailies[0].ID; got != want {
		t.Fatalf("JR-23: entry.id=%q want %q", got, want)
	}
	if env.Data.Window == nil {
		t.Fatalf("JR-23: window is nil")
	}
	// Spec §3.4 rounding contract: the envelope window is always day-rounded
	// outward from the raw --since/--until, so agents can tell the reader
	// rounded their input without re-computing the grammar themselves. If
	// the reader leaked the raw now-6h (an odd time-of-day instant), the
	// string here would end in something other than T00:00:00Z.
	wantSince := dayFloorUTC(now.Add(-6 * time.Hour)).Format(time.RFC3339)
	wantUntil := dayCeilUTC(now).Format(time.RFC3339)
	if env.Data.Window.Since != wantSince {
		t.Fatalf("JR-23: window.since=%q want %q (day-floor(now-6h), not raw now-6h)",
			env.Data.Window.Since, wantSince)
	}
	if env.Data.Window.Until != wantUntil {
		t.Fatalf("JR-23: window.until=%q want %q (day-ceil(now), not raw now)",
			env.Data.Window.Until, wantUntil)
	}
	// Hard lock-in: a raw instant would never end in T00:00:00Z (modulo a
	// once-per-day collision we've already skipped via skipIfNearUTCMidnight).
	// This catches a regression that day-rounds the inputs but then formats
	// the raw values anyway.
	if !strings.HasSuffix(env.Data.Window.Since, "T00:00:00Z") {
		t.Fatalf("JR-23: window.since=%q does not end at a UTC midnight — rounding escaped to the wire", env.Data.Window.Since)
	}
	if !strings.HasSuffix(env.Data.Window.Until, "T00:00:00Z") {
		t.Fatalf("JR-23: window.until=%q does not end at a UTC midnight — rounding escaped to the wire", env.Data.Window.Until)
	}
}

// TestDistillHistoryRead_JR24_StaleFilenameRecentMtime_EventDayWins — 90-day-
// old filename with recent mtime. Failure prevented: reader uses mtime
// or UUID7 for window filtering instead of filename prefix, breaking
// the event-day vs write-time contract. Single most important semantic
// lock-in for spec §2.1.
func TestDistillHistoryRead_JR24_StaleFilenameRecentMtime_EventDayWins(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	skipIfNearUTCMidnight(t, now)
	e := setupDistillHistoryE2E(t, now)
	_ = recipeStaleFilenameRecentMtime(t, e.primaryTeam, now)

	out, exit := e.Run(t, "distill", "history", "list", "--since=24h", "--format=json")
	if exit != 0 {
		t.Fatalf("JR-24: exit=%d want 0\nout:\n%s", exit, out)
	}
	env, _, stderrTail := decodeJournalEnvelope(t, out)
	if !env.Success {
		t.Fatalf("JR-24: success=false want true; err=%+v", env.Error)
	}
	if env.Data == nil {
		t.Fatalf("JR-24: data is nil")
	}
	// Core lock-in: the file's mtime is now-1h, which IS inside the
	// [day-floor(now-24h), day-ceil(now)) window. The file's filename
	// prefix is 90 days ago, which is NOT inside that window. If the
	// reader used mtime for filtering, entries would be 1; it uses the
	// filename prefix, so entries must be 0. This pins spec §5.b item a
	// (trust the prefix) and §2.1 (event day, not write time) at once.
	if got := len(env.Data.Entries); got != 0 {
		t.Fatalf("JR-24: entries len=%d want 0 — reader appears to be filtering by mtime instead of filename prefix; entries=%+v",
			got, env.Data.Entries)
	}
	// The stale file is silently excluded: it is not a malformed filename,
	// not a malformed date, and not a read error — just out of window. No
	// stderr warning should fire.
	if strings.Contains(stderrTail, "warning:") {
		t.Fatalf("JR-24: unexpected stderr warning on a stale-filename exclusion; stderr:\n%s", stderrTail)
	}
	if env.Data.Window == nil {
		t.Fatalf("JR-24: window is nil")
	}
	wantSince := dayFloorUTC(now.Add(-24 * time.Hour)).Format(time.RFC3339)
	wantUntil := dayCeilUTC(now).Format(time.RFC3339)
	if env.Data.Window.Since != wantSince {
		t.Fatalf("JR-24: window.since=%q want %q", env.Data.Window.Since, wantSince)
	}
	if env.Data.Window.Until != wantUntil {
		t.Fatalf("JR-24: window.until=%q want %q", env.Data.Window.Until, wantUntil)
	}
}
