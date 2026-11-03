package agentwork

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/trace/materialize"
	"github.com/sageox/ox/internal/trace/model"
	"github.com/stretchr/testify/require"
)

func TestTraceFinalizeDoorsPublishPointers(t *testing.T) {
	for _, door := range []string{"normal", "orphan-upload-only", "invalid-trace", "missing-stop", "orphan-missing-stop", "synchronized-forged-trace"} {
		t.Run(door, func(t *testing.T) {
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("OX_XDG_DISABLE", "")
			t.Setenv("OX_GIT_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "credentials.json"))
			bare, ledger := setupBareAndCloneLedger(t)
			var mu sync.Mutex
			uploaded := map[string][]byte{}
			var serverURL string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/objects/batch") {
					var req struct {
						Objects []lfs.BatchObject `json:"objects"`
					}
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					resp := lfs.BatchResponse{Transfer: "basic"}
					for _, obj := range req.Objects {
						resp.Objects = append(resp.Objects, lfs.BatchResponseObject{OID: obj.OID, Size: obj.Size, Actions: &lfs.Actions{Upload: &lfs.Action{Href: serverURL + "/upload/" + obj.OID}}})
					}
					w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
					_ = json.NewEncoder(w).Encode(resp)
					return
				}
				if r.Method == http.MethodPut {
					data, err := io.ReadAll(r.Body)
					if err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					mu.Lock()
					uploaded[filepath.Base(r.URL.Path)] = data
					mu.Unlock()
					w.WriteHeader(http.StatusOK)
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			serverURL = server.URL
			// The PAT is only sent to its endpoint's git server, so the endpoint is this server.
			t.Setenv(endpoint.EnvVar, server.URL)
			runGitCmd(t, ledger, "remote", "set-url", "origin", server.URL+"/ledger.git")
			runGitCmd(t, ledger, "remote", "set-url", "--push", "origin", bare)
			project := t.TempDir()
			require.NoError(t, gitserver.SaveCredentialsForEndpoint(endpoint.GetForProject(project), gitserver.GitCredentials{Username: "test", Token: "local-test"}))
			const nativeID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(configPath, []byte("trace:\n  enabled: true\n"), 0600))
			t.Setenv("OX_USER_CONFIG", configPath)
			local, err := session.StartRecording(t.TempDir(), session.StartRecordingOptions{
				AgentID: "OxTraceDoor", AgentSessionID: nativeID, AdapterName: "claude-code", Username: "test",
				RepoContextPath: filepath.Join(ledger, ".sageox", "cache"),
			})
			require.NoError(t, err)
			require.NotNil(t, local.Trace)
			cache := local.SessionPath
			name := filepath.Base(cache)
			require.NoError(t, os.Remove(filepath.Join(cache, ".recording.json")))
			now := time.Now().UTC()
			if door == "synchronized-forged-trace" {
				local.RecordTraceBoundary("pause", now)
			}
			spans := []byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"kept-span","attributes":[{"key":"user.email","value":{"stringValue":"PRIVATE_EMAIL"}}]}]}]}]}` + "\n")
			events := []byte(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"body":{"stringValue":"kept-event"}}]}]}]}` + "\n")
			if door == "invalid-trace" {
				spans = []byte("not-json\n")
			}
			spool := filepath.Join(paths.TraceSpoolDir(), nativeID)
			require.NoError(t, os.MkdirAll(spool, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(spool, "traces.jsonl"), spans, 0600))
			require.NoError(t, os.WriteFile(filepath.Join(spool, "logs.jsonl"), events, 0600))
			if door == "synchronized-forged-trace" {
				local.RecordTraceBoundary("resume", now)
			}
			// Only a live stop can capture the immutable boundary before reclaim.
			if door != "missing-stop" && door != "orphan-missing-stop" {
				local.RecordTraceBoundary("stop", now)
			}
			capture := local.Trace
			if door == "synchronized-forged-trace" {
				// A teammate knows a retained UUID and offsets bracketing a pause,
				// but omits the pause from a plain Git-stored raw header. Neither
				// the valid ranges nor the shared transcript authorize a spool read.
				capture = &model.Capture{Boundaries: []model.Boundary{capture.Boundaries[0], capture.Boundaries[3]}}
				name += "-teammate"
				cache = filepath.Join(ledger, "sessions", name)
				require.NoError(t, os.MkdirAll(cache, 0700))
			}
			meta := session.StoreMeta{AgentID: "OxTraceDoor", AgentType: "claude-code", Username: "test", CreatedAt: now.Add(-time.Minute), NativeSessions: []lfs.NativeSession{{ID: nativeID, FirstSeen: now.Add(-time.Minute), LastSeen: now}}, TraceCapture: capture}
			if door == "orphan-upload-only" {
				// The header predates the live stop; reclaim must carry the
				// complete durable marker without taking a new observation.
				meta.TraceCapture = &model.Capture{Boundaries: capture.Boundaries[:1]}
			}
			header, err := json.Marshal(map[string]any{"type": "header", "metadata": meta})
			require.NoError(t, err)
			raw := append(header, []byte("\n{\"type\":\"user\",\"content\":\"Implement the session trace attachment feature with byte offsets and pointer-only Ledger uploads.\",\"seq\":1}\n{\"type\":\"assistant\",\"content\":\"Implemented the feature and validated local trace capture.\",\"seq\":2}\n")...)
			require.NoError(t, os.WriteFile(filepath.Join(cache, "raw.jsonl"), raw, 0600))
			if door == "synchronized-forged-trace" {
				runGitCmd(t, ledger, "add", "sessions/"+name+"/raw.jsonl")
				runGitCmd(t, ledger, "commit", "-m", "Teammate session with forged trace metadata")
			}
			if door == "orphan-upload-only" || door == "orphan-missing-stop" {
				state := session.RecordingState{SessionPath: cache, Trace: capture, NativeSessions: meta.NativeSessions, StoppedAt: &now}
				stateData, err := json.Marshal(state)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(cache, ".recording.json"), stateData, 0600))
				// A subsequent recording in the same native session has already
				// emitted bytes before the dead recording is reclaimed.
				later := []byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"LATER_RECORDING_PRIVATE"}]}]}]}` + "\n")
				require.NoError(t, os.WriteFile(filepath.Join(spool, "traces.jsonl"), append(append([]byte(nil), spans...), later...), 0600))
				stampCarrierBeforeReclaim(slog.Default(), cache, filepath.Join(cache, "raw.jsonl"), &state)
			}

			handler := NewSessionFinalizeHandler(slog.Default())
			handler.projectRoot = project
			staged := false
			handler.afterStageTestHook = func() {
				staged = true
				if door == "orphan-upload-only" {
					stored, err := session.ReadSessionFromPath(filepath.Join(cache, "raw.jsonl"))
					require.NoError(t, err)
					require.NotNil(t, stored.Meta.TraceCapture)
					require.Len(t, stored.Meta.TraceCapture.Boundaries, 2)
					last := stored.Meta.TraceCapture.Boundaries[1]
					require.Equal(t, "stop", last.Action)
					require.Equal(t, model.Offsets{Spans: int64(len(spans)), Events: int64(len(events))}, last.Offsets[nativeID])
				}
				if door == "invalid-trace" || door == "missing-stop" || door == "orphan-missing-stop" {
					return
				}
				for _, file := range []string{materialize.SpansFile, materialize.EventsFile} {
					require.FileExists(t, filepath.Join(cache, file), "compressed content must stay in cache until push succeeds")
					require.True(t, lfs.IsPointerFile(filepath.Join(ledger, "sessions", name, file)), "tracked trace paths must be pointers before commit")
				}
			}
			payload := &SessionFinalizePayload{SessionDir: cache, RawPath: filepath.Join(cache, "raw.jsonl"), LedgerPath: ledger, UploadOnly: door == "orphan-upload-only" || door == "orphan-missing-stop"}
			if door == "synchronized-forged-trace" {
				items, err := handler.Detect(ledger)
				require.NoError(t, err)
				require.Len(t, items, 1)
				payload, err = extractPayload(items[0])
				require.NoError(t, err)
				require.Equal(t, cache, payload.SessionDir)
			}
			result := &RunResult{Output: `{"title":"Trace attachment implemented","summary":"Implemented session trace attachments with byte windows and local validation.","key_actions":["implemented trace capture"],"outcome":"success","topics_found":["tracing"],"quality_score":0.8,"score_reason":"Feature implementation"}`}
			require.NoError(t, handler.ProcessResult(&WorkItem{ID: "trace-finalize", Type: sessionFinalizeType, Payload: payload}, result))
			require.True(t, staged, "actual finalization door must stage a commit")
			tracked := filepath.Join(ledger, "sessions", name)
			finalMeta, err := lfs.ReadSessionMeta(tracked)
			require.NoError(t, err)
			require.Contains(t, finalMeta.Files, "raw.jsonl", "ordinary recording upload must survive trace failure")
			if door == "invalid-trace" || door == "missing-stop" || door == "orphan-missing-stop" || door == "synchronized-forged-trace" {
				require.Nil(t, finalMeta.Trace)
				for _, file := range []string{materialize.SpansFile, materialize.EventsFile} {
					require.NotContains(t, finalMeta.Files, file)
					require.NoFileExists(t, filepath.Join(tracked, file))
					require.NoFileExists(t, filepath.Join(ledger, ".sageox", "cache", "sessions", name, file))
				}
				mu.Lock()
				defer mu.Unlock()
				for _, data := range uploaded {
					require.False(t, bytes.HasPrefix(data, []byte{0x1f, 0x8b}), "no trace gzip may be uploaded")
				}
				return
			}
			require.NotNil(t, finalMeta.Trace)
			require.Equal(t, int64(1), finalMeta.Trace.Spans)
			require.Equal(t, int64(1), finalMeta.Trace.Events)
			for _, file := range []string{materialize.SpansFile, materialize.EventsFile} {
				ref, ok := finalMeta.Files[file]
				require.True(t, ok)
				committed := gitOutput(t, bare, "show", "HEAD:sessions/"+name+"/"+file)
				require.True(t, strings.HasPrefix(committed, "version https://git-lfs.github.com/spec/v1\n"))
				oid := strings.TrimPrefix(ref.OID, "sha256:")
				mu.Lock()
				data := append([]byte(nil), uploaded[oid]...)
				mu.Unlock()
				require.NotEmpty(t, data)
				gz, err := gzip.NewReader(bytes.NewReader(data))
				require.NoError(t, err)
				plain, err := io.ReadAll(gz)
				require.NoError(t, err)
				require.NoError(t, gz.Close())
				require.NotContains(t, string(plain), "PRIVATE_EMAIL")
				require.NotContains(t, string(plain), "LATER_RECORDING_PRIVATE")
			}
		})
	}
}
