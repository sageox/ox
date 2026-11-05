package session

import (
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/trace/materialize"
	"github.com/sageox/ox/internal/trace/model"
)

// MaterializeTraces is shared by synchronous stop and daemon finalization. A
// nil capture means this recording never opted in. Transcript metadata is only
// a consistency check: local recording state authorizes the spool read. Legacy
// captures without local authorization are omitted, not reconstructed. Callers
// log errors and continue ordinary recording upload.
func MaterializeTraces(ledgerPath, sessionDir string, capture *model.Capture) (string, *model.Metadata, error) {
	if capture == nil {
		return "", nil, nil
	}
	sessionName := filepath.Base(sessionDir)
	if !filepath.IsAbs(sessionDir) || sessionName == "." || sessionName == ".." || sessionName == string(filepath.Separator) {
		return "", nil, fmt.Errorf("invalid trace recording directory")
	}
	local, err := readLocalTraceCapture(sessionDir)
	if err != nil {
		return "", nil, fmt.Errorf("local trace capture unavailable: %w", err)
	}
	if !reflect.DeepEqual(local, capture) {
		return "", nil, fmt.Errorf("trace capture differs from local recording")
	}
	store, err := NewStore(ledgerPath)
	if err != nil {
		return "", nil, err
	}
	cacheDir := store.CacheSessionPath(sessionName)
	meta, err := materialize.Build(paths.TraceSpoolDir(), cacheDir, local)
	return cacheDir, meta, err
}
