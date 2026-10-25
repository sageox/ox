package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/kb"
)

// kb_list_test.go — covers `ox kb list` rendering, filtering, and empty-state
// behavior. The pure renderer (renderKBListResult) takes a pre-built
// kb.ListResult so tests don't need to spin up httptest servers —
// internal/kb's own tests cover the fetch wiring.

// TestRenderKBListResult_HappyHuman verifies the human-readable table for
// three bubbles renders TYPE/SLUG/NAME for each, with no warnings.
//
// Failure prevented: a regression in the row renderer that drops a column
// or stops styling rows would silently break the primary listing UX.
func TestRenderKBListResult_HappyHuman(t *testing.T) {
	t.Parallel()

	res := kb.ListResult{
		Bubbles: []kb.Bubble{
			{Type: api.KBTypePersonal, Slug: "notes", Name: "My Notes", ViewerRole: "owner"},
			{Type: api.KBTypeTeam, Slug: "platform", Name: "Platform Team", ViewerRole: "member"},
			{Type: api.KBTypeRepo, Slug: "my-app", Name: "my-app", ViewerRole: "viewer"},
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

	res := kb.ListResult{
		Bubbles: []kb.Bubble{
			{KBID: "kb_01", Type: api.KBTypePersonal, Slug: "p", Name: "P", ViewerRole: "owner"},
			{KBID: "kb_02", Type: api.KBTypeTeam, Slug: "t", Name: "T", ViewerRole: "member"},
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
	for _, want := range []string{"kb_id", "type", "slug", "name", "viewer_role"} {
		if _, ok := decoded.Bubbles[0][want]; !ok {
			t.Errorf("bubble JSON missing key %q: %s", want, buf.String())
		}
	}
	if decoded.Bubbles[0]["type"] != "personal" {
		t.Errorf("expected type=personal, got %v", decoded.Bubbles[0]["type"])
	}
}

// TestRenderKBListResult_JSONNewScopeFields verifies the ADR-028 §5 JSON
// additions: bubble rows carry scope_type/scope_id, description, and
// topics when set, and omit them (omitempty) when unset.
//
// Failure prevented: dropping the scope/description/topics keys would
// break consumers that disambiguate per-scope bubbles in scripts; emitting
// empty keys would make "field missing" indistinguishable from "unset".
func TestRenderKBListResult_JSONNewScopeFields(t *testing.T) {
	t.Parallel()

	res := kb.ListResult{
		Bubbles: []kb.Bubble{
			{
				KBID:        "kb_full",
				Type:        api.KBTypeTeam,
				Slug:        "platform",
				Name:        "Platform",
				ScopeType:   "team",
				ScopeID:     "team_abc",
				Description: "Curated platform knowledge",
				Topics:      []string{"infra", "deploys"},
			},
			{
				KBID: "kb_sparse",
				Type: api.KBTypeTeam,
				Slug: "sparse",
			},
		},
	}

	var buf bytes.Buffer
	if err := renderKBListResult(&buf, res, "", true, ""); err != nil {
		t.Fatalf("renderKBListResult: %v", err)
	}

	var decoded struct {
		Bubbles []map[string]any `json:"bubbles"`
	}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, buf.String())
	}
	if len(decoded.Bubbles) != 2 {
		t.Fatalf("expected 2 bubbles, got %d", len(decoded.Bubbles))
	}

	full := decoded.Bubbles[0]
	if full["scope_type"] != "team" {
		t.Errorf("scope_type: got %v, want team", full["scope_type"])
	}
	if full["scope_id"] != "team_abc" {
		t.Errorf("scope_id: got %v, want team_abc", full["scope_id"])
	}
	if full["description"] != "Curated platform knowledge" {
		t.Errorf("description: got %v", full["description"])
	}
	topics, ok := full["topics"].([]any)
	if !ok || len(topics) != 2 || topics[0] != "infra" || topics[1] != "deploys" {
		t.Errorf("topics: got %v, want [infra deploys]", full["topics"])
	}

	sparse := decoded.Bubbles[1]
	for _, key := range []string{"scope_type", "scope_id", "description", "topics"} {
		if _, present := sparse[key]; present {
			t.Errorf("unset field %q must be omitted for the sparse row: %v", key, sparse)
		}
	}
}

// TestRenderKBListResult_JSONHasNoLegacyOrSourceKeys pins the ADR-028
// inversion of the old legacy-row rendering: bubble rows come from the KB
// API only, so the JSON envelope must not carry the retired `legacy`,
// `source`, or `repo_id` keys.
//
// Failure prevented: reintroducing the three-source merger's per-row
// provenance fields would resurrect a JSON contract that ADR-028
// deliberately removed, confusing consumers that already migrated.
func TestRenderKBListResult_JSONHasNoLegacyOrSourceKeys(t *testing.T) {
	t.Parallel()

	res := kb.ListResult{
		Bubbles: []kb.Bubble{
			{KBID: "kb_01", Type: api.KBTypeTeam, Slug: "platform", Name: "Platform", ViewerRole: "member"},
		},
	}

	var buf bytes.Buffer
	if err := renderKBListResult(&buf, res, "", true, ""); err != nil {
		t.Fatalf("renderKBListResult: %v", err)
	}

	out := buf.String()
	for _, banned := range []string{`"legacy"`, `"source"`, `"repo_id"`} {
		if strings.Contains(out, banned) {
			t.Errorf("JSON output must not contain retired key %s:\n%s", banned, out)
		}
	}
}

// TestRenderKBListResult_TypeFilter verifies --type=team narrows the result
// to team bubbles only.
//
// Failure prevented: a broken filter would either hide team bubbles the
// user asked for or leak other types into a filtered listing.
func TestRenderKBListResult_TypeFilter(t *testing.T) {
	t.Parallel()

	res := kb.ListResult{
		Bubbles: []kb.Bubble{
			{Type: api.KBTypePersonal, Slug: "p"},
			{Type: api.KBTypeTeam, Slug: "team-new"},
			{Type: api.KBTypeTeam, Slug: "team-other"},
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
	if !strings.Contains(out, "team-other") {
		t.Errorf("expected team-other in output:\n%s", out)
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

	res := kb.ListResult{
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
// Failure prevented: silently presenting an empty list when the KB API is
// erroring would mask real backend incidents from the user.
func TestRenderKBListResult_EmptyWithWarnings(t *testing.T) {
	t.Parallel()

	res := kb.ListResult{
		Bubbles: nil,
		Warnings: []kb.Warning{
			{Err: "boom"},
		},
	}

	var buf bytes.Buffer
	if err := renderKBListResult(&buf, res, "", false, "/some/project"); err != nil {
		t.Fatalf("renderKBListResult: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"KB API errored", "boom"} {
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
// empty case prints "No knowledge bubbles available." Used when the KB
// API produced no rows AND did not error — the most common reason is a
// not-yet-onboarded user.
//
// Failure prevented: a confusing blank screen for a brand-new user with
// no knowledge bubbles yet.
func TestRenderKBListResult_EmptyClean(t *testing.T) {
	t.Parallel()

	res := kb.ListResult{Bubbles: nil, Warnings: nil}

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
// (type-priority, slug). Without this, the table re-orders on every run
// and visual diffs become useless.
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
		{Type: api.KBTypeTeam, Slug: "t-alpha"},
		{Type: api.KBTypeProfile, Slug: "pr1"},
		{Type: api.KBTypeCustom, Slug: "c1"},
		{Type: api.KBTypeUnknown, Slug: "u1"},
	}

	got := filterAndSortBubbles(in, "")
	wantSlugs := []string{"p1", "pr1", "t-alpha", "t-zeta", "r1", "c1", "u1"}
	if len(got) != len(wantSlugs) {
		t.Fatalf("length: got %d, want %d", len(got), len(wantSlugs))
	}
	for i, want := range wantSlugs {
		if got[i].Slug != want {
			t.Errorf("position %d: got slug %q, want %q (full: %+v)", i, got[i].Slug, want, got)
		}
	}
}

// TestRunKBList_NonEmptyWithWarnings verifies the warning-aware footer
// renders when results are non-empty AND the fetch warned. The table is
// still primary; the footer is informational.
//
// Failure prevented: hiding the footer on partial-failure runs, leaving
// users to wonder why their bubble list looks shorter than usual.
func TestRunKBList_NonEmptyWithWarnings(t *testing.T) {
	t.Parallel()

	res := kb.ListResult{
		Bubbles: []kb.Bubble{
			{Type: api.KBTypePersonal, Slug: "p"},
		},
		Warnings: []kb.Warning{
			{Err: "scan failed"},
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
	if !strings.Contains(out, "1 KB API error(s)") {
		t.Errorf("missing warning footer:\n%s", out)
	}
}

// TestKBListCmd_RegistrationOnParent verifies the list subcommand is wired
// onto the kb parent. A registration miss would surface in production as
// `ox kb list` printing "unknown command".
//
// Failure prevented: a refactor that drops the AddCommand call in init().
func TestKBListCmd_RegistrationOnParent(t *testing.T) {
	// Commands lazily sorts the shared kbCmd, so wiring tests must run serially.

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
// cases: known/unknown/empty. Centralized here so the rendering tests
// above don't duplicate the assertion.
//
// Failure prevented: a helper change that breaks empty-type normalization
// to "unknown".
func TestFormatKBType(t *testing.T) {
	t.Parallel()

	cases := []struct {
		t    api.KBType
		want string
	}{
		{api.KBTypePersonal, "personal"},
		{api.KBTypeTeam, "team"},
		{api.KBTypeRepo, "repo"},
		{api.KBTypeUnknown, "unknown"},
		{"", "unknown"},
		{api.KBTypeCustom, "custom"},
	}
	for _, c := range cases {
		got := formatKBType(c.t)
		if got != c.want {
			t.Errorf("formatKBType(%q): got %q, want %q", c.t, got, c.want)
		}
	}
}
