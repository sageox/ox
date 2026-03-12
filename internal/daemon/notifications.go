package daemon

import (
	"log/slog"
	"slices"
	"sync"
	"time"
)

// ChangeEntry tracks a single file change in a team context.
type ChangeEntry struct {
	Path      string    `json:"path"`       // relative file path within team context
	ChangedAt time.Time `json:"changed_at"` // when change was detected
	TeamID    string    `json:"team_id"`
	TeamName  string    `json:"team_name"`
}

// NotificationStore tracks file changes and per-agent read cursors.
//
// Design principles:
//   - Thread-safe: daemon sync loop and IPC handlers access concurrently
//   - Bounded: maxEntries cap prevents unbounded memory growth
//   - Deduped: same (path, teamID) pair updates in place rather than appending
//   - Primary team only: currently only records changes for the project's primary team
//
// Cursor cleanup is driven externally by InstanceStore — when an agent is removed
// as stale, InstanceStore calls RemoveCursor to clean up the notification cursor.
// This avoids a separate cleanup goroutine and ties cursor lifetime to agent lifetime.
//
// The daemon uses NotificationStore to:
//   - Record which team context files changed after git pull
//   - Let agents poll for changes since their last check
//   - Detect when an agent's cursor has fallen behind the buffer
type NotificationStore struct {
	mu         sync.RWMutex
	entries    []ChangeEntry        // sorted by ChangedAt ascending, capped at maxEntries
	cursors    map[string]time.Time // agentID -> last checked timestamp
	maxEntries int
	evicted    bool // true if any entries have been evicted due to capacity
}

// NewNotificationStore creates a new notification store with the given capacity.
func NewNotificationStore(maxEntries int) *NotificationStore {
	if maxEntries <= 0 {
		maxEntries = 1000
	}
	return &NotificationStore{
		entries:    make([]ChangeEntry, 0, maxEntries),
		cursors:    make(map[string]time.Time),
		maxEntries: maxEntries,
	}
}

// RecordChanges records file changes detected after a successful git pull.
// Deduplicates by (path, teamID) — if the same file changes again, its timestamp updates.
// Entries exceeding maxEntries are evicted oldest-first.
func (ns *NotificationStore) RecordChanges(files []string, teamID, teamName string) {
	if len(files) == 0 {
		return
	}

	ns.mu.Lock()
	defer ns.mu.Unlock()

	now := time.Now()

	for _, f := range files {
		updated := false
		for i := range ns.entries {
			if ns.entries[i].Path == f && ns.entries[i].TeamID == teamID {
				ns.entries[i].ChangedAt = now
				ns.entries[i].TeamName = teamName
				updated = true
				break
			}
		}
		if !updated {
			ns.entries = append(ns.entries, ChangeEntry{
				Path:      f,
				ChangedAt: now,
				TeamID:    teamID,
				TeamName:  teamName,
			})
		}
	}

	// re-sort by ChangedAt ascending after updates may have moved entries
	slices.SortFunc(ns.entries, func(a, b ChangeEntry) int {
		return a.ChangedAt.Compare(b.ChangedAt)
	})

	// evict oldest if over capacity
	if len(ns.entries) > ns.maxEntries {
		ns.entries = ns.entries[len(ns.entries)-ns.maxEntries:]
		ns.evicted = true
	}

	slog.Debug("notification changes recorded", "team", teamName, "count", len(files))
}

// GetNotifications returns change entries newer than the agent's cursor.
// On first call for an unknown agent, auto-registers the cursor at time.Now()
// and returns empty (the agent should have read context during prime).
//
// Returns (entries, stale) where stale=true means entries were evicted from
// the buffer since the agent last checked — some changes may have been lost.
func (ns *NotificationStore) GetNotifications(agentID string) ([]ChangeEntry, bool) {
	ns.mu.Lock()
	defer ns.mu.Unlock()

	now := time.Now()

	cursor, exists := ns.cursors[agentID]
	if !exists {
		// first query: register cursor at now, return empty
		ns.cursors[agentID] = now
		return nil, false
	}

	if len(ns.entries) == 0 {
		ns.cursors[agentID] = now
		return nil, false
	}

	// stale only if eviction has actually occurred AND cursor is before oldest entry.
	// Without eviction, all changes are still in the buffer — nothing was lost.
	stale := ns.evicted && cursor.Before(ns.entries[0].ChangedAt)

	// collect entries newer than cursor
	var result []ChangeEntry
	for _, e := range ns.entries {
		if e.ChangedAt.After(cursor) {
			result = append(result, e)
		}
	}

	ns.cursors[agentID] = now
	return result, stale
}

// RemoveCursor removes a specific agent's cursor.
// Called by InstanceStore when an agent is cleaned up as stale.
func (ns *NotificationStore) RemoveCursor(agentID string) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	delete(ns.cursors, agentID)
}

// EntryCount returns the number of change entries in the buffer.
func (ns *NotificationStore) EntryCount() int {
	ns.mu.RLock()
	defer ns.mu.RUnlock()
	return len(ns.entries)
}

// CursorCount returns the number of tracked agent cursors.
func (ns *NotificationStore) CursorCount() int {
	ns.mu.RLock()
	defer ns.mu.RUnlock()
	return len(ns.cursors)
}
