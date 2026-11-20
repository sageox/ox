package livesession_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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

// withheldGapFor is the gap line that replaces a refused batch. It counts the
// batch's bytes without its final newline, which the gap line keeps.
func withheldGapFor(batch []byte) []byte {
	return fmt.Appendf(nil, `{"type":"gap","reason":"secret_withheld","bytes":%d}`+"\n", len(bytes.TrimSuffix(batch, []byte("\n"))))
}

func joined(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// assertSameBytes compares spool or server content. These bytes may hold the
// synthetic credentials under test, so a failure reports lengths and the first
// differing offset, never the content.
func assertSameBytes(t *testing.T, what string, got, want []byte) {
	t.Helper()
	if bytes.Equal(got, want) {
		return
	}
	at := 0
	for at < len(got) && at < len(want) && got[at] == want[at] {
		at++
	}
	t.Fatalf("%s: %d bytes, want %d; first difference at offset %d", what, len(got), len(want), at)
}

// assertNotContains fails when content holds needle, naming only where.
func assertNotContains(t *testing.T, what string, content []byte, needle string) {
	t.Helper()
	if i := bytes.Index(content, []byte(needle)); i >= 0 {
		t.Fatalf("%s holds a withheld fixture at offset %d", what, i)
	}
}

// assertWithheldOnce checks the one Warn line a withheld batch logs: its byte
// count, and never the secret itself. Failures report counts and levels only.
func assertWithheldOnce(t *testing.T, logs string, n int) {
	t.Helper()
	if strings.Contains(logs, syntheticAWSKey) {
		t.Fatal("the withheld secret reached the log")
	}
	var events []string
	for line := range strings.Lines(logs) {
		if strings.Contains(line, "msg=live_session_secret_withheld") {
			events = append(events, line)
		}
	}
	if len(events) != 1 {
		t.Fatalf("logged %d live_session_secret_withheld lines, want 1", len(events))
	}
	if !strings.Contains(events[0], "level=WARN") || !strings.Contains(events[0], fmt.Sprintf("bytes=%d", n)) {
		t.Fatalf("withheld line = %q, want a Warn carrying bytes=%d", events[0], n)
	}
}

// A batch the publication scan refuses must not end the stream (F1): it is
// replaced in the spool by a secret_withheld gap line declaring its size, and
// the next clean batch still streams. Failure prevented: one pasted key froze
// a teammate's live view on "Live" for the rest of the session, with no close
// and no word to the person who pasted it.
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
		t.Fatalf("status %s acked %d, want streaming and acked through %d", st.Status, st.Acked, len(want))
	}
	assertSameBytes(t, "spool", e.spoolBytes(), want)
	assertSameBytes(t, "server copy", e.stored(st), want)
	assertWithheldOnce(t, logs.String(), len(secretLine)-1)
}

// The close drains too: a refused batch there becomes one gap the same way,
// clean lines that shared its batch included, and the stream still closes, so
// the page reads Finished instead of Live forever.
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
	refused := joined(before, secretLine)
	assertSameBytes(t, "server copy", e.stored(e.state()), withheldGapFor(refused))
	if _, ok := e.srv.ClosedWith(e.st.ConversationID); !ok {
		t.Fatal("the server never received the close")
	}
	assertWithheldOnce(t, logs.String(), len(refused)-1)
}

// An owed withheld notice is only ever cleared by the caller that said it.
// Failure prevented: a scan that found the mark but no stream state (a stream
// caught between moveLiveStream's renames) removed the mark, and the notice
// was never given.
func TestAnOwedNoticeOutlivesAStreamCaughtMidMove(t *testing.T) {
	e := newEnv(t)
	sessions := filepath.Dir(e.dir)
	moving := filepath.Join(sessions, ".live-ses_moving")
	if err := os.Mkdir(moving, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := livesession.OweSecretWithheldNotice(moving); err != nil {
		t.Fatal(err)
	}
	if got := livesession.OwedSecretWithheldNotices(sessions, ""); len(got) != 0 {
		t.Fatalf("a folder with no stream state yet owes %d notices, want none", len(got))
	}
	if !livesession.OwesSecretWithheldNotice(moving) {
		t.Fatal("the scan removed the mark of a stream caught mid-move")
	}
	raw, err := os.ReadFile(filepath.Join(e.dir, "stream-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte("{"), []byte(`{"secret_withheld":true,`), 1)
	if err := os.WriteFile(filepath.Join(moving, "stream-state.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := livesession.OwedSecretWithheldNotices(sessions, ""); len(got) != 1 || got[0] != moving {
		t.Fatalf("owed notices = %v once the move finished, want the moved stream", got)
	}
	if got := livesession.OwedSecretWithheldNotices(sessions, e.st.SessionID); len(got) != 0 {
		t.Fatal("the recording's own stream counted as an earlier one")
	}
	if err := livesession.ClearSecretWithheldNotice(moving); err != nil {
		t.Fatal(err)
	}
	if got := livesession.OwedSecretWithheldNotices(sessions, ""); len(got) != 0 {
		t.Fatal("a notice said and cleared is still owed")
	}
}
