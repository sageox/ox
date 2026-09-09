package gitserver

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const readTestRepoID = "repo_01936d5a-0000-7abc-8def-0123456789ab"

// Failure prevented: malformed discovery authorizes a different authority or
// repository before individual Git or LFS requests are checked.
func TestReadTransportRejectsUnsafeDiscovery(t *testing.T) {
	base := "https://sageox.ai/api/v1/cli/repos/" + readTestRepoID + "/ledger.git"
	for _, tc := range []struct {
		name     string
		endpoint string
		repoID   string
		readURL  string
	}{
		{"malformed endpoint", "https://%", readTestRepoID, base},
		{"insecure endpoint", "http://sageox.ai", readTestRepoID, base},
		{"missing authority", "https:", readTestRepoID, base},
		{"endpoint credentials", "https://ox:secret@sageox.ai", readTestRepoID, base},
		{"endpoint path", "https://sageox.ai/other", readTestRepoID, base},
		{"endpoint query", "https://sageox.ai?other=yes", readTestRepoID, base},
		{"endpoint fragment", "https://sageox.ai#other", readTestRepoID, base},
		{"invalid repo", "https://sageox.ai", "repo_invalid", base},
		{"noncanonical repo", "https://sageox.ai", strings.ToUpper(readTestRepoID), base},
		{"malformed discovery", "https://sageox.ai", readTestRepoID, "https://%"},
		{"foreign authority", "https://sageox.ai", readTestRepoID, strings.Replace(base, "sageox.ai", "foreign.example", 1)},
		{"discovery credentials", "https://sageox.ai", readTestRepoID, strings.Replace(base, "://", "://ox:secret@", 1)},
		{"encoded discovery path", "https://sageox.ai", readTestRepoID, strings.Replace(base, "ledger.git", "%6cedger.git", 1)},
		{"discovery query", "https://sageox.ai", readTestRepoID, base + "?"},
		{"discovery fragment", "https://sageox.ai", readTestRepoID, base + "#other"},
		{"wrong discovery path", "https://sageox.ai", readTestRepoID, base + "/info/refs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport, err := NewReadTransport(tc.endpoint, tc.repoID, tc.readURL)
			require.ErrorIs(t, err, ErrUnsafeReadTransport)
			require.Nil(t, transport)
			require.ErrorIs(t, ValidateReadRequestURL(tc.endpoint, tc.repoID, tc.readURL, base), ErrUnsafeReadTransport)
		})
	}
}

// Failure prevented: prefix/encoding tricks disclose the selected credential to
// another origin, resource, service, or upload endpoint.
func TestReadTransportCredentialScope(t *testing.T) {
	base := "https://sageox.ai/api/v1/cli/repos/" + readTestRepoID + "/ledger.git"
	for _, tc := range []struct {
		name string
		url  string
		ok   bool
	}{
		{"resource", base, true},
		{"refs", base + "/info/refs?service=git-upload-pack", true},
		{"refs without service", base + "/info/refs", true},
		{"pack", base + "/git-upload-pack", true},
		{"batch", base + "/info/lfs/objects/batch", true},
		{"object", base + "/info/lfs/objects/" + strings.Repeat("a", 64), true},
		{"http", strings.Replace(base, "https:", "http:", 1), false},
		{"git prefix", strings.Replace(base, "sageox.ai", "git.sageox.ai", 1), false},
		{"foreign port", strings.Replace(base, "sageox.ai", "sageox.ai:444", 1), false},
		{"foreign repo", strings.Replace(base, readTestRepoID, "repo_01936d5a-0001-7abc-8def-0123456789ab", 1), false},
		{"suffix", base + ".evil", false},
		{"traversal", base + "/../other/info/refs", false},
		{"encoded", strings.Replace(base, "/ledger.git", "/%6cedger.git", 1), false},
		{"userinfo", strings.Replace(base, "://", "://ox:secret@", 1), false},
		{"fragment", base + "#leak", false},
		{"query", base + "?secret=yes", false},
		{"malformed request", "https://%", false},
		{"empty query", base + "?", false},
		{"push refs", base + "/info/refs?service=git-receive-pack", false},
		{"push", base + "/git-receive-pack", false},
		{"lfs upload", base + "/info/lfs/objects/" + strings.Repeat("a", 64) + "/verify", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateReadRequestURL("https://sageox.ai", readTestRepoID, base, tc.url)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrUnsafeReadTransport)
			}
		})
	}
}

// Failure prevented: a checkout delegates Git configuration to another path,
// or a damaged config causes validation to skip the command's unsafe settings.
func TestReadTransportRejectsUnsafeConfigFiles(t *testing.T) {
	readURL := "https://sageox.ai/api/v1/cli/repos/" + readTestRepoID + "/ledger.git"
	transport, err := NewReadTransport("https://sageox.ai", readTestRepoID, readURL)
	require.NoError(t, err)
	for _, name := range []string{"git file", "git symlink", "missing config", "config symlink", "malformed config", "worktree override"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			gitDir := filepath.Join(dir, ".git")
			switch name {
			case "git file":
				require.NoError(t, os.WriteFile(gitDir, []byte("gitdir: /other"), 0o600))
			case "git symlink":
				require.NoError(t, os.Symlink(t.TempDir(), gitDir))
			default:
				require.NoError(t, os.Mkdir(gitDir, 0o700))
				configPath := filepath.Join(gitDir, "config")
				switch name {
				case "config symlink":
					target := filepath.Join(t.TempDir(), "config")
					require.NoError(t, os.WriteFile(target, nil, 0o600))
					require.NoError(t, os.Symlink(target, configPath))
				case "malformed config":
					require.NoError(t, os.WriteFile(configPath, []byte("[unterminated"), 0o600))
				case "worktree override":
					require.NoError(t, os.WriteFile(configPath, nil, 0o600))
					require.NoError(t, os.WriteFile(filepath.Join(gitDir, "config.worktree"), []byte("[credential]\nhelper = !echo stolen\n"), 0o600))
				}
			}
			cmd, err := transport.LocalCommand(context.Background(), dir, "status", "--porcelain")
			require.ErrorIs(t, err, ErrUnsafeReadTransport)
			require.Nil(t, cmd)
		})
	}
}

// Failure prevented: an unset credential falls back to ambient authentication,
// or cancellation racing with process startup/completion fails the operation.
func TestReadTransportMissingTokenAndCancellation(t *testing.T) {
	t.Setenv("SAGEOX_TOKEN", "")
	readURL := "https://sageox.ai/api/v1/cli/repos/" + readTestRepoID + "/ledger.git"
	transport, err := NewReadTransport("https://sageox.ai", readTestRepoID, readURL)
	require.NoError(t, err)
	dir := t.TempDir()
	cmd, err := transport.Command(context.Background(), dir, "ls-remote", readURL)
	require.ErrorIs(t, err, ErrReadTokenUnavailable)
	require.Nil(t, cmd)

	cmd, err = transport.LocalCommand(context.Background(), dir, "version")
	require.NoError(t, err)
	require.ErrorIs(t, cmd.Cancel(), os.ErrProcessDone)
	require.NoError(t, cmd.Run())
	require.ErrorIs(t, cmd.Cancel(), os.ErrProcessDone)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd, err = transport.LocalCommand(ctx, dir, "version")
	require.NoError(t, err)
	require.ErrorIs(t, cmd.Run(), context.Canceled)
}

// Failure prevented: missing Git or cancellation is misreported as a hostile
// checkout, causing callers to suggest repairing a valid configuration.
func TestReadTransportConfigInspectionExecutionErrors(t *testing.T) {
	readURL := "https://sageox.ai/api/v1/cli/repos/" + readTestRepoID + "/ledger.git"
	transport, err := NewReadTransport("https://sageox.ai", readTestRepoID, readURL)
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[core]\nbare = false\n"), 0o600))
	for _, name := range []string{"canceled", "deadline", "missing git"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			want := exec.ErrNotFound
			switch name {
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			case "deadline":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Unix(0, 0))
				defer cancel()
				want = context.DeadlineExceeded
			case "missing git":
				t.Setenv("PATH", t.TempDir())
			}
			cmd, err := transport.LocalCommand(ctx, dir, "status", "--porcelain")
			require.ErrorIs(t, err, want)
			require.NotErrorIs(t, err, ErrUnsafeReadTransport)
			require.Nil(t, cmd)
		})
	}
}

// Failure prevented: inherited helpers, URL rewrites, .netrc and trace options
// bypass the selected identity or leak it from a read operation.
func TestReadTransportRejectsUntrustedConfiguration(t *testing.T) {
	readURL := "https://sageox.ai/api/v1/cli/repos/" + readTestRepoID + "/ledger.git"
	transport, err := NewReadTransport("https://sageox.ai", readTestRepoID, readURL)
	require.NoError(t, err)
	t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")
	t.Setenv("GIT_TRACE", "1")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "credential.helper")
	t.Setenv("GIT_CONFIG_VALUE_0", "!false")
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o700))
	for _, cfg := range []string{
		"[credential]\nhelper = !echo stolen\n",
		"[credential \"https://sageox.ai\"]\nhelper = !echo stolen\n",
		"[url \"https://foreign.example/\"]\ninsteadOf = https://sageox.ai/\n",
		"[include]\npath = /tmp/foreign-config\n",
		"[http]\nfollowRedirects = true\n",
		"[http \"https://sageox.ai\"]\nextraHeader = Authorization: stolen\n",
		"[core]\nfsmonitor = evil-command\n",
		"[core]\nworktree = /tmp/another-checkout\n",
		"[filter \"anything\"]\nprocess = evil-command\n",
		"[remote \"origin\"]\nurl = https://foreign.example/ledger.git\n",
		"[remote \"other\"]\nurl = " + readURL + "\npromisor = true\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(cfg), 0o600))
		_, err := transport.Command(context.Background(), dir, "status", "--porcelain")
		require.ErrorIs(t, err, ErrUnsafeReadTransport, cfg)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[core]\nrepositoryformatversion = 0\n[remote \"origin\"]\nurl = "+readURL+"\n"), 0o600))
	cmd, err := transport.LocalCommand(context.Background(), dir, "status", "--porcelain")
	require.NoError(t, err)
	require.NotContains(t, strings.Join(cmd.Env, "\n"), "SAGEOX_TOKEN=")
	require.NotContains(t, strings.Join(cmd.Env, "\n"), "GIT_TRACE=")
	require.NotContains(t, strings.Join(cmd.Env, "\n"), "GIT_CONFIG_VALUE_0=")
	require.Contains(t, cmd.Args, "protocol.https.allow=never")
	require.Contains(t, cmd.Env, "GIT_NO_LAZY_FETCH=1")
}

// Failure prevented: safe-looking argv loses auth in lazy Git children, shallow
// history hides older content, or a reused transport keeps revoked credentials.
func TestReadTransportNativeGitCloneFetchHistoryAndLazyObjects(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git HTTP clone and lazy fetch")
	}
	if runtime.GOOS == "windows" {
		t.Skip("CGI git-http-backend fixture requires Unix process semantics")
	}
	ctx := context.Background()
	root := t.TempDir()
	runGit := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, readGitEnv(dir, "")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		return strings.TrimSpace(string(out))
	}
	source := filepath.Join(root, "source")
	require.NoError(t, os.Mkdir(source, 0o700))
	runGit(source, "init", "-b", "main")
	runGit(source, "config", "user.name", "Test")
	runGit(source, "config", "user.email", "test@example.invalid")
	require.NoError(t, os.WriteFile(filepath.Join(source, "history.txt"), []byte("older body\n"), 0o600))
	runGit(source, "add", "history.txt")
	runGit(source, "commit", "-m", "older")
	older := runGit(source, "rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(source, "history.txt"), []byte("newer body\n"), 0o600))
	runGit(source, "commit", "-am", "newer")
	origin := filepath.Join(root, "ledger.git")
	runGit(root, "clone", "--bare", "--", source, origin)
	runGit(origin, "config", "uploadpack.allowFilter", "true")
	runGit(origin, "config", "uploadpack.allowAnySHA1InWant", "true")
	gitExec := runGit(root, "--exec-path")
	backend := &cgi.Handler{Path: filepath.Join(gitExec, "git-http-backend"), Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	var accepted atomic.Value
	accepted.Store("oxt_test_1ljPfr")
	var authenticated atomic.Int64
	var redirect, stall atomic.Bool
	stallEntered, stallExited := make(chan struct{}), make(chan struct{})
	stopFixture := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if redirect.Load() {
			w.Header().Set("Location", "/must-not-follow")
			w.WriteHeader(http.StatusFound)
			return
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "ox" || password != accepted.Load().(string) {
			w.Header().Set("WWW-Authenticate", `Basic realm="ledger"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authenticated.Add(1)
		if stall.Load() {
			close(stallEntered)
			select {
			case <-r.Context().Done():
			case <-stopFixture:
			}
			close(stallExited)
			return
		}
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/v1/cli/repos/"+readTestRepoID)
		backend.ServeHTTP(w, r)
	}))
	defer func() {
		close(stopFixture)
		server.Close()
	}()
	certPath := filepath.Join(root, "ca.pem")
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600))
	t.Setenv("SAGEOX_TOKEN", accepted.Load().(string))
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	oldHelper := DefaultHelperCommand()
	SetHelperCommand(`!f() { printf 'username=ox\npassword=%s\n\n' "$SAGEOX_TOKEN"; }; f`)
	t.Cleanup(func() { SetHelperCommand(oldHelper) })
	readURL := server.URL + "/api/v1/cli/repos/" + readTestRepoID + "/ledger.git"
	transport, err := NewReadTransport(server.URL, readTestRepoID, readURL)
	require.NoError(t, err)
	readGit := func(dir string, args ...string) (string, error) {
		t.Helper()
		// Trust only this test server's certificate; production keeps normal TLS verification.
		cmd, err := transport.Command(ctx, dir, append([]string{"-c", "http.sslCAInfo=" + certPath}, args...)...)
		if err != nil {
			return "", err
		}
		require.NotContains(t, strings.Join(cmd.Args, " "), os.Getenv("SAGEOX_TOKEN"))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	clone := filepath.Join(root, "checkout")
	out, err := readGit(root, "clone", "--filter=blob:none", "--no-checkout", "--", readURL, clone)
	require.NoError(t, err, out)
	before := authenticated.Load()
	out, err = readGit(clone, "show", older+":history.txt")
	require.NoError(t, err, out)
	require.Equal(t, "older body\n", out)
	require.Greater(t, authenticated.Load(), before, "historical blob must be fetched through an authenticated child")
	require.Equal(t, "2", runGit(clone, "rev-list", "--count", "HEAD"))
	// An owned shallow cache uses a single-branch refspec. Establish full
	// history without rewriting its remote, deleting files or replacing it.
	shallow := filepath.Join(root, "shallow")
	out, err = readGit(root, "clone", "--depth=1", "--no-checkout", "--", readURL, shallow)
	require.NoError(t, err, out)
	require.Equal(t, "true", runGit(shallow, "rev-parse", "--is-shallow-repository"))
	out, err = readGit(shallow, "fetch", "--unshallow", "--", readURL, "+refs/heads/*:refs/remotes/origin/*")
	require.NoError(t, err, out)
	require.Equal(t, "false", runGit(shallow, "rev-parse", "--is-shallow-repository"))
	require.Equal(t, "2", runGit(shallow, "rev-list", "--count", "HEAD"))
	configBytes, err := os.ReadFile(filepath.Join(clone, ".git", "config"))
	require.NoError(t, err)
	require.NotContains(t, string(configBytes), os.Getenv("SAGEOX_TOKEN"))
	accepted.Store("oxt_rotated_1lKvCA")
	out, err = readGit(clone, "fetch", "origin")
	require.Error(t, err, out)
	t.Setenv("SAGEOX_TOKEN", "oxt_rotated_1lKvCA")
	out, err = readGit(clone, "fetch", "origin")
	require.NoError(t, err, out)
	redirect.Store(true)
	out, err = readGit(clone, "fetch", "origin")
	require.Error(t, err, "Git must refuse same-origin redirects: %s", out)
	redirect.Store(false)
	stall.Store(true)
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd, err := transport.Command(cancelCtx, clone, "-c", "http.sslCAInfo="+certPath, "fetch", "origin")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()
	select {
	case <-stallEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("Git request never reached the stalled HTTP fixture")
	}
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Git command survived cancellation")
	}
	select {
	case <-stallExited:
	case <-time.After(5 * time.Second):
		t.Fatal("Git HTTP child survived cancellation and retained its connection")
	}
}
