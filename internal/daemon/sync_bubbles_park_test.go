package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedKBLister fails with a per-scope error (or succeeds) and counts
// calls per scope id.
type scriptedKBLister struct {
	mu    sync.Mutex
	errs  map[string]error
	calls map[string]int
}

func newScriptedKBLister(errs map[string]error) *scriptedKBLister {
	return &scriptedKBLister{errs: errs, calls: map[string]int{}}
}

func (l *scriptedKBLister) ListBubbles(_ context.Context, scope api.KBScope) ([]api.KB, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls[scope.ID]++
	if err := l.errs[scope.ID]; err != nil {
		return nil, err
	}
	return []api.KB{{KBID: "kb_" + scope.ID}}, nil
}

func (l *scriptedKBLister) setErr(scopeID string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err == nil {
		delete(l.errs, scopeID)
		return
	}
	l.errs[scopeID] = err
}

func (l *scriptedKBLister) count(scopeID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls[scopeID]
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

var parkTestScopes = []api.KBScope{
	{Type: api.KBScopeTypeTeam, ID: "team_a"},
	{Type: api.KBScopeTypeTeam, ID: "team_b"},
	{Type: api.KBScopeTypeTeam, ID: "team_c"},
}

func parkTestScheduler(t *testing.T) (*SyncScheduler, *fakeClock, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := NewSyncScheduler(DefaultConfig(), logger)
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	s.kbScopeParks.now = clk.now
	return s, clk, buf
}

// TestListKBScopes_AuthFailureParksScope drives 15s sync cycles and checks the
// rejected scope is listed once, then retried on a 1m, 2m, 4m ... schedule,
// while healthy scopes are listed every cycle.
func TestListKBScopes_AuthFailureParksScope(t *testing.T) {
	t.Parallel()
	s, clk, _ := parkTestScheduler(t)
	lister := newScriptedKBLister(map[string]error{"team_a": api.ErrUnauthorized})

	const cycles = 20
	for i := 0; i < cycles; i++ {
		s.listKBScopes(context.Background(), lister, parkTestScopes)
		clk.advance(15 * time.Second)
	}

	// 20 cycles x 15s = 300s. Attempts at t=0, 60s (park 1m), 180s (park 2m).
	// The next retry is at 180+240=420s, past the window.
	assert.Equal(t, 3, lister.count("team_a"), "rejected scope must be parked between retries")
	assert.Equal(t, cycles, lister.count("team_b"))
	assert.Equal(t, cycles, lister.count("team_c"))
}

func TestKBScopeParks_BackoffGrowthAndCap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
		{5, 16 * time.Minute},
		{6, 32 * time.Minute},
		{7, time.Hour},
		{8, time.Hour},
		{50, time.Hour},
	}
	var p kbScopeParks
	for _, tt := range tests {
		// advance to the attempt count one failure at a time; only the
		// delay returned for the target attempt is asserted.
		got := time.Duration(0)
		for p.scopes["x"] == nil || p.scopes["x"].failures < tt.attempt {
			_, got = p.fail("x")
		}
		assert.Equal(t, tt.want, got, "attempt %d", tt.attempt)
	}
}

func TestListKBScopes_RecoveryClearsPark(t *testing.T) {
	t.Parallel()
	s, clk, buf := parkTestScheduler(t)
	lister := newScriptedKBLister(map[string]error{"team_a": api.ErrUnauthorized})

	s.listKBScopes(context.Background(), lister, parkTestScopes)
	require.True(t, s.kbScopeParks.parked("team_a"))

	lister.setErr("team_a", nil)
	clk.advance(61 * time.Second)
	rows := s.listKBScopes(context.Background(), lister, parkTestScopes)

	assert.False(t, s.kbScopeParks.parked("team_a"))
	assert.Len(t, rows, 3)
	assert.Contains(t, buf.String(), "kb_sync scope recovered")

	// a fresh failure starts over at the 1m step, not where it left off
	lister.setErr("team_a", api.ErrUnauthorized)
	s.listKBScopes(context.Background(), lister, parkTestScopes)
	assert.Equal(t, 1, s.kbScopeParks.scopes["team_a"].failures)
}

func TestListKBScopes_ManualSyncLiftsParks(t *testing.T) {
	t.Parallel()
	s, _, _ := parkTestScheduler(t)
	lister := newScriptedKBLister(map[string]error{"team_a": api.ErrUnauthorized})

	s.listKBScopes(context.Background(), lister, parkTestScopes)
	s.listKBScopes(context.Background(), lister, parkTestScopes)
	require.Equal(t, 1, lister.count("team_a"))

	s.clearKBScopeParks()
	s.listKBScopes(context.Background(), lister, parkTestScopes)
	assert.Equal(t, 2, lister.count("team_a"), "manual sync must retry parked scopes")
}

func TestListKBScopes_ParkLogging(t *testing.T) {
	t.Parallel()
	s, clk, buf := parkTestScheduler(t)
	lister := newScriptedKBLister(map[string]error{"team_a": api.ErrUnauthorized})

	for i := 0; i < 3; i++ {
		s.listKBScopes(context.Background(), lister, parkTestScopes)
		clk.advance(15 * time.Second)
	}
	// parked for the remaining cycles: exactly one WARN, no repeats
	assert.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("kb_sync scope parked")))
	assert.Contains(t, buf.String(), "attempts=1")
	assert.Contains(t, buf.String(), "retry_after=1m0s")
}

func TestListKBScopes_AuthMessageSelection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		errs        map[string]error
		wantDenied  bool
		wantLoginHn bool
	}{
		{
			name:       "other scope succeeded: access denied wording",
			errs:       map[string]error{"team_a": api.ErrUnauthorized},
			wantDenied: true,
		},
		{
			name: "every scope failed: keep login hint",
			errs: map[string]error{
				"team_a": api.ErrUnauthorized,
				"team_b": api.ErrUnauthorized,
				"team_c": api.ErrUnauthorized,
			},
			wantLoginHn: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, _, buf := parkTestScheduler(t)
			s.listKBScopes(context.Background(), newScriptedKBLister(tt.errs), parkTestScopes)
			out := buf.String()
			assert.Equal(t, tt.wantDenied,
				bytes.Contains(buf.Bytes(), []byte("access denied for scope team_a: server rejected the token for this team")), out)
			assert.Equal(t, tt.wantLoginHn, bytes.Contains(buf.Bytes(), []byte("ox login")), out)
		})
	}
}
