package agentwork

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
)

func TestIsPipeDrainTimeout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"wait delay", fmt.Errorf("adapter x read failed: %w (stderr: )", exec.ErrWaitDelay), true},
		{"unrelated error quoting the text", errors.New("adapter error: exec: WaitDelay expired before I/O complete"), false},
		{"other adapter failure", errors.New("adapter error: bad offset"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isPipeDrainTimeout(tt.err); got != tt.want {
				t.Fatalf("isPipeDrainTimeout(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// flakyHandleReader fails its first call, then serves one entry. The failed
// call reports a bogus advanced offset so a watcher that trusts it is caught.
type flakyHandleReader struct {
	mu      sync.Mutex
	offsets []int64
	err     error
}

func (r *flakyHandleReader) ReadFromOffset(_ string, offset int64) ([]adapters.RawEntry, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.offsets = append(r.offsets, offset)
	if len(r.offsets) == 1 {
		return nil, offset + 100, r.err
	}
	return []adapters.RawEntry{{Timestamp: time.Now().UTC(), Role: "user", Content: "recovered"}}, offset + 1, nil
}

func (r *flakyHandleReader) readOffsets() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.offsets...)
}

// lockedBuffer lets the poll goroutine log while the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A pipe-drain timeout is logged as transient and the next poll retries from
// the same offset, so nothing between the old and a bogus new cursor is lost.
func TestPollSession_PipeDrainTimeoutIsLoggedTransientAndRetried(t *testing.T) {
	cache := t.TempDir()
	rawPath := filepath.Join(cache, "raw.jsonl")
	state := session.RecordingState{WatchMode: "tail", AdapterName: "codex", SessionFile: "codex:x"}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, ".recording.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	rw, err := session.NewRawWriter(rawPath, "")
	if err != nil {
		t.Fatal(err)
	}

	logs := &lockedBuffer{}
	m := NewSessionWatcherManager(slog.New(slog.NewTextHandler(logs, nil)))
	aw := &activeWatcher{sessionName: "s", adapterName: "codex", sessionFile: "codex:x", cachePath: cache}
	reader := &flakyHandleReader{err: fmt.Errorf("adapter read-from-offset failed: %w (stderr: )", exec.ErrWaitDelay)}
	const startOffset int64 = 7

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.pollSession(ctx, aw, reader, rw, startOffset)
	}()
	defer func() {
		cancel()
		<-done
		_ = rw.Close()
	}()

	waitFor(t, "the retry to be recorded", func() bool {
		raw, err := os.ReadFile(rawPath)
		return err == nil && strings.Contains(string(raw), "recovered")
	})
	if got := logs.String(); !strings.Contains(got, "handle-based session read failed") || !strings.Contains(got, "transient=true") {
		t.Fatalf("log missing transient flag: %s", got)
	}
	if offsets := reader.readOffsets(); len(offsets) < 2 || offsets[0] != startOffset || offsets[1] != startOffset {
		t.Fatalf("read offsets = %v, want the retry to reuse %d", offsets, startOffset)
	}
}
