package daemon

// Adversarial tests for the daemon's live-session tick (ADR-247): liveness
// under outages and server backoff, coalescing of hook kicks, and a session
// folder that disappears between ticks. Time is the injected clock; every
// tick is awaited (Wait) before the next, so schedules are deterministic.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/sessionid"
)

// advRecorder keeps every log record so a test can assert a failure was loud.
type advRecorder struct {
	mu      sync.Mutex
	records []slog.Record
	t       *testing.T
}

func (r *advRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *advRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec.Clone())
	return nil
}
func (r *advRecorder) WithAttrs(attrs []slog.Attr) slog.Handler { return &advWithAttrs{r, attrs} }
func (r *advRecorder) WithGroup(string) slog.Handler            { return r }

type advWithAttrs struct {
	*advRecorder
	attrs []slog.Attr
}

func (w *advWithAttrs) Handle(ctx context.Context, rec slog.Record) error {
	rec = rec.Clone()
	rec.AddAttrs(w.attrs...)
	return w.advRecorder.Handle(ctx, rec)
}

// loud reports whether a WARN-or-worse record mentions needle in its message
// or any attribute value.
func (r *advRecorder) loud(needle string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.records {
		if rec.Level < slog.LevelWarn {
			continue
		}
		found := strings.Contains(rec.Message, needle)
		rec.Attrs(func(a slog.Attr) bool {
			found = found || strings.Contains(a.Value.String(), needle)
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

type advDaemon struct {
	t           *testing.T
	srv         *livesessiontest.Server
	cfg         livesession.Config
	sessionsDir string
	dir         string
	live        *LiveSessions
	log         *advRecorder
	clock       time.Time
	st          livesession.State
}

// newAdvDaemon builds the manager over a symlink-free sessions folder (the
// path-spelling case has its own test below).
func newAdvDaemon(t *testing.T) *advDaemon {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return newAdvDaemonAt(t, filepath.Join(base, "sessions"))
}

func newAdvDaemonAt(t *testing.T, sessionsDir string) *advDaemon {
	t.Helper()
	srv := livesessiontest.New(t)
	d := &advDaemon{t: t, srv: srv, cfg: srv.Config(t), sessionsDir: sessionsDir,
		clock: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC), log: &advRecorder{t: t}}
	d.dir = filepath.Join(d.sessionsDir, "2026-11-18T09-00-person-a-OxAdv2")
	if err := os.MkdirAll(d.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	d.live = NewLiveSessions(slog.New(d.log), d.sessionsDir, srv.URL)
	d.live.config = func(ep string) livesession.Config { c := d.cfg; c.Endpoint = ep; return c }
	d.live.now = func() time.Time { return d.clock }
	st, err := livesession.Prepare(t.Context(), d.cfg, d.dir, livesession.OpenParams{
		SessionID: sessionid.GenerateSessionID(), SessionName: filepath.Base(d.dir), RepoID: "repo_test",
		Agent: "claude-code", StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	d.st = st
	return d
}

// spool adds turns from..from+n. Turn 0 is the human prompt every session
// opens with: a stream opens only once its spool holds one.
func (d *advDaemon) spool(from, n int) {
	d.t.Helper()
	var b bytes.Buffer
	for i := from; i < from+n; i++ {
		typ := "assistant"
		if i == 0 {
			typ = "user"
		}
		fmt.Fprintf(&b, `{"type":%q,"seq":%d,"text":"line %d ✓"}`+"\n", typ, i, i)
	}
	if _, err := livesession.Spool(d.t.Context(), d.dir, b.Bytes(), livesession.SpoolOptions{}); err != nil {
		d.t.Fatal(err)
	}
}

// tick advances the clock and runs one tick to completion.
func (d *advDaemon) tick(advance time.Duration) {
	d.clock = d.clock.Add(advance)
	d.live.Tick(d.t.Context())
	d.live.Wait()
}

func (d *advDaemon) drained() bool {
	d.t.Helper()
	st, err := livesession.ReadState(d.dir)
	if err != nil {
		return false
	}
	spool, _ := os.ReadFile(filepath.Join(d.dir, "spool.jsonl"))
	return bytes.Equal(d.srv.Stored(d.t, st.ConversationID, st.LayerID), spool)
}

func (d *advDaemon) appends() int { return d.srv.CallsTo(http.MethodPost, "/lines") }

// Property: whatever the outage length and however hooks kick the daemon
// meanwhile, (a) a down server sees at most one append attempt per session
// per backoff step, never one per kick, and (b) once it is back the backlog
// is fully sent within the backoff cap plus one tick (ADR-247 D6: visible
// within 60 s).
func TestAdvDaemonOutageNeverHammersAndRecoversWithinTheCap(t *testing.T) {
	for seed := uint64(1); seed <= 8; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			r := rand.New(rand.NewPCG(seed, 7))
			d := newAdvDaemon(t)
			d.spool(0, 5)
			d.tick(0) // opens and drains
			if !d.drained() {
				t.Fatal("precondition: the first tick drains")
			}
			d.srv.SetDown(true)
			outageTicks := 1 + r.IntN(8)
			before := d.appends()
			for i := range outageTicks {
				d.spool(100*(i+1), 3)
				for range r.IntN(20) { // hook kicks between ticks
					d.live.Drain(t.Context(), d.dir)
				}
				d.live.Wait()
				d.tick(liveTickInterval)
			}
			if attempts := d.appends() - before; attempts > outageTicks {
				t.Fatalf("%d append attempts in %d ticks of outage: kicks bypassed the backoff", attempts, outageTicks)
			}
			d.srv.SetDown(false)
			budget := int(liveBackoffCap/liveTickInterval) + 1
			for i := 0; i <= budget && !d.drained(); i++ {
				d.tick(liveTickInterval)
			}
			if !d.drained() {
				t.Fatalf("backlog not sent within %d ticks of the server coming back", budget)
			}
		})
	}
}

// A 429 with Retry-After is honored on the daemon's clock: no append before
// the server's wait has passed, then the retry happens on the next tick.
func TestAdvDaemonHonorsRetryAfter(t *testing.T) {
	d := newAdvDaemon(t)
	d.spool(0, 3)
	d.tick(0)
	d.spool(3, 3)
	const wait = 45 * time.Second
	d.srv.RateLimit(1, wait.Milliseconds())
	d.tick(0) // refused 429
	refusedAt := d.appends()
	for elapsed := liveTickInterval; elapsed < wait; elapsed += liveTickInterval {
		d.tick(liveTickInterval)
		d.live.Drain(t.Context(), d.dir) // a hook kick must not jump the wait either
		d.live.Wait()
		if d.appends() != refusedAt {
			t.Fatalf("appended %s after a 429 that asked for %s", elapsed, wait)
		}
	}
	d.tick(liveTickInterval)
	if !d.drained() {
		t.Fatalf("not retried on the first tick after Retry-After elapsed")
	}
}

// Many hook kicks while a drain is in flight coalesce into at most one rerun:
// never two requests in flight for one session.
func TestAdvDaemonCoalescesKicksWhileADrainRuns(t *testing.T) {
	d := newAdvDaemon(t)
	d.spool(0, 3)
	d.tick(0)
	d.srv.Latency(30 * time.Millisecond)
	d.spool(3, 20)
	d.log.mu.Lock()
	logged := len(d.log.records)
	d.log.mu.Unlock()
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() { defer wg.Done(); d.live.Drain(t.Context(), d.dir) }()
	}
	wg.Wait()
	d.live.Wait()
	d.srv.Latency(0)
	if n := d.srv.MaxConcurrent(); n > 1 {
		t.Fatalf("%d requests in flight at once for one session", n)
	}
	// live.lock alone would serialize redundant drains; coalescing means they
	// never start: one step for the kicks plus at most one rerun
	steps := 0
	d.log.mu.Lock()
	for _, rec := range d.log.records[logged:] {
		if strings.HasPrefix(rec.Message, "live_session_step_") {
			steps++
		}
	}
	d.log.mu.Unlock()
	if steps > 2 {
		t.Fatalf("40 kicks ran %d drain steps, want at most 2", steps)
	}
	if !d.drained() {
		t.Fatal("kicks did not drain the spool")
	}
}

// A streaming session's folder is moved aside between ticks (the Ledger
// clone self-heal moved a half-made Ledger, sessions and all). The tick no
// longer finds it, so without a report the daemon simply goes quiet while the
// server keeps a session that never closes. It must be reported loudly.
func TestAdvDaemonReportsASessionFolderMovedAsideBetweenTicks(t *testing.T) {
	for _, whole := range []bool{false, true} {
		name := "one session folder"
		if whole {
			name = "the whole sessions folder"
		}
		t.Run(name, func(t *testing.T) {
			d := newAdvDaemon(t)
			d.spool(0, 4)
			d.tick(0)
			st, err := livesession.ReadState(d.dir)
			if err != nil || st.Status != livesession.StatusStreaming {
				t.Fatalf("precondition: streaming, got %+v %v", st, err)
			}
			d.spool(4, 4) // turns still unsent when the folder goes
			aside := filepath.Join(filepath.Dir(d.sessionsDir), "aside")
			from := d.dir
			if whole {
				from = d.sessionsDir
			}
			if err := os.Rename(from, aside); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				d.tick(liveTickInterval)
			}
			if !d.log.loud(st.ConversationID) && !d.log.loud(filepath.Base(d.dir)) {
				t.Fatalf("a streaming session (%s) vanished with unsent turns and the daemon said nothing at WARN or above", st.ConversationID)
			}
		})
	}
}

// The same session reached by two spellings of its path: the tick scans
// sessionsDir as configured, a hook kick names the folder and is resolved
// through symlinks (trusted). If the daemon keys its running, rerun and
// backoff state on the spelling, a kick and a tick are two sessions to it:
// kicks skip the tick's backoff and both can run at once. A symlink anywhere
// in the cache path (a symlinked home or cache folder) is enough.
func TestAdvDaemonKickAndTickAreOneSessionWhateverThePathSpelling(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "real-cache")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "cache-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	d := newAdvDaemonAt(t, filepath.Join(link, "sessions"))
	d.spool(0, 3)
	d.tick(0)
	d.srv.SetDown(true)
	d.spool(3, 3)
	d.tick(liveTickInterval) // fails: the tick's session is now backing off
	before := d.appends()
	for range 5 {
		d.live.Drain(t.Context(), d.dir) // hooks kick right after the failure
	}
	d.live.Wait()
	if n := d.appends() - before; n != 0 {
		t.Fatalf("%d appends from hook kicks while the same session was backing off: the kick path keys the session as %q, the tick as %q",
			n, advResolved(t, d.dir), d.dir)
	}
}

func advResolved(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return r
}
