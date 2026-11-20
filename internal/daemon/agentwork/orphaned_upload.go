package agentwork

import (
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session"
)

// stalenessUploadOrphaned: the recording's owner is dead and a stop finalized
// its capture, chose its publication ID (.upload-retry-pending, bound to the
// current raw.jsonl) and then ended without publishing. See uploadOrphaned.
const stalenessUploadOrphaned = "upload_orphaned"

// captureLockFree reports whether no process holds sessionDir's raw.jsonl
// capture lock. A session stop holds it from its final drain until its upload
// returns, and the kernel drops it when the stop dies.
func captureLockFree(sessionDir string) bool {
	free, err := fileutil.TryWithFileLock(filepath.Join(sessionDir, artifactRaw), func() error { return nil })
	return err == nil && free
}

// pendingUploadToRetry reports whether sessionDir holds an upload a stop
// started: a .upload-retry-pending marker that still matches raw.jsonl. A
// session deleted on SageOx while it streamed is never one: it must not be
// published by anyone.
func pendingUploadToRetry(sessionDir, ledgerPath string) bool {
	return session.PendingUploadID(sessionDir) != "" && !deletedOnServerAnywhere(sessionDir, ledgerPath)
}

// uploadOrphaned reports whether a stop started sessionDir's upload and no
// process is finishing it. That happens when the coding agent kills the stop
// as it exits (sageox/ox#1936): the stop dies after writing the marker and
// before clearing .recording.json, so the recording looks merely young, and
// the ghost grace period would hold it back for up to ten minutes.
func uploadOrphaned(sessionDir, ledgerPath string) bool {
	return captureLockFree(sessionDir) && pendingUploadToRetry(sessionDir, ledgerPath)
}

// recordingOwnerMayBeAlive reports whether sessionDir's recording could still
// be in use: its owner's PID answers, or there is no PID to ask yet.
func (h *SessionFinalizeHandler) recordingOwnerMayBeAlive(sessionDir string) bool {
	recPath := filepath.Join(sessionDir, recordingMarker)
	info, err := os.Stat(recPath)
	if err != nil {
		return false
	}
	_, _, method := isStaleRecording(recPath, info, h.pidLookup, h.logger)
	return method == stalenessPIDAlive || method == stalenessTimeNotReached
}

// HasNewOrphanedUpload reports whether a session upload was orphaned since the
// last call, so the manager can run detection now instead of on its next
// repair scan. Cheap enough to call every few seconds: a directory listing and
// a stat per cached session, and raw.jsonl is hashed only once per marker
// written, after the uploader is gone. Called only from the manager's loop.
func (h *SessionFinalizeHandler) HasNewOrphanedUpload(ledgerPath string) bool {
	found := false
	markers := map[string]bool{}
	for _, dir := range session.HeldSessionDirs(ledgerPath) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			sessionDir := filepath.Join(dir, entry.Name())
			marker := filepath.Join(sessionDir, session.UploadRetryPendingFile)
			info, err := os.Stat(marker)
			if err != nil {
				continue
			}
			markers[marker] = true
			if seen, ok := h.uploadProbed.Load(marker); ok && seen.(time.Time).Equal(info.ModTime()) {
				continue
			}
			// a stop still uploading, or an agent still recording: look again
			// on the next call rather than settling this marker now
			if !captureLockFree(sessionDir) || h.recordingOwnerMayBeAlive(sessionDir) {
				continue
			}
			h.uploadProbed.Store(marker, info.ModTime())
			if pendingUploadToRetry(sessionDir, ledgerPath) {
				h.logger.Info("orphaned session upload found, retrying now", "session", entry.Name(), "ledger", ledgerPath)
				found = true
			}
		}
	}
	h.uploadProbed.Range(func(key, _ any) bool {
		if !markers[key.(string)] {
			h.uploadProbed.Delete(key)
		}
		return true
	})
	return found
}
