package paths

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTracePathsFollowCanonicalRoots(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "")
	cache := t.TempDir()
	state := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("XDG_RUNTIME_DIR", state)
	require.Equal(t, filepath.Join(cache, "sageox", "trace", "spool"), TraceSpoolDir())
	require.Equal(t, filepath.Join(state, "sageox", "trace"), TraceStateDir())
	require.Equal(t, filepath.Join(TempDir(), "trace.log"), TraceLogPath())
}
