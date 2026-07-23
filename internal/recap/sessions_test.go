package recap

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Identity.Matches ---
//
// Failure prevented: a weaker match (slug) silently overriding a stronger
// one (user_id), or a case/whitespace difference in a display name causing
// a coworker's own sessions to be reported as not theirs.

func TestIdentityMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   Identity
		meta *lfs.SessionMeta
		sess string
		want bool
	}{
		{
			name: "user_id match wins",
			id:   Identity{UserID: "user_abc"},
			meta: &lfs.SessionMeta{UserID: "user_abc"},
			want: true,
		},
		{
			name: "user_id mismatch",
			id:   Identity{UserID: "user_abc"},
			meta: &lfs.SessionMeta{UserID: "user_xyz"},
			want: false,
		},
		{
			name: "display name match, exact",
			id:   Identity{DisplayName: "Ryan S"},
			meta: &lfs.SessionMeta{Username: "Ryan S"},
			want: true,
		},
		{
			name: "display name match is case and whitespace insensitive",
			id:   Identity{DisplayName: "  Ryan S  "},
			meta: &lfs.SessionMeta{Username: "ryan s"},
			want: true,
		},
		{
			name: "display name mismatch",
			id:   Identity{DisplayName: "Ryan S"},
			meta: &lfs.SessionMeta{Username: "Someone Else"},
			want: false,
		},
		{
			name: "slug fallback for legacy meta with empty username",
			id:   Identity{Slug: "alice"},
			meta: &lfs.SessionMeta{Username: ""},
			sess: "2026-01-01T00-00-alice-ox1234",
			want: true,
		},
		{
			name: "slug fallback mismatch",
			id:   Identity{Slug: "alice"},
			meta: &lfs.SessionMeta{Username: ""},
			sess: "2026-01-01T00-00-bob-ox1234",
			want: false,
		},
		{
			name: "user_id takes precedence over a display name that would mismatch",
			id:   Identity{UserID: "user_abc", DisplayName: "nonsense"},
			meta: &lfs.SessionMeta{UserID: "user_abc", Username: "totally different"},
			want: true,
		},
		{
			name: "meta has no user_id so identity falls through to display name even though identity carries a user_id",
			id:   Identity{UserID: "user_abc", DisplayName: "Ryan S"},
			meta: &lfs.SessionMeta{UserID: "", Username: "Ryan S"},
			want: true,
		},
		{
			name: "nothing resolvable on either side",
			id:   Identity{},
			meta: &lfs.SessionMeta{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.id.Matches(tt.meta, tt.sess)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- ScanSessions ---

// TestScanSessions_WindowBoundaries verifies the half-open [since, until)
// window at the exact instant boundaries. Failure prevented: an off-by-one
// on the boundary silently double-counts or drops a session created exactly
// at the reporting cutoff.
func TestScanSessions_WindowBoundaries(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)

	f.WriteSession("at-since", WithCreatedAt(since))                    // inclusive lower bound
	f.WriteSession("at-until", WithCreatedAt(until))                    // exclusive upper bound
	f.WriteSession("one-ns-before-since", WithCreatedAt(since.Add(-1))) // just outside
	f.WriteSession("one-ns-before-until", WithCreatedAt(until.Add(-1))) // just inside
	f.WriteSession("mid-window", WithCreatedAt(since.Add(48*time.Hour)))

	got := ScanSessions(f.Ledger, since, until, defaultIdentity())

	names := map[string]bool{}
	for _, s := range got {
		names[s.Name] = true
	}
	assert.True(t, names["at-since"], "session created exactly at `since` must be included (inclusive lower bound)")
	assert.False(t, names["at-until"], "session created exactly at `until` must be excluded (exclusive upper bound)")
	assert.False(t, names["one-ns-before-since"], "session created before `since` must be excluded")
	assert.True(t, names["one-ns-before-until"], "session created just inside `until` must be included")
	assert.True(t, names["mid-window"])
	assert.Len(t, got, 3)
}

// TestScanSessions_NoUpperBound verifies a zero `until` means unbounded —
// used by Build's all-time ledger scan.
func TestScanSessions_NoUpperBound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	since := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	f.WriteSession("far-future", WithCreatedAt(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)))

	got := ScanSessions(f.Ledger, since, time.Time{}, defaultIdentity())
	require.Len(t, got, 1)
	assert.Equal(t, "far-future", got[0].Name)
}

// TestScanSessions_MalformedMetaSkippedNotFatal verifies that one
// unreadable/corrupt meta.json among otherwise-valid sessions does not
// abort the scan. Failure prevented: a single bad session directory
// (partial write, disk corruption) taking down the entire recap report.
func TestScanSessions_MalformedMetaSkippedNotFatal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	since := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	f.WriteSession("good-one", WithCreatedAt(fixtureNow))

	badDir := filepath.Join(f.Ledger, "sessions", "corrupt")
	require.NoError(t, os.MkdirAll(badDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(badDir, "meta.json"), []byte("{not valid json"), 0o644))

	emptyDir := filepath.Join(f.Ledger, "sessions", "no-meta-at-all")
	require.NoError(t, os.MkdirAll(emptyDir, 0o755))

	got := ScanSessions(f.Ledger, since, time.Time{}, defaultIdentity())
	require.Len(t, got, 1, "malformed and missing meta.json sessions must be skipped, not fatal")
	assert.Equal(t, "good-one", got[0].Name)
}

// TestScanSessions_MissingSessionsDir verifies a ledger with no sessions/
// directory at all (brand-new ledger) returns an empty, non-nil-panicking
// result rather than erroring.
func TestScanSessions_MissingSessionsDir(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir() // no sessions/ subdir created
	got := ScanSessions(tmp, time.Time{}, time.Time{}, defaultIdentity())
	assert.Empty(t, got)
}

// TestScanSessions_MineVsTeamPartition verifies sessions are correctly
// tagged Mine so downstream miners (decisions, work) can partition without
// re-deriving identity.
func TestScanSessions_MineVsTeamPartition(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	f.WriteSession("mine-1", WithUserID("user_test01"), WithCreatedAt(fixtureNow))
	f.WriteSession("teammate-1", WithUserID("user_other"), WithUsername("Someone Else"), WithCreatedAt(fixtureNow))

	got := ScanSessions(f.Ledger, time.Time{}, time.Time{}, defaultIdentity())
	require.Len(t, got, 2)

	byName := map[string]SessionFacts{}
	for _, s := range got {
		byName[s.Name] = s
	}
	assert.True(t, byName["mine-1"].Mine)
	assert.False(t, byName["teammate-1"].Mine)

	mine := mineOnly(got)
	require.Len(t, mine, 1)
	assert.Equal(t, "mine-1", mine[0].Name)
}

// --- usernameSlug ---

func TestUsernameSlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		// real shape from session.GenerateSessionName: YYYY-MM-DDTHH-MM-<user>-<sessionID>
		{"canonical shape", "2026-01-06T14-32-alice-ox1234", "alice"},
		{"multi-word username joined by dashes", "2026-01-06T14-32-alice-smith-ox1234", "alice-smith"},
		{"too few parts returns empty", "not-a-session-name", ""},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, usernameSlug(tt.in))
		})
	}
}

// --- equalFold ---

func TestEqualFold(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"exact match", "alice", "alice", true},
		{"case insensitive", "Alice", "aLICE", true},
		{"whitespace trimmed", "  alice  ", "alice", true},
		{"different values", "alice", "bob", false},
		{"both empty", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, equalFold(tt.a, tt.b))
		})
	}
}
