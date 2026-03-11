package search

import (
	"fmt"
	"strings"
)

// paramCollector tracks SQL parameters and returns ?N placeholders.
type paramCollector struct {
	params        []string
	caseSensitive bool
}

func newParamCollector(caseSensitive bool) *paramCollector {
	return &paramCollector{caseSensitive: caseSensitive}
}

func (p *paramCollector) add(value string) string {
	p.params = append(p.params, value)
	return fmt.Sprintf("?%d", len(p.params))
}

// patternMatchClause generates a SQL clause matching column against pattern.
func patternMatchClause(column, pattern string, p *paramCollector) string {
	hasWild := strings.ContainsAny(pattern, "*?")
	if hasWild {
		if p.caseSensitive {
			ph := p.add(pattern)
			return fmt.Sprintf("%s GLOB %s", column, ph)
		}
		ph := p.add(strings.ToLower(pattern))
		return fmt.Sprintf("lower(%s) GLOB %s", column, ph)
	}
	if p.caseSensitive {
		escaped := escapeGlobChars(pattern)
		ph := p.add("*" + escaped + "*")
		return fmt.Sprintf("%s GLOB %s", column, ph)
	}
	ph := p.add("%" + pattern + "%")
	return fmt.Sprintf("%s LIKE %s", column, ph)
}

// escapeGlobChars escapes SQLite GLOB metacharacters in a literal string.
func escapeGlobChars(s string) string {
	r := strings.NewReplacer(
		"[", "[[]",
	)
	return r.Replace(s)
}

// orMatchClause generates (col LIKE ? OR col LIKE ?) for multiple OR groups.
func orMatchClause(column string, groups []string, p *paramCollector) string {
	var clauses []string
	for _, g := range groups {
		if g == "" {
			continue
		}
		clauses = append(clauses, patternMatchClause(column, g, p))
	}
	if len(clauses) == 0 {
		return "1=1"
	}
	if len(clauses) == 1 {
		return clauses[0]
	}
	return "(" + strings.Join(clauses, " OR ") + ")"
}

// resolveRevRef normalizes a rev filter into a full ref name.
func resolveRevRef(rev string) string {
	if rev == "" {
		rev = "main"
	}
	if !strings.HasPrefix(rev, "refs/") {
		rev = "refs/heads/" + rev
	}
	return rev
}

// addRevFilter appends a ref name condition.
// When no rev is specified, matches both main and master default branches.
func addRevFilter(p *paramCollector, conditions *[]string, rev string) {
	if rev == "" {
		phMain := p.add("refs/heads/main")
		phMaster := p.add("refs/heads/master")
		*conditions = append(*conditions, "AND r.name IN ("+phMain+", "+phMaster+")")
		return
	}
	ph := p.add(resolveRevRef(rev))
	*conditions = append(*conditions, "AND r.name = "+ph)
}

// addRepoFilter appends repo LIKE/NOT LIKE conditions and joins.
func addRepoFilter(p *paramCollector, joins *[]string, conditions *[]string, repo, negRepo, repoCol string) {
	needsJoin := repo != "" || negRepo != ""
	if repo != "" {
		*joins = append(*joins, "JOIN repos rp ON rp.id = "+repoCol)
		clause := patternMatchClause("rp.name", repo, p)
		*conditions = append(*conditions, "AND "+clause)
	}
	if negRepo != "" {
		if repo == "" && needsJoin {
			*joins = append(*joins, "JOIN repos rp ON rp.id = "+repoCol)
		}
		clause := patternMatchClause("rp.name", negRepo, p)
		*conditions = append(*conditions, "AND NOT ("+clause+")")
	}
}

// addFileFilter appends file path conditions.
func addFileFilter(p *paramCollector, conditions *[]string, file, negFile, col string) {
	if file != "" {
		clause := patternMatchClause(col, file, p)
		*conditions = append(*conditions, "AND "+clause)
	}
	if negFile != "" {
		clause := patternMatchClause(col, negFile, p)
		*conditions = append(*conditions, "AND NOT ("+clause+")")
	}
}

// addLangFilter appends language conditions.
func addLangFilter(p *paramCollector, conditions *[]string, lang, negLang string) {
	if lang != "" {
		ph := p.add(lang)
		*conditions = append(*conditions, "AND b.language = "+ph)
	}
	if negLang != "" {
		ph := p.add(negLang)
		*conditions = append(*conditions, "AND b.language != "+ph)
	}
}

// addAuthorFilter appends author conditions.
func addAuthorFilter(p *paramCollector, conditions *[]string, author, negAuthor string) {
	if author != "" {
		clause := patternMatchClause("c.author", author, p)
		*conditions = append(*conditions, "AND "+clause)
	}
	if negAuthor != "" {
		clause := patternMatchClause("c.author", negAuthor, p)
		*conditions = append(*conditions, "AND NOT ("+clause+")")
	}
}

// addTimeFilter appends before/after timestamp conditions.
func addTimeFilter(p *paramCollector, conditions *[]string, before, after string) {
	if before != "" {
		ph := p.add(before)
		*conditions = append(*conditions, fmt.Sprintf("AND c.timestamp < CAST(strftime('%%s', %s) AS INTEGER)", ph))
	}
	if after != "" {
		ph := p.add(after)
		*conditions = append(*conditions, fmt.Sprintf("AND c.timestamp > CAST(strftime('%%s', %s) AS INTEGER)", ph))
	}
}

// resolveLimit returns count or default 20.
func resolveLimit(count int) int {
	if count == 0 {
		return 20
	}
	return count
}

// conditionsSQL joins conditions into a WHERE clause fragment.
func conditionsSQL(conditions []string) string {
	if len(conditions) == 0 {
		return ""
	}
	return "\n  " + strings.Join(conditions, "\n  ")
}

// Translate converts a ParsedQuery into a TranslatedQuery (SQL + params).
func Translate(query *ParsedQuery) (*TranslatedQuery, error) {
	if query.Filters.Calls != "" {
		return translateCallers(query)
	}
	if query.Filters.CalledBy != "" {
		return translateCallees(query)
	}
	if query.Filters.Returns != "" {
		return translateSymbol(query)
	}
	switch query.Type {
	case SearchTypeCode:
		return translateCode(query)
	case SearchTypeDiff:
		return translateDiff(query)
	case SearchTypeCommit:
		return translateCommit(query)
	case SearchTypeSymbol:
		return translateSymbol(query)
	case SearchTypeComment:
		return translateComment(query)
	default:
		return translateCode(query)
	}
}

func translateCode(query *ParsedQuery) (*TranslatedQuery, error) {
	if query.HasEmptyPattern() {
		return nil, fmt.Errorf("code search requires a search pattern")
	}

	f := query.Filters
	p := newParamCollector(f.Case)
	searchParam := p.add(query.SearchPattern())

	joins := []string{
		"JOIN blobs b ON b.id = cs.blob_id",
		"JOIN file_revs fr ON fr.blob_id = b.id",
		"JOIN refs r ON r.commit_id = fr.commit_id",
	}
	var conditions []string

	addRepoFilter(p, &joins, &conditions, f.Repo, f.NegRepo, "r.repo_id")
	addFileFilter(p, &conditions, f.File, f.NegFile, "fr.path")
	addLangFilter(p, &conditions, f.Lang, f.NegLang)
	addRevFilter(p, &conditions, f.Rev)

	limit := resolveLimit(f.Count)

	var selectClause, groupBy, orderBy string
	switch f.Select {
	case SelectRepo:
		if f.Repo == "" && f.NegRepo == "" {
			joins = append(joins, "JOIN repos rp ON rp.id = r.repo_id")
		}
		selectClause = "SELECT DISTINCT rp.name"
		orderBy = "ORDER BY rp.name"
	case SelectFile:
		selectClause = "SELECT DISTINCT fr.path"
		orderBy = "ORDER BY fr.path"
	default:
		selectClause = "SELECT fr.path, cs.score, cs.snippet"
		groupBy = "GROUP BY fr.path"
		orderBy = "ORDER BY cs.score DESC"
	}

	vtabCall := fmt.Sprintf("code_search(%s)", searchParam)
	if query.IsRegex {
		vtabCall = fmt.Sprintf("code_search(%s, 'regex')", searchParam)
	}

	parts := []string{
		selectClause,
		"FROM " + vtabCall + " cs",
		strings.Join(joins, "\n"),
		"WHERE 1=1" + conditionsSQL(conditions),
	}
	if groupBy != "" {
		parts = append(parts, groupBy)
	}
	parts = append(parts, orderBy)
	parts = append(parts, fmt.Sprintf("LIMIT %d", limit))

	sql := strings.Join(parts, "\n")
	var lines []string
	for _, line := range strings.Split(sql, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	sql = strings.Join(lines, "\n")

	return &TranslatedQuery{
		SQL:        sql,
		Params:     p.params,
		SearchType: SearchTypeCode,
	}, nil
}

func translateDiff(query *ParsedQuery) (*TranslatedQuery, error) {
	if query.HasEmptyPattern() {
		return nil, fmt.Errorf("diff search requires a search pattern")
	}

	f := query.Filters
	p := newParamCollector(f.Case)
	searchParam := p.add(query.SearchPattern())

	joins := []string{
		"JOIN diffs d ON d.id = ds.diff_id",
		"JOIN commits c ON c.id = d.commit_id",
	}
	var conditions []string

	addRepoFilter(p, &joins, &conditions, f.Repo, f.NegRepo, "c.repo_id")
	addFileFilter(p, &conditions, f.File, f.NegFile, "d.path")
	addAuthorFilter(p, &conditions, f.Author, f.NegAuthor)
	addTimeFilter(p, &conditions, f.Before, f.After)

	limit := resolveLimit(f.Count)

	selectClause := "SELECT substr(c.hash, 1, 10) AS hash,\n       substr(c.message, 1, 80) AS message,\n       d.path, round(ds.score, 2) AS score"
	orderByStr := "ORDER BY ds.score DESC"
	if f.Select == SelectFile {
		selectClause = "SELECT DISTINCT d.path"
		orderByStr = "ORDER BY d.path"
	}

	vtabCall := fmt.Sprintf("diff_search(%s)", searchParam)
	if query.IsRegex {
		vtabCall = fmt.Sprintf("diff_search(%s, 'regex')", searchParam)
	}

	sql := fmt.Sprintf("%s\nFROM %s ds\n%s\nWHERE 1=1%s\n%s\nLIMIT %d",
		selectClause, vtabCall, strings.Join(joins, "\n"), conditionsSQL(conditions), orderByStr, limit)

	return &TranslatedQuery{
		SQL:        sql,
		Params:     p.params,
		SearchType: SearchTypeDiff,
	}, nil
}

func translateCommit(query *ParsedQuery) (*TranslatedQuery, error) {
	if query.IsRegex {
		return nil, fmt.Errorf("regex patterns are only supported for code and diff search")
	}

	f := query.Filters
	p := newParamCollector(f.Case)
	var conditions []string
	var joins []string

	if !query.HasEmptyPattern() {
		clause := orMatchClause("c.message", query.SearchTerms, p)
		conditions = append(conditions, "AND "+clause)
	}
	if f.Message != "" {
		clause := patternMatchClause("c.message", f.Message, p)
		conditions = append(conditions, "AND "+clause)
	}
	if f.NegMessage != "" {
		clause := patternMatchClause("c.message", f.NegMessage, p)
		conditions = append(conditions, "AND NOT ("+clause+")")
	}
	addRepoFilter(p, &joins, &conditions, f.Repo, f.NegRepo, "c.repo_id")
	addAuthorFilter(p, &conditions, f.Author, f.NegAuthor)
	addTimeFilter(p, &conditions, f.Before, f.After)

	if len(conditions) == 0 {
		return nil, fmt.Errorf("commit search requires a search pattern or at least one filter (author:, before:, after:, message:)")
	}

	limit := resolveLimit(f.Count)

	joinsStr := ""
	if len(joins) > 0 {
		joinsStr = "\n" + strings.Join(joins, "\n")
	}

	sql := fmt.Sprintf(
		"SELECT substr(c.hash, 1, 10) AS hash, c.author,\n       substr(c.message, 1, 80) AS message\nFROM commits c%s\nWHERE 1=1%s\nORDER BY c.timestamp DESC\nLIMIT %d",
		joinsStr, conditionsSQL(conditions), limit)

	return &TranslatedQuery{
		SQL:        sql,
		Params:     p.params,
		SearchType: SearchTypeCommit,
	}, nil
}

func translateSymbol(query *ParsedQuery) (*TranslatedQuery, error) {
	if query.IsRegex {
		return nil, fmt.Errorf("regex patterns are only supported for code and diff search")
	}

	f := query.Filters
	p := newParamCollector(f.Case)
	joins := []string{
		"JOIN blobs b ON b.id = s.blob_id",
		"JOIN file_revs fr ON fr.blob_id = b.id",
		"JOIN refs r ON r.commit_id = fr.commit_id",
	}
	var conditions []string

	if !query.HasEmptyPattern() {
		clause := orMatchClause("s.name", query.SearchTerms, p)
		conditions = append(conditions, "AND "+clause)
	}
	addRepoFilter(p, &joins, &conditions, f.Repo, f.NegRepo, "r.repo_id")
	addFileFilter(p, &conditions, f.File, f.NegFile, "fr.path")
	addLangFilter(p, &conditions, f.Lang, f.NegLang)
	if f.Select == SelectSymbolKind {
		ph := p.add(f.SelectKind)
		conditions = append(conditions, "AND s.kind = "+ph)
	}
	if f.Returns != "" {
		clause := patternMatchClause("s.return_type", f.Returns, p)
		conditions = append(conditions, "AND "+clause)
		conditions = append(conditions, "AND s.kind IN ('function', 'method')")
	}
	addRevFilter(p, &conditions, f.Rev)

	if query.HasEmptyPattern() &&
		f.Select != SelectSymbolKind &&
		f.Lang == "" && f.File == "" && f.Returns == "" {
		return nil, fmt.Errorf("symbol search requires a search pattern or filter (lang:, file:, select:symbol.<kind>, returns:)")
	}

	limit := resolveLimit(f.Count)

	sql := fmt.Sprintf(
		"SELECT fr.path, s.name, s.kind, s.line\nFROM symbols s\n%s\nWHERE 1=1%s\nORDER BY fr.path, s.line\nLIMIT %d",
		strings.Join(joins, "\n"), conditionsSQL(conditions), limit)

	return &TranslatedQuery{
		SQL:        sql,
		Params:     p.params,
		SearchType: SearchTypeSymbol,
	}, nil
}

func translateComment(query *ParsedQuery) (*TranslatedQuery, error) {
	f := query.Filters
	p := newParamCollector(f.Case)

	joins := []string{
		"JOIN blobs b ON b.id = cm.blob_id",
		"JOIN file_revs fr ON fr.blob_id = b.id",
		"JOIN refs r ON r.commit_id = fr.commit_id",
	}
	var conditions []string

	if !query.HasEmptyPattern() {
		clause := orMatchClause("cm.text", query.SearchTerms, p)
		conditions = append(conditions, "AND "+clause)
	}
	if f.CommentKind != "" {
		ph := p.add(f.CommentKind)
		conditions = append(conditions, "AND cm.kind = "+ph)
	}

	addRepoFilter(p, &joins, &conditions, f.Repo, f.NegRepo, "r.repo_id")
	addFileFilter(p, &conditions, f.File, f.NegFile, "fr.path")
	addLangFilter(p, &conditions, f.Lang, f.NegLang)
	addRevFilter(p, &conditions, f.Rev)

	if query.HasEmptyPattern() && f.CommentKind == "" && f.Lang == "" && f.File == "" {
		return nil, fmt.Errorf("comment search requires a search pattern or filter (ckind:, lang:, file:)")
	}

	limit := resolveLimit(f.Count)

	sql := fmt.Sprintf(
		"SELECT fr.path, cm.text AS snippet, cm.kind, cm.line, b.language\nFROM comments cm\n%s\nWHERE 1=1%s\nORDER BY fr.path, cm.line\nLIMIT %d",
		strings.Join(joins, "\n"), conditionsSQL(conditions), limit)

	return &TranslatedQuery{
		SQL:        sql,
		Params:     p.params,
		SearchType: SearchTypeComment,
	}, nil
}

func translateCallers(query *ParsedQuery) (*TranslatedQuery, error) {
	if query.IsRegex {
		return nil, fmt.Errorf("regex patterns are only supported for code and diff search")
	}

	f := query.Filters
	p := newParamCollector(f.Case)
	joins := []string{
		"JOIN symbols s ON s.id = sr.symbol_id",
		"JOIN blobs b ON b.id = sr.blob_id",
		"JOIN file_revs fr ON fr.blob_id = b.id",
		"JOIN refs r ON r.commit_id = fr.commit_id",
	}
	var conditions []string

	clause := patternMatchClause("sr.ref_name", f.Calls, p)
	conditions = append(conditions, "AND "+clause)
	conditions = append(conditions, "AND sr.kind = 'call'")
	conditions = append(conditions, "AND s.kind = 'function'")

	addRepoFilter(p, &joins, &conditions, f.Repo, f.NegRepo, "r.repo_id")
	addFileFilter(p, &conditions, f.File, f.NegFile, "fr.path")
	addLangFilter(p, &conditions, f.Lang, f.NegLang)
	addRevFilter(p, &conditions, f.Rev)

	limit := resolveLimit(f.Count)

	sql := fmt.Sprintf(
		"SELECT DISTINCT fr.path, s.name, s.kind, s.line\nFROM symbol_refs sr\n%s\nWHERE 1=1%s\nORDER BY fr.path, s.line\nLIMIT %d",
		strings.Join(joins, "\n"), conditionsSQL(conditions), limit)

	return &TranslatedQuery{
		SQL:        sql,
		Params:     p.params,
		SearchType: SearchTypeSymbol,
	}, nil
}

func translateCallees(query *ParsedQuery) (*TranslatedQuery, error) {
	if query.IsRegex {
		return nil, fmt.Errorf("regex patterns are only supported for code and diff search")
	}

	f := query.Filters
	p := newParamCollector(f.Case)
	joins := []string{
		"JOIN symbol_refs sr ON sr.symbol_id = s.id AND sr.blob_id = s.blob_id",
		"JOIN blobs b ON b.id = sr.blob_id",
		"JOIN file_revs fr ON fr.blob_id = b.id",
		"JOIN refs r ON r.commit_id = fr.commit_id",
	}
	var conditions []string

	clause := patternMatchClause("s.name", f.CalledBy, p)
	conditions = append(conditions, "AND "+clause)
	conditions = append(conditions, "AND s.kind = 'function'")

	addRepoFilter(p, &joins, &conditions, f.Repo, f.NegRepo, "r.repo_id")
	addFileFilter(p, &conditions, f.File, f.NegFile, "fr.path")
	addLangFilter(p, &conditions, f.Lang, f.NegLang)
	addRevFilter(p, &conditions, f.Rev)

	limit := resolveLimit(f.Count)

	sql := fmt.Sprintf(
		"SELECT DISTINCT fr.path, sr.ref_name AS name, sr.kind, sr.line\nFROM symbols s\n%s\nWHERE 1=1%s\nORDER BY sr.line\nLIMIT %d",
		strings.Join(joins, "\n"), conditionsSQL(conditions), limit)

	return &TranslatedQuery{
		SQL:        sql,
		Params:     p.params,
		SearchType: SearchTypeSymbol,
	}, nil
}
