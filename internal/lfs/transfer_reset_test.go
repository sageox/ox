package lfs

import (
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A server that closes the socket while the PUT body is still being written
// surfaces as net.ErrClosed on Linux (and ECONNRESET on macOS). Both are the same
// transport fault and must earn the one retry; an HTTP verdict must not.
func TestIsConnectionReset_ClosedConnectionIsRetried(t *testing.T) {
	assert.True(t, isConnectionReset(fmt.Errorf("upload failed: %w", net.ErrClosed)))
	assert.False(t, isConnectionReset(errors.New("upload returned HTTP 403")))
}
