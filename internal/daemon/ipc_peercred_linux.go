//go:build linux

package daemon

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// peerUID returns the UID of the process that opened the other end of the
// connection. Linux exposes this via SO_PEERCRED — kernel-mediated, the
// peer cannot lie about it. Returns an error if the connection isn't a
// Unix socket or the syscall fails.
//
// Per ox-79cg: required so the daemon refuses connections from any local
// user that isn't the daemon owner. Without this, any process on the box
// (rogue browser file:// fetch, another user on a shared dev host, a
// compromised IDE extension) can drive the daemon's IPC handlers.
func peerUID(conn net.Conn) (uint32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		// Non-Unix connection (shouldn't happen for our listener) — fail
		// closed: caller is expected to reject in this case.
		return 0, fmt.Errorf("peercred: not a unix socket connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("peercred: SyscallConn: %w", err)
	}
	var (
		ucred  *unix.Ucred
		gotErr error
	)
	ctlErr := raw.Control(func(fd uintptr) {
		ucred, gotErr = unix.GetsockoptUcred(int(fd),
			unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if ctlErr != nil {
		return 0, fmt.Errorf("peercred: control: %w", ctlErr)
	}
	if gotErr != nil {
		return 0, fmt.Errorf("peercred: getsockopt: %w", gotErr)
	}
	return ucred.Uid, nil
}
