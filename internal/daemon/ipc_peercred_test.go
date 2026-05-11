//go:build !windows

package daemon

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPeerUID_SameProcessReturnsCallerUID verifies that the peer-cred
// lookup returns the test process's own UID when the test connects to
// itself. This is the load-bearing baseline: any other behavior would
// either (a) silently allow cross-user connections or (b) reject the
// daemon owner's legitimate connections.
func TestPeerUID_SameProcessReturnsCallerUID(t *testing.T) {
	// macOS has a 104-char unix socket path limit; t.TempDir() returns
	// deep paths in /var/folders/.../TestName/NNN which routinely
	// overflow. Use os.MkdirTemp under /tmp directly for a short prefix.
	dir, err := os.MkdirTemp("/tmp", "oxipc-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "t.sock")

	ln, lerr := net.Listen("unix", sock)
	require.NoError(t, lerr)
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()

	client, err := net.Dial("unix", sock)
	require.NoError(t, err)
	defer client.Close()

	server := <-accepted
	defer server.Close()

	uid, err := peerUID(server)
	require.NoError(t, err)
	assert.Equal(t, uint32(os.Geteuid()), uid,
		"peer UID over a same-process connection must match the caller's effective UID")
}

// TestPeerUID_RejectsNonUnixConn verifies the fail-closed shape on the
// wrong connection type. We pass a TCP conn to demonstrate the type
// assertion fires.
func TestPeerUID_RejectsNonUnixConn(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer client.Close()

	server := <-accepted
	defer server.Close()

	_, err = peerUID(server)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a unix socket")
}

// TestListen_ParentDirIs0700 verifies the parent-directory chmod fires
// even when the directory already exists with looser permissions —
// addressing the post-MkdirAll "perms-already-set-by-earlier-writer"
// case called out in ox-79cg.
func TestListen_ParentDirIs0700(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "oxipc-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	parent := filepath.Join(dir, "p")
	// pre-create with loose perms — this is the scenario the explicit
	// chmod is supposed to tighten.
	require.NoError(t, os.MkdirAll(parent, 0755))

	sock := filepath.Join(parent, "t.sock")
	ln, lerr := listen(sock)
	require.NoError(t, lerr)
	defer ln.Close()

	info, err := os.Stat(parent)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm(),
		"parent must be 0700 even when it pre-existed with looser perms")
}
