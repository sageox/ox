package daemon

import (
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// WhisperRegistry aggregates whisper stores across scopes (ledger + team)
// and provides a unified API for adding and querying whispers.
//
// The daemon creates one WhisperRegistry at startup. It wraps:
//   - One ledger whisper store (single-writer, owned by this daemon)
//   - Zero or more team whisper stores (shared across daemons via WAL)
//
// Team stores are tracked with a last-access timestamp so an idle-close
// janitor can release their SQLite file descriptors. Whisper reads/writes
// happen on the order of minutes, so holding every team DB open between
// operations would leak ~3 FDs per team for no benefit.
type WhisperRegistry struct {
	ledgerStore *whisperstore.Store
	teamStores  map[string]*whisperstore.Store // teamID -> store
	lastAccess  map[string]time.Time           // teamID -> last touch (open/read/write)
	mu          sync.RWMutex
	logger      *slog.Logger
	now         func() time.Time // injectable clock for tests
}

// NewWhisperRegistry creates a new registry with the given ledger store.
// Team stores can be added later via AddTeamStore.
func NewWhisperRegistry(ledgerStore *whisperstore.Store, logger *slog.Logger) *WhisperRegistry {
	if logger == nil {
		logger = slog.Default()
	}
	return &WhisperRegistry{
		ledgerStore: ledgerStore,
		teamStores:  make(map[string]*whisperstore.Store),
		lastAccess:  make(map[string]time.Time),
		logger:      logger,
		now:         time.Now,
	}
}

// SetClock overrides the clock used for idle-close tracking. Test-only.
func (r *WhisperRegistry) SetClock(now func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now == nil {
		now = time.Now
	}
	r.now = now
}

// AddTeamStore registers a team whisper store.
func (r *WhisperRegistry) AddTeamStore(teamID string, store *whisperstore.Store) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.teamStores[teamID] = store
	r.lastAccess[teamID] = r.now()
}

// touchTeamLocked bumps the last-access timestamp for a team store.
//
// Caller MUST hold r.mu.Lock() (write lock). RLock is not sufficient —
// this is a Go map write, which requires mutual exclusion against any
// concurrent map access. All current callers (Add for the "team" scope,
// GetWhispers, GetWhispersPage) hold the write lock for the full
// iteration; do not weaken that contract.
func (r *WhisperRegistry) touchTeamLocked(teamID string) {
	r.lastAccess[teamID] = r.now()
}

// TeamIDs returns the set of team IDs with currently-open stores.
func (r *WhisperRegistry) TeamIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.teamStores))
	for id := range r.teamStores {
		ids = append(ids, id)
	}
	return ids
}

// HasTeamStore returns true if a team whisper store is already registered.
func (r *WhisperRegistry) HasTeamStore(teamID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.teamStores[teamID]
	return ok
}

// Add routes entries to the correct store based on scope.
func (r *WhisperRegistry) Add(scope string, entries ...whisperstore.WhisperEntry) error {
	if len(entries) == 0 {
		return nil
	}

	r.mu.RLock()
	ledger := r.ledgerStore
	r.mu.RUnlock()

	switch scope {
	case "ledger":
		if ledger == nil {
			return fmt.Errorf("no ledger store configured")
		}
		return ledger.Add(entries...)
	case "team":
		// team entries go to all team stores (each daemon manages its own view)
		r.mu.Lock()
		defer r.mu.Unlock()
		for teamID, store := range r.teamStores {
			if err := store.Add(entries...); err != nil {
				r.logger.Warn("failed to add to team store", "team_id", teamID, "err", err)
				continue
			}
			r.touchTeamLocked(teamID)
		}
		return nil
	default:
		return fmt.Errorf("unknown scope: %s", scope)
	}
}

// GetWhispers queries ALL stores and merges results, sorted by importance then time.
func (r *WhisperRegistry) GetWhispers(agentID string, attention whisperstore.Attention, topics []string) ([]whisperstore.WhisperEntry, error) {
	// snapshot both stores under one write lock — ReopenLedgerStore holds a write lock
	// while replacing r.ledgerStore, and we bump lastAccess for touched team stores.
	r.mu.Lock()
	ledger := r.ledgerStore
	stores := make(map[string]*whisperstore.Store, len(r.teamStores))
	for k, v := range r.teamStores {
		stores[k] = v
	}

	var all []whisperstore.WhisperEntry

	if ledger != nil {
		entries, err := ledger.GetWhispers(agentID, attention, topics)
		if err != nil {
			r.logger.Warn("ledger whisper query failed", "err", err)
		} else {
			all = append(all, entries...)
		}
	}

	for teamID, store := range stores {
		entries, err := store.GetWhispers(agentID, attention, topics)
		if err != nil {
			r.logger.Warn("team whisper query failed", "team_id", teamID, "err", err)
			continue
		}
		r.touchTeamLocked(teamID)
		all = append(all, entries...)
	}
	r.mu.Unlock()

	if all == nil {
		all = []whisperstore.WhisperEntry{}
	}
	return all, nil
}

// GetAllWhispers queries ALL stores and merges all whispers without advancing cursors.
// Used for inspection — shows pending and already-delivered whispers.
func (r *WhisperRegistry) GetAllWhispers(agentID string) ([]whisperstore.WhisperEntry, error) {
	entries, _, err := r.GetWhispersPage(agentID, time.Time{}, 0)
	return entries, err
}

// GetWhispersPage returns a paginated view of all whispers across all stores.
// before: if non-zero, only entries with created_at strictly before this time.
// limit: max entries to return; 0 uses the store's default (50).
// Merges results from ledger and all team stores, sorts by created_at DESC, and truncates.
// Returns (entries, hasMore, error).
func (r *WhisperRegistry) GetWhispersPage(agentID string, before time.Time, limit int) ([]whisperstore.WhisperEntry, bool, error) {
	const defaultLimit = 50
	const maxLimit = 200
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	r.mu.Lock()
	ledger := r.ledgerStore
	stores := make(map[string]*whisperstore.Store, len(r.teamStores))
	for k, v := range r.teamStores {
		stores[k] = v
	}

	var all []whisperstore.WhisperEntry

	if ledger != nil {
		// fetch limit+1 per store so we can detect hasMore after merge
		entries, _, err := ledger.GetWhispersPage(agentID, before, limit+1)
		if err != nil {
			r.logger.Warn("ledger whisper history query failed", "err", err)
		} else {
			all = append(all, entries...)
		}
	}

	for teamID, store := range stores {
		entries, _, err := store.GetWhispersPage(agentID, before, limit+1)
		if err != nil {
			r.logger.Warn("team whisper history query failed", "team_id", teamID, "err", err)
			continue
		}
		r.touchTeamLocked(teamID)
		all = append(all, entries...)
	}
	r.mu.Unlock()

	// sort merged results by created_at DESC
	slices.SortFunc(all, func(a, b whisperstore.WhisperEntry) int {
		if a.CreatedAt.After(b.CreatedAt) {
			return -1
		}
		if b.CreatedAt.After(a.CreatedAt) {
			return 1
		}
		return 0
	})

	hasMore := len(all) > limit
	if hasMore {
		all = all[:limit]
	}
	if all == nil {
		all = []whisperstore.WhisperEntry{}
	}
	return all, hasMore, nil
}

// GetCursor returns the agent's cursor from the ledger store (earliest cursor wins for display).
func (r *WhisperRegistry) GetCursor(agentID string) (time.Time, error) {
	r.mu.RLock()
	ledger := r.ledgerStore
	r.mu.RUnlock()

	if ledger == nil {
		return time.Time{}, nil
	}
	return ledger.GetCursor(agentID)
}

// IsRelayed checks if a murmur has been relayed in the appropriate store.
func (r *WhisperRegistry) IsRelayed(murmurID, scope string) (bool, error) {
	r.mu.RLock()
	store := r.storeForScopeLocked(scope)
	r.mu.RUnlock()

	if store == nil {
		return false, nil
	}
	return store.IsRelayed(murmurID, scope)
}

// MarkRelayed records that a murmur has been relayed.
func (r *WhisperRegistry) MarkRelayed(murmurID, scope string) error {
	r.mu.RLock()
	store := r.storeForScopeLocked(scope)
	r.mu.RUnlock()

	if store == nil {
		return nil
	}
	return store.MarkRelayed(murmurID, scope)
}

// RemoveCursor removes an agent's cursor from all stores.
//
// Holds the registry read lock through the entire iteration so the idle-close
// janitor (which acquires the write lock to call store.Close) cannot race
// with us and free a *whisperstore.Store mid-operation.
func (r *WhisperRegistry) RemoveCursor(agentID string) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.ledgerStore != nil {
		r.ledgerStore.RemoveCursor(agentID)
	}
	for _, store := range r.teamStores {
		store.RemoveCursor(agentID)
	}
}

// Prune runs cleanup on all stores.
//
// Holds the registry read lock through the entire iteration; see RemoveCursor.
// Prune is a once-an-hour background task, so serializing it against the
// idle-close janitor (5 min cadence) is cheap.
func (r *WhisperRegistry) Prune(retention time.Duration) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.ledgerStore != nil {
		result, err := r.ledgerStore.Prune(retention)
		if err != nil {
			r.logger.Warn("ledger whisper prune failed", "err", err)
		} else if result.WhispersDeleted > 0 {
			r.logger.Debug("ledger whisper prune", "deleted", result.WhispersDeleted, "vacuumed", result.Vacuumed)
		}
	}

	for teamID, store := range r.teamStores {
		result, err := store.Prune(retention)
		if err != nil {
			r.logger.Warn("team whisper prune failed", "team_id", teamID, "err", err)
		} else if result.WhispersDeleted > 0 {
			r.logger.Debug("team whisper prune", "team_id", teamID, "deleted", result.WhispersDeleted)
		}
	}
}

// EnforceMaxSize runs size enforcement on all stores.
//
// Holds the registry read lock through the entire iteration; see RemoveCursor.
func (r *WhisperRegistry) EnforceMaxSize(maxBytes int64) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.ledgerStore != nil {
		if err := r.ledgerStore.EnforceMaxSize(maxBytes); err != nil {
			r.logger.Warn("ledger whisper size enforcement failed", "err", err)
		}
	}
	for teamID, store := range r.teamStores {
		if err := store.EnforceMaxSize(maxBytes); err != nil {
			r.logger.Warn("team whisper size enforcement failed", "team_id", teamID, "err", err)
		}
	}
}

// LedgerStore returns the underlying ledger whisper store.
// Used by DB maintenance to run pruning directly on the store.
func (r *WhisperRegistry) LedgerStore() *whisperstore.Store {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ledgerStore
}

// ReopenLedgerStore closes the current ledger store and opens a new one at dbPath.
// Called after GC reclone swaps the ledger directory — the old sql.DB handle is stale
// because the underlying inode was deleted during the rename-swap.
func (r *WhisperRegistry) ReopenLedgerStore(dbPath string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.ledgerStore != nil {
		r.ledgerStore.Close()
		r.ledgerStore = nil
	}

	store, err := whisperstore.Open(dbPath)
	if err != nil {
		r.logger.Error("failed to reopen ledger whisper store", "path", dbPath, "error", err)
		return err
	}

	r.ledgerStore = store
	r.logger.Info("ledger whisper store reopened after GC", "path", dbPath)
	return nil
}

// CloseLedgerStore closes the ledger whisper store and clears the reference.
// Called by GC before the workspace rename so the OS can release SQLite's
// mmap on -shm/-wal files. Without this, the kernel pins the orphan inode
// (POSIX unlink + open mmap) and FDs accumulate across reclones.
func (r *WhisperRegistry) CloseLedgerStore() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ledgerStore != nil {
		_ = r.ledgerStore.Close()
		r.ledgerStore = nil
	}
}

// CloseTeamStore closes and removes a team whisper store. The next sync of the
// team workspace re-opens it via AddTeamStore. Called by GC before the
// workspace rename so SQLite releases its mmap on the to-be-orphaned files,
// by the doTeamSync reconcile loop when a team disappears from config, and
// by the idle-close janitor.
func (r *WhisperRegistry) CloseTeamStore(teamID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if store, ok := r.teamStores[teamID]; ok {
		_ = store.Close()
		delete(r.teamStores, teamID)
	}
	delete(r.lastAccess, teamID)
}

// CloseIdleTeamStores closes any team whisper store that hasn't been touched
// for at least the given threshold. Whisper traffic is sparse (an "every few
// minutes" workload), so an idle store is pinning ~3 SQLite file descriptors
// for nothing — reopen on next use is cheap.
//
// The ledger store is never subject to idle-close: it's the daemon's
// continuously-written sink for activity summaries, file-change murmurs,
// and IPC reads.
//
// Returns the number of stores closed (useful for tests and metrics).
func (r *WhisperRegistry) CloseIdleTeamStores(threshold time.Duration) int {
	if threshold <= 0 {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := r.now().Add(-threshold)
	closed := 0
	for teamID, store := range r.teamStores {
		last, ok := r.lastAccess[teamID]
		if !ok {
			// store missing from lastAccess is unexpected — seed it and skip
			r.lastAccess[teamID] = r.now()
			continue
		}
		if last.After(cutoff) {
			continue
		}
		if err := store.Close(); err != nil {
			r.logger.Warn("failed to close idle team whisper store",
				"team_id", teamID, "idle_for", r.now().Sub(last), "err", err)
			continue
		}
		delete(r.teamStores, teamID)
		delete(r.lastAccess, teamID)
		closed++
		r.logger.Debug("closed idle team whisper store",
			"team_id", teamID, "idle_for", r.now().Sub(last))
	}
	return closed
}

// Close closes all stores.
func (r *WhisperRegistry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var firstErr error
	if r.ledgerStore != nil {
		if err := r.ledgerStore.Close(); err != nil {
			firstErr = err
		}
	}
	for teamID, store := range r.teamStores {
		if err := store.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(r.teamStores, teamID)
		delete(r.lastAccess, teamID)
	}
	return firstErr
}

// storeForScopeLocked returns the store that handles the given scope.
// For "team", returns the ledger store (relayed records are co-located).
// Returns nil for unknown scopes — callers handle nil gracefully.
// Caller must hold r.mu (at least RLock).
func (r *WhisperRegistry) storeForScopeLocked(scope string) *whisperstore.Store {
	switch scope {
	case "ledger", "team":
		// team relay tracking goes to ledger store (single writer)
		return r.ledgerStore
	default:
		return nil
	}
}
