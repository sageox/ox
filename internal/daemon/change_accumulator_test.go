package daemon

import (
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slogDiscard returns a logger that drops everything below a level no record
// uses, so tests stay quiet. Shared across daemon tests.
func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// --- ChangeAccumulator collapse/settle tests ---

func TestAccumulator_CollapseWrites(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("src/config.go", fsnotify.Write, false)
	acc.AddEvent("src/config.go", fsnotify.Write, false)
	acc.AddEvent("src/config.go", fsnotify.Write, false)

	var changes []FileChange
	require.Eventually(t, func() bool {
		changes = acc.DrainSettled()
		return len(changes) == 1
	}, 2*time.Second, 10*time.Millisecond)
	require.Len(t, changes, 1)
	assert.Equal(t, "src/config.go", changes[0].Path)
	assert.Equal(t, ChangeModified, changes[0].ChangeType)
}

func TestAccumulator_CreateThenDelete(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("tmp/scratch.txt", fsnotify.Create, false)
	acc.AddEvent("tmp/scratch.txt", fsnotify.Remove, false)

	// wait until pending events are fully settled, then verify suppression
	require.Eventually(t, func() bool {
		return acc.PendingCount() == 0
	}, 2*time.Second, 10*time.Millisecond)
	changes := acc.DrainSettled()
	assert.Nil(t, changes, "create+delete of same file should be suppressed")
}

func TestAccumulator_DeleteThenCreate(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("src/config.go", fsnotify.Remove, false)
	acc.AddEvent("src/config.go", fsnotify.Create, false)

	var changes []FileChange
	require.Eventually(t, func() bool {
		changes = acc.DrainSettled()
		return len(changes) == 1
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, ChangeModified, changes[0].ChangeType, "delete+create = atomic save = modified")
}

func TestAccumulator_CreateThenModify(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("src/new.go", fsnotify.Create, false)
	acc.AddEvent("src/new.go", fsnotify.Write, false)

	var changes []FileChange
	require.Eventually(t, func() bool {
		changes = acc.DrainSettled()
		return len(changes) == 1
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, ChangeCreated, changes[0].ChangeType, "create+write should stay as created")
}

func TestAccumulator_SettleTimer(t *testing.T) {
	acc := NewChangeAccumulator(100 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("src/foo.go", fsnotify.Write, false)

	// before settle period
	changes := acc.DrainSettled()
	assert.Nil(t, changes, "changes should not be available before settle period")

	// wait for settle
	require.Eventually(t, func() bool {
		changes = acc.DrainSettled()
		return len(changes) == 1
	}, 2*time.Second, 10*time.Millisecond)
}

func TestAccumulator_DrainClears(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("src/foo.go", fsnotify.Write, false)

	var changes []FileChange
	require.Eventually(t, func() bool {
		changes = acc.DrainSettled()
		return len(changes) == 1
	}, 2*time.Second, 10*time.Millisecond)

	// second drain should return nil
	changes = acc.DrainSettled()
	assert.Nil(t, changes)
}

func TestAccumulator_MultipleFiles(t *testing.T) {
	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("src/a.go", fsnotify.Create, false)
	acc.AddEvent("src/b.go", fsnotify.Write, false)
	acc.AddEvent("src/c.go", fsnotify.Remove, false)

	var changes []FileChange
	require.Eventually(t, func() bool {
		changes = acc.DrainSettled()
		return len(changes) == 3
	}, 2*time.Second, 10*time.Millisecond)

	byPath := make(map[string]ChangeType)
	for _, c := range changes {
		byPath[c.Path] = c.ChangeType
	}
	assert.Equal(t, ChangeCreated, byPath["src/a.go"])
	assert.Equal(t, ChangeModified, byPath["src/b.go"])
	assert.Equal(t, ChangeDeleted, byPath["src/c.go"])
}

func TestAccumulator_SettleResetsOnNewEvent(t *testing.T) {
	acc := NewChangeAccumulator(100 * time.Millisecond)
	defer acc.Stop()

	acc.AddEvent("src/foo.go", fsnotify.Write, false)
	time.Sleep(60 * time.Millisecond)                 // 60ms < 100ms settle
	acc.AddEvent("src/bar.go", fsnotify.Write, false) // resets timer

	// at 60ms after first event, timer was reset — should not have settled yet
	changes := acc.DrainSettled()
	assert.Nil(t, changes)

	// wait for full settle period from last event
	require.Eventually(t, func() bool {
		changes = acc.DrainSettled()
		return len(changes) == 2
	}, 2*time.Second, 10*time.Millisecond)
}

// --- OnSettled callback tests ---

// TestChangeAccumulator_OnSettledCallback verifies that the onSettled callback
// fires after changes settle.
// Failure prevented: ChangeAccumulator settles but codedb never learns about it.
func TestChangeAccumulator_OnSettledCallback(t *testing.T) {
	t.Parallel()

	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	called := make(chan struct{}, 1)
	acc.SetOnSettled(func() {
		select {
		case called <- struct{}{}:
		default:
		}
	})

	acc.AddEvent("src/main.go", fsnotify.Write, false)

	select {
	case <-called:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("onSettled callback not called after settle")
	}
}

// TestChangeAccumulator_OnSettledNotCalledAfterStop verifies that the callback
// doesn't fire after the accumulator is stopped.
// Failure prevented: stale callback fires during daemon shutdown.
func TestChangeAccumulator_OnSettledNotCalledAfterStop(t *testing.T) {
	t.Parallel()

	acc := NewChangeAccumulator(100 * time.Millisecond)

	var count int64
	acc.SetOnSettled(func() {
		atomic.AddInt64(&count, 1)
	})

	acc.AddEvent("src/main.go", fsnotify.Write, false)
	// stop before settle fires
	time.Sleep(20 * time.Millisecond)
	acc.Stop()

	// wait past settle window
	time.Sleep(200 * time.Millisecond)

	assert.Equal(t, int64(0), atomic.LoadInt64(&count), "callback should not fire after Stop()")
}

// TestChangeAccumulator_OnSettledNotCalledWhenEmpty verifies that the callback
// doesn't fire when there are no pending changes (settle with empty pending).
// Failure prevented: spurious dirty overlay rebuilds when no files changed.
func TestChangeAccumulator_OnSettledNotCalledWhenEmpty(t *testing.T) {
	t.Parallel()

	acc := NewChangeAccumulator(50 * time.Millisecond)
	defer acc.Stop()

	var count int64
	acc.SetOnSettled(func() {
		atomic.AddInt64(&count, 1)
	})

	// add and drain before settle
	acc.AddEvent("src/main.go", fsnotify.Write, false)

	// wait for first settle to fire callback
	require.Eventually(t, func() bool {
		return atomic.LoadInt64(&count) == 1
	}, 2*time.Second, 10*time.Millisecond)

	// no new events — next settle timer shouldn't fire the callback
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int64(1), atomic.LoadInt64(&count), "callback should not fire with no pending changes")
}
