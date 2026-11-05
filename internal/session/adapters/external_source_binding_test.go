package adapters

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// An adapter whose sources cannot be bound to this repository is refused
// before its binary runs: the binary would read another project's transcript.
func TestExternalAdapter_RefusesUnboundSourceBeforeRunningTheAdapter(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "ox-adapter-gemini") // would fail differently if spawned
	ea := NewExternalAdapterWithInfo(missing, &adapterprotocol.InfoResponse{Name: "gemini"})
	const id = "0b6a3c2e-7d41-4f2a-9c1e-5a8d2f6b9e10"

	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := ea.FindSessionFile(SessionLookup{RepoRoot: repo, AgentID: "OxBind", AgentSessionID: id})
	if !errors.Is(err, adapterruntime.ErrUnboundSource) {
		t.Errorf("FindSessionFile: %v", err)
	}
	_, err = ea.CapturePrior(adapterprotocol.CapturePriorParams{SessionID: id})
	if !errors.Is(err, adapterruntime.ErrUnboundSource) {
		t.Errorf("CapturePrior: %v", err)
	}
	_, err = ea.ImportSession(id, repo)
	if !errors.Is(err, adapterruntime.ErrUnboundSource) {
		t.Errorf("ImportSession: %v", err)
	}
}
