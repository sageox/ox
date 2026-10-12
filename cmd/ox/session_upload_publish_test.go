package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
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
	recording := func(content string) func(t *testing.T, cacheDir, _ string) {
		return func(t *testing.T, cacheDir, _ string) {
			require.NoError(t, os.WriteFile(filepath.Join(cacheDir, ".recording.json"), []byte(content), 0o600))
		}
	}
	tests := []struct {
		name    string
		setup   func(t *testing.T, cacheDir, ledgerDir string)
		want    bool
		wantErr string
	}{
		{"held, even with a Ledger entry from before the hold", func(t *testing.T, cacheDir, ledgerDir string) {
			writeLedgerMeta(t, ledgerDir, `{"version":"1.0","title":"done"}`)
			require.NoError(t, session.WriteHoldMarker(cacheDir, session.HoldManualPublishing, "test"))
		}, true, ""},
		{"cache-only orphan, no Ledger entry", func(*testing.T, string, string) {}, true, ""},
		{"Ledger holds only a draft placeholder", func(t *testing.T, _, ledgerDir string) {
			writeLedgerMeta(t, ledgerDir, `{"version":"1.0","draft":true}`)
		}, true, ""},
		{"earlier publish left a pending retry", func(t *testing.T, cacheDir, ledgerDir string) {
			writeLedgerMeta(t, ledgerDir, `{"version":"1.0","title":"done"}`)
			require.NoError(t, os.WriteFile(filepath.Join(cacheDir, sessionUploadRetryPendingFile), []byte("{}"), 0o644))
		}, true, ""},
		{"already published", func(t *testing.T, _, ledgerDir string) {
			writeLedgerMeta(t, ledgerDir, `{"version":"1.0","title":"done"}`)
		}, false, ""},
		{"no conversation in the cache copy", func(t *testing.T, cacheDir, _ string) {
			require.NoError(t, os.WriteFile(filepath.Join(cacheDir, ledgerFileRaw), []byte(`{"type":"header"}`+"\n"), 0o644))
			require.NoError(t, session.WriteHoldMarker(cacheDir, session.HoldManualPublishing, "test"))
		}, false, ""},
		{"quarantined for ownership review", recording(`{"agent_id":"OxPuBl","source_rejected":true}`), false, "--release-quarantine"},
		{"still recording", recording(`{"agent_id":"OxPuBl"}`), false, "still recording"},
		{"recording state unreadable", recording(`{not json`), false, "cannot be read"},
		{"downloaded from the Ledger", func(t *testing.T, cacheDir, _ string) {
			// what a download leaves: the marker lands before the transcript
			require.NoError(t, os.WriteFile(filepath.Join(cacheDir, lfs.DownloadedMarkerFile), []byte("x\n"), 0o644))
		}, false, "downloaded from the ledger"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			ledgerPath := t.TempDir()
			cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", name)
			require.NoError(t, os.MkdirAll(cacheDir, 0o755))
			writeTestRawJSONLWithEntries(t, filepath.Join(cacheDir, ledgerFileRaw), 3)
			tt.setup(t, cacheDir, filepath.Join(ledgerPath, "sessions", name))

			gotName, gotDir, ok, err := findPublishableCacheSession(ledgerPath, "OxPuBl")

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
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

// Failure prevented: 'ox session upload' publishes a cache folder that a
// recording still owns. A quarantined recording (#1065) holds content from a
// source that failed its ownership check; a live one is still capturing. The
// quarantine check on the Ledger copy never sees it, because the publish
// copies the transcript there before checking.
func TestUpload_RefusesASessionARecordingStillOwns(t *testing.T) {
	cases := []struct {
		name      string
		recording string
		wantErr   string
	}{
		{"quarantined for ownership review", `{"agent_id":"OxQuAr","source_rejected":true}`, "ownership review"},
		{"still recording", `{"agent_id":"OxQuAr"}`, "still recording"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDownloadLedgerFixture(t)
			const name = "2026-10-05T10-00-devon-OxQuAr"
			cacheDir := filepath.Join(f.ledgerPath, ".sageox", "cache", "sessions", name)
			require.NoError(t, os.MkdirAll(cacheDir, 0o755))
			writeTestRawJSONLWithEntries(t, filepath.Join(cacheDir, ledgerFileRaw), 3)
			require.NoError(t, os.WriteFile(filepath.Join(cacheDir, ".recording.json"), []byte(tc.recording), 0o600))
			remoteBefore := runGit(t, f.barePath, "rev-parse", "HEAD")

			err := sessionUploadCmd.RunE(sessionUploadCmd, []string{"OxQuAr"})

			require.ErrorContains(t, err, tc.wantErr)
			assert.Equal(t, remoteBefore, runGit(t, f.barePath, "rev-parse", "HEAD"), "nothing reached the remote")
			assert.NoDirExists(t, filepath.Join(f.ledgerPath, "sessions", name), "nothing was copied into the shared tree")
		})
	}
}

// Failure prevented: a coworker reads a teammate's session with 'ox session
// download'; the teammate later removes it from the Ledger. With no Ledger
// entry left, the downloaded copy looked like this machine's unpublished
// work: doctor held it, and 'ox session upload' republished it under the
// teammate's name.
func TestUpload_NeverRepublishesADownloadedCopy(t *testing.T) {
	f := newDownloadLedgerFixture(t)
	t.Setenv("OX_SESSION_PUBLISHING", "manual")
	const name = "2026-09-24T23-03-riley-OxRiLy"
	f.publishFinishedSession(t, name, "Riley's title", "ok")
	require.NoError(t, sessionHydrateCmd.RunE(sessionHydrateCmd, []string{name}))
	cacheDir := filepath.Join(f.ledgerPath, ".sageox", "cache", "sessions", name)
	require.FileExists(t, filepath.Join(cacheDir, ledgerFileRaw))

	// the teammate removes the session from the shared Ledger
	runGit(t, f.ledgerPath, "rm", "-r", "-q", "--", "sessions/"+name)
	runGit(t, f.ledgerPath, "commit", "--no-verify", "-q", "-m", "riley: remove "+name)
	runGit(t, f.ledgerPath, "push", "-q", "origin", "HEAD")
	remoteBefore := runGit(t, f.barePath, "rev-parse", "HEAD")

	listed := listUnpublishedCacheSessions(f.ledgerPath)
	assert.NotContains(t, listed.Held, name, "a download is not this machine's work")
	assert.NotContains(t, listed.Waiting, name, "a download is not this machine's work")
	_, _ = checkHeldSessions(f.projectRoot, f.ledgerPath)
	assert.False(t, session.IsHeld(cacheDir), "doctor must not hold a teammate's session as ours")

	err := sessionUploadCmd.RunE(sessionUploadCmd, []string{"OxRiLy"})

	require.ErrorContains(t, err, "downloaded")
	assert.Equal(t, remoteBefore, runGit(t, f.barePath, "rev-parse", "HEAD"), "a teammate's removed session is not republished")
}
