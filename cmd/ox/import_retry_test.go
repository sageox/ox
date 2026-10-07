package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/paths"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- import retry after a failed import ---

const importRetryTeamID = "team_import_retry"

// importRetryFixture is a real team context clone whose LFS server can be made to fail.
type importRetryFixture struct {
	endpoint    string // also the LFS server
	bare        string // team context remote
	docsDir     string
	src         string
	text        string // --text path, empty for none
	batchFails  atomic.Bool
	uploadFails atomic.Bool
	uploads     atomic.Int32           // successful object uploads
	onUpload    atomic.Pointer[func()] // runs inside an object upload, e.g. to simulate a concurrent import
}

// newImportRetryFixture builds the fixture: a team context clone, an LFS test server and a source document.
func newImportRetryFixture(t *testing.T) *importRetryFixture {
	t.Helper()
	// import reads push settings; a developer's push.default or pushInsteadOf must not change outcomes
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	bare, clone := createBareAndClone(t)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(key, t.TempDir())
	}
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("SAGEOX_TOKEN", "")
	priorDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	t.Cleanup(func() { gitserver.TestSetConfigDirOverride(priorDir) })
	priorStorage := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() { gitserver.TestSetForceFileStorage(priorStorage) })

	f := &importRetryFixture{bare: bare}
	server := httptest.NewServer(http.HandlerFunc(f.serveLFS))
	t.Cleanup(server.Close)
	f.endpoint = server.URL
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	f.saveCredentials(t)

	// runImport finds the team by ID under the endpoint's teams dir; fetch URL is the LFS server, pushes go to bare
	tcPath := filepath.Join(paths.TeamsDataDir(server.URL), importRetryTeamID)
	require.NoError(t, os.MkdirAll(filepath.Dir(tcPath), 0o755))
	require.NoError(t, os.Rename(clone, tcPath))
	runGit(t, tcPath, "remote", "set-url", "origin", server.URL+"/team.git")
	runGit(t, tcPath, "remote", "set-url", "--push", "origin", bare)
	f.docsDir = filepath.Join(tcPath, "data", "docs")

	work := t.TempDir()
	t.Chdir(work)
	f.src = filepath.Join(work, "q3-plan.md")
	require.NoError(t, os.WriteFile(f.src, []byte("# Q3 plan\n"), 0o644))

	orig := importFlags
	t.Cleanup(func() { importFlags = orig })
	return f
}

// serveLFS answers the LFS batch API and object uploads, failing whichever step is switched on.
func (f *importRetryFixture) serveLFS(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/teams/"+importRetryTeamID:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id": importRetryTeamID, "repo_url": "http://" + r.Host + "/team.git"})
	case r.Method == http.MethodPost && r.URL.Path == "/team.git/info/lfs/objects/batch":
		if f.batchFails.Load() {
			http.Error(w, "storage unavailable", http.StatusInternalServerError)
			return
		}
		var req struct {
			Objects []lfs.BatchObject `json:"objects"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp := lfs.BatchResponse{Transfer: "basic"}
		for _, obj := range req.Objects {
			resp.Objects = append(resp.Objects, lfs.BatchResponseObject{
				OID:     obj.OID,
				Size:    obj.Size,
				Actions: &lfs.Actions{Upload: &lfs.Action{Href: "http://" + r.Host + "/objects/" + obj.OID}},
			})
		}
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		_ = json.NewEncoder(w).Encode(resp)
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/objects/"):
		if f.uploadFails.Load() {
			http.Error(w, "storage unavailable", http.StatusInternalServerError)
			return
		}
		if hook := f.onUpload.Load(); hook != nil {
			(*hook)()
		}
		f.uploads.Add(1)
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

// saveCredentials stores git credentials for the fixture endpoint so the LFS client can be created.
func (f *importRetryFixture) saveCredentials(t *testing.T) {
	t.Helper()
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(f.endpoint, gitserver.GitCredentials{
		Username: "oauth2", Token: "test-token", ServerURL: f.endpoint, ExpiresAt: time.Now().Add(time.Hour),
	}))
}

// useFileGitRemote serves the LFS URL through the local API while all Git transports use the bare fixture.
func (f *importRetryFixture) useFileGitRemote(t *testing.T) string {
	t.Helper()
	tcPath := filepath.Join(paths.TeamsDataDir(f.endpoint), importRetryTeamID)
	runGit(t, tcPath, "remote", "set-url", "origin", f.bare)
	runGit(t, tcPath, "config", "--local", "--unset-all", "remote.origin.pushurl")
	require.NoError(t, auth.SaveTokenForEndpoint(f.endpoint, &auth.StoredToken{
		AccessToken: "test-bearer", ExpiresAt: time.Now().Add(24 * time.Hour),
	}))
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(f.endpoint, gitserver.GitCredentials{
		Username: "oauth2", Token: "test-token", ServerURL: f.endpoint, ExpiresAt: time.Now().Add(24 * time.Hour),
		BearerTokenHash: gitserver.BearerTokenFingerprint("test-bearer"),
	}))
	return tcPath
}

// importDoc runs `ox import <src> --team <team> --date 2026-09-19`, with --text and --force as set.
func (f *importRetryFixture) importDoc(force bool) (string, error) {
	importFlags = importFlagsT{team: importRetryTeamID, date: "2026-09-19", text: f.text, force: force}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	err := runImport(cmd, []string{f.src})
	return out.String(), err
}

// docDir is the document directory the fixture's import resolves to.
func (f *importRetryFixture) docDir() string {
	return filepath.Join(f.docsDir, "2026", "09", "19", "q3-plan")
}

// importFailures are the ways an import fails before its content is uploaded.
var importFailures = []struct {
	name    string
	wantErr string
	inject  func(t *testing.T, f *importRetryFixture)
	repair  func(t *testing.T, f *importRetryFixture)
}{
	{
		name:    "text file missing",
		wantErr: "--text file not found",
		inject: func(t *testing.T, f *importRetryFixture) {
			f.text = filepath.Join(filepath.Dir(f.src), "extracted.md")
		},
		repair: func(t *testing.T, f *importRetryFixture) {
			require.NoError(t, os.WriteFile(f.text, []byte("extracted text\n"), 0o644))
		},
	},
	{
		name:    "no git credentials",
		wantErr: "no git credentials found",
		inject: func(t *testing.T, f *importRetryFixture) {
			require.NoError(t, gitserver.RemoveCredentialsForEndpoint(f.endpoint))
		},
		repair: func(t *testing.T, f *importRetryFixture) { f.saveCredentials(t) },
	},
	{
		name:    "batch request fails",
		wantErr: "LFS batch upload",
		inject:  func(t *testing.T, f *importRetryFixture) { f.batchFails.Store(true) },
		repair:  func(t *testing.T, f *importRetryFixture) { f.batchFails.Store(false) },
	},
	{
		name:    "object upload fails",
		wantErr: "LFS upload failed",
		inject:  func(t *testing.T, f *importRetryFixture) { f.uploadFails.Store(true) },
		repair:  func(t *testing.T, f *importRetryFixture) { f.uploadFails.Store(false) },
	},
	{
		name:    "source is an LFS pointer",
		wantErr: lfs.ErrPointerContent.Error(),
		inject: func(t *testing.T, f *importRetryFixture) {
			content, err := os.ReadFile(f.src)
			require.NoError(t, err)
			ref := lfs.NewFileRef(content)
			require.NoError(t, os.WriteFile(f.src, []byte(lfs.FormatPointer(ref.OID, ref.Size)), 0o644))
		},
		repair: func(t *testing.T, f *importRetryFixture) {
			require.NoError(t, os.WriteFile(f.src, []byte("# Q3 plan\n"), 0o644))
		},
	},
}

// TestImport_FailedImportDoesNotBlockRetry checks a retry after each pre-upload failure succeeds without --force.
// Without this, a failed import leaves an empty document directory and its retry is refused as already imported.
func TestImport_FailedImportDoesNotBlockRetry(t *testing.T) {
	for _, tc := range importFailures {
		t.Run(tc.name, func(t *testing.T) {
			f := newImportRetryFixture(t)
			tc.inject(t, f)
			_, err := f.importDoc(false)
			require.ErrorContains(t, err, tc.wantErr)
			assert.NoDirExists(t, f.docDir(), "a failed import must leave no document directory behind")

			tc.repair(t, f)
			out, err := f.importDoc(false)
			require.NoError(t, err, "the retry must succeed without --force")
			assert.Contains(t, out, "Imported:")
			assert.FileExists(t, filepath.Join(f.docDir(), "metadata.json"))
			assert.Equal(t, "import: doc q3-plan", runGit(t, f.bare, "log", "-1", "--format=%s"), "the retry must reach the remote")
		})
	}
}

// TestImport_CommitFailureCanBeRetried verifies a retry publishes the document after a failed commit.
// Without this, the leftover metadata makes the retry report success while the remote has no document.
func TestImport_CommitFailureCanBeRetried(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	f := newImportRetryFixture(t)
	tcPath := f.useFileGitRemote(t)
	remoteBefore := runGit(t, f.bare, "rev-parse", "HEAD")
	runGit(t, tcPath, "config", "--local", "user.name", "")
	// Empty names also override any identity inherited from the test runner.
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_COMMITTER_NAME", "")
	t.Setenv("LC_ALL", "C")

	_, err := f.importDoc(false)
	require.ErrorContains(t, err, "commit")
	require.ErrorContains(t, err, "empty ident name")
	t.Logf("first import reached the commit failure: %v", err)
	assert.Equal(t, remoteBefore, runGit(t, f.bare, "rev-parse", "HEAD"))
	assert.FileExists(t, filepath.Join(f.docDir(), "metadata.json"))
	assert.FileExists(t, filepath.Join(f.docDir(), "q3-plan.md"))

	runGit(t, tcPath, "config", "--local", "user.name", "Test")
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	out, err := f.importDoc(false)
	require.NoError(t, err, "the retry must publish the document without --force")
	assert.Contains(t, out, "Imported:")

	const remoteDoc = "data/docs/2026/09/19/q3-plan"
	const sourceOID = "sha256:c322135151cf0bc395a4d1e2a3e560e6cb0a62057e70e749c4f18cdbb81f32d7"
	metadata := runGit(t, f.bare, "show", "HEAD:"+remoteDoc+"/metadata.json")
	var meta docMeta
	require.NoError(t, json.Unmarshal([]byte(metadata), &meta))
	assert.Equal(t, sourceOID, meta.SourceOID)
	assert.Equal(t, int64(10), meta.SourceSize)
	assert.Equal(t, "q3-plan.md", meta.SourceFilename)
	assert.Equal(t, remoteDoc, meta.Path)
	pointer := runGit(t, f.bare, "show", "HEAD:"+remoteDoc+"/q3-plan.md")
	assert.Equal(t, "version https://git-lfs.github.com/spec/v1\noid "+sourceOID+"\nsize 10", pointer)
}

// TestImport_PushFailureCanBeRetriedWithoutAnotherCommit verifies a retry publishes the original import commit.
// Without this, metadata from an unpushed commit makes the retry silently leave the document unpublished.
func TestImport_PushFailureCanBeRetriedWithoutAnotherCommit(t *testing.T) {
	f := newImportRetryFixture(t)
	tcPath := filepath.Join(paths.TeamsDataDir(f.endpoint), importRetryTeamID)
	remoteBefore := runGit(t, f.bare, "rev-parse", "HEAD")
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Permission denied", http.StatusForbidden)
	}))
	t.Cleanup(denied.Close)
	runGit(t, tcPath, "remote", "set-url", "--push", "origin", denied.URL+"/team.git")

	_, err := f.importDoc(false)
	require.ErrorContains(t, err, "git push failed")
	require.ErrorContains(t, err, "403")
	t.Logf("first import reached the push permission failure: %v", err)
	committed := runGit(t, tcPath, "rev-parse", "HEAD")
	require.NotEqual(t, remoteBefore, committed, "the import must have committed before its push failed")
	assert.Equal(t, remoteBefore, runGit(t, f.bare, "rev-parse", "HEAD"))

	runGit(t, tcPath, "remote", "set-url", "origin", denied.URL+"/team.git")
	_, err = f.importDoc(false)
	require.ErrorContains(t, err, "could not confirm publication", "a retry that still cannot push must say what is left to do")
	assert.Equal(t, committed, runGit(t, tcPath, "rev-parse", "HEAD"))

	runGit(t, tcPath, "remote", "set-url", "--push", "origin", f.bare)
	runGit(t, tcPath, "remote", "set-url", "origin", f.bare)
	out, err := f.importDoc(false)
	require.NoError(t, err, "the retry must publish the original commit without --force")
	assert.Contains(t, out, "Imported:")
	assert.Equal(t, committed, runGit(t, tcPath, "rev-parse", "HEAD"), "retrying a push must not create another commit")
	assert.Equal(t, committed, runGit(t, f.bare, "rev-parse", "HEAD"))

	const remoteDoc = "data/docs/2026/09/19/q3-plan"
	const sourceOID = "sha256:c322135151cf0bc395a4d1e2a3e560e6cb0a62057e70e749c4f18cdbb81f32d7"
	metadata := runGit(t, f.bare, "show", "HEAD:"+remoteDoc+"/metadata.json")
	var meta docMeta
	require.NoError(t, json.Unmarshal([]byte(metadata), &meta))
	assert.Equal(t, sourceOID, meta.SourceOID)
	pointer := runGit(t, f.bare, "show", "HEAD:"+remoteDoc+"/q3-plan.md")
	assert.Equal(t, "version https://git-lfs.github.com/spec/v1\noid "+sourceOID+"\nsize 10", pointer)
}

// TestImport_RetryDoesNotPublishPrivateCommits keeps unrelated committed history local during recovery.
// Without this, a retry publishes private commits added after the import's first push failed.
func TestImport_RetryDoesNotPublishPrivateCommits(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	f := newImportRetryFixture(t)
	tcPath := f.useFileGitRemote(t)
	runGit(t, tcPath, "branch", "-M", "main")
	runGit(t, tcPath, "push", "--set-upstream", "origin", "main")
	runGit(t, f.bare, "symbolic-ref", "HEAD", "refs/heads/main")
	initial := runGit(t, f.bare, "rev-parse", "HEAD")
	hook := filepath.Join(f.bare, "hooks", "pre-receive")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\necho 'Permission denied' >&2\nexit 1\n"), 0o755))
	_, err := f.importDoc(false)
	require.ErrorContains(t, err, "git push failed")
	require.ErrorContains(t, err, "Permission denied")
	pendingImport := runGit(t, tcPath, "rev-parse", "HEAD")
	require.NotEqual(t, initial, pendingImport)
	require.Equal(t, "1", runGit(t, tcPath, "rev-list", "--count", initial+"..HEAD"))
	require.Equal(t, initial, runGit(t, f.bare, "rev-parse", "HEAD"))
	t.Logf("real bare hook rejected the saved import commit %s: %v", pendingImport, err)

	private := filepath.Join(tcPath, "private.md")
	require.NoError(t, os.WriteFile(private, []byte("synthetic private notes\n"), 0o644))
	runGit(t, tcPath, "add", "private.md")
	runGit(t, tcPath, "commit", "--no-verify", "-m", "private notes")
	privateHead := runGit(t, tcPath, "rev-parse", "HEAD")
	require.NoError(t, os.Remove(hook))
	pending := filepath.Join(tcPath, "pending.md")
	require.NoError(t, os.WriteFile(pending, []byte("staged personal notes\n"), 0o644))
	runGit(t, tcPath, "add", "pending.md")
	require.NoError(t, os.WriteFile(pending, []byte("working personal notes\n"), 0o644))
	docBefore := readDocFiles(t, f.docDir())
	indexBefore := runGit(t, tcPath, "ls-files", "--stage")
	indexBytes, err := os.ReadFile(filepath.Join(tcPath, ".git", "index"))
	require.NoError(t, err)
	sourceBefore, err := os.ReadFile(f.src)
	require.NoError(t, err)

	out, retryErr := f.importDoc(false)
	remoteHead := runGit(t, f.bare, "rev-parse", "HEAD")
	remotePrivate := runGit(t, f.bare, "ls-tree", "HEAD", "--", "private.md")
	const remoteDoc = "data/docs/2026/09/19/q3-plan"
	remoteFiles := runGit(t, f.bare, "ls-tree", "-r", "HEAD", "--", remoteDoc)
	t.Logf("retry err=%v output=%q localHEAD=%s remoteHEAD=%s private=%q document=%q", retryErr, out, privateHead, remoteHead, remotePrivate, remoteFiles)
	if remotePrivate != "" {
		t.Logf("remote private bytes: %q", runGit(t, f.bare, "show", "HEAD:private.md"))
	}
	if remoteFiles != "" {
		t.Logf("remote metadata: %s", runGit(t, f.bare, "show", "HEAD:"+remoteDoc+"/metadata.json"))
		t.Logf("remote pointer: %s", runGit(t, f.bare, "show", "HEAD:"+remoteDoc+"/q3-plan.md"))
	}
	assert.ErrorContains(t, retryErr, "outgoing history includes other commits or a merge",
		"recovery must refuse to publish unrelated private commits")
	assert.NotContains(t, out, "Imported:")
	assert.NotContains(t, out, "Already imported")
	assert.Equal(t, initial, remoteHead)
	assert.Empty(t, remotePrivate)
	assert.Empty(t, remoteFiles)
	assert.Equal(t, privateHead, runGit(t, tcPath, "rev-parse", "HEAD"))
	assert.Equal(t, initial, runGit(t, tcPath, "rev-parse", "refs/remotes/origin/main"))
	assert.Equal(t, docBefore, readDocFiles(t, f.docDir()))
	assert.Equal(t, indexBefore, runGit(t, tcPath, "ls-files", "--stage"))
	indexAfter, err := os.ReadFile(filepath.Join(tcPath, ".git", "index"))
	require.NoError(t, err)
	assert.Equal(t, indexBytes, indexAfter)
	for path, expected := range map[string][]byte{
		private: []byte("synthetic private notes\n"),
		pending: []byte("working personal notes\n"),
		f.src:   sourceBefore,
	} {
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, expected, content, path)
	}
}

// TestImport_UncommittedRetryRejectsPrivateHistoryBeforeWriting preserves a failed commit's saved files.
// Without this, recovery creates another commit before refusing unrelated unpublished history.
func TestImport_UncommittedRetryRejectsPrivateHistoryBeforeWriting(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	f := newImportRetryFixture(t)
	tcPath := f.useFileGitRemote(t)
	runGit(t, tcPath, "branch", "-M", "main")
	runGit(t, tcPath, "push", "--set-upstream", "origin", "main")
	runGit(t, f.bare, "symbolic-ref", "HEAD", "refs/heads/main")
	initial := runGit(t, f.bare, "rev-parse", "HEAD")
	runGit(t, tcPath, "config", "--local", "user.name", "")
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_COMMITTER_NAME", "")
	_, err := f.importDoc(false)
	require.ErrorContains(t, err, "empty ident name")
	require.Equal(t, initial, runGit(t, tcPath, "rev-parse", "HEAD"))
	require.Contains(t, readDocFiles(t, f.docDir()), "metadata.json")

	runGit(t, tcPath, "config", "--local", "user.name", "Test")
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	private := filepath.Join(tcPath, "private.md")
	require.NoError(t, os.WriteFile(private, []byte("synthetic private notes\n"), 0o644))
	runGit(t, tcPath, "add", "private.md")
	// The user's private commit leaves the failed import's staged files untouched.
	runGit(t, tcPath, "commit", "--no-verify", "-m", "private notes", "--", ":(literal)private.md")
	privateHead := runGit(t, tcPath, "rev-parse", "HEAD")
	require.Equal(t, "1", runGit(t, tcPath, "rev-list", "--count", initial+"..HEAD"))
	require.Empty(t, runGit(t, tcPath, "ls-tree", "-r", "HEAD", "--", "data/docs"))
	docBefore := readDocFiles(t, f.docDir())
	indexBefore, err := os.ReadFile(filepath.Join(tcPath, ".git", "index"))
	require.NoError(t, err)
	stagedBefore := runGit(t, tcPath, "ls-files", "--stage")

	out, err := f.importDoc(false)
	assert.ErrorContains(t, err, "outgoing history includes other commits or a merge")
	assert.NotContains(t, out, "Imported:")
	assert.NotContains(t, out, "Already imported")
	assert.Equal(t, privateHead, runGit(t, tcPath, "rev-parse", "HEAD"), "refusing private history must not create an import commit")
	assert.Equal(t, initial, runGit(t, f.bare, "rev-parse", "HEAD"))
	assert.Equal(t, initial, runGit(t, tcPath, "rev-parse", "refs/remotes/origin/main"))
	assert.Empty(t, runGit(t, f.bare, "ls-tree", "HEAD", "--", "private.md"))
	assert.Equal(t, docBefore, readDocFiles(t, f.docDir()))
	assert.Equal(t, stagedBefore, runGit(t, tcPath, "ls-files", "--stage"))
	indexAfter, err := os.ReadFile(filepath.Join(tcPath, ".git", "index"))
	require.NoError(t, err)
	assert.Equal(t, indexBefore, indexAfter)
	content, err := os.ReadFile(private)
	require.NoError(t, err)
	assert.Equal(t, "synthetic private notes\n", string(content))
}

// TestImport_RetryRejectsOtherChangesInImportCommit refuses unrelated content even within a single commit.
// Without this, amending the pending import bypasses a check that only counts outgoing commits.
func TestImport_RetryRejectsOtherChangesInImportCommit(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	for _, name := range []string{"private file", "unrelated attributes", "private filename with leading space"} {
		t.Run(name, func(t *testing.T) {
			f := newImportRetryFixture(t)
			tcPath := f.useFileGitRemote(t)
			runGit(t, tcPath, "branch", "-M", "main")
			runGit(t, tcPath, "push", "--set-upstream", "origin", "main")
			runGit(t, f.bare, "symbolic-ref", "HEAD", "refs/heads/main")
			initial := runGit(t, f.bare, "rev-parse", "HEAD")
			hook := filepath.Join(f.bare, "hooks", "pre-receive")
			require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\necho 'Permission denied' >&2\nexit 1\n"), 0o755))
			_, err := f.importDoc(false)
			require.ErrorContains(t, err, "Permission denied")
			require.Equal(t, initial, runGit(t, f.bare, "rev-parse", "HEAD"))

			changed := filepath.Join(tcPath, "private.md")
			content := []byte("synthetic private notes\n")
			if name == "private filename with leading space" {
				changed = filepath.Join(tcPath, " .gitattributes")
			}
			if name == "unrelated attributes" {
				changed = filepath.Join(tcPath, ".gitattributes")
				attrs, err := os.ReadFile(changed)
				require.NoError(t, err)
				content = append(attrs, []byte("private.md -diff\n")...)
			}
			require.NoError(t, os.WriteFile(changed, content, 0o644))
			runGit(t, tcPath, "add", "--", filepath.Base(changed))
			runGit(t, tcPath, "commit", "--amend", "--no-verify", "--no-edit")
			amended := runGit(t, tcPath, "rev-parse", "HEAD")
			require.Equal(t, "1", runGit(t, tcPath, "rev-list", "--count", initial+"..HEAD"))
			require.NoError(t, os.Remove(hook))
			docBefore := readDocFiles(t, f.docDir())
			indexBefore, err := os.ReadFile(filepath.Join(tcPath, ".git", "index"))
			require.NoError(t, err)

			out, err := f.importDoc(false)
			t.Logf("amended %s retry err=%v output=%q remoteHEAD=%s", name, err, out, runGit(t, f.bare, "rev-parse", "HEAD"))
			if name == "private filename with leading space" && runGit(t, f.bare, "ls-tree", "HEAD", "--", ":(literal) .gitattributes") != "" {
				t.Logf("remote private filename bytes: %q", runGit(t, f.bare, "show", "HEAD: .gitattributes"))
			}
			if name == "unrelated attributes" {
				assert.ErrorContains(t, err, "unrelated .gitattributes changes")
			} else {
				assert.ErrorContains(t, err, "commit includes changes outside this document")
			}
			assert.NotContains(t, out, "Imported:")
			assert.Equal(t, initial, runGit(t, f.bare, "rev-parse", "HEAD"))
			assert.Empty(t, runGit(t, f.bare, "ls-tree", "HEAD", "--", "private.md"))
			assert.Equal(t, amended, runGit(t, tcPath, "rev-parse", "HEAD"))
			assert.Equal(t, docBefore, readDocFiles(t, f.docDir()))
			indexAfter, err := os.ReadFile(filepath.Join(tcPath, ".git", "index"))
			require.NoError(t, err)
			assert.Equal(t, indexBefore, indexAfter)
			after, err := os.ReadFile(changed)
			require.NoError(t, err)
			assert.Equal(t, content, after)
		})
	}
}

// TestImport_RetryDoesNotRestoreRemovedHistory preserves upstream history removed before a pending import.
// A fork point can include removed commits which are still ancestors of the local import.
func TestImport_RetryDoesNotRestoreRemovedHistory(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	for _, failure := range []string{"push", "commit"} {
		t.Run(failure, func(t *testing.T) {
			f := newImportRetryFixture(t)
			tcPath := f.useFileGitRemote(t)
			runGit(t, tcPath, "branch", "-M", "main")
			runGit(t, tcPath, "push", "--set-upstream", "origin", "main")
			runGit(t, f.bare, "symbolic-ref", "HEAD", "refs/heads/main")
			initial := runGit(t, f.bare, "rev-parse", "HEAD")
			removed := filepath.Join(tcPath, "removed.md")
			require.NoError(t, os.WriteFile(removed, []byte("removed upstream content\n"), 0o644))
			runGit(t, tcPath, "add", "removed.md")
			runGit(t, tcPath, "commit", "--no-verify", "-m", "old upstream content")
			runGit(t, tcPath, "push", "origin", "main")
			oldUpstream := runGit(t, f.bare, "rev-parse", "HEAD")

			hook := filepath.Join(f.bare, "hooks", "pre-receive")
			if failure == "push" {
				require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\necho 'Permission denied' >&2\nexit 1\n"), 0o755))
			} else {
				runGit(t, tcPath, "config", "--local", "user.name", "")
				t.Setenv("GIT_AUTHOR_NAME", "")
				t.Setenv("GIT_COMMITTER_NAME", "")
			}
			_, err := f.importDoc(false)
			if failure == "push" {
				require.ErrorContains(t, err, "Permission denied")
				require.NoError(t, os.Remove(hook))
			} else {
				require.ErrorContains(t, err, "empty ident name")
				runGit(t, tcPath, "config", "--local", "user.name", "Test")
				t.Setenv("GIT_AUTHOR_NAME", "Test")
				t.Setenv("GIT_COMMITTER_NAME", "Test")
			}
			pending := runGit(t, tcPath, "rev-parse", "HEAD")
			other := cloneBare(t, f.bare)
			runGit(t, other, "reset", "--hard", initial)
			runGit(t, other, "push", "--force", "origin", "main")
			runGit(t, tcPath, "fetch", "origin")
			require.Equal(t, initial, runGit(t, tcPath, "rev-parse", "refs/remotes/origin/main"))
			require.Equal(t, oldUpstream, runGit(t, tcPath, "merge-base", "--fork-point", "refs/remotes/origin/main", "HEAD"))
			docBefore := readDocFiles(t, f.docDir())
			indexBefore, err := os.ReadFile(filepath.Join(tcPath, ".git", "index"))
			require.NoError(t, err)

			out, err := f.importDoc(false)
			t.Logf("%s failure retry err=%v output=%q remoteHEAD=%s", failure, err, out, runGit(t, f.bare, "rev-parse", "HEAD"))
			assert.ErrorContains(t, err, "outgoing history includes other commits or a merge")
			assert.NotContains(t, out, "Imported:")
			assert.Equal(t, initial, runGit(t, f.bare, "rev-parse", "HEAD"))
			assert.Empty(t, runGit(t, f.bare, "ls-tree", "-r", "HEAD", "--", "removed.md", "data/docs"))
			assert.Equal(t, pending, runGit(t, tcPath, "rev-parse", "HEAD"))
			assert.Equal(t, initial, runGit(t, tcPath, "rev-parse", "refs/remotes/origin/main"))
			assert.Equal(t, docBefore, readDocFiles(t, f.docDir()))
			indexAfter, err := os.ReadFile(filepath.Join(tcPath, ".git", "index"))
			require.NoError(t, err)
			assert.Equal(t, indexBefore, indexAfter)
			content, err := os.ReadFile(removed)
			require.NoError(t, err)
			assert.Equal(t, "removed upstream content\n", string(content))
		})
	}
}

// TestImport_RetryRejectsDeletedPrivateHistoryAndMerges checks history rather than only the final tree.
func TestImport_RetryRejectsDeletedPrivateHistoryAndMerges(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	for _, name := range []string{"deleted private file", "merge commit"} {
		t.Run(name, func(t *testing.T) {
			f := newImportRetryFixture(t)
			tcPath := f.useFileGitRemote(t)
			runGit(t, tcPath, "branch", "-M", "main")
			runGit(t, tcPath, "push", "--set-upstream", "origin", "main")
			runGit(t, f.bare, "symbolic-ref", "HEAD", "refs/heads/main")
			ancestor := runGit(t, tcPath, "rev-parse", "HEAD")
			require.NoError(t, os.WriteFile(filepath.Join(tcPath, "upstream.md"), []byte("upstream content\n"), 0o644))
			runGit(t, tcPath, "add", "upstream.md")
			runGit(t, tcPath, "commit", "--no-verify", "-m", "upstream content")
			runGit(t, tcPath, "push", "origin", "main")
			initial := runGit(t, f.bare, "rev-parse", "HEAD")
			hook := filepath.Join(f.bare, "hooks", "pre-receive")
			require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\necho 'Permission denied' >&2\nexit 1\n"), 0o755))
			_, err := f.importDoc(false)
			require.ErrorContains(t, err, "Permission denied")
			if name == "deleted private file" {
				require.NoError(t, os.WriteFile(filepath.Join(tcPath, "private.md"), []byte("synthetic private notes\n"), 0o644))
				runGit(t, tcPath, "add", "private.md")
				runGit(t, tcPath, "commit", "--no-verify", "-m", "private notes")
				runGit(t, tcPath, "rm", "private.md")
				runGit(t, tcPath, "commit", "--no-verify", "-m", "remove private notes")
				require.Empty(t, runGit(t, tcPath, "ls-tree", "HEAD", "--", "private.md"))
			} else {
				// Both parents were published: this isolates one outgoing merge with an import-only tree.
				tree := runGit(t, tcPath, "rev-parse", "HEAD^{tree}")
				merge := runGit(t, tcPath, "commit-tree", tree, "-p", initial, "-p", ancestor, "-m", "merge saved import")
				runGit(t, tcPath, "update-ref", "HEAD", merge)
				require.Equal(t, "1", runGit(t, tcPath, "rev-list", "--count", initial+"..HEAD"))
			}
			pending := runGit(t, tcPath, "rev-parse", "HEAD")
			require.NoError(t, os.Remove(hook))
			docBefore := readDocFiles(t, f.docDir())
			indexBefore, err := os.ReadFile(filepath.Join(tcPath, ".git", "index"))
			require.NoError(t, err)

			out, err := f.importDoc(false)
			remoteHistory := runGit(t, f.bare, "log", "--format=%H", "HEAD", "--", ":(literal)private.md")
			t.Logf("%s retry err=%v output=%q remotePrivateHistory=%q", name, err, out, remoteHistory)
			assert.ErrorContains(t, err, "outgoing history includes other commits or a merge")
			assert.NotContains(t, out, "Imported:")
			assert.Equal(t, initial, runGit(t, f.bare, "rev-parse", "HEAD"))
			assert.Empty(t, remoteHistory)
			assert.Empty(t, runGit(t, f.bare, "ls-tree", "-r", "HEAD", "--", "data/docs"))
			assert.Equal(t, pending, runGit(t, tcPath, "rev-parse", "HEAD"))
			assert.Equal(t, docBefore, readDocFiles(t, f.docDir()))
			indexAfter, err := os.ReadFile(filepath.Join(tcPath, ".git", "index"))
			require.NoError(t, err)
			assert.Equal(t, indexBefore, indexAfter)
		})
	}
}

// TestImport_LiteralFilenameCanBeRetried verifies legitimate pathspec metacharacters stay importable.
func TestImport_LiteralFilenameCanBeRetried(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	f := newImportRetryFixture(t)
	tcPath := f.useFileGitRemote(t)
	renamed := filepath.Join(filepath.Dir(f.src), "[q3]-plan.md")
	require.NoError(t, os.Rename(f.src, renamed))
	f.src = renamed
	initial := runGit(t, f.bare, "rev-parse", "HEAD")
	hook := filepath.Join(f.bare, "hooks", "pre-receive")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\necho 'Permission denied' >&2\nexit 1\n"), 0o755))
	_, err := f.importDoc(false)
	require.ErrorContains(t, err, "Permission denied")
	pending := runGit(t, tcPath, "rev-parse", "HEAD")
	require.NotEqual(t, initial, pending)
	require.NoError(t, os.Remove(hook))

	out, err := f.importDoc(false)
	require.NoError(t, err)
	assert.Contains(t, out, "Imported:")
	assert.Equal(t, pending, runGit(t, f.bare, "rev-parse", "HEAD"))
	assert.Equal(t, pending, runGit(t, tcPath, "rev-parse", "HEAD"))
	const remoteDoc = "data/docs/2026/09/19/q3-plan"
	var meta docMeta
	require.NoError(t, json.Unmarshal([]byte(runGit(t, f.bare, "show", "HEAD:"+remoteDoc+"/metadata.json")), &meta))
	assert.Equal(t, "sha256:c322135151cf0bc395a4d1e2a3e560e6cb0a62057e70e749c4f18cdbb81f32d7", meta.SourceOID)
	assert.Equal(t, "[q3]-plan.md", meta.SourceFilename)
	assert.Equal(t, "version https://git-lfs.github.com/spec/v1\noid "+meta.SourceOID+"\nsize 10", runGit(t, f.bare, "show", "HEAD:"+remoteDoc+"/[q3]-plan.md"))
	assert.Equal(t, "data/**/metadata.json !filter !diff !merge text", runGit(t, f.bare, "show", "HEAD:.gitattributes"))
}

// TestImport_RetryHoldsRepoLockThroughPush keeps managed writers out between validation and publication.
func TestImport_RetryHoldsRepoLockThroughPush(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	f := newImportRetryFixture(t)
	tcPath := f.useFileGitRemote(t)
	hook := filepath.Join(f.bare, "hooks", "pre-receive")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\necho 'Permission denied' >&2\nexit 1\n"), 0o755))
	_, err := f.importDoc(false)
	require.ErrorContains(t, err, "Permission denied")
	pending := runGit(t, tcPath, "rev-parse", "HEAD")
	started := filepath.Join(f.bare, "hooks", "push-started")
	released := filepath.Join(f.bare, "hooks", "push-released")
	require.NoError(t, os.WriteFile(hook, []byte(`#!/bin/sh
marker="$(dirname "$0")/push-started"
release="$(dirname "$0")/push-released"
: > "$marker"
n=0
while [ ! -f "$release" ]; do
  n=$((n + 1))
  if [ "$n" -ge 1000 ]; then exit 1; fi
  sleep 0.01
done
`), 0o755))
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	finished := false
	t.Cleanup(func() {
		_ = os.WriteFile(released, nil, 0o644)
		if !finished {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("import did not finish after releasing the bare hook")
			}
		}
	})
	go func() {
		out, err := f.importDoc(false)
		done <- result{out, err}
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(started); return err == nil }, 5*time.Second, 10*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	acquired := false
	err = gitutil.WithRepoLock(ctx, tcPath, func() error { acquired = true; return nil })
	assert.True(t, gitutil.IsRepoLockBusy(err), "a managed writer must wait while the recovery push is in flight")
	assert.False(t, acquired)
	require.NoError(t, os.WriteFile(released, nil, 0o644))
	select {
	case retry := <-done:
		finished = true
		require.NoError(t, retry.err)
		assert.Contains(t, retry.out, "Imported:")
	case <-time.After(5 * time.Second):
		t.Fatal("import did not finish after releasing the bare hook")
	}
	assert.Equal(t, pending, runGit(t, f.bare, "rev-parse", "HEAD"))
	assert.Equal(t, pending, runGit(t, tcPath, "rev-parse", "HEAD"))
}

// TestImport_NoOpCommitStillPushesPendingDocument verifies a no-op snapshot publishes an earlier failed push.
// Without this, an unchanged document returns success while its original commit is still unpublished.
func TestImport_NoOpCommitStillPushesPendingDocument(t *testing.T) {
	f := newImportRetryFixture(t)
	tcPath := filepath.Join(paths.TeamsDataDir(f.endpoint), importRetryTeamID)
	remoteBefore := runGit(t, f.bare, "rev-parse", "HEAD")
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Permission denied", http.StatusForbidden)
	}))
	t.Cleanup(denied.Close)
	runGit(t, tcPath, "remote", "set-url", "--push", "origin", denied.URL+"/team.git")

	_, err := f.importDoc(false)
	require.ErrorContains(t, err, "git push failed")
	require.ErrorContains(t, err, "403")
	t.Logf("first import reached the push permission failure: %v", err)
	committed := runGit(t, tcPath, "rev-parse", "HEAD")
	require.NotEqual(t, remoteBefore, committed)
	assert.Equal(t, remoteBefore, runGit(t, f.bare, "rev-parse", "HEAD"))

	runGit(t, tcPath, "remote", "set-url", "--push", "origin", f.bare)
	runGit(t, tcPath, "remote", "set-url", "origin", f.bare)
	err = commitAndPushDocImport(tcPath, f.endpoint, "q3-plan", filepath.Join(f.docDir(), "metadata.json"), filepath.Join(f.docDir(), "q3-plan.md"), "", false)
	require.NoError(t, err, "an unchanged document must still push its existing commit")
	assert.Equal(t, committed, runGit(t, tcPath, "rev-parse", "HEAD"), "a no-op snapshot must not create a commit")
	assert.Equal(t, committed, runGit(t, f.bare, "rev-parse", "HEAD"))

	const remoteDoc = "data/docs/2026/09/19/q3-plan"
	const sourceOID = "sha256:c322135151cf0bc395a4d1e2a3e560e6cb0a62057e70e749c4f18cdbb81f32d7"
	metadata := runGit(t, f.bare, "show", "HEAD:"+remoteDoc+"/metadata.json")
	var meta docMeta
	require.NoError(t, json.Unmarshal([]byte(metadata), &meta))
	assert.Equal(t, sourceOID, meta.SourceOID)
	assert.Equal(t, "version https://git-lfs.github.com/spec/v1\noid "+sourceOID+"\nsize 10", runGit(t, f.bare, "show", "HEAD:"+remoteDoc+"/q3-plan.md"))
}

// TestImport_PublishedDuplicateDoesNotPushOtherCommits verifies deduplication leaves unrelated local commits unpublished.
func TestImport_PublishedDuplicateDoesNotPushOtherCommits(t *testing.T) {
	f := newImportRetryFixture(t)
	tcPath := filepath.Join(paths.TeamsDataDir(f.endpoint), importRetryTeamID)
	runGit(t, tcPath, "branch", "-M", "main")
	runGit(t, tcPath, "push", "--set-upstream", "origin", "main")
	runGit(t, f.bare, "symbolic-ref", "HEAD", "refs/heads/main")
	_, err := f.importDoc(false)
	require.NoError(t, err)
	published := runGit(t, f.bare, "rev-parse", "HEAD")

	// Match a standard clone: one origin URL and no separate push URL. Refresh is a no-op for this local remote.
	runGit(t, tcPath, "remote", "set-url", "origin", f.bare)
	runGit(t, tcPath, "config", "--local", "--unset-all", "remote.origin.pushurl")
	require.NoError(t, gitserver.RefreshRemoteCredentials(tcPath, f.endpoint))
	assert.Equal(t, f.bare, runGit(t, tcPath, "remote", "get-url", "origin"))
	assert.Equal(t, f.bare, runGit(t, tcPath, "remote", "get-url", "--push", "origin"))
	assert.Equal(t, "refs/remotes/origin/main", runGit(t, tcPath, "rev-parse", "--symbolic-full-name", "@{push}"))

	draft := filepath.Join(tcPath, "draft.md")
	require.NoError(t, os.WriteFile(draft, []byte("private draft\n"), 0o644))
	runGit(t, tcPath, "add", "draft.md")
	runGit(t, tcPath, "commit", "--no-verify", "-m", "private draft")
	unpublished := runGit(t, tcPath, "rev-parse", "HEAD")
	require.NotEqual(t, published, unpublished)

	out, err := f.importDoc(false)
	require.NoError(t, err)
	assert.Contains(t, out, "Already imported")
	assert.Equal(t, unpublished, runGit(t, tcPath, "rev-parse", "HEAD"))
	assert.Equal(t, published, runGit(t, f.bare, "rev-parse", "HEAD"), "deduplication must not push an unrelated commit")
	assert.Empty(t, runGit(t, f.bare, "ls-tree", "HEAD", "--", "draft.md"))
	content, err := os.ReadFile(draft)
	require.NoError(t, err)
	assert.Equal(t, "private draft\n", string(content))
	metadata := runGit(t, f.bare, "show", "HEAD:data/docs/2026/09/19/q3-plan/metadata.json")
	var meta docMeta
	require.NoError(t, json.Unmarshal([]byte(metadata), &meta))
	assert.Equal(t, "sha256:c322135151cf0bc395a4d1e2a3e560e6cb0a62057e70e749c4f18cdbb81f32d7", meta.SourceOID)
	assert.Contains(t, runGit(t, f.bare, "show", "HEAD:data/docs/2026/09/19/q3-plan/q3-plan.md"), "oid "+meta.SourceOID)
}

// TestImport_RemovedPublishedDocumentIsNotRetried preserves an upstream deletion and private local work.
func TestImport_RemovedPublishedDocumentIsNotRetried(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	for _, tc := range []struct {
		name             string
		additionalCommit bool
	}{
		{name: "diverged replacement", additionalCommit: true},
		{name: "rewind to ancestor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newImportRetryFixture(t)
			tcPath := f.useFileGitRemote(t)
			runGit(t, tcPath, "branch", "-M", "main")
			runGit(t, tcPath, "push", "--set-upstream", "origin", "main")
			runGit(t, f.bare, "symbolic-ref", "HEAD", "refs/heads/main")
			initial := runGit(t, f.bare, "rev-parse", "HEAD")
			_, err := f.importDoc(false)
			require.NoError(t, err)
			published := runGit(t, f.bare, "rev-parse", "HEAD")
			require.NotEqual(t, initial, published)
			const remoteDoc = "data/docs/2026/09/19/q3-plan"
			assert.NotEmpty(t, runGit(t, f.bare, "ls-tree", "-r", "HEAD", "--", remoteDoc))

			other := cloneBare(t, f.bare)
			runGit(t, other, "reset", "--hard", initial)
			if tc.additionalCommit {
				require.NoError(t, os.WriteFile(filepath.Join(other, "upstream.md"), []byte("rewritten upstream\n"), 0o644))
				runGit(t, other, "add", "upstream.md")
				runGit(t, other, "commit", "--no-verify", "-m", "replace published import")
			}
			runGit(t, other, "push", "--force", "origin", "main")
			rewritten := runGit(t, f.bare, "rev-parse", "HEAD")
			require.NotEqual(t, published, rewritten)
			require.Empty(t, runGit(t, f.bare, "ls-tree", "-r", "HEAD", "--", remoteDoc))

			runGit(t, tcPath, "fetch", "origin")
			require.Equal(t, rewritten, runGit(t, tcPath, "rev-parse", "refs/remotes/origin/main"))
			require.Equal(t, published, runGit(t, tcPath, "rev-parse", "HEAD"))
			draft := filepath.Join(tcPath, "draft.md")
			require.NoError(t, os.WriteFile(draft, []byte("private draft\n"), 0o644))
			runGit(t, tcPath, "add", "draft.md")
			runGit(t, tcPath, "commit", "--no-verify", "-m", "private draft")
			privateHead := runGit(t, tcPath, "rev-parse", "HEAD")
			require.Equal(t, published, runGit(t, tcPath, "merge-base", "--fork-point", "refs/remotes/origin/main", "HEAD"))

			pending := filepath.Join(tcPath, "pending.md")
			require.NoError(t, os.WriteFile(pending, []byte("staged personal notes\n"), 0o644))
			runGit(t, tcPath, "add", "pending.md")
			require.NoError(t, os.WriteFile(pending, []byte("working personal notes\n"), 0o644))
			docBefore := readDocFiles(t, f.docDir())
			indexBefore := runGit(t, tcPath, "ls-files", "--stage")

			out, err := f.importDoc(false)
			assert.ErrorContains(t, err, "previously published and removed", "a removed published import must not be resumed as a new document")
			assert.NotContains(t, out, "Imported:")
			assert.NotContains(t, out, "Already imported")
			assert.Equal(t, rewritten, runGit(t, f.bare, "rev-parse", "HEAD"), "the retry must not publish private commits")
			assert.Empty(t, runGit(t, f.bare, "ls-tree", "HEAD", "--", "draft.md", "pending.md"))
			assert.Empty(t, runGit(t, f.bare, "ls-tree", "-r", "HEAD", "--", remoteDoc))
			assert.Equal(t, privateHead, runGit(t, tcPath, "rev-parse", "HEAD"))
			assert.Equal(t, rewritten, runGit(t, tcPath, "rev-parse", "refs/remotes/origin/main"))
			assert.Equal(t, docBefore, readDocFiles(t, f.docDir()))
			assert.Equal(t, indexBefore, runGit(t, tcPath, "ls-files", "--stage"))
			content, err := os.ReadFile(pending)
			require.NoError(t, err)
			assert.Equal(t, "working personal notes\n", string(content))
			content, err = os.ReadFile(draft)
			require.NoError(t, err)
			assert.Equal(t, "private draft\n", string(content))
		})
	}
}

// TestImport_RetrySynchronizesWithUpstream verifies a retry publishes a pending import after a teammate pushed.
// Without this, every retry's push is rejected as non-fast-forward and the import never reaches the team.
func TestImport_RetrySynchronizesWithUpstream(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	f := newImportRetryFixture(t)
	tcPath := f.useFileGitRemote(t)
	runGit(t, tcPath, "branch", "-M", "main")
	runGit(t, tcPath, "push", "--set-upstream", "origin", "main")
	runGit(t, f.bare, "symbolic-ref", "HEAD", "refs/heads/main")
	initial := runGit(t, f.bare, "rev-parse", "HEAD")
	hook := filepath.Join(f.bare, "hooks", "pre-receive")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\necho 'Permission denied' >&2\nexit 1\n"), 0o755))
	_, err := f.importDoc(false)
	require.ErrorContains(t, err, "Permission denied")
	require.NotEqual(t, initial, runGit(t, tcPath, "rev-parse", "HEAD"), "the import must have committed before its push failed")
	require.Equal(t, initial, runGit(t, f.bare, "rev-parse", "HEAD"))
	require.NoError(t, os.Remove(hook))

	other := cloneBare(t, f.bare)
	require.NoError(t, os.WriteFile(filepath.Join(other, "upstream.md"), []byte("remote change\n"), 0o644))
	runGit(t, other, "add", "upstream.md")
	runGit(t, other, "commit", "--no-verify", "-m", "advance upstream")
	runGit(t, other, "push", "origin", "main")
	advanced := runGit(t, f.bare, "rev-parse", "HEAD")
	draft := filepath.Join(tcPath, "draft.md")
	require.NoError(t, os.WriteFile(draft, []byte("working draft\n"), 0o644))
	docBefore := readDocFiles(t, f.docDir())

	out, err := f.importDoc(false)
	require.NoError(t, err, "a retry must synchronize with an ordinary upstream advance and publish")
	assert.Contains(t, out, "Imported:")
	assert.Equal(t, advanced, runGit(t, f.bare, "rev-parse", "HEAD^"), "the import must land on top of the teammate's commit")
	assert.Equal(t, "import: doc q3-plan", runGit(t, f.bare, "log", "-1", "--format=%s"), "retrying must not create another commit")
	assert.Equal(t, "remote change", runGit(t, f.bare, "show", "HEAD:upstream.md"))
	assert.Empty(t, runGit(t, f.bare, "ls-tree", "HEAD", "--", "draft.md"), "the retry must not publish unrelated work")
	const remoteDoc = "data/docs/2026/09/19/q3-plan"
	var meta docMeta
	require.NoError(t, json.Unmarshal([]byte(runGit(t, f.bare, "show", "HEAD:"+remoteDoc+"/metadata.json")), &meta))
	assert.Equal(t, "sha256:c322135151cf0bc395a4d1e2a3e560e6cb0a62057e70e749c4f18cdbb81f32d7", meta.SourceOID)
	assert.Equal(t, "version https://git-lfs.github.com/spec/v1\noid "+meta.SourceOID+"\nsize 10", runGit(t, f.bare, "show", "HEAD:"+remoteDoc+"/q3-plan.md"))
	assert.Equal(t, docBefore, readDocFiles(t, f.docDir()))
	content, err := os.ReadFile(draft)
	require.NoError(t, err)
	assert.Equal(t, "working draft\n", string(content), "the retry must keep the coworker's unpublished draft")
}

// TestImport_PublishedDuplicateIgnoresPushConfiguration verifies push settings Git accepts keep deduplication working.
// Without this, re-importing a published document fails instead of reporting it as already imported.
func TestImport_PublishedDuplicateIgnoresPushConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config []string
	}{
		// the fixture fetches from the LFS server and pushes to the bare remote
		{name: "separate push URL"},
		{name: "push.default matching", config: []string{"push.default", "matching"}},
		{name: "explicit push refspec", config: []string{"remote.origin.push", "refs/heads/*:refs/heads/*"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newImportRetryFixture(t)
			tcPath := filepath.Join(paths.TeamsDataDir(f.endpoint), importRetryTeamID)
			_, err := f.importDoc(false)
			require.NoError(t, err)
			if tc.config != nil {
				runGit(t, tcPath, append([]string{"config", "--local"}, tc.config...)...)
			}
			headBefore := runGit(t, tcPath, "rev-parse", "HEAD")
			remoteBefore := runGit(t, f.bare, "rev-parse", "HEAD")

			out, err := f.importDoc(false)
			require.NoError(t, err, "a published document must still deduplicate")
			assert.Contains(t, out, "Already imported")
			assert.Equal(t, headBefore, runGit(t, tcPath, "rev-parse", "HEAD"))
			assert.Equal(t, remoteBefore, runGit(t, f.bare, "rev-parse", "HEAD"))
		})
	}
}

// TestImport_UnknownPublicationIsNotReportedAsSuccess verifies uncertain publication leaves the document untouched.
// Without this, a metadata match reports success even when its publication cannot be established.
func TestImport_UnknownPublicationIsNotReportedAsSuccess(t *testing.T) {
	f := newImportRetryFixture(t)
	tcPath := filepath.Join(paths.TeamsDataDir(f.endpoint), importRetryTeamID)
	_, err := f.importDoc(false)
	require.NoError(t, err)
	runGit(t, tcPath, "remote", "set-url", "origin", f.bare)
	runGit(t, tcPath, "config", "--local", "--unset-all", "remote.origin.pushurl")
	pushRef := runGit(t, tcPath, "rev-parse", "--symbolic-full-name", "@{push}")
	runGit(t, tcPath, "update-ref", "-d", pushRef)
	before := readDocFiles(t, f.docDir())
	indexBefore := runGit(t, tcPath, "ls-files", "--stage")
	headBefore := runGit(t, tcPath, "rev-parse", "HEAD")
	remoteBefore := runGit(t, f.bare, "rev-parse", "HEAD")

	out, err := f.importDoc(false)
	require.ErrorContains(t, err, "cannot determine import publication")
	assert.ErrorContains(t, err, "rerun ox import", "the error must say what the coworker can do next")
	assert.NotContains(t, err.Error(), f.endpoint, "diagnostics must not expose a remote URL")
	assert.NotContains(t, out, "Already imported")
	assert.Equal(t, before, readDocFiles(t, f.docDir()))
	assert.Equal(t, indexBefore, runGit(t, tcPath, "ls-files", "--stage"))
	assert.Equal(t, headBefore, runGit(t, tcPath, "rev-parse", "HEAD"))
	assert.Equal(t, remoteBefore, runGit(t, f.bare, "rev-parse", "HEAD"))
}

// TestImport_WriteFailureAfterUploadDoesNotBlockRetry verifies a failed pointer or manifest write leaves no document directory.
// Without this, the leftover directory makes the retry fail as a concurrent import unless --force is passed.
func TestImport_WriteFailureAfterUploadDoesNotBlockRetry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wantErr string
		write   func(dir string, files map[string]lfs.UploadedRef) ([]string, error)
	}{
		{
			name: "pointer write fails", wantErr: "write pointer files",
			write: func(string, map[string]lfs.UploadedRef) ([]string, error) { return nil, errors.New("disk full") },
		},
		{
			name: "metadata write fails", wantErr: "write metadata.json",
			write: func(dir string, files map[string]lfs.UploadedRef) ([]string, error) {
				written, err := lfs.WritePointerFiles(dir, files)
				if err != nil {
					return written, err
				}
				// a directory where the manifest goes makes the metadata.json write fail
				return written, os.Mkdir(filepath.Join(dir, "metadata.json"), 0o755)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newImportRetryFixture(t)
			prior := writeDocPointerFiles
			t.Cleanup(func() { writeDocPointerFiles = prior })
			writeDocPointerFiles = tc.write
			_, err := f.importDoc(false)
			require.ErrorContains(t, err, tc.wantErr)
			assert.NoDirExists(t, f.docDir(), "a failed write must leave no document directory behind")

			writeDocPointerFiles = prior
			out, err := f.importDoc(false)
			require.NoError(t, err, "the retry must succeed without --force")
			assert.Contains(t, out, "Imported:")
			assert.Equal(t, "import: doc q3-plan", runGit(t, f.bare, "log", "-1", "--format=%s"), "the retry must reach the remote")
		})
	}
}

// Failed pointer rollback reports paths that may now belong to another writer.
// Import cleanup must preserve those bytes instead of assuming it owns the paths.
func TestImport_FailedPointerRollbackPreservesLaterWriter(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real import with a failed pointer rollback")
	}
	f := newImportRetryFixture(t)
	tcPath := filepath.Join(paths.TeamsDataDir(f.endpoint), importRetryTeamID)
	remoteBefore := runGit(t, f.bare, "show-ref")
	indexBefore := runGit(t, tcPath, "ls-files", "--stage")
	prior := writeDocPointerFiles
	t.Cleanup(func() { writeDocPointerFiles = prior })
	var retained string
	writeDocPointerFiles = func(dir string, files map[string]lfs.UploadedRef) ([]string, error) {
		written, err := lfs.WritePointerFiles(dir, files)
		require.NoError(t, err)
		require.NotEmpty(t, written)
		retained = written[0]
		require.NoError(t, os.WriteFile(retained, []byte("later writer's draft\n"), 0o644))
		// this is the documented result of a rollback that discovers changed bytes
		return []string{retained}, errors.New("destination changed during pointer preparation")
	}
	_, err := f.importDoc(false)
	require.ErrorContains(t, err, "destination changed during pointer preparation")
	content, err := os.ReadFile(retained)
	require.NoError(t, err)
	assert.Equal(t, "later writer's draft\n", string(content))
	assert.Equal(t, remoteBefore, runGit(t, f.bare, "show-ref"))
	assert.Equal(t, indexBefore, runGit(t, tcPath, "ls-files", "--stage"))
	assert.NoFileExists(t, filepath.Join(f.docDir(), "metadata.json"))
}

// TestImport_MissingCommittedAttributesPointsToDoctor verifies an import never rewrites attributes it cannot see.
// Without this, an old sparse checkout publishes a replacement .gitattributes that drops the team's attributes.
func TestImport_MissingCommittedAttributesPointsToDoctor(t *testing.T) {
	f := newImportRetryFixture(t)
	tcPath := filepath.Join(paths.TeamsDataDir(f.endpoint), importRetryTeamID)
	attrs := filepath.Join(tcPath, ".gitattributes")
	require.NoError(t, os.WriteFile(attrs, []byte("*.txt text\n"), 0o644))
	runGit(t, tcPath, "add", ".gitattributes")
	runGit(t, tcPath, "commit", "--no-verify", "-m", "team attributes")
	runGit(t, tcPath, "push")
	// a sparse checkout that never materialized root-level files
	runGit(t, tcPath, "update-index", "--skip-worktree", ".gitattributes")
	require.NoError(t, os.Remove(attrs))
	remoteBefore := runGit(t, f.bare, "rev-parse", "HEAD")

	_, err := f.importDoc(false)
	require.ErrorContains(t, err, "ox doctor")
	assert.NoFileExists(t, attrs)
	assert.Equal(t, remoteBefore, runGit(t, f.bare, "rev-parse", "HEAD"))
	assert.Equal(t, "*.txt text", runGit(t, f.bare, "show", "HEAD:.gitattributes"), "the team's attributes must survive")
}

// TestImport_RetryPreservesStagedDocumentDeletion verifies a retry refuses conflicting index ownership.
// Without this, a missing index entry is mistaken for an untouched document and the pending import is pushed.
func TestImport_RetryPreservesStagedDocumentDeletion(t *testing.T) {
	f := newImportRetryFixture(t)
	tcPath := filepath.Join(paths.TeamsDataDir(f.endpoint), importRetryTeamID)
	remoteBefore := runGit(t, f.bare, "rev-parse", "HEAD")
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Permission denied", http.StatusForbidden)
	}))
	t.Cleanup(denied.Close)
	runGit(t, tcPath, "remote", "set-url", "--push", "origin", denied.URL+"/team.git")
	_, err := f.importDoc(false)
	require.ErrorContains(t, err, "403")
	committed := runGit(t, tcPath, "rev-parse", "HEAD")
	require.NotEqual(t, remoteBefore, committed)

	runGit(t, tcPath, "remote", "set-url", "origin", f.bare)
	runGit(t, tcPath, "config", "--local", "--unset-all", "remote.origin.pushurl")
	runGit(t, tcPath, "rm", "--cached", "--", "data/docs/2026/09/19/q3-plan/q3-plan.md")
	indexBefore := runGit(t, tcPath, "ls-files", "--stage")
	before := readDocFiles(t, f.docDir())

	_, err = f.importDoc(false)
	assert.ErrorContains(t, err, "staged")
	assert.Equal(t, before, readDocFiles(t, f.docDir()))
	assert.Equal(t, indexBefore, runGit(t, tcPath, "ls-files", "--stage"))
	assert.Equal(t, committed, runGit(t, tcPath, "rev-parse", "HEAD"))
	assert.Equal(t, remoteBefore, runGit(t, f.bare, "rev-parse", "HEAD"), "a conflicting staged deletion must block publication")
}

// TestImport_RetryRejectsConflictingDocumentContent verifies recovery refuses incomplete or modified saved documents.
// Without this, a metadata match can hide a missing pointer or overwrite another writer's document changes.
func TestImport_RetryRejectsConflictingDocumentContent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wantErr string
		inject  func(t *testing.T, f *importRetryFixture, tcPath string)
	}{
		{
			name: "missing pointer", wantErr: "read import file",
			inject: func(t *testing.T, f *importRetryFixture, tcPath string) {
				require.NoError(t, os.Remove(filepath.Join(f.docDir(), "q3-plan.md")))
			},
		},
		{
			name: "symlink pointer", wantErr: "not a regular file",
			inject: func(t *testing.T, f *importRetryFixture, tcPath string) {
				pointer := filepath.Join(f.docDir(), "q3-plan.md")
				content, err := os.ReadFile(pointer)
				require.NoError(t, err)
				target := filepath.Join(t.TempDir(), "private-pointer.md")
				require.NoError(t, os.WriteFile(target, content, 0o644))
				require.NoError(t, os.Remove(pointer))
				// Windows may not grant the test process permission to create symlinks.
				if err := os.Symlink(target, pointer); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
		},
		{
			name: "unsafe pointer filename", wantErr: "unsafe import pointer filename",
			inject: func(t *testing.T, f *importRetryFixture, tcPath string) {
				path := filepath.Join(f.docDir(), "metadata.json")
				content, err := os.ReadFile(path)
				require.NoError(t, err)
				var meta docMeta
				require.NoError(t, json.Unmarshal(content, &meta))
				meta.SourceFilename = "../private.md"
				content, err = json.MarshalIndent(meta, "", "  ")
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, content, 0o644))
			},
		},
		{
			name: "modified manifest", wantErr: "differs from HEAD",
			inject: func(t *testing.T, f *importRetryFixture, tcPath string) {
				path := filepath.Join(f.docDir(), "metadata.json")
				content, err := os.ReadFile(path)
				require.NoError(t, err)
				var meta docMeta
				require.NoError(t, json.Unmarshal(content, &meta))
				meta.Title = "private title edit"
				content, err = json.MarshalIndent(meta, "", "  ")
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, content, 0o644))
			},
		},
		{
			name: "different staged pointer", wantErr: "different staged content",
			inject: func(t *testing.T, f *importRetryFixture, tcPath string) {
				pointer := filepath.Join(f.docDir(), "q3-plan.md")
				content, err := os.ReadFile(pointer)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(pointer, []byte("private staged content\n"), 0o644))
				runGit(t, tcPath, "add", "data/docs/2026/09/19/q3-plan/q3-plan.md")
				require.NoError(t, os.WriteFile(pointer, content, 0o644))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newImportRetryFixture(t)
			tcPath := filepath.Join(paths.TeamsDataDir(f.endpoint), importRetryTeamID)
			remoteBefore := runGit(t, f.bare, "rev-parse", "HEAD")
			denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "Permission denied", http.StatusForbidden)
			}))
			t.Cleanup(denied.Close)
			runGit(t, tcPath, "remote", "set-url", "--push", "origin", denied.URL+"/team.git")
			_, err := f.importDoc(false)
			require.ErrorContains(t, err, "403")
			committed := runGit(t, tcPath, "rev-parse", "HEAD")
			require.NotEqual(t, remoteBefore, committed)
			runGit(t, tcPath, "remote", "set-url", "origin", f.bare)
			runGit(t, tcPath, "config", "--local", "--unset-all", "remote.origin.pushurl")
			tc.inject(t, f, tcPath)
			before := readDocFiles(t, f.docDir())
			indexBefore := runGit(t, tcPath, "ls-files", "--stage")
			pointer := filepath.Join(f.docDir(), "q3-plan.md")
			var linkBefore string
			if tc.name == "symlink pointer" {
				linkBefore, err = os.Readlink(pointer)
				require.NoError(t, err)
			}

			_, err = f.importDoc(false)
			assert.ErrorContains(t, err, tc.wantErr)
			assert.Equal(t, before, readDocFiles(t, f.docDir()))
			assert.Equal(t, indexBefore, runGit(t, tcPath, "ls-files", "--stage"))
			assert.Equal(t, committed, runGit(t, tcPath, "rev-parse", "HEAD"))
			assert.Equal(t, remoteBefore, runGit(t, f.bare, "rev-parse", "HEAD"))
			if linkBefore != "" {
				linkAfter, err := os.Readlink(pointer)
				require.NoError(t, err)
				assert.Equal(t, linkBefore, linkAfter)
			}
		})
	}
}

// TestImport_RetryKeepsSavedManifestAndSidecars verifies recovery publishes the original saved document.
// Without this, a retry can rename or redate the document and drop a previously uploaded sidecar.
func TestImport_RetryKeepsSavedManifestAndSidecars(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	f := newImportRetryFixture(t)
	tcPath := f.useFileGitRemote(t)
	f.text = filepath.Join(filepath.Dir(f.src), "extracted.md")
	require.NoError(t, os.WriteFile(f.text, []byte("extracted text\n"), 0o644))
	runGit(t, tcPath, "config", "--local", "user.name", "")
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_COMMITTER_NAME", "")
	t.Setenv("LC_ALL", "C")
	_, err := f.importDoc(false)
	require.ErrorContains(t, err, "empty ident name")
	before := readDocFiles(t, f.docDir())
	require.Contains(t, before, "metadata.json")
	require.Contains(t, before, "extracted.md")

	runGit(t, tcPath, "config", "--local", "user.name", "Test")
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	renamed := filepath.Join(filepath.Dir(f.src), "renamed-plan.md")
	require.NoError(t, os.Rename(f.src, renamed))
	importFlags = importFlagsT{team: importRetryTeamID, date: "2026-10-07"}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	err = runImport(cmd, []string{renamed})
	require.NoError(t, err)
	assert.Contains(t, out.String(), "Imported:")
	assert.Equal(t, before, readDocFiles(t, f.docDir()), "recovery must reuse the saved manifest and all pointers")

	const remoteDoc = "data/docs/2026/09/19/q3-plan"
	metadata := runGit(t, f.bare, "show", "HEAD:"+remoteDoc+"/metadata.json")
	assert.Equal(t, strings.TrimSpace(before["metadata.json"]), metadata)
	var meta docMeta
	require.NoError(t, json.Unmarshal([]byte(metadata), &meta))
	assert.Equal(t, remoteDoc, meta.Path)
	assert.Equal(t, "2026-09-19T00:00:00Z", meta.CreatedAt)
	assert.Equal(t, "q3-plan.md", meta.SourceFilename)
	assert.Equal(t, "version https://git-lfs.github.com/spec/v1\noid sha256:c322135151cf0bc395a4d1e2a3e560e6cb0a62057e70e749c4f18cdbb81f32d7\nsize 10", runGit(t, f.bare, "show", "HEAD:"+remoteDoc+"/q3-plan.md"))
	assert.Equal(t, "version https://git-lfs.github.com/spec/v1\noid sha256:0f4c7dd751f2a7e4e79a67aa940c84e5bb10eb101e5b116a8a0aa613f0a6ad8e\nsize 15", runGit(t, f.bare, "show", "HEAD:"+remoteDoc+"/extracted.md"))
	assert.Empty(t, runGit(t, f.bare, "ls-tree", "HEAD", "--", "data/docs/2026/10/07/renamed-plan"))
}

// TestImport_CleanCRLFAttributesAllowNewDocument verifies a clean CRLF checkout can import a document.
// Without this, attributes that Git considers unchanged are mistaken for another writer's edits.
func TestImport_CleanCRLFAttributesAllowNewDocument(t *testing.T) {
	f := newImportRetryFixture(t)
	tcPath := filepath.Join(paths.TeamsDataDir(f.endpoint), importRetryTeamID)
	runGit(t, tcPath, "config", "--local", "core.autocrlf", "false")
	attrs := filepath.Join(tcPath, ".gitattributes")
	require.NoError(t, os.WriteFile(attrs, []byte("*.txt text\n"), 0o644))
	runGit(t, tcPath, "add", ".gitattributes")
	runGit(t, tcPath, "commit", "--no-verify", "-m", "initial attributes")
	runGit(t, tcPath, "push")
	runGit(t, tcPath, "config", "--local", "core.autocrlf", "true")
	require.NoError(t, os.Remove(attrs))
	runGit(t, tcPath, "checkout", "HEAD", "--", ".gitattributes")
	content, err := os.ReadFile(attrs)
	require.NoError(t, err)
	require.Equal(t, "*.txt text\r\n", string(content))
	require.Empty(t, runGit(t, tcPath, "status", "--porcelain"), "the CRLF checkout must be clean before import")

	out, err := f.importDoc(false)
	require.NoError(t, err, "clean CRLF attributes must allow the new import")
	assert.Contains(t, out, "Imported:")
	metadata := runGit(t, f.bare, "show", "HEAD:data/docs/2026/09/19/q3-plan/metadata.json")
	var meta docMeta
	require.NoError(t, json.Unmarshal([]byte(metadata), &meta))
	assert.Equal(t, "sha256:c322135151cf0bc395a4d1e2a3e560e6cb0a62057e70e749c4f18cdbb81f32d7", meta.SourceOID)
	assert.Equal(t, "version https://git-lfs.github.com/spec/v1\noid "+meta.SourceOID+"\nsize 10", runGit(t, f.bare, "show", "HEAD:data/docs/2026/09/19/q3-plan/q3-plan.md"))
}

// TestImport_CleanCRLFDuplicateDoesNotPushOtherCommits verifies clean CRLF documents retain deduplication.
// Without this, a clean checkout is rejected or an already published document pushes unrelated commits.
func TestImport_CleanCRLFDuplicateDoesNotPushOtherCommits(t *testing.T) {
	f := newImportRetryFixture(t)
	tcPath := filepath.Join(paths.TeamsDataDir(f.endpoint), importRetryTeamID)
	runGit(t, tcPath, "config", "--local", "core.autocrlf", "false")
	_, err := f.importDoc(false)
	require.NoError(t, err)
	published := runGit(t, f.bare, "rev-parse", "HEAD")
	runGit(t, tcPath, "remote", "set-url", "origin", f.bare)
	runGit(t, tcPath, "config", "--local", "--unset-all", "remote.origin.pushurl")
	runGit(t, tcPath, "config", "--local", "core.autocrlf", "true")
	for _, path := range []string{".gitattributes", "data/docs/2026/09/19/q3-plan/metadata.json", "data/docs/2026/09/19/q3-plan/q3-plan.md"} {
		require.NoError(t, os.Remove(filepath.Join(tcPath, path)))
		runGit(t, tcPath, "checkout", "HEAD", "--", path)
		content, err := os.ReadFile(filepath.Join(tcPath, path))
		require.NoError(t, err)
		require.Contains(t, string(content), "\r\n", "Git must have produced a CRLF checkout")
	}
	require.Empty(t, runGit(t, tcPath, "status", "--porcelain"))
	before := readDocFiles(t, f.docDir())
	draft := filepath.Join(tcPath, "draft.md")
	require.NoError(t, os.WriteFile(draft, []byte("private draft\n"), 0o644))
	runGit(t, tcPath, "add", "draft.md")
	runGit(t, tcPath, "commit", "--no-verify", "-m", "private draft")
	unpublished := runGit(t, tcPath, "rev-parse", "HEAD")
	require.NotEqual(t, published, unpublished)

	out, err := f.importDoc(false)
	require.NoError(t, err, "Git-clean CRLF documents must remain deduplicated")
	assert.Contains(t, out, "Already imported")
	assert.Equal(t, before, readDocFiles(t, f.docDir()), "deduplication must preserve checkout bytes")
	assert.Equal(t, unpublished, runGit(t, tcPath, "rev-parse", "HEAD"))
	assert.Equal(t, published, runGit(t, f.bare, "rev-parse", "HEAD"))
	assert.Empty(t, runGit(t, f.bare, "ls-tree", "HEAD", "--", "draft.md"))
	metadata := runGit(t, f.bare, "show", "HEAD:data/docs/2026/09/19/q3-plan/metadata.json")
	var meta docMeta
	require.NoError(t, json.Unmarshal([]byte(metadata), &meta))
	assert.Equal(t, "sha256:c322135151cf0bc395a4d1e2a3e560e6cb0a62057e70e749c4f18cdbb81f32d7", meta.SourceOID)
	assert.Equal(t, "version https://git-lfs.github.com/spec/v1\noid "+meta.SourceOID+"\nsize 10", runGit(t, f.bare, "show", "HEAD:data/docs/2026/09/19/q3-plan/q3-plan.md"))
}

// TestImport_StagedContentIsNotPublished verifies importing preserves an unrelated staged draft.
// Without this, an import commits and pushes another writer's private staged content.
func TestImport_StagedContentIsNotPublished(t *testing.T) {
	f := newImportRetryFixture(t)
	tcPath := filepath.Join(paths.TeamsDataDir(f.endpoint), importRetryTeamID)
	draft := filepath.Join(tcPath, "draft.md")
	require.NoError(t, os.WriteFile(draft, []byte("private draft\n"), 0o644))
	runGit(t, tcPath, "add", "draft.md")
	staged := runGit(t, tcPath, "show", ":draft.md")

	_, err := f.importDoc(false)
	require.NoError(t, err)
	assert.Empty(t, runGit(t, f.bare, "ls-tree", "HEAD", "--", "draft.md"), "an import must not publish an unrelated staged file")
	assert.Equal(t, "draft.md", runGit(t, tcPath, "diff", "--cached", "--name-only", "--", "draft.md"))
	assert.Equal(t, staged, runGit(t, tcPath, "show", ":draft.md"))
	content, err := os.ReadFile(draft)
	require.NoError(t, err)
	assert.Equal(t, "private draft\n", string(content))

	const remoteDoc = "data/docs/2026/09/19/q3-plan"
	metadata := runGit(t, f.bare, "show", "HEAD:"+remoteDoc+"/metadata.json")
	var meta docMeta
	require.NoError(t, json.Unmarshal([]byte(metadata), &meta))
	assert.Equal(t, "sha256:c322135151cf0bc395a4d1e2a3e560e6cb0a62057e70e749c4f18cdbb81f32d7", meta.SourceOID)
	assert.Contains(t, runGit(t, f.bare, "show", "HEAD:"+remoteDoc+"/q3-plan.md"), "oid "+meta.SourceOID)
}

// TestImport_UnrelatedAttributesArePreserved verifies import refuses attributes owned by another writer.
// Without this, import appends to and publishes unrelated .gitattributes edits.
func TestImport_UnrelatedAttributesArePreserved(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tracked bool
		staged  bool
	}{
		{name: "untracked attributes"},
		{name: "unstaged attributes", tracked: true},
		{name: "staged attributes", tracked: true, staged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newImportRetryFixture(t)
			tcPath := filepath.Join(paths.TeamsDataDir(f.endpoint), importRetryTeamID)
			attrsPath := filepath.Join(tcPath, ".gitattributes")
			var original string
			if tc.tracked {
				original = "*.txt text\n"
				require.NoError(t, os.WriteFile(attrsPath, []byte(original), 0o644))
				runGit(t, tcPath, "add", ".gitattributes")
				runGit(t, tcPath, "commit", "--no-verify", "-m", "initial attributes")
				runGit(t, tcPath, "push")
			}
			owned := original + "draft.md -diff\n"
			require.NoError(t, os.WriteFile(attrsPath, []byte(owned), 0o644))
			if tc.staged {
				runGit(t, tcPath, "add", ".gitattributes")
			}
			indexBefore := runGit(t, tcPath, "ls-files", "--stage", "--", ".gitattributes")
			headBefore := runGit(t, tcPath, "rev-parse", "HEAD")
			remoteBefore := runGit(t, f.bare, "rev-parse", "HEAD")

			_, err := f.importDoc(false)
			require.ErrorContains(t, err, ".gitattributes")
			content, err := os.ReadFile(attrsPath)
			require.NoError(t, err)
			assert.Equal(t, owned, string(content), "the attributes must survive byte for byte")
			assert.Equal(t, indexBefore, runGit(t, tcPath, "ls-files", "--stage", "--", ".gitattributes"))
			assert.Equal(t, headBefore, runGit(t, tcPath, "rev-parse", "HEAD"))
			assert.Equal(t, remoteBefore, runGit(t, f.bare, "rev-parse", "HEAD"))
		})
	}
}

// TestImport_FailedForceReimportKeepsExistingDocument checks a failed --force reimport leaves the earlier document intact.
// Without this, cleaning up after a failed --force reimport could delete the document an earlier import committed.
func TestImport_FailedForceReimportKeepsExistingDocument(t *testing.T) {
	for _, tc := range importFailures {
		t.Run(tc.name, func(t *testing.T) {
			f := newImportRetryFixture(t)
			_, err := f.importDoc(false)
			require.NoError(t, err)
			before := readDocFiles(t, f.docDir())
			require.Contains(t, before, "metadata.json")

			require.NoError(t, os.WriteFile(f.src, []byte("# Q3 plan, revised\n"), 0o644))
			tc.inject(t, f)
			_, err = f.importDoc(false)
			require.ErrorContains(t, err, "document directory already exists", "an existing document must still refuse a reimport without --force")

			_, err = f.importDoc(true)
			require.ErrorContains(t, err, tc.wantErr)
			require.DirExists(t, f.docDir(), "a failed --force reimport must not delete the existing document")
			assert.Equal(t, before, readDocFiles(t, f.docDir()), "a failed --force reimport must leave the existing document intact")
		})
	}
}

// TestImport_UncreatableDocDirFailsAfterUpload checks the import fails loudly when its directory cannot be created.
// Without this, a directory error after the upload could go unreported and leave the document half-imported.
func TestImport_UncreatableDocDirFailsAfterUpload(t *testing.T) {
	f := newImportRetryFixture(t)
	// a file where the year directory belongs makes MkdirAll fail after the upload succeeds
	require.NoError(t, os.MkdirAll(f.docsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.docsDir, "2026"), []byte("not a dir"), 0o644))

	_, err := f.importDoc(false)

	require.ErrorContains(t, err, "create doc directory")
	assert.Positive(t, f.uploads.Load(), "the directory must be created only after the upload")
	assert.NoFileExists(t, filepath.Join(f.docDir(), "metadata.json"))
}

// TestImport_ConcurrentImportIsNotOverwritten checks an import refuses a document directory another import created during its upload.
// Without this, two overlapping imports of the same document both pass the existence check and the second overwrites the first without --force.
func TestImport_ConcurrentImportIsNotOverwritten(t *testing.T) {
	f := newImportRetryFixture(t)
	other := filepath.Join(f.docDir(), "metadata.json")
	hook := func() {
		// the other import finishes first and writes its document
		_ = os.MkdirAll(f.docDir(), 0o755)
		_ = os.WriteFile(other, []byte(`{"from":"the other import"}`), 0o644)
	}
	f.onUpload.Store(&hook)

	_, err := f.importDoc(false)

	require.ErrorContains(t, err, "created by another import")
	data, readErr := os.ReadFile(other)
	require.NoError(t, readErr)
	assert.JSONEq(t, `{"from":"the other import"}`, string(data), "the other import's document must not be overwritten")

	f.onUpload.Store(nil)
	_, err = f.importDoc(true)
	require.NoError(t, err, "--force must still reimport over an existing document")
}

// readDocFiles returns each file in dir mapped to its content.
func readDocFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	files := map[string]string{}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		files[e.Name()] = string(data)
	}
	return files
}
