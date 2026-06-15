package daemon

import (
	"sync"
	"time"
)

// ChangeType categorizes a filesystem change.
type ChangeType string

const (
	ChangeCreated  ChangeType = "created"
	ChangeModified ChangeType = "modified"
	ChangeDeleted  ChangeType = "deleted"
	ChangeRenamed  ChangeType = "renamed"
)

// Short returns a compact label for human-readable murmur content.
func (ct ChangeType) Short() string {
	switch ct {
	case ChangeCreated:
		return "new"
	case ChangeModified:
		return "mod"
	case ChangeDeleted:
		return "del"
	case ChangeRenamed:
		return "mv"
	default:
		return string(ct)
	}
}

// FileChange represents a single observed filesystem change.
type FileChange struct {
	Path       string // relative to project root
	ChangeType ChangeType
	IsDir      bool
	Timestamp  time.Time
}

// ChangeAccumulator batches classified filesystem changes into settled change
// sets. Inspired by Watchman's PendingCollection + settle mechanism. Changes are
// fed in by a producer (the GitPollWatcher) via AddChange; consumers either react
// to the onSettled callback (dirty-overlay debouncer) or pull batches with
// DrainSettled (file-change murmurs).
type ChangeAccumulator struct {
	mu           sync.Mutex
	pending      map[string]*FileChange // path -> latest change
	settled      []FileChange           // changes ready for consumption
	settlePeriod time.Duration
	settleTimer  *time.Timer
	stopped      bool
	onSettled    func() // called (in a goroutine) when pending changes settle
}

// NewChangeAccumulator creates an accumulator with the given settle period.
func NewChangeAccumulator(settlePeriod time.Duration) *ChangeAccumulator {
	return &ChangeAccumulator{
		pending:      make(map[string]*FileChange),
		settlePeriod: settlePeriod,
	}
}

// AddChange records a classified filesystem change, collapsing with any pending
// change for the same path. Producers pass a ChangeType directly — e.g.
// GitPollWatcher maps git status codes via porcelainChangeType — so the
// accumulator no longer re-derives the type from a raw fsnotify op.
func (a *ChangeAccumulator) AddChange(relPath string, changeType ChangeType, isDir bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.stopped {
		return
	}

	now := time.Now()

	existing, exists := a.pending[relPath]
	if exists {
		// collapse: apply aggregation rules
		changeType = a.collapseChange(existing.ChangeType, changeType)
		if changeType == "" {
			// create+delete = suppressed (temp file)
			delete(a.pending, relPath)
			a.resetSettleTimer()
			return
		}
	}

	a.pending[relPath] = &FileChange{
		Path:       relPath,
		ChangeType: changeType,
		IsDir:      isDir,
		Timestamp:  now,
	}
	a.resetSettleTimer()
}

// collapseChange applies Watchman-inspired aggregation rules.
func (a *ChangeAccumulator) collapseChange(existing, incoming ChangeType) ChangeType {
	switch {
	case existing == ChangeCreated && incoming == ChangeDeleted:
		// create then delete = temp file, suppress
		return ""
	case existing == ChangeCreated && incoming == ChangeModified:
		// create then write = still created
		return ChangeCreated
	case existing == ChangeDeleted && incoming == ChangeCreated:
		// delete then create = atomic save pattern
		return ChangeModified
	case existing == ChangeRenamed && incoming == ChangeCreated:
		return ChangeModified
	default:
		// default: use the latest event type
		return incoming
	}
}

// resetSettleTimer resets the settle timer. Must be called with mu held.
func (a *ChangeAccumulator) resetSettleTimer() {
	if a.settleTimer != nil {
		a.settleTimer.Stop()
	}
	a.settleTimer = time.AfterFunc(a.settlePeriod, func() {
		a.settle()
	})
}

// SetOnSettled registers a callback invoked (in a goroutine) each time pending
// changes settle. Used by the dirty overlay debouncer to trigger index rebuilds.
func (a *ChangeAccumulator) SetOnSettled(fn func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.onSettled = fn
}

// settle moves pending changes to settled. Called when settle timer fires.
func (a *ChangeAccumulator) settle() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.pending) == 0 {
		return
	}

	for _, fc := range a.pending {
		a.settled = append(a.settled, *fc)
	}
	a.pending = make(map[string]*FileChange)

	if a.onSettled != nil {
		go a.onSettled()
	}
}

// DrainSettled returns and clears all settled changes.
func (a *ChangeAccumulator) DrainSettled() []FileChange {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.settled) == 0 {
		return nil
	}
	result := a.settled
	a.settled = nil
	return result
}

// PendingCount returns the number of pending (unsettled) changes.
func (a *ChangeAccumulator) PendingCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.pending)
}

// Stop prevents further timer callbacks.
func (a *ChangeAccumulator) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopped = true
	if a.settleTimer != nil {
		a.settleTimer.Stop()
		a.settleTimer = nil
	}
}
