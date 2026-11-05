//go:build !windows

package daemon

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// listen creates a Unix socket listener with owner-only permissions.
//
// Security (ox-79cg / ox-79cg-followup):
//   - Socket is created mode 0600 (umask 0077) so only the socket owner
//     can connect.
//   - Parent directory is chmod'd to 0700 EVEN IF IT ALREADY EXISTS.
//     MkdirAll only sets perms on directories it creates; if a previous
//     ox version or another process left the parent at 0755, we tighten
//     it now. Otherwise a directory traversal could expose the socket
//     name on a shared host.
//   - Layered with the peer-credential check in handleConnection: even
//     if a directory perm slips, the kernel-mediated UID check still
//     refuses connections from other users.
func listen(path string) (net.Listener, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("socket path must be absolute")
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, err
	}
	// Reject pre-planted symlinks and directories owned by another UID,
	// even when running with privileges that would allow chmod to succeed.
	info, err := os.Lstat(parent)
	if err != nil {
		return nil, fmt.Errorf("stat socket directory: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || stat.Uid != currentProcessUID() {
		return nil, fmt.Errorf("socket directory must be owned by the current user and not be a symlink")
	}
	// Explicit chmod handles the "directory already existed with looser
	// perms" case that MkdirAll silently leaves alone.
	if err := os.Chmod(parent, 0700); err != nil {
		return nil, fmt.Errorf("chmod socket directory: %w", err)
	}
	// remove existing socket file
	os.Remove(path)

	// set restrictive umask before creating socket (mode 0600)
	oldMask := syscall.Umask(0077)
	listener, err := net.Listen("unix", path)
	syscall.Umask(oldMask)
	if err != nil {
		return nil, err
	}

	// Go's *net.UnixListener.Close() unlinks the socket file by default.
	// That's wrong for ox: when a daemon is superseded by a replacement that
	// has already rebound the same path, our shutdown would delete the new
	// daemon's socket file, leaving it running but unreachable. Socket-file
	// lifetime is owned by Daemon.cleanup (which respects wasSuperseded);
	// listener shutdown must not unlink.
	if unixL, ok := listener.(*net.UnixListener); ok {
		unixL.SetUnlinkOnClose(false)
	}

	return listener, nil
}

// dial connects to a Unix socket and authenticates the server before any IPC data.
// Uses 5 second timeout to prevent indefinite hangs if daemon is stuck.
func dial(path string) (net.Conn, error) {
	conn, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		return nil, err
	}
	return authenticateDaemonPeer(conn, currentProcessUID())
}

// authenticateDaemonPeer closes conn unless its kernel-reported peer UID matches expectedUID.
func authenticateDaemonPeer(conn net.Conn, expectedUID uint32) (net.Conn, error) {
	uid, err := peerUID(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("authenticate daemon peer: %w", err)
	}
	if uid != expectedUID {
		_ = conn.Close()
		return nil, fmt.Errorf("authenticate daemon peer: UID mismatch")
	}
	return conn, nil
}

// cleanupSocket removes the socket file.
func cleanupSocket(path string) {
	os.Remove(path)
}
