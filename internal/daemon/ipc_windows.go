//go:build windows

package daemon

import (
	"fmt"
	"net"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// listen creates a Windows named pipe listener.
// SECURITY: Pipe is created with SDDL that restricts access to current user only.
// This prevents other users on the same machine from connecting to the daemon.
func listen(path string) (net.Listener, error) {
	pipePath := `\\.\pipe\` + pipeName(path)

	// get current user's SID for SDDL
	sddl, err := currentUserSDDL()
	if err != nil {
		return nil, fmt.Errorf("get user SDDL: %w", err)
	}

	cfg := &winio.PipeConfig{
		SecurityDescriptor: sddl,
		MessageMode:        false,
		InputBufferSize:    4096,
		OutputBufferSize:   4096,
	}
	return winio.ListenPipe(pipePath, cfg)
}

// currentUserSDDL returns an SDDL string that grants full access only to the current user.
// Format: D:P(A;;GA;;;SID) = DACL with full access for the specified SID
func currentUserSDDL() (string, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("get token user: %w", err)
	}
	sidStr := user.User.Sid.String()
	// D:P = DACL present, protected (no inheritance)
	// A = Allow
	// GA = Generic All (full access)
	// SID = user's SID
	return fmt.Sprintf("D:P(A;;GA;;;%s)", sidStr), nil
}

// dial connects to a Windows named pipe with a timeout.
// Uses 5 second timeout to prevent indefinite hangs if daemon is stuck.
func dial(path string) (net.Conn, error) {
	pipePath := `\\.\pipe\` + pipeName(path)
	timeout := 5 * time.Second
	return winio.DialPipe(pipePath, &timeout)
}

// cleanupSocket is a no-op on Windows (pipes are cleaned up automatically).
func cleanupSocket(path string) {
	// no-op: Windows named pipes are automatically cleaned up when closed
}
