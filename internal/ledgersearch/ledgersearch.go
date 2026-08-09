// Package ledgersearch provides zero-network, local-only full-text search over
// the cached ledger contents: session summaries/markdown, recent murmurs, and
// captured plans (data/plans/<dated-slug>/plan.md). Including plans closes the
// prior-art flywheel — a plan saved by `ox plan` resurfaces in future plans.
//
// Design choice (see ox-m01h): this is an in-memory grep over a bounded window
// of recent files rather than a persistent bleve index. Rationale:
//   - The ledger sparse-checkout already keeps the on-disk footprint small
//     (default 30 days of github data, 12 hours of murmurs, all sessions
//     within sparse checkout).
//   - For a repo of ox's size (~250 sessions over months) a cold scan over
//     just the small per-session summary.md + meta.json files is well under
//     50ms on warm cache, which satisfies the target p95.
//   - Building a bleve index would add an indexing dependency to every hook
//     invocation and add cache-staleness logic for marginal gain at current
//     sizes. Revisit if session count grows or queries get more complex.
//
// All callers MUST go through Search — the package guarantees:
//   - Zero network calls.
//   - Fail-open: any read error or empty cache returns empty results, nil err.
//   - Bounded scan: respects MaxSessionAge and result limit.
package ledgersearch

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MaxSessionAge bounds how far back we scan session folders.
// Older sessions exist in git history but aren't worth scanning live for
// the UserPromptSubmit hook latency budget.
const MaxSessionAge = 90 * 24 * time.Hour

// MaxMurmurAge bounds murmur scanning. Sparse checkout currently keeps only
// the last 12 hours hydrated; we scan a slightly wider window to be safe.
const MaxMurmurAge = 7 * 24 * time.Hour

// MaxPlanAge bounds how far back we scan captured plans. Plans are the prior-art
// flywheel's highest-signal corpus (a saved plan should resurface in future
// plans), so we keep the same generous window as sessions rather than the
// tighter murmur window.
const MaxPlanAge = 90 * 24 * time.Hour

// DefaultLimit is returned when callers pass limit <= 0.
const DefaultLimit = 5

// maxHitsPerTerm bounds the per-term term-frequency scan so scoring stays cheap
// on large documents. It also bounds the density bonus: extraHits can never
// exceed (maxHitsPerTerm-1) * len(terms), which is why tfDensityDivisor has to
// be tuned against it rather than picked freely.
const maxHitsPerTerm = 10

// tfDensityDivisor scales the density bonus in scoreContent. Raising it makes
// scoring stricter about density and therefore biased against large documents;
// lowering it lets bigger on-topic documents clear callers' thresholds. See the
// tuning rationale in scoreContent — this is the knob that decides how large an
// on-topic document may be and still be findable.
const tfDensityDivisor = 5.0

// Result is a single hit returned to callers. Shape mirrors api.QueryResult
// loosely so 'ox query --local' output stays compatible with team-context
// query consumers, but this package stays free of api/ imports to keep the
// dependency graph clean.
type Result struct {
	// Score is a simple 0..1 relevance score (term-frequency normalized).
	Score float64 `json:"score"`
	// Text is a short snippet around the matched term.
	Text string `json:"text"`
	// DocType is "session", "murmur", or "plan".
	DocType string `json:"doc_type"`
	// FilePath is the absolute on-disk path of the source file.
	FilePath string `json:"file_path"`
	// SourceType is "ledger" — distinguishes from team-context results when merged.
	SourceType string `json:"source_type"`
	// SourceID is the session folder name, murmur ID, or plan folder name
	// (the dated slug, e.g. "2026-06-03-add-cache" — usable to locate the plan).
	SourceID string `json:"source_id"`
	// CreatedAt is the source's timestamp (ISO 8601). Empty if unknown.
	CreatedAt string `json:"created_at,omitempty"`
}

// Options controls a Search invocation.
type Options struct {
	// LedgerPath is the absolute path to the ledger checkout. If empty,
	// Search returns no results (fail-open: no ledger = no local matches).
	LedgerPath string
	// Query is the search text. Multi-word queries are AND-matched.
	Query string
	// Limit caps the number of results. <=0 uses DefaultLimit.
	Limit int
	// Now is injectable for tests. Zero value uses time.Now().
	Now time.Time
}

// Search scans the local ledger for query matches. Pure-local, no network.
// Returns ([], nil) on any read error — this is intentional fail-open behavior
// per the ox-m01h spec (hooks must never error out on missing cache).
func Search(opts Options) ([]Result, error) {
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		return nil, nil
	}
	if opts.LedgerPath == "" {
		return nil, nil
	}
	if opts.Limit <= 0 {
		opts.Limit = DefaultLimit
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	// short-circuit if ledger directory doesn't exist (first session before sync)
	if _, err := os.Stat(opts.LedgerPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		slog.Debug("ledgersearch: ledger path stat failed", "path", opts.LedgerPath, "err", err)
		return nil, nil
	}

	terms := tokenize(query)
	if len(terms) == 0 {
		return nil, nil
	}

	var results []Result
	results = append(results, scanSessions(opts.LedgerPath, terms, now)...)
	results = append(results, scanMurmurs(opts.LedgerPath, terms, now)...)
	results = append(results, scanPlans(opts.LedgerPath, terms, now)...)
	results = append(results, scanPlanFeedback(opts.LedgerPath, terms, now)...)

	// sort by score desc, ties broken by recency desc
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].CreatedAt > results[j].CreatedAt
	})

	if len(results) > opts.Limit {
		results = results[:opts.Limit]
	}
	return results, nil
}

// tokenize splits a query into lowercase terms. Strips punctuation so
// "auth?" matches "auth".
func tokenize(q string) []string {
	var out []string
	var cur strings.Builder
	for _, r := range strings.ToLower(q) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
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

// isDraftSession reports whether a ledger session directory holds a draft
// placeholder (ADR-029) rather than a finalized recording.
//
// A draft has no summary.md or session.md of its own, so skipping it is usually
// a no-op — but the SageOx server may author a summary against the zero-turn
// draft and push it, and indexing that would make a fabricated summary of an
// empty session searchable and citable as team knowledge. Finalize purges those;
// this keeps them out of results in the meantime.
//
// Decoded locally rather than via lfs.ReadSessionMeta, matching planTimestamp
// below: this runs on the UserPromptSubmit hook latency path, and the package
// deliberately carries no ox-internal dependencies. Fail-open — an unreadable
// or absent meta.json is not a draft, so a malformed file can never make a real
// session invisible to search.
func isDraftSession(sessionPath string) bool {
	data, err := os.ReadFile(filepath.Join(sessionPath, "meta.json"))
	if err != nil {
		return false
	}
	var meta struct {
		Draft bool `json:"draft"`
	}
	if json.Unmarshal(data, &meta) != nil {
		return false
	}
	return meta.Draft
}

// scanSessions walks sessions/<session-name>/ and scores summary/meta hits.
// Reads only summary.md and meta.json — skips raw.jsonl (large, low signal,
// often dehydrated to LFS pointer files).
func scanSessions(ledgerPath string, terms []string, now time.Time) []Result {
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil
	}

	cutoff := now.Add(-MaxSessionAge)
	var results []Result

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		ts := parseSessionTimestamp(name)
		if !ts.IsZero() && ts.Before(cutoff) {
			continue
		}
		sessionPath := filepath.Join(sessionsDir, name)

		if isDraftSession(sessionPath) {
			continue
		}

		// scan summary.md first (highest signal), fall back to session.md
		for _, candidate := range []string{"summary.md", "session.md"} {
			path := filepath.Join(sessionPath, candidate)
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				continue
			}
			score, snippet := scoreContent(string(data), terms)
			if score <= 0 {
				continue
			}
			results = append(results, Result{
				Score:      score,
				Text:       snippet,
				DocType:    "session",
				FilePath:   path,
				SourceType: "ledger",
				SourceID:   name,
				CreatedAt:  ts.Format(time.RFC3339),
			})
			break // one hit per session
		}
	}
	return results
}

// scanMurmurs walks data/murmurs/YYYY-MM-DD/HH/*.json scoring content matches.
func scanMurmurs(ledgerPath string, terms []string, now time.Time) []Result {
	murmursDir := filepath.Join(ledgerPath, "data", "murmurs")
	if _, err := os.Stat(murmursDir); err != nil {
		return nil
	}

	cutoff := now.Add(-MaxMurmurAge)
	var results []Result

	dayEntries, err := os.ReadDir(murmursDir)
	if err != nil {
		return nil
	}
	for _, day := range dayEntries {
		if !day.IsDir() {
			continue
		}
		dayTime, perr := time.Parse("2006-01-02", day.Name())
		if perr != nil || dayTime.Before(cutoff.Truncate(24*time.Hour)) {
			continue
		}
		dayPath := filepath.Join(murmursDir, day.Name())
		hourEntries, _ := os.ReadDir(dayPath)
		for _, hour := range hourEntries {
			if !hour.IsDir() {
				continue
			}
			hourPath := filepath.Join(dayPath, hour.Name())
			files, _ := os.ReadDir(hourPath)
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
					continue
				}
				path := filepath.Join(hourPath, f.Name())
				data, rerr := os.ReadFile(path)
				if rerr != nil {
					continue
				}
				var m struct {
					ID        string    `json:"id"`
					Timestamp time.Time `json:"timestamp"`
					Topic     string    `json:"topic"`
					Content   string    `json:"content"`
				}
				if jerr := json.Unmarshal(data, &m); jerr != nil {
					continue
				}
				if !m.Timestamp.IsZero() && m.Timestamp.Before(cutoff) {
					continue
				}
				// search across topic + content
				score, snippet := scoreContent(m.Topic+"\n"+m.Content, terms)
				if score <= 0 {
					continue
				}
				results = append(results, Result{
					Score:      score,
					Text:       snippet,
					DocType:    "murmur",
					FilePath:   path,
					SourceType: "ledger",
					SourceID:   m.ID,
					CreatedAt:  m.Timestamp.Format(time.RFC3339),
				})
			}
		}
	}
	return results
}

// scanPlans walks data/plans/<YYYY-MM-DD-slug>/ and scores plan.md hits. This
// closes the prior-art flywheel: a plan saved by `ox plan` must resurface as
// prior art in future plans. Mirrors scanSessions — same TF scoring, same age
// filter, same Result shape — but reads the canonical plan.md (always plain
// git, never an LFS pointer) and pulls created_at from meta.json when present.
// Fail-open: a missing data/plans/ dir yields no results, never an error.
func scanPlans(ledgerPath string, terms []string, now time.Time) []Result {
	plansDir := filepath.Join(ledgerPath, "data", "plans")
	entries, err := os.ReadDir(plansDir)
	if err != nil {
		// missing dir (no plans saved yet) or unreadable — fail open.
		return nil
	}

	cutoff := now.Add(-MaxPlanAge)
	var results []Result

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		planPath := filepath.Join(plansDir, name)

		// created_at comes from meta.json (authoritative); fall back to the
		// dated dir-name prefix when meta is missing/partial so age filtering
		// and recency tiebreaks still work on a partial plan dir.
		ts := planTimestamp(planPath, name)
		if !ts.IsZero() && ts.Before(cutoff) {
			continue
		}

		// plan.md is the canonical plan text — always plain git, the same role
		// summary.md plays for sessions.
		path := filepath.Join(planPath, "plan.md")
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			continue
		}
		score, snippet := scoreContent(string(data), terms)
		if score <= 0 {
			continue
		}
		results = append(results, Result{
			Score:      score,
			Text:       snippet,
			DocType:    "plan",
			FilePath:   path,
			SourceType: "ledger",
			SourceID:   name, // dated slug — locates the plan via `ox plan view`
			CreatedAt:  ts.Format(time.RFC3339),
		})
	}
	return results
}

// scanPlanFeedback walks data/plans/<dated-slug>/feedback/round-*.json and
// scores each human review round as its own document. Review feedback is
// sacred-tier data — a reviewer's words on a plan must stay findable by asking
// ("what did Sam flag on the auth plan?"), not only by opening the right plan.
// One doc per round: reviewer, per-item status/section/label, and the notes.
// Fail-open like every scanner here: unreadable dirs and malformed rounds are
// skipped, never an error.
func scanPlanFeedback(ledgerPath string, terms []string, now time.Time) []Result {
	plansDir := filepath.Join(ledgerPath, "data", "plans")
	entries, err := os.ReadDir(plansDir)
	if err != nil {
		return nil
	}

	cutoff := now.Add(-MaxPlanAge)
	var results []Result

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		planPath := filepath.Join(plansDir, name)
		if ts := planTimestamp(planPath, name); !ts.IsZero() && ts.Before(cutoff) {
			continue
		}
		fbDir := filepath.Join(planPath, "feedback")
		rounds, rerr := os.ReadDir(fbDir)
		if rerr != nil {
			continue
		}
		for _, r := range rounds {
			if r.IsDir() || !strings.HasPrefix(r.Name(), "round-") || !strings.HasSuffix(r.Name(), ".json") {
				continue
			}
			path := filepath.Join(fbDir, r.Name())
			data, ferr := os.ReadFile(path)
			if ferr != nil {
				continue
			}
			var round struct {
				Reviewer  string    `json:"reviewer"`
				CreatedAt time.Time `json:"created_at"`
				Items     []struct {
					Section  string `json:"section"`
					Label    string `json:"label"`
					Status   string `json:"status"`
					Note     string `json:"note"`
					Reviewer string `json:"reviewer"`
				} `json:"items"`
			}
			if jerr := json.Unmarshal(data, &round); jerr != nil {
				continue
			}
			var b strings.Builder
			if round.Reviewer != "" {
				b.WriteString(round.Reviewer)
				b.WriteByte('\n')
			}
			for _, it := range round.Items {
				for _, f := range []string{it.Reviewer, it.Status, it.Section, it.Label, it.Note} {
					if f != "" {
						b.WriteString(f)
						b.WriteByte(' ')
					}
				}
				b.WriteByte('\n')
			}
			score, snippet := scoreContent(b.String(), terms)
			if score <= 0 {
				continue
			}
			created := ""
			if !round.CreatedAt.IsZero() {
				created = round.CreatedAt.Format(time.RFC3339)
			}
			results = append(results, Result{
				Score:      score,
				Text:       snippet,
				DocType:    "plan-feedback",
				FilePath:   path,
				SourceType: "ledger",
				SourceID:   name, // dated slug — `ox plan feedback show <slug>` reads the merged state
				CreatedAt:  created,
			})
		}
	}
	return results
}

// planTimestamp resolves a plan's created_at, preferring meta.json's explicit
// timestamp and falling back to the YYYY-MM-DD prefix of the dir name. Returns
// zero time when neither is available — callers treat that as "include".
func planTimestamp(planPath, dirName string) time.Time {
	metaPath := filepath.Join(planPath, "meta.json")
	if data, err := os.ReadFile(metaPath); err == nil {
		var m struct {
			CreatedAt time.Time `json:"created_at"`
		}
		if json.Unmarshal(data, &m) == nil && !m.CreatedAt.IsZero() {
			return m.CreatedAt
		}
	}
	// fall back to the dated dir-name prefix "YYYY-MM-DD-<slug>".
	if len(dirName) >= 10 {
		if t, err := time.Parse("2006-01-02", dirName[:10]); err == nil {
			return t
		}
	}
	return time.Time{}
}

// scoreContent returns a relevance score and a short snippet for content matching all terms.
// AND semantics: every term must appear, or the score is 0. A match scores a flat
// 0.5 floor plus a density bonus in [0, 0.5], so the range is [0.5, 1.0].
// Snippet is the first ~160 chars around the first matched term.
func scoreContent(content string, terms []string) (float64, string) {
	if content == "" {
		return 0, ""
	}
	lower := strings.ToLower(content)
	matched := 0
	firstIdx := -1
	totalHits := 0
	for _, t := range terms {
		idx := strings.Index(lower, t)
		if idx < 0 {
			continue
		}
		matched++
		if firstIdx < 0 || idx < firstIdx {
			firstIdx = idx
		}
		// count tf with a cheap bounded scan (maxHitsPerTerm hits per term)
		hits := 0
		start := 0
		for hits < maxHitsPerTerm {
			next := strings.Index(lower[start:], t)
			if next < 0 {
				break
			}
			hits++
			start += next + len(t)
		}
		totalHits += hits
	}
	if matched == 0 {
		return 0, ""
	}
	// AND semantics: require all terms matched
	if matched < len(terms) {
		return 0, ""
	}
	// The tf bonus rewards term DENSITY, not raw volume. The previous form
	// (totalHits/100, capped) was length-biased: any long transcript that
	// mentioned each term somewhere accumulated enough scattered hits to score
	// ~0.95, so "long and vaguely related" outranked "short and on-topic" and
	// sailed past callers' relevance thresholds (plan enrich surfaced unrelated
	// sessions at 0.95 exactly this way). Normalizing by content size makes the
	// bonus mean "these terms are what this document is ABOUT": hits beyond the
	// first per term, per KB of content.
	//
	// tfDensityDivisor is what keeps this from over-correcting. The scan above
	// caps at maxHitsPerTerm, so extraHits <= (maxHitsPerTerm-1)*len(terms) —
	// which means the divisor sets a hard ceiling on how large a document can
	// be and still clear a caller's threshold. At 20 (the first cut of this
	// fix) a 3-term query could not surface anything over ~13.5 KB past plan
	// enrich's 0.6 gate, silently dropping 14% of this repo's own saved plans
	// no matter how on-topic they were. At 5 a densely on-topic 20 KB plan
	// scores ~0.77 while a 96 KB transcript with scattered hits still decays to
	// ~0.51 — false positives stay gated, true positives survive.
	extraHits := totalHits - matched
	kb := float64(len(content)) / 1024.0
	if kb < 1 {
		// floor tiny documents at 1 KB so a 200-byte doc with one repeat
		// cannot out-score everything by density alone.
		kb = 1
	}
	tfBonus := (float64(extraHits) / kb) / tfDensityDivisor
	if tfBonus > 0.5 {
		tfBonus = 0.5
	}
	score := 0.5 + tfBonus
	return score, snippetAround(content, firstIdx, 160)
}

// snippetAround returns a readable window of about size bytes centered on idx,
// with both edges snapped to whole-word boundaries so the snippet never begins
// or ends mid-word. Elided ends are marked with "...". Snapping at ASCII
// whitespace is also UTF-8 safe: multi-byte runes never contain a byte < 0x80,
// so a cut at a space can't split a rune.
func snippetAround(s string, idx, size int) string {
	if idx < 0 || s == "" {
		return ""
	}
	start := idx - size/2
	if start < 0 {
		start = 0
	}
	end := start + size
	if end > len(s) {
		end = len(s)
	}

	elidedLeft := start > 0
	elidedRight := end < len(s)

	wStart, wEnd := start, end
	if elidedLeft {
		// drop the partial leading word, then advance onto the next word start
		for wStart < wEnd && !isASCIISpace(s[wStart]) {
			wStart++
		}
		for wStart < wEnd && isASCIISpace(s[wStart]) {
			wStart++
		}
	}
	if elidedRight {
		// retreat to the end of the last whole word inside the window
		for wEnd > wStart && !isASCIISpace(s[wEnd-1]) {
			wEnd--
		}
		for wEnd > wStart && isASCIISpace(s[wEnd-1]) {
			wEnd--
		}
	}
	// degenerate case: a single token longer than the window collapsed it —
	// fall back to a rune-safe cut so we still show something.
	if wStart >= wEnd {
		wStart = alignRuneStart(s, start)
		wEnd = alignRuneStart(s, end)
		if wEnd < wStart {
			wEnd = wStart
		}
	}

	out := strings.TrimSpace(s[wStart:wEnd])
	out = strings.Join(strings.Fields(out), " ")
	if out == "" {
		return ""
	}
	if elidedLeft {
		out = "..." + out
	}
	if elidedRight {
		out = out + "..."
	}
	return out
}

// isASCIISpace reports whether b is an ASCII whitespace byte. Used for word-
// boundary snapping where only single-byte ASCII spaces are valid cut points.
func isASCIISpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	default:
		return false
	}
}

// alignRuneStart moves i back to the start of the UTF-8 rune it lands inside,
// so a fallback byte-slice never splits a multi-byte rune.
func alignRuneStart(s string, i int) int {
	if i >= len(s) {
		return len(s)
	}
	for i > 0 && s[i]&0xC0 == 0x80 {
		i--
	}
	return i
}

// parseSessionTimestamp pulls the prefix from "YYYY-MM-DDTHH-MM-<user>-<id>".
// Returns zero time if the prefix doesn't match — callers treat that as "include".
func parseSessionTimestamp(name string) time.Time {
	if len(name) < 16 {
		return time.Time{}
	}
	// reshape "2026-02-13T14-56" -> "2026-02-13T14:56"
	prefix := name[:16]
	if prefix[13] != '-' || prefix[10] != 'T' {
		return time.Time{}
	}
	candidate := prefix[:13] + ":" + prefix[14:]
	t, err := time.Parse("2006-01-02T15:04", candidate)
	if err != nil {
		return time.Time{}
	}
	return t
}
