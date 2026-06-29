package plan

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/paths"
)

func init() {
	RegisterDetector(&collisionDetector{})
}

// murmurWindowHours bounds how far back a murmur is treated as an active-work
// signal. Capped at ledger.MaxMurmurWindowHours (24h) inside ReadMurmursInWindow,
// so this asks for the widest meaningful window. A teammate murmuring "editing
// internal/auth/" is the earliest collision signal — it predates any commit or PR.
const murmurWindowHours = ledger.MaxMurmurWindowHours

// collisionDetector reports active-work contention on a plan's referenced files
// from three local, zero-token sources: open PRs touching the file, multi-
// workspace contention, and recent teammate murmurs. It is fail-open: any
// missing/unreadable data path returns no annotations rather than an error.
type collisionDetector struct{}

// Name identifies the detector in logs and registry diagnostics.
func (d *collisionDetector) Name() string { return "collision" }

// Detect unions the plan's section files, then asks codedb (open PRs +
// contention) and the ledger murmur cache which of those files a teammate is
// already active in. Returns one annotation per (file, signal). FAIL-OPEN
// everywhere: no codedb, no ledger, no murmurs -> (nil, nil).
func (d *collisionDetector) Detect(ctx context.Context, in Input, gitRoot string) ([]Annotation, error) {
	files := planFiles(in)
	if len(files) == 0 {
		return nil, nil
	}

	var annotations []Annotation
	annotations = append(annotations, d.detectCodeDB(ctx, gitRoot, files)...)
	annotations = append(annotations, d.detectMurmurs(gitRoot, in, files)...)

	if len(annotations) == 0 {
		return nil, nil
	}
	return annotations, nil
}

// detectCodeDB opens the shared codedb in SQL-only mode (mirroring
// `ox code insights`) and maps plan files to open PRs and contention. Returns
// nil on any failure so enrichment is never blocked by a missing/locked index.
func (d *collisionDetector) detectCodeDB(ctx context.Context, gitRoot string, files []string) []Annotation {
	dataDir := resolveCodeDBDir(gitRoot)
	if dataDir == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dataDir, store.MetadataDBFile)); err != nil {
		// no built index — fail open
		return nil
	}

	db, err := codedb.OpenSQLOnly(dataDir)
	if err != nil {
		slog.Debug("collision codedb open failed", "dir", dataDir, "error", err)
		return nil
	}
	defer func() { _ = db.Close() }()

	s := db.Store()
	want := make(map[string]struct{}, len(files))
	for _, f := range files {
		want[f] = struct{}{}
	}

	var annotations []Annotation
	annotations = append(annotations, prCollisions(s, want)...)
	annotations = append(annotations, contentionCollisions(s, want)...)
	return annotations
}

// prCollisions finds open PRs whose changed files intersect the plan's files.
// A PR's files are the union of its commits' diffs (via pr_commits.sha and the
// merge_commit), joined commits.hash -> diffs.path. Files reviewed in
// pr_comments.path are also treated as touched, since a review comment on a path
// is a strong signal the PR is editing it.
func prCollisions(s *store.Store, want map[string]struct{}) []Annotation {
	rows, err := s.Query(`
		SELECT DISTINCT pr.number, COALESCE(pr.author, ''), COALESCE(pr.url, ''), d.path
		FROM pull_requests pr
		JOIN pr_commits pc ON pc.pr_id = pr.id
		JOIN commits c ON c.hash = pc.sha
		JOIN diffs d ON d.commit_id = c.id
		WHERE pr.state = 'open'
		UNION
		SELECT DISTINCT pr.number, COALESCE(pr.author, ''), COALESCE(pr.url, ''), d.path
		FROM pull_requests pr
		JOIN commits c ON c.hash = pr.merge_commit
		JOIN diffs d ON d.commit_id = c.id
		WHERE pr.state = 'open'
		UNION
		SELECT DISTINCT pr.number, COALESCE(pr.author, ''), COALESCE(pr.url, ''), prc.path
		FROM pull_requests pr
		JOIN pr_comments prc ON prc.pr_id = pr.id
		WHERE pr.state = 'open' AND prc.path IS NOT NULL AND prc.path != ''`)
	if err != nil {
		slog.Debug("collision PR query failed", "error", err)
		return nil
	}
	defer func() { _ = rows.Close() }()

	// group by file: a file may be touched by multiple open PRs.
	type prRef struct {
		number int
		author string
		url    string
	}
	byFile := make(map[string][]prRef)
	for rows.Next() {
		var (
			number      int
			author, url string
			path        string
		)
		if err := rows.Scan(&number, &author, &url, &path); err != nil {
			continue
		}
		if _, ok := want[path]; !ok {
			continue
		}
		byFile[path] = append(byFile[path], prRef{number: number, author: author, url: url})
	}

	var annotations []Annotation
	for _, file := range sortedKeys(byFile) {
		refs := byFile[file]
		// deterministic order: ascending PR number
		sort.Slice(refs, func(i, j int) bool { return refs[i].number < refs[j].number })
		// emit one annotation per (file, PR) so each carries its own URL/author.
		for _, r := range refs {
			who := r.author
			if who == "" {
				who = "a teammate"
			}
			annotations = append(annotations, Annotation{
				Kind:      BadgeDeterministic,
				Type:      BadgeCollision,
				Why:       fmt.Sprintf("%s is in open PR #%d (%s)", file, r.number, who),
				SourceURL: r.url,
				Expert:    r.author,
				Files:     []string{file},
			})
		}
	}
	return annotations
}

// contentionCollisions reports plan files touched by more than one workspace in
// the recent commit window — a sign two efforts are converging on the same file.
func contentionCollisions(s *store.Store, want map[string]struct{}) []Annotation {
	rows, err := s.Query(`
		SELECT d.path, COUNT(DISTINCT r.name) AS workspace_count
		FROM diffs d
		JOIN commits c ON c.id = d.commit_id
		JOIN repos r ON r.id = c.repo_id
		WHERE c.timestamp > unixepoch('now', '-14 days')
		GROUP BY d.path HAVING workspace_count > 1`)
	if err != nil {
		slog.Debug("collision contention query failed", "error", err)
		return nil
	}
	defer func() { _ = rows.Close() }()

	type hit struct {
		path  string
		count int
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.path, &h.count); err != nil {
			continue
		}
		if _, ok := want[h.path]; !ok {
			continue
		}
		hits = append(hits, h)
	}

	sort.Slice(hits, func(i, j int) bool { return hits[i].path < hits[j].path })

	var annotations []Annotation
	for _, h := range hits {
		annotations = append(annotations, Annotation{
			Kind:     BadgeDeterministic,
			Type:     BadgeCollision,
			Why:      fmt.Sprintf("%s is contended — touched by %d workspaces recently", h.path, h.count),
			HumanWhy: fmt.Sprintf("%s — contended with other active work; coordinate before editing", h.path),
			Files:    []string{h.path},
		})
	}
	return annotations
}

// detectMurmurs reads recent murmurs from the ledger cache and matches their
// metadata["files"] (and topic against section headings) to the plan's files.
// Murmurs are the EARLIEST collision signal — they exist before any commit/PR.
func (d *collisionDetector) detectMurmurs(gitRoot string, in Input, files []string) []Annotation {
	ledgerPath := resolveLedgerPath(gitRoot)
	if ledgerPath == "" {
		return nil
	}

	murmurs, err := ledger.ReadMurmursInWindow(ledgerPath, murmurWindowHours)
	if err != nil || len(murmurs) == 0 {
		return nil
	}

	return matchMurmurs(murmurs, files, sectionHeadings(in), time.Now().UTC())
}

// matchMurmurs is the pure, testable core of murmur collision detection: given a
// set of murmurs, the plan's files, and section headings, it returns one
// annotation per (file, murmur) where the murmur references that file in its
// metadata["files"] list. Topic-vs-heading matches are reported when a murmur's
// topic overlaps a section heading even without an explicit file list.
//
// now is injected so recency phrasing is deterministic under test.
func matchMurmurs(murmurs []ledger.MurmurFile, files []string, headings []string, now time.Time) []Annotation {
	want := make(map[string]struct{}, len(files))
	for _, f := range files {
		want[f] = struct{}{}
	}

	var annotations []Annotation
	seen := make(map[string]struct{})

	for _, m := range murmurs {
		who := murmurAuthor(m)
		ago := humanizeAgo(now.Sub(m.Timestamp.UTC()))

		// explicit file references in murmur metadata.
		for _, mf := range murmurFiles(m) {
			matched, ok := matchPlanFile(mf, want)
			if !ok {
				continue
			}
			key := matched + "\x00" + m.ID
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			annotations = append(annotations, Annotation{
				Kind:     BadgeDeterministic,
				Type:     BadgeCollision,
				Why:      fmt.Sprintf("%s murmured editing %s %s", who, matched, ago),
				HumanWhy: fmt.Sprintf("%s is editing %s", who, matched),
				Expert:   who,
				Files:    []string{matched},
			})
		}
	}

	// topic-vs-heading overlap is a softer signal; only emit when no explicit
	// file match already fired for the same murmur, to avoid double-reporting.
	matchedMurmur := make(map[string]struct{})
	for k := range seen {
		if i := strings.IndexByte(k, 0); i >= 0 {
			matchedMurmur[k[i+1:]] = struct{}{}
		}
	}
	for _, m := range murmurs {
		if _, done := matchedMurmur[m.ID]; done {
			continue
		}
		if m.Topic == "" || len(headings) == 0 {
			continue
		}
		if !topicMatchesHeading(m.Topic, headings) {
			continue
		}
		who := murmurAuthor(m)
		ago := humanizeAgo(now.Sub(m.Timestamp.UTC()))
		annotations = append(annotations, Annotation{
			Kind:     BadgeDeterministic,
			Type:     BadgeCollision,
			Why:      fmt.Sprintf("%s murmured on topic %q %s", who, m.Topic, ago),
			HumanWhy: fmt.Sprintf("%s is discussing %q", who, m.Topic),
			Expert:   who,
		})
	}

	sort.SliceStable(annotations, func(i, j int) bool { return annotations[i].Why < annotations[j].Why })
	return annotations
}

// matchPlanFile reports whether a murmur-referenced path corresponds to one of
// the plan's files. Matching tolerates directory references ("internal/auth/")
// and exact-or-suffix file matches, returning the canonical plan-file path.
func matchPlanFile(murmurRef string, want map[string]struct{}) (string, bool) {
	murmurRef = strings.TrimSpace(murmurRef)
	if murmurRef == "" {
		return "", false
	}
	clean := normalizePath(murmurRef)

	// exact match against a plan file.
	if _, ok := want[clean]; ok {
		return clean, true
	}

	// directory-style murmur ("internal/auth/" or "internal/auth"): match any
	// plan file under that directory prefix.
	dirPrefix := clean
	if !strings.HasSuffix(dirPrefix, "/") {
		dirPrefix += "/"
	}
	for planFile := range want {
		if strings.HasPrefix(planFile, dirPrefix) {
			return planFile, true
		}
		// suffix match: the murmur cites a fuller path (e.g. an absolute or
		// repo-prefixed path) ending in the plan's relative file ref.
		if strings.HasSuffix(clean, "/"+normalizePath(planFile)) {
			return planFile, true
		}
	}
	return "", false
}

// topicMatchesHeading reports whether a murmur topic shares a meaningful token
// with any section heading. Tokens shorter than 4 chars are ignored to avoid
// matching on stopwords like "the" or "api".
func topicMatchesHeading(topic string, headings []string) bool {
	topicTokens := tokenize(topic)
	if len(topicTokens) == 0 {
		return false
	}
	for _, h := range headings {
		hTokens := make(map[string]struct{})
		for _, t := range tokenize(h) {
			hTokens[t] = struct{}{}
		}
		for _, t := range topicTokens {
			if len(t) < 4 {
				continue
			}
			if _, ok := hTokens[t]; ok {
				return true
			}
		}
	}
	return false
}

// tokenize lowercases and splits text into alphanumeric word tokens.
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

// murmurFiles extracts the file list a murmur references from its metadata. The
// convention is a comma- or newline-separated metadata["files"] value.
func murmurFiles(m ledger.MurmurFile) []string {
	raw, ok := m.Metadata["files"]
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';' || r == ' '
	})
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// murmurAuthor returns a human-friendly attribution for a murmur, preferring the
// principal (the coworker the agent works for) over the agent instance.
func murmurAuthor(m ledger.MurmurFile) string {
	if m.PrincipalID != "" {
		return m.PrincipalID
	}
	if m.AgentID != "" {
		return m.AgentID
	}
	return "a teammate"
}

// humanizeAgo renders a duration as a short recency phrase ("3h ago", "2d ago").
func humanizeAgo(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// planFiles returns the deduped, normalized union of every section's file refs.
// The :line suffix produced by the input parser is stripped for matching.
func planFiles(in Input) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, sec := range in.Sections {
		for _, f := range sec.Files {
			clean := normalizePath(f)
			if clean == "" {
				continue
			}
			if _, ok := seen[clean]; ok {
				continue
			}
			seen[clean] = struct{}{}
			out = append(out, clean)
		}
	}
	sort.Strings(out)
	return out
}

// sectionHeadings returns the non-empty H2 headings of the plan.
func sectionHeadings(in Input) []string {
	var out []string
	for _, sec := range in.Sections {
		if h := strings.TrimSpace(sec.Heading); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// normalizePath strips a trailing :line suffix and surrounding whitespace so a
// plan ref like "internal/auth/token.go:42" matches the codedb/murmur path.
func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	if i := strings.LastIndexByte(p, ':'); i > 0 {
		// only treat as :line if the suffix is all digits.
		if allDigits(p[i+1:]) {
			p = p[:i]
		}
	}
	return strings.TrimPrefix(p, "./")
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sortedKeys returns the map keys in ascending order for deterministic output.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// resolveCodeDBDir mirrors cmd/ox's resolution: prefer the shared codedb keyed
// by repo_id+endpoint, falling back to the per-project cache dir. cmd/ox is
// package main and can't be imported, so the canonical paths helpers are reused
// directly here.
func resolveCodeDBDir(gitRoot string) string {
	if ctx, err := config.LoadProjectContext(gitRoot); err == nil {
		if dir := paths.CodeDBSharedDir(ctx.RepoID(), ctx.Endpoint()); dir != "" {
			return dir
		}
	}
	return paths.CodeDBDataDir(gitRoot)
}

// resolveLedgerPath returns the ledger checkout root for the repo, which hosts
// the murmur cache under data/murmurs/. Uses the canonical ProjectContext helper
// so path construction is never duplicated. Returns "" when uninitialized.
func resolveLedgerPath(gitRoot string) string {
	ctx, err := config.LoadProjectContext(gitRoot)
	if err != nil {
		return ""
	}
	p := ctx.DefaultLedgerPath()
	if p == "" {
		return ""
	}
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}
