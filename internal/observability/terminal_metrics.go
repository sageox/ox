package observability

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/pkg/adapterruntime"
)

// silenceWindowBuckets are the histogram boundaries (in ms) used for
// silence-window observation. Tuned for the 20s default window with
// generous headroom on either side so vendor wording drift (faster
// streaming, slower trailing renders) is still visible without
// re-bucketing. Aligned to the OTLP histogram spec for direct export
// when an OTLP meter provider gets wired in.
var silenceWindowBuckets = []float64{5000, 10000, 15000, 20000, 30000, 60000, 120000, 300000}

// InMemoryMetrics is an adapterruntime.Metrics implementation that
// counts events in process-local maps. Designed for early-rollout
// visibility today, before OTLP metric exporters get wired into
// internal/observability/otel.go alongside the existing tracer.
//
// Two reasons this ships before a real OTLP path:
//
//  1. The existing observability package only initializes a
//     TracerProvider (otel.go:Init), not a MeterProvider. Building a
//     MeterProvider with the OTLP/HTTP metric exporter, JWT auth, and
//     correct shutdown semantics is a non-trivial change that deserves
//     its own PR for review.
//  2. The Metrics interface lets us swap the implementation later
//     without touching adapter or detector code — the detector wiring
//     in pkg/adapterruntime never needs to learn about OTLP.
//
// Snapshot() returns a serialized view of every counter and histogram
// observed so far, suitable for dumping in `ox doctor`, a JSON debug
// endpoint, or a periodic flush to logs while the OTLP integration
// is in flight.
//
// Thread-safe: counters/histograms guarded by a single mutex. The
// detector currently fires one event per matched entry plus one per
// silence-window tick, so contention is negligible.
type InMemoryMetrics struct {
	mu               sync.Mutex
	patternHit       map[string]uint64
	patternConfirmed map[string]uint64
	patternRevoked   map[string]uint64
	resetsParseFail  map[string]uint64
	silenceMs        map[string]*histogramSeries
}

type histogramSeries struct {
	count   uint64
	sumMs   float64
	buckets []uint64 // parallel to silenceWindowBuckets; final entry counts >last
}

// NewInMemoryMetrics constructs an empty in-memory metrics sink.
func NewInMemoryMetrics() *InMemoryMetrics {
	return &InMemoryMetrics{
		patternHit:       make(map[string]uint64),
		patternConfirmed: make(map[string]uint64),
		patternRevoked:   make(map[string]uint64),
		resetsParseFail:  make(map[string]uint64),
		silenceMs:        make(map[string]*histogramSeries),
	}
}

// PatternHit increments ox.adapter.terminal.pattern_hit{adapter, pattern_id, source, reason}.
func (m *InMemoryMetrics) PatternHit(adapter, patternID string, source adapterruntime.PatternSource, reason string) {
	key := joinLabels(adapter, patternID, string(source), reason)
	m.mu.Lock()
	m.patternHit[key]++
	m.mu.Unlock()
}

// PatternConfirmed increments ox.adapter.terminal.pattern_confirmed{adapter, pattern_id}.
func (m *InMemoryMetrics) PatternConfirmed(adapter, patternID string) {
	key := joinLabels(adapter, patternID)
	m.mu.Lock()
	m.patternConfirmed[key]++
	m.mu.Unlock()
}

// PatternRevoked increments ox.adapter.terminal.pattern_revoked{adapter, pattern_id}.
func (m *InMemoryMetrics) PatternRevoked(adapter, patternID string) {
	key := joinLabels(adapter, patternID)
	m.mu.Lock()
	m.patternRevoked[key]++
	m.mu.Unlock()
}

// ResetsAtParseFailure increments ox.adapter.terminal.resets_at_parse_failure{adapter, pattern_id}.
func (m *InMemoryMetrics) ResetsAtParseFailure(adapter, patternID string) {
	key := joinLabels(adapter, patternID)
	m.mu.Lock()
	m.resetsParseFail[key]++
	m.mu.Unlock()
}

// SilenceWindowObservedMs records ox.adapter.terminal.silence_window_observed_ms
// {adapter, pattern_id} as a histogram with silenceWindowBuckets boundaries.
func (m *InMemoryMetrics) SilenceWindowObservedMs(adapter, patternID string, observed time.Duration) {
	key := joinLabels(adapter, patternID)
	ms := float64(observed.Milliseconds())
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.silenceMs[key]
	if !ok {
		h = &histogramSeries{buckets: make([]uint64, len(silenceWindowBuckets)+1)}
		m.silenceMs[key] = h
	}
	h.count++
	h.sumMs += ms
	placed := false
	for i, boundary := range silenceWindowBuckets {
		if ms <= boundary {
			h.buckets[i]++
			placed = true
			break
		}
	}
	if !placed {
		h.buckets[len(silenceWindowBuckets)]++ // overflow bucket
	}
}

// MetricsSnapshot is a serialized view of every counter and histogram
// observed by an InMemoryMetrics. Keys are the joined label tuple
// (adapter|pattern_id|source|reason) — split on "|" to recover labels.
type MetricsSnapshot struct {
	PatternHit       map[string]uint64           `json:"pattern_hit,omitempty"`
	PatternConfirmed map[string]uint64           `json:"pattern_confirmed,omitempty"`
	PatternRevoked   map[string]uint64           `json:"pattern_revoked,omitempty"`
	ResetsParseFail  map[string]uint64           `json:"resets_at_parse_failure,omitempty"`
	SilenceWindowMs  map[string]HistogramSummary `json:"silence_window_observed_ms,omitempty"`
}

// HistogramSummary is the snapshot view of one histogram series.
type HistogramSummary struct {
	Count   uint64    `json:"count"`
	SumMs   float64   `json:"sum_ms"`
	Buckets []uint64  `json:"buckets"`    // parallel to Boundaries + overflow
	Bounds  []float64 `json:"boundaries"` // canonical bucket boundaries (ms)
}

// Snapshot returns a deep copy of the metrics state. Safe to call from
// any goroutine. The returned maps are independent and can be mutated
// without affecting the in-memory store.
func (m *InMemoryMetrics) Snapshot() MetricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	cp := func(in map[string]uint64) map[string]uint64 {
		if len(in) == 0 {
			return nil
		}
		out := make(map[string]uint64, len(in))
		for k, v := range in {
			out[k] = v
		}
		return out
	}

	var silence map[string]HistogramSummary
	if len(m.silenceMs) > 0 {
		silence = make(map[string]HistogramSummary, len(m.silenceMs))
		for k, h := range m.silenceMs {
			b := make([]uint64, len(h.buckets))
			copy(b, h.buckets)
			bounds := make([]float64, len(silenceWindowBuckets))
			copy(bounds, silenceWindowBuckets)
			silence[k] = HistogramSummary{
				Count:   h.count,
				SumMs:   h.sumMs,
				Buckets: b,
				Bounds:  bounds,
			}
		}
	}

	return MetricsSnapshot{
		PatternHit:       cp(m.patternHit),
		PatternConfirmed: cp(m.patternConfirmed),
		PatternRevoked:   cp(m.patternRevoked),
		ResetsParseFail:  cp(m.resetsParseFail),
		SilenceWindowMs:  silence,
	}
}

// SortedKeys returns the sorted union of every metric key, useful for
// deterministic display (table rendering in `ox doctor` or test
// assertions).
func (s MetricsSnapshot) SortedKeys() []string {
	seen := map[string]struct{}{}
	add := func(in map[string]uint64) {
		for k := range in {
			seen[k] = struct{}{}
		}
	}
	add(s.PatternHit)
	add(s.PatternConfirmed)
	add(s.PatternRevoked)
	add(s.ResetsParseFail)
	for k := range s.SilenceWindowMs {
		seen[k] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// joinLabels canonicalizes a label tuple into a deterministic key.
// "|" cannot appear in any label produced by adapterruntime (labels
// are pattern IDs and adapter names from registered config), so it is
// safe as a delimiter. Empty labels are preserved so the position is
// unambiguous on split.
func joinLabels(labels ...string) string {
	return strings.Join(labels, "|")
}

// compile-time interface check — keeps the wire contract honest if the
// adapterruntime.Metrics signature ever changes.
var _ adapterruntime.Metrics = (*InMemoryMetrics)(nil)
