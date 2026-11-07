package daemon

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Pacing policy knobs. The daemon is already niced and capped at GOMAXPROCS 2,
// but a multi-hour history backfill still pins those cores flat out, which on a
// laptop is a heat problem (thermal emergency sleeps), not a scheduling one.
// Pacing adds idle time between checkpoints instead of entering macOS's
// background band, which starved the daemon in #1235.
const (
	paceHotLoadPerCore  = 1.0 // load1 / ncpu at or above this: the machine is saturated
	paceBusyLoadPerCore = 0.7 // load1 / ncpu at or above this: someone is using the cores

	paceHotMinDelay  = time.Second
	paceHotMaxDelay  = time.Minute
	paceBusyMinDelay = 500 * time.Millisecond
	// halving below this snaps to zero; sleeping 100ms every 50 commits is
	// bookkeeping noise, not relief.
	paceSnapToZeroBelow = 250 * time.Millisecond

	// a user who touched the keyboard or mouse inside this window is working.
	paceUserActiveWindow = 2 * time.Minute

	paceBatteryTTL   = 60 * time.Second
	paceIdleTTL      = 30 * time.Second
	paceProbeTimeout = 2 * time.Second
	paceLogInterval  = 5 * time.Minute
)

// paceSignals is one sample of host state. An unknown signal never makes the
// pacer slower: it reads as "nothing to yield to".
type paceSignals struct {
	LoadPerCore   float64 // 1-minute load average divided by logical CPU count
	LoadKnown     bool
	OnBattery     bool
	UserIdle      time.Duration // time since last keyboard/mouse input
	UserIdleKnown bool
}

type paceMode string

const (
	paceCalm paceMode = "calm"
	paceBusy paceMode = "busy"
	paceHot  paceMode = "hot"
)

func (s paceSignals) mode() paceMode {
	switch {
	case s.OnBattery || (s.LoadKnown && s.LoadPerCore >= paceHotLoadPerCore):
		return paceHot
	case (s.LoadKnown && s.LoadPerCore >= paceBusyLoadPerCore) ||
		(s.UserIdleKnown && s.UserIdle < paceUserActiveWindow):
		return paceBusy
	default:
		return paceCalm
	}
}

// nextPaceDelay returns the sleep to apply at this checkpoint given the delay
// applied at the previous one.
//
//   - hot:  back off exponentially, 1s doubling up to 60s.
//   - busy: hold a gentle floor; ease toward 500ms but never to zero.
//   - calm: halve, and snap to zero below 250ms so the backfill returns to
//     full speed once the machine is idle and plugged in.
//
// Pure so the whole policy is table-testable.
func nextPaceDelay(prev time.Duration, s paceSignals) time.Duration {
	prev = max(prev, 0)
	switch s.mode() {
	case paceHot:
		// clamp before doubling so a corrupt prev cannot overflow
		doubled := min(prev, paceHotMaxDelay) * 2
		return min(max(doubled, paceHotMinDelay), paceHotMaxDelay)
	case paceBusy:
		return max(prev/2, paceBusyMinDelay)
	default:
		next := prev / 2
		if next < paceSnapToZeroBelow {
			return 0
		}
		return next
	}
}

// indexPacer throttles background history backfill between checkpoints so a
// deep index yields to load, battery power and an active user.
//
// One pacer is shared across runs; delay carries over between calls because the
// policy is a feedback loop on the previous delay. Safe for concurrent use:
// state steps once per Pace call and the sleep itself happens outside the lock.
type indexPacer struct {
	logger *slog.Logger

	// seams: tests replace these with fakes.
	sample func(ctx context.Context) paceSignals
	sleep  func(ctx context.Context, d time.Duration) error
	now    func() time.Time

	mu      sync.Mutex
	delay   time.Duration
	lastLog time.Time
}

// newIndexPacer returns a pacer that samples host signals on demand.
func newIndexPacer(logger *slog.Logger) *indexPacer {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	// runtime.NumCPU, not GOMAXPROCS: the daemon caps GOMAXPROCS at 2, which
	// would make a calm 8-core machine at load 2 look saturated.
	sampler := newHostSampler(defaultHostProbes(), runtime.NumCPU(), time.Now)
	return &indexPacer{
		logger: logger,
		sample: sampler.sample,
		sleep:  sleepContext,
		now:    time.Now,
	}
}

// Pace sleeps for the current backoff delay, or returns ctx.Err() if ctx ends
// first. Its signature matches index.IndexOptions.Pace.
func (p *indexPacer) Pace(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	delay := p.step(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay == 0 {
		return nil
	}
	return p.sleep(ctx, delay)
}

// step samples the host, advances the delay and logs. The mutex also serializes
// the sampler, so concurrent callers never spawn duplicate probe subprocesses.
func (p *indexPacer) step(ctx context.Context) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()

	signals := p.sample(ctx)
	prev := p.delay
	next := nextPaceDelay(prev, signals)
	p.delay = next
	p.logStep(ctx, prev, next, signals)
	return next
}

// logStep logs every zero<->nonzero transition, and otherwise at most once per
// paceLogInterval while paced, so a day-long backfill leaves a few lines, not
// one per checkpoint.
func (p *indexPacer) logStep(ctx context.Context, prev, next time.Duration, s paceSignals) {
	now := p.now()
	var msg string
	switch {
	case prev == 0 && next == 0:
		return
	case next == 0:
		msg = "codedb backfill pace cleared"
	case prev == 0 || now.Sub(p.lastLog) >= paceLogInterval:
		msg = "codedb backfill paced"
	default:
		return
	}
	p.lastLog = now
	p.logger.LogAttrs(ctx, slog.LevelInfo, msg, paceLogAttrs(next, s)...)
}

func paceLogAttrs(delay time.Duration, s paceSignals) []slog.Attr {
	// strings, not slog.Duration: the JSON handler would emit raw nanoseconds.
	attrs := []slog.Attr{slog.String("delay", delay.String())}
	if s.LoadKnown {
		attrs = append(attrs, slog.Float64("load_per_core", math.Round(s.LoadPerCore*100)/100))
	} else {
		attrs = append(attrs, slog.String("load_per_core", "unknown"))
	}
	attrs = append(attrs, slog.Bool("on_battery", s.OnBattery))
	if s.UserIdleKnown {
		attrs = append(attrs, slog.String("user_idle", s.UserIdle.Round(time.Second).String()))
	} else {
		attrs = append(attrs, slog.String("user_idle", "unknown"))
	}
	return append(attrs, slog.String("mode", string(s.mode())))
}

// sleepContext waits for d or until ctx ends, whichever is first.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// hostProbes are the OS-specific signal readers; defaultHostProbes (one per
// platform file) wires the real ones. A nil probe, or ok=false, means the
// signal is unknown on this host.
type hostProbes struct {
	loadAverage func() (load1 float64, ok bool)
	onBattery   func(ctx context.Context) (onBattery, ok bool)
	userIdle    func(ctx context.Context) (idle time.Duration, ok bool)
}

// hostSampler turns raw probes into paceSignals. Not safe for concurrent use;
// indexPacer.step serializes calls.
type hostSampler struct {
	probes  hostProbes
	ncpu    int
	battery ttlSignal[bool]
	idle    ttlSignal[time.Duration]
}

func newHostSampler(probes hostProbes, ncpu int, now func() time.Time) *hostSampler {
	return &hostSampler{
		probes:  probes,
		ncpu:    max(ncpu, 1),
		battery: ttlSignal[bool]{ttl: paceBatteryTTL, probe: probes.onBattery, now: now},
		idle:    ttlSignal[time.Duration]{ttl: paceIdleTTL, probe: probes.userIdle, now: now},
	}
}

func (h *hostSampler) sample(ctx context.Context) paceSignals {
	var s paceSignals
	// load comes from a syscall or a procfs read: cheap, so never cached.
	if h.probes.loadAverage != nil {
		if load, ok := h.probes.loadAverage(); ok {
			s.LoadPerCore = load / float64(h.ncpu)
			s.LoadKnown = true
		}
	}
	if onBattery, ok := h.battery.get(ctx); ok {
		s.OnBattery = onBattery
	}
	if idle, ok := h.idle.get(ctx); ok {
		s.UserIdle = idle
		s.UserIdleKnown = true
	}
	return s
}

// ttlSignal caches a subprocess-backed probe. Failures are cached for the same
// TTL as successes: a broken or hanging pmset/ioreg would otherwise cost its
// full 2s timeout at every checkpoint.
type ttlSignal[T any] struct {
	ttl   time.Duration
	probe func(ctx context.Context) (T, bool)
	now   func() time.Time

	at  time.Time
	val T
	ok  bool
}

func (c *ttlSignal[T]) get(ctx context.Context) (T, bool) {
	var zero T
	if c.probe == nil {
		return zero, false
	}
	if !c.at.IsZero() && c.now().Sub(c.at) < c.ttl {
		return c.val, c.ok
	}
	val, ok := c.probe(ctx)
	if ctx.Err() != nil {
		// a cancelled subprocess looks like a probe failure; caching it would
		// hide a working signal for a full TTL.
		return zero, false
	}
	if !ok {
		val = zero
	}
	c.at, c.val, c.ok = c.now(), val, ok
	return val, ok
}

// runProbeCommand runs a short-lived system command with a hard timeout. The
// subprocess is killed when ctx ends or the timeout expires.
func runProbeCommand(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	// bounds Wait if something still holds the output pipe after the kill.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("run %s: %w", name, err)
	}
	return string(out), nil
}

// The parsers below are pure text/byte functions so they are tested on every
// platform even though each is only wired up on one.

// darwinLoadAvgSize is sizeof(struct loadavg) on LP64 macOS: three uint32
// fixed-point averages, four bytes of padding, then a long fscale.
const darwinLoadAvgSize = 24

// parseDarwinLoadAvg decodes the raw `vm.loadavg` sysctl into the 1-minute
// load average. Each average is fixed-point: value / fscale.
func parseDarwinLoadAvg(raw []byte) (float64, bool) {
	if len(raw) < darwinLoadAvgSize {
		return 0, false
	}
	fscale := int64(binary.NativeEndian.Uint64(raw[16:24]))
	if fscale <= 0 {
		return 0, false
	}
	return float64(binary.NativeEndian.Uint32(raw[0:4])) / float64(fscale), true
}

// parseProcLoadAvg reads the 1-minute load from /proc/loadavg
// ("0.52 0.58 0.59 1/467 12345").
func parseProcLoadAvg(content string) (float64, bool) {
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return 0, false
	}
	load, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(load) || math.IsInf(load, 0) || load < 0 {
		return 0, false
	}
	return load, true
}

// parsePmsetOnBattery reads the power source from the first line of
// `pmset -g batt` ("Now drawing from 'Battery Power'" / "'AC Power'").
func parsePmsetOnBattery(out string) (onBattery, ok bool) {
	first, _, _ := strings.Cut(out, "\n")
	switch {
	case strings.Contains(first, "'Battery Power'"):
		return true, true
	case strings.Contains(first, "Now drawing from"):
		return false, true
	default:
		return false, false
	}
}

// parseHIDIdleTime extracts HIDIdleTime (nanoseconds since the last input
// event) from `ioreg -c IOHIDSystem -d 4`. With several matches the smallest
// wins: any recent input means the user is present.
func parseHIDIdleTime(out string) (time.Duration, bool) {
	var best time.Duration
	found := false
	for line := range strings.SplitSeq(out, "\n") {
		_, rest, hasKey := strings.Cut(line, `"HIDIdleTime"`)
		if !hasKey {
			continue
		}
		_, value, hasValue := strings.Cut(rest, "=")
		if !hasValue {
			continue
		}
		// 63 bits so the value fits a Duration; the kernel's all-ones
		// "no event yet" sentinel is rejected rather than read as 292 years.
		ns, err := strconv.ParseUint(strings.TrimSpace(value), 10, 63)
		if err != nil {
			continue
		}
		if d := time.Duration(ns); !found || d < best {
			best, found = d, true
		}
	}
	return best, found
}

// readPowerSupplyOnBattery inspects a /sys/class/power_supply-style directory.
// A host with no battery is on mains (false, true); an unreadable root is
// unknown.
func readPowerSupplyOnBattery(root string) (onBattery, ok bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, false
	}
	for _, entry := range entries {
		dir := filepath.Join(root, entry.Name())
		if !strings.EqualFold(readTrimmedFile(filepath.Join(dir, "type")), "Battery") {
			continue
		}
		// wireless mice and headsets also register as Battery with scope
		// Device; they drain constantly and say nothing about the host.
		if strings.EqualFold(readTrimmedFile(filepath.Join(dir, "scope")), "Device") {
			continue
		}
		if strings.EqualFold(readTrimmedFile(filepath.Join(dir, "status")), "Discharging") {
			return true, true
		}
	}
	return false, true
}

func readTrimmedFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
