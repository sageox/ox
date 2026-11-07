package daemon

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newIdleExitDaemon is a daemon whose inactivity timeout is one hour and whose
// pending-work grace period is one minute.
func newIdleExitDaemon() *Daemon {
	return New(&Config{
		InactivityTimeout:      time.Hour,
		PendingWorkGracePeriod: time.Minute,
	}, slog.New(slog.DiscardHandler))
}

func setCodeDBIndexing(m *CodeDBManager, indexing bool) {
	m.mu.Lock()
	m.indexing = indexing
	m.mu.Unlock()
}

// TestInactivityExitDue verifies the idle exit never fires while the daemon's
// own codedb index pass is running, however long the daemon has been idle.
// Failure prevented: a daemon with no CLI traffic exited at its 1h inactivity
// timeout and canceled a 55 minute index of a large repo mid-pass; the next
// start began the index again from zero.
func TestInactivityExitDue(t *testing.T) {
	const (
		pastTimeout = 2 * time.Hour
		longUptime  = 3 * time.Hour
	)
	tests := []struct {
		name     string
		inactive time.Duration
		uptime   time.Duration
		indexing bool
		noMgr    bool // daemon has no project root, so no codedb manager
		cloning  bool // a background clone is in flight (pending work)
		graceOld bool // pending-work grace period began long ago
		want     bool
	}{
		{name: "idle with nothing running exits", inactive: pastTimeout, uptime: longUptime, want: true},
		{name: "idle without a codedb manager exits", inactive: pastTimeout, uptime: longUptime, noMgr: true, want: true},
		{name: "indexing holds the exit", inactive: pastTimeout, uptime: longUptime, indexing: true, want: false},
		{
			// the pending-work grace period caps how long a clone holds the
			// exit; an index must not be cut off by it
			name:     "indexing outlasts an expired pending-work grace period",
			inactive: pastTimeout, uptime: longUptime, indexing: true, cloning: true, graceOld: true, want: false,
		},
		{name: "expired grace still ends a clone-only wait", inactive: pastTimeout, uptime: longUptime, cloning: true, graceOld: true, want: true},
		{name: "not idle long enough does not exit", inactive: time.Minute, uptime: longUptime, want: false},
		{name: "under one minute of uptime does not exit", inactive: pastTimeout, uptime: 30 * time.Second, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := newIdleExitDaemon()
			if !tt.noMgr {
				d.codedb = NewCodeDBManager(t.TempDir(), slog.New(slog.DiscardHandler), nil)
				setCodeDBIndexing(d.codedb, tt.indexing)
			}
			if tt.cloning {
				d.scheduler = NewSyncScheduler(d.config, slog.New(slog.DiscardHandler))
				d.scheduler.cloneInFlight.Store("team", true)
			}
			if tt.graceOld {
				d.mu.Lock()
				d.pendingWorkSince = time.Now().Add(-time.Hour)
				d.mu.Unlock()
			}

			require.Equal(t, tt.want, d.inactivityExitDue(tt.inactive, tt.uptime))
		})
	}
}

// TestInactivityExitDue_FiresOnceIndexingEnds verifies the hold is only for the
// length of the index pass: the daemon still shuts down on its own afterwards.
// Failure prevented: an index flag that keeps the daemon alive forever.
func TestInactivityExitDue_FiresOnceIndexingEnds(t *testing.T) {
	d := newIdleExitDaemon()
	d.codedb = NewCodeDBManager(t.TempDir(), slog.New(slog.DiscardHandler), nil)

	setCodeDBIndexing(d.codedb, true)
	require.False(t, d.inactivityExitDue(2*time.Hour, 3*time.Hour), "an index pass is running")

	setCodeDBIndexing(d.codedb, false)
	require.True(t, d.inactivityExitDue(2*time.Hour, 3*time.Hour), "the pass ended, so the idle exit applies again")
}
