//go:build windows

package daemon

import (
	"net"
)

// peerUID is a no-op on Windows. The Windows IPC transport uses named
// pipes (see ipc_windows.go); same-user enforcement is provided by the
// pipe's ACL — only processes running as the owning user can connect.
// Returning an error here would make handleConnection reject every
// connection, breaking IPC on Windows. The ACL is the trust boundary;
// this function intentionally trusts it.
//
// DisablePeerCredForTesting still gates the call site identically on
// every platform — its semantics are unchanged.
func peerUID(conn net.Conn) (uint32, error) {
	_ = conn
	return 0, nil
}
