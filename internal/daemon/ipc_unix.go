//go:build !windows

package daemon

import (
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
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, err
	}
	// Explicit chmod handles the "directory already existed with looser
	// perms" case that MkdirAll silently leaves alone.
	if err := os.Chmod(parent, 0700); err != nil {
		// non-fatal — the socket itself is still 0600 and the peer-cred
		// check defends the IPC handlers regardless. Best-effort to log.
		_ = err
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

// dial connects to a Unix socket with a timeout.
// Uses 5 second timeout to prevent indefinite hangs if daemon is stuck.
func dial(path string) (net.Conn, error) {
	return net.DialTimeout("unix", path, 5*time.Second)
}

// cleanupSocket removes the socket file.
func cleanupSocket(path string) {
	os.Remove(path)
}
