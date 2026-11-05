package agentwork

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/pkg/adapterruntime"
)

// The watcher re-checks the binding on every read, so a source that cannot be
// tied to this recording is never tailed.
func TestValidateWatcherSource_RefusesUnboundSources(t *testing.T) {
	foreign := filepath.Join(t.TempDir(), "T-other-thread.jsonl")
	if err := os.WriteFile(foreign, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, aw := range []*activeWatcher{
		{adapterName: "gemini", sessionFile: foreign},
		{adapterName: "amp", sessionFile: foreign, nativeID: "T-0b6a3c2e-7d41-4f2a-9c1e-5a8d2f6b9e10"},
	} {
		if err := validateWatcherSource(aw, 0); !errors.Is(err, adapterruntime.ErrUnboundSource) {
			t.Errorf("%s: validateWatcherSource accepted %s: %v", aw.adapterName, aw.sessionFile, err)
		}
		if err := validateWatcherSourceFrom(aw, 0, nil); !errors.Is(err, adapterruntime.ErrUnboundSource) {
			t.Errorf("%s: validateWatcherSourceFrom accepted %s: %v", aw.adapterName, aw.sessionFile, err)
		}
	}
}
