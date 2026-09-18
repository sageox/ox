package agentwork

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIncompleteImportNeverEscapesThroughDaemonRecovery(t *testing.T) {
	for _, kind := range []string{"pending", "malformed", "verified"} {
		t.Run(kind, func(t *testing.T) {
			ledger := t.TempDir()
			dir := filepath.Join(ledger, ".sageox", "cache", "sessions", "2026-09-16T00-00-import-test")
			require.NoError(t, os.MkdirAll(dir, 0700))
			// A malformed later native record can leave this fully valid redacted
			// prefix. Its syntax alone must never authorize a partial upload.
			raw := []byte("{\"type\":\"header\",\"version\":\"1.0\",\"agent_type\":\"codex\"}\n{\"type\":\"user\",\"content\":\"converted before malformed source record\"}\n")
			rawPath := filepath.Join(dir, "raw.jsonl")
			require.NoError(t, os.WriteFile(rawPath, raw, 0600))
			journal := map[string]any{"version": 1, "session_name": filepath.Base(dir), "native_session_id": "019c6d2e-27b0-798d-aaed-b036114dc63a", "generation": strings.Repeat("a", 64), "snapshot_digest": strings.Repeat("b", 64), "upload_verified": kind == "verified"}
			b, err := json.Marshal(journal)
			require.NoError(t, err)
			if kind == "malformed" {
				b = []byte("{")
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".import-journal.json"), b, 0600))
			h := NewSessionFinalizeHandler(slog.Default())
			// Verified imports remain eligible for summary detection without requiring
			// a real remote in this fixture. Unverified cases retain production gates.
			if kind == "verified" {
				h.skipGit = true
				h.skipLFS = true
			}
			items, err := h.Detect(ledger)
			require.NoError(t, err)
			if kind == "verified" {
				require.Len(t, items, 1)
				return
			}
			require.Empty(t, items)
			payload := &SessionFinalizePayload{SessionDir: dir, RawPath: rawPath, LedgerPath: ledger, UploadOnly: true}
			item := &WorkItem{Payload: payload}
			_, err = h.BuildPrompt(item)
			require.Error(t, err)
			require.Error(t, h.ProcessResult(item, &RunResult{}))
			require.Error(t, h.publishRawPending(payload))
			after, err := os.ReadFile(rawPath)
			require.NoError(t, err)
			require.Equal(t, raw, after)
			require.NoDirExists(t, filepath.Join(ledger, "sessions"))
			require.NoFileExists(t, filepath.Join(dir, "meta.json"))
			require.NoFileExists(t, filepath.Join(dir, "summary.json"))
			require.NoFileExists(t, filepath.Join(dir, ".raw-uploaded"))
		})
	}
}
