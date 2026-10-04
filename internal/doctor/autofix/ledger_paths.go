package autofix

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/paths"
)

// DiscoverLedgerPaths lists real canonical Ledger checkouts for one endpoint.
// Operational siblings created by reclone/GC are deliberately excluded: they
// are recovery artifacts, not independent Ledgers, and mutating them would race
// or invalidate the active recovery.
func DiscoverLedgerPaths(endpointURL string) ([]string, error) {
	if endpointURL == "" {
		return nil, nil
	}
	root := paths.LedgersDataDir("", endpointURL)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	ledgers := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Canonical repo IDs are repo_<uuid> and contain no dots. Every
		// blue-green/reclone sibling adds a dotted suffix (.bak.*, .gc-*).
		if !strings.HasPrefix(name, "repo_") || strings.Contains(name, ".") {
			continue
		}
		ledgerPath := filepath.Join(root, name)
		if _, err := os.Stat(filepath.Join(ledgerPath, ".git")); err != nil {
			continue
		}
		ledgers = append(ledgers, ledgerPath)
	}
	sort.Strings(ledgers)
	return ledgers, nil
}
