//go:build ledger_twin

package ledger_twin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/carts"
	"github.com/stretchr/testify/require"
)

// TestCartAnalyzeWindows runs carts.Analyze on each interesting time window
// and writes the JSON output to /tmp/cart-analysis-<window>.json.
// Run: go test -tags=ledger_twin -v -run TestCartAnalyzeWindows ./tests/ledger_twin/...
func TestCartAnalyzeWindows(t *testing.T) {
	ledgerPath := manifest.LedgerPath
	require.NotEmpty(t, ledgerPath, "manifest.LedgerPath must be set")

	cartsDir := filepath.Join(ledgerPath, "carts")
	allCarts, err := carts.LoadCartsFromDir(cartsDir)
	require.NoError(t, err)
	require.NotEmpty(t, allCarts, "should have carts")

	// Interesting windows for cart analysis
	windows := []struct {
		Name  string
		Since time.Time
		Until time.Time
	}{
		{"hot_zone", ts(20, 10, 0), ts(20, 18, 0)},
		{"sprint_end_rush", ts(23, 8, 0), ts(23, 20, 0)},
		{"escalation_full", ts(10, 0, 0), ts(12, 23, 59)},
		{"escalation_day3", ts(12, 0, 0), ts(12, 23, 59)},
		{"active_recording", ts(24, 14, 0), ts(24, 23, 59)},
		{"cluster_bridge", ts(17, 9, 0), ts(17, 17, 0)},
		{"all_windows", ts(10, 0, 0), ts(24, 23, 59)},
	}

	outDir := "/tmp/cart-analysis"
	require.NoError(t, os.MkdirAll(outDir, 0755))

	for _, w := range windows {
		t.Run(w.Name, func(t *testing.T) {
			// Filter carts to this time window
			var windowCarts []*carts.Issue
			for _, c := range allCarts {
				if !c.CreatedAt.Before(w.Since) && !c.CreatedAt.After(w.Until) {
					windowCarts = append(windowCarts, c)
				}
			}

			output, err := carts.Analyze(carts.AnalyzeInput{
				Carts:        windowCarts,
				Dependencies: make(map[string][]*carts.Dependency),
				LedgerPath:   ledgerPath,
				Since:        w.Since,
				Until:        w.Until,
			})
			require.NoError(t, err)

			data, err := json.MarshalIndent(output, "", "  ")
			require.NoError(t, err)

			outPath := filepath.Join(outDir, w.Name+".json")
			require.NoError(t, os.WriteFile(outPath, data, 0644))
			t.Logf("wrote %s (%d carts, %d sessions, %d murmurs)",
				outPath, output.Progress.TotalCarts, output.Progress.SessionCount, output.Progress.MurmurCount)
		})
	}

	// Also preserve the twin ledger path
	t.Logf("twin ledger preserved at: %s", ledgerPath)
	ledgerInfoPath := filepath.Join(outDir, "LEDGER_PATH.txt")
	os.WriteFile(ledgerInfoPath, []byte(ledgerPath+"\n"), 0644)
}
