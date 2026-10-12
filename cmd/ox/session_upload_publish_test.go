package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
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

// Failure prevented: names have minute precision, so a held session can share
// its name with a different session already published. 'ox session upload'
// replaced that session's transcript under its session ID, and every link to
// it showed this one instead.
func TestUpload_NeverPublishesOverADifferentSession(t *testing.T) {
	f := newDownloadLedgerFixture(t)
	const name = "2026-09-24T23-03-riley-OxRiLy"
	f.publishFinishedSession(t, name, "Riley's title", "ok")
	ledgerDir := filepath.Join(f.ledgerPath, "sessions", name)
	rawBefore, err := os.ReadFile(filepath.Join(ledgerDir, ledgerFileRaw))
	require.NoError(t, err)
	cacheDir := filepath.Join(f.ledgerPath, ".sageox", "cache", "sessions", name)
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, ledgerFileRaw), []byte(`{"type":"header","metadata":{"schema_version":"1","agent_type":"claude-code","username":"riley","agent_id":"OxRiLy","session_id":"ses_01890000-0000-7000-8000-0000000c0de1"}}
{"type":"user","content":"A different conversation recorded in the same minute.","seq":1}
{"entry_count":1}
`), 0o644))
	require.NoError(t, session.WriteHoldMarker(cacheDir, session.HoldManualPublishing, "session_stop"))
	remoteBefore := runGit(t, f.barePath, "rev-parse", "HEAD")

	err = sessionUploadCmd.RunE(sessionUploadCmd, []string{name})

	require.ErrorIs(t, err, errRecoveryRefused)
	assert.Equal(t, remoteBefore, runGit(t, f.barePath, "rev-parse", "HEAD"), "the published session is untouched on the remote")
	rawAfter, err := os.ReadFile(filepath.Join(ledgerDir, ledgerFileRaw))
	require.NoError(t, err)
	assert.Equal(t, string(rawBefore), string(rawAfter), "the published transcript is untouched")
	assert.True(t, session.IsHeld(cacheDir), "the held session stays held")
}

// Failure prevented: with agent.summarizer=off, a session held by an exit hook
// (transcript only, no summary files) is published and left in the cache. The
// daemon, which never reads agent.summarizer, finds a session with no summary
// and runs the LLM on it.
func TestUpload_SummarizerOffLeavesNothingForTheDaemon(t *testing.T) {
	f := newDownloadLedgerFixture(t)
	userConfig := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(userConfig, []byte("agent_summarizer: \"off\"\n"), 0o600))
	t.Setenv("OX_USER_CONFIG", userConfig)
	require.Equal(t, config.AgentSummarizerOff, config.GetAgentSummarizer(f.projectRoot))
	const name = "2026-10-05T10-00-devon-OxNoSm"
	cacheDir := filepath.Join(f.ledgerPath, ".sageox", "cache", "sessions", name)
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	// a conversation worth summarizing, so the daemon would run the LLM on it
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, ledgerFileRaw), []byte(`{"metadata":{"schema_version":"1","agent_type":"claude-code","username":"devon","agent_id":"OxNoSm"},"type":"header"}
{"type":"user","content":"Refactor the ledger sync retry loop so failed pushes back off exponentially, and log each attempt with its delay.","seq":1}
{"type":"assistant","content":"Done: pushes now back off exponentially with jitter, and every attempt logs its delay and error.","seq":2}
{"entry_count":2}
`), 0o644))
	require.NoError(t, session.WriteHoldMarker(cacheDir, session.HoldManualPublishing, "hook_end"))
	remoteBefore := runGit(t, f.barePath, "rev-parse", "HEAD")

	require.NoError(t, sessionUploadCmd.RunE(sessionUploadCmd, []string{"OxNoSm"}))

	assert.NotEqual(t, remoteBefore, runGit(t, f.barePath, "rev-parse", "HEAD"), "the publish reached the remote")
	queued, llmRuns := runFinalizePass(t, f.projectRoot, f.ledgerPath)
	assert.Zero(t, queued, "agent.summarizer=off: the daemon has nothing left to do")
	assert.Zero(t, llmRuns, "agent.summarizer=off: the published session is never summarized")
}

// Failure prevented: daemon recovery rebuilds a lost raw.jsonl header in the
// {"_meta":...} form, then holds the manual recording. 'ox session upload'
// accepted only {"metadata":...} and refused it with "no metadata in header",
// so the coworker could never publish it.
func TestUpload_PublishesARecoveredHeader(t *testing.T) {
	f := newDownloadLedgerFixture(t)
	const name = "2026-10-05T10-00-devon-OxReCv"
	cacheDir := filepath.Join(f.ledgerPath, ".sageox", "cache", "sessions", name)
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, ledgerFileRaw), []byte(`{"_meta":{"schema_version":"1","agent_id":"OxReCv","agent_type":"claude-code","recovered":true}}
{"type":"user","content":"Recovered after the original header was lost.","seq":1}
{"type":"assistant","content":"The recovered conversation is intact.","seq":2}
`), 0o644))
	require.NoError(t, session.WriteHoldMarker(cacheDir, session.HoldManualPublishing, "daemon_recovery"))
	remoteBefore := runGit(t, f.barePath, "rev-parse", "HEAD")

	require.NoError(t, sessionUploadCmd.RunE(sessionUploadCmd, []string{"OxReCv"}))

	assert.NotEqual(t, remoteBefore, runGit(t, f.barePath, "rev-parse", "HEAD"), "the recovered session reached the remote")
	meta, err := lfs.ReadSessionMeta(filepath.Join(f.ledgerPath, "sessions", name))
	require.NoError(t, err)
	assert.Equal(t, 2, meta.EntryCount)
}

// Failure prevented: a suffix that names several sessions, routine once a
// /clear reuses the agent ID, silently published one the coworker did not
// name: an ambiguous match in one cache location was skipped and a unique
// match in the next location won.
func TestFindPublishableCacheSession_RefusesAnAmbiguousName(t *testing.T) {
	held := func(t *testing.T, base, name string) {
		dir := filepath.Join(base, name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		writeTestRawJSONLWithEntries(t, filepath.Join(dir, ledgerFileRaw), 3)
		require.NoError(t, session.WriteHoldMarker(dir, session.HoldManualPublishing, "test"))
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, ledgerPath string)
	}{
		{"two held here, one more in the XDG cache", func(t *testing.T, ledgerPath string) {
			dirs := session.HeldSessionDirs(ledgerPath)
			held(t, dirs[0], "2026-10-05T10-00-devon-OxAmBg")
			held(t, dirs[0], "2026-10-05T11-00-devon-OxAmBg")
			held(t, dirs[1], "2026-09-30T08-00-devon-OxAmBg")
		}},
		{"held in the cache, another already published", func(t *testing.T, ledgerPath string) {
			held(t, session.HeldSessionDirs(ledgerPath)[0], "2026-10-05T10-00-devon-OxAmBg")
			writeLedgerMeta(t, filepath.Join(ledgerPath, "sessions", "2026-09-30T08-00-devon-OxAmBg"), `{"version":"1.0","title":"done"}`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			ledgerPath := t.TempDir()
			tc.setup(t, ledgerPath)

			_, _, ok, err := findPublishableCacheSession(ledgerPath, "OxAmBg")
			require.ErrorContains(t, err, "ambiguous session name")
			assert.False(t, ok)

			name, _, ok, err := findPublishableCacheSession(ledgerPath, "2026-10-05T10-00-devon-OxAmBg")
			require.NoError(t, err, "the full name is never ambiguous")
			assert.True(t, ok)
			assert.Equal(t, "2026-10-05T10-00-devon-OxAmBg", name)
		})
	}
}
