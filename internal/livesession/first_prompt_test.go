package livesession_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/sessionstream"
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

// Failure prevented (PR 1971 review): a first prompt over the batch limit
// spools as an oversized_line gap, so the stream never opened and stop marked
// a real session empty. Any declared gap may have been the prompt.
func TestAnOversizedFirstPromptStillOpensTheStream(t *testing.T) {
	srv, cfg, dir, _ := prepared(t)
	spoolOrFail(t, dir, startOnly)
	big := `{"type":"user","content":"` + strings.Repeat("x", sessionstream.MaxBatchBytes) + `"}` + "\n"
	spoolOrFail(t, dir, []byte(big))

	res, err := livesession.Close(t.Context(), cfg, dir, "", testBudget)
	if err != nil || res.Status != livesession.StatusClosed {
		t.Fatalf("stop after an oversized first prompt: %+v %v, want the stream opened and closed with its gap", res, err)
	}
	st, _ := livesession.ReadState(dir)
	if _, ok := srv.ClosedWith(st.ConversationID); !ok {
		t.Fatal("the server never saw the session")
	}
}

// Failure prevented (PR 1971 review): Spool recorded a failed first prompt's
// loss only after releasing live.lock, so a stop landing in between saw no
// prompt and no loss and ended a real session empty.
func TestAFailedFirstPromptIsRecordedBeforeAStopCanEndTheStream(t *testing.T) {
	srv, cfg, dir, _ := prepared(t)
	spoolOrFail(t, dir, startOnly)
	spool := filepath.Join(dir, "spool.jsonl")
	if err := os.Chmod(spool, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(spool, 0o600) })
	if f, err := os.OpenFile(spool, os.O_WRONLY|os.O_APPEND, 0); err == nil {
		_ = f.Close()
		t.Skip("a read-only spool is still writable here (root?): the append failure cannot be injected")
	}
	stopped := false
	livesession.SetSpoolUnlockedHookForTest(t, func() {
		if !stopped {
			stopped = true // the stop, landing the moment the failed append lets go of live.lock
			_, _ = livesession.Close(t.Context(), cfg, dir, "", testBudget)
		}
	})

	if _, err := livesession.Spool(t.Context(), dir, prompt(), livesession.SpoolOptions{}); err == nil {
		t.Fatal("precondition: the prompt's append should fail on a read-only spool")
	}

	st, err := livesession.ReadState(dir)
	if err != nil || st.Status == livesession.StatusEmpty {
		t.Fatalf("status=%s err=%v: a stop must not end a stream whose first prompt failed to spool as empty", st.Status, err)
	}
	if srv.CallsTo("PUT", st.ConversationID) == 0 {
		t.Fatal("the stream was never opened, so its lost prompt is never declared")
	}
}
