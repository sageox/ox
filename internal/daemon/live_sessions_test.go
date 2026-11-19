package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
)

type liveEnv struct {
	t           *testing.T
	srv         *livesessiontest.Server
	cfg         livesession.Config
	sessionsDir string
	dir         string
	live        *LiveSessions
	clock       time.Time
	params      livesession.OpenParams
}

type testLogWriter struct{ t *testing.T }

func (w testLogWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// newLiveEnv builds a daemon-side manager over a fake server and one session
// dir holding a pending stream state, the way a hook leaves it.
func newLiveEnv(t *testing.T) *liveEnv {
	t.Helper()
	srv := livesessiontest.New(t)
	e := &liveEnv{t: t, srv: srv, cfg: srv.Config(t), sessionsDir: t.TempDir(), clock: time.Now()}
	e.dir = filepath.Join(e.sessionsDir, "2026-03-04T13-41-person-a-Oxabc1")
	if err := os.MkdirAll(e.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(testLogWriter{t}, nil))
	e.live = NewLiveSessions(logger, e.sessionsDir, srv.URL)
	e.live.config = func(ep string) livesession.Config { c := e.cfg; c.Endpoint = ep; return c }
	e.live.now = func() time.Time { return e.clock }
	e.params = livesession.OpenParams{SessionID: sessionid.GenerateSessionID(), SessionName: filepath.Base(e.dir), RepoID: "repo_test", Agent: "claude-code", StartedAt: time.Now()}
	if _, err := livesession.Prepare(t.Context(), e.cfg, e.dir, e.params); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *liveEnv) spool(batch string) {
	e.t.Helper()
	if _, err := livesession.Spool(e.t.Context(), e.dir, []byte(batch), livesession.SpoolOptions{}); err != nil {
		e.t.Fatalf("spool: %v", err)
	}
}

func (e *liveEnv) state() livesession.State {
	e.t.Helper()
	st, err := livesession.ReadState(e.dir)
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func (e *liveEnv) stored() string {
	st := e.state()
	return string(e.srv.Stored(e.t, st.ConversationID, st.LayerID))
}

func (e *liveEnv) spooled() string {
	b, err := os.ReadFile(filepath.Join(e.dir, "spool.jsonl"))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

func lineN(n int) string { return fmt.Sprintf(`{"type":"msg","n":%d}`+"\n", n) }

func TestLiveIPCDrainOpensAPendingSessionAndDrainsIt(t *testing.T) {
	e := newLiveEnv(t)
	e.spool(lineN(1) + lineN(2))

	s := NewServer(slog.Default())
	s.SetLiveSessionDrainHandler(func(p LiveSessionDrainPayload) { e.live.Drain(t.Context(), p.SessionPath) })
	raw, _ := json.Marshal(LiveSessionDrainPayload{SessionPath: e.dir})
	res := handleLiveSessionDrain(s, Message{Type: MsgTypeLiveSessionDrain, Payload: raw}, nil)
	if !res.SkipDefault || res.Response != nil {
		t.Fatalf("a fire-and-forget handler must not reply: %+v", res)
	}
	e.live.Wait()

	if st := e.state(); st.Status != livesession.StatusStreaming {
		t.Fatalf("status = %s, want streaming after the first drain", st.Status)
	}
	if got, want := e.stored(), e.spooled(); got != want || got == "" {
		t.Fatalf("server holds %q, want the spool %q", got, want)
	}
}

func TestLiveIPCDrainRefusesAPathOutsideTheSessionsDir(t *testing.T) {
	e := newLiveEnv(t)
	e.spool(lineN(1))
	elsewhere := filepath.Join(t.TempDir(), "victim")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := livesession.Prepare(t.Context(), e.cfg, elsewhere, livesession.OpenParams{SessionID: sessionid.GenerateSessionID()}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{elsewhere, filepath.Join(e.dir, "..", "..", filepath.Base(elsewhere)), e.sessionsDir} {
		e.live.Drain(t.Context(), p)
	}
	e.live.Wait()
	if n := len(e.srv.Calls()); n != 0 {
		t.Fatalf("an IPC peer steered %d server calls at a path the daemon does not own", n)
	}
}

func TestLiveTickFinishesAClosingSessionAfterTheServerComesBack(t *testing.T) {
	e := newLiveEnv(t)
	e.spool(lineN(1) + lineN(2))
	if _, err := livesession.Open(t.Context(), e.cfg, e.dir, e.params); err != nil {
		t.Fatal(err)
	}
	e.srv.SetDown(true)
	if _, err := livesession.Close(t.Context(), e.cfg, e.dir, "", 300*time.Millisecond); err == nil {
		t.Fatal("close succeeded against a down server")
	}
	if st := e.state(); st.Status != livesession.StatusClosing {
		t.Fatalf("status = %s, want closing", st.Status)
	}

	e.live.Tick(t.Context())
	e.live.Wait()
	if st := e.state(); st.Status != livesession.StatusClosing {
		t.Fatalf("status = %s while the server is still down", st.Status)
	}

	e.srv.SetDown(false)
	e.clock = e.clock.Add(liveBackoffBase + time.Second) // past the first backoff
	e.live.Tick(t.Context())
	e.live.Wait()

	st := e.state()
	if st.Status != livesession.StatusClosed {
		t.Fatalf("status = %s, want closed once the server is back", st.Status)
	}
	if _, ok := e.srv.ClosedWith(st.ConversationID); !ok {
		t.Fatal("the server never saw the close")
	}
}

func TestLiveBackoffSpacesRetriesAndCaps(t *testing.T) {
	e := newLiveEnv(t)
	e.spool(lineN(1))
	e.srv.SetDown(true)

	attempts := func() int { return e.srv.CallsTo("POST", "/keys") }
	e.live.Tick(t.Context())
	e.live.Wait()
	first := attempts()
	if first == 0 {
		t.Fatal("the first tick never tried the server")
	}
	e.live.Tick(t.Context()) // immediately again: inside the backoff window
	e.live.Wait()
	if got := attempts(); got != first {
		t.Fatalf("retried inside the backoff window: %d -> %d attempts", first, got)
	}

	delay := time.Duration(0)
	for range 12 {
		e.clock = e.clock.Add(10 * time.Minute)
		e.live.Tick(t.Context())
		e.live.Wait()
		e.live.mu.Lock()
		delay = e.live.backoff[canonicalDir(e.dir)].delay
		e.live.mu.Unlock()
	}
	if delay != liveBackoffCap {
		t.Fatalf("backoff delay = %s after many failures, want the %s cap", delay, liveBackoffCap)
	}
}

// reconnectNoticeBudget is how long after SageOx comes back the daemon may take
// to deliver what it spooled during an outage. ADR-247 D6 promises a teammate
// sees a line within 60 s of it being written; about 15 s of that belongs to the
// server's notice and the web page's fetch, so the client gets the rest.
const reconnectNoticeBudget = 45 * time.Second

// TestLiveDeliversAfterALongOutageWithinTheVisibilityBudget is the failure the
// "Marcus keeps working through a network outage" scenario caught: backoff that
// doubled toward minutes left a backlog sitting long after the network was
// back, however long the outage lasted.
func TestLiveDeliversAfterALongOutageWithinTheVisibilityBudget(t *testing.T) {
	for _, outage := range []time.Duration{2 * time.Minute, 4 * time.Minute, 20 * time.Minute} {
		t.Run(outage.String(), func(t *testing.T) {
			e := newLiveEnv(t)
			e.spool(lineN(1))
			e.srv.SetDown(true)
			// the real daemon sweeps every liveTickInterval; so does this outage
			for elapsed := time.Duration(0); elapsed < outage; elapsed += liveTickInterval {
				e.live.Tick(t.Context())
				e.live.Wait()
				e.clock = e.clock.Add(liveTickInterval)
			}
			if st := e.state(); st.Acked != 0 {
				t.Fatalf("acked %d bytes while the server was down", st.Acked)
			}

			e.srv.SetDown(false)
			back := e.clock
			for {
				e.live.Tick(t.Context())
				e.live.Wait()
				// bound every tick, including the one that delivers: checked after
				// success, a delivery that came too late still passed
				if waited := e.clock.Sub(back); waited > reconnectNoticeBudget {
					t.Fatalf("after a %s outage the backlog was still undelivered %s after the server came back (budget %s)",
						outage, waited, reconnectNoticeBudget)
				}
				if st := e.state(); st.Status == livesession.StatusStreaming && st.Acked > 0 {
					break
				}
				e.clock = e.clock.Add(liveTickInterval)
			}
			if got := e.stored(); got != lineN(1) {
				t.Fatalf("server holds %q, want the spooled line once", got)
			}
		})
	}
}

func TestLiveConcurrentDrainRequestsForOneSessionAreCoalesced(t *testing.T) {
	e := newLiveEnv(t)
	for i := range 40 {
		e.spool(lineN(i))
	}
	e.srv.Latency(20 * time.Millisecond)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() { defer wg.Done(); e.live.Drain(t.Context(), e.dir) }()
	}
	wg.Wait()
	e.live.Wait()
	// the live lock alone would keep requests from overlapping; coalescing is what
	// stops 16 requests from each re-registering and re-opening the session
	if got := e.srv.CallsTo("POST", "/keys"); got != 1 {
		t.Fatalf("session was opened %d times for 16 simultaneous drain requests, want 1 (coalesced per session)", got)
	}
	if e.stored() != e.spooled() {
		t.Fatal("server does not hold the whole spool")
	}
}

// The daemon's capture paths spool through the shared step, then ask for a
// drain (the wiring daemon.go sets up); a paused batch never reaches the server.
func TestLiveCaptureFromTheTailWatcherSpoolsAndDrainsButNeverPausedBatches(t *testing.T) {
	e := newLiveEnv(t)
	rec := &session.RecordingState{SessionID: e.params.SessionID, SessionPath: e.dir}
	capture := func(b session.AppendedBatch) {
		spooled, err := livesession.SpoolCommitted(t.Context(), "", rec, b)
		if err != nil {
			t.Fatalf("spool committed batch: %v", err)
		}
		if spooled {
			e.live.Spooled(t.Context(), e.dir)
		}
	}
	capture(session.AppendedBatch{Bytes: []byte(lineN(1))})
	capture(session.AppendedBatch{Bytes: []byte(`{"type":"msg","content":"PAUSED-SECRET"}` + "\n"), Suspended: true})
	capture(session.AppendedBatch{Bytes: []byte(lineN(3))})
	e.live.Wait()

	got := e.stored()
	if strings.Contains(got, "PAUSED-SECRET") || !bytes.Contains([]byte(got), []byte(`"n":3`)) {
		t.Fatalf("server holds %q", got)
	}
}

func TestLiveTickRetriesQueuedAbortDeletes(t *testing.T) {
	e := newLiveEnv(t)
	e.spool(lineN(1))
	st, err := livesession.Open(context.Background(), e.cfg, e.dir, e.params)
	if err != nil {
		t.Fatal(err)
	}
	e.srv.SetDown(true)
	res, err := livesession.Abort(t.Context(), e.cfg, e.dir, 300*time.Millisecond)
	if err != nil || !res.Queued {
		t.Fatalf("abort offline: %+v %v, want queued", res, err)
	}
	e.srv.SetDown(false)
	e.live.Tick(t.Context())
	if !e.srv.IsDeleted(st.ConversationID) {
		t.Fatal("the tick did not retry the queued delete")
	}
}
