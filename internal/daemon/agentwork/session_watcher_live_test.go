package agentwork

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/session"
)

// The tail watcher appends through appendBatch. Live streaming needs the exact
// bytes of each committed batch, handed over only after the append succeeded; a
// failed append must hand over nothing.
func TestAppendBatchHandsTheCommittedBytesToTheLiveSpooler(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, recordingMarker)
	data, _ := json.Marshal(session.RecordingState{CommandRedactionVersion: 1})
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	rawPath := filepath.Join(dir, "raw.jsonl")
	if err := os.WriteFile(rawPath, []byte(`{"type":"header"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewSessionWatcherManager(slog.Default())
	var got []session.AppendedBatch
	var gotDir []string
	m.SetLiveSpooler(func(d string, b session.AppendedBatch) { gotDir, got = append(gotDir, d), append(got, b) })
	rw, err := session.NewRawWriter(rawPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	aw := &activeWatcher{cachePath: dir, sessionName: "s"}

	if err := m.appendBatch(aw, rw, []session.Entry{{Type: session.EntryTypeUser, Content: "hello"}}, 10); err != nil {
		t.Fatal(err)
	}
	all, _ := os.ReadFile(rawPath)
	header := len(`{"type":"header"}` + "\n")
	if len(got) != 1 || string(got[0].Bytes) != string(all[header:]) || gotDir[0] != dir {
		t.Fatalf("spooler got %+v (dirs %v), want the %q batch in %s", got, gotDir, all[header:], dir)
	}

	if err := m.appendBatch(aw, rw, []session.Entry{{Type: session.EntryTypeUser, Content: "again"}}, 5); err == nil { // cursor goes backwards
		t.Fatal("expected the append to fail")
	}
	if len(got) != 1 {
		t.Fatalf("a failed append reached the live spooler: %d batches", len(got))
	}
}
