package testutil

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// AwaitUnixSocket polls until a unix socket accepts connections.
// Completes immediately when the socket is ready (typically <5ms),
// times out with a clear message if it never becomes available.
func AwaitUnixSocket(t *testing.T, socketPath string) {
	t.Helper()
	require.Eventually(t, func() bool {
		conn, err := net.Dial("unix", socketPath)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}, 5*time.Second, 5*time.Millisecond,
		"unix socket never became ready: %s", socketPath)
}
