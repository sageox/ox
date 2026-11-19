package livesession_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
)

// Failure prevented (S5): stop asked for the close, the close could not finish
// (SageOx unreachable), and a batch committed to raw.jsonl after that: a capture
// already in flight when stop fired, or a stop that failed later and kept the
// recording going. Spool refused the batch as "not streaming", nothing recorded
// it, and the daemon's next close finalized the stream as complete without it.
func TestABatchCommittedWhileClosingReachesTheClosedStream(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 3))
	e.srv.Hang(true)
	if res, _ := livesession.Close(t.Context(), e.cfg, e.dir, "", 300*time.Millisecond); res.Status != livesession.StatusClosing {
		t.Fatalf("precondition: close against a hung server left %s, want closing", res.Status)
	}
	e.srv.Hang(false)

	late := lines(3, 5)
	_, spoolErr := livesession.Spool(t.Context(), e.dir, late, livesession.SpoolOptions{})

	res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget) // the daemon's later tick
	if err != nil || res.Status != livesession.StatusClosed {
		t.Fatalf("retried close: %+v %v", res, err)
	}
	stored := e.stored(e.state())
	if !bytes.HasSuffix(stored, late) {
		t.Fatalf("the stream closed without the batch committed while it was closing (spool err=%v, stored %d bytes, no gap line=%v)",
			spoolErr, len(stored), !bytes.Contains(stored, []byte(`"type":"gap"`)))
	}
}

// A batch that lands while the close request is already on the wire cannot be
// sent any more: the server answers session_closed to anything after a close.
// The close re-measures the spool once the server accepted it, so those bytes
// are reported at Error instead of vanishing from the stream unremarked.
func TestABatchSpooledDuringTheCloseRequestIsReported(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 3))
	e.mustDrain()
	var logs lockedBuffer
	e.cfg.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	e.srv.Latency(2 * time.Second) // the close request is the only one left; hold it on the wire

	type closeResult struct {
		res livesession.DrainResult
		err error
	}
	done := make(chan closeResult, 1)
	go func() {
		res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget)
		done <- closeResult{res, err}
	}()
	deadline := time.Now().Add(testBudget)
	for e.srv.InFlight() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the close request never reached the server")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// the close holds live.lock, so this append goes in unlocked after spoolLockWait
	late := lines(3, 4)
	if _, err := livesession.Spool(t.Context(), e.dir, late, livesession.SpoolOptions{}); err != nil {
		t.Fatalf("a batch committed while closing was refused: %v", err)
	}

	got := <-done
	if got.err != nil || got.res.Status != livesession.StatusClosed {
		t.Fatalf("close: %+v %v", got.res, got.err)
	}
	line := logs.lineWith("live_session_closed_without_tail")
	if line == "" {
		t.Fatalf("bytes spooled after the close measured the stream were never reported; logs:\n%s", logs.String())
	}
	if !strings.Contains(line, "level=ERROR") || !strings.Contains(line, fmt.Sprintf("unsent_bytes=%d", len(late))) {
		t.Fatalf("report = %q, want an Error naming the %d unsent bytes", line, len(late))
	}
}

// lockedBuffer is a log sink a background close can write while the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
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

func (b *lockedBuffer) lineWith(msg string) string {
	for _, l := range strings.Split(b.String(), "\n") {
		if strings.Contains(l, "msg="+msg) {
			return l
		}
	}
	return ""
}
