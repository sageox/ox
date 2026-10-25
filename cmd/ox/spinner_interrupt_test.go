//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cancellation must survive callers that otherwise retry, downgrade errors, or
// read unfinished results. Exercise actual commands with blocked local I/O.
func TestSpinnerCLIInterrupt(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds the CLI and exercises terminal cancellation")
	}
	oxBin := testguard.BuildOxBinary(t, repoPath("..", ".."))

	t.Run("recap does not restart the interrupted read", func(t *testing.T) {
		repo, ledger := newRecapCmdFixture(t)
		sessionDir := filepath.Join(ledger, "sessions", "pending")
		require.NoError(t, os.MkdirAll(sessionDir, 0o700))
		// An unreadable or missing file returns quickly; a FIFO holds the real
		// read until the process exits, exposing a fallback that retries it.
		require.NoError(t, syscall.Mkfifo(filepath.Join(sessionDir, "meta.json"), 0o600))
		output := runInterruptedCLI(t, oxBin, repo, nil,
			[]string{"recap", "--json=false", "--user", "test"}, "Reading your ledger")
		assert.NotContains(t, output, "SageOx recap")
	})

	t.Run("login preserves credential sync interruption", func(t *testing.T) {
		server := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			var response any
			switch r.URL.Path {
			case auth.DeviceCodeEndpoint:
				response = auth.DeviceCodeResponse{
					DeviceCode: "test-device", UserCode: "TEST-CODE",
					VerificationURI: "http://localhost/verify", ExpiresIn: 60, Interval: 1,
				}
			case auth.DeviceTokenEndpoint:
				response = auth.TokenResponse{AccessToken: "test-access", RefreshToken: "test-refresh", TokenType: "Bearer", ExpiresIn: 3600}
			case "/api/v1/cli/auth/token":
				response = auth.JWTExchangeResponse{AccessToken: "test-jwt", TokenType: "Bearer", ExpiresIn: 3600}
			case auth.UserInfoEndpoint:
				response = auth.UserInfo{UserID: "test-user", Email: "test@example.com", Name: "Test User"}
			case "/api/v1/cli/repos":
				<-r.Context().Done()
				return
			default:
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(response)
		}))
		defer server.Close()
		output := runInterruptedCLI(t, oxBin, t.TempDir(), []string{
			"SKIP_BROWSER=1", "OX_TRUST_ENDPOINT=1", "SAGEOX_ENDPOINT=" + server.URL,
		}, []string{"login", "--endpoint", server.URL}, "Syncing git credentials...")
		assert.NotContains(t, output, "Git credentials synced")
		assert.NotContains(t, output, "Git credentials sync failed - this won't affect your login.")
	})

	t.Run("doctor stops repairs and keeps completion markers", func(t *testing.T) {
		server := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case auth.IntrospectEndpoint:
				_ = json.NewEncoder(w).Encode(auth.IntrospectResult{Active: true, PrincipalKind: auth.PrincipalKindUser})
			case "/api/v1/cli/repos":
				<-r.Context().Done()
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()
		repo, configDir := t.TempDir(), t.TempDir()
		runGit(t, repo, "init")
		project := config.GetDefaultProjectConfig()
		project.Endpoint = server.URL
		require.NoError(t, config.SaveProjectConfig(repo, project))
		old := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
		require.NoError(t, os.Chtimes(filepath.Join(repo, ".sageox", "config.json"), old, old))
		t.Setenv("OX_XDG_DISABLE", "")
		t.Setenv("XDG_CONFIG_HOME", configDir)
		t.Setenv("OX_GIT_CREDENTIALS_FILE", "")
		previousStorage := gitserver.TestSetForceFileStorage(true)
		t.Cleanup(func() { gitserver.TestSetForceFileStorage(previousStorage) })
		previousDir := gitserver.TestSetConfigDirOverride(configDir)
		t.Cleanup(func() { gitserver.TestSetConfigDirOverride(previousDir) })
		require.NoError(t, auth.SaveTokenForEndpoint(server.URL, &auth.StoredToken{
			AccessToken: "test-access", ExpiresAt: time.Now().Add(time.Hour),
			UserInfo: auth.UserInfo{UserID: "test-user", Email: "test@example.com"},
		}))
		require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, gitserver.GitCredentials{
			Token: "test-git", ExpiresAt: time.Now().Add(time.Hour),
		}))
		authPath, err := auth.GetAuthFilePath()
		require.NoError(t, err)
		require.NoError(t, os.Chmod(authPath, 0o644)) // repaired by a later automatic check
		health := &config.Health{LastDoctorAt: old, LastDoctorFixAt: old}
		require.NoError(t, config.SaveHealth(repo, health))
		require.NoError(t, doctor.SetNeedsDoctorAgent(repo))

		output := runInterruptedCLI(t, oxBin, repo, []string{
			"XDG_CONFIG_HOME=" + configDir, "SAGEOX_ENDPOINT=" + server.URL,
		}, []string{"doctor", "--fix-slug", "git-repo-paths"}, "Fetching repos from cloud...")
		info, err := os.Stat(authPath)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "must not run later permission repairs")
		after, err := config.LoadHealth(repo)
		require.NoError(t, err)
		assert.Equal(t, health, after, "must not record an interrupted doctor run as completed")
		assert.True(t, doctor.NeedsDoctorAgent(repo), "must not clear follow-up markers")
		assert.NotContains(t, output, "Checking Ledger Git Health")
	})

	for _, tt := range []struct {
		name      string
		args      []string
		message   string
		blockType string
	}{
		{name: "workspace sync", args: []string{"sync"}, message: "Syncing via daemon...", blockType: daemon.MsgTypeSync},
		{name: "one team", args: []string{"sync", "--team", "test-team"}, message: "Syncing team test-team via daemon...", blockType: daemon.MsgTypeTeamSync},
		{name: "all teams", args: []string{"sync", "--all-teams"}, message: "Syncing team contexts via daemon...", blockType: daemon.MsgTypeTeamSync},
		{name: "export team sync", args: []string{"export", "--sync"}, message: "Syncing team contexts via daemon...", blockType: daemon.MsgTypeTeamSync},
		{name: "export ledger sync", args: []string{"export", "--sync"}, message: "Syncing via daemon...", blockType: daemon.MsgTypeSync},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			runGit(t, repo, "init")
			// Keep the socket under macOS's Unix socket path length limit.
			runtimeDir, err := os.MkdirTemp("/tmp", "ox-interrupt-")
			require.NoError(t, err)
			t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
			t.Setenv("OX_XDG_DISABLE", "")
			t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
			release := make(chan struct{})
			sock := startFakeDaemon(t, func(msg daemon.Message) daemon.Response {
				if msg.Type == tt.blockType {
					<-release
				}
				if msg.Type == daemon.MsgTypeTeamSync {
					return daemon.Response{Success: true, Data: json.RawMessage(`[{"team_id":"test-team","status":"synced"}]`)}
				}
				return daemon.Response{Success: true, Data: json.RawMessage(`{}`)}
			})
			t.Cleanup(func() { close(release) })
			socketPath := paths.DaemonSocketFile(daemon.RepoBasedWorkspaceID(repo))
			require.True(t, strings.HasPrefix(socketPath, runtimeDir+string(os.PathSeparator)), "socket must stay inside the fixture: %s", socketPath)
			require.NoError(t, os.MkdirAll(filepath.Dir(socketPath), 0o700))
			require.NoError(t, os.Symlink(sock, socketPath))
			output := runInterruptedCLI(t, oxBin, repo, []string{"XDG_RUNTIME_DIR=" + runtimeDir}, tt.args, tt.message)
			assert.NotContains(t, output, "Synced via daemon")
			if tt.blockType == daemon.MsgTypeTeamSync {
				assert.NotContains(t, output, "synced via daemon")
				assert.NotContains(t, output, "Syncing via daemon...")
			}
			assert.NotContains(t, output, "older version")
			assert.NotContains(t, output, "Sync failed")
			assert.NotContains(t, output, "Team sync failed")
			assert.NotContains(t, output, "Sync incomplete")
			assert.NotContains(t, output, "Take your data with you")
		})
	}
}

// runInterruptedCLI sends Ctrl+C only after the named spinner is visible, so a
// passing case proves interruption of pending work rather than an early failure.
func runInterruptedCLI(t *testing.T, binary, dir string, env, args []string, message string) string {
	t.Helper()
	scratch := t.TempDir()
	isolated := []string{
		"HOME=" + scratch,
		"TERM=xterm-256color", "NO_COLOR=1", "OX_XDG_ENABLE=1",
		"SAGEOX_ENDPOINT=http://127.0.0.1:1",
		"HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1",
		"NO_PROXY=localhost,127.0.0.1",
	}
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		isolated = append(isolated, key+"="+filepath.Join(scratch, key))
	}
	isolated = append(isolated, env...)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := testguard.OxCmdContext(t, ctx, binary, dir, isolated, args...)
	ptmx, tty, err := pty.Open()
	require.NoError(t, err)
	defer ptmx.Close()
	defer tty.Close()
	require.NoError(t, pty.Setsize(ptmx, &pty.Winsize{Rows: 30, Cols: 120}))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	require.NoError(t, cmd.Start())
	require.NoError(t, tty.Close())

	var terminalOutput bytes.Buffer
	readDone := make(chan error, 1)
	interrupted := false
	go func() {
		buf := make([]byte, 4096)
		answered := false
		for {
			n, readErr := ptmx.Read(buf)
			terminalOutput.Write(buf[:n])
			if !answered && bytes.Contains(terminalOutput.Bytes(), []byte(ansi.RequestPrimaryDeviceAttributes)) {
				_, _ = ptmx.Write([]byte("\x1b]11;rgb:0000/0000/0000\a\x1b[?1;2c"))
				answered = true
			}
			if !interrupted && bytes.Contains(terminalOutput.Bytes(), []byte(message)) {
				_, _ = ptmx.Write([]byte{3})
				interrupted = true
			}
			if readErr != nil {
				readDone <- readErr
				return
			}
		}
	}()
	err = cmd.Wait()
	readErr := <-readDone
	output := ansi.Strip(terminalOutput.String())
	require.True(t, errors.Is(readErr, io.EOF) || errors.Is(readErr, syscall.EIO), "terminal: %v", readErr)
	require.NoError(t, ctx.Err(), "CLI did not exit promptly: %s", output)
	require.True(t, interrupted, "never reached the spinner: %s", output)
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, "output: %s", output)
	require.Equal(t, 130, exitErr.ExitCode(), "output: %s", output)
	assert.Contains(t, output, "Interrupted.")
	assert.Contains(t, terminalOutput.String(), "\x1b[?25h", "terminal cursor must be restored")
	return output
}
