package session

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const needsSummaryMarker = ".needs-summary"

// NeedsSummaryInfo describes a session that needs summary generation.
type NeedsSummaryInfo struct {
	CacheDir         string `json:"cache_dir"`
	RawPath          string `json:"raw_path"`
	LedgerSessionDir string `json:"ledger_session_dir"`
}

// WriteNeedsSummaryMarker writes a .needs-summary marker to the session cache directory.
// This marker indicates that session stop completed but summary.json was not yet generated.
func WriteNeedsSummaryMarker(sessionCacheDir, rawPath, ledgerSessionDir string) error {
	info := NeedsSummaryInfo{
		CacheDir:         sessionCacheDir,
		RawPath:          rawPath,
		LedgerSessionDir: ledgerSessionDir,
	}

	data, err := json.Marshal(info)
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(sessionCacheDir, needsSummaryMarker), data, 0644)
}

// ClearNeedsSummaryMarker removes the .needs-summary marker from a session cache directory.
func ClearNeedsSummaryMarker(sessionCacheDir string) error {
	path := filepath.Join(sessionCacheDir, needsSummaryMarker)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// HasNeedsSummaryMarker reports whether a session directory has a .needs-summary marker,
// indicating that stub artifacts were written at stop time and need LLM regeneration.
func HasNeedsSummaryMarker(sessionDir string) bool {
	_, err := os.Stat(filepath.Join(sessionDir, needsSummaryMarker))
	return err == nil
}

// FindSessionsNeedingSummary scans the sessions directory under contextPath
// for cache session directories containing a .needs-summary marker.
func FindSessionsNeedingSummary(contextPath string) ([]NeedsSummaryInfo, error) {
	sessionsDir := filepath.Join(contextPath, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var results []NeedsSummaryInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		markerPath := filepath.Join(sessionsDir, entry.Name(), needsSummaryMarker)
		data, err := os.ReadFile(markerPath)
		if err != nil {
			continue
		}

		var info NeedsSummaryInfo
		if err := json.Unmarshal(data, &info); err != nil {
			continue
		}

		results = append(results, info)
	}

	return results, nil
}
