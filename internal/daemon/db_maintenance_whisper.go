package daemon

import (
	"context"
	"os"
	"time"

	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// WhisperDBMaintainer wraps a WhisperRegistry for the DBMaintainer interface.
// It runs pruning and size enforcement on ALL stores (ledger + team) via the
// registry's Prune() and EnforceMaxSize() methods. Integrity checks and size
// measurement use the ledger store specifically.
type WhisperDBMaintainer struct {
	name      string
	registry  *WhisperRegistry
	retention time.Duration
	maxBytes  int64
}

// NewWhisperDBMaintainer creates a maintainer that covers all whisper stores
// (ledger + team) via the registry.
func NewWhisperDBMaintainer(name string, registry *WhisperRegistry, retention time.Duration, maxBytes int64) *WhisperDBMaintainer {
	return &WhisperDBMaintainer{
		name:      name,
		registry:  registry,
		retention: retention,
		maxBytes:  maxBytes,
	}
}

func (m *WhisperDBMaintainer) Name() string { return m.name }

func (m *WhisperDBMaintainer) Maintain(_ context.Context) DBMaintenanceResult {
	start := time.Now()
	result := DBMaintenanceResult{
		Name:       m.name,
		SizeBefore: -1,
		SizeAfter:  -1,
		Healthy:    true,
	}

	// measure ledger store size before (primary indicator)
	ledger := m.registry.LedgerStore()
	var dbPath string
	if ledger != nil {
		dbPath = ledger.DBPath()
		if info, err := os.Stat(dbPath); err == nil {
			result.SizeBefore = info.Size()
		}

		// integrity check on ledger store
		if err := ledger.CheckIntegrity(); err != nil {
			result.Healthy = false
		}
	}

	// prune ALL stores (ledger + team) via registry
	m.registry.Prune(m.retention)

	// enforce size cap on ALL stores (ledger + team) via registry
	m.registry.EnforceMaxSize(m.maxBytes)

	// measure ledger store size after
	if dbPath != "" {
		if info, err := os.Stat(dbPath); err == nil {
			result.SizeAfter = info.Size()
		}
	}

	result.Duration = time.Since(start)
	return result
}

// newWhisperDBMaintainerForStore creates a maintainer for a single whisper store.
// Used in tests that need to test maintenance on an isolated store.
func newWhisperDBMaintainerForStore(name string, store *whisperstore.Store, retention time.Duration, maxBytes int64) *singleWhisperDBMaintainer {
	return &singleWhisperDBMaintainer{name: name, store: store, retention: retention, maxBytes: maxBytes}
}

type singleWhisperDBMaintainer struct {
	name      string
	store     *whisperstore.Store
	retention time.Duration
	maxBytes  int64
}

func (m *singleWhisperDBMaintainer) Name() string { return m.name }

func (m *singleWhisperDBMaintainer) Maintain(_ context.Context) DBMaintenanceResult {
	start := time.Now()
	result := DBMaintenanceResult{
		Name:       m.name,
		SizeBefore: -1,
		SizeAfter:  -1,
		Healthy:    true,
	}

	dbPath := m.store.DBPath()
	if info, err := os.Stat(dbPath); err == nil {
		result.SizeBefore = info.Size()
	}
	if err := m.store.CheckIntegrity(); err != nil {
		result.Healthy = false
	}
	pruneResult, err := m.store.Prune(m.retention)
	if err != nil {
		result.Error = err
		result.Duration = time.Since(start)
		return result
	}
	result.Pruned = pruneResult.WhispersDeleted + pruneResult.RelayedDeleted + pruneResult.CursorsDeleted
	result.Vacuumed = pruneResult.Vacuumed
	if err := m.store.EnforceMaxSize(m.maxBytes); err != nil {
		result.Error = err
		result.Duration = time.Since(start)
		return result
	}
	if info, err := os.Stat(dbPath); err == nil {
		result.SizeAfter = info.Size()
	}
	result.Duration = time.Since(start)
	return result
}
