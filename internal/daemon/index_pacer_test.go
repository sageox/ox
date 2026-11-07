package daemon

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/codedb/index"
)

// compile-time proof the pacer plugs into the indexer's hook unchanged.
var _ = index.IndexOptions{Pace: (*indexPacer)(nil).Pace}

// --- A. Policy: nextPaceDelay ---

var (
	paceSigCalm         = paceSignals{LoadKnown: true, LoadPerCore: 0.1, UserIdleKnown: true, UserIdle: time.Hour}
	paceSigHot          = paceSignals{LoadKnown: true, LoadPerCore: 1.5, UserIdleKnown: true, UserIdle: time.Hour}
	paceSigBusyLoad     = paceSignals{LoadKnown: true, LoadPerCore: 0.8, UserIdleKnown: true, UserIdle: time.Hour}
	paceSigBusyUser     = paceSignals{LoadKnown: true, LoadPerCore: 0.1, UserIdleKnown: true, UserIdle: 30 * time.Second}
	paceSigBattery      = paceSignals{LoadKnown: true, LoadPerCore: 0.1, OnBattery: true, UserIdleKnown: true, UserIdle: time.Hour}
	paceSigAllUnknown   = paceSignals{}
	paceSigUnknownNoise = paceSignals{LoadPerCore: 9, UserIdle: 0} // values set but flagged unknown
)

// TestNextPaceDelay pins the whole policy table.
// Failure prevented: a refactor that quietly changes a threshold, so a backfill
// either cooks a laptop (too eager) or never finishes (too timid).
func TestNextPaceDelay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		prev time.Duration
		sig  paceSignals
		want time.Duration
	}{
		// hot: doubles, 1s floor, 60s cap
		{"hot from zero starts at floor", 0, paceSigHot, time.Second},
		{"hot doubles 1s", time.Second, paceSigHot, 2 * time.Second},
		{"hot doubles 8s", 8 * time.Second, paceSigHot, 16 * time.Second},
		{"hot doubles 16s", 16 * time.Second, paceSigHot, 32 * time.Second},
		{"hot caps at 60s", 32 * time.Second, paceSigHot, time.Minute},
		{"hot stays at cap", time.Minute, paceSigHot, time.Minute},
		{"hot clamps a corrupt huge prev", 10 * time.Hour, paceSigHot, time.Minute},
		{"hot lifts a sub-floor prev to the floor", 100 * time.Millisecond, paceSigHot, time.Second},
		{"hot at exactly 1.0 load per core", 0, paceSignals{LoadKnown: true, LoadPerCore: 1.0}, time.Second},
		{"battery alone is hot", 0, paceSigBattery, time.Second},
		{"battery doubles", 4 * time.Second, paceSigBattery, 8 * time.Second},
		{"battery beats a calm machine and an idle user", 2 * time.Second, paceSigBattery, 4 * time.Second},
		{"hot via load beats busy user", 2 * time.Second, paceSignals{LoadKnown: true, LoadPerCore: 2, UserIdleKnown: true, UserIdle: 0}, 4 * time.Second},

		// busy: max(prev/2, 500ms), never zero
		{"busy from zero starts at floor", 0, paceSigBusyLoad, 500 * time.Millisecond},
		{"busy lifts below-floor prev", 200 * time.Millisecond, paceSigBusyLoad, 500 * time.Millisecond},
		{"busy holds floor", 500 * time.Millisecond, paceSigBusyLoad, 500 * time.Millisecond},
		{"busy 600ms halves to 300ms but floor wins", 600 * time.Millisecond, paceSigBusyLoad, 500 * time.Millisecond},
		{"busy halves 8s", 8 * time.Second, paceSigBusyLoad, 4 * time.Second},
		{"busy eases from the 60s cap", time.Minute, paceSigBusyLoad, 30 * time.Second},
		{"busy via active user", 0, paceSigBusyUser, 500 * time.Millisecond},
		{"busy at exactly 0.7 load per core", 0, paceSignals{LoadKnown: true, LoadPerCore: 0.7}, 500 * time.Millisecond},
		{"just under 0.7 is calm", 0, paceSignals{LoadKnown: true, LoadPerCore: 0.69}, 0},
		{"just under 1.0 is busy not hot", 8 * time.Second, paceSignals{LoadKnown: true, LoadPerCore: 0.99}, 4 * time.Second},
		{"user idle just under 2m is busy", 0, paceSignals{UserIdleKnown: true, UserIdle: 2*time.Minute - time.Nanosecond}, 500 * time.Millisecond},
		{"user idle exactly 2m is calm", 0, paceSignals{UserIdleKnown: true, UserIdle: 2 * time.Minute}, 0},

		// calm: halve, snap to zero below 250ms
		{"calm from zero stays zero", 0, paceSigCalm, 0},
		{"calm halves 60s", time.Minute, paceSigCalm, 30 * time.Second},
		{"calm halves 1s", time.Second, paceSigCalm, 500 * time.Millisecond},
		{"calm keeps exactly 250ms", 500 * time.Millisecond, paceSigCalm, 250 * time.Millisecond},
		{"calm snaps 125ms to zero", 250 * time.Millisecond, paceSigCalm, 0},
		{"calm 498ms halves to 249ms and snaps", 498 * time.Millisecond, paceSigCalm, 0},
		{"calm snaps tiny prev", time.Millisecond, paceSigCalm, 0},

		// unknown signals never slow the pacer
		{"all unknown from zero is calm", 0, paceSigAllUnknown, 0},
		{"all unknown decays", 4 * time.Second, paceSigAllUnknown, 2 * time.Second},
		{"unknown flag masks a hot-looking load value", 0, paceSigUnknownNoise, 0},
		{"unknown load with known active user is busy", 0, paceSignals{UserIdleKnown: true, UserIdle: time.Second}, 500 * time.Millisecond},

		// defensive: a negative prev is treated as zero
		{"negative prev hot", -time.Second, paceSigHot, time.Second},
		{"negative prev calm", -time.Second, paceSigCalm, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, nextPaceDelay(tt.prev, tt.sig))
		})
	}
}

// TestNextPaceDelay_Trajectories checks the feedback loop end to end.
// Failure prevented: a policy that is right per step but never recovers
// (stuck throttled after the load clears) or never reaches the cap.
func TestNextPaceDelay_Trajectories(t *testing.T) {
	t.Parallel()

	run := func(prev time.Duration, sig paceSignals, steps int) []time.Duration {
		out := make([]time.Duration, 0, steps)
		for range steps {
			prev = nextPaceDelay(prev, sig)
			out = append(out, prev)
		}
		return out
	}

	t.Run("sustained heat climbs to the cap and stays", func(t *testing.T) {
		t.Parallel()
		got := run(0, paceSigHot, 9)
		want := []time.Duration{
			1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
			16 * time.Second, 32 * time.Second, time.Minute, time.Minute, time.Minute,
		}
		assert.Equal(t, want, got)
	})

	t.Run("calm after the cap returns to zero and stays", func(t *testing.T) {
		t.Parallel()
		got := run(time.Minute, paceSigCalm, 12)
		assert.Equal(t, time.Duration(0), got[len(got)-1])
		zeroAt := -1
		for i, d := range got {
			if d == 0 && zeroAt < 0 {
				zeroAt = i
			}
			if zeroAt >= 0 {
				assert.Zero(t, d, "step %d must stay at zero once reached", i)
			}
		}
		assert.Equal(t, 7, zeroAt, "60s halves seven times before 234ms snaps to zero on the eighth checkpoint")
	})

	t.Run("busy never reaches zero", func(t *testing.T) {
		t.Parallel()
		for _, d := range run(time.Minute, paceSigBusyLoad, 20) {
			assert.GreaterOrEqual(t, d, paceBusyMinDelay)
		}
	})
}

// --- B. Sampler: caching, unknown signals, cpu normalization ---

type paceTestClock struct {
	mu sync.Mutex
	t  time.Time
}

func newPaceTestClock() *paceTestClock {
	return &paceTestClock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func (c *paceTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *paceTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type paceProbeCounter struct {
	batteryCalls, idleCalls int
	battery                 bool
	batteryOK               bool
	idle                    time.Duration
	idleOK                  bool
}

func (p *paceProbeCounter) probes(load float64, loadOK bool) hostProbes {
	return hostProbes{
		loadAverage: func() (float64, bool) { return load, loadOK },
		onBattery: func(context.Context) (bool, bool) {
			p.batteryCalls++
			return p.battery, p.batteryOK
		},
		userIdle: func(context.Context) (time.Duration, bool) {
			p.idleCalls++
			return p.idle, p.idleOK
		},
	}
}

// TestHostSampler_NormalizesLoadByCPUCount verifies load is divided by the
// injected CPU count, not GOMAXPROCS.
// Failure prevented: the daemon caps GOMAXPROCS at 2, so dividing by it would
// flag a calm 8-core laptop at load 2 as saturated and throttle it forever.
func TestHostSampler_NormalizesLoadByCPUCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		load     float64
		ncpu     int
		want     float64
		wantMode paceMode
	}{
		{"8 cores at load 2 is calm", 2, 8, 0.25, paceCalm},
		{"4 cores at load 2.8 is busy", 2.8, 4, 0.7, paceBusy},
		{"4 cores at load 4 is hot", 4, 4, 1.0, paceHot},
		{"zero cpu count is clamped to one", 0.5, 0, 0.5, paceCalm},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var pc paceProbeCounter
			s := newHostSampler(pc.probes(tt.load, true), tt.ncpu, newPaceTestClock().Now).sample(t.Context())
			require.True(t, s.LoadKnown)
			assert.InDelta(t, tt.want, s.LoadPerCore, 1e-9)
			// battery and idle probes report failure here, so only load decides
			assert.Equal(t, tt.wantMode, s.mode())
		})
	}
}

// TestHostSampler_UnknownWhenProbesMissingOrFailing covers "other OS" (no
// probes at all) and a failing probe.
// Failure prevented: a failed probe surfacing as an error or as a fabricated
// "hot" reading would stall or abort a backfill over a missing tool.
func TestHostSampler_UnknownWhenProbesMissingOrFailing(t *testing.T) {
	t.Parallel()

	t.Run("no probes at all", func(t *testing.T) {
		t.Parallel()
		s := newHostSampler(hostProbes{}, 8, newPaceTestClock().Now).sample(t.Context())
		assert.Equal(t, paceSignals{}, s)
		assert.Equal(t, paceCalm, s.mode())
	})

	t.Run("every probe fails", func(t *testing.T) {
		t.Parallel()
		pc := paceProbeCounter{battery: true, idle: time.Second} // values must be ignored when ok=false
		s := newHostSampler(pc.probes(5, false), 8, newPaceTestClock().Now).sample(t.Context())
		assert.Equal(t, paceSignals{}, s)
	})
}

// TestHostSampler_CachesSubprocessSignals pins the cache TTLs: battery 60s,
// idle 30s.
// Failure prevented: spawning pmset and ioreg at every 50-commit checkpoint.
func TestHostSampler_CachesSubprocessSignals(t *testing.T) {
	t.Parallel()

	clock := newPaceTestClock()
	pc := paceProbeCounter{battery: true, batteryOK: true, idle: 5 * time.Second, idleOK: true}
	sampler := newHostSampler(pc.probes(0.1, true), 4, clock.Now)

	first := sampler.sample(t.Context())
	assert.True(t, first.OnBattery)
	assert.Equal(t, 5*time.Second, first.UserIdle)
	assert.Equal(t, 1, pc.batteryCalls)
	assert.Equal(t, 1, pc.idleCalls)

	clock.Advance(29 * time.Second)
	sampler.sample(t.Context())
	assert.Equal(t, 1, pc.batteryCalls, "battery cached inside 60s")
	assert.Equal(t, 1, pc.idleCalls, "idle cached inside 30s")

	clock.Advance(1 * time.Second) // t+30s: idle expires, battery does not
	sampler.sample(t.Context())
	assert.Equal(t, 1, pc.batteryCalls)
	assert.Equal(t, 2, pc.idleCalls, "idle re-probed at 30s")

	// battery state changes; the stale cache still serves the old value
	pc.battery = false
	clock.Advance(29 * time.Second) // t+59s
	assert.True(t, sampler.sample(t.Context()).OnBattery, "stale until TTL")

	clock.Advance(1 * time.Second) // t+60s
	assert.False(t, sampler.sample(t.Context()).OnBattery, "fresh after TTL")
	assert.Equal(t, 2, pc.batteryCalls)
}

// TestHostSampler_CachesFailuresToo verifies a failing subprocess probe is not
// retried until its TTL expires.
// Failure prevented: a hung ioreg costing its full 2s timeout at every
// checkpoint for the whole backfill.
func TestHostSampler_CachesFailuresToo(t *testing.T) {
	t.Parallel()

	clock := newPaceTestClock()
	pc := paceProbeCounter{} // both subprocess probes fail
	sampler := newHostSampler(pc.probes(0.1, true), 4, clock.Now)

	for range 5 {
		s := sampler.sample(t.Context())
		assert.False(t, s.UserIdleKnown)
		assert.False(t, s.OnBattery)
		clock.Advance(5 * time.Second)
	}
	assert.Equal(t, 1, pc.batteryCalls)
	assert.Equal(t, 1, pc.idleCalls)

	// 25s elapsed; cross the idle TTL and the failure is retried, and a
	// recovered probe is picked up
	pc.idle, pc.idleOK = time.Minute, true
	clock.Advance(10 * time.Second)
	s := sampler.sample(t.Context())
	assert.True(t, s.UserIdleKnown)
	assert.Equal(t, time.Minute, s.UserIdle)
	assert.Equal(t, 2, pc.idleCalls)
}

// TestHostSampler_CancelledProbeIsNotCached verifies a probe interrupted by
// cancellation is retried next time instead of poisoning the cache.
// Failure prevented: one cancelled subprocess hiding a working battery signal
// for a full minute.
func TestHostSampler_CancelledProbeIsNotCached(t *testing.T) {
	t.Parallel()

	clock := newPaceTestClock()
	calls := 0
	probes := hostProbes{
		onBattery: func(context.Context) (bool, bool) {
			calls++
			return true, true
		},
	}
	sampler := newHostSampler(probes, 4, clock.Now)

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	s := sampler.sample(cancelled)
	assert.False(t, s.OnBattery, "result of a cancelled probe is discarded")

	s = sampler.sample(t.Context())
	assert.True(t, s.OnBattery, "next sample probes again")
	assert.Equal(t, 2, calls)
}

// --- C. Pacer: sleeping, cancellation, concurrency ---

// pacerHarness wires a pacer to scripted signals and a recording sleep.
type pacerHarness struct {
	pacer   *indexPacer
	clock   *paceTestClock
	sleeps  []time.Duration
	samples int
	signals []paceSignals // consumed in order; the last repeats
}

func newPacerHarness(logger *slog.Logger, signals ...paceSignals) *pacerHarness {
	h := &pacerHarness{pacer: newIndexPacer(logger), clock: newPaceTestClock(), signals: signals}
	h.pacer.sample = func(context.Context) paceSignals {
		i := min(h.samples, len(h.signals)-1)
		h.samples++
		return h.signals[i]
	}
	h.pacer.sleep = func(_ context.Context, d time.Duration) error {
		h.sleeps = append(h.sleeps, d)
		h.clock.Advance(d)
		return nil
	}
	h.pacer.now = h.clock.Now
	return h
}

// TestPace_SleepsFollowThePolicy runs the real Pace loop over scripted signals.
// Failure prevented: the policy being right in isolation but Pace feeding the
// wrong previous delay back in, or sleeping when the delay is zero.
func TestPace_SleepsFollowThePolicy(t *testing.T) {
	t.Parallel()

	h := newPacerHarness(nil,
		paceSigHot, paceSigHot, paceSigHot, // 1s 2s 4s
		paceSigCalm, paceSigCalm, paceSigCalm, paceSigCalm, paceSigCalm, // 2s 1s 500ms 250ms 0
		paceSigCalm,     // zero: no sleep
		paceSigBusyLoad, // 500ms
	)
	for range 10 {
		require.NoError(t, h.pacer.Pace(t.Context()))
	}

	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second,
		2 * time.Second, time.Second, 500 * time.Millisecond, 250 * time.Millisecond,
		500 * time.Millisecond,
	}
	assert.Equal(t, want, h.sleeps)
	assert.Equal(t, 10, h.samples)
}

// TestPace_CancelledContextReturnsBeforeSampling verifies a dead ctx short
// circuits Pace.
// Failure prevented: a cancelled index run still shelling out to pmset/ioreg
// and logging after it was told to stop.
func TestPace_CancelledContextReturnsBeforeSampling(t *testing.T) {
	t.Parallel()

	h := newPacerHarness(nil, paceSigHot)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := h.pacer.Pace(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, h.samples)
	assert.Empty(t, h.sleeps)
}

// TestPace_ContextCancelledWhileSampling verifies cancellation during the
// sample is reported even when the computed delay is zero.
// Failure prevented: Pace returning nil for a cancelled run, so the indexer
// keeps going for another 50 commits.
func TestPace_ContextCancelledWhileSampling(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	p := newIndexPacer(nil)
	p.sample = func(context.Context) paceSignals {
		cancel()
		return paceSigCalm
	}
	p.sleep = func(context.Context, time.Duration) error {
		t.Error("must not sleep after cancellation")
		return nil
	}
	assert.ErrorIs(t, p.Pace(ctx), context.Canceled)
}

// TestPace_SleepErrorIsReturned verifies the sleep error reaches the indexer.
// Failure prevented: swallowing the abort signal so cancellation is ignored.
func TestPace_SleepErrorIsReturned(t *testing.T) {
	t.Parallel()

	boom := errors.New("sleep failed")
	p := newIndexPacer(nil)
	p.sample = func(context.Context) paceSignals { return paceSigHot }
	p.sleep = func(context.Context, time.Duration) error { return boom }
	assert.ErrorIs(t, p.Pace(t.Context()), boom)
}

// TestPace_RealSleepHonorsContextCancel uses the production sleep under a fake
// clock: a 60s sleep must end the moment ctx is cancelled, not 60s later.
// Failure prevented: daemon shutdown blocked for up to a minute behind a
// sleeping backfill.
func TestPace_RealSleepHonorsContextCancel(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		p := newIndexPacer(nil)
		p.sample = func(context.Context) paceSignals { return paceSigHot }
		p.delay = 30 * time.Second // next hot step is the 60s cap

		ctx, cancel := context.WithCancel(t.Context())
		start := time.Now()
		done := make(chan error, 1)
		go func() { done <- p.Pace(ctx) }()

		synctest.Wait() // Pace is now durably blocked in its 60s sleep
		select {
		case err := <-done:
			t.Fatalf("Pace returned early: %v", err)
		default:
		}

		time.Sleep(5 * time.Second)
		cancel()
		synctest.Wait()

		err := <-done
		assert.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 5*time.Second, time.Since(start), "returned at cancel time, not after the full 60s")
	})
}

// TestPace_RealSleepLastsTheDelay verifies the production sleep waits exactly
// the computed delay.
// Failure prevented: a pacer that computes a delay but never actually waits it.
func TestPace_RealSleepLastsTheDelay(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		p := newIndexPacer(nil)
		p.sample = func(context.Context) paceSignals { return paceSigHot }
		start := time.Now()

		require.NoError(t, p.Pace(t.Context())) // 1s
		require.NoError(t, p.Pace(t.Context())) // 2s
		assert.Equal(t, 3*time.Second, time.Since(start))
	})
}

// TestPace_ConcurrentCallsAreSafe is meant for `go test -race`: a pacer shared
// by concurrent index runs must not race on its delay or log state.
// Failure prevented: data race / lost updates in the shared feedback state.
func TestPace_ConcurrentCallsAreSafe(t *testing.T) {
	t.Parallel()

	var sleepMu sync.Mutex
	var sleeps int
	p := newIndexPacer(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	p.sample = func(context.Context) paceSignals { return paceSigHot }
	p.sleep = func(context.Context, time.Duration) error {
		sleepMu.Lock()
		defer sleepMu.Unlock()
		sleeps++
		return nil
	}

	const workers, calls = 8, 20
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range calls {
				assert.NoError(t, p.Pace(t.Context()))
			}
		})
	}
	wg.Wait()

	assert.Equal(t, workers*calls, sleeps)
	p.mu.Lock()
	defer p.mu.Unlock()
	assert.Equal(t, paceHotMaxDelay, p.delay, "160 hot steps saturate at the cap")
}

// --- D. Logging ---

type paceCaptureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *paceCaptureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *paceCaptureHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *paceCaptureHandler) WithGroup(string) slog.Handler            { return h }
func (h *paceCaptureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *paceCaptureHandler) messages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.records))
	for _, r := range h.records {
		out = append(out, r.Message)
	}
	return out
}

// TestPace_LogsTransitionsAndRateLimitsWhilePaced pins the logging contract:
// every zero<->nonzero transition, otherwise at most once per 5 minutes.
// Failure prevented: a day-long backfill writing one log line per checkpoint,
// or going silent so nobody can tell why indexing is slow.
func TestPace_LogsTransitionsAndRateLimitsWhilePaced(t *testing.T) {
	t.Parallel()

	capture := &paceCaptureHandler{}
	h := newPacerHarness(slog.New(capture))
	pace := func(sig paceSignals, advance time.Duration) {
		t.Helper()
		h.signals = []paceSignals{sig}
		h.samples = 0
		h.clock.Advance(advance)
		require.NoError(t, h.pacer.Pace(t.Context()))
	}
	const paced, cleared = "codedb backfill paced", "codedb backfill pace cleared"

	pace(paceSigCalm, 0)
	assert.Empty(t, capture.messages(), "calm at zero is silent")

	pace(paceSigHot, 0) // zero -> nonzero
	assert.Equal(t, []string{paced}, capture.messages())

	// the fake sleep advances the clock by each delay, so six more hot steps
	// (2s..60s) cover about two minutes: inside the 5 minute window
	for range 6 {
		pace(paceSigHot, 0)
	}
	assert.Equal(t, []string{paced}, capture.messages(), "inside 5 min stays quiet")

	pace(paceSigHot, 5*time.Minute) // window elapsed
	assert.Equal(t, []string{paced, paced}, capture.messages())

	pace(paceSigHot, 0)
	assert.Equal(t, []string{paced, paced}, capture.messages(), "window restarts after a log")

	// from the 60s cap, calm halves 30s, 15s, 7.5s, 3.75s, 1.875s, 937ms,
	// 468ms: seven nonzero steps, then 234ms snaps to zero on the eighth
	for range 7 {
		pace(paceSigCalm, 0)
	}
	assert.Equal(t, []string{paced, paced}, capture.messages(), "decay while nonzero is rate limited, not a transition")
	pace(paceSigCalm, 0)
	assert.Equal(t, []string{paced, paced, cleared}, capture.messages(), "nonzero -> zero logs once")

	pace(paceSigCalm, 0)
	assert.Equal(t, []string{paced, paced, cleared}, capture.messages(), "calm at zero is silent again")

	pace(paceSigHot, 0) // zero -> nonzero again
	assert.Equal(t, []string{paced, paced, cleared, paced}, capture.messages())
}

// TestPace_LogLineMatchesContract asserts the exact single-line key=value
// format operators grep for.
// Failure prevented: log drift that breaks the grep/alert on "codedb backfill paced".
func TestPace_LogLineMatchesContract(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == slog.LevelKey) {
				return slog.Attr{}
			}
			return a
		},
	}))

	t.Run("all signals known", func(t *testing.T) {
		buf.Reset()
		h := newPacerHarness(logger, paceSignals{
			LoadKnown: true, LoadPerCore: 1.3141, OnBattery: true,
			UserIdleKnown: true, UserIdle: 12*time.Second + 400*time.Millisecond,
		})
		h.pacer.delay = 4 * time.Second // hot doubles it to 8s
		require.NoError(t, h.pacer.Pace(t.Context()))
		assert.Equal(t,
			"msg=\"codedb backfill paced\" delay=8s load_per_core=1.31 on_battery=true user_idle=12s mode=hot\n",
			buf.String())
	})

	t.Run("unknown signals are spelled out", func(t *testing.T) {
		buf.Reset()
		h := newPacerHarness(logger, paceSignals{OnBattery: true})
		require.NoError(t, h.pacer.Pace(t.Context()))
		assert.Equal(t,
			"msg=\"codedb backfill paced\" delay=1s load_per_core=unknown on_battery=true user_idle=unknown mode=hot\n",
			buf.String())
		assert.Equal(t, 1, strings.Count(buf.String(), "\n"), "exactly one line")
	})
}

// --- E. Parsers ---

func TestParsePmsetOnBattery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		out         string
		wantBattery bool
		wantOK      bool
	}{
		{
			"on battery",
			"Now drawing from 'Battery Power'\n -InternalBattery-0 (id=7471203)\t80%; discharging; 1:54 remaining present: true\n",
			true, true,
		},
		{
			"on AC and charging",
			"Now drawing from 'AC Power'\n -InternalBattery-0 (id=7471203)\t80%; charging; 0:42 remaining present: true\n",
			false, true,
		},
		{"desktop on AC with no battery", "Now drawing from 'AC Power'\n", false, true},
		{"battery power but no trailing newline", "Now drawing from 'Battery Power'", true, true},
		{
			"battery word on a later line does not count",
			"Now drawing from 'AC Power'\n -InternalBattery-0\t'Battery Power' mentioned in detail\n",
			false, true,
		},
		{"empty output is unknown", "", false, false},
		{"unrecognized output is unknown", "pmset: command not supported\n", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotBattery, gotOK := parsePmsetOnBattery(tt.out)
			assert.Equal(t, tt.wantBattery, gotBattery)
			assert.Equal(t, tt.wantOK, gotOK)
		})
	}
}

func TestParseHIDIdleTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		out    string
		want   time.Duration
		wantOK bool
	}{
		{
			"typical ioreg line",
			"    | |   \"IOClass\" = \"IOHIDSystem\"\n    | |     \"HIDIdleTime\" = 795217833\n    | |   \"Other\" = 1\n",
			795217833 * time.Nanosecond, true,
		},
		{"minute-scale idle", "      \"HIDIdleTime\" = 115474432666\n", 115474432666 * time.Nanosecond, true},
		{
			"several matches: the most recent input wins",
			"\"HIDIdleTime\" = 9000000000\n\"HIDIdleTime\" = 1000000000\n\"HIDIdleTime\" = 5000000000\n",
			time.Second, true,
		},
		{"zero idle is valid", "\"HIDIdleTime\" = 0\n", 0, true},
		{"no key", "\"IOClass\" = \"IOHIDSystem\"\n", 0, false},
		{"empty", "", 0, false},
		{"non numeric value is skipped", "\"HIDIdleTime\" = banana\n", 0, false},
		{"missing equals is skipped", "\"HIDIdleTime\" 5\n", 0, false},
		{"all-ones sentinel does not read as 292 years", "\"HIDIdleTime\" = 18446744073709551615\n", 0, false},
		{
			"bad line does not hide a good one",
			"\"HIDIdleTime\" = -5\n\"HIDIdleTime\" = 2000000000\n",
			2 * time.Second, true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseHIDIdleTime(tt.out)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseProcLoadAvg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		in     string
		want   float64
		wantOK bool
	}{
		{"typical", "0.52 0.58 0.59 1/467 12345\n", 0.52, true},
		{"integer load", "3 2 1 1/1 1\n", 3, true},
		{"high load", "212.40 150.00 90.10 5/999 1\n", 212.40, true},
		{"empty", "", 0, false},
		{"whitespace only", " \n", 0, false},
		{"garbage", "banana\n", 0, false},
		{"negative", "-1.0 0 0 1/1 1\n", 0, false},
		{"nan", "NaN 0 0 1/1 1\n", 0, false},
		{"inf", "Inf 0 0 1/1 1\n", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseProcLoadAvg(tt.in)
			assert.Equal(t, tt.wantOK, ok)
			assert.InDelta(t, tt.want, got, 1e-9)
		})
	}
}

func paceDarwinLoadAvgBytes(load1, load5, load15 uint32, fscale int64) []byte {
	raw := make([]byte, darwinLoadAvgSize)
	binary.NativeEndian.PutUint32(raw[0:], load1)
	binary.NativeEndian.PutUint32(raw[4:], load5)
	binary.NativeEndian.PutUint32(raw[8:], load15)
	binary.NativeEndian.PutUint64(raw[16:], uint64(fscale))
	return raw
}

// TestParseDarwinLoadAvg pins the struct loadavg layout: three uint32
// fixed-point values, 4 bytes of padding, a long fscale at offset 16.
// Failure prevented: reading fscale from the padding (offset 12) and getting a
// load of garbage or a divide by zero.
func TestParseDarwinLoadAvg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		raw    []byte
		want   float64
		wantOK bool
	}{
		// real values observed on a Mac: ldavg[0]=33862 fscale=2048 -> 16.53
		{"observed sample", paceDarwinLoadAvgBytes(33862, 95744, 118548, 2048), 33862.0 / 2048.0, true},
		{"idle machine", paceDarwinLoadAvgBytes(205, 300, 400, 2048), 205.0 / 2048.0, true},
		{"zero load", paceDarwinLoadAvgBytes(0, 0, 0, 2048), 0, true},
		{"only the 1-minute average is read", paceDarwinLoadAvgBytes(2048, 9999, 9999, 2048), 1, true},
		{"zero fscale is unknown not a divide by zero", paceDarwinLoadAvgBytes(100, 100, 100, 0), 0, false},
		{"negative fscale is unknown", paceDarwinLoadAvgBytes(100, 100, 100, -1), 0, false},
		{"short buffer is unknown", make([]byte, darwinLoadAvgSize-1), 0, false},
		{"nil is unknown", nil, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseDarwinLoadAvg(tt.raw)
			assert.Equal(t, tt.wantOK, ok)
			assert.InDelta(t, tt.want, got, 1e-9)
		})
	}

	t.Run("padding bytes are ignored", func(t *testing.T) {
		t.Parallel()
		raw := paceDarwinLoadAvgBytes(2048, 0, 0, 2048)
		copy(raw[12:16], []byte{0xff, 0xff, 0xff, 0xff})
		got, ok := parseDarwinLoadAvg(raw)
		require.True(t, ok)
		assert.InDelta(t, 1.0, got, 1e-9)
	})
}

type paceSupply struct {
	name, typ, status, scope string // empty field = file not created
}

func writePaceSupply(t *testing.T, supplies ...paceSupply) string {
	t.Helper()
	root := t.TempDir()
	for _, s := range supplies {
		dir := filepath.Join(root, s.name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		for file, content := range map[string]string{"type": s.typ, "status": s.status, "scope": s.scope} {
			if content == "" {
				continue
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte(content+"\n"), 0o644))
		}
	}
	return root
}

// TestReadPowerSupplyOnBattery runs the sysfs reader against fake
// /sys/class/power_supply trees.
// Failure prevented: a wireless mouse (always "Discharging") or a mains adapter
// being read as "the laptop is on battery", throttling backfill forever.
func TestReadPowerSupplyOnBattery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		supplies    []paceSupply
		wantBattery bool
		wantOK      bool
	}{
		{
			"laptop discharging",
			[]paceSupply{{"AC", "Mains", "", ""}, {"BAT0", "Battery", "Discharging", "System"}},
			true, true,
		},
		{
			"laptop charging on AC",
			[]paceSupply{{"AC", "Mains", "", ""}, {"BAT0", "Battery", "Charging", ""}},
			false, true,
		},
		{"battery full on AC", []paceSupply{{"BAT0", "Battery", "Full", ""}}, false, true},
		{"battery not charging on AC", []paceSupply{{"BAT0", "Battery", "Not charging", ""}}, false, true},
		{
			"peripheral battery discharging while laptop is on AC",
			[]paceSupply{
				{"BAT0", "Battery", "Charging", "System"},
				{"hidpp_battery_0", "Battery", "Discharging", "Device"},
			},
			false, true,
		},
		{
			"second battery discharging counts",
			[]paceSupply{{"BAT0", "Battery", "Full", ""}, {"BAT1", "Battery", "Discharging", ""}},
			true, true,
		},
		{"mains adapter whose status says discharging is not a battery", []paceSupply{{"AC", "Mains", "Discharging", ""}}, false, true},
		{"status casing and whitespace", []paceSupply{{"BAT0", "battery", "discharging", ""}}, true, true},
		{"desktop with no supplies is on mains", nil, false, true},
		{"battery missing status file is not discharging", []paceSupply{{"BAT0", "Battery", "", ""}}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotBattery, gotOK := readPowerSupplyOnBattery(writePaceSupply(t, tt.supplies...))
			assert.Equal(t, tt.wantBattery, gotBattery)
			assert.Equal(t, tt.wantOK, gotOK)
		})
	}

	t.Run("missing root is unknown", func(t *testing.T) {
		t.Parallel()
		gotBattery, gotOK := readPowerSupplyOnBattery(filepath.Join(t.TempDir(), "nope"))
		assert.False(t, gotBattery)
		assert.False(t, gotOK)
	})

	t.Run("symlinked paceSupply entries are followed", func(t *testing.T) {
		t.Parallel()
		// sysfs lists supplies as symlinks into /sys/devices
		target := writePaceSupply(t, paceSupply{"BAT0", "Battery", "Discharging", ""})
		root := t.TempDir()
		if err := os.Symlink(filepath.Join(target, "BAT0"), filepath.Join(root, "BAT0")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		gotBattery, gotOK := readPowerSupplyOnBattery(root)
		assert.True(t, gotBattery)
		assert.True(t, gotOK)
	})
}

// --- F. Subprocess runner ---

// TestPaceProbeHelperProcess is the re-exec target for the runProbeCommand
// tests; it is a no-op in a normal test run.
func TestPaceProbeHelperProcess(t *testing.T) {
	mode := os.Getenv("OX_PACE_PROBE_HELPER")
	if mode == "" {
		t.Skip("helper process only")
	}
	switch mode {
	case "echo":
		fmt.Print("Now drawing from 'AC Power'\n")
		os.Exit(0)
	case "fail":
		os.Exit(3)
	case "hang":
		time.Sleep(time.Hour)
	}
	os.Exit(2)
}

func paceHelperCommand(t *testing.T, mode string) (string, []string) {
	t.Helper()
	t.Setenv("OX_PACE_PROBE_HELPER", mode)
	return os.Args[0], []string{"-test.run=^TestPaceProbeHelperProcess$"}
}

// TestRunProbeCommand covers the subprocess guard rails.
// Failure prevented: a hung pmset/ioreg wedging the backfill (and daemon
// shutdown) instead of degrading to an unknown signal.
func TestRunProbeCommand(t *testing.T) {
	// not parallel: the helper-process cases use t.Setenv

	t.Run("returns stdout", func(t *testing.T) {
		name, args := paceHelperCommand(t, "echo")
		out, err := runProbeCommand(t.Context(), 10*time.Second, name, args...)
		require.NoError(t, err)
		assert.Equal(t, "Now drawing from 'AC Power'\n", out)
	})

	t.Run("non-zero exit is an error", func(t *testing.T) {
		name, args := paceHelperCommand(t, "fail")
		_, err := runProbeCommand(t.Context(), 10*time.Second, name, args...)
		assert.Error(t, err)
	})

	t.Run("missing binary is an error", func(t *testing.T) {
		_, err := runProbeCommand(t.Context(), time.Second, filepath.Join(t.TempDir(), "no-such-tool"))
		assert.Error(t, err)
	})

	t.Run("hung command is killed at the timeout", func(t *testing.T) {
		name, args := paceHelperCommand(t, "hang")
		start := time.Now()
		_, err := runProbeCommand(t.Context(), 200*time.Millisecond, name, args...)
		require.Error(t, err)
		assert.Less(t, time.Since(start), 10*time.Second, "killed long before the helper's hour")
	})

	t.Run("parent cancel kills the command", func(t *testing.T) {
		name, args := paceHelperCommand(t, "hang")
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := runProbeCommand(ctx, time.Hour, name, args...)
		require.Error(t, err)
		assert.Less(t, time.Since(start), 10*time.Second)
	})
}

// --- G. Real probes ---

// TestRealHostProbes_Smoke runs the production probes on whatever OS this is.
// Failure prevented: a probe that panics or returns nonsense on a real host
// (wrong struct layout, tool output change) only showing up in the daemon.
func TestRealHostProbes_Smoke(t *testing.T) {
	if testing.Short() {
		t.Skip("short: spawns pmset and ioreg subprocesses")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	sampler := newHostSampler(defaultHostProbes(), runtime.NumCPU(), time.Now)
	s := sampler.sample(ctx)
	t.Logf("os=%s ncpu=%d signals=%+v mode=%s", runtime.GOOS, runtime.NumCPU(), s, s.mode())

	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		require.True(t, s.LoadKnown, "load average must be readable on %s", runtime.GOOS)
	}
	if s.LoadKnown {
		assert.GreaterOrEqual(t, s.LoadPerCore, 0.0)
		assert.Less(t, s.LoadPerCore, 1000.0, "implausible load per core")
	}
	if s.UserIdleKnown {
		assert.GreaterOrEqual(t, s.UserIdle, time.Duration(0))
		assert.Less(t, s.UserIdle, 365*24*time.Hour, "implausible idle time")
	}

	// a second sample inside the TTL must serve the cached subprocess signals
	again := sampler.sample(ctx)
	assert.Equal(t, s.OnBattery, again.OnBattery)
	assert.Equal(t, s.UserIdle, again.UserIdle)
	assert.Equal(t, s.UserIdleKnown, again.UserIdleKnown)

	// the production pacer constructs and, with an already-cancelled ctx,
	// returns without sleeping
	cancelled, stop := context.WithCancel(t.Context())
	stop()
	assert.ErrorIs(t, newIndexPacer(nil).Pace(cancelled), context.Canceled)
}
