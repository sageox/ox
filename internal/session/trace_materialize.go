package session

import (
	"fmt"
	"path/filepath"

	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/trace/materialize"
	"github.com/sageox/ox/internal/trace/model"
)

// MaterializeTraces is shared by synchronous stop and daemon finalization. A
// nil capture means this recording never opted in; legacy recordings remain
// unchanged. Callers log errors and continue ordinary recording upload.
func MaterializeTraces(ledgerPath, sessionName string, capture *model.Capture) (string, *model.Metadata, error) {
	if capture == nil {
		return "", nil, nil
	}
	if sessionName == "." || sessionName == ".." || filepath.Base(sessionName) != sessionName || sessionName == "" {
		return "", nil, fmt.Errorf("invalid trace recording name")
	}
	store, err := NewStore(ledgerPath)
	if err != nil {
		return "", nil, err
	}
	cacheDir := store.CacheSessionPath(sessionName)
	meta, err := materialize.Build(paths.TraceSpoolDir(), cacheDir, capture)
	return cacheDir, meta, err
}
