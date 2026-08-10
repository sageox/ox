package agentwork

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
)

// opencode and goose read from a SQLite database, so there is no file to tail
// and their offset is a row count only the adapter can compute. The live loop
// used to persist the offset the watcher STARTED with, which never advanced —
// so a daemon restart re-read every entry written during the live phase and
// appended each one to raw.jsonl a second time.
//
// These tests drive the poll loop against a fake reader whose cursor semantics
// match the real ones (offset = rows already consumed).

// fakeHandleReader serves rows from a slice, advancing the cursor by the number
// of rows it hands out — the same contract opencode's readMessages has.
type fakeHandleReader struct {
	mu   sync.Mutex
	rows []string
	// stall makes the reader return entries without advancing, which is the
	// pathological case the loop must refuse to persist.
	stall bool
	calls int
}

func (r *fakeHandleReader) append(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, text)
}

func (r *fakeHandleReader) ReadFromOffset(_ string, offset int64) ([]adapters.RawEntry, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++

	if offset >= int64(len(r.rows)) {
		return nil, offset, nil
	}
	var out []adapters.RawEntry
	for _, text := range r.rows[offset:] {
		out = append(out, adapters.RawEntry{
			Timestamp: time.Now().UTC(),
			Role:      "user",
			Content:   text,
		})
	}
	if r.stall {
		return out, offset, nil
	}
	return out, int64(len(r.rows)), nil
}

// runPoll starts the poll loop against a temp cache dir and returns the raw
// path plus a stop func.
func runPoll(t *testing.T, reader adapters.IncrementalReader, startOffset int64) (rawPath string, recPath string, stop func()) {
	t.Helper()

	cache := t.TempDir()
	rawPath = filepath.Join(cache, "raw.jsonl")
	recPath = filepath.Join(cache, ".recording.json")

	// persistOffset does a read-modify-write of .recording.json, so it has to
	// exist for the offset to be observable
	state := session.RecordingState{WatchMode: "tail", AdapterName: "opencode", SessionFile: "opencode:ses_x"}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	rw, err := session.NewRawWriter(rawPath, "")
	if err != nil {
		t.Fatal(err)
	}

	m := NewSessionWatcherManager(slog.New(slog.DiscardHandler))
	aw := &activeWatcher{
		sessionName: "s",
		adapterName: "opencode",
		sessionFile: "opencode:ses_x",
		cachePath:   cache,
		startOffset: startOffset,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.pollSession(ctx, aw, reader, rw, startOffset)
	}()

	return rawPath, recPath, func() {
		cancel()
		<-done
		_ = rw.Close()
	}
}

func persistedOffset(t *testing.T, recPath string) int64 {
	t.Helper()
	data, err := os.ReadFile(recPath)
	if err != nil {
		t.Fatal(err)
	}
	var state session.RecordingState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state.SourceOffset
}

func countOccurrences(t *testing.T, path, needle string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	return strings.Count(string(data), needle)
}

// waitFor polls until cond holds or the deadline passes. The loop ticks on a
// timer, so the test has to wait for real passes rather than sleeping a fixed
// amount and hoping.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestPollSession_AdvancesTheCursorAcrossBatches is the regression: after
// two live batches the persisted offset must reflect everything consumed, not
// the offset the watcher started with.
func TestPollSession_AdvancesTheCursorAcrossBatches(t *testing.T) {
	reader := &fakeHandleReader{}
	reader.append("first")

	rawPath, recPath, stop := runPoll(t, reader, 0)
	defer stop()

	waitFor(t, "the first batch to be recorded", func() bool {
		return countOccurrences(t, rawPath, "first") == 1
	})
	waitFor(t, "the cursor to advance past the first batch", func() bool {
		return persistedOffset(t, recPath) == 1
	})

	reader.append("second")
	waitFor(t, "the second batch to be recorded", func() bool {
		return countOccurrences(t, rawPath, "second") == 1
	})
	waitFor(t, "the cursor to advance past the second batch", func() bool {
		return persistedOffset(t, recPath) == 2
	})

	// the defect: a stalled cursor means a restart re-reads both entries
	if got := persistedOffset(t, recPath); got != 2 {
		t.Fatalf("persisted offset = %d, want 2 — a restart would replay %d already-recorded entries", got, 2-got)
	}
	if n := countOccurrences(t, rawPath, "first"); n != 1 {
		t.Errorf("the first entry appears %d times in raw.jsonl, want 1", n)
	}
}

// TestPollSession_RestartDoesNotReplay simulates the restart directly:
// a second loop started from the persisted offset must record nothing new.
func TestPollSession_RestartDoesNotReplay(t *testing.T) {
	reader := &fakeHandleReader{}
	reader.append("alpha")
	reader.append("beta")

	rawPath, recPath, stop := runPoll(t, reader, 0)
	waitFor(t, "both entries to be recorded", func() bool {
		return countOccurrences(t, rawPath, "alpha") == 1 && countOccurrences(t, rawPath, "beta") == 1
	})
	waitFor(t, "the cursor to reach the end", func() bool {
		return persistedOffset(t, recPath) == 2
	})
	resumeFrom := persistedOffset(t, recPath)
	stop()

	// restart from what was persisted, writing into the same cache dir
	rawPath2, _, stop2 := runPoll(t, reader, resumeFrom)
	defer stop2()

	time.Sleep(2 * pollInterval)
	if n := countOccurrences(t, rawPath2, "alpha"); n != 0 {
		t.Errorf("restart replayed %d already-recorded entries — the ledger would double-count them", n)
	}
}

// failingWriterReader returns rows normally; the test pairs it with a closed
// writer so every WriteEntry fails.
type failingWriterReader struct{ fakeHandleReader }

// TestPollSession_DoesNotAdvancePastEntriesTheLedgerNeverReceived covers a
// write failure. Advancing the cursor there marks entries consumed that were
// never written, and the adapter resumes past them — they are gone. Leaving the
// cursor costs a re-read, which is visible and fixable.
func TestPollSession_DoesNotAdvancePastEntriesTheLedgerNeverReceived(t *testing.T) {
	reader := &failingWriterReader{}
	reader.append("lost-if-acknowledged")

	cache := t.TempDir()
	recPath := filepath.Join(cache, ".recording.json")
	state := session.RecordingState{WatchMode: "tail", AdapterName: "opencode", SessionFile: "opencode:ses_x"}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	rw, err := session.NewRawWriter(filepath.Join(cache, "raw.jsonl"), "")
	if err != nil {
		t.Fatal(err)
	}
	// close it so every write fails, standing in for a full disk or a
	// permissions change mid-session
	_ = rw.Close()

	m := NewSessionWatcherManager(slog.New(slog.DiscardHandler))
	aw := &activeWatcher{sessionName: "s", adapterName: "opencode", sessionFile: "opencode:ses_x", cachePath: cache}

	ctx, cancel := context.WithTimeout(context.Background(), 3*pollInterval)
	defer cancel()
	m.pollSession(ctx, aw, reader, rw, 0)

	if got := persistedOffset(t, recPath); got != 0 {
		t.Errorf("cursor advanced to %d after the write failed — those entries are marked consumed and can never be recovered", got)
	}
}

// TestPollSession_StopsOnAStalledCursor covers an adapter that returns rows but
// reports the same offset.
//
// Checking the cursor only AFTER writing would append those rows to raw.jsonl
// on every poll: the duplication lands in the ledger, and declining to persist
// the offset afterwards does not undo it. Nothing the loop can do makes such an
// adapter usable, so it stops instead of accumulating copies.
func TestPollSession_StopsOnAStalledCursor(t *testing.T) {
	reader := &fakeHandleReader{stall: true}
	reader.append("only")

	rawPath, recPath, stop := runPoll(t, reader, 0)
	defer stop()

	time.Sleep(3 * pollInterval)

	if n := countOccurrences(t, rawPath, "only"); n != 0 {
		t.Errorf("a stalled adapter wrote %d copies of its rows to raw.jsonl, want 0 — each poll would add another", n)
	}
	if got := persistedOffset(t, recPath); got != 0 {
		t.Errorf("persisted offset = %d, want 0 — a cursor that did not advance must not be written over a good one", got)
	}
}
