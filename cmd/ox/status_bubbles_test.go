package main

// status_bubbles_test.go — coverage for the `ox status` knowledge-bubbles
// summary line. Tests the two pure functions that produce the user-visible
// strings (formatBubblesLine, summarizeBubbles), the JSON envelope
// construction, and the statusBubblesFetch seam. Bubble rows come from the
// KB API only (ox ADR-028) — team contexts and ledgers have their own
// status sections and never fold into this count.

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFormatBubblesLine_MixedTypes verifies the canonical example from the
// plan: total + per-type breakdown in the documented order.
//
// Failure prevented: per-type counts render in the wrong order or under
// the wrong total, drifting the user-visible format away from the spec
// other docs and skills will quote.
func TestFormatBubblesLine_MixedTypes(t *testing.T) {
	t.Parallel()

	s := statusBubblesSummary{
		Total: 5,
		ByType: map[string]int{
			"personal": 1,
			"profile":  1,
			"team":     2,
			"repo":     1,
		},
	}

	got := formatBubblesLine(s)
	want := "Knowledge bubbles: 5 (1 personal, 1 profile, 2 team, 1 repo)"
	assert.Equal(t, want, got)
}

// TestFormatBubblesLine_Empty verifies the zero-bubble shape: no parens,
// no per-type breakdown.
//
// Failure prevented: an empty list rendering as "Knowledge bubbles: 0 ()"
// or hiding the line entirely — the first leaves dangling parens, the
// second confuses users who expect to see the field at all times.
func TestFormatBubblesLine_Empty(t *testing.T) {
	t.Parallel()

	s := statusBubblesSummary{Total: 0, ByType: map[string]int{}}
	assert.Equal(t, "Knowledge bubbles: 0", formatBubblesLine(s))
}

// TestFormatBubblesLine_AllOneType verifies the line collapses to a single
// breakdown segment when only one bucket has entries.
//
// Failure prevented: the renderer pads zero buckets ("3 team, 0 personal")
// instead of skipping them, making the line longer than the plan spec.
func TestFormatBubblesLine_AllOneType(t *testing.T) {
	t.Parallel()

	s := statusBubblesSummary{
		Total:  3,
		ByType: map[string]int{"team": 3},
	}
	assert.Equal(t, "Knowledge bubbles: 3 (3 team)", formatBubblesLine(s))
}

// TestFormatBubblesLine_SkipsZeroBuckets verifies that a per-type map
// containing explicit zero entries does NOT contribute a "0 X" segment.
// summarizeBubbles drops zeros, but renderers must also defend against
// callers that pre-populate zero buckets.
//
// Failure prevented: a future caller stores all known types up front
// (with zeros for unused buckets) and the line bloats to "1 personal,
// 0 profile, 2 team" — exactly the pre-collapse format the plan
// rejected.
func TestFormatBubblesLine_SkipsZeroBuckets(t *testing.T) {
	t.Parallel()

	s := statusBubblesSummary{
		Total: 3,
		ByType: map[string]int{
			"personal": 1,
			"profile":  0, // must be skipped, not rendered as "0 profile"
			"team":     2,
		},
	}
	got := formatBubblesLine(s)
	assert.Equal(t, "Knowledge bubbles: 3 (1 personal, 2 team)", got)
	assert.NotContains(t, got, "profile")
}

// TestFormatBubblesLine_Unavailable verifies the fetch-unavailable fallback
// renders without per-type breakdown so the rest of `ox status` can still
// surround it.
//
// Failure prevented: a transient KB fetch failure breaks the entire status
// command instead of degrading to a single "(unavailable)" cell.
func TestFormatBubblesLine_Unavailable(t *testing.T) {
	t.Parallel()

	s := statusBubblesSummary{Unavailable: true}
	assert.Equal(t, "Knowledge bubbles: (unavailable)", formatBubblesLine(s))
}

// TestFormatBubblesLine_ForwardCompatType verifies an unknown server-side
// type slug still renders rather than being silently dropped from the
// breakdown.
//
// Failure prevented: server rolls out a sixth type before the CLI knows
// about it; the row counts toward Total but vanishes from the breakdown,
// confusing users about why the numbers don't add up.
func TestFormatBubblesLine_ForwardCompatType(t *testing.T) {
	t.Parallel()

	s := statusBubblesSummary{
		Total: 2,
		ByType: map[string]int{
			"team":    1,
			"unknown": 1,
		},
	}
	got := formatBubblesLine(s)
	assert.Equal(t, "Knowledge bubbles: 2 (1 team, 1 unknown)", got)
}

// TestSummarizeBubbles_Counts verifies the fetch-result-to-counts mapping
// buckets each bubble under its kb_type slug, with the total matching the
// row count.
//
// Failure prevented: rows vanishing from the total or landing in the
// wrong bucket after a fetch refactor.
func TestSummarizeBubbles_Counts(t *testing.T) {
	t.Parallel()

	res := kb.ListResult{
		Bubbles: []kb.Bubble{
			{Type: api.KBTypePersonal},
			{Type: api.KBTypeTeam},
			{Type: api.KBTypeTeam},
			{Type: api.KBTypeRepo},
		},
	}
	s := summarizeBubbles(res)
	assert.Equal(t, 4, s.Total)
	assert.Equal(t, 1, s.ByType["personal"])
	assert.Equal(t, 2, s.ByType["team"])
	assert.Equal(t, 1, s.ByType["repo"])
}

// TestSummarizeBubbles_EmptyOrUnknownTypeCollapses verifies forward-compat:
// rows with an empty Type field bucket as "unknown" rather than crashing
// or vanishing from the total.
//
// Failure prevented: a malformed server response with an empty kb_type
// produces silently-dropped rows or a "" key in the output JSON.
func TestSummarizeBubbles_EmptyOrUnknownTypeCollapses(t *testing.T) {
	t.Parallel()

	res := kb.ListResult{
		Bubbles: []kb.Bubble{
			{Type: ""},                   // collapses to unknown
			{Type: api.KBTypeUnknown},    // already unknown
			{Type: api.KBType("future")}, // a future type — kept as-is
		},
	}
	s := summarizeBubbles(res)
	assert.Equal(t, 3, s.Total)
	assert.Equal(t, 2, s.ByType["unknown"])
	assert.Equal(t, 1, s.ByType["future"])
	_, hasEmpty := s.ByType[""]
	assert.False(t, hasEmpty, "empty type must not appear as a literal '' key")
}

// TestSummarizeBubbles_PassesWarnings verifies fetch warnings flow
// through the summary unmodified so renderers can decide what to show.
//
// Failure prevented: the renderer never receives the warnings slice and
// silently swallows partial-failure notifications the user needs to act
// on.
func TestSummarizeBubbles_PassesWarnings(t *testing.T) {
	t.Parallel()

	res := kb.ListResult{
		Warnings: []kb.Warning{
			{Err: "boom"},
		},
	}
	s := summarizeBubbles(res)
	require.Len(t, s.Warnings, 1)
	assert.Equal(t, "boom", s.Warnings[0].Err)
}

// TestRenderBubblesLine_AppendsWarningHint verifies the human renderer
// appends "(warnings: see ox doctor)" when the fetch flagged any
// warnings, but suppresses it for the unavailable case (which already
// shows its own muted message).
//
// Failure prevented: warnings are silently dropped from human output
// (users don't know to run `ox doctor`), or the unavailable case
// double-warns confusingly.
func TestRenderBubblesLine_AppendsWarningHint(t *testing.T) {
	t.Parallel()

	withWarn := statusBubblesSummary{
		Total:  1,
		ByType: map[string]int{"team": 1},
		Warnings: []kb.Warning{
			{Err: "boom"},
		},
	}
	got := renderBubblesLine(withWarn)
	assert.Contains(t, got, "warnings: see ox doctor",
		"warnings hint must appear when the fetch reports errors")

	unavail := statusBubblesSummary{
		Unavailable: true,
		Warnings: []kb.Warning{
			{Err: "boom"},
		},
	}
	got = renderBubblesLine(unavail)
	assert.NotContains(t, got, "warnings: see ox doctor",
		"unavailable case has its own messaging — must not double-warn")
}

// TestBuildBubblesJSON_PopulatesByType verifies the JSON envelope mirrors
// the human output: total + by_type map keyed by type slug.
//
// Failure prevented: scriptable consumers parsing the JSON see a missing
// or differently-shaped bubbles field after a fetch refactor.
func TestBuildBubblesJSON_PopulatesByType(t *testing.T) {
	t.Parallel()

	s := statusBubblesSummary{
		Total: 5,
		ByType: map[string]int{
			"personal": 1,
			"profile":  1,
			"team":     2,
			"repo":     1,
		},
	}
	js := buildBubblesJSON(s)
	require.NotNil(t, js)
	assert.Equal(t, 5, js.Total)
	assert.Equal(t, 1, js.ByType["personal"])
	assert.Equal(t, 1, js.ByType["profile"])
	assert.Equal(t, 2, js.ByType["team"])
	assert.Equal(t, 1, js.ByType["repo"])
	assert.Empty(t, js.Warnings)
}

// TestBuildBubblesJSON_Unavailable verifies the unavailable case surfaces
// a synthetic warning rather than omitting the field entirely. JSON
// consumers should see "the fetch ran but produced nothing", not a
// silently-missing key.
//
// Failure prevented: bubbles field is absent on fetch error, leaving
// callers unable to distinguish "no bubbles" from "ox can't tell you
// right now".
func TestBuildBubblesJSON_Unavailable(t *testing.T) {
	t.Parallel()

	s := statusBubblesSummary{Unavailable: true}
	js := buildBubblesJSON(s)
	require.NotNil(t, js)
	assert.Equal(t, 0, js.Total)
	require.Len(t, js.Warnings, 1)
	assert.Equal(t, "kb fetch unavailable", js.Warnings[0].Error)
}

// TestBuildStatusJSON_BubblesAndStoresCoexist verifies bubbles is populated
// alongside the permanent ledger / team_contexts fields, not in place of
// them — conversation stores are first-class status output under ADR-028,
// never folded into the bubbles count.
//
// Failure prevented: a regression that drops Ledger or TeamContexts from
// the JSON output the moment Bubbles is populated, hiding the
// conversation-store sections from JSON consumers.
func TestBuildStatusJSON_BubblesAndStoresCoexist(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: tmpDir},
	}

	bubbles := statusBubblesSummary{
		Total:  3,
		ByType: map[string]int{"team": 2, "repo": 1},
	}

	output := buildStatusJSON(
		false, nil, nil, "test.sageox.ai", "/tmp/auth.json", false,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", false,
		localCfg, "", nil, nil,
		nil, nil,
		bubbles,
	)

	require.NotNil(t, output.Bubbles, "bubbles section must be populated")
	assert.Equal(t, 3, output.Bubbles.Total)
	assert.Equal(t, 2, output.Bubbles.ByType["team"])

	require.NotNil(t, output.Ledger, "ledger section must remain populated — conversation stores are permanent status output")
	assert.True(t, output.Ledger.Configured)
}

// TestCollectBubblesSummary_UsesFetchResult verifies the statusBubblesFetch
// seam: the summary reflects exactly what the injected fetch returned.
//
// Failure prevented: the status renderer bypassing the seam (or dropping
// the fetch result) would leave `ox status` disagreeing with `ox kb list`.
func TestCollectBubblesSummary_UsesFetchResult(t *testing.T) {
	t.Parallel()

	fetch := func(_ context.Context) kb.ListResult {
		return kb.ListResult{
			Bubbles: []kb.Bubble{
				{Type: api.KBTypeTeam},
				{Type: api.KBTypeRepo},
			},
			Warnings: []kb.Warning{{Err: "partial"}},
		}
	}
	s := collectBubblesSummary(fetch)
	assert.False(t, s.Unavailable)
	assert.Equal(t, 2, s.Total)
	assert.Equal(t, 1, s.ByType["team"])
	assert.Equal(t, 1, s.ByType["repo"])
	require.Len(t, s.Warnings, 1)
	assert.Equal(t, "partial", s.Warnings[0].Err)
}

// TestCollectBubblesSummary_NilFetchIsUnavailable verifies the defensive
// nil-fetch path. Tests don't need to pass real plumbing.
//
// Failure prevented: a nil-pointer panic in the rare path where the
// fetch constructor returns nil (e.g., during early-init).
func TestCollectBubblesSummary_NilFetchIsUnavailable(t *testing.T) {
	t.Parallel()

	s := collectBubblesSummary(nil)
	assert.True(t, s.Unavailable)
}

// TestRenderBubblesLine_FormatStringMatchesPlan verifies the rendered
// human line — once styling is stripped — matches the literal shape
// quoted in the plan and other docs. Anchors the format string so
// downstream skills/agents can pattern-match it.
//
// Failure prevented: a styling refactor sneaks a stray space or
// punctuation change into the line, breaking grep-based skills.
func TestRenderBubblesLine_FormatStringMatchesPlan(t *testing.T) {
	t.Parallel()

	s := statusBubblesSummary{
		Total: 5,
		ByType: map[string]int{
			"personal": 1,
			"profile":  1,
			"team":     2,
			"repo":     1,
		},
	}
	got := renderBubblesLine(s)
	clean := stripANSIBubbles(got)
	assert.Contains(t, clean, "Knowledge bubbles")
	assert.Contains(t, clean, "5 (1 personal, 1 profile, 2 team, 1 repo)")
}

// stripANSIBubbles removes ANSI escape sequences so tests assert on textual
// content, not styling. lipgloss may emit colors when the test harness
// detects a TTY-like env.
func stripANSIBubbles(s string) string {
	for {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "m")
		if j < 0 {
			return s
		}
		s = s[:i] + s[i+j+1:]
	}
}

// TestRenderBubbleStatus covers each status cell variant and that the freshness
// age (not the word "synced") is the clean-repo signal.
func TestRenderBubbleStatus(t *testing.T) {
	t.Parallel()

	clean := stripANSIBubbles(renderBubbleStatus(
		gitRepoStatus{Exists: true, HasLastSync: true, LastSync: time.Now().Add(-3 * 24 * time.Hour)}, true, false))
	assert.Equal(t, "✓ 3d", clean)

	dirty := stripANSIBubbles(renderBubbleStatus(gitRepoStatus{Exists: true, UncommittedCount: 18}, true, false))
	assert.Equal(t, "⚠ 18 uncommitted", dirty)

	wedged := stripANSIBubbles(renderBubbleStatus(gitRepoStatus{Exists: true, RebaseInProgress: true}, true, false))
	assert.Equal(t, "⚠ rebase wedged", wedged)

	// the other IsWedged() branch: diverged (both ahead and behind)
	diverged := stripANSIBubbles(renderBubbleStatus(gitRepoStatus{Exists: true, AheadCount: 2, BehindCount: 1}, true, false))
	assert.Equal(t, "⚠ diverged", diverged)

	notCloned := stripANSIBubbles(renderBubbleStatus(gitRepoStatus{}, false, false))
	assert.Equal(t, "⚠ not cloned", notCloned)
}

// fakeKBStore redirects statusKBDirForBubble at a temp dir for the duration
// of the test and returns it. Tests using it must NOT be parallel — the seam
// is a package global.
func fakeKBStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := statusKBDirForBubble
	statusKBDirForBubble = func(kbID string) string {
		return filepath.Join(dir, kbID)
	}
	t.Cleanup(func() { statusKBDirForBubble = orig })
	return dir
}

// gitInitKB creates a real (empty) git repo at dir/kbID so getGitRepoStatus
// exercises the actual clone-detection path, not a stubbed .git marker.
func gitInitKB(t *testing.T, dir, kbID string) string {
	t.Helper()
	path := filepath.Join(dir, kbID)
	cmd := exec.Command("git", "init", "-q", path)
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	return path
}

// TestCollectBubbleRows_PathAndSortOrder verifies each row resolves its
// canonical checkout path from the kb_id, detects clone state from a real
// git repo, and that personal-scope rows sort ahead of team-scope ones.
//
// Failure prevented: the section lists a team bubble first (burying the
// user's own bubbles), points Path at the wrong directory, or reports a
// cloned bubble as missing after a path-resolution refactor.
func TestCollectBubbleRows_PathAndSortOrder(t *testing.T) {
	store := fakeKBStore(t)
	clonedPath := gitInitKB(t, store, "kb_team")

	s := summarizeBubbles(kb.ListResult{
		Bubbles: []kb.Bubble{
			{KBID: "kb_team", Type: api.KBTypeTeam, Slug: "eng", ScopeType: "team"},
			{KBID: "kb_me", Type: api.KBTypePersonal, Slug: "me", ScopeType: "user"},
		},
	})
	rows := collectBubbleRows(s, nil)
	require.Len(t, rows, 2)

	assert.Equal(t, "kb_me", rows[0].Bubble.KBID, "personal-scope bubble must sort first")
	assert.False(t, rows[0].Cloned)
	assert.Equal(t, filepath.Join(store, "kb_me"), rows[0].Path)

	assert.Equal(t, "kb_team", rows[1].Bubble.KBID)
	assert.True(t, rows[1].Cloned, "a real git checkout must be detected as cloned")
	assert.Equal(t, clonedPath, rows[1].Path)
}

// TestRenderBubblesSection_CardsShowPathAndStatus verifies the human
// output: the summary line stays, and each bubble gets a card with name,
// type + slug, mount path, and a sync-status cell — with the doctor hint
// and remote URL on not-cloned rows.
//
// Failure prevented: the section regresses to the count-only line, or a
// not-cloned bubble renders without the repair path users need.
func TestRenderBubblesSection_CardsShowPathAndStatus(t *testing.T) {
	store := fakeKBStore(t)
	gitInitKB(t, store, "kb_team")

	s := summarizeBubbles(kb.ListResult{
		Bubbles: []kb.Bubble{
			{KBID: "kb_team", Type: api.KBTypeTeam, Slug: "eng", Name: "Engineering Bubble", ScopeType: "team"},
			{KBID: "kb_me", Type: api.KBTypePersonal, Slug: "me", Name: "My Bubble", ScopeType: "user",
				RepoURL: "https://git.sageox.ai/kb/kb_me.git"},
		},
	})
	clean := stripANSIBubbles(renderBubblesSection(s, nil))

	assert.Contains(t, clean, "Knowledge bubbles", "summary line must survive the section expansion")
	assert.Contains(t, clean, "Engineering Bubble")
	assert.Contains(t, clean, "#eng")
	assert.Contains(t, clean, filepath.Join(store, "kb_team"))
	assert.Contains(t, clean, "✓", "cloned clean bubble must show a success cell")

	assert.Contains(t, clean, "My Bubble")
	assert.Contains(t, clean, "⚠ not cloned")
	assert.Contains(t, clean, "https://git.sageox.ai/kb/kb_me.git")
	assert.Contains(t, clean, "Run 'ox doctor --fix' to clone")

	meIdx := strings.Index(clean, "My Bubble")
	engIdx := strings.Index(clean, "Engineering Bubble")
	assert.Less(t, meIdx, engIdx, "personal-scope card must render before the team-scope card")
}

// TestRenderBubblesSection_ProvisioningSuppressesCloneHint verifies that
// unmounted bubbles still being provisioned (or whose provisioning
// failed) do NOT show the "Run 'ox doctor --fix' to clone" hint — there
// is no repo to clone yet, so the hint would send the user (and doctor)
// after a repo that does not exist.
//
// Failure prevented: a provisioning bubble renders the misleading clone
// hint; the user runs doctor, the clone fails, and the failure looks
// like an ox bug instead of an in-flight server operation.
func TestRenderBubblesSection_ProvisioningSuppressesCloneHint(t *testing.T) {
	fakeKBStore(t)

	s := summarizeBubbles(kb.ListResult{
		Bubbles: []kb.Bubble{
			{KBID: "kb_prov", Type: api.KBTypeTeam, Slug: "prov", Name: "Provisioning Bubble",
				ScopeType: "team", LifecycleState: "provisioning"},
			{KBID: "kb_dead", Type: api.KBTypeTeam, Slug: "dead", Name: "Failed Bubble",
				ScopeType: "team", LifecycleState: "provision-failed"},
		},
	})
	clean := stripANSIBubbles(renderBubblesSection(s, nil))

	assert.Contains(t, clean, "⟳ provisioning")
	assert.Contains(t, clean, "✗ provisioning failed")
	assert.NotContains(t, clean, "Run 'ox doctor --fix' to clone",
		"clone hint must be suppressed while there is no repo to clone")
	assert.NotContains(t, clean, "not cloned")
}

// TestBuildBubblesJSON_LifecycleSyncStatus verifies the JSON sync_status
// distinguishes provisioning states from a plain missing clone.
//
// Failure prevented: scriptable consumers treat an in-flight provisioning
// bubble as a broken mount and trigger spurious repair automation.
func TestBuildBubblesJSON_LifecycleSyncStatus(t *testing.T) {
	fakeKBStore(t)

	s := summarizeBubbles(kb.ListResult{
		Bubbles: []kb.Bubble{
			{KBID: "kb_prov", Type: api.KBTypeTeam, Slug: "prov", ScopeType: "team", LifecycleState: "provisioning"},
			{KBID: "kb_dead", Type: api.KBTypeTeam, Slug: "dead", ScopeType: "team", LifecycleState: "provision-failed"},
			{KBID: "kb_gone", Type: api.KBTypeTeam, Slug: "gone", ScopeType: "team", LifecycleState: "active"},
		},
	})
	js := buildBubblesJSON(s)
	require.NotNil(t, js)
	require.Len(t, js.Bubbles, 3)

	byID := map[string]string{}
	for _, b := range js.Bubbles {
		byID[b.KBID] = b.SyncStatus
	}
	assert.Equal(t, "provisioning", byID["kb_prov"])
	assert.Equal(t, "provisioning failed", byID["kb_dead"],
		"JSON must use the same status vocabulary as the human card")
	assert.Equal(t, "not cloned", byID["kb_gone"])
}

// TestRenderBubblesSection_NoCardsWhenEmptyOrUnavailable verifies the
// degraded cases stay a single line — no stray card scaffolding.
//
// Failure prevented: an unavailable fetch or empty list renders orphaned
// "KB"/"Status" labels under the summary line.
func TestRenderBubblesSection_NoCardsWhenEmptyOrUnavailable(t *testing.T) {
	t.Parallel()

	empty := stripANSIBubbles(renderBubblesSection(statusBubblesSummary{}, nil))
	assert.NotContains(t, empty, "Type")
	assert.NotContains(t, empty, "Status")

	unavail := stripANSIBubbles(renderBubblesSection(statusBubblesSummary{Unavailable: true}, nil))
	assert.Contains(t, unavail, "(unavailable)")
	assert.NotContains(t, unavail, "Status")
}

// TestBuildBubblesJSON_PopulatesRows verifies the JSON envelope carries
// per-bubble rows mirroring the human cards: identity, path, cloned flag,
// and a sync-status string ("not cloned" when there is no checkout).
//
// Failure prevented: scriptable consumers only ever see counts and cannot
// locate a bubble's checkout or detect an unmounted one.
func TestBuildBubblesJSON_PopulatesRows(t *testing.T) {
	store := fakeKBStore(t)
	gitInitKB(t, store, "kb_team")

	s := summarizeBubbles(kb.ListResult{
		Bubbles: []kb.Bubble{
			{KBID: "kb_team", Type: api.KBTypeTeam, Slug: "eng", ScopeType: "team"},
			{KBID: "kb_me", Type: api.KBTypePersonal, Slug: "me", ScopeType: "user"},
		},
	})
	js := buildBubblesJSON(s)
	require.NotNil(t, js)
	require.Len(t, js.Bubbles, 2)

	me := js.Bubbles[0]
	assert.Equal(t, "kb_me", me.KBID)
	assert.Equal(t, "personal", me.Type)
	assert.False(t, me.Cloned)
	assert.Equal(t, "not cloned", me.SyncStatus)
	assert.Equal(t, filepath.Join(store, "kb_me"), me.Path)

	team := js.Bubbles[1]
	assert.Equal(t, "kb_team", team.KBID)
	assert.True(t, team.Cloned)
	assert.NotEmpty(t, team.SyncStatus)
	assert.NotEqual(t, "not cloned", team.SyncStatus)
}

// TestRenderSlugRef verifies the sigil and slug are rendered as distinct
// segments so the muted sigil + bright slug styling can apply per the design.
func TestRenderSlugRef(t *testing.T) {
	t.Parallel()

	out := stripANSIBubbles(renderSlugRef("@", "sageox"))
	assert.Equal(t, "@sageox", out)
	out = stripANSIBubbles(renderSlugRef("#", "marketing"))
	assert.Equal(t, "#marketing", out)
}
