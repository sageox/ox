package agentwork

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionCapture_UploadsNativeEntries verifies that native messages and tool
// calls survive recording, a watcher restart, finalization, LFS upload, and git
// push. Failure prevented: Codex recordings remain empty or lose custom tools
// while isolated parser and upload tests continue to pass.
func TestSessionCapture_UploadsNativeEntries(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds adapters, polls recordings, and pushes to a local git remote")
	}

	repoRoot, err := filepath.Abs("../../..")
	require.NoError(t, err)
	binDir := t.TempDir()
	for _, name := range []string{"codex", "claude-code"} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(binDir, "ox-adapter-"+name), "./cmd/ox-adapter-"+name)
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "build %s: %s", name, out)
	}

	// Keep every credential and cache write inside the fixture. The production
	// LFS client still loads credentials normally, using a loopback endpoint.
	for _, variable := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		t.Setenv(variable, t.TempDir())
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	previousConfigDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	t.Cleanup(func() { gitserver.TestSetConfigDirOverride(previousConfigDir) })
	previousFileStorage := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() { gitserver.TestSetForceFileStorage(previousFileStorage) })

	const userPrompt = "Verify that native conversation messages and tool calls survive recording, daemon restart, finalization, and upload to the team ledger."
	const firstResponse = "The initial capture contains the requested conversation and tool activity."
	const finalResponse = "The resumed recording is complete and all session content is ready for upload."
	for _, tc := range []struct {
		name           string
		sourceRelative string
		firstBatch     string
		lastBatch      string
		entryCount     int
	}{
		{
			name:           "codex",
			sourceRelative: ".codex/sessions/2026/09/07/session.jsonl",
			firstBatch: `{"type":"response_item","timestamp":"STAMP","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"PROMPT"}]}}
{"type":"response_item","timestamp":"STAMP","payload":{"type":"custom_tool_call","name":"functions.exec","input":"text(\"capture canary\")","call_id":"call_capture"}}
{"type":"response_item","timestamp":"STAMP","payload":{"type":"custom_tool_call_output","output":"capture canary","call_id":"call_capture"}}
{"type":"response_item","timestamp":"STAMP","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"FIRST"}]}}
`,
			lastBatch: `{"type":"response_item","timestamp":"STAMP","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Continue the same recording after the watcher restarts."}]}}
{"type":"response_item","timestamp":"STAMP","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"FINAL"}]}}
`,
			entryCount: 5,
		},
		{
			name:           "claude-code",
			sourceRelative: ".claude/projects/test/session.jsonl",
			firstBatch: `{"type":"user","timestamp":"STAMP","message":{"role":"user","content":"PROMPT"}}
{"type":"assistant","timestamp":"STAMP","message":{"role":"assistant","content":[{"type":"tool_use","id":"call_capture","name":"functions.exec","input":{"code":"text(\"capture canary\")"}}]}}
{"type":"user","timestamp":"STAMP","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_capture","content":"capture canary"}]}}
{"type":"assistant","timestamp":"STAMP","message":{"role":"assistant","content":[{"type":"text","text":"FIRST"}]}}
`,
			lastBatch: `{"type":"user","timestamp":"STAMP","message":{"role":"user","content":"Continue the same recording after the watcher restarts."}}
{"type":"assistant","timestamp":"STAMP","message":{"role":"assistant","content":[{"type":"text","text":"FINAL"}]}}
`,
			entryCount: 6,
		},
	} {
		for _, mode := range []struct {
			name       string
			uploadOnly bool
			rejectPush bool
			inLedger   bool
		}{
			{name: "finalize"},
			{name: "upload-only", uploadOnly: true},
			{name: "finalize-push-failure", rejectPush: true},
			{name: "upload-only-push-failure", uploadOnly: true, rejectPush: true},
			{name: "tracked-finalize-push-failure", inLedger: true, rejectPush: true},
		} {
			t.Run(tc.name+"/"+mode.name, func(t *testing.T) {
				adapter, err := adapters.NewExternalAdapter(filepath.Join(binDir, "ox-adapter-"+tc.name))
				require.NoError(t, err)
				var previousAdapter adapters.Adapter
				for _, name := range adapters.ListAdapters() {
					if name == tc.name {
						previousAdapter, err = adapters.GetAdapter(name)
						require.NoError(t, err)
					}
				}
				adapters.Unregister(tc.name)
				adapters.Register(adapter)
				t.Cleanup(func() {
					_ = adapter.Close()
					adapters.Unregister(tc.name)
					if previousAdapter != nil {
						adapters.Register(previousAdapter)
					}
				})

				// Reuse the real git fixture; only LFS HTTP is supplied locally. Git
				// pushes use a separate file:// push URL and never contact a service.
				barePath, ledgerPath := setupBareAndCloneLedger(t)
				var uploaded sync.Map
				var serverURL string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.Method == http.MethodPost && r.URL.Path == "/ledger.git/info/lfs/objects/batch":
						var request struct {
							Operation string            `json:"operation"`
							Objects   []lfs.BatchObject `json:"objects"`
						}
						if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
							http.Error(w, err.Error(), http.StatusBadRequest)
							return
						}
						response := lfs.BatchResponse{Transfer: "basic"}
						for _, object := range request.Objects {
							action := &lfs.Action{Href: serverURL + "/objects/" + object.OID}
							result := lfs.BatchResponseObject{OID: object.OID, Size: object.Size, Actions: &lfs.Actions{}}
							if request.Operation == "upload" {
								result.Actions.Upload = action
							} else if _, ok := uploaded.Load(object.OID); ok {
								result.Actions.Download = action
							} else {
								result.Error = &lfs.ObjectError{Code: 404, Message: "not uploaded"}
							}
							response.Objects = append(response.Objects, result)
						}
						w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
						_ = json.NewEncoder(w).Encode(response)
					case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/objects/"):
						content, err := io.ReadAll(r.Body)
						if err != nil || lfs.ComputeOID(content) != filepath.Base(r.URL.Path) {
							http.Error(w, "invalid uploaded content", http.StatusBadRequest)
							return
						}
						uploaded.Store(filepath.Base(r.URL.Path), content)
						w.WriteHeader(http.StatusOK)
					default:
						http.NotFound(w, r)
					}
				}))
				t.Cleanup(server.Close)
				serverURL = server.URL
				t.Setenv("SAGEOX_ENDPOINT", serverURL)
				require.NoError(t, gitserver.SaveCredentialsForEndpoint(serverURL, gitserver.GitCredentials{
					Username: "testuser", Token: "test-token", ServerURL: serverURL,
					ExpiresAt: time.Now().Add(time.Hour),
				}))
				runGitCmd(t, ledgerPath, "remote", "set-url", "origin", serverURL+"/ledger.git")
				runGitCmd(t, ledgerPath, "remote", "set-url", "--push", "origin", "file://"+barePath)

				projectRoot := t.TempDir()
				sessionHome := t.TempDir()
				source := filepath.Join(sessionHome, filepath.FromSlash(tc.sourceRelative))
				require.NoError(t, os.MkdirAll(filepath.Dir(source), 0o755))
				require.NoError(t, os.WriteFile(source, nil, 0o600))
				state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
					AgentID: "OxCapture", AdapterName: tc.name, AgentType: tc.name,
					SessionFile: source, Username: "testuser", WorkspacePath: projectRoot,
					RepoContextPath: filepath.Join(ledgerPath, ".sageox", "cache"),
					ParentPID:       os.Getpid(), WatchMode: "tail",
				})
				require.NoError(t, err)
				sessionName := filepath.Base(state.SessionPath)
				require.Equal(t, filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName), state.SessionPath)
				rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
				writer, err := session.NewRawWriter(rawPath, projectRoot)
				require.NoError(t, err)
				require.NoError(t, writer.WriteRaw(map[string]any{
					"type": "header",
					"metadata": &session.StoreMeta{
						Version: "1.0", SessionID: state.SessionID, AgentID: state.AgentID,
						AgentType: tc.name, Username: "testuser", CreatedAt: state.StartedAt,
					},
				}))
				require.NoError(t, writer.CloseAndSync())

				var sourceContent string
				for _, batch := range []string{tc.firstBatch, tc.lastBatch} {
					sourceContent += strings.NewReplacer(
						"STAMP", time.Now().UTC().Format(time.RFC3339Nano),
						"PROMPT", userPrompt, "FIRST", firstResponse, "FINAL", finalResponse,
					).Replace(batch)
					require.NoError(t, os.WriteFile(source, []byte(sourceContent), 0o600))
					manager := NewSessionWatcherManager(slog.Default())
					manager.SetHomeDirForTest(sessionHome)
					t.Cleanup(manager.StopAll)
					require.Equal(t, 1, manager.DetectAndRestart(ledgerPath), "recording created by StartRecording must be discoverable")
					require.Eventually(t, func() bool {
						data, err := os.ReadFile(filepath.Join(state.SessionPath, recordingMarker))
						var recorded session.RecordingState
						return err == nil && json.Unmarshal(data, &recorded) == nil && recorded.SourceOffset == int64(len(sourceContent))
					}, 10*time.Second, 25*time.Millisecond, "native entries must reach the recording cache")
					manager.StopAll()
				}

				captured, err := session.ReadSessionFromPath(rawPath)
				require.NoError(t, err)
				require.Len(t, captured.Entries, tc.entryCount, "restart must preserve all entries without duplicates")
				// The finalizer owns the finished cache after the recording marker is
				// removed, as in the explicit stop/SessionEnd handoff.
				require.NoError(t, os.Remove(filepath.Join(state.SessionPath, recordingMarker)))
				cachedRaw, err := os.ReadFile(rawPath)
				require.NoError(t, err)
				if mode.inLedger {
					trackedDir := filepath.Join(ledgerPath, "sessions", sessionName)
					require.NoError(t, os.Rename(state.SessionPath, trackedDir))
					rawPath = filepath.Join(trackedDir, "raw.jsonl")
				}
				if mode.uploadOnly {
					for _, name := range requiredArtifacts {
						require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(rawPath), name), []byte("{}"), 0o600))
					}
				}
				if mode.rejectPush {
					runGitCmd(t, ledgerPath, "remote", "set-url", "--push", "origin", "file://"+filepath.Join(t.TempDir(), "missing.git"))
				}
				handler := NewSessionFinalizeHandler(slog.Default())
				handler.SetProjectRoot(projectRoot)
				handler.SetLedgerMu(&sync.Mutex{})
				items, err := handler.Detect(ledgerPath)
				require.NoError(t, err)
				require.Len(t, items, 1)
				request, err := handler.BuildPrompt(items[0])
				require.NoError(t, err)
				require.Equal(t, mode.uploadOnly, request.SkipLLM)
				// Only the summary response is supplied: artifact generation, LFS
				// upload, pointer publication, and git push use production code.
				processErr := handler.ProcessResult(items[0], &RunResult{Output: `{"title":"Native session capture and upload","summary":"Verified native conversation and tool capture across watcher restart, then finalized the session into the team ledger.","key_actions":["Captured native messages and tools","Resumed recording from its persisted cursor","Uploaded session artifacts"],"outcome":"success","topics_found":["session capture"],"quality_score":0.9,"score_reason":"Verified the complete session recording and upload lifecycle"}`})
				if mode.uploadOnly && mode.rejectPush {
					require.Error(t, processErr)
				} else {
					require.NoError(t, processErr)
				}

				// Even a failed push must not leave a raw-only intermediate commit
				// that a later retry could publish and trigger GitLab GC against.
				commits := strings.Fields(gitOutput(t, ledgerPath, "log", "--reverse", "--format=%H", "--", "sessions/"+sessionName))
				require.Len(t, commits, 1, "finalization commits the uploaded pointers once")
				for _, name := range []string{"raw.jsonl", "session.md", "summary.md"} {
					firstContent := gitOutput(t, ledgerPath, "show", commits[0]+":sessions/"+sessionName+"/"+name)
					_, _, err := lfs.ParsePointer(firstContent)
					require.NoError(t, err, "%s must be an LFS pointer in the first session commit", name)
				}
				if mode.rejectPush {
					retained, err := os.ReadFile(filepath.Join(state.SessionPath, "raw.jsonl"))
					require.NoError(t, err, "a failed push must retain the source cache")
					assert.Equal(t, cachedRaw, retained)
					assert.Empty(t, gitOutput(t, barePath, "ls-tree", "HEAD", "sessions/"+sessionName), "failed publication must leave the remote unchanged")
					runGitCmd(t, ledgerPath, "remote", "set-url", "--push", "origin", "file://"+barePath)
					retryItems, err := handler.Detect(ledgerPath)
					require.NoError(t, err)
					require.Len(t, retryItems, 1, "retained cache must be rediscovered")
					require.True(t, retryItems[0].Payload.(*SessionFinalizePayload).UploadOnly, "retry should reuse finalized artifacts")
					require.NoError(t, handler.ProcessResult(retryItems[0], &RunResult{}), "pending pointer publication must succeed on retry")
				}

				verifyClone := filepath.Join(t.TempDir(), "verify")
				runGitCmd(t, t.TempDir(), "clone", "file://"+barePath, verifyClone)
				publishedDir := filepath.Join(verifyClone, "sessions", sessionName)
				meta, err := lfs.ReadSessionMeta(publishedDir)
				require.NoError(t, err)
				require.Equal(t, state.SessionID, meta.SessionID)
				require.Equal(t, tc.name, meta.AgentType)
				for _, name := range []string{"raw.jsonl", "session.md", "summary.md"} {
					assert.Empty(t, gitOutput(t, ledgerPath, "status", "--porcelain", "--", "sessions/"+sessionName+"/"+name),
						"published pointers must leave no staged or unstaged changes")
					ref, err := lfs.ReadPointerFile(filepath.Join(publishedDir, name))
					require.NoError(t, err, "%s must be an LFS pointer in a fresh clone", name)
					require.Equal(t, meta.Files[name], ref)
					blob, ok := uploaded.Load(ref.BareOID())
					require.True(t, ok, "%s must reference an uploaded object", name)
					require.Equal(t, ref.Size, int64(len(blob.([]byte))))
					if name == "raw.jsonl" {
						content := string(blob.([]byte))
						for _, expected := range []string{userPrompt, firstResponse, finalResponse, "functions.exec"} {
							assert.Equal(t, 1, strings.Count(content, expected), "%q must be uploaded once", expected)
						}
						assert.Contains(t, content, "capture canary", "tool input must survive upload")
					}
				}
				assert.NoDirExists(t, state.SessionPath, "cache is pruned only after successful publication")
				remaining, err := handler.Detect(ledgerPath)
				require.NoError(t, err)
				assert.Empty(t, remaining, "the completed upload must not be queued again")
			})
		}
	}
}
