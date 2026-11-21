package livesession_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
)

// drainNormalTime bounds a drain of a few KB against the in-process fake
// server: milliseconds normally, so a wait on anything slower (the rules fetch
// times out after 15 s, the daemon's drain budget is 30 s) shows plainly, with
// headroom for a loaded machine.
const drainNormalTime = 3 * time.Second

// The drain never waits on the network for secret rules (F2). Failure
// prevented: a network that blocks raw.githubusercontent.com streamed nothing,
// silently, because every drain needed a fresh upstream catalog first.
func TestDrainSendsWhileTheRulesFetchFails(t *testing.T) {
	livesession.SetCurrentCatalogForTest(t, func(context.Context) ([]byte, error) {
		return nil, errors.New("catalog unavailable")
	})
	e := newEnv(t)
	e.spool(lines(0, 3))

	if res, err := e.drain(); err != nil || !res.Done {
		t.Fatalf("drain with the rules fetch failing: %+v %v, want it sent", res, err)
	}
	assertSameBytes(t, "server copy", e.stored(e.state()), e.spoolBytes())
}

// A fetch that never answers (a hung proxy, a stalled TLS handshake) must not
// hold the drain either.
func TestDrainSendsWhileTheRulesFetchNeverAnswers(t *testing.T) {
	release := make(chan struct{})
	livesession.SetCurrentCatalogForTest(t, func(context.Context) ([]byte, error) {
		<-release // ignores ctx on purpose: blocked for as long as the test runs
		return nil, errors.New("released at test end")
	})
	t.Cleanup(func() { close(release) })
	e := newEnv(t)
	e.spool(lines(0, 3))

	type result struct {
		res livesession.DrainResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := livesession.Drain(context.Background(), e.cfg, e.dir, testBudget)
		done <- result{res, err}
	}()
	select {
	case r := <-done:
		if r.err != nil || !r.res.Done {
			t.Fatalf("drain: %+v %v, want it sent", r.res, r.err)
		}
	case <-time.After(drainNormalTime):
		t.Fatalf("drain still waiting on the rules fetch after %s", drainNormalTime)
	}
	assertSameBytes(t, "server copy", e.stored(e.state()), e.spoolBytes())
}

// The close summary is scanned the same way: a clean summary goes with the
// close even while the rules fetch fails.
func TestCloseSendsACleanSummaryWhileTheRulesFetchFails(t *testing.T) {
	livesession.SetCurrentCatalogForTest(t, func(context.Context) ([]byte, error) {
		return nil, errors.New("catalog unavailable")
	})
	e := newEnv(t)
	e.spool(lines(0, 2))

	res, err := livesession.Close(t.Context(), e.cfg, e.dir, "a clean summary", testBudget)
	if err != nil || res.Status != livesession.StatusClosed {
		t.Fatalf("close: %+v %v", res, err)
	}
	if got, _ := e.srv.ClosedWith(e.st.ConversationID); got.Summary != "a clean summary" {
		t.Fatalf("summary = %q, want the clean summary sent", got.Summary)
	}
}
