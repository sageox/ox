package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sageox/ox/internal/ledger"
	"github.com/stretchr/testify/require"
)

// The IPC payload's path and its inner murmur are both caller-supplied, so a
// payload is only written when they describe the same file.
func TestDecodeMurmurPayload_PathMustMatchMurmur(t *testing.T) {
	ts := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	valid := ledger.MurmurFile{
		SchemaVersion: "1", ID: "0190-abc", Timestamp: ts,
		Topic: "build", Importance: "normal", Content: "tests pass",
	}
	payload := func(mf ledger.MurmurFile, relPath string) MurmurPayload {
		raw, err := json.Marshal(mf)
		require.NoError(t, err)
		return MurmurPayload{TargetDir: "/ledger", RelPath: relPath, MurmurJSON: raw}
	}

	_, err := decodeMurmurPayload(payload(valid, ledger.MurmurFilePath(ts, valid.ID)))
	require.NoError(t, err)

	other := valid
	other.ID = "0190-other"
	dotID := valid
	dotID.ID = ".."
	badImportance := valid
	badImportance.Importance = "urgent"
	for name, p := range map[string]MurmurPayload{
		"path names another murmur": payload(valid, ledger.MurmurFilePath(ts, other.ID)),
		"path in another hour":      payload(valid, ledger.MurmurFilePath(ts.Add(time.Hour), valid.ID)),
		"traversal ID":              payload(dotID, ledger.MurmurFilePath(ts, dotID.ID)),
		"unknown importance":        payload(badImportance, ledger.MurmurFilePath(ts, valid.ID)),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeMurmurPayload(p)
			require.Error(t, err)
		})
	}
}
