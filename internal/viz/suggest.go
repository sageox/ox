package viz

import (
	"sort"
	"strings"
	"unicode"
)

// Suggestion is an actionable, deterministic catalog match. It is intentionally
// small because AI coworkers consume it directly from `ox viz suggest --json`.
type Suggestion struct {
	ID        string `json:"id"`
	Category  string `json:"category"`
	Authoring string `json:"authoring"`
	Reason    string `json:"reason"`
	Next      string `json:"next"`
}

type scoredSuggestion struct {
	order int
	score int
	hits  []string
	p     Pattern
}

// Suggest ranks reviewed catalog tags against an intent. It never calls a model
// or the network: ox supplies a precise visual vocabulary; the AI coworker still
// decides what the artifact should say.
func Suggest(intent string, limit int) []Suggestion {
	if limit <= 0 {
		limit = 3
	}
	q := normalize(intent)
	qTokens := tokenSet(q)
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
		if score > 0 {
			scored = append(scored, scoredSuggestion{order: order, score: score, hits: unique(hits), p: p})
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
		out = append(out, Suggestion{
			ID: s.p.ID, Category: s.p.Category, Authoring: s.p.Authoring,
			Reason: "matched " + strings.Join(hits, ", "), Next: next,
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
