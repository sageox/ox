package session

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/trace/model"
)

// localTraceAuthorizationMaxAge bounds how long an authorization outlives its
// recording. It is far beyond any stop/finalize/recovery retry window, so
// pruning never strands a recording that can still publish.
const localTraceAuthorizationMaxAge = 30 * 24 * time.Hour

// localTraceAuthorization is the trusted local record of a recording's trace
// capture. NativeIDs lists every native session the local recording hooks
// authorized, even ones whose offset snapshot failed, so later boundaries
// retry them.
type localTraceAuthorization struct {
	Capture   *model.Capture `json:"capture"`
	NativeIDs []string       `json:"native_ids,omitempty"`
	// SessionDir lets pruning keep authorizations whose recording still
	// awaits finalize, recovery, or upload retry.
	SessionDir string `json:"session_dir,omitempty"`
}

// localTraceRecordingsDir sits beside the trace spool on purpose: the spool is
// what an authorization unlocks, so a process that resolves a different cache
// home cannot read this recording's spool either.
func localTraceRecordingsDir() string {
	return filepath.Join(filepath.Dir(paths.TraceSpoolDir()), "recordings")
}

// Trace authorization must not live in the Ledger, even in its ignored cache:
// a Ledger writer can force-add files there. Bind the local capture to the
// original recording directory, not a native UUID supplied by a transcript.
func localTraceCapturePath(sessionDir string) (string, error) {
	if !filepath.IsAbs(sessionDir) {
		return "", fmt.Errorf("trace recording directory must be absolute")
	}
	key := sha256.Sum256([]byte(filepath.Clean(sessionDir)))
	return filepath.Join(localTraceRecordingsDir(), fmt.Sprintf("%x.json", key)), nil
}

func readLocalTraceAuthorization(sessionDir string) (*localTraceAuthorization, error) {
	path, err := localTraceCapturePath(sessionDir)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var auth localTraceAuthorization
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, err
	}
	if auth.Capture == nil {
		return nil, fmt.Errorf("local trace authorization has no capture")
	}
	return &auth, nil
}

func readLocalTraceCapture(sessionDir string) (*model.Capture, error) {
	auth, err := readLocalTraceAuthorization(sessionDir)
	if err != nil {
		return nil, err
	}
	return auth.Capture, nil
}

func writeLocalTraceAuthorization(sessionDir string, auth *localTraceAuthorization) error {
	path, err := localTraceCapturePath(sessionDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	auth.SessionDir = filepath.Clean(sessionDir)
	return fileutil.AtomicWriteJSON(path, auth, 0600)
}

func writeLocalTraceCapture(sessionDir string, capture *model.Capture) error {
	return writeLocalTraceAuthorization(sessionDir, &localTraceAuthorization{Capture: capture})
}

// pruneLocalTraceAuthorizations removes authorizations untouched for longer
// than localTraceAuthorizationMaxAge whose recording directory is gone; a
// recording still on disk can yet be recovered and upload its traces.
func pruneLocalTraceAuthorizations(now time.Time) {
	dir := localTraceRecordingsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) <= localTraceAuthorizationMaxAge {
			continue
		}
		full := filepath.Join(dir, entry.Name())
		if localTraceRecordingPending(full) {
			continue
		}
		_ = os.Remove(full)
	}
}

func localTraceRecordingPending(authPath string) bool {
	data, err := os.ReadFile(authPath)
	if err != nil {
		return false
	}
	var auth localTraceAuthorization
	if json.Unmarshal(data, &auth) != nil || auth.SessionDir == "" {
		return false
	}
	_, err = os.Stat(auth.SessionDir)
	return err == nil
}
