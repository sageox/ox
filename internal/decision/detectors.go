package decision

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// This file holds the v1 deterministic detectors and the corpus retriever.
// All register via init() (the internal/plan registry pattern) and fail open.

func init() {
	RegisterDetector(relatedDetector{})
	RegisterDetector(conventionsDetector{})
	RegisterDetector(refsDetector{})
	RegisterDetector(driftDetector{})
	RegisterRetriever(relatedRetriever{})
}

const (
	// bundleCap / minScore mirror the plan context-bundle conventions.
	bundleCap      = 12
	minBundleScore = 0.55
	// relatedCap bounds related-decision annotations so a broad topic does not
	// bury numbering and diagnostics. Overflow is reported explicitly.
	relatedCap = 5
)

// ---------------------------------------------------------------- related ---

type relatedDetector struct{}

func (relatedDetector) Name() string { return "related-decisions" }

func (relatedDetector) Detect(_ context.Context, env *Env, in Input) ([]Annotation, error) {
	terms := in.Terms()
	if terms == "" || len(env.Corpus) == 0 {
		return nil, nil
	}
	var matches []scored
	for _, s := range relevantCorpus(env.Corpus, terms) {
		if in.Path != "" && samePath(in.Path, s.rec.Path) {
			continue // the file being enriched is not "related" to itself
		}
		matches = append(matches, s)
	}
	var out []Annotation
	for _, s := range matches {
		if len(out) >= relatedCap {
			break
		}
		ann := Annotation{
			Kind:     BadgeDeterministic,
			Type:     BadgeRelatedDecision,
			Ref:      s.rec.ID,
			RefPath:  s.rec.RelPath,
			Relation: RelationCandidate,
			Date:     s.rec.Date,
			Why:      relatedWhy(s.rec),
		}
		// overlap against an Accepted DR's decision anchors → the agent should
		// decide amend-vs-supersede explicitly; still a candidate, never a verdict.
		if len(s.rec.DSections) > 0 && strings.HasPrefix(strings.ToLower(s.rec.Status), "accepted") {
			ann.Relation = VariantSupersedeCandidate
			ann.Anchor = s.rec.DSections[0].ID
		}
		out = append(out, ann)
	}
	if omitted := len(matches) - min(len(matches), relatedCap); omitted > 0 {
		out = append(out, Annotation{
			Kind: BadgeDeterministic,
			Type: BadgeDiagnostic,
			Rule: RuleRelatedOverflow,
			Why:  fmt.Sprintf("%d additional related decision candidate(s) cleared the relevance floor but were omitted from this bounded result; narrow the topic or use `ox decision enrich --explain` / `ox code search --decisions` to inspect them", omitted),
		})
	}
	return out, nil
}

func relatedWhy(rec Record) string {
	parts := []string{}
	if s := normalizeStatus(rec.Status); s != "" {
		parts = append(parts, s)
	}
	if rec.Date != "" {
		parts = append(parts, rec.Date)
	}
	meta := ""
	if len(parts) > 0 {
		meta = " (" + strings.Join(parts, ", ") + ")"
	}
	label := rec.ID
	if label == "" {
		label = rec.RelPath
	}
	return fmt.Sprintf("%s%s — %q overlaps this topic; reconcile explicitly (aligns, amends, or supersedes — your call)", label, meta, rec.Title)
}

type relatedRetriever struct{}

func (relatedRetriever) Name() string { return "related-decisions" }

func (relatedRetriever) Retrieve(_ context.Context, env *Env, in Input) ([]ContextItem, error) {
	terms := in.Terms()
	if terms == "" || len(env.Corpus) == 0 {
		return nil, nil
	}
	var out []ContextItem
	for _, s := range relevantCorpus(env.Corpus, terms) {
		if len(out) >= bundleCap {
			break
		}
		if in.Path != "" && samePath(in.Path, s.rec.Path) {
			continue
		}
		rec := s.rec
		label := rec.ID
		if label == "" {
			label = rec.RelPath
		}
		out = append(out, ContextItem{
			Kind:    "decision",
			Title:   strings.TrimSpace(label + " — " + rec.Title),
			Ref:     rec.RelPath,
			Snippet: rec.Excerpt,
			Score:   s.score,
			When:    rec.Date,
			Cite: &Cite{
				ProseHint: fmt.Sprintf("See %s (%s).", label, rec.Title),
				Comment:   fmt.Sprintf("<!-- SOURCE: sageox adr:%s -->", rec.RelPath),
			},
		})
	}
	return out, nil
}

func samePath(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return aa == bb
}

// ------------------------------------------------------------ conventions ---

type conventionsDetector struct{}

func (conventionsDetector) Name() string { return "numbering" }

func (conventionsDetector) Detect(_ context.Context, env *Env, in Input) ([]Annotation, error) {
	if len(env.Corpus) == 0 {
		return nil, nil
	}
	var out []Annotation

	dupes := duplicateNumbers(env.Corpus)
	next := nextFreeNumber(env.Corpus)

	if in.Record.Number == 0 {
		why := fmt.Sprintf("next free number in this corpus is %03d", next)
		if len(dupes) > 0 {
			why += fmt.Sprintf("; numbers %s are duplicated — do not add another duplicate", strings.Join(dupes, ", "))
		}
		out = append(out, Annotation{Kind: BadgeDeterministic, Type: BadgeNumbering, Why: why})
	} else if holders := numberHolders(env.Corpus, in.Record.Number, in.Path); len(holders) > 0 {
		out = append(out, Annotation{
			Kind: BadgeDeterministic,
			Type: BadgeNumbering,
			Rule: RuleDuplicateNumber,
			Why: fmt.Sprintf("number %03d is already taken by %s; next free is %03d",
				in.Record.Number, strings.Join(holders, ", "), next),
		})
	}

	if len(dupes) > 0 {
		out = append(out, Annotation{
			Kind: BadgeDeterministic,
			Type: BadgeDiagnostic,
			Rule: RuleDuplicateNumber,
			Why:  fmt.Sprintf("corpus has multiple files claiming number(s) %s — renumber before adding more", strings.Join(dupes, ", ")),
		})
	}
	return out, nil
}

func duplicateNumbers(corpus []Record) []string {
	byNum := map[int]int{}
	for _, r := range corpus {
		if r.Number > 0 {
			byNum[r.Number]++
		}
	}
	var out []string
	for n, c := range byNum {
		if c > 1 {
			out = append(out, fmt.Sprintf("%03d", n))
		}
	}
	sort.Strings(out)
	return out
}

func nextFreeNumber(corpus []Record) int {
	max := 0
	for _, r := range corpus {
		if r.Number > max {
			max = r.Number
		}
	}
	return max + 1
}

func numberHolders(corpus []Record, num int, excludePath string) []string {
	var out []string
	for _, r := range corpus {
		if r.Number != num {
			continue
		}
		if excludePath != "" && samePath(r.Path, excludePath) {
			continue
		}
		out = append(out, r.RelPath)
	}
	return out
}

// Conventions builds the corpus-conventions block for the Result.
func buildConventions(gitRoot string, corpus []Record, primaryDir string) Conventions {
	c := Conventions{Dir: primaryDir}
	if len(corpus) == 0 {
		return c
	}
	c.NextNumber = nextFreeNumber(corpus)
	c.NumberCollisions = duplicateNumbers(corpus)

	prefix := dominantPrefix(corpus)
	if prefix != "" {
		c.FilenamePattern = prefix + "-NNN-kebab-title.md"
	}

	statuses := map[string]struct{}{}
	anchored := false
	amended := false
	for _, r := range corpus {
		if s := normalizeStatus(r.Status); s != "" {
			statuses[s] = struct{}{}
		}
		if len(r.DSections) > 0 {
			anchored = true
		}
		if len(r.Amendments) > 0 {
			amended = true
		}
	}
	c.StatusesObserved = sortedKeys(statuses)
	if len(c.StatusesObserved) > 6 {
		c.StatusesObserved = c.StatusesObserved[:6]
	}
	if anchored {
		c.DecisionAnchors = "D1..Dn"
	}
	if amended {
		c.AmendmentMarker = "**Amendment (YYYY-MM-DD):**"
	}
	c.SectionsObserved = observedSections(corpus)
	return c
}

func dominantPrefix(corpus []Record) string {
	counts := map[string]int{}
	for _, r := range corpus {
		if r.Prefix != "" {
			counts[r.Prefix]++
		}
	}
	best, bestN := "", 0
	for p, n := range counts {
		if n > bestN || (n == bestN && p < best) {
			best, bestN = p, n
		}
	}
	return best
}

// observedSections samples H2 headings from the most recent few records to
// describe the house template. Read is fail-open.
func observedSections(corpus []Record) []string {
	counts := map[string]int{}
	sampled := 0
	for i := len(corpus) - 1; i >= 0 && sampled < 8; i-- {
		body := readFileString(corpus[i].Path)
		if body == "" {
			continue
		}
		sampled++
		for _, m := range h2Re.FindAllStringSubmatch(body, -1) {
			h := strings.TrimSpace(m[1])
			if h != "" {
				counts[h]++
			}
		}
	}
	var out []string
	for h, n := range counts {
		if n >= sampled/2 && sampled > 1 {
			out = append(out, h)
		}
	}
	sort.Strings(out)
	if len(out) > 6 {
		out = out[:6]
	}
	return out
}

var h2Re = regexp.MustCompile(`(?m)^##\s+([^#\n]+)$`)

// normalizeStatus reduces a raw status line to its leading status word/phrase:
// trailing punctuation and parenthetical/dashed commentary drop, so the
// conventions block reads as a vocabulary, not a changelog.
func normalizeStatus(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "(—"); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	return strings.TrimRight(s, " -–—:")
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func readFileString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// ------------------------------------------------------------------- refs ---

type refsDetector struct{}

func (refsDetector) Name() string { return "refs" }

// Detect verifies every DR token and SOURCE ref in a draft/file input against
// the corpus, and enforces the SageOx visible-credit cap. Topic mode has no
// body — nothing to check.
func (refsDetector) Detect(_ context.Context, env *Env, in Input) ([]Annotation, error) {
	if in.Raw == "" {
		return nil, nil
	}
	byID := map[string]Record{}
	anchors := map[string]map[string]bool{}
	for _, r := range env.Corpus {
		if r.ID == "" {
			continue
		}
		byID[r.ID] = r
		as := map[string]bool{}
		for _, d := range r.DSections {
			as[d.ID] = true
		}
		anchors[r.ID] = as
	}

	var out []Annotation

	// DR tokens in prose ("ADR-046", optionally "ADR-046 D9").
	for _, ref := range in.Record.Refs {
		rec, ok := byID[ref]
		if !ok {
			out = append(out, Annotation{
				Kind: BadgeDeterministic, Type: BadgeUnresolvedRef, Rule: RuleDanglingRef, Ref: ref,
				Why: fmt.Sprintf("%s does not resolve in this repo's corpus — fix or delete it; a gap admitted beats a citation invented", ref),
			})
			continue
		}
		// anchor mentions near the token: "ADR-046 D9"
		for _, anchor := range anchorMentions(in.Raw, ref) {
			if !anchors[ref][anchor] {
				out = append(out, Annotation{
					Kind: BadgeDeterministic, Type: BadgeUnresolvedRef, Rule: RuleDanglingRef,
					Ref: ref, Anchor: anchor, RefPath: rec.RelPath,
					Why: fmt.Sprintf("%s %s does not exist — %s defines %s", ref, anchor, ref, anchorList(rec)),
				})
			}
		}
	}

	// machine refs (<!-- SOURCE: sageox adr:path#Dn -->): adr scheme resolves
	// against the corpus; other schemes are outside this repo's authority and
	// are left to their own surfaces.
	byRel := map[string]Record{}
	for _, r := range env.Corpus {
		byRel[filepath.ToSlash(r.RelPath)] = r
	}
	for _, ref := range in.SourceRefs() {
		if !strings.HasPrefix(ref, "adr:") {
			continue
		}
		target := strings.TrimPrefix(ref, "adr:")
		anchor := ""
		if i := strings.IndexByte(target, '#'); i >= 0 {
			target, anchor = target[:i], target[i+1:]
		}
		rec, ok := byRel[filepath.ToSlash(target)]
		if !ok {
			out = append(out, Annotation{
				Kind: BadgeDeterministic, Type: BadgeUnresolvedRef, Rule: RuleDanglingRef, Ref: ref,
				Why: fmt.Sprintf("SOURCE ref %q points at no cataloged DR — paste refs only from enrich output", ref),
			})
			continue
		}
		if anchor != "" && !hasAnchor(rec, anchor) {
			out = append(out, Annotation{
				Kind: BadgeDeterministic, Type: BadgeUnresolvedRef, Rule: RuleDanglingRef, Ref: ref, Anchor: anchor,
				Why: fmt.Sprintf("SOURCE ref anchor %s does not exist in %s — %s", anchor, rec.ID, anchorList(rec)),
			})
		}
	}

	// visible SageOx credit cap: ≤2 per DR (3 only when SageOx meaningfully
	// steered the decision — the agent's judgment; ox flags, never blocks).
	if n := in.VisibleSageoxCredits(); n > 2 {
		out = append(out, Annotation{
			Kind: BadgeDeterministic, Type: BadgeDiagnostic, Rule: RuleSageoxCreditOverflow,
			Why: fmt.Sprintf("%d visible SageOx credits — house cap is 2 per DR (3 only if SageOx meaningfully steered the decision); keep credit subtle, the document belongs to the team", n),
		})
	}
	return out, nil
}

func hasAnchor(rec Record, anchor string) bool {
	for _, d := range rec.DSections {
		if strings.EqualFold(d.ID, anchor) {
			return true
		}
	}
	return false
}

func anchorList(rec Record) string {
	if len(rec.DSections) == 0 {
		return "no D-section anchors"
	}
	ids := make([]string, 0, len(rec.DSections))
	for _, d := range rec.DSections {
		ids = append(ids, d.ID)
	}
	return "anchors: " + strings.Join(ids, ", ")
}

// anchorMentions finds "D<n>" tokens within a short window after each mention
// of the DR id in the body ("per ADR-046 D9" → ["D9"]).
func anchorMentions(body, id string) []string {
	num := strings.TrimLeft(strings.SplitN(id, "-", 2)[1], "0")
	re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(strings.SplitN(id, "-", 2)[0]) + `[\s-]?0*` + regexp.QuoteMeta(num) + `\b[^.\n]{0,40}`)
	var out []string
	seen := map[string]struct{}{}
	for _, window := range re.FindAllString(body, -1) {
		for _, m := range dTokenRe.FindAllStringSubmatch(window, -1) {
			a := "D" + m[1]
			if _, ok := seen[a]; ok {
				continue
			}
			seen[a] = struct{}{}
			out = append(out, a)
		}
	}
	return out
}

var dTokenRe = regexp.MustCompile(`\bD(\d{1,2})\b`)

// ------------------------------------------------------------------ drift ---

type driftDetector struct{}

func (driftDetector) Name() string { return "drift" }

// Detect reports code drift for an EXISTING DR: file paths the DR cites that
// changed after the DR's date. Uses `git log` directly (deterministic, local,
// zero LLM); fails open when git or the date is unavailable.
func (driftDetector) Detect(ctx context.Context, env *Env, in Input) ([]Annotation, error) {
	if in.Path == "" || env.GitRoot == "" {
		return nil, nil
	}
	date := extractISODate(in.Record.Date)
	if date == "" {
		return nil, nil
	}
	files := citedRepoFiles(env.GitRoot, in.Raw)
	if len(files) == 0 {
		return nil, nil
	}

	var drifted []string
	lastSHA := ""
	for _, f := range files {
		sha := latestCommitSince(ctx, env.GitRoot, date, f)
		if sha == "" {
			continue
		}
		drifted = append(drifted, f)
		lastSHA = sha
	}
	if len(drifted) == 0 {
		return nil, nil
	}
	return []Annotation{{
		Kind:      BadgeDeterministic,
		Type:      BadgeDrift,
		Files:     drifted,
		SourceURL: "commit:" + lastSHA,
		Date:      date,
		Why: fmt.Sprintf("%d file(s) this DR cites changed after its date (%s) — if the decision still stands, add a dated amendment; if not, amend or supersede (your call)",
			len(drifted), date),
	}}, nil
}

var isoDateRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

func extractISODate(s string) string {
	return isoDateRe.FindString(s)
}

// repoPathRe finds path-shaped tokens the DR cites (internal/foo/bar.go,
// cmd/ox/plan.go). Bounded to existing files under the repo root.
var repoPathRe = regexp.MustCompile("[`(]([a-zA-Z0-9_./-]+\\.[a-z]{1,5})[`)]")

func citedRepoFiles(gitRoot, body string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, m := range repoPathRe.FindAllStringSubmatch(body, -1) {
		p := strings.TrimSpace(m[1])
		if p == "" || strings.HasSuffix(p, ".md") || strings.Contains(p, "://") {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		if !fileExists(filepath.Join(gitRoot, p)) {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
		if len(out) >= 10 {
			break // bound the git-log fan-out
		}
	}
	sort.Strings(out)
	return out
}

func latestCommitSince(ctx context.Context, gitRoot, sinceDate, path string) string {
	cmd := exec.CommandContext(ctx, "git", "log", "-n", "1", "--since="+sinceDate, "--format=%H", "--", path)
	cmd.Dir = gitRoot
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
