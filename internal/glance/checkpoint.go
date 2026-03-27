package glance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/paths"
)

const checkpointFileName = "checkpoint.json"

type checkpointData struct {
	Checkpoints map[string]time.Time `json:"checkpoints"` // keyed by ledger path
}

func checkpointPath() string {
	return filepath.Join(paths.ConfigDir(), "glance", checkpointFileName)
}

func loadCheckpoints() (*checkpointData, error) {
	data, err := os.ReadFile(checkpointPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &checkpointData{Checkpoints: make(map[string]time.Time)}, nil
		}
		return nil, err
	}
	var cp checkpointData
	if err := json.Unmarshal(data, &cp); err != nil {
		return &checkpointData{Checkpoints: make(map[string]time.Time)}, nil
	}
	if cp.Checkpoints == nil {
		cp.Checkpoints = make(map[string]time.Time)
	}
	return &cp, nil
}

func saveCheckpoints(cp *checkpointData) error {
	path := checkpointPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// GetSince returns the last checkpoint for the given ledger path,
// or falls back to 4 hours ago.
func GetSince(ledgerPath string) time.Time {
	cp, err := loadCheckpoints()
	if err != nil {
		return time.Now().Add(-4 * time.Hour)
	}
	if t, ok := cp.Checkpoints[ledgerPath]; ok {
		return t
	}
	return time.Now().Add(-4 * time.Hour)
}

// MarkRead saves the current time as the checkpoint for the given ledger.
func MarkRead(ledgerPath string) error {
	cp, err := loadCheckpoints()
	if err != nil {
		cp = &checkpointData{Checkpoints: make(map[string]time.Time)}
	}
	cp.Checkpoints[ledgerPath] = time.Now()
	return saveCheckpoints(cp)
}

// ParseTimeFlag parses a --since or --until flag value into a time.Time.
// Accepts: "3d", "7d", "24h", "1w", or RFC3339/date strings.
func ParseTimeFlag(s string) (time.Time, error) {
	s = strings.TrimSpace(s)

	// Duration shortcuts: Nd, Nw
	if len(s) >= 2 {
		num := s[:len(s)-1]
		unit := s[len(s)-1]
		switch unit {
		case 'd':
			n, err := parseInt(num)
			if err == nil {
				return time.Now().Add(-time.Duration(n) * 24 * time.Hour), nil
			}
		case 'w':
			n, err := parseInt(num)
			if err == nil {
				return time.Now().Add(-time.Duration(n) * 7 * 24 * time.Hour), nil
			}
		}
	}

	// Try as Go duration (e.g. "24h", "30m")
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}

	// Try as RFC3339
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}

	// Try as date (use local timezone so --since 2026-03-24 means midnight local, not UTC)
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("cannot parse %q as duration or date (try: 3d, 7d, 24h, 1w, 2026-03-24)", s)
}

func parseInt(s string) (int, error) {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number: %s", s)
		}
		n = n*10 + int(c-'0')
	}
	if len(s) == 0 {
		return 0, fmt.Errorf("empty string")
	}
	return n, nil
}
