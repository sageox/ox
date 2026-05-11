//go:build windows

package daemon

import (
	"fmt"
	"net"
)

// peerUID is a stub on Windows. The Windows IPC transport uses named pipes
// (see ipc_windows.go); peer authentication there is enforced by ACLs on
// the pipe object, not by an after-accept syscall. Returning an error
// here causes handleConnection to fail closed — when Windows support is
// added with proper named-pipe ACLs, this can be implemented as a no-op
// or wired into the pipe's ACL check.
func peerUID(conn net.Conn) (uint32, error) {
	_ = conn
	return 0, fmt.Errorf("peercred: not implemented on Windows; rely on named-pipe ACLs")
}
