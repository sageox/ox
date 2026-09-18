package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/pkg/sessionprovenance"
	"github.com/stretchr/testify/require"
)

func TestConcurrentIdenticalImportsConvergeBothHistories(t *testing.T) {
	for _, unrelated := range []bool{false, true} {
		t.Run(fmt.Sprintf("unrelated_%v", unrelated), func(t *testing.T) {
			bare, left := createBareAndClone(t)
			right := cloneBare(t, bare)
			ctx := context.Background()
			raw := []byte("{\"type\":\"user\",\"content\":\"complete native history\"}\n")
			ref := lfs.NewFileRef(raw)
			name := "2026-01-01T00-00-00-codex-import-identical"
			native := "019c6d2e-27b0-798d-aaed-b036114dc63a"
			candidate := &importCandidate{SessionName: name}
			candidate.NativeID = native
			candidate.Generation = strings.Repeat("a", 64)
			candidate.Digest = strings.Repeat("b", 64)
			candidate.Size = 100
			destination := importDestination{RepoID: "repo_test"}
			sourcePath, err := sessionprovenance.Path(native)
			require.NoError(t, err)
			write := func(repo, path string, value any) {
				data, err := json.Marshal(value)
				require.NoError(t, err)
				full := filepath.Join(repo, path)
				require.NoError(t, os.MkdirAll(filepath.Dir(full), 0700))
				require.NoError(t, os.WriteFile(full, data, 0600))
			}
			commitImport := func(repo string, publication time.Time) {
				source := &sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: native, Generation: candidate.Generation, SnapshotDigest: candidate.Digest, Ranges: []sessionprovenance.Range{{Start: 0, End: 100}}, ImportedAt: &publication}
				meta := &lfs.SessionMeta{RepoID: destination.RepoID, SessionName: name, Source: source, PublishedAt: &publication, Files: map[string]lfs.FileRef{"raw.jsonl": ref}}
				write(repo, "sessions/"+name+"/meta.json", meta)
				require.NoError(t, os.WriteFile(filepath.Join(repo, "sessions", name, "raw.jsonl"), []byte(lfs.FormatPointer(ref.OID, ref.Size)), 0600))
				write(repo, sourcePath, &sessionprovenance.Record{Version: 1, Agent: "codex", NativeSessionID: native, Generation: candidate.Generation, UpdatedAt: publication, Coverage: []sessionprovenance.Coverage{{Start: 0, End: 100, SessionName: name, RawOID: ref.BareOID()}}})
				runGit(t, repo, "add", ".")
				runGit(t, repo, "commit", "-m", "import fixture")
			}
			commitImport(left, time.Unix(100, 0).UTC())
			leftTip := runGit(t, left, "rev-parse", "HEAD")
			commitImport(right, time.Unix(200, 0).UTC())
			if unrelated {
				require.NoError(t, os.WriteFile(filepath.Join(right, "unrelated.txt"), []byte("retain"), 0600))
				runGit(t, right, "add", "unrelated.txt")
				runGit(t, right, "commit", "-m", "unrelated fixture")
			}
			rightTip := runGit(t, right, "rev-parse", "HEAD")
			runGit(t, left, "push")
			runGit(t, right, "fetch", "origin")
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/objects/batch") {
					_ = json.NewEncoder(w).Encode(lfs.BatchResponse{Objects: []lfs.BatchResponseObject{{OID: ref.BareOID(), Size: ref.Size, Actions: &lfs.Actions{Download: &lfs.Action{Href: server.URL + "/blob"}}}}})
					return
				}
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			client := lfs.NewClient(server.URL, "test", "test")
			verified, err := verifyImportReceipt(ctx, right, leftTip, destination, candidate, client)
			require.NoError(t, err)
			require.True(t, verified)
			var merged bool
			err = gitutil.WithRepoLock(ctx, right, func() error {
				var err error
				merged, err = convergeVerifiedImport(ctx, right, leftTip, destination, candidate, client, true)
				return err
			})
			if unrelated {
				require.ErrorContains(t, err, "unrelated")
				require.Equal(t, rightTip, runGit(t, right, "rev-parse", "HEAD"))
				require.FileExists(t, filepath.Join(right, "unrelated.txt"))
				return
			}
			require.NoError(t, err)
			require.True(t, merged)
			require.Equal(t, runGit(t, left, "rev-parse", "HEAD^{tree}"), runGit(t, right, "rev-parse", "HEAD^{tree}"))
			require.Equal(t, rightTip+" "+leftTip, runGit(t, right, "show", "-s", "--format=%P", "HEAD"))
			require.Empty(t, runGit(t, right, "status", "--porcelain"))
			runGit(t, right, "push")
			fresh := cloneBare(t, bare)
			require.NoError(t, gitutil.RefuseSourcePublicationRebase(ctx, fresh))
			require.NoError(t, gitutil.CheckSourcePublication(ctx, fresh))
			entries, err := os.ReadDir(filepath.Join(fresh, "sessions"))
			require.NoError(t, err)
			require.Len(t, entries, 1)
		})
	}
}
