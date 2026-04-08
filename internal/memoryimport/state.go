// Package memoryimport provides shared memory import logic for both CLI and daemon.
//
// NOTE: All import logic lives in this package.
// cmd/ox/memory_import.go is ONLY cobra flag parsing and output formatting.
// internal/daemon/ is ONLY scheduling and mtime checking.
// Do not duplicate import/merge/state logic in callers — this package owns all decisions.
package memoryimport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	sageoxDirName         = ".sageox"
	cacheDir              = "cache"
	memoryImportStateFile = "memory-import-state.json"

	// SourceTTL is how long a source stays relevant after its last import.
	// Conductor workspaces are ephemeral — a source that hasn't imported in
	// 7 days is likely a deleted workspace and shouldn't permanently force
	// merge mode. Expired sources are pruned on load.
	SourceTTL = 7 * 24 * time.Hour
)

// SourceRecord tracks per-source import metadata, keyed by opaque fingerprint.
type SourceRecord struct {
	LastImport  time.Time `json:"last_import"`
	LastMtime   time.Time `json:"last_mtime"`
}

// MemoryImportState tracks import history for merge-vs-rsync decisions.
// Stored in {ledger}/.sageox/cache/memory-import-state.json (gitignored, local-only).
//
// The cheap rsync path is ONLY available when all active (non-expired) sources
// are a single workspace. Once a second active source is seen, imports require
// LLM merge. Expired sources (older than SourceTTL) are pruned on load, so
// ephemeral Conductor workspaces that rotated away don't permanently force
// merge mode.
//
// Sources are keyed by opaque SHA-256 fingerprints (not raw project hashes)
// to avoid leaking local filesystem paths. The fingerprint is derived from
// hostname + project_hash, so the same workspace on different machines produces
// different fingerprints.
//
// Each source tracks its own last_import and last_mtime independently, so the
// daemon can skip unchanged sources without re-importing every workspace hourly.
type MemoryImportState struct {
	Sources map[string]*SourceRecord `json:"sources"` // fingerprint → per-source record
}

// NeedsMerge returns true if the import requires semantic LLM merge.
// Cheap rsync is only allowed when ALL prior imports have been from the
// same single source fingerprint. Once a second is seen, merge is permanently required.
//
// Callers should pass the raw project hash — it's fingerprinted internally.
func (s *MemoryImportState) NeedsMerge(projectHash string) bool {
	fp := sourceFingerprint(projectHash)
	switch len(s.Sources) {
	case 0:
		return false
	case 1:
		_, exists := s.Sources[fp]
		return !exists // single source, but not this one → merge
	default:
		return true // multiple sources ever seen → always merge
	}
}

// RecordImport updates the per-source record after a successful import.
// Fingerprints the project hash before storage.
func (s *MemoryImportState) RecordImport(projectHash string, sourceMtime time.Time) {
	fp := sourceFingerprint(projectHash)
	if s.Sources == nil {
		s.Sources = make(map[string]*SourceRecord)
	}
	s.Sources[fp] = &SourceRecord{
		LastImport: time.Now().UTC(),
		LastMtime:  sourceMtime,
	}
}

// SourceChanged returns true if the given source has new content since last import.
// Callers pass the raw project hash and the current mtime of the source directory.
func (s *MemoryImportState) SourceChanged(projectHash string, currentMtime time.Time) bool {
	fp := sourceFingerprint(projectHash)
	rec, ok := s.Sources[fp]
	if !ok {
		return true // never imported → changed
	}
	return currentMtime.After(rec.LastMtime)
}

// SourceCount returns the number of distinct active (non-expired) sources.
func (s *MemoryImportState) SourceCount() int {
	return len(s.Sources)
}

// PruneExpired removes sources whose last import is older than ttl.
// Ephemeral workspaces (Conductor) rotate frequently — a source that hasn't
// imported in a week is dead and shouldn't permanently force merge mode.
// Returns the number of sources removed.
func (s *MemoryImportState) PruneExpired(ttl time.Duration) int {
	cutoff := time.Now().UTC().Add(-ttl)
	pruned := 0
	for fp, rec := range s.Sources {
		if rec.LastImport.Before(cutoff) {
			delete(s.Sources, fp)
			pruned++
		}
	}
	return pruned
}

// sourceFingerprint produces an opaque identifier from a project hash.
// Combines hostname + project hash, then SHA-256 truncated to 16 hex chars.
// This avoids storing raw filesystem paths (which contain usernames, directory
// structure) while still allowing equality comparison for merge decisions.
func sourceFingerprint(projectHash string) string {
	hostname, _ := os.Hostname()
	h := sha256.Sum256([]byte(hostname + ":" + projectHash))
	return hex.EncodeToString(h[:8]) // 16 hex chars — plenty for equality checks
}

// LoadImportState reads memory import state from {ledger}/.sageox/cache/memory-import-state.json.
// Returns empty state (not error) if the file is missing or corrupt.
func LoadImportState(ledgerPath string) *MemoryImportState {
	if ledgerPath == "" {
		return &MemoryImportState{}
	}

	statePath := filepath.Join(ledgerPath, sageoxDirName, cacheDir, memoryImportStateFile)
	data, err := os.ReadFile(statePath)
	if err != nil {
		return &MemoryImportState{}
	}

	var state MemoryImportState
	if err := json.Unmarshal(data, &state); err != nil {
		return &MemoryImportState{}
	}

	// prune expired sources on load — ephemeral workspaces shouldn't
	// permanently force merge mode after they're gone
	state.PruneExpired(SourceTTL)

	return &state
}

// SaveImportState writes memory import state to {ledger}/.sageox/cache/memory-import-state.json.
// Writes to .sageox/cache/ which is gitignored — local-only, never committed to the ledger.
func SaveImportState(ledgerPath string, state *MemoryImportState) error {
	if ledgerPath == "" || state == nil {
		return nil
	}

	dir := filepath.Join(ledgerPath, sageoxDirName, cacheDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	return os.WriteFile(filepath.Join(dir, memoryImportStateFile), data, 0600)
}
