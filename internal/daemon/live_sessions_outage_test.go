package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/livesession"
)

// testClock is a goroutine-safe fake clock: drain goroutines read it while the
// fake network charges it for the time a failed request would take.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// outageLink is the network between the daemon and SageOx. While down, every
// request fails the way the chosen outage fails and charges the fake clock what
// that failure costs in production, without the test waiting for it.
type outageLink struct {
	base http.RoundTripper
	clk  *testClock
	fail func() error
	cost time.Duration

	mu       sync.Mutex
	down     bool
	failures int
}

func (o *outageLink) failed() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.failures
}

func (o *outageLink) setDown(down bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.down = down
}

func (o *outageLink) RoundTrip(r *http.Request) (*http.Response, error) {
	o.mu.Lock()
	down := o.down
	if down {
		o.failures++
	}
	o.mu.Unlock()
	if !down {
		return o.base.RoundTrip(r)
	}
	o.clk.Advance(o.cost)
	return nil, o.fail()
}

// logSink captures the daemon's JSON log lines for assertions.
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// records decodes every line logged with msg.
func (s *logSink) records(t *testing.T, msg string) []map[string]any {
	t.Helper()
	s.mu.Lock()
	raw := append([]byte(nil), s.buf.Bytes()...)
	s.mu.Unlock()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("daemon log line is not JSON: %q: %v", sc.Text(), err)
		}
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

func (e *liveEnv) captureLogs() *logSink {
	sink := &logSink{}
	e.live.logger = slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return sink
}

// productionRequestTimeout is livesession's per-request bound: what one failed
// request costs when the link swallows packets instead of refusing them.
const productionRequestTimeout = 10 * time.Second

// TestLiveRetriesOnTheTickTheBackoffEnds pins the retry spacing during a long
// outage, when only the 15 s tick drives attempts (the person stopped typing,
// so no hook kicks the daemon). The backoff deadline must be measured from when
// the attempt started, and a tick that lands a hair before it must still
// retry. Stamped at the attempt's END, the tick exactly one cap later always
// looked early, so every retry waited a whole extra tick: a 30 s cap behaved
// like 45 s, and the backlog sat unsent for up to 45 s after the link returned.
func TestLiveRetriesOnTheTickTheBackoffEnds(t *testing.T) {
	cases := []struct {
		name string
		cost time.Duration
		fail func() error
	}{
		{
			name: "connection refused",
			cost: time.Millisecond,
			fail: func() error {
				return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
			},
		},
		{
			name: "link accepts and never answers",
			cost: productionRequestTimeout,
			fail: func() error {
				return fmt.Errorf("awaiting response headers: %w", context.DeadlineExceeded)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newLiveEnv(t)
			clk := &testClock{t: e.clock}
			e.live.now = clk.Now
			link := &outageLink{base: http.DefaultTransport, clk: clk, fail: tc.fail, cost: tc.cost}
			e.cfg.HTTP = &http.Client{Timeout: 5 * time.Second, Transport: link}

			// ticks keep a fixed phase with a few ms of scheduling jitter either way,
			// the way a time.Ticker fires
			start := clk.Now()
			// period 3, so a tick and the one a backoff later never share a phase
			tickAt := func(n int) time.Time {
				jitter := time.Duration(n%3-1) * 3 * time.Millisecond
				return start.Add(time.Duration(n)*liveTickInterval + jitter)
			}
			attempts := func() int { return len(e.srv.Calls()) + link.failed() }
			tick := func(n int) (attempted bool) {
				clk.Set(tickAt(n))
				before := attempts()
				e.live.Tick(t.Context())
				e.live.Wait()
				return attempts() != before
			}

			e.spool(lineN(1))
			tick(0)
			if st := e.state(); st.Status != livesession.StatusStreaming || st.Acked == 0 {
				t.Fatalf("before the outage: %+v", st)
			}

			e.spool(lineN(2))
			link.setDown(true)
			n := 1
			lastFailed := -1
			for ; n < 1+16; n++ { // four minutes of ticks with the link down
				if tick(n) {
					lastFailed = n
				}
			}
			if lastFailed < 0 {
				t.Fatal("no attempt reached the network during the outage")
			}
			// the worst moment to reconnect: right after an attempt failed
			for !tick(n) {
				n++
			}
			lastFailed = n
			link.setDown(false)

			for n++; ; n++ {
				tick(n)
				if strings.Contains(e.stored(), `"n":2`) {
					break
				}
				if waited := tickAt(n).Sub(tickAt(lastFailed)); waited > liveBackoffCap+liveTickInterval {
					t.Fatalf("backlog still unsent %s after the link returned", waited)
				}
			}
			if waited := tickAt(n).Sub(tickAt(lastFailed)); waited > liveBackoffCap+time.Second {
				t.Fatalf("the first retry after the link returned came %s after the last failed attempt, want at most the %s cap",
					waited.Round(time.Second), liveBackoffCap)
			}
		})
	}
}

// TestLiveAWedgedAttemptIsReported is the BDD outage's signature: four fast
// failures, then no line about the session at all while the daemon kept
// running, because one attempt never returned and held the session's slot. No
// budget reaches a wedge like that, so the next requests must say so.
func TestLiveAWedgedAttemptIsReported(t *testing.T) {
	e := newLiveEnv(t)
	logs := e.captureLogs()
	clk := &testClock{t: e.clock}
	e.live.now = clk.Now
	e.spool(lineN(1))

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	token := e.cfg.Token
	e.cfg.Token = func(ctx context.Context, ep string) (string, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release // ignores ctx: a wedge no deadline can end
		return token(ctx, ep)
	}
	e.live.Tick(t.Context())
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the attempt never started")
	}

	clk.Advance(liveTickInterval)
	e.live.Tick(t.Context())
	if got := logs.records(t, "live_session_step_stuck"); len(got) != 0 {
		t.Fatalf("an attempt one tick old was reported stuck: %v", got)
	}

	clk.Advance(liveStepStuckAfter)
	e.live.Tick(t.Context())
	clk.Advance(liveTickInterval)
	e.live.Tick(t.Context())

	got := logs.records(t, "live_session_step_stuck")
	close(release)
	e.live.Wait()
	if len(got) != 1 {
		t.Fatalf("logged live_session_step_stuck %d times for one wedged attempt, want once: %v", len(got), got)
	}
	st := e.state()
	if got[0]["conversation_id"] != st.ConversationID || got[0]["running_ms"] == nil {
		t.Fatalf("stuck report lacks the session or the time it has run: %v", got[0])
	}
}

// TestLiveStepNeverEndsSilently: every way a drain step can end without
// sending is logged, so "the daemon stopped trying" is never invisible.
func TestLiveStepNeverEndsSilently(t *testing.T) {
	t.Run("unreadable stream state", func(t *testing.T) {
		e := newLiveEnv(t)
		logs := e.captureLogs()
		if err := os.WriteFile(filepath.Join(e.dir, "stream-state.json"), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		e.live.Drain(t.Context(), e.dir)
		e.live.Wait()
		got := logs.records(t, "live_session_step_skipped")
		if len(got) != 1 || got[0]["reason"] != "state_unreadable" {
			t.Fatalf("skipped records = %v, want one state_unreadable", got)
		}
	})
	t.Run("drain ends with bytes left and no error", func(t *testing.T) {
		e := newLiveEnv(t)
		logs := e.captureLogs()
		e.spool(lineN(1))
		e.live.Tick(t.Context())
		e.live.Wait()
		// a line longer than one batch can never be sent; Spool gaps such lines,
		// so only a damaged spool holds one
		long := `{"type":"msg","pad":"` + strings.Repeat("x", 300<<10) + `"}` + "\n"
		f, err := os.OpenFile(filepath.Join(e.dir, "spool.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(long); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		e.live.Drain(t.Context(), e.dir)
		e.live.Wait()
		if got := logs.records(t, "live_session_step_partial"); len(got) != 1 {
			t.Fatalf("partial records = %v, want one", got)
		}
	})
}

// TestLiveCloseThatLostTheLockStillHappens is the "finished session still
// reads live" symptom: session stop (or the SessionEnd hook) asked for the
// close while a daemon drain held the session, gave up waiting within its
// budget, and nothing remembered that stop had happened. The daemon kept the
// stream open forever.
func TestLiveCloseThatLostTheLockStillHappens(t *testing.T) {
	e := newLiveEnv(t)
	for i := range 4 {
		e.spool(lineN(i))
	}
	st, err := livesession.Open(t.Context(), e.cfg, e.dir, e.params)
	if err != nil {
		t.Fatal(err)
	}

	// a daemon drain is mid-request on a slow link when the person ends the session
	e.srv.Latency(1500 * time.Millisecond)
	e.live.Drain(t.Context(), e.dir)
	deadline := time.Now().Add(10 * time.Second)
	for e.srv.InFlight() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the daemon drain never reached the server")
		}
		time.Sleep(10 * time.Millisecond)
	}
	res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", 200*time.Millisecond)
	var lockTimeout *fileutil.ErrLockTimeout
	if res.Done || !(errors.As(err, &lockTimeout) || errors.Is(err, context.DeadlineExceeded)) {
		t.Fatalf("close = %+v, %v; want it to give up waiting for the session the drain holds", res, err)
	}
	e.live.Wait()
	e.srv.Latency(0)

	e.clock = e.clock.Add(liveBackoffCap + liveTickInterval)
	e.live.Tick(t.Context())
	e.live.Wait()

	if got := e.state(); got.Status != livesession.StatusClosed {
		t.Fatalf("status = %s after the daemon's next tick, want closed", got.Status)
	}
	if _, ok := e.srv.ClosedWith(st.ConversationID); !ok {
		t.Fatal("the server never saw the close")
	}
}
