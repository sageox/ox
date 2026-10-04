package main

// Customer promise under test (tests/acceptance lens):
// session-recording/list-and-view.feature, Rule "Reading a teammate's session
// never rewrites it". Devon downloads Riley's finished session to read it, and
// the daemon on Devon's machine must not re-summarize it and publish a new
// title and summary over Riley's (GH #1107).
//
// The proof drives the real `ox session download` command against a fake
// content store, then the real daemon finalize handler against a real bare
// remote, and asserts what a teammate would see: the remote's HEAD and the
// session's meta.json on it.
//
// Red-first (verified while authoring): make isDownloadedCopy return false
// after the .needs-summary check (dropping the marker and ledger checks) →
// both cases fail on "no new commit" — the daemon runs the LLM and pushes a
// new title to the remote.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// downloadLedgerFixture is a project + ledger clone + bare remote, with a fake
// content store the ledger's LFS URL points at. Pushes still land on the bare
// remote.
type downloadLedgerFixture struct {
	*draftLedgerFixture

	mu    sync.Mutex
	blobs map[string][]byte // bare OID → content
}

func newDownloadLedgerFixture(t *testing.T) *downloadLedgerFixture {
	t.Helper()
	f := &downloadLedgerFixture{draftLedgerFixture: newDraftLedgerFixture(t), blobs: map[string][]byte{}}
	t.Chdir(f.projectRoot)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	t.Setenv("SAGEOX_DAEMON", "false")
	priorDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	t.Cleanup(func() { gitserver.TestSetConfigDirOverride(priorDir) })
	priorStorage := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() { gitserver.TestSetForceFileStorage(priorStorage) })
	oldCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = oldCfg })
	cli.SetNoInteractive(true)
	t.Cleanup(func() { cli.SetNoInteractive(false) })

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/info/lfs/objects/batch"):
			var request struct {
				Operation string            `json:"operation"`
				Objects   []lfs.BatchObject `json:"objects"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			response := lfs.BatchResponse{Transfer: "basic"}
			for _, obj := range request.Objects {
				actions := &lfs.Actions{
					Upload: &lfs.Action{Href: serverURL + "/upload/" + obj.OID},
					Verify: &lfs.Action{Href: serverURL + "/verify"},
				}
				if request.Operation == "download" {
					actions = &lfs.Actions{Download: &lfs.Action{Href: serverURL + "/download/" + obj.OID}}
				}
				response.Objects = append(response.Objects, lfs.BatchResponseObject{OID: obj.OID, Size: obj.Size, Actions: actions})
			}
			w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
			_ = json.NewEncoder(w).Encode(response)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/download/"):
			f.mu.Lock()
			data, ok := f.blobs[strings.TrimPrefix(r.URL.Path, "/download/")]
			f.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(data)
		case r.Method == http.MethodPut, r.URL.Path == "/verify":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	serverURL = server.URL
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, gitserver.GitCredentials{
		Username: "testuser", Token: "test-token", ServerURL: server.URL, ExpiresAt: time.Now().Add(time.Hour),
	}))
	// the LFS batch URL derives from the fetch remote; pushes land on the bare repo
	runGit(t, f.ledgerPath, "remote", "set-url", "origin", server.URL+"/ledger.git")
	runGit(t, f.ledgerPath, "remote", "set-url", "--push", "origin", f.barePath)
	return f
}

// publishFinishedSession puts a session on the remote the way a teammate's
// finished session arrives: transcript and markdown as pointers into the
// content store, meta.json and summary.json as plain git files.
func (f *downloadLedgerFixture) publishFinishedSession(t *testing.T, name, title, status string) {
	t.Helper()
	sessionDir := filepath.Join(f.ledgerPath, "sessions", name)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	content := map[string][]byte{
		"raw.jsonl": []byte(`{"metadata":{"schema_version":"1","agent_type":"claude-code","username":"riley","agent_id":"OxRiLy"}}
{"type":"user","content":"Please make the ledger sync loop retry failed pushes with backoff and log every attempt clearly.","seq":1}
{"type":"assistant","content":"Added exponential backoff to the push loop and a structured log line for each attempt.","seq":2}
{"entry_count":2}
`),
		"summary.md": []byte("# " + title + "\n\nRiley's summary.\n"),
		"session.md": []byte("# Session\n\nRiley's session.\n"),
	}
	refs := map[string]lfs.FileRef{}
	f.mu.Lock()
	for filename, data := range content {
		ref := lfs.NewFileRef(data)
		f.blobs[ref.BareOID()] = data
		refs[filename] = ref
	}
	f.mu.Unlock()
	for filename, ref := range refs {
		require.NoError(t, lfs.WritePointerFile(filepath.Join(sessionDir, filename), lfs.AssertUploaded(ref)))
	}

	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, &lfs.SessionMeta{
		Version: "1.0", SessionName: name, SessionID: sessionScopedID(name),
		Username: "riley", AgentID: "OxRiLy", AgentType: "claude-code",
		CreatedAt: time.Date(2026, 9, 24, 23, 3, 0, 0, time.UTC),
		Title:     title, Summary: title, SummaryStatus: status,
		Files: refs,
	}))
	summaryJSON, err := json.Marshal(map[string]any{"title": title, "summary": "Riley's summary."})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.json"), summaryJSON, 0o644))

	runGit(t, f.ledgerPath, "add", "--sparse", "--", "sessions/"+name)
	runGit(t, f.ledgerPath, "commit", "--no-verify", "-m", "riley: finalize "+name)
	runGit(t, f.ledgerPath, "push", "origin", "HEAD")
}

// runFinalizePass runs one daemon finalize cycle the way the work manager
// does: Detect, then BuildPrompt, the LLM unless SkipLLM, and ProcessResult
// for every queued item. The "LLM" returns a valid summary with a new title,
// so any item that reaches it would publish that title.
func runFinalizePass(t *testing.T, projectRoot, ledgerPath string) (queued, llmRuns int) {
	t.Helper()
	h := agentwork.NewSessionFinalizeHandler(slog.New(slog.DiscardHandler))
	h.SetLedgerMu(&sync.Mutex{})
	h.SetProjectRoot(projectRoot)

	items, err := h.Detect(ledgerPath)
	require.NoError(t, err)
	for _, item := range items {
		req, err := h.BuildPrompt(item)
		require.NoError(t, err)
		result := &agentwork.RunResult{}
		if !req.SkipLLM {
			llmRuns++
			result = &agentwork.RunResult{
				Output:   `{"title":"Rewritten on Devon's machine","summary":"A fresh summary produced by the daemon that passes every validator in place today.","key_actions":["re-read the transcript","wrote a new title","pushed it"],"outcome":"success","topics_found":["ledger"],"quality_score":0.8}`,
				Duration: time.Second,
			}
		}
		require.NoError(t, h.ProcessResult(item, result))
	}
	return len(items), llmRuns
}

func TestSessionDownload_DaemonDoesNotRewriteTeammateSession(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		// preMarker simulates a copy downloaded by an ox that did not yet
		// write the download marker; only the Ledger's meta.json can tell.
		preMarker bool
	}{
		{name: "download", status: "ok"},
		{name: "download made before the marker existed", status: "ok", preMarker: true},
		{name: "download of an unrecoverable session made before the marker existed", status: "unrecoverable", preMarker: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDownloadLedgerFixture(t)
			const name = "2026-09-24T23-03-riley-OxRiLy"
			title := "Riley's title"
			if tc.status == "unrecoverable" {
				title = ""
			}
			f.publishFinishedSession(t, name, title, tc.status)
			remoteBefore := runGit(t, f.barePath, "rev-parse", "HEAD")
			localBefore := runGit(t, f.ledgerPath, "rev-parse", "HEAD")

			// When: Devon downloads Riley's session to read it.
			require.NoError(t, sessionHydrateCmd.RunE(sessionHydrateCmd, []string{name}))
			cacheDir := filepath.Join(f.ledgerPath, ".sageox", "cache", "sessions", name)
			require.FileExists(t, filepath.Join(cacheDir, "raw.jsonl"), "precondition: the download must land the transcript")
			if tc.preMarker {
				require.NoError(t, os.Remove(filepath.Join(cacheDir, lfs.DownloadedMarkerFile)))
			}

			// And the daemon's finalize scan runs on Devon's machine.
			queued, llmRuns := runFinalizePass(t, f.projectRoot, f.ledgerPath)

			// Then: nothing new is published from Devon's machine.
			assert.Equal(t, remoteBefore, runGit(t, f.barePath, "rev-parse", "HEAD"),
				"no new commit may reach the remote after a read-only download")
			assert.Equal(t, localBefore, runGit(t, f.ledgerPath, "rev-parse", "HEAD"),
				"no new commit may be made locally after a read-only download")
			var remoteMeta lfs.SessionMeta
			require.NoError(t, json.Unmarshal([]byte(runGit(t, f.barePath, "show", "HEAD:sessions/"+name+"/meta.json")), &remoteMeta))
			assert.Equal(t, title, remoteMeta.Title, "the Ledger must keep Riley's title")
			assert.Equal(t, tc.status, remoteMeta.SummaryStatus)
			assert.Zero(t, queued, "the downloaded copy must not be queued for finalization")
			assert.Zero(t, llmRuns, "the LLM must not run for a downloaded copy")

			// And the copy stays readable.
			assert.FileExists(t, filepath.Join(cacheDir, "raw.jsonl"))
			assert.NoFileExists(t, filepath.Join(cacheDir, "meta.json"), "a download must never write meta.json into the cache")
		})
	}
}

// TestReadOnlyDownloads_WriteMarker pins which paths mark their cache folder
// as a read-only download. view, regenerate, lint, token-optimize,
// redact-history and the secrets scan all read through openSessionContent;
// `ox session download` and view call hydrateFromLedger directly.
func TestReadOnlyDownloads_WriteMarker(t *testing.T) {
	for _, tc := range []struct {
		name     string
		download func(t *testing.T, f *downloadLedgerFixture, name string)
	}{
		{name: "ox session download", download: func(t *testing.T, f *downloadLedgerFixture, name string) {
			require.NoError(t, sessionHydrateCmd.RunE(sessionHydrateCmd, []string{name}))
		}},
		{name: "openSessionContent", download: func(t *testing.T, f *downloadLedgerFixture, name string) {
			_, err := openSessionContent(f.projectRoot, f.ledgerPath, name, "raw.jsonl")
			require.NoError(t, err)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDownloadLedgerFixture(t)
			const name = "2026-09-24T23-03-riley-OxRiLy"
			f.publishFinishedSession(t, name, "Riley's title", "ok")

			tc.download(t, f, name)

			cacheDir := filepath.Join(f.ledgerPath, ".sageox", "cache", "sessions", name)
			assert.FileExists(t, filepath.Join(cacheDir, "raw.jsonl"))
			assert.True(t, lfs.HasDownloadedMarker(cacheDir), "a read-only download must mark its cache folder")
			assert.NoFileExists(t, filepath.Join(cacheDir, "meta.json"), "a download must never write meta.json into the cache")
		})
	}
}

// TestSessionDownload_LeavesOwnWorkUnmarked: downloading into a cache folder
// that already holds this machine's pending work (here, a recording awaiting
// its summary) must not reclassify it as a read-only copy, or the daemon
// would drop the owner's own finalization.
func TestSessionDownload_LeavesOwnWorkUnmarked(t *testing.T) {
	f := newDownloadLedgerFixture(t)
	const name = "2026-09-24T23-03-riley-OxRiLy"
	f.publishFinishedSession(t, name, "Riley's title", "ok")
	cacheDir := filepath.Join(f.ledgerPath, ".sageox", "cache", "sessions", name)
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, ".needs-summary"), []byte("{}"), 0o644))

	require.NoError(t, sessionHydrateCmd.RunE(sessionHydrateCmd, []string{name}))

	assert.False(t, lfs.HasDownloadedMarker(cacheDir))
}
