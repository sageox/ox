package kb

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- fakes ---

// fakeKBSource is a hand-rolled fake (rather than testify mock) so each
// test reads top-to-bottom without indirection — matches the style in
// internal/api/kb_test.go.
type fakeKBSource struct {
	rows  []api.KB
	err   error
	calls atomic.Int32
	delay time.Duration
}

func (f *fakeKBSource) ListBubbles(ctx context.Context) ([]api.KB, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

type fakeTeamSource struct {
	rows     []LegacyTeamRow
	endpoint string
	err      error
	delay    time.Duration
}

func (f *fakeTeamSource) ListTeamContexts(ctx context.Context) ([]LegacyTeamRow, string, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	if f.err != nil {
		return nil, "", f.err
	}
	return f.rows, f.endpoint, nil
}

type fakeLedgerSource struct {
	rows  []LegacyLedgerRow
	err   error
	delay time.Duration
}

func (f *fakeLedgerSource) ListLedgers(ctx context.Context) ([]LegacyLedgerRow, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

// --- helpers ---

// findBySlug returns the first bubble with the given slug; nil if none.
// Cleaner than indexing into the slice when ordering between sources isn't
// load-bearing.
func findBySlug(bubbles []Bubble, slug string) *Bubble {
	for i := range bubbles {
		if bubbles[i].Slug == slug {
			return &bubbles[i]
		}
	}
	return nil
}

// --- tests ---

// TestMerge_HappyPath verifies all three sources contribute and Source/Legacy
// tags are set correctly.
// Failure prevented: a regression where a source's rows never reach the
// merged output (e.g. a goroutine that returns before assigning to the
// shared slice) goes silently undetected.
func TestMerge_HappyPath(t *testing.T) {
	t.Parallel()

	kb := &fakeKBSource{rows: []api.KB{
		{KBID: "kb_01", KBType: api.KBTypePersonal, Slug: "personal-abc", Name: "Personal", ViewerRole: "owner"},
		{KBID: "kb_02", KBType: api.KBTypeTeam, Slug: "platform", Name: "Platform", ViewerRole: "member"},
	}}
	teams := &fakeTeamSource{
		rows:     []LegacyTeamRow{{TeamID: "team_legacy", Name: "Legacy Team", Slug: "legacy", URL: "https://git.example/legacy.git"}},
		endpoint: "https://sageox.ai",
	}
	ledger := &fakeLedgerSource{rows: []LegacyLedgerRow{
		{RepoID: "repo_01", Name: "ox", Slug: "ox", LocalDir: "/tmp/ox-ledger", Endpoint: "https://sageox.ai"},
	}}

	res, err := NewMerger(kb, teams, ledger).Merge(context.Background())

	require.NoError(t, err)
	require.Empty(t, res.Warnings)
	require.Len(t, res.Bubbles, 4)

	personal := findBySlug(res.Bubbles, "personal-abc")
	require.NotNil(t, personal)
	assert.Equal(t, SourceKB, personal.Source)
	assert.False(t, personal.Legacy)
	assert.Equal(t, api.KBTypePersonal, personal.Type)

	legacy := findBySlug(res.Bubbles, "legacy")
	require.NotNil(t, legacy)
	assert.Equal(t, SourceTeamLegacy, legacy.Source)
	assert.True(t, legacy.Legacy)
	assert.Equal(t, api.KBTypeTeam, legacy.Type)

	ox := findBySlug(res.Bubbles, "ox")
	require.NotNil(t, ox)
	assert.Equal(t, SourceLedger, ox.Source)
	assert.True(t, ox.Legacy)
	assert.Equal(t, api.KBTypeRepo, ox.Type)
}

// TestMerge_DedupBySlugKBWins verifies that when a kb-API row and a legacy
// team row share a slug, the kb-API row is kept and the legacy synthesis
// is suppressed.
// Failure prevented: showing the same logical bubble twice in `ox kb list`
// once the server starts emitting kb_id for what was previously a legacy
// team row.
func TestMerge_DedupBySlugKBWins(t *testing.T) {
	t.Parallel()

	kb := &fakeKBSource{rows: []api.KB{
		{KBID: "kb_platform", KBType: api.KBTypeTeam, Slug: "platform", Name: "Platform"},
	}}
	teams := &fakeTeamSource{
		rows:     []LegacyTeamRow{{TeamID: "team_legacy", Name: "Platform (legacy)", Slug: "platform"}},
		endpoint: "https://sageox.ai",
	}

	res, err := NewMerger(kb, teams, nil).Merge(context.Background())

	require.NoError(t, err)
	require.Empty(t, res.Warnings)
	require.Len(t, res.Bubbles, 1)
	assert.Equal(t, SourceKB, res.Bubbles[0].Source)
	assert.False(t, res.Bubbles[0].Legacy)
	assert.Equal(t, "kb_platform", res.Bubbles[0].KBID)
}

// TestMerge_DedupByRepoIDKBWins verifies the secondary dedup tier: kb row
// without a slug match still suppresses a legacy ledger row with the same
// repo_id (hypothetical — RepoID isn't currently on api.KB but the code
// path is exercised via legacy rows pre-claiming the key).
// Failure prevented: a future kb-API field add (kb.RepoID) silently
// breaking dedup because the merger forgot to register it.
//
// NOTE: api.KB doesn't expose RepoID today, so this test specifically
// covers cross-legacy dedup: a team row with TeamID="x" and a ledger row
// with RepoID="x" must collapse to one entry.
func TestMerge_DedupByRepoIDAcrossLegacy(t *testing.T) {
	t.Parallel()

	teams := &fakeTeamSource{
		rows:     []LegacyTeamRow{{TeamID: "shared_id", Name: "Shared", Slug: "shared-team"}},
		endpoint: "https://sageox.ai",
	}
	ledger := &fakeLedgerSource{rows: []LegacyLedgerRow{
		{RepoID: "shared_id", Name: "Shared (ledger)", Slug: "shared-ledger", Endpoint: "https://sageox.ai"},
	}}

	res, err := NewMerger(nil, teams, ledger).Merge(context.Background())

	require.NoError(t, err)
	require.Len(t, res.Bubbles, 1, "shared repo_id across two legacy sources must collapse to one row")
	assert.Equal(t, SourceTeamLegacy, res.Bubbles[0].Source, "team source admitted first wins")
}

// TestMerge_KBUnavailableIsNotWarning verifies api.ErrKBAPIUnavailable
// (the flag-off / endpoint-missing path) is treated as "0 rows from this
// source" with NO warning surfaced.
// Failure prevented: every CLI invocation showing a scary "kb API failed"
// message during the gradual flag rollout, when the expected behavior is
// silent legacy-only.
func TestMerge_KBUnavailableIsNotWarning(t *testing.T) {
	t.Parallel()

	kb := &fakeKBSource{err: fmt.Errorf("wrap: %w", api.ErrKBAPIUnavailable)}
	teams := &fakeTeamSource{rows: []LegacyTeamRow{{TeamID: "t1", Name: "T1", Slug: "t1"}}, endpoint: "https://sageox.ai"}

	res, err := NewMerger(kb, teams, nil).Merge(context.Background())

	require.NoError(t, err)
	assert.Empty(t, res.Warnings, "ErrKBAPIUnavailable must not surface as a warning")
	require.Len(t, res.Bubbles, 1)
	assert.Equal(t, SourceTeamLegacy, res.Bubbles[0].Source)
}

// TestMerge_KB500IsWarning verifies a real 5xx from the kb API surfaces
// as a SourceWarning while other sources continue to deliver rows.
// Failure prevented: a real outage being silently swallowed because every
// kb error looked like the flag-off path.
func TestMerge_KB500IsWarning(t *testing.T) {
	t.Parallel()

	kb := &fakeKBSource{err: errors.New("HTTP 500 from kb")}
	teams := &fakeTeamSource{rows: []LegacyTeamRow{{TeamID: "t1", Name: "T1", Slug: "t1"}}, endpoint: "https://sageox.ai"}

	res, err := NewMerger(kb, teams, nil).Merge(context.Background())

	require.NoError(t, err)
	require.Len(t, res.Warnings, 1)
	assert.Equal(t, SourceKB, res.Warnings[0].Source)
	assert.Contains(t, res.Warnings[0].Err, "500")
	require.Len(t, res.Bubbles, 1, "kb failure must NOT block other sources from reaching the result")
}

// TestMerge_LegacyTeamTimeoutIsWarning verifies a timeout from
// /api/v1/cli/repos surfaces as a warning while kb + ledger still deliver.
// Failure prevented: a slow legacy endpoint dragging the entire `ox kb
// list` to a hard error rather than a partial result.
func TestMerge_LegacyTeamTimeoutIsWarning(t *testing.T) {
	t.Parallel()

	kb := &fakeKBSource{rows: []api.KB{{KBID: "kb_01", KBType: api.KBTypePersonal, Slug: "personal", Name: "Personal"}}}
	teams := &fakeTeamSource{err: context.DeadlineExceeded}
	ledger := &fakeLedgerSource{rows: []LegacyLedgerRow{{RepoID: "r1", Name: "ox", Slug: "ox", Endpoint: "https://sageox.ai"}}}

	res, err := NewMerger(kb, teams, ledger).Merge(context.Background())

	require.NoError(t, err)
	require.Len(t, res.Warnings, 1)
	assert.Equal(t, SourceTeamLegacy, res.Warnings[0].Source)
	assert.Len(t, res.Bubbles, 2, "kb + ledger must still appear when team source fails")
}

// TestMerge_OXKBDisableSkipsKBSource verifies the OX_KB_DISABLE escape
// hatch causes zero calls to the kb client and no warning.
// Failure prevented: the env-var override silently doing nothing during a
// rollout incident, leaving operators without a way to bypass the source.
func TestMerge_OXKBDisableSkipsKBSource(t *testing.T) {
	t.Setenv(envDisableKBSource, "1")

	kb := &fakeKBSource{rows: []api.KB{{KBID: "kb_01", KBType: api.KBTypePersonal, Slug: "personal", Name: "Personal"}}}
	teams := &fakeTeamSource{rows: []LegacyTeamRow{{TeamID: "t1", Name: "T1", Slug: "t1"}}, endpoint: "https://sageox.ai"}

	res, err := NewMerger(kb, teams, nil).Merge(context.Background())

	require.NoError(t, err)
	assert.Empty(t, res.Warnings)
	assert.Equal(t, int32(0), kb.calls.Load(), "OX_KB_DISABLE must short-circuit before calling the kb client")
	require.Len(t, res.Bubbles, 1)
	assert.Equal(t, SourceTeamLegacy, res.Bubbles[0].Source)
}

// TestMerge_ForwardCompatUnknownKBType verifies an unknown kb_type
// arriving from a future server rollout collapses to KBTypeUnknown and
// the row still surfaces.
// Failure prevented: a sixth kb_type breaking every CLI client until
// they upgrade.
func TestMerge_ForwardCompatUnknownKBType(t *testing.T) {
	t.Parallel()

	// the kb client is responsible for normalizing — we model that here
	// by preloading the row with KBTypeUnknown to confirm the merger
	// passes it through verbatim.
	kb := &fakeKBSource{rows: []api.KB{{KBID: "kb_y", KBType: api.KBTypeUnknown, Slug: "future", Name: "Future"}}}

	res, err := NewMerger(kb, nil, nil).Merge(context.Background())

	require.NoError(t, err)
	require.Len(t, res.Bubbles, 1)
	assert.Equal(t, api.KBTypeUnknown, res.Bubbles[0].Type)
	assert.Equal(t, "kb_y", res.Bubbles[0].KBID)
}

// TestMerge_AllEmpty verifies the merger returns a clean empty result
// when every source returns zero rows — no nil slice, no spurious warning.
// Failure prevented: callers needing to special-case nil vs empty slices
// because the merger sometimes returned one and sometimes the other.
func TestMerge_AllEmpty(t *testing.T) {
	t.Parallel()

	res, err := NewMerger(&fakeKBSource{}, &fakeTeamSource{}, &fakeLedgerSource{}).Merge(context.Background())

	require.NoError(t, err)
	assert.Empty(t, res.Bubbles)
	assert.Empty(t, res.Warnings)
}

// TestMerge_ContextCancellation verifies all three goroutines respect a
// canceled context and the merger returns promptly rather than blocking.
// Failure prevented: a hung CLI when the user hits Ctrl-C during a slow
// kb/legacy fetch.
func TestMerge_ContextCancellation(t *testing.T) {
	t.Parallel()

	kb := &fakeKBSource{rows: []api.KB{{KBID: "kb_01", Slug: "p", Name: "P"}}, delay: 2 * time.Second}
	teams := &fakeTeamSource{rows: []LegacyTeamRow{{TeamID: "t1", Slug: "t1"}}, delay: 2 * time.Second}
	ledger := &fakeLedgerSource{rows: []LegacyLedgerRow{{RepoID: "r1", Slug: "r1"}}, delay: 2 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	start := time.Now()
	res, err := NewMerger(kb, teams, ledger).Merge(ctx)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Less(t, elapsed, time.Second, "canceled context must abort fan-out goroutines promptly")
	// the result is allowed to be empty (cancellation arrived first) or
	// to contain a warning — the contract here is just "returned fast";
	// the warnings/bubbles values are advisory.
	_ = res
}

// TestMerge_NilSourcesAreOK verifies the merger tolerates nil for any of
// its three source slots — useful for narrow tests and for daemon code
// paths that haven't wired a source yet.
// Failure prevented: a nil-deref crash during partial wiring, or during
// startup when one source isn't ready.
func TestMerge_NilSourcesAreOK(t *testing.T) {
	t.Parallel()

	res, err := NewMerger(nil, nil, nil).Merge(context.Background())

	require.NoError(t, err)
	assert.Empty(t, res.Bubbles)
	assert.Empty(t, res.Warnings)
}

// TestMerge_LedgerErrorWarningWithOtherRowsPreserved verifies a ledger
// scan failure surfaces as a warning but the other two sources still land.
// Failure prevented: a corrupt local ledger directory wiping out the
// user's whole `ox kb list` output.
func TestMerge_LedgerErrorWarningWithOtherRowsPreserved(t *testing.T) {
	t.Parallel()

	kb := &fakeKBSource{rows: []api.KB{{KBID: "kb_01", KBType: api.KBTypePersonal, Slug: "personal", Name: "P"}}}
	teams := &fakeTeamSource{rows: []LegacyTeamRow{{TeamID: "t1", Name: "T1", Slug: "t1"}}, endpoint: "https://sageox.ai"}
	ledger := &fakeLedgerSource{err: errors.New("permission denied scanning ledgers dir")}

	res, err := NewMerger(kb, teams, ledger).Merge(context.Background())

	require.NoError(t, err)
	require.Len(t, res.Warnings, 1)
	assert.Equal(t, SourceLedger, res.Warnings[0].Source)
	assert.Contains(t, res.Warnings[0].Err, "permission denied")
	assert.Len(t, res.Bubbles, 2)
}

// --- ox-gzp.27 dedup edge cases ---
//
// These tests pin the exact behavior of the three-pass dedup so future
// refactors of mergeAndDedup don't quietly change the contract. Each test
// tries to exercise a corner the existing happy-path tests don't reach.

// TestMerge_ThreeWaySlugCollision_KBWins covers the documented worst case:
// a kb-API row, a legacy team row, and a legacy ledger row all describe the
// same conceptual entity by slug. The kb-API row must own the slot and the
// two legacy synthesizers must be suppressed.
//
// Failure prevented: a regression that admits two of the three rows (or
// even all three) would triple-count the same bubble in `ox kb list`,
// which is exactly the user-visible bug the merger exists to prevent.
func TestMerge_ThreeWaySlugCollision_KBWins(t *testing.T) {
	t.Parallel()

	kb := &fakeKBSource{rows: []api.KB{
		{KBID: "kb_platform", KBType: api.KBTypeTeam, Slug: "platform", Name: "Platform (kb)"},
	}}
	teams := &fakeTeamSource{
		rows:     []LegacyTeamRow{{TeamID: "team_legacy", Name: "Platform (team-legacy)", Slug: "platform"}},
		endpoint: "https://sageox.ai",
	}
	ledger := &fakeLedgerSource{rows: []LegacyLedgerRow{
		{RepoID: "repo_legacy", Name: "Platform (ledger-legacy)", Slug: "platform", Endpoint: "https://sageox.ai"},
	}}

	res, err := NewMerger(kb, teams, ledger).Merge(context.Background())

	require.NoError(t, err)
	require.Len(t, res.Bubbles, 1, "three rows describing the same slug must collapse to one")
	assert.Equal(t, SourceKB, res.Bubbles[0].Source)
	assert.Equal(t, "kb_platform", res.Bubbles[0].KBID)
	assert.Equal(t, "Platform (kb)", res.Bubbles[0].Name, "kb-API row must own the surviving record")
}

// TestMerge_SlugCaseSensitivity pins the documented case-sensitivity
// contract: slugs are compared with a direct string equality so "Foo" and
// "foo" are distinct rows. Slugs are server-normalized to lowercase per
// ADR-036, but the merger doesn't re-lower-case — that's the kb client's
// (or the server's) job. Both rows must therefore appear.
//
// Failure prevented: a future "be lenient" change that lower-cases on the
// way through would silently dedup an unmigrated mixed-case legacy slug
// against a properly-lowercased kb-API row, hiding the legacy entry from
// `ox kb list`.
func TestMerge_SlugCaseSensitivity(t *testing.T) {
	t.Parallel()

	kb := &fakeKBSource{rows: []api.KB{
		{KBID: "kb_01", KBType: api.KBTypeTeam, Slug: "foo", Name: "lower-case"},
	}}
	teams := &fakeTeamSource{
		rows:     []LegacyTeamRow{{TeamID: "team_legacy", Name: "Mixed-Case", Slug: "Foo"}},
		endpoint: "https://sageox.ai",
	}

	res, err := NewMerger(kb, teams, nil).Merge(context.Background())

	require.NoError(t, err)
	require.Len(t, res.Bubbles, 2, "case-different slugs must NOT be deduped")
	// confirm the kb-API row claimed "foo" (not "Foo") so the contract is
	// asymmetric and explicit.
	gotSlugs := []string{res.Bubbles[0].Slug, res.Bubbles[1].Slug}
	assert.Contains(t, gotSlugs, "foo")
	assert.Contains(t, gotSlugs, "Foo")
}

// TestMerge_LegacyTeamSameSlugDifferentEndpoints_KeepsBoth verifies that
// when the merger's secondary key is (slug, endpoint), two rows differing
// only by endpoint are both kept. In practice this happens when a user
// briefly bridges staging↔prod during an endpoint migration and lands a
// dual-endpoint local ledger registry. Cross-endpoint dedup is wrong: the
// rows point at different stores.
//
// Failure prevented: a future refactor that drops the endpoint half of
// the key would collapse two genuinely-distinct ledger rows and route
// status/path commands at the wrong endpoint's checkout.
func TestMerge_LegacyLedgerSameSlugDifferentEndpoints_KeepsBoth(t *testing.T) {
	t.Parallel()

	ledger := &fakeLedgerSource{rows: []LegacyLedgerRow{
		{RepoID: "repo_prod", Name: "ox (prod)", Slug: "ox", Endpoint: "https://sageox.ai", LocalDir: "/p/prod"},
		{RepoID: "repo_stg", Name: "ox (staging)", Slug: "ox", Endpoint: "https://staging.sageox.ai", LocalDir: "/p/staging"},
	}}

	res, err := NewMerger(nil, nil, ledger).Merge(context.Background())

	require.NoError(t, err)
	require.Len(t, res.Bubbles, 2, "different endpoints must NOT collapse to one row")
	// both must be present and distinguishable by RepoID/LocalDir
	gotIDs := []string{res.Bubbles[0].RepoID, res.Bubbles[1].RepoID}
	assert.Contains(t, gotIDs, "repo_prod")
	assert.Contains(t, gotIDs, "repo_stg")
}

// TestMerge_KBRowEmptyKBIDStillIncluded covers a defensive case the merger
// is expected to handle gracefully: a kb-API row that for whatever reason
// arrives without a kb_id (shouldn't happen, but server bugs do happen).
// The merger must not crash, must include the row, and must use the
// secondary key (slug+endpoint) to suppress legacy rows that share the slug.
//
// Failure prevented: a panic on a nil/empty kb_id would knock out the
// merger for every caller during the rollout window when this could
// realistically appear; silently dropping the row would hide a real
// server-side bug instead of surfacing it.
func TestMerge_KBRowEmptyKBIDStillIncluded(t *testing.T) {
	t.Parallel()

	kb := &fakeKBSource{rows: []api.KB{
		{KBID: "", KBType: api.KBTypeTeam, Slug: "platform", Name: "Defensive"},
	}}
	teams := &fakeTeamSource{
		rows:     []LegacyTeamRow{{TeamID: "team_legacy", Name: "Platform (legacy)", Slug: "platform"}},
		endpoint: "https://sageox.ai",
	}

	res, err := NewMerger(kb, teams, nil).Merge(context.Background())

	require.NoError(t, err)
	require.Len(t, res.Bubbles, 1, "kb row without kb_id must still suppress legacy via slug")
	assert.Equal(t, SourceKB, res.Bubbles[0].Source)
	assert.Empty(t, res.Bubbles[0].KBID, "kb_id stays empty rather than being fabricated")
	assert.Equal(t, "Defensive", res.Bubbles[0].Name)
}

// TestMerge_MultipleKBRowsSameSlug_BothKept exercises a documented edge
// case: the kb API can legitimately return two rows with the same slug
// but different kb_ids (e.g., a personal bubble and a team bubble that
// happen to share a slug, or a migration window where both old + new
// rows briefly coexist). Pass 1 admits all kb-API rows unconditionally —
// it does NOT dedup within the kb-API source itself.
//
// Failure prevented: a "tighten dedup within kb-API rows" refactor would
// silently drop one of the two slugs, making `ox kb show <slug>` resolve
// only the surviving one and orphan the other.
func TestMerge_MultipleKBRowsSameSlug_BothKept(t *testing.T) {
	t.Parallel()

	kb := &fakeKBSource{rows: []api.KB{
		{KBID: "kb_01", KBType: api.KBTypePersonal, Slug: "notes", Name: "Personal Notes"},
		{KBID: "kb_02", KBType: api.KBTypeTeam, Slug: "notes", Name: "Team Notes"},
	}}

	res, err := NewMerger(kb, nil, nil).Merge(context.Background())

	require.NoError(t, err)
	require.Len(t, res.Bubbles, 2, "two kb-API rows with the same slug but different kb_ids must both pass through")
	gotIDs := []string{res.Bubbles[0].KBID, res.Bubbles[1].KBID}
	assert.Contains(t, gotIDs, "kb_01")
	assert.Contains(t, gotIDs, "kb_02")
}
