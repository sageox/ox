package gitserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stallingGitServer serves a bare repo over smart HTTP, sends the first
// stallAfter bytes of each response, then holds the connection open, the way
// a dead link looks to git mid-download.
type stallingGitServer struct {
	URL       string
	streaming chan struct{} // closed once a response has started stalling
}

func serveStallingRepo(t *testing.T, blobBytes, stallAfter int) *stallingGitServer {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("CGI Git HTTP fixture and SIGINT need Unix process semantics")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is not available")
	}

	root := t.TempDir()
	bare := filepath.Join(root, "ledger.git")
	work := filepath.Join(root, "work")
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	runGit(root, "init", "--quiet", "--bare", "--initial-branch=main", bare)
	runGit(root, "init", "--quiet", "--initial-branch=main", work)
	blob := make([]byte, blobBytes) // random, so the pack cannot compress below stallAfter
	_, err := rand.Read(blob)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(work, "history.bin"), blob, 0o600))
	runGit(work, "add", ".")
	runGit(work, "commit", "--quiet", "-m", "seed")
	runGit(work, "push", "--quiet", bare, "HEAD:main")

	gitExec, err := exec.Command("git", "--exec-path").Output()
	require.NoError(t, err)
	backend := &cgi.Handler{
		Path: filepath.Join(strings.TrimSpace(string(gitExec)), "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	}

	srv := &stallingGitServer{streaming: make(chan struct{})}
	var once sync.Once
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend.ServeHTTP(&stallingWriter{
			ResponseWriter: w, ctx: r.Context(), release: release, stallAfter: stallAfter,
			onStall: func() { once.Do(func() { close(srv.streaming) }) },
		}, r)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) }) // runs first: unblock stalled handlers
	srv.URL = server.URL + "/ledger.git"
	return srv
}

type stallingWriter struct {
	http.ResponseWriter
	ctx        context.Context
	release    <-chan struct{}
	stallAfter int
	onStall    func()
	sent       int
}

func (s *stallingWriter) Write(b []byte) (int, error) {
	if s.sent+len(b) <= s.stallAfter {
		n, err := s.ResponseWriter.Write(b)
		s.sent += n
		return n, err
	}
	n, err := s.ResponseWriter.Write(b[:s.stallAfter-s.sent])
	s.sent += n
	if err != nil {
		return n, err
	}
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
	s.onStall()
	select {
	case <-s.ctx.Done():
	case <-s.release:
	}
	return n, http.ErrHandlerTimeout
}

// TestCloneRepo_CanceledCloneLeavesNothingBehind verifies a clone whose
// context ends mid-download leaves nothing at the target path.
// Failure prevented: git killed outright left a half-written .git whose HEAD
// points at refs/heads/.invalid, which ox then took for a real clone, failing
// every later commit with "cannot lock ref HEAD".
func TestCloneRepo_CanceledCloneLeavesNothingBehind(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git clone over HTTP")
	}
	for _, name := range []string{"direct", "child ignores interrupt"} {
		t.Run(name, func(t *testing.T) {
			srv := serveStallingRepo(t, 512<<10, 64<<10)
			target := filepath.Join(t.TempDir(), "ledger")
			if name == "child ignores interrupt" {
				git, err := exec.LookPath("git")
				require.NoError(t, err)
				bin := t.TempDir()
				// Like a transport helper that does not exit on SIGINT, this
				// child keeps downloading when only its parent is interrupted.
				script := "#!/bin/sh\ntrap '' INT\n\"$CLONE_TEST_GIT\" \"$@\" &\nwait\n"
				require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755))
				t.Setenv("CLONE_TEST_GIT", git)
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var output bytes.Buffer
			done := make(chan error, 1)
			go func() {
				done <- cloneRepo(ctx, srv.URL, target, nil, &CheckoutOptions{Quiet: true, Output: &output})
			}()

			select {
			case <-srv.streaming:
			case err := <-done:
				t.Fatalf("the clone ended before the download stalled: %v; output: %s", err, output.String())
			case <-time.After(30 * time.Second):
				t.Fatal("the download never started")
			}
			require.DirExists(t, filepath.Join(target, ".git"), "git writes the clone's .git before the pack arrives")
			cancel()

			select {
			case err := <-done:
				require.ErrorIs(t, err, ErrCloneFailed)
			case <-time.After(10 * time.Second):
				t.Fatal("the canceled clone did not exit")
			}
			assert.NoDirExists(t, target, "an interrupted clone must remove its half-written repo")
		})
	}
}

// TestCloneRepo_OutputReceivesGitErrors verifies git's error text goes to
// CheckoutOptions.Output, so a caller can read why a clone failed.
// Failure prevented: doctor could not tell a stalled download from any other
// failure, because git's message went straight to the terminal.
func TestCloneRepo_OutputReceivesGitErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git clone over HTTP")
	}
	srv := serveStallingRepo(t, 1<<10, 1<<20)
	var output bytes.Buffer
	err := cloneRepo(context.Background(), strings.TrimSuffix(srv.URL, "ledger.git")+"missing.git",
		filepath.Join(t.TempDir(), "missing"), nil, &CheckoutOptions{Quiet: true, Output: &output})

	require.ErrorIs(t, err, ErrCloneFailed)
	assert.Contains(t, output.String(), "not found", "git's own reason must reach Output")
}
