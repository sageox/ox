package agentwork

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Inspection must use the same eligibility and work kinds as finalization,
// including downloads explicitly re-armed for summarization.
func TestInspect_UsesFinalizationEligibility(t *testing.T) {
	for _, tc := range []struct {
		name       string
		setup      func(*testing.T, cacheFixture)
		items      int
		uploadOnly bool
		held       bool
		refused    bool
	}{
		{name: "real local work", setup: func(t *testing.T, f cacheFixture) { f.transcript(t) }, items: 1},
		{name: "empty raw", setup: func(t *testing.T, f cacheFixture) { f.write(t, artifactRaw, "") }},
		{name: "header-only raw", setup: func(t *testing.T, f cacheFixture) { f.write(t, artifactRaw, emptyRaw) }},
		{name: "LFS pointer", setup: func(t *testing.T, f cacheFixture) {
			f.write(t, artifactRaw, "version https://git-lfs.github.com/spec/v1\noid sha256:"+strings.Repeat("a", 64)+"\nsize 10\n")
		}},
		{name: "read-only download", setup: func(t *testing.T, f cacheFixture) {
			f.downloaded(t)
			f.write(t, lfs.DownloadedMarkerFile, "")
		}},
		{name: "re-armed download", setup: func(t *testing.T, f cacheFixture) {
			f.downloaded(t)
			f.write(t, lfs.DownloadedMarkerFile, "")
			f.write(t, ".needs-summary", "{}")
		}, items: 1},
		{name: "summary requested with artifacts present", setup: func(t *testing.T, f cacheFixture) {
			f.allArtifacts(t)
			f.write(t, ".needs-summary", "{}")
		}, items: 1},
		{name: "finalized cache waiting for upload", setup: func(t *testing.T, f cacheFixture) {
			f.allArtifacts(t)
			f.write(t, artifactSummJSON, `{"title":"done"}`)
			f.write(t, "meta.json", `{"version":"1.0","title":"done","summary_status":"ok"}`)
		}, items: 1, uploadOnly: true},
		{name: "held cache", setup: func(t *testing.T, f cacheFixture) {
			f.transcript(t)
			hold(t, f.cacheDir)
		}, held: true},
		{name: "manual publication inferred without writing hold", setup: func(t *testing.T, f cacheFixture) {
			t.Setenv("OX_SESSION_PUBLISHING", "manual")
			f.transcript(t)
		}, held: true},
		{name: "unchanged refused publication", setup: func(t *testing.T, f cacheFixture) {
			f.transcript(t)
			fingerprint, err := refusalFingerprint(f.cacheDir)
			require.NoError(t, err)
			markPublicationRefused(f.cacheDir, fingerprint, slog.Default())
		}, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHeldFixture(t)
			tc.setup(t, f)
			heldBefore := session.IsHeld(f.cacheDir)
			h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
			got, err := h.Inspect(f.ledgerPath)
			require.NoError(t, err)
			require.Len(t, got.Items, tc.items)
			assert.Empty(t, got.Recovery)
			assert.Empty(t, got.Blocked)
			assert.Equal(t, tc.held, len(got.Held) == 1)
			assert.Equal(t, tc.refused, len(got.Refused) == 1)
			assert.Equal(t, heldBefore, session.IsHeld(f.cacheDir), "inspection must not create a hold")
			if tc.items > 0 {
				assert.Equal(t, tc.uploadOnly, got.Items[0].Payload.(*SessionFinalizePayload).UploadOnly)
			}
			items, err := h.Detect(f.ledgerPath)
			require.NoError(t, err)
			require.Len(t, items, len(got.Items), "inspection and processing must agree about eligible work")
		})
	}
}

// A re-armed empty Ledger session is settled deterministically by the daemon;
// inspection must not ask an AI coworker to summarize it. Once settled, its
// remaining upload keeps the same work kind as any other finalized session.
func TestInspect_EmptyLedgerTranscriptWorkKind(t *testing.T) {
	for _, settled := range []bool{false, true} {
		name := "needs deterministic settlement"
		if settled {
			name = "settled, upload pending"
		}
		t.Run(name, func(t *testing.T) {
			f := newHeldFixture(t)
			f.write(t, artifactRaw, emptyRaw)
			f.ledgerEntry(t, emptyRaw, false)
			if settled {
				for _, artifact := range requiredArtifacts {
					f.write(t, artifact, `{"title":"Brief session"}`)
				}
				f.write(t, "meta.json", `{"version":"1.0","title":"Brief session","summary_status":"ok"}`)
			} else {
				f.write(t, ".needs-summary", "{}")
			}
			h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
			got, err := h.Inspect(f.ledgerPath)
			require.NoError(t, err)
			if settled {
				assert.Empty(t, got.Automatic)
				require.Len(t, got.Items, 1)
				assert.True(t, got.Items[0].Payload.(*SessionFinalizePayload).UploadOnly)
			} else {
				assert.Equal(t, []string{f.name}, got.Automatic)
				assert.Empty(t, got.Items)
				assert.FileExists(t, filepath.Join(f.cacheDir, ".needs-summary"), "inspection must not settle the session")
				assert.NoFileExists(t, filepath.Join(f.cacheDir, artifactSummJSON))
			}
			items, err := h.Detect(f.ledgerPath)
			require.NoError(t, err)
			require.Len(t, items, 1, "daemon processing still queues either work kind")
			assert.Equal(t, settled, items[0].Payload.(*SessionFinalizePayload).UploadOnly)
		})
	}
}

// A health check must neither delete ghosts nor recover/clear recordings nor
// repair stale JSON tails. Raw belonging to a stale recording can be partial.
func TestInspect_DoesNotRepairRecordings(t *testing.T) {
	for _, tc := range []struct {
		name         string
		headerOnly   bool
		manual       bool
		trailingJSON bool
		rejected     bool
		live         bool
		ledgerCopy   bool
	}{
		{name: "stale cache recording"},
		{name: "stale Ledger recording", ledgerCopy: true},
		{name: "empty abandoned ghost", headerOnly: true},
		{name: "recorded manual mode", manual: true},
		{name: "recording with stale JSON tail", trailingJSON: true},
		{name: "quarantined source", rejected: true},
		{name: "live recording", live: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHeldFixture(t)
			if tc.ledgerCopy {
				f.cacheDir = filepath.Join(f.ledgerPath, "sessions", f.name)
			}
			f.transcript(t)
			if tc.headerOnly {
				f.write(t, artifactRaw, emptyRaw)
			}
			state := session.RecordingState{
				AgentID: "OxHeLd", SessionPath: f.cacheDir,
				StartedAt: time.Now().Add(-48 * time.Hour), ParentPID: 99999999,
				SourceRejected: tc.rejected,
			}
			if tc.manual {
				state.PublishingMode = "manual"
			}
			if tc.live {
				state.ParentPID = os.Getpid()
			}
			marker, err := json.Marshal(state)
			require.NoError(t, err)
			if tc.trailingJSON {
				marker = append(marker, []byte("stale tail")...)
			}
			f.write(t, recordingMarker, string(marker))
			if !tc.ledgerCopy && !tc.live && !tc.trailingJSON && !tc.rejected {
				// A second copy must not bypass a local hold or use raw before
				// this machine's recording has finished recovery.
				ledgerCopy := filepath.Join(f.ledgerPath, "sessions", f.name)
				require.NoError(t, os.MkdirAll(ledgerCopy, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(ledgerCopy, artifactRaw), []byte(testRawContent), 0600))
			}
			type originalFile struct {
				bytes []byte
				mtime time.Time
			}
			original := make(map[string]originalFile)
			for _, name := range []string{artifactRaw, recordingMarker} {
				path := filepath.Join(f.cacheDir, name)
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				info, err := os.Stat(path)
				require.NoError(t, err)
				original[path] = originalFile{data, info.ModTime()}
			}
			got, err := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler)).Inspect(f.ledgerPath)
			if tc.trailingJSON || tc.rejected {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Empty(t, got.Items, "inspection must not summarize a recording before recovery")
			if tc.manual {
				assert.Equal(t, []string{f.name}, got.Held)
			} else if !tc.live && !tc.trailingJSON && !tc.rejected {
				assert.Equal(t, []string{f.name}, got.Recovery)
			}
			for path, before := range original {
				data, err := os.ReadFile(path)
				require.NoError(t, err, "inspection must preserve %s", path)
				assert.Equal(t, before.bytes, data)
				info, err := os.Stat(path)
				require.NoError(t, err)
				assert.Equal(t, before.mtime, info.ModTime(), "inspection must not rewrite %s", path)
			}
			assert.NoFileExists(t, filepath.Join(f.cacheDir, session.HeldMarkerFile))
		})
	}
}

// A broken directory or session must be reported while preserving the valid
// work found elsewhere; failure must not read as an empty healthy Ledger.
func TestInspect_ReturnsPartialWorkAndReadErrors(t *testing.T) {
	for _, failure := range []string{"sessions directory", "metadata"} {
		t.Run(failure, func(t *testing.T) {
			f := newHeldFixture(t)
			f.transcript(t)
			f.write(t, ".needs-summary", "{}") // explicit local work, not an ambiguous download
			sessionsDir := filepath.Join(f.ledgerPath, "sessions")
			if failure == "sessions directory" {
				require.NoError(t, os.WriteFile(sessionsDir, []byte("not a directory"), 0600))
			} else {
				broken := filepath.Join(sessionsDir, "broken")
				require.NoError(t, os.MkdirAll(broken, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(broken, "meta.json"), []byte("{"), 0600))
			}
			got, err := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler)).Inspect(f.ledgerPath)
			require.Error(t, err)
			require.Len(t, got.Items, 1)
			assert.Equal(t, f.cacheDir, got.Items[0].Payload.(*SessionFinalizePayload).SessionDir)
			if failure != "sessions directory" {
				assert.ErrorContains(t, err, "broken")
			}
		})
	}
}
