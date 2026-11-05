package adapters

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
	"github.com/stretchr/testify/require"
)

// The host must refuse discovery even if an older adapter binary still has
// the newest-file fallback. No subprocess should be needed to reject it.
func TestExternalAdapterRejectsUnboundDiscovery(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".sageox"), 0o700))
	for _, name := range []string{"gemini", "amp"} {
		t.Run(name, func(t *testing.T) {
			adapter := NewExternalAdapterWithInfo("nonexistent-adapter", &adapterprotocol.InfoResponse{Name: name})
			path, err := adapter.FindSessionFile(SessionLookup{RepoRoot: repo, AgentID: "OxCurrent", Since: time.Now()})
			require.Empty(t, path)
			require.ErrorIs(t, err, adapterruntime.ErrUnboundSource)
			_, err = adapter.CapturePrior(adapterprotocol.CapturePriorParams{RepoRoot: repo, AgentID: "OxCurrent"})
			require.ErrorIs(t, err, adapterruntime.ErrUnboundSource)
			if name == "gemini" {
				_, err = adapter.ImportSession("current", repo)
				require.ErrorIs(t, err, adapterruntime.ErrUnboundSource)
			}
		})
	}
}