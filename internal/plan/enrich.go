package plan

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
)

const (
	// NonTrivialMinFiles: a multi-file plan (>= 2 distinct files) is non-trivial.
	// Exported as the single source of truth: the plan-exit hook mirrors these
	// for wording and a drift test asserts the copies stay equal.
	NonTrivialMinFiles = 2
	// NonTrivialMinSteps: a ~5+ step plan is non-trivial, matching the prime
	// "~5+ steps" criterion. H2 sections are the step proxy.
	NonTrivialMinSteps = 5
)

// registry holds the detectors and retrievers contributed by Round 2 packages.
// Registration happens via init() in collision.go / expert.go / priorart.go
// (detectors) and the context-bundle assembler (retrievers). The registry is
// intentionally global so feature files self-register without touching the
// orchestrator. Enrich works correctly with zero registered detectors/retrievers
// (it returns an empty, non-material Result).
var (
	registryMu sync.RWMutex
	detectors  []Detector
	retrievers []Retriever
)

// RegisterDetector adds a deterministic detector to the global registry.
// Call from an init() in the detector's file. Nil detectors are ignored.
func RegisterDetector(d Detector) {
	if d == nil {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	detectors = append(detectors, d)
}

// RegisterRetriever adds a context-bundle retriever to the global registry.
// Call from an init() in the retriever's file. Nil retrievers are ignored.
func RegisterRetriever(r Retriever) {
	if r == nil {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	retrievers = append(retrievers, r)
}

// snapshotRegistry returns copies of the registered detectors and retrievers
// under the read lock, so Enrich can run without holding the lock across the
// (potentially slow) detector calls.
func snapshotRegistry() ([]Detector, []Retriever) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	ds := make([]Detector, len(detectors))
	copy(ds, detectors)
	rs := make([]Retriever, len(retrievers))
	copy(rs, retrievers)
	return ds, rs
}

// Enrich runs every registered detector and retriever against the plan, FAIL-OPEN:
// a panic or error in any one detector/retriever is logged and skipped, never
// aborting the others. It aggregates the annotations and context items, computes
// a deterministic SignalSummary, and returns a sorted, deduped Result.
//
// ox makes NO network or LLM call here — detectors and retrievers read only
// local data. Round 2 owns their implementations.
func Enrich(ctx context.Context, in Input, gitRoot string) Result {
	ds, rs := snapshotRegistry()

	var annotations []Annotation
	for _, d := range ds {
		annotations = append(annotations, runDetector(ctx, d, in, gitRoot)...)
	}

	var items []ContextItem
	for _, r := range rs {
		items = append(items, runRetriever(ctx, r, in, gitRoot)...)
	}

	annotations = sortDedupeAnnotations(annotations)

	return Result{
		Annotations: annotations,
		Context:     items,
		Signals:     summarize(annotations, in),
	}
}

// runDetector invokes a single detector with panic recovery so a misbehaving
// detector can never abort enrichment.
func runDetector(ctx context.Context, d Detector, in Input, gitRoot string) (out []Annotation) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("plan detector panicked", "detector", d.Name(), "recover", r)
			out = nil
		}
	}()
	anns, err := d.Detect(ctx, in, gitRoot)
	if err != nil {
		slog.Warn("plan detector failed", "detector", d.Name(), "error", err)
		return nil
	}
	return anns
}

// runRetriever invokes a single retriever with panic recovery (fail-open).
func runRetriever(ctx context.Context, r Retriever, in Input, gitRoot string) (out []ContextItem) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Warn("plan retriever panicked", "retriever", r.Name(), "recover", rec)
			out = nil
		}
	}()
	items, err := r.Retrieve(ctx, in, gitRoot)
	if err != nil {
		slog.Warn("plan retriever failed", "retriever", r.Name(), "error", err)
		return nil
	}
	return items
}

// summarize rolls up the two independent signal axes. Material (team-context):
// any collision OR expert-route fired, or there is at least one prior-art hit
// (the "strong prior-art" gate is refined in Round 2 once prior-art scoring
// exists). NonTrivial (structural): the plan is multi-file or many-step, so an
// enriched HTML render is worth recommending even when team context is silent.
func summarize(annotations []Annotation, in Input) SignalSummary {
	var s SignalSummary
	for _, a := range annotations {
		switch a.Type {
		case BadgeCollision:
			s.Collisions++
		case BadgePriorArt:
			s.PriorArt++
		case BadgeExpertRoute:
			s.ExpertRoutes++
		}
	}
	s.Material = s.Collisions > 0 || s.ExpertRoutes > 0 || s.PriorArt >= 1

	s.Files = countDistinctFiles(in.Sections)
	s.Steps = countSteps(in.Sections)
	s.NonTrivial = s.Files >= NonTrivialMinFiles || s.Steps >= NonTrivialMinSteps
	return s
}

// countDistinctFiles unions Section.Files across all sections, deduped. Each
// section's Files is already path-validated + per-section deduped by Parse; this
// only collapses the cross-section union so a single file cited in three
// sections counts once, not three times.
func countDistinctFiles(sections []Section) int {
	seen := make(map[string]struct{})
	for _, sec := range sections {
		for _, f := range sec.Files {
			seen[f] = struct{}{}
		}
	}
	return len(seen)
}

// countSteps counts H2-delimited sections (the plan's structural steps),
// EXCLUDING the empty-heading preamble Parse emits for content before the first
// H2 (it is framing, not a step). Counting it would inflate by one and push a
// 4-section plan over the non-trivial step threshold.
func countSteps(sections []Section) int {
	n := 0
	for _, sec := range sections {
		if strings.TrimSpace(sec.Heading) == "" {
			continue // preamble, not a step
		}
		n++
	}
	return n
}

// sortDedupeAnnotations produces a deterministic, duplicate-free ordering so the
// JSON output is stable across runs (hooks diff it; tests assert on it).
func sortDedupeAnnotations(annotations []Annotation) []Annotation {
	if len(annotations) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(annotations))
	deduped := make([]Annotation, 0, len(annotations))
	for _, a := range annotations {
		key := a.Section + "\x00" + string(a.Type) + "\x00" + a.Why + "\x00" + a.SourceURL + "\x00" + a.Expert
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, a)
	}

	sort.SliceStable(deduped, func(i, j int) bool {
		a, b := deduped[i], deduped[j]
		if a.Section != b.Section {
			return a.Section < b.Section
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Expert != b.Expert {
			return a.Expert < b.Expert
		}
		return a.Why < b.Why
	})

	return deduped
}
