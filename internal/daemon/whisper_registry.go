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
type WhisperRegistry struct {
	ledgerStore *whisperstore.Store
	teamStores  map[string]*whisperstore.Store // teamID -> store
	mu          sync.RWMutex
	logger      *slog.Logger
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
		logger:      logger,
	}
}

// AddTeamStore registers a team whisper store.
func (r *WhisperRegistry) AddTeamStore(teamID string, store *whisperstore.Store) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.teamStores[teamID] = store
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

	switch scope {
	case "ledger":
		if r.ledgerStore == nil {
			return fmt.Errorf("no ledger store configured")
		}
		return r.ledgerStore.Add(entries...)
	case "team":
		// team entries go to all team stores (each daemon manages its own view)
		r.mu.RLock()
		defer r.mu.RUnlock()
		for teamID, store := range r.teamStores {
			if err := store.Add(entries...); err != nil {
				r.logger.Warn("failed to add to team store", "team_id", teamID, "err", err)
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown scope: %s", scope)
	}
}

// GetWhispers queries ALL stores and merges results, sorted by importance then time.
func (r *WhisperRegistry) GetWhispers(agentID string, attention whisperstore.Attention, topics []string) ([]whisperstore.WhisperEntry, error) {
	var all []whisperstore.WhisperEntry

	// query ledger store
	if r.ledgerStore != nil {
		entries, err := r.ledgerStore.GetWhispers(agentID, attention, topics)
		if err != nil {
			r.logger.Warn("ledger whisper query failed", "err", err)
		} else {
			all = append(all, entries...)
		}
	}

	// query all team stores
	r.mu.RLock()
	stores := make(map[string]*whisperstore.Store, len(r.teamStores))
	for k, v := range r.teamStores {
		stores[k] = v
	}
	r.mu.RUnlock()

	for teamID, store := range stores {
		entries, err := store.GetWhispers(agentID, attention, topics)
		if err != nil {
			r.logger.Warn("team whisper query failed", "team_id", teamID, "err", err)
			continue
		}
		all = append(all, entries...)
	}

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

	var all []whisperstore.WhisperEntry

	if r.ledgerStore != nil {
		// fetch limit+1 per store so we can detect hasMore after merge
		entries, _, err := r.ledgerStore.GetWhispersPage(agentID, before, limit+1)
		if err != nil {
			r.logger.Warn("ledger whisper history query failed", "err", err)
		} else {
			all = append(all, entries...)
		}
	}

	r.mu.RLock()
	stores := make(map[string]*whisperstore.Store, len(r.teamStores))
	for k, v := range r.teamStores {
		stores[k] = v
	}
	r.mu.RUnlock()

	for teamID, store := range stores {
		entries, _, err := store.GetWhispersPage(agentID, before, limit+1)
		if err != nil {
			r.logger.Warn("team whisper history query failed", "team_id", teamID, "err", err)
			continue
		}
		all = append(all, entries...)
	}

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
	if r.ledgerStore == nil {
		return time.Time{}, nil
	}
	return r.ledgerStore.GetCursor(agentID)
}

// IsRelayed checks if a murmur has been relayed in the appropriate store.
func (r *WhisperRegistry) IsRelayed(murmurID, scope string) (bool, error) {
	store := r.storeForScope(scope)
	if store == nil {
		return false, nil
	}
	return store.IsRelayed(murmurID, scope)
}

// MarkRelayed records that a murmur has been relayed.
func (r *WhisperRegistry) MarkRelayed(murmurID, scope string) error {
	store := r.storeForScope(scope)
	if store == nil {
		return nil
	}
	return store.MarkRelayed(murmurID, scope)
}

// RemoveCursor removes an agent's cursor from all stores.
func (r *WhisperRegistry) RemoveCursor(agentID string) {
	if r.ledgerStore != nil {
		r.ledgerStore.RemoveCursor(agentID)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, store := range r.teamStores {
		store.RemoveCursor(agentID)
	}
}

// Prune runs cleanup on all stores.
func (r *WhisperRegistry) Prune(retention time.Duration) {
	if r.ledgerStore != nil {
		result, err := r.ledgerStore.Prune(retention)
		if err != nil {
			r.logger.Warn("ledger whisper prune failed", "err", err)
		} else if result.WhispersDeleted > 0 {
			r.logger.Debug("ledger whisper prune", "deleted", result.WhispersDeleted, "vacuumed", result.Vacuumed)
		}
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
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
func (r *WhisperRegistry) EnforceMaxSize(maxBytes int64) {
	if r.ledgerStore != nil {
		if err := r.ledgerStore.EnforceMaxSize(maxBytes); err != nil {
			r.logger.Warn("ledger whisper size enforcement failed", "err", err)
		}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
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

// Close closes all stores.
func (r *WhisperRegistry) Close() error {
	var firstErr error
	if r.ledgerStore != nil {
		if err := r.ledgerStore.Close(); err != nil {
			firstErr = err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, store := range r.teamStores {
		if err := store.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// storeForScope returns the store that handles the given scope.
// For "team", returns the ledger store (relayed records are co-located).
// Returns nil for unknown scopes — callers handle nil gracefully.
func (r *WhisperRegistry) storeForScope(scope string) *whisperstore.Store {
	switch scope {
	case "ledger":
		return r.ledgerStore
	case "team":
		// team relay tracking goes to ledger store (single writer)
		return r.ledgerStore
	default:
		return nil
	}
}
