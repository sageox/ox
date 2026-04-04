package adapterruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// collectWriter captures PushEvent output by writing to a temp file we can read back.
// We test the FileWatcher by providing a mock ReadFunc and verifying pushed events.

func TestFileWatcher_WatchAndPush(t *testing.T) {
	// create a temp session file
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(sessionFile, []byte("line1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// track calls to ReadFunc
	readCalled := make(chan struct{}, 10)
	readFn := func(file string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
		entries := []adapterprotocol.RawEntry{
			{Role: adapterprotocol.RoleUser, Content: "hello"},
		}
		readCalled <- struct{}{}
		return entries, offset + 1, nil
	}

	// create writer to capture output
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	writer := &Writer{w: w}

	fw, err := NewFileWatcher(writer, readFn)
	if err != nil {
		t.Fatal(err)
	}
	defer fw.Close()
	// speed up debounce for test
	fw.debounce = 10 * time.Millisecond

	if err := fw.Watch("agent-1", sessionFile, 0); err != nil {
		t.Fatal(err)
	}

	// trigger a file write
	f, err := os.OpenFile(sessionFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("line2\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	// wait for ReadFunc to be called
	select {
	case <-readCalled:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ReadFunc call")
	}

	// give the event time to be written after ReadFunc returns
	time.Sleep(50 * time.Millisecond)

	// close the watcher first (stops writes), then close write end to read
	fw.Close()
	w.Close()

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	if n == 0 {
		t.Fatal("no event written to output")
	}

	var evt adapterprotocol.Event
	if err := json.Unmarshal(buf[:n], &evt); err != nil {
		t.Fatalf("failed to parse event: %v (raw: %s)", err, buf[:n])
	}
	if evt.Event != "entries" {
		t.Errorf("event type = %q, want %q", evt.Event, "entries")
	}
	if evt.AgentID != "agent-1" {
		t.Errorf("agent ID = %q, want %q", evt.AgentID, "agent-1")
	}
}

func TestFileWatcher_Unwatch(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(sessionFile, []byte("init\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	readCalled := make(chan struct{}, 10)
	readFn := func(file string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
		readCalled <- struct{}{}
		return []adapterprotocol.RawEntry{{Role: adapterprotocol.RoleUser, Content: "hi"}}, offset + 1, nil
	}

	_, w, _ := os.Pipe()
	writer := &Writer{w: w}
	defer w.Close()

	fw, err := NewFileWatcher(writer, readFn)
	if err != nil {
		t.Fatal(err)
	}
	defer fw.Close()
	fw.debounce = 10 * time.Millisecond

	if err := fw.Watch("agent-1", sessionFile, 0); err != nil {
		t.Fatal(err)
	}
	fw.Unwatch("agent-1")

	// write to file after unwatch — should NOT trigger read
	f, _ := os.OpenFile(sessionFile, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("after unwatch\n")
	f.Close()

	select {
	case <-readCalled:
		t.Error("ReadFunc called after Unwatch")
	case <-time.After(200 * time.Millisecond):
		// expected — no call
	}
}

func TestFileWatcher_NoEntriesNoPush(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(sessionFile, []byte("init\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	readCalled := make(chan struct{}, 10)
	readFn := func(file string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
		readCalled <- struct{}{}
		return nil, offset, nil // no new entries
	}

	r, w, _ := os.Pipe()
	writer := &Writer{w: w}

	fw, err := NewFileWatcher(writer, readFn)
	if err != nil {
		t.Fatal(err)
	}
	defer fw.Close()
	fw.debounce = 10 * time.Millisecond

	if err := fw.Watch("agent-1", sessionFile, 0); err != nil {
		t.Fatal(err)
	}

	f, _ := os.OpenFile(sessionFile, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("more data\n")
	f.Close()

	// wait for read to happen
	select {
	case <-readCalled:
		// good, read was triggered
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ReadFunc call")
	}

	// close write end and verify nothing was pushed
	w.Close()
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	if n > 0 {
		t.Errorf("unexpected event written when no entries returned: %s", buf[:n])
	}
}

func TestFileWatcher_MultipleAgentsSameFile(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "shared.jsonl")
	if err := os.WriteFile(sessionFile, []byte("init\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	agentsSeen := make(chan string, 10)
	readFn := func(file string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
		return []adapterprotocol.RawEntry{{Role: adapterprotocol.RoleUser, Content: "msg"}}, offset + 1, nil
	}

	_, w, _ := os.Pipe()
	writer := &Writer{w: w}
	defer w.Close()

	fw, err := NewFileWatcher(writer, readFn)
	if err != nil {
		t.Fatal(err)
	}
	defer fw.Close()
	fw.debounce = 10 * time.Millisecond

	// watch same file for two agents
	if err := fw.Watch("agent-a", sessionFile, 0); err != nil {
		t.Fatal(err)
	}
	if err := fw.Watch("agent-b", sessionFile, 0); err != nil {
		t.Fatal(err)
	}

	// unwatch one — file should still be watched for the other
	fw.Unwatch("agent-a")

	fw.mu.Lock()
	_, aExists := fw.sessions["agent-a"]
	_, bExists := fw.sessions["agent-b"]
	fw.mu.Unlock()

	if aExists {
		t.Error("agent-a should be removed after Unwatch")
	}
	if !bExists {
		t.Error("agent-b should still exist after unwatching agent-a")
	}

	_ = agentsSeen // used for setup clarity
}

func TestFileWatcher_Close(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(sessionFile, []byte("init\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	readFn := func(file string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
		return nil, offset, nil
	}

	_, w, _ := os.Pipe()
	writer := &Writer{w: w}
	defer w.Close()

	fw, err := NewFileWatcher(writer, readFn)
	if err != nil {
		t.Fatal(err)
	}

	if err := fw.Watch("agent-1", sessionFile, 0); err != nil {
		t.Fatal(err)
	}

	fw.Close()

	fw.mu.Lock()
	count := len(fw.sessions)
	fw.mu.Unlock()

	if count != 0 {
		t.Errorf("sessions not cleared after Close: got %d", count)
	}
}
