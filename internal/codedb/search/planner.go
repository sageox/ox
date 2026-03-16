package search

import "fmt"

// JoinStrategy determines how Bleve and SQL results are combined.
type JoinStrategy int

const (
	// JoinIntersect means both Bleve and SQL queries run, results are intersected.
	JoinIntersect JoinStrategy = iota
	// JoinSQLOnly means only SQL is needed (commits, symbols, calls).
	JoinSQLOnly
	// JoinBleveOnly means only Bleve full-text search is needed (no metadata filters).
	JoinBleveOnly
)

func (js JoinStrategy) String() string {
	switch js {
	case JoinSQLOnly:
		return "sql_only"
	case JoinBleveOnly:
		return "bleve_only"
	default:
		return "intersect"
	}
}

// ExecutionPlan describes how a query will be executed.
type ExecutionPlan struct {
	// Strategy determines how results from Bleve and SQL are combined.
	Strategy JoinStrategy

	// BleveQuery is the full-text search query for Bleve (empty if SQLOnly).
	BleveQuery string

	// BleveIndex specifies which Bleve index to search: "code" or "diff".
	BleveIndex string

	// SQL is the metadata query (may reference Bleve result IDs).
	SQL string

	// SQLParams are the bound parameters for the SQL query.
	SQLParams []string

	// SearchType is the resolved search type.
	SearchType SearchType

	// Limit is the max number of results.
	Limit int

	// IsRegex indicates if BleveQuery should use regex matching.
	IsRegex bool
}

// Plan converts a ParsedQuery into an ExecutionPlan.
func Plan(query *ParsedQuery) (*ExecutionPlan, error) {
	// calls:, calledby:, returns: -> SQL only (symbol tables)
	if query.Filters.Calls != "" || query.Filters.CalledBy != "" || query.Filters.Returns != "" {
		return planSQLOnly(query)
	}

	switch query.Type {
	case SearchTypeCommit:
		return planSQLOnly(query)
	case SearchTypeSymbol:
		return planSQLOnly(query)
	case SearchTypePR:
		return planSQLOnly(query)
	case SearchTypeIssue:
		return planSQLOnly(query)
	case SearchTypeCode:
		return planCodeSearch(query)
	case SearchTypeDiff:
		return planDiffSearch(query)
	case SearchTypeComment:
		return planCommentSearch(query)
	default:
		return planCodeSearch(query)
	}
}

func planSQLOnly(query *ParsedQuery) (*ExecutionPlan, error) {
	translated, err := Translate(query)
	if err != nil {
		return nil, err
	}

	limit := query.Filters.Count
	if limit == 0 {
		limit = 20
	}

	return &ExecutionPlan{
		Strategy:   JoinSQLOnly,
		SQL:        translated.SQL,
		SQLParams:  translated.Params,
		SearchType: translated.SearchType,
		Limit:      limit,
	}, nil
}

func planCodeSearch(query *ParsedQuery) (*ExecutionPlan, error) {
	if query.HasEmptyPattern() {
		return nil, fmt.Errorf("code search requires a search pattern")
	}

	limit := query.Filters.Count
	if limit == 0 {
		limit = 20
	}

	// Determine if we need SQL metadata filtering
	hasMetadataFilters := query.Filters.Repo != "" || query.Filters.NegRepo != "" ||
		query.Filters.File != "" || query.Filters.NegFile != "" ||
		query.Filters.Lang != "" || query.Filters.NegLang != "" ||
		query.Filters.Rev != "" ||
		query.Filters.Select != SelectNone

	strategy := JoinBleveOnly
	if hasMetadataFilters {
		strategy = JoinIntersect
	}

	// Also generate SQL translation for --sql debug output
	translated, _ := Translate(query)
	var sql string
	var sqlParams []string
	if translated != nil {
		sql = translated.SQL
		sqlParams = translated.Params
	}

	return &ExecutionPlan{
		Strategy:   strategy,
		BleveQuery: query.SearchPattern(),
		BleveIndex: "code",
		SQL:        sql,
		SQLParams:  sqlParams,
		SearchType: SearchTypeCode,
		Limit:      limit,
		IsRegex:    query.IsRegex,
	}, nil
}

func planDiffSearch(query *ParsedQuery) (*ExecutionPlan, error) {
	if query.HasEmptyPattern() {
		return nil, fmt.Errorf("diff search requires a search pattern")
	}

	limit := query.Filters.Count
	if limit == 0 {
		limit = 20
	}

	hasMetadataFilters := query.Filters.Repo != "" || query.Filters.NegRepo != "" ||
		query.Filters.File != "" || query.Filters.NegFile != "" ||
		query.Filters.Author != "" || query.Filters.NegAuthor != "" ||
		query.Filters.Before != "" || query.Filters.After != "" ||
		query.Filters.Select != SelectNone

	strategy := JoinBleveOnly
	if hasMetadataFilters {
		strategy = JoinIntersect
	}

	translated, _ := Translate(query)
	var sql string
	var sqlParams []string
	if translated != nil {
		sql = translated.SQL
		sqlParams = translated.Params
	}

	return &ExecutionPlan{
		Strategy:   strategy,
		BleveQuery: query.SearchPattern(),
		BleveIndex: "diff",
		SQL:        sql,
		SQLParams:  sqlParams,
		SearchType: SearchTypeDiff,
		Limit:      limit,
		IsRegex:    query.IsRegex,
	}, nil
}

func planCommentSearch(query *ParsedQuery) (*ExecutionPlan, error) {
	if query.HasEmptyPattern() {
		// allow filter-only comment queries (e.g. ckind:doc lang:go)
		return planSQLOnly(query)
	}

	limit := query.Filters.Count
	if limit == 0 {
		limit = 20
	}

	hasMetadataFilters := query.Filters.Repo != "" || query.Filters.NegRepo != "" ||
		query.Filters.File != "" || query.Filters.NegFile != "" ||
		query.Filters.Lang != "" || query.Filters.NegLang != "" ||
		query.Filters.Rev != "" ||
		query.Filters.CommentKind != ""

	strategy := JoinBleveOnly
	if hasMetadataFilters {
		strategy = JoinIntersect
	}

	translated, _ := Translate(query)
	var sql string
	var sqlParams []string
	if translated != nil {
		sql = translated.SQL
		sqlParams = translated.Params
	}

	return &ExecutionPlan{
		Strategy:   strategy,
		BleveQuery: query.SearchPattern(),
		BleveIndex: "comment",
		SQL:        sql,
		SQLParams:  sqlParams,
		SearchType: SearchTypeComment,
		Limit:      limit,
		IsRegex:    query.IsRegex,
	}, nil
}
