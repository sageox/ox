package livesession_test

import (
	"errors"
	"testing"

	"github.com/sageox/ox/internal/livesession"
)

// what a session start and its prime leave in the spool: the raw.jsonl header
// and machine-written lines, no human prompt
var startOnly = []byte(`{"type":"header","metadata":{"agent_id":"Oxabc1"}}` + "\n" + `{"type":"system","content":"primed"}` + "\n")

func spoolOrFail(t *testing.T, dir string, batch []byte) {
	t.Helper()
	if _, err := livesession.Spool(t.Context(), dir, batch, livesession.SpoolOptions{}); err != nil {
		t.Fatalf("spool: %v", err)
	}
}

// Failure prevented: every prime and /clear opened a server stream for a
// header alone, so teammates saw empty "live" sessions.
func TestPreparedStreamOpensOnlyOnceItsSpoolHoldsAPrompt(t *testing.T) {
	srv, cfg, dir, p := prepared(t)
	spoolOrFail(t, dir, startOnly)

	st, err := livesession.Open(t.Context(), cfg, dir, p)
	if !errors.Is(err, livesession.ErrAwaitingPrompt) || st.Status != livesession.StatusPending {
		t.Fatalf("open with no prompt: status=%s err=%v, want pending and ErrAwaitingPrompt", st.Status, err)
	}
	if n := len(srv.Calls()); n != 0 {
		t.Fatalf("an open with no prompt made %d server calls, want 0", n)
	}

	spoolOrFail(t, dir, prompt())
	st, err = livesession.Open(t.Context(), cfg, dir, p)
	if err != nil || st.Status != livesession.StatusStreaming {
		t.Fatalf("open after the first prompt: status=%s err=%v, want streaming", st.Status, err)
	}
}

// Failure prevented: a recording stopped before its first prompt was opened by
// the stop's close and published as an empty session.
func TestStopBeforeTheFirstPromptEndsTheStreamEmptyWithNoServerCall(t *testing.T) {
	srv, cfg, dir, _ := prepared(t)
	spoolOrFail(t, dir, startOnly)

	res, err := livesession.Close(t.Context(), cfg, dir, "", testBudget)
	if err != nil || !res.Done || res.Status != livesession.StatusEmpty {
		t.Fatalf("close with no prompt: %+v %v, want done and empty", res, err)
	}
	st, err := livesession.ReadState(dir)
	if err != nil || st.CloseRequested {
		t.Fatalf("state after close: close_requested=%v err=%v, want no close left for the daemon", st.CloseRequested, err)
	}
	if _, err := livesession.Spool(t.Context(), dir, prompt(), livesession.SpoolOptions{}); !errors.Is(err, livesession.ErrNotStreaming) {
		t.Fatalf("spool after the stream ended empty: %v, want ErrNotStreaming", err)
	}
	if res, err := livesession.Abort(t.Context(), cfg, dir, testBudget); err != nil || res != (livesession.AbortResult{}) {
		t.Fatalf("abort of an empty stream: %+v %v, want nothing to delete", res, err)
	}
	if n := len(srv.Calls()); n != 0 {
		t.Fatalf("a stream that ended before its first prompt made %d server calls, want 0", n)
	}
}

// Failure prevented: a prompt batch that could not be spooled left a stream
// with no prompt line, which then vanished as empty instead of declaring the
// loss.
func TestARecordedSpoolLossOpensTheStreamAnyway(t *testing.T) {
	_, cfg, dir, p := prepared(t)
	spoolOrFail(t, dir, startOnly)
	if err := livesession.DeclareLost(t.Context(), dir, 120, errors.New("disk full")); err != nil {
		t.Fatalf("declare lost: %v", err)
	}

	st, err := livesession.Open(t.Context(), cfg, dir, p)
	if err != nil || st.Status != livesession.StatusStreaming {
		t.Fatalf("open after a recorded loss: status=%s err=%v, want streaming: the lost batch may have been the prompt", st.Status, err)
	}
}
