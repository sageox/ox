//go:build !linux && !darwin && !windows

package daemon

import (
	"fmt"
	"net"
)

// peerUID is a stub on platforms where we haven't yet implemented kernel-
// mediated peer-credential lookup. The handleConnection caller is
// expected to log this and fail closed (refuse the connection). We
// intentionally do NOT silently accept on unknown platforms — that would
// regress the security posture ox-79cg established on Linux/Darwin.
func peerUID(conn net.Conn) (uint32, error) {
	_ = conn
	return 0, fmt.Errorf("peercred: not implemented on this platform")
}
