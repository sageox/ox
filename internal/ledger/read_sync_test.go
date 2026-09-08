package ledger

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/require"
)

const readRepoID = "repo_01936d5a-0000-7abc-8def-0123456789ab"

type readFixture struct {
	opts   ReadSyncOptions
	source string
	bare   string
	server *httptest.Server
	denied atomic.Bool
}

func readTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func newReadFixture(t *testing.T, lfsHandler ...http.HandlerFunc) *readFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("short: native Git clone and checkout lifecycle")
	}
	f := &readFixture{}
	root := t.TempDir()
	f.source = filepath.Join(root, "source")
	require.NoError(t, os.Mkdir(f.source, 0700))
	readTestGit(t, f.source, "init", "-b", "main")
	readTestGit(t, f.source, "config", "user.name", "Test")
	readTestGit(t, f.source, "config", "user.email", "test@example.invalid")
	for _, p := range []string{"sessions/old/session.md", "data/plans/plan/plan.md", "data/murmurs/2000-01-01/00/old.json", "assets/large.bin"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(f.source, p)), 0700))
		require.NoError(t, os.WriteFile(filepath.Join(f.source, p), []byte(p+"\n"), 0600))
	}
	readTestGit(t, f.source, "add", "--all")
	readTestGit(t, f.source, "commit", "-m", "original")
	f.bare = filepath.Join(root, "ledger.git")
	readTestGit(t, root, "clone", "--bare", f.source, f.bare)
	readTestGit(t, f.bare, "config", "uploadpack.allowFilter", "true")
	readTestGit(t, f.bare, "config", "uploadpack.allowAnySHA1InWant", "true")
	backend := &cgi.Handler{Path: filepath.Join(readTestGit(t, root, "--exec-path"), "git-http-backend"), Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if f.denied.Load() || !ok || u != "ox" || p != "oxt_test_1ljPfr" {
			w.Header().Set("WWW-Authenticate", `Basic realm="ledger"`)
			w.WriteHeader(401)
			return
		}
		if len(lfsHandler) != 0 && strings.Contains(r.URL.Path, "/info/lfs/") {
			lfsHandler[0](w, r)
			return
		}
		if r.Method == "POST" && !strings.HasSuffix(r.URL.Path, "/git-upload-pack") {
			t.Error("read sync attempted a write endpoint")
			w.WriteHeader(403)
			return
		}
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/v1/cli/repos/"+readRepoID)
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	f.server = server
	cert := filepath.Join(root, "ca.pem")
	require.NoError(t, os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600))
	// Add only this fixture CA at Git's invocation boundary; production TLS
	// validation and transport policy still run unchanged.
	git, err := exec.LookPath("git")
	require.NoError(t, err)
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(bin, 0700))
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexec "+quote(git)+" -c "+quote("http.sslCAInfo="+cert)+" \"$@\"\n"), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")
	old := gitserver.DefaultHelperCommand()
	gitserver.SetHelperCommand(`!f() { printf 'username=ox\npassword=%s\n\n' "$SAGEOX_TOKEN"; }; f`)
	t.Cleanup(func() { gitserver.SetHelperCommand(old) })
	f.opts = ReadSyncOptions{RepoID: readRepoID, Endpoint: server.URL, ReadURL: server.URL + "/api/v1/cli/repos/" + readRepoID + "/ledger.git", Path: filepath.Join(root, "checkout")}
	return f
}

// Failure prevented: a warm read drops plans/history, loses local changes, or
// reuses a receipt after HEAD changed or the caller's token was revoked.
func TestReadSyncNativeLifecycle(t *testing.T) {
	f := newReadFixture(t)
	ctx := context.Background()
	first := ReadSync(ctx, f.opts)
	require.True(t, first.Ready, "%+v", first)
	require.Empty(t, first.ErrorClass)
	require.Equal(t, "full", first.History)
	require.NotNil(t, first.LastSuccessfulSync)
	require.FileExists(t, filepath.Join(f.opts.Path, "data/plans/plan/plan.md"))
	require.NoFileExists(t, filepath.Join(f.opts.Path, "assets/large.bin"))
	require.NoFileExists(t, filepath.Join(f.opts.Path, "data/murmurs/2000-01-01/00/old.json"))
	warm := ReadSync(ctx, f.opts)
	require.True(t, warm.Ready, "%+v", warm)
	require.True(t, warm.LastSuccessfulSync.After(*first.LastSuccessfulSync))
	require.Equal(t, first.Head, warm.Head)

	f.denied.Store(true)
	failed := ReadSync(ctx, f.opts)
	require.Equal(t, "denied", failed.ErrorClass)
	require.True(t, failed.Ready, "%+v", failed)
	require.Equal(t, warm.LastSuccessfulSync, failed.LastSuccessfulSync)
	t.Setenv("SAGEOX_TOKEN", "")
	local := CheckReadiness(ctx, f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
	require.True(t, local.Ready, "%+v", local)
	require.Equal(t, warm.LastSuccessfulSync, local.LastSuccessfulSync)
	f.denied.Store(false)
	t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")

	file := filepath.Join(f.opts.Path, "sessions/old/session.md")
	require.NoError(t, os.WriteFile(file, []byte("local work\n"), 0600))
	dirty := ReadSync(ctx, f.opts)
	require.False(t, dirty.Ready)
	require.Equal(t, "dirty", dirty.ErrorClass)
	content, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "local work\n", string(content))
	require.False(t, CheckReadiness(ctx, f.opts.Path, f.opts.RepoID, f.opts.Endpoint).Ready)
}

// Failure prevented: refreshing a narrow shallow cache reports full coverage
// while plans or older commits remain missing, or a valid empty tree is denied.
func TestReadSyncShallowUpgradeAndEmptyContent(t *testing.T) {
	f := newReadFixture(t)
	ctx := context.Background()
	readTestGit(t, f.source, "commit", "--allow-empty", "-m", "second")
	readTestGit(t, f.source, "push", f.bare, "main")
	transport, err := gitserver.NewReadTransport(f.opts.Endpoint, f.opts.RepoID, f.opts.ReadURL)
	require.NoError(t, err)
	_, err = runReadGit(ctx, transport, true, filepath.Dir(f.opts.Path), "clone", "--depth=1", "--sparse", "--", f.opts.ReadURL, f.opts.Path)
	require.NoError(t, err)
	_, err = runReadGit(ctx, transport, true, f.opts.Path, "sparse-checkout", "set", "--cone", "sessions")
	require.NoError(t, err)
	result := ReadSync(ctx, f.opts)
	require.True(t, result.Ready, "%+v", result)
	require.Equal(t, "full", result.History)
	require.Equal(t, "2", readTestGit(t, f.opts.Path, "rev-list", "--count", "HEAD"))
	require.FileExists(t, filepath.Join(f.opts.Path, "data/plans/plan/plan.md"))

	readTestGit(t, f.source, "rm", "-r", ".")
	readTestGit(t, f.source, "commit", "-m", "empty ledger content")
	readTestGit(t, f.source, "push", f.bare, "main")
	empty := ReadSync(ctx, f.opts)
	require.True(t, empty.Ready, "%+v", empty)
	require.True(t, empty.Coverage.Empty)
	require.Equal(t, 0, empty.Coverage.Files)
	require.NotEqual(t, result.Head, empty.Head)
	require.True(t, empty.LastSuccessfulSync.After(*result.LastSuccessfulSync))
}

// Failure prevented: ignored sessions/plans disappear when an upstream checkout
// introduces the same path; local history and all local files remain owned.
func TestReadSyncPreservesIgnoredFiles(t *testing.T) {
	f := newReadFixture(t)
	ctx := context.Background()
	require.True(t, ReadSync(ctx, f.opts).Ready)
	exclude := filepath.Join(f.opts.Path, ".git", "info", "exclude")
	require.NoError(t, os.WriteFile(exclude, []byte("sessions/local-plan.md\n"), 0600))
	file := filepath.Join(f.opts.Path, "sessions/local-plan.md")
	require.NoError(t, os.WriteFile(file, []byte("unpublished work"), 0600))
	result := ReadSync(ctx, f.opts)
	require.Equal(t, "dirty", result.ErrorClass)
	content, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "unpublished work", string(content))
}

// Failure prevented: killing mutation leaves an old receipt trusted, or local
// verification renews freshness for a revision never observed from the server.
func TestReadSyncRecoveryAndReaderLock(t *testing.T) {
	f := newReadFixture(t)
	ctx := context.Background()
	first := ReadSync(ctx, f.opts)
	require.True(t, first.Ready, "%+v", first)
	previous := loadReadReceipt(f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
	require.NotNil(t, previous)
	invalid := newReadResult(f.opts)
	require.NoError(t, publishReadReceipt(f.opts.Path, readReceipt{ReadSyncResult: invalid, ReadURL: f.opts.ReadURL}, previous))
	recovered := CheckReadiness(ctx, f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
	require.True(t, recovered.Ready)
	require.Equal(t, first.LastSuccessfulSync, recovered.LastSuccessfulSync)

	readTestGit(t, f.opts.Path, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "local revision")
	local := CheckReadiness(ctx, f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
	require.True(t, local.Ready, "%+v", local)
	require.Nil(t, local.LastSuccessfulSync, "unobserved HEAD cannot inherit freshness")
	require.Equal(t, "dirty", ReadSync(ctx, f.opts).ErrorClass)

	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- WithReadCheckout(ctx, f.opts.Path, f.opts.RepoID, f.opts.Endpoint, func(ReadSyncResult) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	deadline, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	require.Equal(t, "interrupted", CheckReadiness(deadline, f.opts.Path, f.opts.RepoID, f.opts.Endpoint).ErrorClass)
	close(release)
	require.NoError(t, <-done)
}

// Failure prevented: failed cold clones become published empty ledgers, or an
// existing reader's receipt can be used for another repo/credential context.
func TestReadSyncColdFailureAndIdentityIsolation(t *testing.T) {
	f := newReadFixture(t)
	f.denied.Store(true)
	result := ReadSync(context.Background(), f.opts)
	require.False(t, result.Ready)
	require.NoDirExists(t, f.opts.Path)
	f.denied.Store(false)
	require.True(t, ReadSync(context.Background(), f.opts).Ready)
	wrong := CheckReadiness(context.Background(), f.opts.Path, "repo_01936d5a-0001-7abc-8def-0123456789ab", f.opts.Endpoint)
	require.False(t, wrong.Ready)

	target := filepath.Join(t.TempDir(), "untouched")
	require.NoError(t, os.WriteFile(target, []byte("keep"), 0600))
	require.NoError(t, os.Remove(filepath.Join(f.opts.Path, readReceiptRelative)))
	require.NoError(t, os.Symlink(target, filepath.Join(f.opts.Path, readReceiptRelative)))
	require.False(t, ReadSync(context.Background(), f.opts).Ready)
	content, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "keep", string(content))
}

// Failure prevented: local hydration is mistaken for dirty content, or a raw
// pointer is advertised as complete data; bytes must verify independently.
func TestReadSyncLocalHydrationEvidence(t *testing.T) {
	f := newReadFixture(t)
	ctx := context.Background()
	require.True(t, ReadSync(ctx, f.opts).Ready)
	content := []byte("hydrated session body\n")
	pointer := lfs.FormatPointer("sha256:"+lfs.ComputeOID(content), int64(len(content)))
	path := filepath.Join(f.opts.Path, "sessions/old/session.md")
	require.NoError(t, os.WriteFile(path, []byte(pointer), 0600))
	readTestGit(t, f.opts.Path, "add", "sessions/old/session.md")
	readTestGit(t, f.opts.Path, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "pointer")
	missing := CheckReadiness(ctx, f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
	require.Equal(t, "missing_hydration", missing.ErrorClass)
	require.False(t, missing.Ready)
	require.NoError(t, os.WriteFile(path, content, 0600))
	ready := CheckReadiness(ctx, f.opts.Path, f.opts.RepoID, f.opts.Endpoint)
	require.True(t, ready.Ready, "%+v", ready)
	require.Nil(t, ready.LastSuccessfulSync)
	require.Equal(t, 1, ready.Hydration.Completed)
	transport, err := gitserver.NewReadTransport(f.opts.Endpoint, f.opts.RepoID, f.opts.ReadURL)
	require.NoError(t, err)
	require.NoError(t, gitutil.WithRepoLock(ctx, f.opts.Path, func() error { return dehydrateReadFiles(ctx, transport, f.opts.Path, "") }))
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, pointer, string(actual))
	backup, err := os.ReadFile(filepath.Join(f.opts.Path, ".sageox/cache/read-sync/objects", lfs.ComputeOID(content)))
	require.NoError(t, err)
	require.Equal(t, content, backup, "dehydration retains already downloaded bytes")
}
