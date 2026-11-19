package livesession_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/secretguard"
)

// captureLogs sends the session's log lines to a buffer the test can read.
func (e *env) captureLogs() *lockedBuffer {
	logs := &lockedBuffer{}
	e.cfg.Logger = slog.New(slog.NewTextHandler(logs, nil))
	return logs
}

// withheldGapFor is the gap line that replaces one refused line. It counts the
// line's bytes without its newline, which the gap line keeps.
func withheldGapFor(line []byte) []byte {
	return fmt.Appendf(nil, `{"type":"gap","reason":"secret_withheld","bytes":%d}`+"\n", len(bytes.TrimSuffix(line, []byte("\n"))))
}

func joined(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// assertWithheldOnce checks the one Warn line a withheld batch logs: its byte
// count, and never the secret itself.
func assertWithheldOnce(t *testing.T, logs string, n int) {
	t.Helper()
	var events []string
	for line := range strings.Lines(logs) {
		if strings.Contains(line, "msg=live_session_secret_withheld") {
			events = append(events, line)
		}
	}
	if len(events) != 1 {
		t.Fatalf("logged %d live_session_secret_withheld lines, want 1:\n%s", len(events), logs)
	}
	if !strings.Contains(events[0], "level=WARN") || !strings.Contains(events[0], fmt.Sprintf("bytes=%d", n)) {
		t.Fatalf("withheld line = %q, want a Warn carrying bytes=%d", events[0], n)
	}
	if strings.Contains(logs, syntheticAWSKey) {
		t.Fatal("the withheld secret reached the log")
	}
}

// A batch the publication scan refuses must not end the stream (F1): its
// refused line is replaced in the spool by a secret_withheld gap line declaring
// its size, and the next clean batch still streams. Failure prevented: one pasted key
// froze a teammate's live view on "Live" for the rest of the session, with no
// close and no word to the person who pasted it.
func TestDrainReplacesARefusedBatchWithAGapAndKeepsStreaming(t *testing.T) {
	if secretguard.ValidateContent("sessions/content", secretLine) == nil {
		t.Fatal("test setup: the bundled scan accepts the fixture")
	}
	e := newEnv(t)
	logs := e.captureLogs()
	before, after := lines(0, 2), lines(2, 3)
	e.spool(before)
	e.mustDrain()
	e.spool(secretLine)
	if res, err := e.drain(); err != nil || !res.Done {
		t.Fatalf("drain over the refused batch: %+v %v, want it caught up", res, err)
	}
	e.spool(after)
	e.mustDrain()

	want := joined(before, withheldGapFor(secretLine), after)
	st := e.state()
	if st.Status != livesession.StatusStreaming || st.Acked != int64(len(want)) {
		t.Fatalf("state = %+v, want streaming and acked through %d", st, len(want))
	}
	if got := e.spoolBytes(); !bytes.Equal(got, want) {
		t.Fatalf("spool =\n%s\nwant the refused batch replaced by its gap line:\n%s", got, want)
	}
	if got := e.stored(st); !bytes.Equal(got, want) {
		t.Fatalf("server holds\n%s\nwant\n%s", got, want)
	}
	assertWithheldOnce(t, logs.String(), len(secretLine)-1)
}

// The close drains too: a refused line there is gapped the same way, the
// clean lines in its batch still stream, and the stream still closes, so the
// page reads Finished instead of Live forever.
func TestCloseGapsARefusedBatchAndStillCloses(t *testing.T) {
	e := newEnv(t)
	logs := e.captureLogs()
	before := lines(0, 2)
	e.spool(before)
	e.spool(secretLine)

	res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget)
	if err != nil || res.Status != livesession.StatusClosed {
		t.Fatalf("close: %+v %v, want closed", res, err)
	}
	want := joined(before, withheldGapFor(secretLine))
	if got := e.stored(e.state()); !bytes.Equal(got, want) {
		t.Fatalf("server holds\n%s\nwant\n%s", got, want)
	}
	if _, ok := e.srv.ClosedWith(e.st.ConversationID); !ok {
		t.Fatal("the server never received the close")
	}
	assertWithheldOnce(t, logs.String(), len(secretLine)-1)
}
