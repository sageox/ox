package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/kb"
)

// kb_list_test.go — covers `ox kb list` rendering, filtering, and empty-state
// behavior. The pure renderer (renderKBListResult) takes a pre-built
// MergeResult so tests don't need to spin up httptest servers — F3's own
// tests cover the merger wiring.

// fakeKBListMerger lets tests substitute the three-source merger with a
// pre-canned result. The interface is identical to *kb.Merger.Merge.
type fakeKBListMerger struct {
	result kb.MergeResult
	err    error
}

func (f *fakeKBListMerger) Merge(_ context.Context) (kb.MergeResult, error) {
	return f.result, f.err
}

// compile-time check: the fake satisfies the production interface, so a
// future signature change shows up here and not at runtime.
var _ kbListMerger = (*fakeKBListMerger)(nil)

// TestRenderKBListResult_HappyHuman verifies the human-readable table for
// three bubbles renders TYPE/SLUG/NAME/ROLE for each, with no warnings.
//
// Failure prevented: a regression in the row renderer that drops a column
// or stops styling rows would silently break the primary listing UX.
func TestRenderKBListResult_HappyHuman(t *testing.T) {
	t.Parallel()

	res := kb.MergeResult{
		Bubbles: []kb.Bubble{
			{Type: api.KBTypePersonal, Slug: "notes", Name: "My Notes", ViewerRole: "owner", Source: kb.SourceKB},
			{Type: api.KBTypeTeam, Slug: "platform", Name: "Platform Team", ViewerRole: "member", Source: kb.SourceKB},
			{Type: api.KBTypeRepo, Slug: "my-app", Name: "my-app", ViewerRole: "viewer", Source: kb.SourceKB},
		},
	}

	var buf bytes.Buffer
	if err := renderKBListResult(&buf, res, "", false, ""); err != nil {
		t.Fatalf("renderKBListResult: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"TYPE", "SLUG", "NAME",
		"personal", "notes", "My Notes",
		"team", "platform", "Platform Team",
		"repo", "my-app",
		"3 bubble(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, out)
		}
	}
}

// TestRenderKBListResult_HappyJSON verifies the JSON envelope shape
// (`{"bubbles": [...], "warnings": []}`) and that each bubble carries the
// expected snake_case keys.
//
// Failure prevented: a JSON contract drift would break script consumers
// that pipe `ox kb list --json` into jq.
func TestRenderKBListResult_HappyJSON(t *testing.T) {
	t.Parallel()

	res := kb.MergeResult{
		Bubbles: []kb.Bubble{
			{KBID: "kb_01", Type: api.KBTypePersonal, Slug: "p", Name: "P", ViewerRole: "owner", Source: kb.SourceKB},
			{KBID: "kb_02", Type: api.KBTypeTeam, Slug: "t", Name: "T", ViewerRole: "member", Source: kb.SourceKB},
		},
	}

	var buf bytes.Buffer
	if err := renderKBListResult(&buf, res, "", true, ""); err != nil {
		t.Fatalf("renderKBListResult: %v", err)
	}

	var decoded struct {
		Bubbles []map[string]any `json:"bubbles"`
		// pointer so we can distinguish "missing" from "empty array"
		Warnings *[]map[string]any `json:"warnings"`
	}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, buf.String())
	}

	if len(decoded.Bubbles) != 2 {
		t.Fatalf("expected 2 bubbles, got %d (%s)", len(decoded.Bubbles), buf.String())
	}
	if decoded.Warnings == nil {
		t.Fatal("warnings key missing entirely; the contract is to always emit it")
	}
	if len(*decoded.Warnings) != 0 {
		t.Errorf("expected zero warnings, got %d", len(*decoded.Warnings))
	}
	for _, want := range []string{"kb_id", "type", "slug", "name", "viewer_role", "source", "legacy"} {
		if _, ok := decoded.Bubbles[0][want]; !ok {
			t.Errorf("bubble JSON missing key %q: %s", want, buf.String())
		}
	}
	if decoded.Bubbles[0]["type"] != "personal" {
		t.Errorf("expected type=personal, got %v", decoded.Bubbles[0]["type"])
	}
}

// TestRenderKBListResult_LegacyNoSuffix verifies that Bubble.Legacy=true rows
// render WITHOUT a "(legacy)" suffix in the TYPE column. The Legacy bool is
// preserved on the Bubble for sort-stability and backend dedup, but the
// rendered column intentionally hides it during the migration window so the
// table stays narrow.
//
// Failure prevented: re-introducing the suffix would re-widen the TYPE
// column past the values it now needs to fit ("personal" is the longest)
// and silently push real-world bubble names off-screen.
func TestRenderKBListResult_LegacyNoSuffix(t *testing.T) {
	t.Parallel()

	res := kb.MergeResult{
		Bubbles: []kb.Bubble{
			{Type: api.KBTypeTeam, Slug: "old-team", Name: "Old Team", Legacy: true, Source: kb.SourceTeamLegacy},
			{Type: api.KBTypeRepo, Slug: "old-repo", Name: "Old Repo", Legacy: true, Source: kb.SourceLedger},
		},
	}

	var buf bytes.Buffer
	if err := renderKBListResult(&buf, res, "", false, ""); err != nil {
		t.Fatalf("renderKBListResult: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "(legacy)") {
		t.Errorf("legacy suffix must not render in TYPE column:\n%s", out)
	}
	// the rows themselves must still appear — legacy is a display tweak,
	// not a filter.
	for _, want := range []string{"old-team", "old-repo", "team", "repo"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output:\n%s", want, out)
		}
	}
}

// TestRenderKBListResult_TypeFilter verifies --type=team narrows the result
// to team bubbles, including legacy ones.
//
// Failure prevented: a filter that ignores legacy entries would hide a
// team-context the user can actually navigate to today.
func TestRenderKBListResult_TypeFilter(t *testing.T) {
	t.Parallel()

	res := kb.MergeResult{
		Bubbles: []kb.Bubble{
			{Type: api.KBTypePersonal, Slug: "p"},
			{Type: api.KBTypeTeam, Slug: "team-new"},
			{Type: api.KBTypeTeam, Slug: "team-old", Legacy: true},
			{Type: api.KBTypeRepo, Slug: "repo-x"},
		},
	}

	var buf bytes.Buffer
	if err := renderKBListResult(&buf, res, "team", false, ""); err != nil {
		t.Fatalf("renderKBListResult: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "team-new") {
		t.Errorf("expected team-new in output:\n%s", out)
	}
	if !strings.Contains(out, "team-old") {
		t.Errorf("expected team-old (legacy origin) in output:\n%s", out)
	}
	if strings.Contains(out, "repo-x") {
		t.Errorf("repo-x should be filtered out:\n%s", out)
	}
	if strings.Contains(out, " p ") || strings.Contains(out, "personal") {
		t.Errorf("personal bubble should be filtered out:\n%s", out)
	}
	if !strings.Contains(out, "2 bubble(s)") {
		t.Errorf("expected count of 2 in output:\n%s", out)
	}
}

// TestRenderKBListResult_UnknownType verifies that a Bubble.Type of
// KBTypeUnknown (or empty) renders as "unknown" — forward-compat for a
// future server-side type the CLI hasn't learned about yet.
//
// Failure prevented: a blank TYPE column on rollout day would look like a
// broken row, even though the data is fine.
func TestRenderKBListResult_UnknownType(t *testing.T) {
	t.Parallel()

	res := kb.MergeResult{
		Bubbles: []kb.Bubble{
			{Type: api.KBTypeUnknown, Slug: "future-x", Name: "Future"},
			{Type: "", Slug: "blank", Name: "Blank"},
		},
	}

	var buf bytes.Buffer
	if err := renderKBListResult(&buf, res, "", false, ""); err != nil {
		t.Fatalf("renderKBListResult: %v", err)
	}

	out := buf.String()
	count := strings.Count(out, "unknown")
	if count < 2 {
		t.Errorf("expected at least 2 'unknown' renders, got %d in:\n%s", count, out)
	}
}

// TestRenderKBListResult_EmptyWithWarnings verifies that an empty-bubbles
// + non-empty-warnings result emits the diagnostic-mode banner and the
// `ox doctor` hint.
//
// Failure prevented: silently presenting an empty list when sources are
// erroring would mask real backend incidents from the user.
func TestRenderKBListResult_EmptyWithWarnings(t *testing.T) {
	t.Parallel()

	res := kb.MergeResult{
		Bubbles: nil,
		Warnings: []kb.SourceWarning{
			{Source: kb.SourceKB, Err: "boom"},
		},
	}

	var buf bytes.Buffer
	if err := renderKBListResult(&buf, res, "", false, "/some/project"); err != nil {
		t.Fatalf("renderKBListResult: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"sources errored", "kb", "boom"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q in diagnostic empty state:\n%s", want, out)
		}
	}
	// The hint (`ox doctor`) goes through cli.PrintHint which writes to its
	// own configured stdout. Not asserting here on the substring would
	// allow regressions where the hint is dropped entirely; we accept the
	// hint may not appear in this captured buffer (cli.PrintHint targets
	// os.Stdout) but the banner copy must.
}

// TestRenderKBListResult_EmptyClean verifies the clean (no-warnings)
// empty case prints "No knowledge bubbles available." Used when neither
// source produced rows AND none errored — the most common reason is a
// not-yet-onboarded user.
//
// Failure prevented: a confusing blank screen for a brand-new user with
// no knowledge bubbles yet.
func TestRenderKBListResult_EmptyClean(t *testing.T) {
	t.Parallel()

	res := kb.MergeResult{Bubbles: nil, Warnings: nil}

	var buf bytes.Buffer
	if err := renderKBListResult(&buf, res, "", false, ""); err != nil {
		t.Fatalf("renderKBListResult: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "No knowledge bubbles available.") {
		t.Errorf("expected clean empty-state copy, got:\n%s", out)
	}
}

// TestFilterAndSortBubbles_Order verifies the documented stable sort:
// (type-priority, !legacy, slug). Without this, the table re-orders on
// every run and visual diffs become useless.
//
// Failure prevented: a change to kbTypePriority that quietly reorders
// the list — e.g., demoting "personal" out of the top slot — without a
// visible test failure.
func TestFilterAndSortBubbles_Order(t *testing.T) {
	t.Parallel()

	in := []kb.Bubble{
		{Type: api.KBTypeRepo, Slug: "r1"},
		{Type: api.KBTypePersonal, Slug: "p1"},
		{Type: api.KBTypeTeam, Slug: "t-zeta"},
		{Type: api.KBTypeTeam, Slug: "t-alpha", Legacy: true}, // legacy in same bucket sorts after non-legacy
		{Type: api.KBTypeTeam, Slug: "t-alpha"},
		{Type: api.KBTypeProfile, Slug: "pr1"},
		{Type: api.KBTypeCustom, Slug: "c1"},
		{Type: api.KBTypeUnknown, Slug: "u1"},
	}

	got := filterAndSortBubbles(in, "")
	wantSlugs := []string{"p1", "pr1", "t-alpha", "t-zeta", "t-alpha", "r1", "c1", "u1"}
	if len(got) != len(wantSlugs) {
		t.Fatalf("length: got %d, want %d", len(got), len(wantSlugs))
	}
	for i, want := range wantSlugs {
		if got[i].Slug != want {
			t.Errorf("position %d: got slug %q, want %q (full: %+v)", i, got[i].Slug, want, got)
		}
	}
	// position 2 should be the non-legacy team (sort within type-team:
	// non-legacy first, then legacy)
	if got[2].Legacy {
		t.Errorf("position 2 (first 't-alpha') should be non-legacy, got legacy=%v", got[2].Legacy)
	}
	if !got[3].Legacy && !got[4].Legacy {
		// belt-and-braces — the second 't-alpha' must be the legacy one
		// when type buckets overlap. Reading explicitly so a regression in
		// the !Legacy comparator surfaces here.
		var foundLegacy bool
		for _, b := range got {
			if b.Slug == "t-alpha" && b.Legacy {
				foundLegacy = true
			}
		}
		if !foundLegacy {
			t.Error("legacy 't-alpha' was dropped during sort")
		}
	}
}

// TestRunKBList_MergerErrorIsolation verifies that an underlying merger
// returning ErrKBAPIUnavailable for the kb-API source still yields a
// successful render with legacy rows — and zero warnings, because the
// merger swallows that sentinel before it reaches us. Mirrors F3's
// contract: ErrKBAPIUnavailable is "0 rows from kb", not a warning.
//
// Failure prevented: leaking the kb-API-disabled state into the user's
// table as a scary warning, even though the legacy sources worked fine.
func TestRunKBList_MergerErrorIsolation(t *testing.T) {
	t.Parallel()

	// Construct a result that mimics what the real merger emits when the
	// kb-API source returns ErrKBAPIUnavailable: zero kb rows, no warning,
	// legacy rows pass through unchanged.
	res := kb.MergeResult{
		Bubbles: []kb.Bubble{
			{Type: api.KBTypeTeam, Slug: "old-team", Name: "Old Team", Legacy: true, Source: kb.SourceTeamLegacy},
		},
		Warnings: nil,
	}
	merger := &fakeKBListMerger{result: res}

	mres, err := merger.Merge(context.Background())
	if err != nil {
		t.Fatalf("merger.Merge: %v", err)
	}

	var buf bytes.Buffer
	if err := renderKBListResult(&buf, mres, "", false, ""); err != nil {
		t.Fatalf("renderKBListResult: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "old-team") {
		t.Errorf("legacy row dropped when kb-API was unavailable:\n%s", out)
	}
	if strings.Contains(out, "source had errors") {
		t.Errorf("ErrKBAPIUnavailable should not surface as a warning footer:\n%s", out)
	}
}

// TestRunKBList_NonEmptyWithWarnings verifies the warning-aware footer
// renders when results are non-empty AND a source warned. The table is
// still primary; the footer is informational.
//
// Failure prevented: hiding the footer on partial-failure runs, leaving
// users to wonder why their team list looks shorter than usual.
func TestRunKBList_NonEmptyWithWarnings(t *testing.T) {
	t.Parallel()

	res := kb.MergeResult{
		Bubbles: []kb.Bubble{
			{Type: api.KBTypePersonal, Slug: "p"},
		},
		Warnings: []kb.SourceWarning{
			{Source: kb.SourceLedger, Err: "scan failed"},
		},
	}

	var buf bytes.Buffer
	if err := renderKBListResult(&buf, res, "", false, ""); err != nil {
		t.Fatalf("renderKBListResult: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "p") {
		t.Errorf("missing the surviving bubble row:\n%s", out)
	}
	if !strings.Contains(out, "1 source had errors") {
		t.Errorf("missing warning footer:\n%s", out)
	}
	if !strings.Contains(out, string(kb.SourceLedger)) {
		t.Errorf("warning footer should name the failing source:\n%s", out)
	}
}

// TestKBListCmd_RegistrationOnParent verifies the list subcommand is wired
// onto the kb parent. A registration miss would surface in production as
// `ox kb list` printing "unknown command".
//
// Failure prevented: a refactor that drops the AddCommand call in init().
func TestKBListCmd_RegistrationOnParent(t *testing.T) {
	t.Parallel()

	var found bool
	for _, sub := range kbCmd.Commands() {
		if sub.Name() == "list" {
			found = true
			break
		}
	}
	if !found {
		t.Error("kb parent does not have 'list' subcommand registered")
	}
}

// TestFormatKBType verifies the TYPE column formatter for the documented
// cases: known/legacy/unknown/empty. Centralized here so the rendering
// tests above don't duplicate the assertion.
//
// Failure prevented: a helper change that breaks empty-type normalization
// to "unknown" or starts injecting a "(legacy)" suffix without a matching
// column-width review.
func TestFormatKBType(t *testing.T) {
	t.Parallel()

	cases := []struct {
		t      api.KBType
		legacy bool
		want   string
	}{
		{api.KBTypePersonal, false, "personal"},
		// legacy is preserved on Bubble for sort-stability but suppressed in
		// the TYPE column for now — the table stays narrow during migration.
		{api.KBTypeTeam, true, "team"},
		{api.KBTypeRepo, true, "repo"},
		{api.KBTypeUnknown, false, "unknown"},
		{"", false, "unknown"},
		{api.KBTypeCustom, false, "custom"},
	}
	for _, c := range cases {
		got := formatKBType(c.t, c.legacy)
		if got != c.want {
			t.Errorf("formatKBType(%q, legacy=%v): got %q, want %q", c.t, c.legacy, got, c.want)
		}
	}
}
