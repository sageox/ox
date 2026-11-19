package livesession_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
)

// A batch committed to raw.jsonl that the capture could not read back is
// declared like a batch the spool could not write: the next write carries one
// spool_write_failed gap of its size.
func TestDeclareLostIsDeclaredByTheNextWrite(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 2))

	if err := livesession.DeclareLost(t.Context(), e.dir, 1234, errors.New("read back failed")); err != nil {
		t.Fatalf("declare lost: %v", err)
	}
	res, err := livesession.Spool(t.Context(), e.dir, lines(2, 3), livesession.SpoolOptions{})

	if err != nil || res.Gaps != 1 {
		t.Fatalf("spool after the loss: %+v %v, want one gap declared", res, err)
	}
	spool, before := e.spoolBytes(), lines(0, 2)
	gap, _, _ := bytes.Cut(spool[len(before):], []byte("\n"))
	if g := decodeGap(t, gap); g.Reason != "spool_write_failed" || g.Bytes != 1234 {
		t.Fatalf("gap = %+v, want spool_write_failed for 1234 bytes", g)
	}
}

// Not a loss: a session that does not stream records nothing.
func TestDeclareLostOnANonStreamingSessionRecordsNothing(t *testing.T) {
	dir := t.TempDir()
	if err := livesession.DeclareLost(t.Context(), dir, 10, errors.New("read back failed")); !errors.Is(err, livesession.ErrNotStreaming) {
		t.Fatalf("err = %v, want ErrNotStreaming", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "spool-lost")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a non-streaming session recorded a loss: %v", err)
	}
}

// A loss declared while a write is settling the previous one must keep its gap
// line. settleLost read the count, then removed it; a loss recorded in between
// vanished. The hook lands a second DeclareLost in exactly that window. Held
// off by the lost-count lock, it cannot finish there, so the wait gives up and
// the loss lands after the settle; unserialized, it lands inside and is wiped.
func TestALossDeclaredWhileTheLastIsSettlingIsKept(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 2))
	if err := livesession.DeclareLost(t.Context(), e.dir, 100, errors.New("first")); err != nil {
		t.Fatalf("declare the first loss: %v", err)
	}
	done := make(chan error, 1)
	fired := false
	livesession.SetSettleLostReadHookForTest(t, func() {
		if fired {
			return
		}
		fired = true
		go func() { done <- livesession.DeclareLost(context.Background(), e.dir, 7, errors.New("second")) }()
		select {
		case err := <-done:
			done <- err
		case <-time.After(300 * time.Millisecond):
		}
	})
	if _, err := livesession.Spool(t.Context(), e.dir, lines(2, 3), livesession.SpoolOptions{}); err != nil {
		t.Fatalf("spool that settles the first loss: %v", err)
	}
	livesession.SetSettleLostReadHookForTest(t, nil)
	if err := <-done; err != nil {
		t.Fatalf("declare the second loss: %v", err)
	}

	before := e.spoolBytes()
	res, err := livesession.Spool(t.Context(), e.dir, lines(3, 4), livesession.SpoolOptions{})
	if err != nil || res.Gaps != 1 {
		t.Fatalf("spool after the second loss: %+v %v, want its gap declared", res, err)
	}
	gap, _, _ := bytes.Cut(e.spoolBytes()[len(before):], []byte("\n"))
	if g := decodeGap(t, gap); g.Reason != "spool_write_failed" || g.Bytes != 7 {
		t.Fatalf("gap = %+v, want spool_write_failed for the 7 bytes declared mid-settle", g)
	}
}
