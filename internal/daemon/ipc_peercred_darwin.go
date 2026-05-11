//go:build darwin

package daemon

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// peerUID returns the UID of the process at the other end of a Unix
// socket on macOS. macOS exposes LOCAL_PEERCRED via getsockopt(SOL_LOCAL),
// returning an Xucred struct. See ipc_peercred_linux.go for the Linux
// equivalent and ox-79cg for the threat model.
func peerUID(conn net.Conn) (uint32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("peercred: not a unix socket connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("peercred: SyscallConn: %w", err)
	}
	var (
		uid    uint32
		gotErr error
	)
	ctlErr := raw.Control(func(fd uintptr) {
		xu, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil {
			gotErr = err
			return
		}
		uid = xu.Uid
	})
	if ctlErr != nil {
		return 0, fmt.Errorf("peercred: control: %w", ctlErr)
	}
	if gotErr != nil {
		return 0, fmt.Errorf("peercred: GetsockoptXucred: %w", gotErr)
	}
	return uid, nil
}
