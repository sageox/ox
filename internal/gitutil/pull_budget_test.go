package gitutil

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: a ledger hundreds of commits ahead needs minutes to
// rebase; a flat 60s budget kills it mid-replay every cycle so the backlog
// never drains.
func TestPullBudget_ScalesWithAheadAndIsCapped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		ahead int
		want  time.Duration
	}{
		{"negative is treated as zero", -5, 60 * time.Second},
		{"zero ahead keeps the base", 0, 60 * time.Second},
		{"scales one second per commit", 100, 160 * time.Second},
		{"550 commits gets ten minutes", 550, 610 * time.Second},
		{"exactly at the cap", 840, 15 * time.Minute},
		{"beyond the cap is clamped", 100000, 15 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, PullBudget(tt.ahead))
		})
	}
}

func TestPullContext(t *testing.T) {
	t.Parallel()

	t.Run("small backlog keeps the cycle context so a network hang stays bounded", func(t *testing.T) {
		t.Parallel()
		parent, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		got, release := PullContext(parent, longBacklogAhead-1, nil)
		defer release()
		assert.Equal(t, parent, got)
	})

	t.Run("long backlog outlives the cycle deadline with the scaled budget", func(t *testing.T) {
		t.Parallel()
		parent, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		got, release := PullContext(parent, 550, nil)
		defer release()

		<-parent.Done()
		require.Error(t, parent.Err())
		assert.NoError(t, got.Err(), "cycle deadline must not kill a long rebase")
		dl, ok := got.Deadline()
		require.True(t, ok)
		assert.InDelta(t, PullBudget(550).Seconds(), time.Until(dl).Seconds(), 5)
	})

	t.Run("long backlog still honors cancellation such as daemon shutdown", func(t *testing.T) {
		t.Parallel()
		parent, cancel := context.WithCancel(context.Background())
		got, release := PullContext(parent, 550, nil)
		defer release()
		cancel()
		select {
		case <-got.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("shutdown cancel did not reach the pull context")
		}
	})

	t.Run("shutdown cancels the pull even after the cycle deadline expired", func(t *testing.T) {
		t.Parallel()
		parent, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		shutdown, stopDaemon := context.WithCancel(context.Background())
		got, release := PullContext(parent, 550, shutdown)
		defer release()
		<-parent.Done()
		require.NoError(t, got.Err())
		stopDaemon()
		select {
		case <-got.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("daemon shutdown did not cancel the detached pull")
		}
	})

	t.Run("a parent with more time than the budget is left alone", func(t *testing.T) {
		t.Parallel()
		parent, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		got, release := PullContext(parent, 100, nil)
		defer release()
		assert.Equal(t, parent, got)
	})
}
