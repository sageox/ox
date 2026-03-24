package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// stubSource is a test source with configurable behavior.
type stubSource struct {
	name     string
	interval time.Duration
	entries  []whisperstore.WhisperEntry
	mu       sync.Mutex
	calls    int
}

func (s *stubSource) Name() string            { return s.name }
func (s *stubSource) Interval() time.Duration { return s.interval }

func (s *stubSource) Produce(_ context.Context) []whisperstore.WhisperEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.entries
}

func (s *stubSource) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// newTestRegistry creates a WhisperRegistry backed by a real SQLite store in a temp dir.
func newTestRegistry(t *testing.T) *WhisperRegistry {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "whisper.db")
	store, err := whisperstore.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return NewWhisperRegistry(store, testLogger())
}

func TestWhisperScheduler_ProduceAtInterval(t *testing.T) {
	registry := newTestRegistry(t)
	scheduler := NewWhisperScheduler(registry, testLogger())

	source := &stubSource{
		name:     "test-source",
		interval: 10 * time.Millisecond,
		entries: []whisperstore.WhisperEntry{{
			ID:         "entry-1",
			Scope:      "ledger",
			Type:       whisperstore.WhisperTimeBased,
			Source:     "test-source",
			Topic:      "test",
			Content:    "hello",
			Importance: whisperstore.ImportanceAmbient,
			CreatedAt:  time.Now(),
		}},
	}
	scheduler.RegisterSource(source)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	scheduler.Start(ctx, &wg)

	// wait for at least 2 ticks
	require.Eventually(t, func() bool {
		return source.callCount() >= 2
	}, 500*time.Millisecond, 5*time.Millisecond, "source should be called at least twice")

	cancel()
	wg.Wait()

	// verify entries were added to the store
	entries, err := registry.GetWhispers("test-agent", whisperstore.AttentionAll, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, entries, "entries should be present in the store")
}

func TestWhisperScheduler_ContextCancellation(t *testing.T) {
	registry := newTestRegistry(t)
	scheduler := NewWhisperScheduler(registry, testLogger())

	source := &stubSource{
		name:     "cancel-test",
		interval: 10 * time.Millisecond,
	}
	scheduler.RegisterSource(source)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	scheduler.Start(ctx, &wg)

	// cancel immediately
	cancel()

	// goroutines should exit promptly
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler goroutines did not exit after context cancellation")
	}
}

func TestWhisperScheduler_EmptyEntriesNoOp(t *testing.T) {
	registry := newTestRegistry(t)
	scheduler := NewWhisperScheduler(registry, testLogger())

	source := &stubSource{
		name:     "empty-source",
		interval: 10 * time.Millisecond,
		entries:  nil, // returns no entries
	}
	scheduler.RegisterSource(source)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	scheduler.Start(ctx, &wg)

	// wait for a few ticks
	require.Eventually(t, func() bool {
		return source.callCount() >= 3
	}, 500*time.Millisecond, 5*time.Millisecond)

	cancel()
	wg.Wait()

	// no entries should be in the store
	entries, err := registry.GetWhispers("test-agent", whisperstore.AttentionAll, nil)
	require.NoError(t, err)
	assert.Empty(t, entries, "empty source should not add entries")
}

func TestWhisperScheduler_MultipleSources(t *testing.T) {
	registry := newTestRegistry(t)
	scheduler := NewWhisperScheduler(registry, testLogger())

	fastSource := &stubSource{
		name:     "fast",
		interval: 10 * time.Millisecond,
		entries: []whisperstore.WhisperEntry{{
			ID:         "fast-entry",
			Scope:      "ledger",
			Type:       whisperstore.WhisperTimeBased,
			Source:     "fast",
			Topic:      "speed",
			Content:    "fast data",
			Importance: whisperstore.ImportanceNormal,
			CreatedAt:  time.Now(),
		}},
	}

	slowSource := &stubSource{
		name:     "slow",
		interval: 50 * time.Millisecond,
		entries: []whisperstore.WhisperEntry{{
			ID:         "slow-entry",
			Scope:      "ledger",
			Type:       whisperstore.WhisperTimeBased,
			Source:     "slow",
			Topic:      "patience",
			Content:    "slow data",
			Importance: whisperstore.ImportanceAmbient,
			CreatedAt:  time.Now(),
		}},
	}

	scheduler.RegisterSource(fastSource)
	scheduler.RegisterSource(slowSource)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	scheduler.Start(ctx, &wg)

	// wait for fast source to tick several times and slow at least once
	require.Eventually(t, func() bool {
		return fastSource.callCount() >= 3 && slowSource.callCount() >= 1
	}, 500*time.Millisecond, 5*time.Millisecond)

	// fast should have more calls than slow
	assert.Greater(t, fastSource.callCount(), slowSource.callCount(),
		"fast source should tick more frequently than slow source")

	cancel()
	wg.Wait()

	// both sources should have contributed entries
	entries, err := registry.GetWhispers("test-agent", whisperstore.AttentionAll, nil)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(entries), 2, "both sources should contribute entries")
}

func TestWhisperScheduler_RunPrune(t *testing.T) {
	registry := newTestRegistry(t)
	scheduler := NewWhisperScheduler(registry, testLogger())

	// add an old entry directly
	err := registry.Add("ledger", whisperstore.WhisperEntry{
		ID:         "old-entry",
		Scope:      "ledger",
		Type:       whisperstore.WhisperTimeBased,
		Source:     "test",
		Topic:      "test",
		Content:    "old data",
		Importance: whisperstore.ImportanceAmbient,
		CreatedAt:  time.Now().Add(-48 * time.Hour), // 2 days old
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	scheduler.RunPrune(ctx, &wg, 10*time.Millisecond)

	// use a unique agent ID to avoid cursor interference from other tests
	require.Eventually(t, func() bool {
		entries, err := registry.GetWhispers("prune-check-agent", whisperstore.AttentionAll, nil)
		return err == nil && len(entries) == 0
	}, 2*time.Second, 10*time.Millisecond, "old entry should be pruned")

	cancel()
	wg.Wait()
}

func TestActivitySummarySource_Content(t *testing.T) {
	tests := []struct {
		name        string
		setupHB     func(hb *HeartbeatHandler)
		wantNil     bool
		wantContent string
	}{
		{
			name:    "no heartbeat activity produces nil",
			wantNil: true,
		},
		{
			name: "active agents reported",
			setupHB: func(hb *HeartbeatHandler) {
				hb.agentActivity.Record("agent-1")
			},
			wantContent: "1 coworker(s) active on this repo",
		},
		{
			name: "multiple agents reported",
			setupHB: func(hb *HeartbeatHandler) {
				hb.agentActivity.Record("agent-1")
				hb.agentActivity.Record("agent-2")
				hb.agentActivity.Record("agent-3")
			},
			wantContent: "3 coworker(s) active on this repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hb := NewHeartbeatHandler(testLogger())
			if tt.setupHB != nil {
				tt.setupHB(hb)
			}

			source := NewActivitySummarySource(hb, nil)
			assert.Equal(t, "activity-summary", source.Name())
			assert.Equal(t, 30*time.Minute, source.Interval())

			entries := source.Produce(context.Background())
			if tt.wantNil {
				assert.Nil(t, entries)
				return
			}

			require.Len(t, entries, 1)
			entry := entries[0]
			assert.Contains(t, entry.Content, tt.wantContent)
			assert.Equal(t, "ledger", entry.Scope)
			assert.Equal(t, whisperstore.WhisperTimeBased, entry.Type)
			assert.Equal(t, "activity-summary", entry.Source)
			assert.Equal(t, "activity", entry.Topic)
			assert.Equal(t, whisperstore.ImportanceAmbient, entry.Importance)
			assert.NotEmpty(t, entry.ID)
		})
	}
}

func TestActivitySummarySource_NilDeps(t *testing.T) {
	// both nil heartbeat and nil scheduler should not panic
	source := NewActivitySummarySource(nil, nil)
	entries := source.Produce(context.Background())
	assert.Nil(t, entries)
}
