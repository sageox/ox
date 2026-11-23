package viz

import (
	"sort"
	"strings"
	"unicode"
)

// Suggestion is an actionable, deterministic catalog match. It is intentionally
// small because AI teammates consume it directly from `ox viz suggest --json`.
type Suggestion struct {
	ID        string `json:"id"`
	Category  string `json:"category"`
	Authoring string `json:"authoring"`
	Reason    string `json:"reason"`
	Guidance  string `json:"guidance"`
	Next      string `json:"next"`
}

type scoredSuggestion struct {
	order      int
	score      int
	hits       []string
	p          Pattern
	confidence bool
}

// Suggest ranks reviewed catalog tags against an intent. It never calls a model
// or the network: ox supplies a precise visual vocabulary; the AI teammate still
// decides what the artifact should say.
func Suggest(intent string, limit int) []Suggestion {
	if limit <= 0 {
		limit = 3
	}
	q := normalize(intent)
	rawTokens := tokenSet(q)
	qTokens := expandedTokenSet(q)
	var scored []scoredSuggestion
	for order, p := range Catalog() {
		score := 0
		var hits []string
		for _, tag := range p.Tags {
			t := normalize(tag)
			if t == "" {
				continue
			}
			if phraseContains(q, t) {
				score += 8 + len(strings.Fields(t))
				hits = append(hits, tag)
				continue
			}
			tokens := strings.Fields(t)
			matched := 0
			for _, token := range tokens {
				if qTokens[token] {
					matched++
				}
			}
			if matched > 0 && (len(tokens) == 1 || matched == len(tokens)) {
				score += matched * 3
				hits = append(hits, tag)
			}
		}
		id := normalize(strings.ReplaceAll(p.ID, "-", " "))
		if phraseContains(q, id) {
			score += 12
			hits = append(hits, p.ID)
		}
		// Tags are the retrieval contract. Use/Why contribute only a small
		// secondary signal so normal prose can still find a reviewed pattern
		// without turning every shared word into a confident match.
		if proseHits := matchingProseTokens(p.Use+" "+p.Why, rawTokens); len(proseHits) >= 2 {
			score += len(proseHits)
			hits = append(hits, strings.Join(proseHits, "/"))
		}
		if score > 0 {
			scored = append(scored, scoredSuggestion{order: order, score: score, hits: unique(hits), p: p, confidence: true})
		}
	}
	if len(scored) == 0 && hasVisualIntent(qTokens) {
		// An explicit request to show/explain a system should never silently
		// suppress a visual just because it did not use catalog vocabulary.
		// These are broad, review-safe starting points—not a claim of a precise
		// match—and the reason makes that distinction visible to the caller.
		for _, id := range []string{"architecture", "flowchart", "sequence-diagram"} {
			if p, ok := PatternByID(id); ok {
				scored = append(scored, scoredSuggestion{order: catalogOrder(id), hits: []string{"visual explanation"}, p: p})
			}
		}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].order < scored[j].order
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	out := make([]Suggestion, 0, len(scored))
	for _, s := range scored {
		next := "ox viz " + s.p.ID
		if s.p.Authoring == "ox-render" {
			next = "ox viz render " + s.p.ID + " --data <file.json>"
		}
		hits := s.hits
		if len(hits) > 3 {
			hits = hits[:3]
		}
		reason := "matched " + strings.Join(hits, ", ")
		if !s.confidence {
			reason = "low-confidence fallback for an explicit visual explanation"
		}
		out = append(out, Suggestion{
			ID: s.p.ID, Category: s.p.Category, Authoring: s.p.Authoring,
			Reason: reason, Guidance: s.p.Guidance, Next: next,
		})
	}
	return out
}

func phraseContains(text, phrase string) bool {
	return strings.Contains(" "+text+" ", " "+phrase+" ")
}

func normalize(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

func tokenSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, token := range strings.Fields(s) {
		out[token] = true
	}
	return out
}

// expandedTokenSet adds a deliberately small, reviewed vocabulary for common
// PR-review language. It is not a model or fuzzy matcher: aliases only bridge
// ordinary inflections/concepts to catalog tags, preserving deterministic output.
func expandedTokenSet(s string) map[string]bool {
	out := tokenSet(s)
	base := make([]string, 0, len(out))
	for token := range out {
		base = append(base, token)
	}
	for _, token := range base {
		for _, alias := range visualAliases[token] {
			out[alias] = true
		}
	}
	return out
}

var visualAliases = map[string][]string{
	"reconcile":      {"reconciled", "reconciliation", "flow"},
	"reconciled":     {"reconcile", "reconciliation", "flow"},
	"reconciliation": {"reconcile", "reconciled", "flow"},
	"dedupe":         {"deduplication"},
	"deduplicated":   {"deduplication"},
	"deduplication":  {"dedupe", "deduplicated"},
	"shared":         {"dependency", "dependencies"},
	"recover":        {"recovery", "sequence", "state"},
	"recovered":      {"recovery", "sequence", "state"},
	"recovery":       {"recover", "sequence", "state"},
	"interrupted":    {"recovery", "sequence"},
}

func matchingProseTokens(prose string, query map[string]bool) []string {
	proseTokens := tokenSet(normalize(prose))
	hits := make([]string, 0, 3)
	for token := range proseTokens {
		if len(token) >= 5 && query[token] && !visualStopWords[token] {
			hits = append(hits, token)
		}
	}
	sort.Strings(hits)
	if len(hits) > 3 {
		hits = hits[:3]
	}
	return hits
}

var visualStopWords = map[string]bool{
	"about": true, "after": true, "before": true, "between": true,
	"does": true, "from": true, "have": true, "how": true, "land": true,
	"other": true, "their": true, "these": true, "this": true, "those": true,
	"where": true, "which": true, "would": true,
}

func hasVisualIntent(tokens map[string]bool) bool {
	for _, token := range []string{
		"show", "explain", "architecture", "flow", "workflow", "diagram",
		"visual", "compare", "comparison", "before", "after", "timeline",
		"sequence", "state", "dependency", "dependencies", "component",
	} {
		if tokens[token] {
			return true
		}
	}
	return false
}

func catalogOrder(id string) int {
	for order, p := range Catalog() {
		if p.ID == id {
			return order
		}
	}
	return len(Catalog())
}

func unique(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
