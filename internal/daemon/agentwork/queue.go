package agentwork

import (
	"container/heap"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// WorkItem represents a unit of background agent work.
type WorkItem struct {
	ID        string    // UUIDv7
	Type      string    // e.g. "session-finalize"
	Priority  int       // lower = higher priority
	Payload   any       // type-specific data
	CreatedAt time.Time
	Attempts  int
	LastErr   string
	DedupKey  string // only one item per dedup key at a time

	// index is maintained by the heap implementation; not exported.
	index int
}

// newItemID returns a time-sortable UUIDv7 string.
func newItemID() string {
	id, err := uuid.NewV7()
	if err != nil {
		// fall back to v4 if v7 generation fails (extremely unlikely)
		return uuid.New().String()
	}
	return id.String()
}

// ---- priority queue heap implementation ----

type itemHeap []*WorkItem

func (h itemHeap) Len() int { return len(h) }

func (h itemHeap) Less(i, j int) bool {
	if h[i].Priority != h[j].Priority {
		return h[i].Priority < h[j].Priority
	}
	// equal priority: FIFO by creation time
	return h[i].CreatedAt.Before(h[j].CreatedAt)
}

func (h itemHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *itemHeap) Push(x any) {
	item := x.(*WorkItem)
	item.index = len(*h)
	*h = append(*h, item)
}

func (h *itemHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil // avoid memory leak
	item.index = -1
	*h = old[:n-1]
	return item
}

// ---- WorkQueue ----

const maxQueueDepth = 100

// WorkQueue is a thread-safe, priority-ordered work queue with deduplication.
// It tracks both queued and in-progress items by dedup key so that the same
// logical work unit cannot be enqueued twice concurrently.
// The queue is capped at maxQueueDepth; excess items are rejected and
// re-detected on the next sync cycle.
type WorkQueue struct {
	mu         sync.Mutex
	items      itemHeap
	queued     map[string]struct{} // dedup keys currently in the queue
	inProgress map[string]struct{} // dedup keys currently being processed
	logger     *slog.Logger
}

// NewWorkQueue creates a ready-to-use WorkQueue.
func NewWorkQueue(logger *slog.Logger) *WorkQueue {
	if logger == nil {
		logger = slog.Default()
	}
	wq := &WorkQueue{
		queued:     make(map[string]struct{}),
		inProgress: make(map[string]struct{}),
		logger:     logger,
	}
	heap.Init(&wq.items)
	return wq
}

// Enqueue adds an item to the queue. It returns false without enqueuing if the
// dedup key is already present in the queue or in-progress set.
// The item's ID and CreatedAt are populated automatically if empty.
func (q *WorkQueue) Enqueue(item *WorkItem) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.items.Len() >= maxQueueDepth {
		q.logger.Warn("enqueue skipped: queue full", "depth", q.items.Len(), "max", maxQueueDepth, "dedup_key", item.DedupKey)
		return false
	}

	if item.DedupKey != "" {
		if _, ok := q.queued[item.DedupKey]; ok {
			q.logger.Debug("enqueue skipped: dedup key already queued", "dedup_key", item.DedupKey)
			return false
		}
		if _, ok := q.inProgress[item.DedupKey]; ok {
			q.logger.Debug("enqueue skipped: dedup key in progress", "dedup_key", item.DedupKey)
			return false
		}
	}

	if item.ID == "" {
		item.ID = newItemID()
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}

	heap.Push(&q.items, item)
	if item.DedupKey != "" {
		q.queued[item.DedupKey] = struct{}{}
	}
	q.logger.Debug("enqueued work item", "id", item.ID, "type", item.Type, "priority", item.Priority, "dedup_key", item.DedupKey)
	return true
}

// Dequeue removes and returns the highest-priority item, or nil if the queue
// is empty. The item's dedup key moves from the queued set to the in-progress set.
func (q *WorkQueue) Dequeue() *WorkItem {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.items.Len() == 0 {
		return nil
	}

	item := heap.Pop(&q.items).(*WorkItem)
	if item.DedupKey != "" {
		delete(q.queued, item.DedupKey)
		q.inProgress[item.DedupKey] = struct{}{}
	}
	return item
}

// Complete marks a dedup key as no longer in-progress, allowing the same key
// to be enqueued again in the future.
func (q *WorkQueue) Complete(dedupKey string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.inProgress, dedupKey)
}

// Requeue atomically moves a dedup key from in-progress back to queued.
// This prevents a race where a concurrent detectAndEnqueue could see the key
// absent from both sets and enqueue a duplicate.
func (q *WorkQueue) Requeue(item *WorkItem) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.items.Len() >= maxQueueDepth {
		q.logger.Warn("requeue skipped: queue full", "depth", q.items.Len(), "max", maxQueueDepth, "dedup_key", item.DedupKey)
		delete(q.inProgress, item.DedupKey)
		return false
	}

	// atomically: remove from inProgress, add to queued + heap
	delete(q.inProgress, item.DedupKey)

	if item.ID == "" {
		item.ID = newItemID()
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}

	heap.Push(&q.items, item)
	if item.DedupKey != "" {
		q.queued[item.DedupKey] = struct{}{}
	}
	q.logger.Debug("requeued work item", "id", item.ID, "type", item.Type, "priority", item.Priority, "dedup_key", item.DedupKey, "attempts", item.Attempts)
	return true
}

// InProgress reports whether the given dedup key is currently being processed.
func (q *WorkQueue) InProgress(dedupKey string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.inProgress[dedupKey]
	return ok
}

// Len returns the number of items waiting in the queue (excludes in-progress).
func (q *WorkQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.items.Len()
}
