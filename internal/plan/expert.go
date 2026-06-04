package plan

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/paths"
)

// expertDetector routes each plan area/file to the team expert with CITED
// evidence drawn from local data only. It computes author ownership per path
// from codedb (commits joined to diffs) and emits one BadgeExpertRoute
// annotation per dominant author, citing that author's most relevant real
// artifact (a commit hash, or a PR url when one is on record).
//
// CITED-ONLY GUARANTEE: this detector never synthesizes an opinion and never
// fabricates a link. SourceURL is only set from a value that came out of the
// database (pull_requests.url or a real commit hash). If neither exists, the
// expert is still routed by name but SourceURL is omitted.
//
// FAIL-OPEN: a missing index, an unreadable DB, an empty plan, or any query
// error yields (nil, nil). Enrich recovers from panics; we also guard the DB
// open path so a corrupt index can never abort enrichment.
type expertDetector struct{}

func init() {
	RegisterDetector(&expertDetector{})
}

// Name identifies the detector in logs and the registry.
func (e *expertDetector) Name() string { return "expert-routing" }

// minCommitsToRoute is the floor for claiming someone "owns" a path. A single
// drive-by commit is not ownership; we want a dominant, recurring author.
const minCommitsToRoute = 2

// authorStat is the per-path, per-author rollup pulled from codedb. It is the
// pure input to ranking — no DB handle, so the ranking logic is unit-testable.
type authorStat struct {
	Author      string
	Path        string
	Commits     int
	LastTouched time.Time
	// CiteHash is a real commit hash for this author+path (most recent),
	// usable as a citation ref. Empty when unknown.
	CiteHash string
	// CitePRURL is a real PR url touching this path (most recent merged/open),
	// preferred over a bare commit ref when present. Empty when unknown.
	CitePRURL string
}

// Detect resolves the codedb index, gathers author ownership for each cited
// plan file, and emits expert-routing annotations. Fail-open throughout.
func (e *expertDetector) Detect(ctx context.Context, in Input, gitRoot string) ([]Annotation, error) {
	files := expertPlanFiles(in)
	if len(files) == 0 {
		return nil, nil
	}

	dataDir := resolveExpertCodeDBDir(gitRoot)
	if dataDir == "" {
		return nil, nil
	}
	if _, err := os.Stat(dataDir); err != nil {
		// no index on disk — fail open, nothing to route from
		return nil, nil
	}

	db, err := codedb.OpenSQLOnly(dataDir)
	if err != nil {
		slog.Debug("plan expert: open codedb failed", "error", err)
		return nil, nil
	}
	defer func() { _ = db.Close() }()

	stats := gatherAuthorStats(ctx, db.Store(), files)
	if len(stats) == 0 {
		return nil, nil
	}

	anns := rankExperts(stats)
	if len(anns) == 0 {
		return nil, nil
	}
	slog.Debug("plan expert: routed areas", "files", len(files), "experts", len(anns))
	return anns, nil
}

// expertPlanFiles returns the deduped, sorted set of file references cited
// anywhere in the plan. Sections already carry extracted Files; we union them.
// Named distinctly from the package-shared planFiles helper so this file builds
// independently of peers' in-flight files.
func expertPlanFiles(in Input) []string {
	seen := make(map[string]struct{})
	var files []string
	for _, s := range in.Sections {
		for _, f := range s.Files {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			// strip any :line suffix so path ownership matches the file
			if idx := strings.LastIndexByte(f, ':'); idx > 0 {
				if _, err := fmt.Sscanf(f[idx+1:], "%d", new(int)); err == nil {
					f = f[:idx]
				}
			}
			if _, ok := seen[f]; ok {
				continue
			}
			seen[f] = struct{}{}
			files = append(files, f)
		}
	}
	sort.Strings(files)
	return files
}

// gatherAuthorStats queries codedb for per-path author commit counts, last-touch
// time, and a citable artifact (commit hash, PR url). Each query is best-effort:
// a failure on one path skips that path, never the whole detector.
func gatherAuthorStats(ctx context.Context, s *store.Store, files []string) []authorStat {
	var out []authorStat
	for _, path := range files {
		if ctx.Err() != nil {
			break
		}
		out = append(out, authorStatsForPath(s, path)...)
	}
	return out
}

// authorStatsForPath returns one authorStat per author who has touched path,
// each carrying a real, citable commit hash and (when available) a PR url.
func authorStatsForPath(s *store.Store, path string) []authorStat {
	rows, err := s.Query(`
		SELECT c.author, COUNT(*) AS commits, MAX(c.timestamp) AS last_ts
		FROM diffs d
		JOIN commits c ON c.id = d.commit_id
		WHERE d.path = ? AND c.author IS NOT NULL AND c.author != ''
		GROUP BY c.author
		ORDER BY commits DESC, last_ts DESC`, path)
	if err != nil {
		slog.Debug("plan expert: author query failed", "path", path, "error", err)
		return nil
	}
	defer func() { _ = rows.Close() }()

	var stats []authorStat
	for rows.Next() {
		var st authorStat
		var lastTS int64
		if err := rows.Scan(&st.Author, &st.Commits, &lastTS); err != nil {
			continue
		}
		st.Path = path
		st.LastTouched = time.Unix(lastTS, 0).UTC()
		st.CiteHash = mostRecentCommitHash(s, path, st.Author)
		st.CitePRURL = mostRecentPRURL(s, path, st.Author)
		stats = append(stats, st)
	}
	return stats
}

// mostRecentCommitHash returns the newest real commit hash by author on path,
// for use as a citation ref. Empty string when none — never fabricated.
func mostRecentCommitHash(s *store.Store, path, author string) string {
	row := s.QueryRow(`
		SELECT c.hash
		FROM diffs d
		JOIN commits c ON c.id = d.commit_id
		WHERE d.path = ? AND c.author = ?
		ORDER BY c.timestamp DESC
		LIMIT 1`, path, author)
	var hash string
	if err := row.Scan(&hash); err != nil {
		return ""
	}
	return hash
}

// mostRecentPRURL returns the url of the newest PR whose commits touched path
// and were authored by author. Empty when none — a PR url, when present, is a
// stronger citation than a bare commit hash. Never fabricated.
func mostRecentPRURL(s *store.Store, path, author string) string {
	row := s.QueryRow(`
		SELECT p.url
		FROM pull_requests p
		JOIN pr_commits pc ON pc.pr_id = p.id
		JOIN commits c ON c.hash = pc.sha
		JOIN diffs d ON d.commit_id = c.id
		WHERE d.path = ? AND c.author = ? AND p.url IS NOT NULL AND p.url != ''
		ORDER BY COALESCE(p.merged_at, p.updated_at, p.created_at) DESC
		LIMIT 1`, path, author)
	var url string
	if err := row.Scan(&url); err != nil {
		return ""
	}
	return url
}

// rankExperts is the pure routing core: given per-file author stats, it picks
// the dominant author for each file, groups files under their dominant author,
// and produces one annotation per expert citing the strongest real artifact.
//
// Deterministic: ties broken by recency then author name; outputs sorted by
// expert name. No DB handle, no network — unit-testable in isolation.
func rankExperts(stats []authorStat) []Annotation {
	// dominant author per path
	type winner struct {
		author      string
		commits     int
		lastTouched time.Time
		citeHash    string
		citePRURL   string
	}
	byPath := make(map[string]winner)
	for _, st := range stats {
		if st.Author == "" || st.Path == "" {
			continue
		}
		w, ok := byPath[st.Path]
		if !ok || betterOwner(st, w.commits, w.lastTouched, w.author) {
			byPath[st.Path] = winner{
				author:      st.Author,
				commits:     st.Commits,
				lastTouched: st.LastTouched,
				citeHash:    st.CiteHash,
				citePRURL:   st.CitePRURL,
			}
		}
	}

	// group paths under their dominant author
	type areaOwnership struct {
		files       []string
		commits     int
		lastTouched time.Time
		citeHash    string
		citePRURL   string
	}
	byExpert := make(map[string]*areaOwnership)
	for path, w := range byPath {
		if w.commits < minCommitsToRoute {
			continue // a single drive-by commit is not ownership
		}
		ao, ok := byExpert[w.author]
		if !ok {
			ao = &areaOwnership{}
			byExpert[w.author] = ao
		}
		ao.files = append(ao.files, path)
		ao.commits += w.commits
		if w.lastTouched.After(ao.lastTouched) {
			ao.lastTouched = w.lastTouched
			// cite the artifact from the author's most recent owned path
			ao.citeHash = w.citeHash
			ao.citePRURL = w.citePRURL
		}
	}

	experts := make([]string, 0, len(byExpert))
	for name := range byExpert {
		experts = append(experts, name)
	}
	sort.Strings(experts)

	var anns []Annotation
	for _, name := range experts {
		ao := byExpert[name]
		sort.Strings(ao.files)
		anns = append(anns, Annotation{
			Kind:      BadgeDeterministic,
			Type:      BadgeExpertRoute,
			Expert:    name,
			Files:     ao.files,
			Why:       whyOwns(name, ao.files, ao.commits, ao.lastTouched),
			SourceURL: citation(ao.citePRURL, ao.citeHash),
		})
	}
	return anns
}

// betterOwner reports whether candidate stat beats the current per-path winner.
// Primary key: commit count. Tie-break: recency, then author name (stable).
func betterOwner(cand authorStat, curCommits int, curLast time.Time, curAuthor string) bool {
	if cand.Commits != curCommits {
		return cand.Commits > curCommits
	}
	if !cand.LastTouched.Equal(curLast) {
		return cand.LastTouched.After(curLast)
	}
	return cand.Author < curAuthor
}

// whyOwns builds the human-readable routing rationale. It names the area
// (a shared directory prefix when the files share one, else "these files").
func whyOwns(name string, files []string, commits int, lastTouched time.Time) string {
	area := commonArea(files)
	when := "unknown"
	if !lastTouched.IsZero() {
		when = lastTouched.Format("Jan 2006")
	}
	return fmt.Sprintf("%s owns %s (%d commits, last touched %s)", name, area, commits, when)
}

// citation returns the cited artifact ref. A PR url wins over a commit hash.
// Returns "" when neither is real — the caller then omits SourceURL rather than
// inventing one.
func citation(prURL, commitHash string) string {
	if prURL != "" {
		return prURL
	}
	if commitHash != "" {
		return "commit:" + commitHash
	}
	return ""
}

// commonArea returns the longest shared directory prefix across files, or
// "these files" when they don't share a directory. Used only for the Why string.
func commonArea(files []string) string {
	if len(files) == 0 {
		return "these files"
	}
	if len(files) == 1 {
		return files[0]
	}
	parts := strings.Split(dirOf(files[0]), "/")
	for _, f := range files[1:] {
		fp := strings.Split(dirOf(f), "/")
		n := len(parts)
		if len(fp) < n {
			n = len(fp)
		}
		i := 0
		for i < n && parts[i] == fp[i] {
			i++
		}
		parts = parts[:i]
		if len(parts) == 0 {
			break
		}
	}
	if len(parts) == 0 || (len(parts) == 1 && parts[0] == "") {
		return "these files"
	}
	return strings.Join(parts, "/") + "/"
}

// dirOf returns the directory portion of a repo-relative path ("" for a
// top-level file).
func dirOf(path string) string {
	if idx := strings.LastIndexByte(path, '/'); idx >= 0 {
		return path[:idx]
	}
	return ""
}

// resolveExpertCodeDBDir resolves the codedb index directory the same way the
// `ox code` commands do: prefer the shared ledger cache (repo-scoped), fall back
// to the project-local cache. Returns "" when nothing can be resolved so the
// caller fails open. No hardcoded paths.
func resolveExpertCodeDBDir(gitRoot string) string {
	if gitRoot == "" {
		return ""
	}
	if ctx, err := config.LoadProjectContext(gitRoot); err == nil {
		if dir := paths.CodeDBSharedDir(ctx.RepoID(), ctx.Endpoint()); dir != "" {
			return dir
		}
	}
	return paths.CodeDBDataDir(gitRoot)
}
