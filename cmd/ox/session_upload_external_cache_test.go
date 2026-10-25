package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A missing LFS association must not turn an external recording into an empty
// publication that doctor calls successful and prunes from its source cache.
func TestSessionUpload_ExternalCacheSurvivesMissingLFSAndRetry(t *testing.T) {
	for _, mode := range []string{"stop", "doctor"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newSessionUploadFixture(t)
			bare, ledger := createBareAndClone(t)
			fixture.ledgerPath = ledger
			isolatePushEnv(t, ledger)
			for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
				t.Setenv(key, t.TempDir())
			}
			priorDir := gitserver.TestSetConfigDirOverride(t.TempDir())
			t.Cleanup(func() { gitserver.TestSetConfigDirOverride(priorDir) })
			priorStorage := gitserver.TestSetForceFileStorage(true)
			t.Cleanup(func() { gitserver.TestSetForceFileStorage(priorStorage) })
			var healthy atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Objects []lfs.BatchObject `json:"objects"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				response := lfs.BatchResponse{Transfer: "basic"}
				for _, obj := range request.Objects {
					result := lfs.BatchResponseObject{OID: obj.OID, Size: obj.Size}
					if healthy.Load() {
						result.Actions = &lfs.Actions{Download: &lfs.Action{Href: "http://127.0.0.1/blob"}}
					} else {
						result.Error = &lfs.ObjectError{Code: http.StatusNotFound, Message: "association lost after upload"}
					}
					response.Objects = append(response.Objects, result)
				}
				w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
				_ = json.NewEncoder(w).Encode(response)
			}))
			t.Cleanup(server.Close)
			t.Setenv("SAGEOX_ENDPOINT", server.URL)
			require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, gitserver.GitCredentials{
				Username: "testuser", Token: "test-token", ServerURL: server.URL, ExpiresAt: time.Now().Add(time.Hour),
			}))
			runGit(t, ledger, "remote", "set-url", "origin", server.URL+"/ledger.git")
			runGit(t, ledger, "remote", "set-url", "--push", "origin", bare)
			hook := "#!/bin/sh\nif test -f hooks/lfs-objects-present; then exit 0; fi\n" +
				"while read oldrev newrev refname; do\n" +
				"  if git grep -q -F 'version https://git-lfs' \"$newrev\" -- sessions/; then\n" +
				"    echo 'GitLab: LFS objects are missing' >&2\n    exit 1\n  fi\ndone\n"
			require.NoError(t, os.WriteFile(filepath.Join(bare, "hooks", "pre-receive"), []byte(hook), 0o755))
			var calls []string
			effects := scriptedSessionUploadEffects(&calls, fixture.refs, "")
			effects.commitInitial = commitAndPushLedger
			effects.commitRetry = commitAndPushLedgerWithExtras
			publish := func() error {
				if mode == "doctor" {
					return retrySessionUploadWithEffects(fixture.projectRoot, ledger, fixture.orphan(), effects)
				}
				return uploadSessionToLedgerWithEffects(fixture.projectRoot, fixture.result, fixture.state, ledger, fixture.sessionName, effects)
			}

			require.Error(t, publish(), "missing objects must never become successful empty publication")
			assert.Empty(t, runGit(t, bare, "ls-tree", "HEAD", "sessions/"+fixture.sessionName), "no empty replacement may reach the remote")
			pendingHead := runGit(t, ledger, "rev-parse", "HEAD")
			store, err := session.NewStore(ledger)
			require.NoError(t, err)
			recoveryDir := store.CacheSessionPath(fixture.sessionName)
			require.NotEqual(t, fixture.state.SessionPath, recoveryDir)
			for _, dir := range []string{fixture.state.SessionPath, recoveryDir} {
				rawPath := filepath.Join(dir, ledgerFileRaw)
				content, err := os.ReadFile(rawPath)
				require.NoError(t, err)
				assert.Equal(t, fixture.rawContent, content, "the actual source and recovery copy must survive")
				info, err := os.Stat(rawPath)
				require.NoError(t, err)
				if runtime.GOOS != "windows" {
					assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
				}
			}
			meta, err := lfs.ReadSessionMeta(recoveryDir)
			require.NoError(t, err, "the recovery copy must carry session identity and upload metadata")
			assert.Equal(t, fixture.state.SessionID, meta.SessionID)
			orphans, err := findOrphanedSessionsInDir(filepath.Dir(fixture.state.SessionPath), ledger)
			require.NoError(t, err)
			require.Len(t, orphans, 1, "doctor must still discover the external source after failure")

			healthy.Store(true)
			require.NoError(t, os.WriteFile(filepath.Join(bare, "hooks", "lfs-objects-present"), nil, 0o600))
			require.NoError(t, publish())
			assert.Equal(t, pendingHead, runGit(t, bare, "rev-parse", "HEAD"), "retry must publish the retained pointer commit")
			remoteRaw := runGit(t, bare, "show", "HEAD:sessions/"+fixture.sessionName+"/raw.jsonl")
			oid, size, err := lfs.ParsePointer(remoteRaw)
			require.NoError(t, err)
			assert.Equal(t, fixture.refs[ledgerFileRaw].OID, oid)
			assert.Equal(t, fixture.refs[ledgerFileRaw].Size, size)
			assert.NoDirExists(t, recoveryDir, "only the extra recovery copy is pruned after publication")
			require.FileExists(t, fixture.result.RawPath, "stop must retain the original; doctor's caller owns its pruning")
			if mode == "stop" {
				assert.NoFileExists(t, filepath.Join(fixture.state.SessionPath, sessionUploadRetryPendingFile))
			}
		})
	}
}

func TestSessionStop_RecoveryCacheFailureRemainsRetryable(t *testing.T) {
	fixture := newSessionUploadFixture(t)
	store, err := session.NewStore(fixture.ledgerPath)
	require.NoError(t, err)
	recoveryDir := store.CacheSessionPath(fixture.sessionName)
	blockedRawPath := filepath.Join(recoveryDir, ledgerFileRaw)
	require.NoError(t, os.MkdirAll(blockedRawPath, 0o700))
	var calls []string
	effects := scriptedSessionUploadEffects(&calls, fixture.refs, "")
	publish := func() error {
		return uploadSessionToLedgerWithEffects(fixture.projectRoot, fixture.result, fixture.state,
			fixture.ledgerPath, fixture.sessionName, effects)
	}

	require.ErrorContains(t, publish(), "preserve session artifact raw.jsonl")
	assert.Equal(t, []string{"upload_lfs"}, calls, "failed preservation must stop before pointer publication")
	assertSessionBytesPreserved(t, fixture)
	assert.False(t, lfs.IsPointerFile(filepath.Join(fixture.ledgerPath, "sessions", fixture.sessionName, ledgerFileRaw)))
	orphans, err := findOrphanedSessionsInDir(filepath.Dir(fixture.state.SessionPath), fixture.ledgerPath)
	require.NoError(t, err)
	require.Len(t, orphans, 1, "a failed recovery copy must remain discoverable despite finalized ledger metadata")
	assert.Equal(t, fixture.state.SessionPath, orphans[0].CachePath)
	require.FileExists(t, filepath.Join(fixture.state.SessionPath, sessionUploadRetryPendingFile))

	require.NoError(t, os.Remove(blockedRawPath))
	require.NoError(t, publish())
	assertSessionBytesPreserved(t, fixture)
	assert.NoDirExists(t, recoveryDir)
	assert.NoFileExists(t, filepath.Join(fixture.state.SessionPath, sessionUploadRetryPendingFile))
}

// Canonical caches, including symlink aliases, are the original recording and
// must remain readable for the subsequent inline push-summary step.
func TestSessionStop_RetainsOriginalCanonicalCache(t *testing.T) {
	for _, alias := range []bool{false, true} {
		name := "direct"
		if alias {
			name = "symlink"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newSessionUploadFixture(t)
			store, err := session.NewStore(fixture.ledgerPath)
			require.NoError(t, err)
			canonical := store.CacheSessionPath(fixture.sessionName)
			require.NoError(t, os.MkdirAll(filepath.Dir(canonical), 0o700))
			require.NoError(t, os.Rename(fixture.state.SessionPath, canonical))
			source := canonical
			if alias {
				source = filepath.Join(t.TempDir(), fixture.sessionName)
				if err := os.Symlink(canonical, source); err != nil {
					t.Skipf("symlink aliases unsupported: %v", err)
				}
			}
			fixture.state.SessionPath = source
			fixture.result.RawPath = filepath.Join(source, ledgerFileRaw)
			var calls []string
			effects := scriptedSessionUploadEffects(&calls, fixture.refs, "")
			require.NoError(t, uploadSessionToLedgerWithEffects(fixture.projectRoot, fixture.result, fixture.state,
				fixture.ledgerPath, fixture.sessionName, effects))
			content, err := os.ReadFile(fixture.result.RawPath)
			require.NoError(t, err)
			assert.Equal(t, fixture.rawContent, content)
			assert.Equal(t, filepath.Join(source, ledgerFileRaw), fixture.result.RawPath)
			assert.NoFileExists(t, filepath.Join(source, sessionUploadRetryPendingFile))
		})
	}
}
