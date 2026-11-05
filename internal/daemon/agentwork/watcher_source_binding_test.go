package agentwork

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
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

func TestStartWatchAt_RefusesUnboundSource(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, ".gemini", "tmp", "proj", "chats", "session.json")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	data, err := json.Marshal(session.RecordingState{WatchMode: "tail", AdapterName: "gemini", SessionFile: source})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, recordingMarker), data, 0o600); err != nil {
		t.Fatal(err)
	}
	adapters.Register(&testAdapter{name: "gemini"})
	t.Cleanup(func() { adapters.Unregister("gemini") })
	m := NewSessionWatcherManager(slog.New(slog.DiscardHandler))
	m.SetHomeDirForTest(home)

	err = m.startWatchAt("s", source, "gemini", t.TempDir(), cache, 0)
	if !errors.Is(err, adapterruntime.ErrUnboundSource) {
		t.Fatalf("startWatchAt accepted an unbound gemini source: %v", err)
	}
	if _, ok := m.watchers["s"]; ok {
		t.Fatal("an unbound source must not get a watcher")
	}
}
