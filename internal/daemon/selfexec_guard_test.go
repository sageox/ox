//go:build !windows

package daemon

import (
	"testing"

	"github.com/sageox/ox/internal/selfexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnsureDaemonImpl_RefusesToSpawnUnderTest pins the guard to this call site.
//
// ensureDaemonImpl re-executes os.Executable() as `<exe> daemon start
// --foreground`. Under `go test` that path is the compiled test binary, and
// Go's flag package drops the subcommand, so the child re-runs the whole suite
// with no -test.timeout and spawns further generations. selfexec.Path refuses,
// and this test fails if that wiring is ever undone here.
func TestEnsureDaemonImpl_RefusesToSpawnUnderTest(t *testing.T) {
	setupIsolatedRegistry(t, map[string]DaemonInfo{})

	err := ensureDaemonImpl(false)

	require.Error(t, err, "must not spawn a daemon from inside a test binary")
	assert.ErrorIs(t, err, selfexec.ErrUnderTest)
}
