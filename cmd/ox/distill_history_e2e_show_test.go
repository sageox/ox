//go:build slow

// distill_history_e2e_show_test.go — Batch D JR-* cases for `ox distill history
// show`. The 7 cases that exercise the show command. Harness, fixtures,
// and recipes live in distill_history_e2e_test.go; this file only adds the
// per-case test funcs.
//
// All 7 cases assert real envelope / stdout behavior against Unit 4
// (LoadEntries + ox distill history show). Per plan §3 Unit 4, show is
// daily-only, its flag surface is {--team, --format}, and per-ID
// failures are partial-success unless every requested ID failed (in
// which case success:false + exit 1).
//
// See docs/specs/distill-history-read-test-plan.md §2 rows JR-04..JR-08,
// JR-17, JR-18.

package main

import (
	"strings"
	"testing"
	"time"
)

// TestDistillHistoryRead_JR04_ShowBareDate_ReturnsAllSnapshots — bare-date
// match returns the full same-day union. Failure prevented: reader
// picks "newest snapshot" as the default or returns only one entry.
func TestDistillHistoryRead_JR04_ShowBareDate_ReturnsAllSnapshots(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupDistillHistoryE2E(t, now)
	fx := recipeMultiSnapshotSameDay(t, e.primaryTeam, now)

	// Use the captured `now` to form the date argument. Both staged
	// snapshots share this date. show uses NoTimeFilter so midnight
	// straddling does NOT affect the lookup — the date arg is compared
	// against the filename prefix, not a window bound.
	date := dayString(now)
	out, exit := e.Run(t, "distill", "history", "show", date, "--format=json")
	if exit != 0 {
		t.Fatalf("JR-04: exit=%d want 0\nout:\n%s", exit, out)
	}
	env, _, _ := decodeJournalEnvelope(t, out)
	if !env.Success {
		t.Fatalf("JR-04: success=false want true; err=%+v", env.Error)
	}
	if env.Type != "distill_history_show" {
		t.Fatalf("JR-04: type=%q want %q", env.Type, "distill_history_show")
	}
	if env.Error != nil {
		t.Fatalf("JR-04: unexpected error envelope: %+v", env.Error)
	}
	if env.Data == nil || len(env.Data.Entries) != 2 {
		t.Fatalf("JR-04: want 2 entries (bare-date expands to same-day union per spec §4.4), got data=%+v", env.Data)
	}
	// show is not window-scoped: the reader must not populate Window.
	// A regression that stamps a day-rounded window on show responses
	// would confuse agents into filtering by window the command didn't
	// actually apply.
	if env.Data.Window != nil {
		t.Fatalf("JR-04: window=%+v want nil — show is not window-scoped", env.Data.Window)
	}

	// Recipe stages fx.Dailies[0]=older (-4h mtime), fx.Dailies[1]=newer
	// (-2h mtime). The bare-date path in loadEntries walks the listEntries
	// index, which sorts (date asc, created_at asc) — so the older
	// snapshot must precede the newer one. This is the same ordering
	// contract as list's JR-03.
	want0 := fx.Dailies[0]
	want1 := fx.Dailies[1]
	got0 := env.Data.Entries[0]
	got1 := env.Data.Entries[1]
	if got0.ID != want0.ID || got1.ID != want1.ID {
		t.Fatalf("JR-04: ordering wrong: got [%s, %s] want [%s, %s] (older mtime first)",
			got0.ID, got1.ID, want0.ID, want1.ID)
	}
	if got0.Date != date || got1.Date != date {
		t.Fatalf("JR-04: dates wrong: [%s, %s] want both %q", got0.Date, got1.Date, date)
	}
	ca0, err := time.Parse(time.RFC3339, got0.CreatedAt)
	if err != nil {
		t.Fatalf("JR-04: parse created_at[0]=%q: %v", got0.CreatedAt, err)
	}
	ca1, err := time.Parse(time.RFC3339, got1.CreatedAt)
	if err != nil {
		t.Fatalf("JR-04: parse created_at[1]=%q: %v", got1.CreatedAt, err)
	}
	if !ca0.Before(ca1) {
		t.Fatalf("JR-04: expected created_at ascending, got [0]=%s [1]=%s",
			ca0.Format(time.RFC3339), ca1.Format(time.RFC3339))
	}
	// show populates BodyMD on every ok row — that's the whole point of
	// the command. Frontmatter must be stripped (reader delegates to
	// memoryio.StripFrontmatter in parseEntryFile) so the body starts
	// with the "# Daily Memory" heading, not a "---" fence.
	if got0.BodyMD == "" || got1.BodyMD == "" {
		t.Fatalf("JR-04: empty BodyMD on at least one row: [0]=%q [1]=%q", got0.BodyMD, got1.BodyMD)
	}
	if strings.HasPrefix(got0.BodyMD, "---") || strings.HasPrefix(got1.BodyMD, "---") {
		t.Fatalf("JR-04: frontmatter fence leaked into BodyMD on at least one row")
	}
	// Strict Status pin — parseEntryFile at internal/distill/history/read/list.go:365
	// unconditionally stamps Status: "ok" on every successful parse, and
	// distill_history_show.go:97's allFailed detector is an exact-string "ok" check.
	// A silent refactor that dropped the stamp would flip allFailed true for
	// healthy rows and route into the Exit 1 branch with a spurious
	// id_not_found. Byte-match the unit-level pin at show_test.go:179.
	if got0.Status != "ok" {
		t.Fatalf("JR-04: entries[0].status=%q want 'ok' on a success row", got0.Status)
	}
	if got1.Status != "ok" {
		t.Fatalf("JR-04: entries[1].status=%q want 'ok' on a success row", got1.Status)
	}
}

// TestDistillHistoryRead_JR05_ShowLatestRejected — --latest must be rejected
// by cobra at parse time. Failure prevented: --latest was accidentally
// implemented, or is silently ignored (drops to JR-04 behavior).
func TestDistillHistoryRead_JR05_ShowLatestRejected(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupDistillHistoryE2E(t, now)
	fx := recipeMultiSnapshotSameDay(t, e.primaryTeam, now)

	date := dayString(now)
	out, exit := e.Run(t, "distill", "history", "show", date, "--latest", "--format=json")
	// cobra rejects unknown flags at parse time — BEFORE runDistillHistoryShow
	// runs — so this path does NOT emit a distillHistoryEnvelope at all. The
	// error travels via cobra's own "Error: unknown flag: --latest\n"
	// write to stderr and a non-zero Execute() return, which main.go
	// surfaces as a non-zero exit. Do NOT call decodeJournalEnvelope
	// here: it would t.Fatalf on "no JSON envelope found", masking the
	// actual cobra output.
	if exit == 0 {
		t.Fatalf("JR-05: exit=0 unexpected — cobra must reject --latest at parse time\nout:\n%s\nfx=%d",
			out, len(fx.Dailies))
	}
	// Match the cobra error text. This is stable across cobra versions:
	// "Error: unknown flag: --latest".
	if !strings.Contains(out, "unknown flag") || !strings.Contains(out, "--latest") {
		t.Fatalf("JR-05: output must mention 'unknown flag' and '--latest' — cobra is not rejecting it; got:\n%s", out)
	}
	// Hard lock-in: none of the distillHistoryEnvelope shape markers may appear
	// on stdout. If a future change silently swallows unknown flags and
	// fell through to runDistillHistoryShow, a JR-04-shaped envelope would leak
	// out and this check would trip.
	if strings.Contains(out, `"success"`) || strings.Contains(out, `"distill_history_show"`) || strings.Contains(out, `"entries"`) {
		t.Fatalf("JR-05: envelope fields leaked into output — --latest may be silently honored:\n%s", out)
	}
}

// TestDistillHistoryRead_JR06_ShowShortUUID7Prefix — 8-char UUID7 prefix
// matches exactly one file. Failure prevented: prefix matcher only
// accepts full IDs or only date-prefixed IDs (spec §4.4).
func TestDistillHistoryRead_JR06_ShowShortUUID7Prefix(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupDistillHistoryE2E(t, now)
	fx := recipeMultiSnapshotSameDay(t, e.primaryTeam, now)

	// Pull a deterministic short prefix off the first staged file's ID.
	// The recipe uses "019c8a3f" for the older snapshot, but drive the
	// test off fx rather than hard-coding — that way recipe edits
	// propagate automatically.
	if len(fx.Dailies) < 1 {
		t.Fatalf("JR-06: recipe staged %d dailies, want ≥1", len(fx.Dailies))
	}
	// The stem shape is "<date>-<uuid7>[-extra]"; slice off the date prefix
	// (11 chars incl. trailing '-') to reach the UUID7 segment.
	stem := fx.Dailies[0].ID
	const datePrefixLen = len("2006-01-02") + 1 // YYYY-MM-DD + '-'
	shortID := stem[datePrefixLen : datePrefixLen+8]

	out, exit := e.Run(t, "distill", "history", "show", shortID, "--format=json")
	if exit != 0 {
		t.Fatalf("JR-06: exit=%d want 0 (short prefix must be a valid lookup)\nshortID=%s\nout:\n%s",
			exit, shortID, out)
	}
	env, _, _ := decodeJournalEnvelope(t, out)
	if !env.Success {
		t.Fatalf("JR-06: success=false want true; err=%+v", env.Error)
	}
	if env.Type != "distill_history_show" {
		t.Fatalf("JR-06: type=%q want distill_history_show", env.Type)
	}
	if env.Error != nil {
		t.Fatalf("JR-06: unexpected error envelope: %+v", env.Error)
	}
	// The crucial invariant: "019c8a3f" is a prefix of the older snapshot
	// only — the newer snapshot is "019c87aa" in the same fixture. If
	// matchID's looksHexOrDash branch ever widened (e.g. substring match)
	// this would promote to ambiguous and this check would trip.
	if env.Data == nil || len(env.Data.Entries) != 1 {
		t.Fatalf("JR-06: want exactly 1 entry for a unique short-prefix, got data=%+v", env.Data)
	}
	got := env.Data.Entries[0]
	if got.ID != fx.Dailies[0].ID {
		t.Fatalf("JR-06: entry.id=%q want %q — short prefix matched the wrong file",
			got.ID, fx.Dailies[0].ID)
	}
	// Strict Status pin — parseEntryFile stamps "ok" unconditionally on
	// every successful parse (list.go:365); distill_history_show.go:97 does an
	// exact-string "ok" check to decide allFailed. Loose "" || "ok" here
	// would miss a silent drop of the stamp. Byte-match show_test.go:179.
	if got.Status != "ok" {
		t.Fatalf("JR-06: entry.status=%q want 'ok' on a success row", got.Status)
	}
	if got.Error != nil {
		t.Fatalf("JR-06: entry.error=%+v want nil on a success row", got.Error)
	}
	if got.BodyMD == "" {
		t.Fatalf("JR-06: BodyMD empty — show must materialize the body even for short-ID lookups")
	}
}

// TestDistillHistoryRead_JR07_ShowAmbiguousPrefix_Errors — duplicate prefix
// forces id_ambiguous. Failure prevented: ambiguous-prefix logic
// returns the first match instead of failing, silently feeding agents
// the wrong content.
func TestDistillHistoryRead_JR07_ShowAmbiguousPrefix_Errors(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupDistillHistoryE2E(t, now)
	fx := recipeAmbiguousPrefix(t, e.primaryTeam, now)

	// recipeAmbiguousPrefix builds on recipeMultiSnapshotSameDay (which
	// stages "019c8a3f" and "019c87aa") and adds a third daily with stem
	// "019c8a3f-dup". Looking up "019c8a3f" matches TWO files — the base
	// snapshot and the dup — so loadEntries must promote to id_ambiguous.
	out, exit := e.Run(t, "distill", "history", "show", "019c8a3f", "--format=json")
	// Single-ID all-failed (the ONE requested ID resolved as ambiguous)
	// must surface as success:false + exit 1. Per show's allFailed branch
	// in distill_history_show.go, only if some row came back ok does exit 0
	// hold — an ambiguous single-ID query fails the whole call.
	if exit != 1 {
		t.Fatalf("JR-07: exit=%d want 1 (single-ID ambiguous → runtime error)\nout:\n%s\ndailies=%d",
			exit, out, len(fx.Dailies))
	}
	env, _, _ := decodeJournalEnvelope(t, out)
	if env.Success {
		t.Fatalf("JR-07: success=true want false on a single-ID ambiguous lookup")
	}
	if env.Error == nil {
		t.Fatalf("JR-07: error envelope is nil — want id_ambiguous")
	}
	if env.Error.Code != "id_ambiguous" {
		t.Fatalf("JR-07: error.code=%q want 'id_ambiguous'", env.Error.Code)
	}
	if env.Error.Retryable {
		t.Fatalf("JR-07: error.retryable=true want false (ambiguous lookup is not a transient fault)")
	}
	if env.Error.Message == "" {
		t.Fatalf("JR-07: error.message is empty — must list candidate stems for the agent to disambiguate")
	}
	// Defensive: all-failed show emits NO data block. A partial data
	// leak here would tempt agents to pick one of the ambiguous matches
	// and silently feed the wrong content to the next step.
	if env.Data != nil {
		t.Fatalf("JR-07: data=%+v want nil on all-failed show", env.Data)
	}
}

// TestDistillHistoryRead_JR08_ShowContentFormatStripsEnvelope — --format=content
// emits only markdown on stdout. Failure prevented: frontmatter not
// stripped, JSON envelope leaks into stdout, or stderr content lands on
// stdout and corrupts the pipe to `claude`. factory/distill/summary.ts
// depends on this exact contract.
func TestDistillHistoryRead_JR08_ShowContentFormatStripsEnvelope(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupDistillHistoryE2E(t, now)
	fx := recipeMinimalDaily(t, e.primaryTeam, now)

	if len(fx.Dailies) < 1 {
		t.Fatalf("JR-08: recipe staged %d dailies, want 1", len(fx.Dailies))
	}
	fullID := fx.Dailies[0].ID

	out, exit := e.Run(t, "distill", "history", "show", fullID, "--format=content")
	if exit != 0 {
		t.Fatalf("JR-08: exit=%d want 0\nout:\n%s", exit, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatalf("JR-08: stdout empty — content format must emit the body")
	}
	// Content mode is what factory/distill/summary.ts uses to pipe
	// markdown straight to `claude`. A JSON envelope leak here would
	// poison that pipe with non-markdown tokens. Scan for every
	// envelope shape marker — any one of them means the writer
	// accidentally dropped the JSON line onto stdout.
	if strings.Contains(out, `"success"`) || strings.Contains(out, `"entries"`) || strings.Contains(out, `"distill_history_show"`) {
		t.Fatalf("JR-08: envelope fields leaked into content stdout:\n%s", out)
	}
	// The per-entry marker comment is allowed on the first non-blank
	// line (renderDistillHistoryShowContent emits `<!-- entry: %s -->` before
	// each body), so we walk past any leading marker/blank lines to
	// find the first real markdown line. That line must be the
	// "# Daily Memory — <date>" heading — NOT a "---" fence and NOT a
	// "sources:" line, either of which would mean the reader failed to
	// strip frontmatter.
	wantHeading := "# Daily Memory — " + fx.Dailies[0].Date
	foundHeading := false
	for _, ln := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "<!-- entry:") {
			continue
		}
		if trimmed == "---" || strings.HasPrefix(trimmed, "sources:") {
			t.Fatalf("JR-08: frontmatter line %q appears before heading — reader did not strip it:\n%s",
				trimmed, out)
		}
		if trimmed != wantHeading {
			t.Fatalf("JR-08: first content line %q — want %q\nfull stdout:\n%s",
				trimmed, wantHeading, out)
		}
		foundHeading = true
		break
	}
	if !foundHeading {
		t.Fatalf("JR-08: heading %q not found in stdout:\n%s", wantHeading, out)
	}
}

// TestDistillHistoryRead_JR17_ShowJsonFormat_FullBody — JSON show returns
// body_md, citations, source_files, elapsed_ms. Failure prevented:
// show returns only metadata or leaves frontmatter in body_md.
func TestDistillHistoryRead_JR17_ShowJsonFormat_FullBody(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupDistillHistoryE2E(t, now)
	fx := recipeMinimalDaily(t, e.primaryTeam, now)

	if len(fx.Dailies) < 1 {
		t.Fatalf("JR-17: recipe staged %d dailies, want 1", len(fx.Dailies))
	}
	fullID := fx.Dailies[0].ID

	out, exit := e.Run(t, "distill", "history", "show", fullID, "--format=json")
	if exit != 0 {
		t.Fatalf("JR-17: exit=%d want 0\nout:\n%s", exit, out)
	}
	env, _, _ := decodeJournalEnvelope(t, out)
	if !env.Success {
		t.Fatalf("JR-17: success=false want true; err=%+v", env.Error)
	}
	if env.Type != "distill_history_show" {
		t.Fatalf("JR-17: type=%q want distill_history_show", env.Type)
	}
	// elapsed_ms is always present on the envelope (see §4.1). We can't
	// pin an upper bound without flake, but a negative value is a bug in
	// the wall-clock measurement.
	if env.ElapsedMS < 0 {
		t.Fatalf("JR-17: elapsed_ms=%d want >= 0", env.ElapsedMS)
	}
	if env.Data == nil || len(env.Data.Entries) != 1 {
		t.Fatalf("JR-17: want exactly 1 entry, got data=%+v", env.Data)
	}
	got := env.Data.Entries[0]
	if got.ID != fullID {
		t.Fatalf("JR-17: entry.id=%q want %q", got.ID, fullID)
	}
	if got.BodyMD == "" {
		t.Fatalf("JR-17: body_md empty — show must materialize the body")
	}
	// Frontmatter stripping lock-in. Matches the unit-level assertion
	// in internal/distill/history/read/show_test.go:185 so this e2e test will
	// trip at the same time the unit test would if parseEntryFile ever
	// stops calling memoryio.StripFrontmatter.
	if strings.HasPrefix(got.BodyMD, "---") {
		head := got.BodyMD
		if len(head) > 40 {
			head = head[:40]
		}
		t.Fatalf("JR-17: body_md still has frontmatter fence: %q", head)
	}
	wantHeading := "# Daily Memory — " + fx.Dailies[0].Date
	if !strings.Contains(got.BodyMD, wantHeading) {
		t.Fatalf("JR-17: body_md missing heading %q\nbody_md=%q", wantHeading, got.BodyMD)
	}
	// source_files passes through verbatim from the frontmatter sources:
	// list. Recipe stages exactly one source.
	if len(got.SourceFiles) != 1 || got.SourceFiles[0] != fx.Dailies[0].Sources[0] {
		t.Fatalf("JR-17: source_files=%v want [%q]", got.SourceFiles, fx.Dailies[0].Sources[0])
	}
	// recipeMinimalDaily emits exactly one `[1]: url` numbered reference,
	// so citation_count must be 1 and the citations array must be
	// populated (show sets both — list only sets the count).
	if got.CitationCount != 1 {
		t.Fatalf("JR-17: citation_count=%d want 1", got.CitationCount)
	}
	if len(got.Citations) == 0 {
		t.Fatalf("JR-17: citations array empty but citation_count=%d — show must populate both",
			got.CitationCount)
	}
	// Strict Status pin — see JR-04/JR-06 comments. Loose "" || "ok" hides
	// a regression in parseEntryFile that silently drops the stamp, which
	// would flip distill_history_show.go:97's allFailed detector to true for a
	// healthy row and route it into the Exit 1 branch.
	if got.Status != "ok" {
		t.Fatalf("JR-17: entry.status=%q want 'ok' on a success row", got.Status)
	}
	if got.Error != nil {
		t.Fatalf("JR-17: entry.error=%+v want nil on a success row", got.Error)
	}
}

// TestDistillHistoryRead_JR18_ShowBadFrontmatterNeighbor_SurvivesGoodID —
// one malformed neighbor does not break show for an unrelated good ID.
// Failure prevented: one bad frontmatter file poisons the whole
// directory walk and hides the valid entry.
func TestDistillHistoryRead_JR18_ShowBadFrontmatterNeighbor_SurvivesGoodID(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	e := setupDistillHistoryE2E(t, now)
	fx := recipeBadFrontmatterNeighbor(t, e.primaryTeam, now)

	if len(fx.Dailies) < 1 {
		t.Fatalf("JR-18: recipe staged %d dailies, want 1", len(fx.Dailies))
	}
	goodID := fx.Dailies[0].ID

	out, exit := e.Run(t, "distill", "history", "show", goodID, "--format=json")
	if exit != 0 {
		t.Fatalf("JR-18: exit=%d want 0 — one bad neighbor must not break show for a valid ID\nextras=%d\nout:\n%s",
			exit, len(fx.ExtraFiles), out)
	}
	env, _, _ := decodeJournalEnvelope(t, out)
	if !env.Success {
		t.Fatalf("JR-18: success=false want true; err=%+v", env.Error)
	}
	if env.Error != nil {
		t.Fatalf("JR-18: unexpected error envelope: %+v", env.Error)
	}
	if env.Data == nil || len(env.Data.Entries) != 1 {
		t.Fatalf("JR-18: want exactly 1 entry (the good daily), got data=%+v", env.Data)
	}
	got := env.Data.Entries[0]
	if got.ID != goodID {
		t.Fatalf("JR-18: entry.id=%q want %q — bad neighbor poisoned the good lookup",
			got.ID, goodID)
	}
	// The good file's body must have materialized — if listEntries had
	// crashed on the bad neighbor's frontmatter, the good file (same
	// directory, same scan) would either be missing from the index or
	// returned without body. Both failure modes would trip this check.
	if got.BodyMD == "" {
		t.Fatalf("JR-18: BodyMD empty — good entry did not materialize despite a valid ID lookup")
	}
	// Strict Status pin — see JR-04/JR-06/JR-17 comments. The surviving
	// row must carry the "ok" stamp even in the presence of a bad
	// neighbor, so the allFailed detector in distill_history_show.go:97 keeps the
	// call on the Exit 0 success branch.
	if got.Status != "ok" {
		t.Fatalf("JR-18: entry.status=%q want 'ok' on the surviving row", got.Status)
	}
	if got.Error != nil {
		t.Fatalf("JR-18: entry.error=%+v want nil on the surviving row", got.Error)
	}
	// Note: we deliberately do NOT assert a stderr warning about the bad
	// neighbor. parseEntryFile today tolerates broken frontmatter (the
	// memoryio parsers return empty values rather than errors, see
	// internal/distill/history/read/list.go:342-372), so the bad .md is listed
	// as a metadata-less index row without triggering a read_error
	// Warning. If the reader starts failing fast on broken frontmatter,
	// a stderr assertion becomes meaningful — add it then.
}
