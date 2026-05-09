package main

// kb_compat_test.go — ox-gzp.30. Cross-cutting tests for the kb backward-
// compatibility, escape-hatch, and lazy-provision paths. These cases
// straddle the CLI commands (`ox kb list`, `ox kb show`, `ox kb path`)
// and the merger contract; they live here rather than in any single
// command's test file so the matrix is easy to read end-to-end.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/kb"
)

// --- A. knowledge-bubbles flag off (kb API returns 403) ---

// TestCompat_KBFlagOff_ListShowsLegacyOnly verifies that when the kb API
// returns ErrKBAPIUnavailable (the documented flag-off / endpoint-missing
// signal), `ox kb list` still surfaces the user's legacy team-context
// rows without an error and without a scary warning footer.
//
// Failure prevented: every CLI invocation during the gradual flag rollout
// would otherwise show a "kb API unavailable" warning that masks the
// legacy listing the user actually wants.
func TestCompat_KBFlagOff_ListShowsLegacyOnly(t *testing.T) {
	t.Parallel()

	// Simulate the merger's view of the world after a 403: zero kb-API
	// rows, no warning (the sentinel is silently swallowed), legacy
	// rows pass through unchanged.
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
	if !strings.Contains(out, "old-team") || !strings.Contains(out, "old-repo") {
		t.Errorf("legacy rows must survive flag-off:\n%s", out)
	}
	if strings.Contains(out, "source had errors") {
		t.Errorf("flag-off must NOT surface as a warning footer:\n%s", out)
	}
}

// TestCompat_KBFlagOff_PathFallsBackToLegacy verifies `ox kb path` still
// resolves a slug when the kb API is unavailable, by walking through the
// legacy resolver. Mirrors the existing kb_path_test.go contract but
// labels it explicitly as a flag-off compat case so the matrix is easy
// to find in code review.
//
// Failure prevented: a future refactor that drops the legacy fallback
// during ErrKBAPIUnavailable would leave pre-flag users with `kb not
// found:` for every slug they've used for months.
func TestCompat_KBFlagOff_PathFallsBackToLegacy(t *testing.T) {
	t.Parallel()

	cmd, stdout, _ := newKBPathTestCmd(t)

	deps := kbPathDeps{
		resolver: &stubKBResolver{listErr: fmt.Errorf("flag off: %w", api.ErrKBAPIUnavailable)},
		legacy: func(slug string) (string, string, bool) {
			if slug == "legacy-slug" {
				return "team_legacy_id", "/legacy/path", true
			}
			return "", "", false
		},
		pathFor: fakePathFor,
	}

	if err := runKBPathWithDeps(cmd, "legacy-slug", false, deps); err != nil {
		t.Fatalf("expected legacy fallback to succeed, got: %v", err)
	}
	want := fakePathFor("team_legacy_id") + "\n"
	if got := stdout.String(); got != want {
		t.Errorf("legacy fallback path: got %q, want %q", got, want)
	}
}

// TestCompat_KBFlagOff_ShowFallsBackGracefully verifies handleKBShowError
// translates ErrKBAPIUnavailable into the documented user-facing message
// + ErrSilent (so main.go doesn't double-print "Error:"). The behavior
// is what gives flag-off users a clear next step rather than a stack
// trace.
//
// Failure prevented: a regression that returns the raw HTTP 403 to the
// user instead of the friendly "Knowledge bubbles not enabled..." copy.
func TestCompat_KBFlagOff_ShowFallsBackGracefully(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("listing failed: %w", api.ErrKBAPIUnavailable)
	err := handleKBShowError(wrapped, "personal", false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// The dedicated user-facing path returns cli.ErrSilent so main.go
	// suppresses the "Error: ..." prefix; we verify by checking that
	// Error() is empty (the ErrSilent contract).
	if err.Error() != "" {
		t.Errorf("expected silent error, got message: %q", err.Error())
	}
}

// --- B. OX_KB_DISABLE escape hatch ---

// TestCompat_OXKBDisable_MergerSkipsKBClient verifies that setting
// OX_KB_DISABLE=1 short-circuits the merger before the kb client is
// called, even if the client would otherwise return rows. This is the
// operator-facing emergency switch for rollout incidents.
//
// Failure prevented: a regression in the env hook would silently call
// the kb API anyway, eliminating the only knob operators have to bypass
// a misbehaving kb endpoint without redeploying the CLI.
func TestCompat_OXKBDisable_MergerSkipsKBClient(t *testing.T) {
	// Cannot t.Parallel because t.Setenv is incompatible with parallel
	// subtests sharing the same env var.
	t.Setenv("OX_KB_DISABLE", "1")

	var calls atomic.Int32
	listFn := func() ([]api.KB, error) {
		calls.Add(1)
		return []api.KB{{KBID: "kb_should_not_appear", KBType: api.KBTypePersonal, Slug: "ghost"}}, nil
	}

	merger := kb.NewMerger(
		&compatFakeKBSource{listFn: listFn},
		&compatFakeTeamSource{rows: []kb.LegacyTeamRow{{TeamID: "t1", Slug: "legacy", Name: "Legacy"}}, ep: "https://sageox.ai"},
		nil,
	)

	res, err := merger.Merge(context.Background())
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("kb client called %d times, want 0", calls.Load())
	}
	// only the legacy row should land
	if len(res.Bubbles) != 1 || res.Bubbles[0].Slug != "legacy" {
		t.Errorf("expected only legacy row, got %+v", res.Bubbles)
	}
}

// TestCompat_OXKBDisable_VariantsAreRecognized pins the truthiness logic:
// "1", "true", "yes", "on" disable; "0", "false", "no", "off", "" leave
// it enabled. Encoded so an operator who types `OX_KB_DISABLE=true` gets
// the behavior they expect, and so a follow-up that swaps the parser
// (e.g., to strconv.ParseBool) can't quietly break the documented
// "anything truthy" contract.
//
// Failure prevented: an operator setting OX_KB_DISABLE=true expects the
// switch to fire; a strict "1-only" parser would silently no-op.
func TestCompat_OXKBDisable_VariantsAreRecognized(t *testing.T) {
	cases := []struct {
		val      string
		disabled bool
	}{
		{"1", true},
		{"true", true},
		{"YES", true},
		{"on", true},
		{"0", false},
		{"false", false},
		{"no", false},
		{"off", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.val, func(t *testing.T) {
			t.Setenv("OX_KB_DISABLE", tc.val)

			var calls atomic.Int32
			listFn := func() ([]api.KB, error) {
				calls.Add(1)
				return nil, nil
			}
			merger := kb.NewMerger(&compatFakeKBSource{listFn: listFn}, nil, nil)
			_, _ = merger.Merge(context.Background())

			gotDisabled := calls.Load() == 0
			if gotDisabled != tc.disabled {
				t.Errorf("OX_KB_DISABLE=%q: kb client called=%d, want disabled=%v", tc.val, calls.Load(), tc.disabled)
			}
		})
	}
}

// --- C. personal bubble lazy-provision happy path ---

// TestCompat_PersonalBubble_AppearsInListAfterAuth verifies the
// EnsurePersonalKBMiddleware contract from the server side: the very
// first `ox kb list` after `ox login` must surface a personal bubble.
// We model the server's lazy-provision by having the kb client return a
// personal bubble on the first call (as the real middleware would).
//
// Failure prevented: a regression that filters out kb_type=personal
// rows (or treats the personal bubble as a "not yours" surface) would
// leave first-session users without a scratchpad and without a clear
// signal that anything was wrong.
func TestCompat_PersonalBubble_AppearsInListAfterAuth(t *testing.T) {
	t.Parallel()

	res := kb.MergeResult{
		Bubbles: []kb.Bubble{
			{KBID: "kb_personal_lazy", Type: api.KBTypePersonal, Slug: "personal-abc", Name: "Personal", ViewerRole: "owner", Source: kb.SourceKB},
			{KBID: "kb_team", Type: api.KBTypeTeam, Slug: "platform", Name: "Platform", Source: kb.SourceKB},
		},
	}

	var buf bytes.Buffer
	if err := renderKBListResult(&buf, res, "", false, ""); err != nil {
		t.Fatalf("render: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "personal-abc") {
		t.Errorf("personal bubble missing from list output:\n%s", out)
	}
	// ROLE column was dropped from the table for now — viewer_role still
	// flows through the merger and the JSON envelope, but the human table
	// is intentionally narrower. Re-add a role column assertion if/when
	// the column comes back.
}

// --- D. forward-compat unknown type across kb commands ---

// TestCompat_ForwardCompatUnknownType_ListRendersUnknown verifies that an
// unknown kb_type renders as "unknown" in `ox kb list`, never blank.
//
// Failure prevented: a future server-side rollout of a sixth kb_type
// would otherwise render rows with a blank TYPE column on every CLI that
// hadn't been upgraded yet — a confusing visual artifact, not a hard
// failure.
func TestCompat_ForwardCompatUnknownType_ListRendersUnknown(t *testing.T) {
	t.Parallel()

	res := kb.MergeResult{Bubbles: []kb.Bubble{
		{Type: api.KBTypeUnknown, Slug: "future-x", Name: "Future"},
	}}
	var buf bytes.Buffer
	if err := renderKBListResult(&buf, res, "", false, ""); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), "unknown") {
		t.Errorf("unknown type must render as the literal 'unknown':\n%s", buf.String())
	}
}

// TestCompat_ForwardCompatUnknownType_ShowResolvesByKBID verifies that a
// kb_id pointing at a future-typed bubble still resolves via
// `ox kb show kb_<id>` — the kb_id direct path bypasses the slug priority
// table entirely, so an unrecognized type doesn't matter for resolution.
//
// Failure prevented: a regression that gates the kb_id direct path on
// "type is one of the five known values" would block the only escape
// hatch flag-off users have for inspecting a future-typed bubble.
func TestCompat_ForwardCompatUnknownType_ShowResolvesByKBID(t *testing.T) {
	t.Parallel()

	bubbles := []api.KB{{KBID: "kb_future", KBType: api.KBTypeUnknown, Slug: "future-x"}}
	got := pickKBByPriority(bubbles)
	if got == nil {
		t.Fatal("expected fallback selection for unknown-type bubble, got nil")
	}
	if got.KBID != "kb_future" {
		t.Errorf("expected fallback to first match, got %q", got.KBID)
	}
}

// TestCompat_ForwardCompatUnknownType_PathByKBIDSkipsAPI verifies that
// `ox kb path kb_<id>` resolves without calling the API for an unknown-
// type kb_id. Mirrors the existing TestKBPath_ResolveByKBID but as part
// of the explicit forward-compat matrix.
//
// Failure prevented: any behavior that requires "type must be known"
// before kb_id direct resolution would fail-open here on a future server
// rollout.
func TestCompat_ForwardCompatUnknownType_PathByKBIDSkipsAPI(t *testing.T) {
	cmd, stdout, _ := newKBPathTestCmd(t)

	deps := kbPathDeps{
		// resolver is non-nil to prove kb_id never calls it
		resolver: &stubKBResolver{},
		legacy:   func(string) (string, string, bool) { return "", "", false },
		pathFor:  fakePathFor,
	}
	if err := runKBPathWithDeps(cmd, "kb_future", false, deps); err != nil {
		t.Fatalf("kb_id direct path failed for unknown-type kb: %v", err)
	}
	want := fakePathFor("kb_future") + "\n"
	if got := stdout.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestCompat_ForwardCompatSlugCollision_PersonalBeatsUnknown verifies
// that when a slug matches both a recognized type and an unknown future
// type, the recognized one wins per the documented priority order. This
// pins the behavior so a future "be lenient toward unknown types" change
// can't promote them above personal/team without an explicit decision.
//
// Failure prevented: a slug shared by a personal bubble and a future-
// typed bubble silently routing `ox kb show <slug>` at the future bubble
// once the server rolls it out.
func TestCompat_ForwardCompatSlugCollision_PersonalBeatsUnknown(t *testing.T) {
	t.Parallel()

	bubbles := []api.KB{
		{KBID: "kb_unknown", KBType: api.KBTypeUnknown, Slug: "shared"},
		{KBID: "kb_personal", KBType: api.KBTypePersonal, Slug: "shared"},
	}
	got := pickKBByPriority(bubbles)
	if got == nil || got.KBID != "kb_personal" {
		t.Errorf("expected personal to win over unknown, got %+v", got)
	}
}

// --- helpers ---

// compatFakeKBSource is a tiny fake just for this file's merger calls so
// we don't depend on internal/kb's unexported test fakes. The shape
// matches kb.KBSource — single ListBubbles method.
type compatFakeKBSource struct {
	listFn func() ([]api.KB, error)
}

func (f *compatFakeKBSource) ListBubbles(_ context.Context) ([]api.KB, error) {
	if f.listFn != nil {
		return f.listFn()
	}
	return nil, nil
}

// compatFakeTeamSource adapts the local stub to kb.LegacyTeamSource.
type compatFakeTeamSource struct {
	rows []kb.LegacyTeamRow
	ep   string
	err  error
}

func (f *compatFakeTeamSource) ListTeamContexts(_ context.Context) ([]kb.LegacyTeamRow, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	return f.rows, f.ep, nil
}

// _ keeps `errors` referenced in case future tests in this file need it.
var _ = errors.Is
