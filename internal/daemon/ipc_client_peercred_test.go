//go:build linux || darwin

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exercise real kernel credentials, not socket-file ownership or a mocked UID.
// This test needs root to launch the counterfeit server under a different UID.
func TestClientRejectsCrossUIDServer(t *testing.T) {
	if socket := os.Getenv("OX_TEST_CROSS_UID_SOCKET"); socket != "" {
		// Deliberately bypass listen's directory and socket protections, as an
		// attacker controlling the old shared runtime fallback could do.
		require.NoError(t, os.MkdirAll(filepath.Dir(socket), 0755))
		require.NoError(t, os.Chmod(filepath.Dir(socket), 0755))
		ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
		require.NoError(t, err)
		defer ln.Close()
		require.NoError(t, os.Chmod(socket, 0666))
		require.NoError(t, ln.SetDeadline(time.Now().Add(10*time.Second)))
		ready := os.NewFile(3, "ready")
		_, err = ready.Write([]byte{1})
		require.NoError(t, err)
		require.NoError(t, ready.Close())

		// Each client operation below makes one connection. The counterfeit
		// endpoint offers a successful ping but must receive no IPC bytes.
		for range 6 {
			conn, err := ln.Accept()
			require.NoError(t, err)
			require.NoError(t, conn.SetDeadline(time.Now().Add(2*time.Second)))
			_, _ = io.WriteString(conn, "{\"success\":true}\n")
			var b [1]byte
			n, err := conn.Read(b[:])
			assert.Zero(t, n, "unauthenticated server received IPC data")
			assert.Error(t, err)
			assert.False(t, os.IsTimeout(err), "rejected connection must be closed")
			conn.Close()
		}
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root to launch a server under another UID")
	}

	dir, err := os.MkdirTemp("/tmp", "oxpeer-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	require.NoError(t, os.Chmod(dir, 0755))
	runtimeDir := filepath.Join(dir, "runtime")
	require.NoError(t, os.Mkdir(runtimeDir, 0777))
	require.NoError(t, os.Chmod(runtimeDir, 0777))
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	socket := SocketPath()

	// The go test build directory may be private to root. Put an executable
	// copy in the fixture so the unprivileged subprocess can run it.
	exe, err := os.Executable()
	require.NoError(t, err)
	binary, err := os.ReadFile(exe)
	require.NoError(t, err)
	helper := filepath.Join(dir, "peer.test")
	require.NoError(t, os.WriteFile(helper, binary, 0755))
	require.NoError(t, os.Chmod(helper, 0755))
	readyRead, readyWrite, err := os.Pipe()
	require.NoError(t, err)
	defer readyRead.Close()
	defer readyWrite.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, helper, "-test.run=^TestClientRejectsCrossUIDServer$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "OX_TEST_CROSS_UID_SOCKET="+socket) // safe: re-executes only this IPC fixture with an isolated runtime directory
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	cmd.ExtraFiles = []*os.File{readyWrite}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	require.NoError(t, cmd.Start())
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	require.NoError(t, readyWrite.Close())
	require.NoError(t, readyRead.SetReadDeadline(time.Now().Add(5*time.Second)))
	var ready [1]byte
	_, err = io.ReadFull(readyRead, ready[:])
	require.NoError(t, err)

	client := &Client{socketPath: socket, timeout: time.Second}
	conn, err := client.Connect()
	if conn != nil {
		conn.Close()
	}
	assert.ErrorContains(t, err, "authenticate daemon peer")
	assert.ErrorContains(t, client.Ping(), "authenticate daemon peer")
	heartbeat, err := json.Marshal(HeartbeatPayload{
		Credentials: &HeartbeatCreds{Token: "test-git-token", AuthToken: "test-oauth-token"},
	})
	require.NoError(t, err)
	assert.ErrorContains(t, client.SendOneWay(Message{Type: MsgTypeHeartbeat, Payload: heartbeat}), "authenticate daemon peer")
	assert.ErrorContains(t, client.Murmur(MurmurPayload{Content: "private murmur"}), "authenticate daemon peer")
	assert.Nil(t, TryConnect())
	assert.Equal(t, DaemonStateStopped, GetState(), "an unauthenticated ping must not establish a running daemon")
	waitErr := cmd.Wait()
	require.NoError(t, waitErr, "%s", output.String())

	// Even root must not adopt a socket directory belonging to another UID.
	ln, err := listen(socket)
	if ln != nil {
		ln.Close()
	}
	assert.ErrorContains(t, err, "socket directory")
	info, err := os.Stat(filepath.Dir(socket))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm(), "foreign directory must not be chmod'd")
}
