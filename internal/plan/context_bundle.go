package plan

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/ledgersearch"
	"github.com/sageox/ox/internal/teamdocs"
)

// init self-registers the context-bundle retriever with the global registry so
// the orchestrator (enrich.go) picks it up without being touched. RegisterRetriever
// ignores nil and is concurrency-safe.
func init() {
	RegisterRetriever(&contextBundleRetriever{})
}

// bundleCap caps the TOTAL number of items in the assembled context bundle
// across all kinds. The bundle is what the CLIENT agent reasons over to author
// the judgment badges (aligns/conflicts, expert-perspective); every item costs
// tokens in that agent's context window. Past the top handful, additional items
// add noise without changing the agent's conclusion. Capping here protects the
// client's context budget — local retrieval is cheap, but the agent's attention
// is not.
const bundleCap = 12

// murmurBundleWindowHours bounds murmur recency for the bundle. The murmur cache
// is sparsely checked out (~12h hydrated by default), and ReadMurmursInWindow
// hard-caps at ledger.MaxMurmurWindowHours (24h) regardless, so this asks for the
// widest meaningful window. Recent murmurs are the freshest "what's a teammate
// actively thinking about" signal feeding the bundle.
const murmurBundleWindowHours = ledger.MaxMurmurWindowHours

// sessionBundleLimit caps prior sessions pulled from ledgersearch before the
// global bundleCap merge. A few strong session summaries give the client agent
// enough prior-work grounding; more just dilutes.
const sessionBundleLimit = 8

// Relative weights per kind. Murmurs are the freshest (active work), sessions are
// concrete prior work, team decisions/ADRs/docs are durable conventions the
// judgment badges cite. These tune cross-kind ordering after each item's own
// match strength is folded in; they are deliberately close so a strong hit in a
// lower-weighted kind can still outrank a weak hit in a higher one.
const (
	weightMurmur   = 1.00
	weightSession  = 0.95
	weightDecision = 0.90
	weightDoc      = 0.85
)

// contextBundleRetriever assembles the ranked, capped context bundle the client
// agent reasons over. It is RETRIEVAL + RANKING ONLY — ox makes no inference and,
// in the default path, no LLM or network call: every source is local (the ledger
// murmur/session cache and the on-disk team context). FAIL-OPEN: any missing or
// unreadable source contributes nothing rather than erroring.
type contextBundleRetriever struct{}

// Name identifies the retriever in logs and the registry.
func (r *contextBundleRetriever) Name() string { return "context-bundle" }

// Retrieve gathers murmurs, prior sessions, and team decisions/ADRs/docs that
// match the plan, scores and merges them, and returns the top bundleCap items.
// Fail-open everywhere: an empty plan or any source failure yields the items that
// could be gathered (possibly none), never an error.
func (r *contextBundleRetriever) Retrieve(ctx context.Context, in Input, gitRoot string) ([]ContextItem, error) {
	files := planFiles(in)
	headings := sectionHeadings(in)
	query := deriveQuery(in)

	var items []ContextItem
	items = append(items, r.gatherMurmurs(gitRoot, files, headings)...)
	items = append(items, r.gatherSessions(gitRoot, query)...)
	items = append(items, r.gatherTeamContext(gitRoot, query, headings)...)

	if len(items) == 0 {
		return nil, nil
	}

	items = rankAndCap(items, bundleCap)
	slog.Debug("plan context-bundle: assembled", "items", len(items), "files", len(files))
	return items, nil
}

// gatherMurmurs surfaces recent teammate murmurs whose referenced files or topic
// overlap the plan. Reuses resolveLedgerPath + ReadMurmursInWindow + the murmur
// accessor helpers from collision.go rather than reimplementing ledger access.
func (r *contextBundleRetriever) gatherMurmurs(gitRoot string, files, headings []string) []ContextItem {
	ledgerPath := resolveLedgerPath(gitRoot)
	if ledgerPath == "" {
		return nil
	}
	murmurs, err := ledger.ReadMurmursInWindow(ledgerPath, murmurBundleWindowHours)
	if err != nil || len(murmurs) == 0 {
		return nil
	}
	return murmurContextItems(murmurs, files, headings, time.Now().UTC())
}

// gatherSessions pulls the most relevant prior-session summaries from the local
// ledger via ledgersearch, reusing deriveQuery (priorart.go) for the query and
// parseSessionAuthorDate (priorart.go) for attribution. The Ref is the session
// folder name, usable directly by `ox session view <ref>`.
func (r *contextBundleRetriever) gatherSessions(gitRoot, query string) []ContextItem {
	if query == "" {
		return nil
	}
	ledgerPath := resolveLedgerPath(gitRoot)
	if ledgerPath == "" {
		return nil
	}
	results, err := ledgersearch.Search(ledgersearch.Options{
		LedgerPath: ledgerPath,
		Query:      query,
		Limit:      sessionBundleLimit,
	})
	if err != nil || len(results) == 0 {
		return nil
	}
	return sessionContextItems(results)
}

// gatherTeamContext surfaces team decisions, ADRs, and docs that match the plan's
// topic, LOCAL-FIRST from the on-disk team context checkout (the same source
// `ox agent team-ctx` reads). No network in the default path.
func (r *contextBundleRetriever) gatherTeamContext(gitRoot, query string, headings []string) []ContextItem {
	if gitRoot == "" {
		return nil
	}
	tc := config.FindRepoTeamContext(gitRoot)
	if tc == nil || tc.Path == "" {
		return nil
	}

	terms := topicTerms(query, headings)
	if len(terms) == 0 {
		return nil
	}

	docs, err := teamdocs.DiscoverDocs(tc.Path)
	if err != nil {
		slog.Debug("plan context-bundle: discover team docs failed", "error", err)
		docs = nil
	}
	return teamDocContextItems(docs, terms)

	// TODO(cloud-augment): when the local team-context checkout is sparse or the
	// topic match is weak, optionally fan out to the cloud `ox query` / team-ctx
	// API for un-cached decisions and cross-team discussions. That path makes a
	// NETWORK call, so it must stay behind an explicit opt-in and never run in
	// this default, local-first retrieval.
}

// murmurContextItems is the pure core of murmur bundling: given murmurs, the plan
// files, and section headings, it returns one ContextItem per relevant murmur,
// scored by match strength. now is injected so recency phrasing is deterministic
// under test.
func murmurContextItems(murmurs []ledger.MurmurFile, files, headings []string, now time.Time) []ContextItem {
	want := make(map[string]struct{}, len(files))
	for _, f := range files {
		want[f] = struct{}{}
	}

	var items []ContextItem
	for _, m := range murmurs {
		matched, score := scoreMurmur(m, want, headings)
		if !matched {
			continue
		}
		who := murmurAuthor(m)
		ts := m.Timestamp.UTC()
		title := m.Topic
		if title == "" {
			title = "murmur from " + who
		}
		items = append(items, ContextItem{
			Kind:    "murmur",
			Title:   title,
			Ref:     m.ID,
			Snippet: snippet(m.Content, 200),
			Score:   weightMurmur * score,
			Author:  who,
			When:    formatWhen(ts),
		})
	}
	return items
}

// scoreMurmur reports whether a murmur is relevant to the plan and how strongly.
// An explicit file reference (metadata["files"] overlapping a plan file) is the
// strongest signal; a topic-vs-heading token overlap is a softer one. Returns
// (false, 0) when neither fires.
func scoreMurmur(m ledger.MurmurFile, want map[string]struct{}, headings []string) (bool, float64) {
	for _, mf := range murmurFiles(m) {
		if _, ok := matchPlanFile(mf, want); ok {
			return true, 1.0
		}
	}
	if m.Topic != "" && len(headings) > 0 && topicMatchesHeading(m.Topic, headings) {
		return true, 0.6
	}
	return false, 0
}

// sessionContextItems converts ranked ledgersearch results into session/murmur
// ContextItems. ledgersearch returns both doc types; murmurs already covered by
// the dedicated murmur pass are skipped here to avoid double-surfacing — the
// murmur pass carries richer file-match scoring. Sessions become Kind="session"
// with a Ref usable by `ox session view`.
func sessionContextItems(results []ledgersearch.Result) []ContextItem {
	var items []ContextItem
	for _, res := range results {
		if res.DocType != "session" {
			continue
		}
		author, date := parseSessionAuthorDate(res.SourceID, res.CreatedAt)
		title := res.SourceID
		if author != "" {
			title = author + " — " + res.SourceID
		}
		items = append(items, ContextItem{
			Kind:    "session",
			Title:   title,
			Ref:     res.SourceID,
			Snippet: snippet(res.Text, 200),
			Score:   weightSession * res.Score,
			Author:  author,
			When:    date,
		})
	}
	return items
}

// teamDocContextItems matches discovered team docs against the plan's topic terms
// and classifies each by kind (adr / decision / discussion) from its filename and
// title. Score is the fraction of the doc's title+description+name tokens that the
// plan's terms hit — a cheap, deterministic relevance proxy that needs no index.
func teamDocContextItems(docs []teamdocs.TeamDoc, terms map[string]struct{}) []ContextItem {
	var items []ContextItem
	for _, d := range docs {
		haystack := tokenizeWords(strings.ToLower(d.Name + " " + d.Title + " " + d.Description))
		if len(haystack) == 0 {
			continue
		}
		hits := 0
		for _, tok := range haystack {
			if _, ok := terms[tok]; ok {
				hits++
			}
		}
		if hits == 0 {
			continue
		}
		score := float64(hits) / float64(len(haystack))
		title := d.Title
		if title == "" {
			title = d.Name
		}
		items = append(items, ContextItem{
			Kind:    classifyDocKind(d.Name, d.Title),
			Title:   title,
			Ref:     d.Path,
			Snippet: snippet(d.Description, 200),
			Score:   weightDoc * (0.5 + score), // floor so any real match clears weak murmurs
			When:    d.When,
		})
	}
	return items
}

// classifyDocKind maps a team doc to one of the bundle's allowed kinds. An ADR
// (name/title mentioning "adr" or "architecture decision") is an "adr"; a doc
// about a decision is a "decision"; everything else surfaced from team context is
// a "discussion" (the team's shared written knowledge).
func classifyDocKind(name, title string) string {
	hay := strings.ToLower(name + " " + title)
	switch {
	case strings.Contains(hay, "adr") ||
		strings.Contains(hay, "architecture decision") ||
		strings.Contains(hay, "architecture-decision"):
		return "adr"
	case strings.Contains(hay, "decision"):
		return "decision"
	default:
		return "discussion"
	}
}

// topicTerms builds the set of salient lowercase tokens describing the plan's
// topic, drawn from the derived query (priorart.go's keyword extraction) plus the
// section headings. Short tokens are dropped to avoid matching on stopwords.
func topicTerms(query string, headings []string) map[string]struct{} {
	terms := make(map[string]struct{})
	add := func(text string) {
		for _, tok := range tokenizeWords(strings.ToLower(text)) {
			if len(tok) < minKeyword {
				continue
			}
			if _, skip := stopwords[tok]; skip {
				continue
			}
			terms[tok] = struct{}{}
		}
	}
	add(query)
	for _, h := range headings {
		add(h)
	}
	return terms
}

// rankAndCap sorts items by score descending (deterministic tiebreak on kind then
// ref) and truncates to cap. This is the single place the bundle's token budget is
// enforced — see bundleCap's doc comment for why the cap protects the client agent.
func rankAndCap(items []ContextItem, limit int) []ContextItem {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].Ref < items[j].Ref
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

// snippet trims and collapses whitespace in text, truncating to max runes so a
// long murmur/summary body doesn't blow the bundle's per-item footprint.
func snippet(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > max {
		return strings.TrimSpace(string(runes[:max-1])) + "…"
	}
	return text
}

// formatWhen renders a timestamp as YYYY-MM-DD for the bundle's When field,
// returning "" for a zero time so the field is omitted.
func formatWhen(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}
