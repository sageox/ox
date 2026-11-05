package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const heartbeatProjectAPI = "https://api.northwind.example"

// heartbeatProject is a logged-in repository whose config names
// heartbeatProjectAPI, with a fake daemon socket that captures heartbeats.
func heartbeatProject(t *testing.T) (string, <-chan daemon.HeartbeatPayload) {
	t.Helper()
	runtime, err := os.MkdirTemp("", "oxhb") // short: unix socket path limit
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(runtime) })
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(endpoint.EnvVar, "")
	t.Setenv("SAGEOX_TOKEN", "")
	prevDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	prevForce := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() {
		gitserver.TestSetConfigDirOverride(prevDir)
		gitserver.TestSetForceFileStorage(prevForce)
	})

	repo := t.TempDir()
	require.NoError(t, config.SaveProjectConfig(repo, &config.ProjectConfig{Endpoint: heartbeatProjectAPI}))
	t.Chdir(repo)
	require.NoError(t, auth.SaveTokenForEndpoint(heartbeatProjectAPI, &auth.StoredToken{
		AccessToken: "oauth-token", ExpiresAt: time.Now().Add(time.Hour), TokenType: "Bearer",
	}))
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(heartbeatProjectAPI, gitserver.GitCredentials{
		Token: "git-pat", ServerURL: "https://git.northwind.example", ExpiresAt: time.Now().Add(time.Hour),
	}))

	sock := daemon.SocketPath()
	require.NoError(t, os.MkdirAll(filepath.Dir(sock), 0o700))
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan daemon.HeartbeatPayload, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(conn).ReadBytes('\n')
			_ = conn.Close()
			var msg daemon.Message
			var payload daemon.HeartbeatPayload
			if json.Unmarshal(line, &msg) == nil && msg.Type == daemon.MsgTypeHeartbeat && json.Unmarshal(msg.Payload, &payload) == nil {
				got <- payload
			}
		}
	}()
	return repo, got
}

func receivedHeartbeat(t *testing.T, got <-chan daemon.HeartbeatPayload) daemon.HeartbeatPayload {
	t.Helper()
	DrainHeartbeats()
	select {
	case p := <-got:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no heartbeat reached the daemon socket")
		return daemon.HeartbeatPayload{}
	}
}

// The daemon releases a heartbeat's OAuth token only to the origin it was
// loaded for, so the CLI must say which endpoint that is.
func TestHeartbeat_TokenNamesTheEndpointItWasLoadedFor(t *testing.T) {
	repo, got := heartbeatProject(t)

	Heartbeat(repo, nil, "")

	creds := receivedHeartbeat(t, got).Credentials
	require.NotNil(t, creds)
	assert.Equal(t, "oauth-token", creds.AuthToken)
	assert.Equal(t, heartbeatProjectAPI, creds.AuthEndpoint)
}

func TestHeartbeatWithCreds_TokenNamesTheEndpointItWasLoadedFor(t *testing.T) {
	repo, got := heartbeatProject(t)

	HeartbeatWithCreds(repo, nil, "", &gitserver.GitCredentials{
		Token: "git-pat", ServerURL: "https://git.northwind.example", ExpiresAt: time.Now().Add(time.Hour),
	})

	creds := receivedHeartbeat(t, got).Credentials
	require.NotNil(t, creds)
	assert.Equal(t, "oauth-token", creds.AuthToken)
	assert.Equal(t, heartbeatProjectAPI, creds.AuthEndpoint)
}
