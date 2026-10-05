package agentwork

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A session held on this machine (session_publishing: manual) is never
// summarized, uploaded, or discarded by the daemon (GH #1093). The E2E proof
// through real CLI stop and the real finalize pass lives in cmd/ox.

const heldTestSession = "2026-10-05T10-00-faridun-OxHeLd"

func newHeldFixture(t *testing.T) cacheFixture {
	t.Helper()
	// keep XDG cache lookups inside the test
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ledgerPath := t.TempDir()
	return cacheFixture{
		ledgerPath: ledgerPath,
		name:       heldTestSession,
		cacheDir:   filepath.Join(ledgerPath, ".sageox", "cache", "sessions", heldTestSession),
	}
}

func hold(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, session.WriteHoldMarker(dir, session.HoldManualPublishing, "test"))
}

func TestDetect_HeldSessionIsLeftAlone(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(t *testing.T, f cacheFixture)
		wantItem bool
	}{
		{"held after a manual stop", func(t *testing.T, f cacheFixture) {
			f.allArtifacts(t)
			hold(t, f.cacheDir)
		}, false},
		{"held copy in another cache location holds this one", func(t *testing.T, f cacheFixture) {
			f.allArtifacts(t)
			dirs := session.HeldSessionDirs(f.ledgerPath)
			require.Greater(t, len(dirs), 1, "an XDG cache location is scanned too")
			hold(t, filepath.Join(dirs[1], f.name))
		}, false},
		{"control: not held", func(t *testing.T, f cacheFixture) {
			f.allArtifacts(t)
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newHeldFixture(t)
			tc.setup(t, f)
			h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
			items, skips, err := h.detectInDir(filepath.Join(f.ledgerPath, ".sageox", "cache", "sessions"), f.ledgerPath)
			require.NoError(t, err)
			assert.Equal(t, tc.wantItem, len(items) == 1)
			assert.Equal(t, !tc.wantItem, skips.held[f.name], "a held session is counted as held")
			assert.Zero(t, skips.total(), "a held session is not a skip anomaly")
		})
	}
}

// Failure prevented: a manual-mode recording whose agent crashed is recovered
// by the daemon, which cannot see the CLI's environment, and published.
func TestDetect_RecoveredManualRecordingIsHeld(t *testing.T) {
	for _, mode := range []string{"manual", "auto"} {
		t.Run(mode, func(t *testing.T) {
			f := newHeldFixture(t)
			f.transcript(t)
			rec, err := json.Marshal(map[string]any{
				"agent_id":        "OxHeLd",
				"started_at":      time.Now().Add(-25 * time.Hour).Format(time.RFC3339),
				"publishing_mode": mode,
			})
			require.NoError(t, err)
			recPath := filepath.Join(f.cacheDir, recordingMarker)
			require.NoError(t, os.WriteFile(recPath, rec, 0o644))

			h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
			items := detectCacheOnly(t, h, f.ledgerPath)

			assert.NoFileExists(t, recPath, "the stale recording is still reclaimed")
			assert.FileExists(t, filepath.Join(f.cacheDir, "raw.jsonl"), "its transcript stays local")
			if mode == "manual" {
				assert.True(t, session.IsHeld(f.cacheDir), "the recorded mode must survive the marker as a hold")
				assert.Empty(t, items, "a held recovery must not be finalized")
			} else {
				assert.False(t, session.IsHeld(f.cacheDir))
				assert.Len(t, items, 1, "an auto-mode recovery is finalized as before")
			}
		})
	}
}

// Failure prevented: a hold placed after the scan, or an item queued by IPC
// (which never passes the scan), reaches the LLM, LFS, or the discard.
func TestFinalizeEntryPoints_DropHeldItem(t *testing.T) {
	for _, uploadOnly := range []bool{false, true} {
		name := "full finalize"
		if uploadOnly {
			name = "upload-only"
		}
		t.Run(name, func(t *testing.T) {
			f := newHeldFixture(t)
			f.transcript(t)
			h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
			items := detectCacheOnly(t, h, f.ledgerPath)
			require.Len(t, items, 1, "precondition: unheld local work is queued")
			items[0].Payload.(*SessionFinalizePayload).UploadOnly = uploadOnly

			hold(t, f.cacheDir)

			req, err := h.BuildPrompt(items[0])
			require.NoError(t, err)
			assert.True(t, req.SkipLLM, "no LLM run for a held session")
			require.NoError(t, h.ProcessResult(items[0], &RunResult{}))
			assert.NoFileExists(t, filepath.Join(f.cacheDir, artifactSummJSON))
			assert.FileExists(t, filepath.Join(f.cacheDir, "raw.jsonl"), "a held session is never discarded")
			assert.NoDirExists(t, filepath.Join(f.ledgerPath, "sessions", f.name), "nothing is staged into the Ledger")
		})
	}
}

// Failure prevented: machine-local state (.upload-retry-pending, found
// committed on a Ledger remote; a .held would hold the session on every
// teammate's machine) is copied into the shared sessions/ tree.
func TestStageSessionInLedger_LeavesDotfilesBehind(t *testing.T) {
	f := newHeldFixture(t)
	f.transcript(t)
	f.write(t, "summary.md", "summary")
	for _, local := range []string{".upload-retry-pending", ".needs-summary", ".held", ".tmp-123"} {
		f.write(t, local, "{}")
	}

	payload := &SessionFinalizePayload{SessionDir: f.cacheDir, LedgerPath: f.ledgerPath}
	_, err := NewSessionFinalizeHandler(slog.Default()).stageSessionInLedger(payload)
	require.NoError(t, err)

	dest := filepath.Join(f.ledgerPath, "sessions", f.name)
	assert.FileExists(t, filepath.Join(dest, artifactRaw))
	assert.FileExists(t, filepath.Join(dest, "summary.md"))
	entries, err := os.ReadDir(dest)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotEqual(t, '.', rune(e.Name()[0]), "dotfile staged into the Ledger: %s", e.Name())
	}
}
