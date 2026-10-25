package decision

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/config"
)

// looseIDRe matches loose DR-id query forms: "ADR-021", "adr 21", "021".
var looseIDRe = regexp.MustCompile(`(?i)^([a-z]{2,4})?[\s-]?0*(\d{1,4})$`)

// RelevantDR is a scored corpus hit for external consumers (the ox plan
// context bundle ties plans back to the DRs that shaped them).
type RelevantDR struct {
	ID      string
	Title   string
	RelPath string
	Excerpt string
	Date    string
	Score   float64
}

// Relevant walks this repo's DR corpus fresh and returns a bounded page of
// above-floor records plus the number omitted. Ranking gives priority to the
// earliest query prefix that provides enough evidence to clear the relevance
// floor, then uses stable record metadata. Appending detail can therefore add
// matches without reordering or evicting matches found by the shorter prefix
// query. Zero LLM/network; fail-open on any miss.
// Exported for internal/plan so both enrich commands share the same retrieval.
func Relevant(gitRoot, query string, limit int) ([]RelevantDR, int) {
	if gitRoot == "" || strings.TrimSpace(query) == "" || limit <= 0 {
		return nil, 0
	}
	var cfg *config.DecisionConfig
	if pc, err := config.LoadProjectConfig(gitRoot); err == nil && pc != nil {
		cfg = pc.Decision
	}
	corpus := LoadCorpus(gitRoot, cfg)
	if len(corpus) == 0 {
		return nil, 0
	}
	var out []RelevantDR
	matches := relevantCorpus(corpus, query)
	for _, s := range matches {
		if len(out) >= limit {
			break
		}
		out = append(out, RelevantDR{
			ID:      s.rec.ID,
			Title:   s.rec.Title,
			RelPath: s.rec.RelPath,
			Excerpt: s.rec.Excerpt,
			Date:    s.rec.Date,
			Score:   s.score,
		})
	}
	return out, len(matches) - len(out)
}

// minDRScore is the relevance floor for the DR-corpus scorer (Relevant,
// relatedDetector, relatedRetriever). It is deliberately SEPARATE from
// minBundleScore, which gates the ledgersearch/sessions path on a different
// score scale ([0.5,1.0]). Under the saturating fieldScore (see below), a match
// on a single title term scores ~0.43 and a two-term title match ~0.60, while a
// lone excerpt hit scores ~0.20 — so 0.30 admits real title/anchor matches and
// rejects excerpt-only noise. Errs toward recall by design (#823): callers can
// see what the floor dropped via `--explain`.
const minDRScore = 0.30

// fieldScoreK is the saturation constant in fieldScore's mw/(mw+K). Larger K
// makes scores rise more slowly with matched signal; K=4 keeps a single title
// term below a two-term title match while both clear minDRScore.
const fieldScoreK = 4.0

// scored pairs a corpus record with its lexical relevance to the input terms.
type scored struct {
	rec   Record
	score float64
}

// relevantCorpus filters to the relevance floor and orders matches so a
// prefix query's bounded results survive when more descriptive words are
// appended. A candidate ranks by the first query position where its cumulative
// field evidence clears the floor; a record that needs appended evidence must
// therefore follow every record that the original prefix already qualified.
func relevantCorpus(corpus []Record, query string) []scored {
	terms := tokenize(query)
	if len(terms) == 0 {
		return nil
	}
	var out []scored
	for _, s := range scoreCorpus(corpus, query) {
		if s.score >= minDRScore {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		iPos, iWeight := qualificationRank(terms, out[i].rec, query)
		jPos, jWeight := qualificationRank(terms, out[j].rec, query)
		if iPos != jPos {
			return iPos < jPos
		}
		if iWeight != jWeight {
			return iWeight > jWeight
		}
		if out[i].rec.Date != out[j].rec.Date {
			return out[i].rec.Date > out[j].rec.Date
		}
		return out[i].rec.ID < out[j].rec.ID
	})
	return out
}

func qualificationRank(terms []string, rec Record, query string) (int, int) {
	if queryContainsRecordID(query, rec.ID) {
		return -1, 3
	}
	title, mid, excerpt := recordFields(rec)
	seen := make(map[string]struct{}, len(terms))
	var cumulative float64
	for i, term := range terms {
		if _, duplicate := seen[term]; duplicate {
			continue
		}
		seen[term] = struct{}{}
		weight := 0
		switch {
		case strings.Contains(title, term):
			weight = 3
		case strings.Contains(mid, term):
			weight = 2
		case strings.Contains(excerpt, term):
			weight = 1
		}
		cumulative += float64(weight)
		if cumulative/(cumulative+fieldScoreK) >= minDRScore {
			return i, weight
		}
	}
	return len(terms), 0
}

// droppedCandidates lists records omitted by the related-annotation cap, then
// records that matched below minDRScore. Surfaced under --explain so a caller
// can distinguish a true miss from a bounded or thresholded result (#823).
// The explanation itself is bounded so a broad query cannot flood output.
func droppedCandidates(env *Env, in Input) []DroppedCandidate {
	terms := in.Terms()
	if strings.TrimSpace(terms) == "" || len(env.Corpus) == 0 {
		return nil
	}
	var out []DroppedCandidate
	related := 0
	for _, s := range relevantCorpus(env.Corpus, terms) {
		if in.Path != "" && samePath(in.Path, s.rec.Path) {
			continue
		}
		related++
		if related <= relatedCap {
			continue
		}
		out = append(out, DroppedCandidate{
			Ref:     s.rec.ID,
			RefPath: s.rec.RelPath,
			Title:   s.rec.Title,
			Score:   s.score,
			Reason:  fmt.Sprintf("cleared the relevance floor but ranked below the %d-item related-decision annotation cap", relatedCap),
		})
		if len(out) >= 10 {
			return out
		}
	}
	for _, s := range scoreCorpus(env.Corpus, terms) {
		if in.Path != "" && samePath(in.Path, s.rec.Path) {
			continue
		}
		if s.score >= minDRScore {
			continue // surfaced already, not dropped
		}
		out = append(out, DroppedCandidate{
			Ref:     s.rec.ID,
			RefPath: s.rec.RelPath,
			Title:   s.rec.Title,
			Score:   s.score,
			Reason:  fmt.Sprintf("scored %.3f, below the relevance floor %.2f", s.score, minDRScore),
		})
		if len(out) >= 10 {
			break
		}
	}
	return out
}

// scoreCorpus ranks corpus records against query terms with the house lexical
// approach (ledgersearch family): tokenize, AND-lean scoring, field weights —
// exact-ID short-circuit 1.0, title ×3, anchors/status/deciders ×2, excerpt ×1.
// Deterministic order: score desc, date desc, id asc. Full-text body search
// stays codedb's job (`ox code search`); this scorer only ranks the structured
// fields the parser extracted.
func scoreCorpus(corpus []Record, query string) []scored {
	terms := tokenize(query)
	if len(terms) == 0 {
		return nil
	}
	qUpper := strings.ToUpper(strings.TrimSpace(query))

	var out []scored
	for _, rec := range corpus {
		if rec.ID != "" && (rec.ID == normalizeLooseID(qUpper) || queryContainsRecordID(query, rec.ID)) {
			out = append(out, scored{rec: rec, score: 1.0})
			continue
		}
		s := fieldScore(terms, rec)
		if s > 0 {
			out = append(out, scored{rec: rec, score: s})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		if out[i].rec.Date != out[j].rec.Date {
			return out[i].rec.Date > out[j].rec.Date
		}
		return out[i].rec.ID < out[j].rec.ID
	})
	return out
}

func queryContainsRecordID(query, recordID string) bool {
	if recordID == "" {
		return false
	}
	if normalizeLooseID(strings.ToUpper(strings.TrimSpace(query))) == recordID {
		return true
	}
	for _, match := range refTokenRe.FindAllStringSubmatch(query, -1) {
		if normalizeRefToken(match[1], match[2]) == recordID {
			return true
		}
	}
	return false
}

// fieldScore ranks a record by how much distinctive signal the query lands on
// its structured fields — NOT by what fraction of the query matched. The query
// is often a whole plan or issue body, so the old coverage=matched/len(terms)
// let every off-topic word dilute a real match until the record vanished
// (#823). Instead we sum field-weighted hits over the DISTINCT query terms
// (title 3, anchors/status/deciders 2, excerpt 1 — best field per term) and
// saturate: score = mw/(mw+fieldScoreK), always in [0,1).
//
// This is monotonic in the query: adding a term can only add a match (raising
// mw) or match nothing (leaving mw unchanged), never remove one — so a longer
// query can never score a record below a shorter subset query. That is exactly
// the invariant #823 needs, and TestScoreCorpus_MonotonicInQueryLength guards it.
func fieldScore(terms []string, rec Record) float64 {
	title, mid, excerpt := recordFields(rec)

	seen := make(map[string]struct{}, len(terms))
	matched := 0
	var mw float64
	for _, t := range terms {
		if _, dup := seen[t]; dup {
			continue // a repeated term is one signal, not many
		}
		seen[t] = struct{}{}
		switch { // credit the strongest field the term appears in, once
		case strings.Contains(title, t):
			mw += 3
			matched++
		case strings.Contains(mid, t):
			mw += 2
			matched++
		case strings.Contains(excerpt, t):
			mw += 1
			matched++
		}
	}
	if matched == 0 {
		return 0
	}
	return mw / (mw + fieldScoreK)
}

func recordFields(rec Record) (title, mid, excerpt string) {
	title = strings.ToLower(rec.Title)
	var anchors strings.Builder
	for _, d := range rec.DSections {
		anchors.WriteString(strings.ToLower(d.Heading))
		anchors.WriteByte(' ')
	}
	mid = anchors.String() + strings.ToLower(rec.Status) + " " + strings.ToLower(strings.Join(rec.Deciders, " "))
	excerpt = strings.ToLower(rec.Excerpt)
	return title, mid, excerpt
}

// tokenize mirrors the ledgersearch tokenizer: lowercase alphanumeric terms,
// punctuation stripped, stopword-light (short tokens dropped).
func tokenize(q string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		t := cur.String()
		cur.Reset()
		if len(t) < 3 {
			return
		}
		switch t {
		case "the", "and", "for", "with", "this", "that", "into", "over", "our",
			"about", "accepted", "decision", "need", "proposal", "record", "want":
			return
		}
		out = append(out, t)
	}
	for _, r := range strings.ToLower(q) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// normalizeLooseID canonicalizes loose id forms ("adr 21", "ADR-021", "021")
// to the catalog form "ADR-021"; empty when the input isn't id-shaped.
func normalizeLooseID(q string) string {
	m := looseIDRe.FindStringSubmatch(strings.TrimSpace(q))
	if m == nil {
		return ""
	}
	prefix := strings.ToUpper(m[1])
	if prefix == "" {
		prefix = "ADR"
	}
	return normalizeRefToken(prefix, m[2])
}
