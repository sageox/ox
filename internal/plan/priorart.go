package plan

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/ledgersearch"
)

// init self-registers the prior-art detector with the global registry so the
// orchestrator picks it up without touching Enrich (see enrich.go).
func init() {
	RegisterDetector(&priorArtDetector{})
}

// priorArtScoreThreshold gates weak ledger matches out of the emitted set.
// ledgersearch scores are 0.5 (all terms matched, no tf bonus) .. 1.0. A bare
// 0.5 match means the query terms appeared once each — too weak to claim a
// teammate "already did this". Require some term-frequency lift so we only
// surface sessions that are substantively about the plan's subject. This keeps
// prior-art precise: enrich's materiality gate counts PriorArt>=1 as material,
// so a noisy hit would falsely flag every plan.
const priorArtScoreThreshold = 0.6

// maxPriorArtHits caps how many prior-art annotations we emit per plan. Past
// the top few, additional sessions add noise without changing the agent's
// decision ("someone has worked near here — go read it").
const maxPriorArtHits = 3

// minKeyword is the shortest token we treat as a meaningful search keyword.
// Anything shorter is almost always a stopword or noise ("to", "a", "of").
const minKeyword = 3

// priorArtDetector finds prior sessions/murmurs where a teammate already did or
// planned similar work, using LOCAL-FIRST keyword retrieval over the cached
// ledger (zero LLM tokens, zero network). The cloud `ox query` augment is a
// fallback, NOT the deterministic path — see the TODO at the bottom.
type priorArtDetector struct{}

// Name identifies the detector in logs and registry snapshots.
func (d *priorArtDetector) Name() string { return "prior-art" }

// Detect runs the local-first retrieval and emits one Annotation per strong
// prior-art hit. FAIL-OPEN: missing config, no ledger, empty plan, or any read
// error yields (nil, nil) — never an aborting error.
func (d *priorArtDetector) Detect(ctx context.Context, in Input, gitRoot string) ([]Annotation, error) {
	query := deriveQuery(in)
	if query == "" {
		return nil, nil
	}

	// resolveLedgerPath is a package-level helper shared with the collision
	// detector (collision.go) — it returns the canonical ledger checkout root
	// via ProjectContext and "" when uninitialized. Reuse, never duplicate.
	ledgerPath := resolveLedgerPath(gitRoot)
	if ledgerPath == "" {
		return nil, nil
	}

	results, err := ledgersearch.Search(ledgersearch.Options{
		LedgerPath: ledgerPath,
		Query:      query,
		// ask for a few extra so thresholding+ranking still has headroom to
		// fill maxPriorArtHits after weak matches are dropped.
		Limit: maxPriorArtHits * 3,
	})
	if err != nil {
		// ledgersearch is already fail-open, but stay defensive.
		return nil, nil
	}

	hits := rankHits(results)
	if len(hits) == 0 {
		return nil, nil
	}

	annotations := make([]Annotation, 0, len(hits))
	for _, h := range hits {
		annotations = append(annotations, annotationFromHit(h))
	}

	// TODO(cloud-augment): when local retrieval is sparse (e.g. fewer than
	// maxPriorArtHits strong hits) optionally fan out to the cloud `ox query`
	// API for cross-repo / un-cached prior art. This MUST stay out of the
	// deterministic local path: it makes a network call, so it belongs behind
	// an explicit opt-in flag and a retriever, never here in Detect.

	return annotations, nil
}

// deriveQuery builds a keyword search query from the plan. Salient terms come
// from section HEADINGS plus the preamble/title (the "what we're building"
// signal), not full bodies — bodies are full of generic implementation prose
// ("add a function", "write a test") that dilutes the query and pulls in
// unrelated sessions. Keeping the query tight keeps prior-art precise.
func deriveQuery(in Input) string {
	var b strings.Builder
	for _, s := range in.Sections {
		if s.Heading != "" {
			b.WriteString(s.Heading)
			b.WriteByte(' ')
		} else {
			// the preamble (empty heading) typically holds the plan title /
			// one-line topic — high signal, include it.
			b.WriteString(s.Body)
			b.WriteByte(' ')
		}
	}

	keywords := extractKeywords(b.String())
	return strings.Join(keywords, " ")
}

// stopwords are common English + plan-boilerplate tokens that carry no topical
// signal. Filtering them keeps the AND-matched ledgersearch query from being
// over-constrained by noise words that appear in every session.
var stopwords = map[string]struct{}{
	"the": {}, "and": {}, "for": {}, "with": {}, "this": {}, "that": {},
	"from": {}, "into": {}, "are": {}, "was": {}, "will": {}, "should": {},
	"add": {}, "use": {}, "new": {}, "via": {}, "per": {}, "any": {},
	"all": {}, "not": {}, "but": {}, "our": {}, "its": {}, "out": {},
	"plan": {}, "task": {}, "step": {}, "code": {}, "file": {}, "test": {},
	"todo": {}, "section": {}, "implement": {}, "implementation": {},
	"function": {}, "method": {}, "support": {}, "feature": {},
}

// extractKeywords lowercases, tokenizes, drops stopwords/short tokens, dedupes,
// and ranks by frequency so the most-repeated salient terms lead the query.
// Returned slice is bounded so the AND-matched query stays satisfiable —
// requiring 20 terms to co-occur in one session would match nothing.
func extractKeywords(text string) []string {
	const maxKeywords = 8

	counts := make(map[string]int)
	var order []string
	for _, tok := range tokenizeWords(text) {
		if len(tok) < minKeyword {
			continue
		}
		if _, skip := stopwords[tok]; skip {
			continue
		}
		if _, seen := counts[tok]; !seen {
			order = append(order, tok)
		}
		counts[tok]++
	}
	if len(order) == 0 {
		return nil
	}

	// stable sort by frequency desc, then original appearance order (already
	// the order slice's order) for ties — deterministic output.
	sort.SliceStable(order, func(i, j int) bool {
		return counts[order[i]] > counts[order[j]]
	})

	if len(order) > maxKeywords {
		order = order[:maxKeywords]
	}
	return order
}

// tokenizeWords splits text into lowercase alphanumeric tokens. Mirrors
// ledgersearch tokenization semantics so the query terms line up with how the
// index scores them.
func tokenizeWords(text string) []string {
	var out []string
	var cur strings.Builder
	for _, r := range strings.ToLower(text) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			cur.WriteRune(r)
			continue
		}
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// priorArtHit is a thresholded, ranked ledger result ready to become an
// Annotation. Kept separate from ledgersearch.Result so ranking/threshold logic
// is pure and testable without constructing on-disk fixtures.
type priorArtHit struct {
	Score    float64
	DocType  string
	SourceID string
	Author   string
	Date     string // YYYY-MM-DD if derivable, else ""
}

// rankHits filters raw ledger results below the threshold, sorts by score
// (recency as tiebreak), and caps to maxPriorArtHits. Pure function — the unit
// tests exercise threshold + cap + ordering directly.
func rankHits(results []ledgersearch.Result) []priorArtHit {
	hits := make([]priorArtHit, 0, len(results))
	for _, r := range results {
		if r.Score < priorArtScoreThreshold {
			continue
		}
		author, date := parseSessionAuthorDate(r.SourceID, r.CreatedAt)
		hits = append(hits, priorArtHit{
			Score:    r.Score,
			DocType:  r.DocType,
			SourceID: r.SourceID,
			Author:   author,
			Date:     date,
		})
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Date > hits[j].Date
	})

	if len(hits) > maxPriorArtHits {
		hits = hits[:maxPriorArtHits]
	}
	return hits
}

// parseSessionAuthorDate extracts the author username and a YYYY-MM-DD date from
// a session folder name of the form "YYYY-MM-DDTHH-MM-<username>-<agentID>".
// Murmurs (and any non-conforming name) yield empty author; date falls back to
// the parsed CreatedAt timestamp when the name doesn't carry one.
func parseSessionAuthorDate(sourceID, createdAt string) (author, date string) {
	date = dateOnly(createdAt)

	// session name prefix is exactly 16 chars: "YYYY-MM-DDTHH-MM".
	if len(sourceID) >= 18 && sourceID[10] == 'T' && sourceID[13] == '-' {
		if date == "" {
			date = sourceID[:10]
		}
		rest := sourceID[16:] // "-<username>-<agentID>"
		rest = strings.TrimPrefix(rest, "-")
		// username is everything up to the last hyphen (agentID has no hyphen).
		if i := strings.LastIndex(rest, "-"); i > 0 {
			author = rest[:i]
		} else {
			author = rest
		}
		return author, date
	}

	// plan dir names are "YYYY-MM-DD-<slug>" (no author embedded); recover the
	// date from the prefix so a plan hit still carries a recency tiebreak and a
	// dated annotation. Author stays empty — meta.json authors aren't in the ref.
	if date == "" && len(sourceID) >= 10 && sourceID[4] == '-' && sourceID[7] == '-' {
		if _, err := time.Parse("2006-01-02", sourceID[:10]); err == nil {
			date = sourceID[:10]
		}
	}
	return author, date
}

// dateOnly reduces an ISO-8601 timestamp to its YYYY-MM-DD prefix. Returns ""
// when the input isn't a recognizable timestamp.
func dateOnly(ts string) string {
	if ts == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.Format("2006-01-02")
	}
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ""
}

// annotationFromHit renders a prior-art Annotation. The SourceURL carries the
// session ref (folder name) so a human/agent can `ox session view <ref>`.
func annotationFromHit(h priorArtHit) Annotation {
	who := h.Author
	if who == "" {
		who = "a teammate"
	}

	verb := "worked on"
	subject := "session"
	switch h.DocType {
	case "murmur":
		verb = "mentioned"
		subject = "murmur"
	case "plan":
		// a saved plan is the strongest prior-art signal — someone already
		// scoped this exact work. Phrase it as "planned this in plan <slug>".
		verb = "planned"
		subject = "plan"
	}

	why := who + " " + verb + " this in " + subject + " " + h.SourceID
	if h.Date != "" {
		why += " (" + h.Date + ")"
	}

	return Annotation{
		Kind:      BadgeDeterministic,
		Type:      BadgePriorArt,
		Why:       why,
		SourceURL: h.SourceID,
		Expert:    h.Author,
	}
}
