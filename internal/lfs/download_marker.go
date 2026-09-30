package lfs

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DownloadedMarkerFile marks a ledger cache folder
// (<ledger>/.sageox/cache/sessions/<name>/) as a read-only copy of a session
// that was pulled down from the ledger so a coworker could read it.
//
// The daemon's finalize scan walks the ledger cache and treats a folder there
// as this machine's unfinished work. A download has the same shape — a real
// raw.jsonl with no summary.json next to it, because summary.json is a plain
// git file and never downloaded — so without this marker the scan
// re-summarized every downloaded session and pushed the new title and summary
// over the teammate's (GH #1107).
const DownloadedMarkerFile = ".downloaded"

// MarkCacheDownload records that a read-only download is about to populate
// cacheDir. Call it BEFORE any content lands, so the scan can never observe
// the transcript without the marker.
//
// It leaves the folder alone when it already holds this machine's own work:
// a transcript, a meta.json, or a .needs-summary request. Those came from a
// recording, a push that has not landed, or a deliberate retry, and a
// download reading through them must not reclassify them as read-only.
func MarkCacheDownload(cacheDir string) error {
	for _, name := range []string{"raw.jsonl", metaFilename, ".needs-summary"} {
		if _, err := os.Lstat(filepath.Join(cacheDir, name)); err == nil {
			return nil
		}
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("create session cache dir: %w", err)
	}
	stamp := []byte(time.Now().UTC().Format(time.RFC3339) + "\n")
	if err := os.WriteFile(filepath.Join(cacheDir, DownloadedMarkerFile), stamp, 0o644); err != nil {
		return fmt.Errorf("write download marker: %w", err)
	}
	return nil
}

// HasDownloadedMarker reports whether dir was populated by a read-only
// download (see DownloadedMarkerFile).
func HasDownloadedMarker(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, DownloadedMarkerFile))
	return err == nil
}
