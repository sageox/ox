package livesession_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/sessionstream"
)

func TestSpoolBeforeOpenIsNotStreaming(t *testing.T) {
	_, err := livesession.Spool(t.Context(), t.TempDir(), lines(0, 1), livesession.SpoolOptions{})
	if !errors.Is(err, livesession.ErrNotStreaming) {
		t.Fatalf("err = %v, want ErrNotStreaming", err)
	}
}

func TestSpoolPausedBytesNeverReachTheSpool(t *testing.T) {
	e := newEnv(t)
	res, err := livesession.Spool(t.Context(), e.dir, lines(0, 3), livesession.SpoolOptions{Paused: true})
	if err != nil || !res.Skipped {
		t.Fatalf("paused spool: %+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "live.ndjson")); !os.IsNotExist(err) {
		t.Fatalf("paused bytes created a spool: %v", err)
	}
	e.spool(lines(5, 6))
	if got := e.spoolBytes(); !bytes.Equal(got, lines(5, 6)) {
		t.Fatalf("spool = %d bytes, want only the unpaused batch", len(got))
	}
}

func TestSpoolOversizedLineBecomesDeclaredGap(t *testing.T) {
	e := newEnv(t)
	huge := bytes.Repeat([]byte("y"), sessionstream.MaxBatchBytes+10)
	batch := append(append(lines(0, 1), []byte(`{"type":"big","d":"`)...), huge...)
	batch = append(append(batch, []byte("\"}\n")...), lines(1, 2)...)
	e.spool(batch)

	want := append(append(lines(0, 1), []byte(fmt.Sprintf(`{"type":"gap","reason":"oversized_line","bytes":%d}`+"\n", len(huge)+21))...), lines(1, 2)...)
	if got := e.spoolBytes(); !bytes.Equal(got, want) {
		t.Fatalf("spool = %d bytes, want neighbors intact and one oversized_line gap (%d bytes)", len(got), len(want))
	}
	e.mustDrain() // the server must accept the spool as written
}

func TestSpoolInvalidLineBecomesGapSoServerNeverWedges(t *testing.T) {
	e := newEnv(t)
	e.spool(append(append(lines(0, 1), []byte("not json at all\n")...), lines(1, 2)...))
	if !bytes.Contains(e.spoolBytes(), []byte(`"reason":"invalid_line"`)) {
		t.Fatal("invalid line was spooled verbatim")
	}
	e.mustDrain()
}

func TestSpoolCapWritesOneSpoolFullGapThenResumesAfterAck(t *testing.T) {
	e := newEnv(t)
	opts := livesession.SpoolOptions{CapBytes: 3000}
	for i := 0; i < 8; i++ { // ~1 KB each: only the first two fit under the cap
		if _, err := livesession.Spool(t.Context(), e.dir, lines(i, i+1), opts); err != nil {
			t.Fatal(err)
		}
	}
	if n := bytes.Count(e.spoolBytes(), []byte(`"reason":"spool_full"`)); n != 1 {
		t.Fatalf("spool_full gaps = %d, want exactly 1", n)
	}
	if n := bytes.Count(e.spoolBytes(), []byte(`"type":"msg"`)); n != 2 {
		t.Fatalf("spooled messages = %d, want 2 under the cap", n)
	}

	e.mustDrain() // acking frees room
	if _, err := livesession.Spool(t.Context(), e.dir, lines(100, 101), opts); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(e.spoolBytes(), []byte(`"n":100,`)) {
		t.Fatal("spool did not accept lines again after acked bytes were freed")
	}
}

func TestSpoolTornTailIsDroppedAndDeclared(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 2))
	f, err := os.OpenFile(filepath.Join(e.dir, "live.ndjson"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"type":"msg","n":99,"pa`) // a crash mid-write
	_ = f.Close()

	e.spool(lines(2, 3))
	spool := e.spoolBytes()
	if bytes.Contains(spool, []byte(`"n":99`)) || !bytes.Contains(spool, []byte(`"reason":"torn_tail"`)) {
		t.Fatalf("torn tail not replaced by a gap: %q", spool[len(spool)-200:])
	}
	e.mustDrain()
}
