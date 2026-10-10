package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: 'ox session upload' cannot find a held or cache-only
// session (GH #1019, #1077) — or, the opposite failure, it re-publishes a
// session that is already in the Ledger. The publish itself is proven
// end-to-end in session_manual_hold_e2e_test.go.
func TestFindPublishableCacheSession(t *testing.T) {
	const name = "2026-10-05T10-00-faridun-OxPuBl"
	tests := []struct {
		name  string
		setup func(t *testing.T, cacheDir, ledgerDir string)
		want  bool
	}{
		{"held, even with a Ledger entry from before the hold", func(t *testing.T, cacheDir, ledgerDir string) {
			writeLedgerMeta(t, ledgerDir, `{"version":"1.0","title":"done"}`)
			require.NoError(t, session.WriteHoldMarker(cacheDir, session.HoldManualPublishing, "test"))
		}, true},
		{"cache-only orphan, no Ledger entry", func(*testing.T, string, string) {}, true},
		{"Ledger holds only a draft placeholder", func(t *testing.T, _, ledgerDir string) {
			writeLedgerMeta(t, ledgerDir, `{"version":"1.0","draft":true}`)
		}, true},
		{"earlier publish left a pending retry", func(t *testing.T, cacheDir, ledgerDir string) {
			writeLedgerMeta(t, ledgerDir, `{"version":"1.0","title":"done"}`)
			require.NoError(t, os.WriteFile(filepath.Join(cacheDir, sessionUploadRetryPendingFile), []byte("{}"), 0o644))
		}, true},
		{"already published", func(t *testing.T, _, ledgerDir string) {
			writeLedgerMeta(t, ledgerDir, `{"version":"1.0","title":"done"}`)
		}, false},
		{"no conversation in the cache copy", func(t *testing.T, cacheDir, _ string) {
			require.NoError(t, os.WriteFile(filepath.Join(cacheDir, ledgerFileRaw), []byte(`{"type":"header"}`+"\n"), 0o644))
			require.NoError(t, session.WriteHoldMarker(cacheDir, session.HoldManualPublishing, "test"))
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			ledgerPath := t.TempDir()
			cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", name)
			require.NoError(t, os.MkdirAll(cacheDir, 0o755))
			writeTestRawJSONLWithEntries(t, filepath.Join(cacheDir, ledgerFileRaw), 3)
			tt.setup(t, cacheDir, filepath.Join(ledgerPath, "sessions", name))

			gotName, gotDir, ok := findPublishableCacheSession(ledgerPath, "OxPuBl")

			assert.Equal(t, tt.want, ok)
			if tt.want {
				assert.Equal(t, name, gotName, "resolved from the agent-id suffix")
				assert.Equal(t, cacheDir, gotDir)
			}
		})
	}
}

func writeLedgerMeta(t *testing.T, ledgerSessionDir, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(ledgerSessionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ledgerSessionDir, "meta.json"), []byte(content), 0o644))
}
