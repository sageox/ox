package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckImportPublicationFailsClosed(t *testing.T) {
	for _, kind := range []string{"absent", "verified", "pending", "malformed", "version", "oversize", "identity", "trailing"} {
		t.Run(kind, func(t *testing.T) {
			ledger := t.TempDir()
			dir := filepath.Join(ledger, ".sageox", "cache", "sessions", "session-one")
			require.NoError(t, os.MkdirAll(dir, 0700))
			journal := map[string]any{"version": 1, "session_name": "session-one", "native_session_id": "019c6d2e-27b0-798d-aaed-b036114dc63a", "generation": strings.Repeat("a", 64), "snapshot_digest": strings.Repeat("b", 64), "upload_verified": true}
			if kind == "pending" {
				journal["upload_verified"] = false
			}
			if kind == "version" {
				journal["version"] = 2
			}
			if kind == "identity" {
				journal["session_name"] = "other"
			}
			b, err := json.Marshal(journal)
			require.NoError(t, err)
			if kind == "malformed" {
				b = []byte("{")
			}
			if kind == "oversize" {
				b = []byte(strings.Repeat(" ", 65537))
			}
			if kind == "trailing" {
				b = append(b, []byte(" {}")...)
			}
			if kind != "absent" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".import-journal.json"), b, 0600))
			}
			for _, candidateDir := range []string{dir, filepath.Join(ledger, "sessions", "session-one")} {
				err := CheckImportPublication(ledger, candidateDir)
				if kind == "verified" || kind == "absent" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			}
		})
	}
}
