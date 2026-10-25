package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// maintStateSuffix names the sidecar next to metadata.db that remembers when
	// maintenance last did its two expensive jobs.
	maintStateSuffix = ".maint.json"

	// maintInterval is how long a verification or vacuum stays "recent". Both
	// are O(database size), so they run at most this often per database no
	// matter how frequently maintenance itself is scheduled.
	maintInterval = 24 * time.Hour
)

// maintState is the content of the sidecar. A missing, unreadable, or corrupt
// sidecar decodes to the zero value, which means "both jobs are due" — losing it
// costs one extra scan, never a skipped one.
type maintState struct {
	LastVerified time.Time `json:"last_verified"`
	LastVacuum   time.Time `json:"last_vacuum"`
}

func maintStatePath(dbPath string) string {
	return dbPath + maintStateSuffix
}

func loadMaintState(path string) maintState {
	data, err := os.ReadFile(path)
	if err != nil {
		return maintState{}
	}
	var st maintState
	if err := json.Unmarshal(data, &st); err != nil {
		return maintState{}
	}
	return st
}

// save writes the sidecar atomically (temp file in the same directory, then
// rename) so a crash never leaves a half-written file for loadMaintState to
// misread.
func (st maintState) save(path string) error {
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("encode maintenance state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".maint-*.tmp")
	if err != nil {
		return fmt.Errorf("create maintenance state temp file: %w", err)
	}
	tmpName := tmp.Name()
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(tmpName)
		if writeErr != nil {
			return fmt.Errorf("write maintenance state: %w", writeErr)
		}
		return fmt.Errorf("close maintenance state: %w", closeErr)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("replace maintenance state: %w", err)
	}
	return nil
}

func (st maintState) verifyDue(now time.Time) bool { return due(st.LastVerified, now) }

func (st maintState) vacuumDue(now time.Time) bool { return due(st.LastVacuum, now) }

// due reports whether last is older than maintInterval at now. A zero or
// future timestamp (clock stepped backwards, hand-edited file) is due: trusting
// it could suppress the job indefinitely.
func due(last, now time.Time) bool {
	return last.IsZero() || now.Before(last) || now.Sub(last) >= maintInterval
}
