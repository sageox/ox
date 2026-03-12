package agentwork

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkQueue_EnqueueDequeue(t *testing.T) {
	tests := []struct {
		name       string
		items      []*WorkItem
		wantOrder  []string // expected dequeue order by Type
		wantLen    int      // queue length after all enqueues
	}{
		{
			name: "single item",
			items: []*WorkItem{
				{Type: "a", Priority: 1, DedupKey: "k1"},
			},
			wantOrder: []string{"a"},
			wantLen:   1,
		},
		{
			name: "priority ordering",
			items: []*WorkItem{
				{Type: "low", Priority: 3, DedupKey: "k1"},
				{Type: "high", Priority: 1, DedupKey: "k2"},
				{Type: "mid", Priority: 2, DedupKey: "k3"},
			},
			wantOrder: []string{"high", "mid", "low"},
			wantLen:   3,
		},
		{
			name: "equal priority uses FIFO",
			items: []*WorkItem{
				{Type: "first", Priority: 1, DedupKey: "k1", CreatedAt: time.Now()},
				{Type: "second", Priority: 1, DedupKey: "k2", CreatedAt: time.Now().Add(time.Millisecond)},
			},
			wantOrder: []string{"first", "second"},
			wantLen:   2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := NewWorkQueue(nil)

			for _, item := range tc.items {
				ok := q.Enqueue(item)
				require.True(t, ok, "enqueue should succeed for %s", item.Type)
			}

			assert.Equal(t, tc.wantLen, q.Len())

			for i, wantType := range tc.wantOrder {
				got := q.Dequeue()
				require.NotNil(t, got, "dequeue %d should not be nil", i)
				assert.Equal(t, wantType, got.Type)
				assert.NotEmpty(t, got.ID, "ID should be auto-populated")
			}

			assert.Nil(t, q.Dequeue(), "dequeue from empty queue should return nil")
		})
	}
}

func TestWorkQueue_Dedup(t *testing.T) {
	t.Run("rejects duplicate dedup key", func(t *testing.T) {
		q := NewWorkQueue(nil)

		ok := q.Enqueue(&WorkItem{Type: "a", Priority: 1, DedupKey: "dup"})
		assert.True(t, ok)

		ok = q.Enqueue(&WorkItem{Type: "b", Priority: 1, DedupKey: "dup"})
		assert.False(t, ok, "second enqueue with same dedup key should be rejected")

		assert.Equal(t, 1, q.Len())
	})

	t.Run("rejects enqueue while in progress", func(t *testing.T) {
		q := NewWorkQueue(nil)

		q.Enqueue(&WorkItem{Type: "a", Priority: 1, DedupKey: "dup"})
		q.Dequeue() // moves to in-progress

		ok := q.Enqueue(&WorkItem{Type: "b", Priority: 1, DedupKey: "dup"})
		assert.False(t, ok, "enqueue should be rejected while key is in progress")
	})

	t.Run("complete allows re-enqueue", func(t *testing.T) {
		q := NewWorkQueue(nil)

		q.Enqueue(&WorkItem{Type: "a", Priority: 1, DedupKey: "dup"})
		q.Dequeue()
		q.Complete("dup")

		ok := q.Enqueue(&WorkItem{Type: "b", Priority: 1, DedupKey: "dup"})
		assert.True(t, ok, "enqueue should succeed after Complete()")
		assert.Equal(t, 1, q.Len())
	})

	t.Run("empty dedup key allows duplicates", func(t *testing.T) {
		q := NewWorkQueue(nil)

		ok1 := q.Enqueue(&WorkItem{Type: "a", Priority: 1})
		ok2 := q.Enqueue(&WorkItem{Type: "b", Priority: 1})
		assert.True(t, ok1)
		assert.True(t, ok2)
		assert.Equal(t, 2, q.Len())
	})
}

func TestWorkQueue_InProgress(t *testing.T) {
	q := NewWorkQueue(nil)

	q.Enqueue(&WorkItem{Type: "a", Priority: 1, DedupKey: "ip"})
	assert.False(t, q.InProgress("ip"), "should not be in-progress before dequeue")

	q.Dequeue()
	assert.True(t, q.InProgress("ip"), "should be in-progress after dequeue")

	q.Complete("ip")
	assert.False(t, q.InProgress("ip"), "should not be in-progress after complete")
}

func TestWorkQueue_LenAccuracy(t *testing.T) {
	q := NewWorkQueue(nil)
	assert.Equal(t, 0, q.Len())

	q.Enqueue(&WorkItem{Type: "a", Priority: 1, DedupKey: "k1"})
	q.Enqueue(&WorkItem{Type: "b", Priority: 2, DedupKey: "k2"})
	assert.Equal(t, 2, q.Len())

	q.Dequeue()
	assert.Equal(t, 1, q.Len(), "Len should decrease after dequeue")

	q.Dequeue()
	assert.Equal(t, 0, q.Len())
}

func TestWorkQueue_ConcurrentAccess(t *testing.T) {
	q := NewWorkQueue(nil)
	const goroutines = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(n int) {
			defer wg.Done()
			// each goroutine enqueues with a unique dedup key
			q.Enqueue(&WorkItem{
				Type:     "concurrent",
				Priority: n % 5,
				DedupKey: "key-" + time.Now().String() + "-" + string(rune(n)),
			})
		}(i)
	}
	wg.Wait()

	// drain the queue; no panics or races is the primary assertion
	dequeued := 0
	for q.Dequeue() != nil {
		dequeued++
	}
	assert.Greater(t, dequeued, 0, "should have dequeued at least one item")
}
