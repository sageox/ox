package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/store"
)

// CodeDBMaintainer wraps a CodeDBManager for the DBMaintainer interface.
// Opens each store on demand for maintenance since CodeDBManager doesn't hold a persistent handle.
//
// One maintainer covers both code indexes the daemon keeps: the shared index
// and the ledger index. The ledger index is the larger of the two and grows
// with every ledger build, so leaving it out is how it reached 5 GB.
type CodeDBMaintainer struct {
	name    string
	manager *CodeDBManager
}

// NewCodeDBMaintainer creates a maintainer for a codedb store.
func NewCodeDBMaintainer(name string, manager *CodeDBManager) *CodeDBMaintainer {
	return &CodeDBMaintainer{name: name, manager: manager}
}

func (m *CodeDBMaintainer) Name() string { return m.name }

func (m *CodeDBMaintainer) Maintain(ctx context.Context) DBMaintenanceResult {
	start := time.Now()
	result := DBMaintenanceResult{
		Name:       m.name,
		Healthy:    true,
		SizeBefore: -1,
		SizeAfter:  -1,
	}

	if m.manager == nil {
		result.Healthy = false
		result.Duration = time.Since(start)
		return result
	}

	var errs []error
	for _, dir := range m.manager.maintenanceDirs() {
		if ctx.Err() != nil {
			break
		}
		if _, err := os.Stat(filepath.Join(dir, store.MetadataDBFile)); os.IsNotExist(err) {
			// no index exists yet — nothing to maintain, and opening would create one
			continue
		}
		if m.manager.buildActiveFor(dir) {
			// the build holds the write lock for minutes; try again next cycle
			m.manager.logger.Debug("codedb maintenance deferred, index build active", "data_dir", dir)
			continue
		}
		if err := m.maintainDir(ctx, dir, &result); err != nil {
			errs = append(errs, fmt.Errorf("maintain %s: %w", dir, err))
		}
	}
	result.Error = errors.Join(errs...)
	result.Duration = time.Since(start)
	return result
}

// maintainDir runs store maintenance on one codedb directory and folds the
// outcome into result.
func (m *CodeDBMaintainer) maintainDir(ctx context.Context, dir string, result *DBMaintenanceResult) error {
	// SQLite only: maintenance never touches bleve, and opening it would contend
	// with the indexer for bbolt's exclusive lock.
	db, err := codedb.OpenSQLOnly(dir)
	if err != nil {
		if errors.Is(err, store.ErrCorrupt) {
			// Open met damage and removed the database; make the next pass rebuild.
			m.manager.discardCorruptIndex(dir, err)
			result.Healthy = false
			result.Healed = true
			return nil
		}
		result.Healthy = false
		return err
	}
	// closed explicitly on the corruption path below, which must not hold the
	// files open while they are removed; Close is idempotent
	defer db.Close()

	storeResult := db.Store().Maintain(ctx)

	result.Pruned += storeResult.TotalPruned()
	result.Vacuumed = result.Vacuumed || storeResult.Vacuumed
	addSize(&result.SizeBefore, storeResult.SizeBefore)
	addSize(&result.SizeAfter, storeResult.SizeAfter)

	if storeResult.IntegrityOK {
		return nil
	}
	result.Healthy = false
	if licensesDiscard(storeResult.IntegrityErr) {
		_ = db.Close()
		m.manager.discardCorruptIndex(dir, storeResult.IntegrityErr)
		result.Healed = true
	}
	return nil
}

// licensesDiscard reports whether a failed integrity verdict justifies deleting
// the index. Only a check that RAN and found damage does (#875); one that could
// not run (I/O error, BUSY, canceled) says nothing about the data, and a rebuild
// costs minutes of CPU.
func licensesDiscard(integrityErr error) bool {
	return errors.Is(integrityErr, store.ErrCorrupt)
}

// addSize accumulates a size into total, leaving total at -1 ("unknown") until
// at least one directory reports a real size.
func addSize(total *int64, size int64) {
	if size < 0 {
		return
	}
	if *total < 0 {
		*total = 0
	}
	*total += size
}
