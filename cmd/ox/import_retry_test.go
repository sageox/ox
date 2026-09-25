package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
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
	onUpload    atomic.Pointer[func()] // runs inside an object upload, e.g. to simulate a concurrent import
}

// newImportRetryFixture builds the fixture: a team context clone, an LFS test server and a source document.
func newImportRetryFixture(t *testing.T) *importRetryFixture {
	t.Helper()
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
