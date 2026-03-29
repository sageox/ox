package daemon

import (
	"context"
	"os"
	"time"

	"github.com/sageox/ox/internal/codedb"
)

// CodeDBMaintainer wraps a CodeDBManager for the DBMaintainer interface.
// Opens the store on demand for maintenance since CodeDBManager doesn't hold a persistent handle.
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
		SizeBefore: -1,
		SizeAfter:  -1,
	}

	if m.manager == nil {
		result.Duration = time.Since(start)
		return result
	}

	dataDir := m.manager.resolveSharedDataDir()
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		// no index exists yet — nothing to maintain
		result.Healthy = true
		result.Duration = time.Since(start)
		return result
	}

	db, err := codedb.Open(dataDir)
	if err != nil {
		result.Error = err
		result.Duration = time.Since(start)
		return result
	}
	defer db.Close()

	storeResult := db.Store().Maintain(ctx)

	result.Pruned = storeResult.TotalPruned()
	result.SizeBefore = storeResult.SizeBefore
	result.SizeAfter = storeResult.SizeAfter
	result.Vacuumed = storeResult.Vacuumed
	result.Healthy = storeResult.IntegrityOK
	result.Duration = storeResult.Duration
	return result
}
