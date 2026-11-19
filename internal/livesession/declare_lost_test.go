package livesession_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
// line. The write reads the loss totals, writes the gap line, then stores what
// it read as declared; the hook lands a second DeclareLost in between. Storing
// a total re-read after the gap line, or clearing the record, would swallow it.
func TestALossDeclaredWhileTheLastIsSettlingIsKept(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 2))
	if err := livesession.DeclareLost(t.Context(), e.dir, 100, errors.New("first")); err != nil {
		t.Fatalf("declare the first loss: %v", err)
	}
	var second error
	fired := false
	livesession.SetLostDeclareHookForTest(t, func() {
		if !fired {
			fired = true
			second = livesession.DeclareLost(t.Context(), e.dir, 7, errors.New("second"))
		}
	})
	e.spoolDeclaresLoss(lines(2, 3), 100)
	livesession.SetLostDeclareHookForTest(t, nil)
	if !fired || second != nil {
		t.Fatalf("second loss inside the settle window: fired=%v err=%v", fired, second)
	}

	e.spoolDeclaresLoss(lines(3, 4), 7)
}
