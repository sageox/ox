package search

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search/query"
	blevesearch "github.com/blevesearch/bleve/v2/search"

	"github.com/sageox/ox/internal/codedb/store"
)

// Result represents a single search result.
type Result struct {
	Repo       string  `json:"repo,omitempty"`
	FilePath   string  `json:"file_path,omitempty"`
	Content    string  `json:"content,omitempty"`
	Score      float64 `json:"score,omitempty"`
	Line       int     `json:"line,omitempty"`
	Language   string  `json:"language,omitempty"`
	CommitHash string  `json:"commit_hash,omitempty"`
	Author     string  `json:"author,omitempty"`
	Message    string  `json:"message,omitempty"`
	SymbolName string  `json:"symbol_name,omitempty"`
	SymbolKind string  `json:"symbol_kind,omitempty"`
}

// Execute runs a parsed query against the store using the planner to determine
// the execution strategy (SQL only, Bleve only, or intersect).
func Execute(ctx context.Context, s *store.Store, query *ParsedQuery) ([]Result, error) {
	plan, err := Plan(query)
	if err != nil {
		return nil, err
	}

	switch plan.Strategy {
	case JoinSQLOnly:
		return executePlanSQL(ctx, s, plan)
	case JoinBleveOnly:
		return executePlanBleve(ctx, s, plan, nil)
	case JoinIntersect:
		return executePlanBleve(ctx, s, plan, &query.Filters)
	default:
		return executePlanSQL(ctx, s, plan)
	}
}

// executePlanSQL executes a plan that only needs SQL (commits, symbols, calls).
func executePlanSQL(ctx context.Context, s *store.Store, plan *ExecutionPlan) ([]Result, error) {
	args := make([]interface{}, len(plan.SQLParams))
	for i, p := range plan.SQLParams {
		args[i] = p
	}

	rows, err := s.QueryContext(ctx, plan.SQL, args...)
	if err != nil {
		return nil, fmt.Errorf("execute query: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var results []Result
	for rows.Next() {
		values := make([]sql.NullString, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			slog.Warn("scan error, skipping row", "err", err)
			continue
		}

		r := Result{}
		for i, col := range cols {
			val := values[i].String
			switch col {
			case "path":
				r.FilePath = val
			case "name":
				r.SymbolName = val
			case "kind":
				r.SymbolKind = val
			case "line":
				fmt.Sscanf(val, "%d", &r.Line)
			case "hash":
				r.CommitHash = val
			case "author":
				r.Author = val
			case "message":
				r.Message = val
			case "score":
				fmt.Sscanf(val, "%f", &r.Score)
			case "snippet":
				r.Content = val
			}
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}

	return results, nil
}

// executePlanBleve runs a Bleve full-text search and enriches results from SQL.
// When filters is non-nil (intersect strategy), metadata filters are applied.
// When filters is nil (bleve-only strategy), no additional filtering is done.
func executePlanBleve(ctx context.Context, s *store.Store, plan *ExecutionPlan, filters *Filters) ([]Result, error) {
	idx := s.CodeIndex
	if plan.BleveIndex == "diff" {
		idx = s.DiffIndex
	}

	var bleveQuery query.Query
	if plan.IsRegex {
		rq := bleve.NewRegexpQuery(plan.BleveQuery)
		rq.SetField("content")
		bleveQuery = rq
	} else {
		bleveQuery = bleve.NewQueryStringQuery(plan.BleveQuery)
	}
	searchReq := bleve.NewSearchRequestOptions(bleveQuery, plan.Limit*5, 0, false)
	searchReq.Fields = []string{"content"}
	searchReq.Highlight = bleve.NewHighlightWithStyle("ansi")
	searchResult, err := idx.Search(searchReq)
	if err != nil {
		return nil, fmt.Errorf("bleve search: %w", err)
	}

	if searchResult.Total == 0 {
		return nil, nil
	}

	var results []Result
	for _, hit := range searchResult.Hits {
		if err := ctx.Err(); err != nil {
			return results, err
		}

		fragment := extractFragment(hit)

		var hitResults []Result
		if plan.BleveIndex == "diff" {
			hitResults, err = enrichDiffHit(ctx, s, hit, fragment, filters)
		} else {
			hitResults, err = enrichCodeHit(ctx, s, hit, fragment, filters)
		}
		if err != nil {
			continue
		}
		results = append(results, hitResults...)

		if len(results) >= plan.Limit {
			results = results[:plan.Limit]
			break
		}
	}

	return results, nil
}

// extractFragment pulls the first highlighted content fragment from a Bleve hit.
func extractFragment(hit *blevesearch.DocumentMatch) string {
	if frags, ok := hit.Fragments["content"]; ok && len(frags) > 0 {
		return frags[0]
	}
	return ""
}

// enrichDiffHit looks up diff metadata from SQL for a Bleve diff hit.
func enrichDiffHit(ctx context.Context, s *store.Store, hit *blevesearch.DocumentMatch, fragment string, filters *Filters) ([]Result, error) {
	diffID := strings.TrimPrefix(hit.ID, "diff_")

	sqlQ := `
		SELECT substr(c.hash, 1, 10), c.author, substr(c.message, 1, 80), d.path
		FROM diffs d JOIN commits c ON c.id = d.commit_id
		WHERE d.id = ?`
	args := []interface{}{diffID}

	if filters != nil {
		addDiffFilters(&sqlQ, &args, filters)
	}

	rows, err := s.QueryContext(ctx, sqlQ, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Result
	for rows.Next() {
		var hash, author, message, path string
		if err := rows.Scan(&hash, &author, &message, &path); err != nil {
			slog.Warn("diff hit scan error, skipping row", "err", err)
			continue
		}
		results = append(results, Result{
			CommitHash: hash, Author: author, Message: message,
			FilePath: path, Score: hit.Score, Content: fragment,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate diff rows: %w", err)
	}
	return results, nil
}

// enrichCodeHit looks up file metadata from SQL for a Bleve code hit.
func enrichCodeHit(ctx context.Context, s *store.Store, hit *blevesearch.DocumentMatch, fragment string, filters *Filters) ([]Result, error) {
	blobID := strings.TrimPrefix(hit.ID, "blob_")

	var revFilter string
	if filters != nil && filters.Rev != "" {
		revFilter = resolveRevRef(filters.Rev)
	}

	sqlQ := `
		SELECT fr.path, b.language, rp.name
		FROM blobs b
		JOIN file_revs fr ON fr.blob_id = b.id
		JOIN refs r ON r.commit_id = fr.commit_id
		JOIN repos rp ON rp.id = r.repo_id
		WHERE b.id = ?`
	args := []interface{}{blobID}

	if revFilter != "" {
		sqlQ += " AND r.name = ?"
		args = append(args, revFilter)
	}

	if filters != nil {
		addCodeFilters(&sqlQ, &args, filters)
	}

	rows, err := s.QueryContext(ctx, sqlQ, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Result
	for rows.Next() {
		var path string
		var lang, repo sql.NullString
		if err := rows.Scan(&path, &lang, &repo); err != nil {
			slog.Warn("code hit scan error, skipping row", "err", err)
			continue
		}
		results = append(results, Result{
			FilePath: path, Score: hit.Score, Content: fragment,
			Language: lang.String, Repo: repo.String,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate code rows: %w", err)
	}
	return results, nil
}

// addDiffFilters appends SQL WHERE clauses for diff metadata filtering.
func addDiffFilters(sqlQ *string, args *[]interface{}, f *Filters) {
	if f.Repo != "" {
		*sqlQ += " AND c.repo_id IN (SELECT id FROM repos WHERE " + likeOrGlob("name", f.Repo, f.Case) + ")"
		*args = append(*args, likeOrGlobParam(f.Repo))
	}
	if f.NegRepo != "" {
		*sqlQ += " AND c.repo_id NOT IN (SELECT id FROM repos WHERE " + likeOrGlob("name", f.NegRepo, f.Case) + ")"
		*args = append(*args, likeOrGlobParam(f.NegRepo))
	}
	if f.File != "" {
		*sqlQ += " AND " + likeOrGlob("d.path", f.File, f.Case)
		*args = append(*args, likeOrGlobParam(f.File))
	}
	if f.NegFile != "" {
		*sqlQ += " AND NOT (" + likeOrGlob("d.path", f.NegFile, f.Case) + ")"
		*args = append(*args, likeOrGlobParam(f.NegFile))
	}
	if f.Author != "" {
		*sqlQ += " AND c.author LIKE ?"
		*args = append(*args, "%"+f.Author+"%")
	}
	if f.NegAuthor != "" {
		*sqlQ += " AND c.author NOT LIKE ?"
		*args = append(*args, "%"+f.NegAuthor+"%")
	}
	if f.Before != "" {
		*sqlQ += " AND c.timestamp < CAST(strftime('%s', ?) AS INTEGER)"
		*args = append(*args, f.Before)
	}
	if f.After != "" {
		*sqlQ += " AND c.timestamp > CAST(strftime('%s', ?) AS INTEGER)"
		*args = append(*args, f.After)
	}
}

// addCodeFilters appends SQL WHERE clauses for code metadata filtering.
func addCodeFilters(sqlQ *string, args *[]interface{}, f *Filters) {
	if f.Repo != "" {
		*sqlQ += " AND " + likeOrGlob("rp.name", f.Repo, f.Case)
		*args = append(*args, likeOrGlobParam(f.Repo))
	}
	if f.NegRepo != "" {
		*sqlQ += " AND NOT (" + likeOrGlob("rp.name", f.NegRepo, f.Case) + ")"
		*args = append(*args, likeOrGlobParam(f.NegRepo))
	}
	if f.File != "" {
		*sqlQ += " AND " + likeOrGlob("fr.path", f.File, f.Case)
		*args = append(*args, likeOrGlobParam(f.File))
	}
	if f.NegFile != "" {
		*sqlQ += " AND NOT (" + likeOrGlob("fr.path", f.NegFile, f.Case) + ")"
		*args = append(*args, likeOrGlobParam(f.NegFile))
	}
	if f.Lang != "" {
		*sqlQ += " AND b.language = ?"
		*args = append(*args, f.Lang)
	}
	if f.NegLang != "" {
		*sqlQ += " AND b.language != ?"
		*args = append(*args, f.NegLang)
	}
}

// likeOrGlob returns a SQL clause using GLOB for wildcard patterns, LIKE otherwise.
func likeOrGlob(column, pattern string, caseSensitive bool) string {
	if strings.ContainsAny(pattern, "*?") {
		if caseSensitive {
			return column + " GLOB ?"
		}
		return "lower(" + column + ") GLOB ?"
	}
	return column + " LIKE ?"
}

// likeOrGlobParam returns the appropriate parameter value for likeOrGlob.
func likeOrGlobParam(pattern string) string {
	if strings.ContainsAny(pattern, "*?") {
		return pattern
	}
	return "%" + pattern + "%"
}
