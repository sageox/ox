package kb

// resolve_enrich.go — local-only KB type/slug enrichment for the binding
// returned by ResolveCurrentKB.
//
// The plain resolver intentionally avoids network calls and never knows the
// KB type — only the marker file's kb_id and endpoint. Many CLI code paths
// (session recording defaults, doctor displays, agent_hook banners) need the
// type for per-type behavior. The daemon writes a small meta.json into the
// KB checkout during sync (see internal/daemon/sync_bubbles.go:writeKBMeta);
// reading that file is the canonical local enrichment path.
//
// Keeping enrichment in internal/kb/ (not in internal/config/) is what lets
// callers like internal/doctor/ and cmd/ox/ pass real kbID/kbType to
// config.ResolveSessionRecording without re-introducing the
// config → kb → api → config import cycle.

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/paths"
)

// kbMetaOnDisk mirrors the daemon's kbMeta payload at
// <paths.KBDir(kbID)>/.sageox/meta.json. Field tags match the writer so the
// daemon and reader stay in lock-step. Only fields the enrichment helper
// consumes are surfaced; additional fields the daemon writes (owner,
// viewer_role, last_sync) are accepted-and-ignored.
type kbMetaOnDisk struct {
	Type string `json:"type"`
	Slug string `json:"slug"`
}

// ResolveCurrentKBEnriched walks up from cwd like ResolveCurrentKB, then
// enriches the binding's KBType (and any slug surfaced through the
// binding's Source path) from the local KB metadata at
// <paths.KBDir(KBID)>/.sageox/meta.json (written by the daemon during sync).
//
// Local-only — no network. If meta.json is missing or unreadable, KBType
// remains empty (the resolver still returns the KBID/Endpoint). This is the
// expected state for brand-new bubbles the daemon hasn't synced yet.
func ResolveCurrentKBEnriched(cwd string) (*KBBinding, error) {
	binding, err := ResolveCurrentKB(cwd)
	if err != nil || binding == nil {
		return binding, err
	}
	if binding.KBID == "" {
		return binding, nil
	}

	// Read the daemon-written meta.json. Any I/O or parse error is treated
	// as "unenriched" — callers must already tolerate empty KBType because
	// the plain resolver never populates it.
	metaPath := filepath.Join(paths.KBDir(binding.KBID), ".sageox", "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return binding, nil
	}
	var meta kbMetaOnDisk
	if err := json.Unmarshal(data, &meta); err != nil {
		return binding, nil
	}

	binding.KBType = meta.Type
	return binding, nil
}

// ResolveCurrentKBIDAndType returns (kbID, kbType) for the current cwd, or
// ("", "") if no current KB. Convenience wrapper for callers that just want
// to pass these two values to config.ResolveSessionRecording.
//
// Errors are intentionally swallowed: callers in this position
// (session-recording resolution, hook banners, doctor) always have a
// well-defined fallback when there is no binding, so a resolver failure
// should degrade to "no KB" rather than propagate up.
func ResolveCurrentKBIDAndType(cwd string) (kbID, kbType string) {
	binding, err := ResolveCurrentKBEnriched(cwd)
	if err != nil || binding == nil {
		return "", ""
	}
	return binding.KBID, binding.KBType
}
