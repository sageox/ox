package daemon

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
)

// TestLiveHeldSessionIsNeverPublished: a session that started streaming in
// auto mode and was then held (stopped with session_publishing: manual, or
// handed off by /clear) is local-only from that moment. The daemon's tick and
// a hook kick must send it nowhere: no further append, no close, and the copy
// already on SageOx is deleted.
func TestLiveHeldSessionIsNeverPublished(t *testing.T) {
	e := newLiveEnv(t)
	e.spool(lineN(1))
	e.live.Tick(t.Context())
	e.live.Wait()
	st := e.state()
	if st.Status != livesession.StatusStreaming || st.Acked == 0 {
		t.Fatalf("before the hold: %+v", st)
	}

	e.spool(lineN(2))
	if err := session.WriteHoldMarker(e.dir, session.HoldManualPublishing, "test"); err != nil {
		t.Fatal(err)
	}
	appendsBefore := e.srv.CallsTo("POST", "/lines")

	e.clock = e.clock.Add(liveTickInterval)
	e.live.Tick(t.Context())
	e.live.Drain(t.Context(), e.dir) // a hook kick lands too
	e.live.Wait()

	if n := e.srv.CallsTo("POST", "/lines") - appendsBefore; n != 0 {
		t.Fatalf("sent %d appends after the session was held", n)
	}
	if _, ok := e.srv.ClosedWith(st.ConversationID); ok {
		t.Fatal("closed (published) a held session")
	}
	if !e.srv.IsDeleted(st.ConversationID) {
		t.Fatal("the copy already on SageOx was not deleted")
	}
	if got := e.state(); got.Status != livesession.StatusDeleted {
		t.Fatalf("status = %s, want deleted so no later tick touches it", got.Status)
	}
}

// TestLiveRetryAfterCountsFromTheRefusal: the server's Retry-After is a wait
// that starts when the refusal arrives. Measured from the start of an attempt
// that spent 10 s on the link before the 429, the daemon came back 10 s early
// and could keep drawing 429s; the early-tick slack must not shorten it either.
func TestLiveRetryAfterCountsFromTheRefusal(t *testing.T) {
	e := newLiveEnv(t)
	clk := &testClock{t: e.clock}
	e.live.now = clk.Now
	if _, err := livesession.Open(t.Context(), e.cfg, e.dir, e.params); err != nil {
		t.Fatal(err)
	}
	e.spool(lineN(1))

	const requestCost = 10 * time.Second
	const retryAfter = 30 * time.Second
	link := &outageLink{base: http.DefaultTransport, clk: clk}
	e.cfg.HTTP = &http.Client{Timeout: 5 * time.Second, Transport: chargedLink{link: link, cost: requestCost}}
	e.srv.RateLimit(1, retryAfter.Milliseconds())

	start := clk.Now()
	appends := func() int { return e.srv.CallsTo("POST", "/lines") }
	e.live.Tick(t.Context())
	e.live.Wait()
	if appends() != 1 {
		t.Fatalf("appends = %d, want the one that drew the 429", appends())
	}
	refusedAt := start.Add(requestCost)

	for _, at := range []time.Duration{retryAfter - time.Second, retryAfter - 500*time.Millisecond} {
		clk.Set(refusedAt.Add(at))
		e.live.Tick(t.Context())
		e.live.Wait()
		if appends() != 1 {
			t.Fatalf("retried %s after the 429 that asked for %s", at, retryAfter)
		}
	}
	clk.Set(refusedAt.Add(retryAfter))
	e.live.Tick(t.Context())
	e.live.Wait()
	if appends() != 2 || !strings.Contains(e.stored(), `"n":1`) {
		t.Fatalf("no retry once the server's %s had passed (appends=%d)", retryAfter, appends())
	}
}

// chargedLink charges the fake clock for every request, so an attempt takes
// the time a slow link takes before its answer arrives.
type chargedLink struct {
	link *outageLink
	cost time.Duration
}

func (c chargedLink) RoundTrip(r *http.Request) (*http.Response, error) {
	c.link.clk.Advance(c.cost)
	return c.link.RoundTrip(r)
}

// TestLiveSessionDirThatDisappearsIsReportedAndRediscovered is the outage
// BDD's real failure: a Ledger clone retry moved the half-made Ledger folder
// aside, the sessions folder went with it, and the daemon went quiet for good.
// A streaming session whose folder vanishes is reported once, and streaming
// resumes when the folder is back. A whole sessions folder vanishing is never
// legitimate (ERROR); one session folder can be an abort or a prune that beat
// the tick (WARN).
func TestLiveSessionDirThatDisappearsIsReportedAndRediscovered(t *testing.T) {
	cases := []struct {
		name  string
		level string
		move  func(e *liveEnv) string // returns the path the tick watched
	}{
		{name: "the whole sessions folder moved aside", level: "ERROR", move: func(e *liveEnv) string { return e.sessionsDir }},
		{name: "one session folder removed", level: "WARN", move: func(e *liveEnv) string { return e.dir }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newLiveEnv(t)
			logs := e.captureLogs()
			e.spool(lineN(1))
			e.live.Tick(t.Context())
			e.live.Wait()
			st := e.state()
			watched := canonicalDir(e.dir) // the key the daemon reports, however the path was spelled

			moved := tc.move(e)
			aside := filepath.Join(t.TempDir(), "moved-aside")
			if err := os.Rename(moved, aside); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				e.clock = e.clock.Add(liveTickInterval)
				e.live.Tick(t.Context())
				e.live.Wait()
			}
			missing := logs.records(t, "live_session_dir_missing")
			if len(missing) != 1 {
				t.Fatalf("live_session_dir_missing logged %d times over 3 ticks, want once: %v", len(missing), missing)
			}
			if missing[0]["level"] != tc.level || missing[0]["path"] != watched || missing[0]["conversation_id"] != st.ConversationID {
				t.Fatalf("missing-dir report: %v, want level %s for %s", missing[0], tc.level, watched)
			}

			if err := os.Rename(aside, moved); err != nil {
				t.Fatal(err)
			}
			e.spool(lineN(2))
			e.clock = e.clock.Add(liveTickInterval)
			e.live.Tick(t.Context())
			e.live.Wait()
			if len(logs.records(t, "live_session_dir_back")) != 1 {
				t.Fatal("the session's return was not logged")
			}
			if !strings.Contains(e.stored(), `"n":2`) {
				t.Fatalf("streaming did not resume once the folder was back: server holds %q", e.stored())
			}
		})
	}
}
