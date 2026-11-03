package session

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/trace/model"
)

// Trace authorization must not live in the Ledger, even in its ignored cache:
// a Ledger writer can force-add files there. Bind the local capture to the
// original recording directory, not a native UUID supplied by a transcript.
func localTraceCapturePath(sessionDir string) (string, error) {
	if !filepath.IsAbs(sessionDir) {
		return "", fmt.Errorf("trace recording directory must be absolute")
	}
	key := sha256.Sum256([]byte(filepath.Clean(sessionDir)))
	return filepath.Join(paths.TraceDir(), "recordings", fmt.Sprintf("%x.json", key)), nil
}

func readLocalTraceCapture(sessionDir string) (*model.Capture, error) {
	path, err := localTraceCapturePath(sessionDir)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var capture model.Capture
	if err := json.Unmarshal(data, &capture); err != nil {
		return nil, err
	}
	return &capture, nil
}

func writeLocalTraceCapture(sessionDir string, capture *model.Capture) error {
	path, err := localTraceCapturePath(sessionDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return fileutil.AtomicWriteJSON(path, capture, 0600)
}
