package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/pkg/sessionprovenance"
)

func ReadSourceRecord(ledgerPath, nativeID string) (*sessionprovenance.Record, error) {
	rel, err := sessionprovenance.Path(nativeID)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(ledgerPath, rel))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r sessionprovenance.Record
	if err = json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	if err = r.Validate(); err != nil {
		return nil, err
	}
	if r.NativeSessionID != nativeID {
		return nil, fmt.Errorf("source identity mismatch")
	}
	// The contract validates agent shape only. This reader serves the codex
	// namespace, so a record claiming another agent does not belong in it.
	if r.Agent != "codex" {
		return nil, fmt.Errorf("source agent mismatch")
	}
	return &r, nil
}

// WriteSourceRecord requires the caller to hold the Ledger repository lock.
// Records are replaced atomically; exclusions must be retained by the caller.
func WriteSourceRecord(ledgerPath string, r *sessionprovenance.Record) error {
	if err := r.Validate(); err != nil {
		return err
	}
	rel, _ := sessionprovenance.Path(r.NativeSessionID)
	p := filepath.Join(ledgerPath, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".source-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), p); err != nil {
		return err
	}
	// Preserve exclusion receipts across hard crashes without following a
	// pre-existing record symlink. Directory fsync is unsupported on some hosts.
	if dir, err := os.Open(filepath.Dir(p)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// ExcludeNativeSession records intent before capture discards local content.
// The caller holds the repository lock and publishes this record with its
// lifecycle mutation. End=-1 excludes future bytes as well as existing history.
func ExcludeNativeSession(ledgerPath, nativeID, reason string, start, end int64) error {
	r, err := ReadSourceRecord(ledgerPath, nativeID)
	if err != nil {
		return err
	}
	if r == nil {
		r = &sessionprovenance.Record{Version: 1, Agent: "codex", NativeSessionID: nativeID}
	}
	for _, e := range r.Exclusions {
		if e.Start == start && e.End == end && e.Reason == reason {
			return nil
		}
	}
	r.Exclusions = append(r.Exclusions, sessionprovenance.Exclusion{Start: start, End: end, Reason: reason, CreatedAt: time.Now().UTC()})
	r.UpdatedAt = time.Now().UTC()
	return WriteSourceRecord(ledgerPath, r)
}
