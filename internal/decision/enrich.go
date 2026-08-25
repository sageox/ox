package decision

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync"

	"github.com/sageox/ox/internal/config"
)

// registry holds detectors and retrievers; features self-register via init()
// so the orchestrator never changes when a signal is added (the internal/plan
// pattern). Enrich works correctly with zero registrations.
var (
	registryMu sync.RWMutex
	detectors  []Detector
	retrievers []Retriever
)

// RegisterDetector adds a deterministic detector. Nil is ignored.
func RegisterDetector(d Detector) {
	if d == nil {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	detectors = append(detectors, d)
}

// RegisterRetriever adds a context retriever. Nil is ignored.
func RegisterRetriever(r Retriever) {
	if r == nil {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	retrievers = append(retrievers, r)
}

func snapshotRegistry() ([]Detector, []Retriever) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	ds := make([]Detector, len(detectors))
	copy(ds, detectors)
	rs := make([]Retriever, len(retrievers))
	copy(rs, retrievers)
	return ds, rs
}

// EnrichOption tunes a single Enrich call without changing the fail-open
// defaults. Options are applied to the resolved Env after buildEnv.
type EnrichOption func(*Env)

// WithExplain toggles Result.Dropped population (cap-omitted and sub-floor
// candidates). Off by default so the agent path stays lean.
func WithExplain(on bool) EnrichOption {
	return func(e *Env) { e.Explain = on }
}

// Enrich runs every registered detector and retriever, FAIL-OPEN: a panic or
// error in any one is logged and skipped, never aborting the others. A source
// that errors marks the Result DEGRADED so guidance never reports a swallowed
// failure as a verified absence (#823). Zero LLM or network calls — everything
// reads local data resolved once into Env.
func Enrich(ctx context.Context, in Input, gitRoot string, opts ...EnrichOption) Result {
	env := buildEnv(gitRoot)
	for _, o := range opts {
		o(env)
	}
	ds, rs := snapshotRegistry()

	degraded := env.CorpusUnparsed > 0 || env.SourceUnavailable

	var annotations []Annotation
	// A decision dir contains markdown that did not make it into the catalog —
	// surface the gap as a visible diagnostic, not a silent partial result.
	if env.CorpusUnparsed > 0 {
		annotations = append(annotations, Annotation{
			Kind: BadgeDeterministic, Type: BadgeDiagnostic, Rule: RuleUnreadableCorpus,
			Why: fmt.Sprintf("%d markdown file(s) in this repo's decision dir could not be cataloged (unreadable or not shaped as Decision Records) — a DR needs a number in its filename/H1, or a title plus a Status/Date line. Fix the files; retrieval cannot see them, so treat 'no prior decision' as UNVERIFIED here", env.CorpusUnparsed),
		})
	}
	for _, d := range ds {
		anns, errored := runDetector(ctx, d, env, in)
		annotations = append(annotations, anns...)
		degraded = degraded || errored
	}
	var items []ContextItem
	for _, r := range rs {
		its, errored := runRetriever(ctx, r, env, in)
		items = append(items, its...)
		degraded = degraded || errored
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].Ref < items[j].Ref
	})
	if len(items) > bundleCap {
		items = items[:bundleCap]
	}

	sum := summarize(annotations, items)
	sum.Degraded = degraded
	info := decisionInfo(in, env)
	var cfg *config.DecisionConfig
	if pc, _ := config.LoadProjectConfig(gitRoot); pc != nil {
		cfg = pc.Decision
	}
	conv := buildConventions(gitRoot, env.Corpus, PrimaryDir(gitRoot, cfg))
	if info.ID == "" && conv.NextNumber > 0 {
		prefix := dominantPrefix(env.Corpus)
		if prefix == "" {
			prefix = "ADR"
		}
		info.SuggestedID = normalizeRefToken(prefix, strconv.Itoa(conv.NextNumber))
	}

	res := Result{
		SchemaVersion: SchemaVersion,
		Decision:      info,
		Conventions:   conv,
		Annotations:   annotations,
		Context:       items,
		Signals:       sum,
		Guidance:      buildGuidance(in, sum, conv, annotations, items),
	}
	if env.Explain {
		res.Dropped = droppedCandidates(env, in)
	}
	return res
}

// buildEnv resolves the shared read-only environment once per Enrich call.
// Every field is best-effort; consumers fail open on empties.
func buildEnv(gitRoot string) *Env {
	env := &Env{GitRoot: gitRoot}
	if gitRoot == "" {
		return env
	}
	var cfg *config.DecisionConfig
	pc, cfgErr := config.LoadProjectConfig(gitRoot)
	if cfgErr != nil {
		slog.Warn("decision: project config unavailable", "error", cfgErr)
		env.SourceUnavailable = true
	} else if pc != nil {
		cfg = pc.Decision
		if err := config.ValidateDecisionConfig(cfg); err != nil {
			slog.Warn("decision: invalid decision config", "error", err)
			env.SourceUnavailable = true
		}
	}
	loaded := loadCorpus(gitRoot, cfg)
	env.Corpus = loaded.records
	env.CorpusUnparsed = loaded.unparsed
	if loaded.err != nil {
		slog.Warn("decision: corpus source unavailable", "error", loaded.err)
		env.SourceUnavailable = true
	}
	if pctx, err := config.LoadProjectContext(gitRoot); err == nil && pctx != nil {
		env.LedgerPath = pctx.DefaultLedgerPath()
	} else if err != nil {
		slog.Warn("decision: project context unavailable", "error", err)
		env.SourceUnavailable = true
	}
	return env
}

// runDetector isolates one detector: a panic or error is logged and swallowed
// so siblings still run, but it also reports errored=true so Enrich can mark
// the whole Result degraded — a swallowed failure must never masquerade as a
// verified "nothing found" (#823).
func runDetector(ctx context.Context, d Detector, env *Env, in Input) (out []Annotation, errored bool) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("decision detector panicked", "detector", d.Name(), "recover", r)
			out, errored = nil, true
		}
	}()
	anns, err := d.Detect(ctx, env, in)
	if err != nil {
		slog.Warn("decision detector failed", "detector", d.Name(), "error", err)
		return nil, true
	}
	return anns, false
}

func runRetriever(ctx context.Context, r Retriever, env *Env, in Input) (out []ContextItem, errored bool) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Warn("decision retriever panicked", "retriever", r.Name(), "recover", rec)
			out, errored = nil, true
		}
	}()
	items, err := r.Retrieve(ctx, env, in)
	if err != nil {
		slog.Warn("decision retriever failed", "retriever", r.Name(), "error", err)
		return items, true
	}
	return items, false
}

func summarize(annotations []Annotation, items []ContextItem) SignalSummary {
	var s SignalSummary
	for _, a := range annotations {
		switch a.Type {
		case BadgeRelatedDecision:
			s.Related++
		case BadgeUnresolvedRef:
			s.UnresolvedRefs++
		case BadgeDiagnostic:
			s.Diagnostics++
		}
	}
	for _, it := range items {
		switch it.Kind {
		case "session", "plan":
			s.PriorSessions++
		case "murmur":
			s.Murmurs++
		}
	}
	s.Material = s.Related > 0 || s.PriorSessions > 0 || s.UnresolvedRefs > 0
	return s
}

func decisionInfo(in Input, env *Env) DecisionInfo {
	info := DecisionInfo{
		ID:     in.Record.ID,
		Title:  in.Record.Title,
		Status: normalizeStatus(in.Record.Status),
	}
	if in.Topic != "" && info.Title == "" {
		info.Title = in.Topic
	}
	return info
}
