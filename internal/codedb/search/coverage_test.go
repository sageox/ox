package search

import (
	"context"
	"testing"

	blevesearch "github.com/blevesearch/bleve/v2/search"

	"github.com/sageox/ox/internal/codedb/store"
)

// --- ast.go: SearchType.String() coverage ---

func TestSearchTypeString(t *testing.T) {
	tests := []struct {
		st   SearchType
		want string
	}{
		{SearchTypeCode, "code"},
		{SearchTypeDiff, "diff"},
		{SearchTypeCommit, "commit"},
		{SearchTypeSymbol, "symbol"},
		{SearchTypeComment, "comment"},
		{SearchTypePR, "pr"},
		{SearchTypeIssue, "issue"},
		{SearchType(99), "code"}, // unknown defaults to code
	}
	for _, tt := range tests {
		if got := tt.st.String(); got != tt.want {
			t.Errorf("SearchType(%d).String() = %q, want %q", tt.st, got, tt.want)
		}
	}
}

func TestHasEmptyPatternAllEmpty(t *testing.T) {
	q := &ParsedQuery{SearchTerms: []string{"", ""}}
	if !q.HasEmptyPattern() {
		t.Error("expected empty pattern for all-empty terms")
	}
}

// --- query.go: parseSelect coverage ---

func TestParseSelectAllTypes(t *testing.T) {
	tests := []struct {
		input    string
		wantType SelectType
		wantKind string
		wantErr  bool
	}{
		{"repo", SelectRepo, "", false},
		{"file", SelectFile, "", false},
		{"symbol", SelectSymbol, "", false},
		{"symbol.class", SelectSymbolKind, "class", false},
		{"symbol.interface", SelectSymbolKind, "interface", false},
		{"bogus", SelectNone, "", true},
	}
	for _, tt := range tests {
		st, kind, err := parseSelect(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseSelect(%q) err = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if st != tt.wantType {
			t.Errorf("parseSelect(%q) type = %v, want %v", tt.input, st, tt.wantType)
		}
		if kind != tt.wantKind {
			t.Errorf("parseSelect(%q) kind = %q, want %q", tt.input, kind, tt.wantKind)
		}
	}
}

// --- translate.go: translateComment coverage ---

func TestTranslateCommentBasic(t *testing.T) {
	tq := mustTranslate(t, "type:comment TODO")
	if tq.SearchType != SearchTypeComment {
		t.Errorf("type = %v, want comment", tq.SearchType)
	}
	if !containsStr(tq.SQL, "comments cm") {
		t.Errorf("sql missing comments table: %s", tq.SQL)
	}
	if !containsStr(tq.SQL, "cm.text LIKE") {
		t.Errorf("sql missing cm.text LIKE: %s", tq.SQL)
	}
	if !containsStr(tq.SQL, "LIMIT 20") {
		t.Errorf("sql missing LIMIT 20: %s", tq.SQL)
	}
}

func TestTranslateCommentWithKind(t *testing.T) {
	tq := mustTranslate(t, "type:comment ckind:doc lang:go TODO")
	if !containsStr(tq.SQL, "cm.kind =") {
		t.Errorf("sql missing cm.kind: %s", tq.SQL)
	}
	if !hasParam(tq.Params, "doc") {
		t.Errorf("missing doc param: %v", tq.Params)
	}
	if !containsStr(tq.SQL, "b.language =") {
		t.Errorf("sql missing b.language: %s", tq.SQL)
	}
}

func TestTranslateCommentWithFile(t *testing.T) {
	tq := mustTranslate(t, "type:comment file:*.go TODO")
	if !containsStr(tq.SQL, "fr.path") {
		t.Errorf("sql missing fr.path filter: %s", tq.SQL)
	}
}

func TestTranslateCommentWithNegFile(t *testing.T) {
	tq := mustTranslate(t, "type:comment -file:test TODO")
	if !containsStr(tq.SQL, "NOT") {
		t.Errorf("sql missing NOT: %s", tq.SQL)
	}
}

func TestTranslateCommentWithRepo(t *testing.T) {
	tq := mustTranslate(t, "type:comment repo:myrepo TODO")
	if !containsStr(tq.SQL, "repos rp") {
		t.Errorf("sql missing repos join: %s", tq.SQL)
	}
}

func TestTranslateCommentWithNegRepo(t *testing.T) {
	tq := mustTranslate(t, "type:comment -repo:unwanted TODO")
	if !containsStr(tq.SQL, "NOT") {
		t.Errorf("sql missing NOT: %s", tq.SQL)
	}
}

func TestTranslateCommentWithNegLang(t *testing.T) {
	tq := mustTranslate(t, "type:comment -lang:python TODO")
	if !containsStr(tq.SQL, "b.language !=") {
		t.Errorf("sql missing b.language !=: %s", tq.SQL)
	}
}

func TestTranslateCommentWithRev(t *testing.T) {
	tq := mustTranslate(t, "type:comment rev:develop TODO")
	if !hasParam(tq.Params, "refs/heads/develop") {
		t.Errorf("missing develop ref: %v", tq.Params)
	}
}

func TestTranslateCommentEmptyPatternWithKind(t *testing.T) {
	// filter-only comment query should succeed
	tq := mustTranslate(t, "type:comment ckind:doc")
	if tq.SearchType != SearchTypeComment {
		t.Errorf("type = %v, want comment", tq.SearchType)
	}
}

func TestTranslateCommentEmptyPatternWithLang(t *testing.T) {
	tq := mustTranslate(t, "type:comment lang:go")
	if tq.SearchType != SearchTypeComment {
		t.Errorf("type = %v, want comment", tq.SearchType)
	}
}

func TestTranslateCommentEmptyPatternWithFile(t *testing.T) {
	tq := mustTranslate(t, "type:comment file:*.go")
	if tq.SearchType != SearchTypeComment {
		t.Errorf("type = %v, want comment", tq.SearchType)
	}
}

func TestTranslateCommentNoFiltersError(t *testing.T) {
	q := mustParse(t, "type:comment")
	_, err := Translate(q)
	if err == nil {
		t.Fatal("expected error for empty comment search without filters")
	}
	if !containsStr(err.Error(), "requires a search pattern") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestTranslateCommentOR(t *testing.T) {
	tq := mustTranslate(t, "type:comment TODO OR FIXME")
	if !containsStr(tq.SQL, " OR ") {
		t.Errorf("sql missing OR: %s", tq.SQL)
	}
	if !hasParam(tq.Params, "%TODO%") {
		t.Errorf("missing TODO param: %v", tq.Params)
	}
	if !hasParam(tq.Params, "%FIXME%") {
		t.Errorf("missing FIXME param: %v", tq.Params)
	}
}

func TestTranslateCommentWithCount(t *testing.T) {
	tq := mustTranslate(t, "type:comment count:5 TODO")
	if !containsStr(tq.SQL, "LIMIT 5") {
		t.Errorf("sql missing LIMIT 5: %s", tq.SQL)
	}
}

// --- planner.go: planCommentSearch coverage ---

func TestPlanCommentBareSearch(t *testing.T) {
	q := &ParsedQuery{
		SearchTerms: []string{"TODO"},
		Type:        SearchTypeComment,
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != JoinBleveOnly {
		t.Errorf("strategy = %v, want BleveOnly", plan.Strategy)
	}
	if plan.BleveIndex != "comment" {
		t.Errorf("bleve index = %q, want comment", plan.BleveIndex)
	}
	if plan.SearchType != SearchTypeComment {
		t.Errorf("search type = %v, want comment", plan.SearchType)
	}
	if plan.Limit != 20 {
		t.Errorf("limit = %d, want 20", plan.Limit)
	}
}

func TestPlanCommentWithFiltersIntersect(t *testing.T) {
	tests := []struct {
		name    string
		filters Filters
	}{
		{"repo", Filters{Repo: "myrepo"}},
		{"negRepo", Filters{NegRepo: "test"}},
		{"file", Filters{File: "*.go"}},
		{"negFile", Filters{NegFile: "vendor"}},
		{"lang", Filters{Lang: "go"}},
		{"negLang", Filters{NegLang: "python"}},
		{"rev", Filters{Rev: "develop"}},
		{"commentKind", Filters{CommentKind: "doc"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &ParsedQuery{
				SearchTerms: []string{"TODO"},
				Type:        SearchTypeComment,
				Filters:     tt.filters,
			}
			plan, err := Plan(q)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Strategy != JoinIntersect {
				t.Errorf("strategy = %v, want Intersect for filter %s", plan.Strategy, tt.name)
			}
		})
	}
}

func TestPlanCommentEmptyPatternFallsBackToSQL(t *testing.T) {
	// empty pattern with filters should fall back to planSQLOnly
	q := &ParsedQuery{
		Type:    SearchTypeComment,
		Filters: Filters{CommentKind: "doc", Lang: "go"},
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != JoinSQLOnly {
		t.Errorf("strategy = %v, want SQLOnly for empty comment with filters", plan.Strategy)
	}
}

func TestPlanCommentCustomCount(t *testing.T) {
	q := &ParsedQuery{
		SearchTerms: []string{"TODO"},
		Type:        SearchTypeComment,
		Filters:     Filters{Count: 50},
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Limit != 50 {
		t.Errorf("limit = %d, want 50", plan.Limit)
	}
}

func TestPlanCommentRegex(t *testing.T) {
	q := &ParsedQuery{
		SearchTerms: []string{`TODO.*fix`},
		Type:        SearchTypeComment,
		IsRegex:     true,
	}
	plan, err := Plan(q)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsRegex {
		t.Error("expected IsRegex")
	}
}

// --- executor.go: pure helper functions ---

func TestLikeOrGlob(t *testing.T) {
	tests := []struct {
		name          string
		column        string
		pattern       string
		caseSensitive bool
		wantClause    string
	}{
		{"simple substring", "name", "foo", false, "name LIKE ?"},
		{"wildcard case-insensitive", "name", "*.go", false, "lower(name) GLOB ?"},
		{"wildcard case-sensitive", "name", "*.go", true, "name GLOB ?"},
		{"question mark wildcard", "path", "test?.go", false, "lower(path) GLOB ?"},
		{"no wildcard", "path", "main", true, "path LIKE ?"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := likeOrGlob(tt.column, tt.pattern, tt.caseSensitive)
			if got != tt.wantClause {
				t.Errorf("likeOrGlob(%q, %q, %v) = %q, want %q",
					tt.column, tt.pattern, tt.caseSensitive, got, tt.wantClause)
			}
		})
	}
}

func TestLikeOrGlobParam(t *testing.T) {
	tests := []struct {
		pattern string
		want    string
	}{
		{"foo", "%foo%"},       // substring wrapping
		{"*.go", "*.go"},      // glob passthrough
		{"test?.rs", "test?.rs"}, // question-mark glob passthrough
	}
	for _, tt := range tests {
		got := likeOrGlobParam(tt.pattern)
		if got != tt.want {
			t.Errorf("likeOrGlobParam(%q) = %q, want %q", tt.pattern, got, tt.want)
		}
	}
}

func TestExtractFragment(t *testing.T) {
	tests := []struct {
		name string
		hit  *blevesearch.DocumentMatch
		want string
	}{
		{
			"with content fragment",
			&blevesearch.DocumentMatch{
				Fragments: blevesearch.FieldFragmentMap{
					"content": []string{"matched text here"},
				},
			},
			"matched text here",
		},
		{
			"empty fragments",
			&blevesearch.DocumentMatch{
				Fragments: blevesearch.FieldFragmentMap{},
			},
			"",
		},
		{
			"nil fragments",
			&blevesearch.DocumentMatch{},
			"",
		},
		{
			"multiple fragments returns first",
			&blevesearch.DocumentMatch{
				Fragments: blevesearch.FieldFragmentMap{
					"content": []string{"first", "second"},
				},
			},
			"first",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFragment(tt.hit)
			if got != tt.want {
				t.Errorf("extractFragment() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnrichDirtyHit(t *testing.T) {
	tests := []struct {
		name     string
		hitID    string
		fragment string
		wantPath string
		wantLang string
	}{
		{
			"go file",
			"dirty_cmd/main.go",
			"func main() {}",
			"cmd/main.go",
			"go",
		},
		{
			"rust file",
			"dirty_src/lib.rs",
			"fn process()",
			"src/lib.rs",
			"rust",
		},
		{
			"unknown extension falls back to ext",
			"dirty_data/config.xyz",
			"some content",
			"data/config.xyz",
			"xyz",
		},
		{
			"no extension",
			"dirty_Makefile",
			"all: build",
			"Makefile",
			"", // no extension and no detected language
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hit := &blevesearch.DocumentMatch{
				ID:    tt.hitID,
				Score: 1.5,
			}
			results, err := enrichDirtyHit(hit, tt.fragment)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(results))
			}
			r := results[0]
			if r.FilePath != tt.wantPath {
				t.Errorf("path = %q, want %q", r.FilePath, tt.wantPath)
			}
			if r.Content != tt.fragment {
				t.Errorf("content = %q, want %q", r.Content, tt.fragment)
			}
			if r.Score != 1.5 {
				t.Errorf("score = %f, want 1.5", r.Score)
			}
			if r.Language != tt.wantLang {
				t.Errorf("language = %q, want %q", r.Language, tt.wantLang)
			}
		})
	}
}

func TestAddDiffFilters(t *testing.T) {
	tests := []struct {
		name      string
		filters   Filters
		wantSQL   []string // substrings expected in appended SQL
		wantArgs  int      // number of args appended
	}{
		{
			"repo filter",
			Filters{Repo: "myrepo"},
			[]string{"c.repo_id IN", "repos"},
			1,
		},
		{
			"neg repo filter",
			Filters{NegRepo: "test"},
			[]string{"c.repo_id NOT IN", "repos"},
			1,
		},
		{
			"file filter",
			Filters{File: "*.go"},
			[]string{"d.path"},
			1,
		},
		{
			"neg file filter",
			Filters{NegFile: "vendor"},
			[]string{"NOT", "d.path"},
			1,
		},
		{
			"author filter",
			Filters{Author: "alice"},
			[]string{"c.author LIKE"},
			1,
		},
		{
			"neg author filter",
			Filters{NegAuthor: "bot"},
			[]string{"c.author NOT LIKE"},
			1,
		},
		{
			"before filter",
			Filters{Before: "2026-01-01"},
			[]string{"c.timestamp <", "strftime"},
			1,
		},
		{
			"after filter",
			Filters{After: "2025-06-01"},
			[]string{"c.timestamp >", "strftime"},
			1,
		},
		{
			"all filters combined",
			Filters{Repo: "r", NegRepo: "nr", File: "f", NegFile: "nf", Author: "a", NegAuthor: "na", Before: "b", After: "a"},
			[]string{"c.repo_id IN", "c.repo_id NOT IN", "d.path", "c.author LIKE", "c.author NOT LIKE", "c.timestamp <", "c.timestamp >"},
			8,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sqlQ := "SELECT * FROM diffs d JOIN commits c ON c.id = d.commit_id WHERE d.id = ?"
			args := []interface{}{"diff1"}
			addDiffFilters(&sqlQ, &args, &tt.filters)
			for _, want := range tt.wantSQL {
				if !containsStr(sqlQ, want) {
					t.Errorf("sql missing %q: %s", want, sqlQ)
				}
			}
			// -1 for the initial arg
			appended := len(args) - 1
			if appended != tt.wantArgs {
				t.Errorf("appended %d args, want %d", appended, tt.wantArgs)
			}
		})
	}
}

func TestAddCodeFilters(t *testing.T) {
	tests := []struct {
		name     string
		filters  Filters
		wantSQL  []string
		wantArgs int
	}{
		{
			"repo filter",
			Filters{Repo: "myrepo"},
			[]string{"rp.name"},
			1,
		},
		{
			"neg repo",
			Filters{NegRepo: "fork"},
			[]string{"NOT", "rp.name"},
			1,
		},
		{
			"file filter",
			Filters{File: "src/*.go"},
			[]string{"fr.path"},
			1,
		},
		{
			"neg file",
			Filters{NegFile: "test"},
			[]string{"NOT", "fr.path"},
			1,
		},
		{
			"lang filter",
			Filters{Lang: "rust"},
			[]string{"b.language = ?"},
			1,
		},
		{
			"neg lang",
			Filters{NegLang: "python"},
			[]string{"b.language != ?"},
			1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sqlQ := "SELECT * FROM blobs b WHERE b.id = ?"
			args := []interface{}{"blob1"}
			addCodeFilters(&sqlQ, &args, &tt.filters)
			for _, want := range tt.wantSQL {
				if !containsStr(sqlQ, want) {
					t.Errorf("sql missing %q: %s", want, sqlQ)
				}
			}
			appended := len(args) - 1
			if appended != tt.wantArgs {
				t.Errorf("appended %d args, want %d", appended, tt.wantArgs)
			}
		})
	}
}

func TestAddCommentFilters(t *testing.T) {
	tests := []struct {
		name     string
		filters  Filters
		wantSQL  []string
		wantArgs int
	}{
		{
			"repo",
			Filters{Repo: "myrepo"},
			[]string{"rp.name"},
			1,
		},
		{
			"neg repo",
			Filters{NegRepo: "fork"},
			[]string{"NOT", "rp.name"},
			1,
		},
		{
			"file",
			Filters{File: "*.go"},
			[]string{"fr.path"},
			1,
		},
		{
			"neg file",
			Filters{NegFile: "vendor"},
			[]string{"NOT", "fr.path"},
			1,
		},
		{
			"lang",
			Filters{Lang: "go"},
			[]string{"b.language = ?"},
			1,
		},
		{
			"neg lang",
			Filters{NegLang: "python"},
			[]string{"b.language != ?"},
			1,
		},
		{
			"comment kind",
			Filters{CommentKind: "doc"},
			[]string{"cm.kind = ?"},
			1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sqlQ := "SELECT * FROM comments cm WHERE cm.id = ?"
			args := []interface{}{"cm1"}
			addCommentFilters(&sqlQ, &args, &tt.filters)
			for _, want := range tt.wantSQL {
				if !containsStr(sqlQ, want) {
					t.Errorf("sql missing %q: %s", want, sqlQ)
				}
			}
			appended := len(args) - 1
			if appended != tt.wantArgs {
				t.Errorf("appended %d args, want %d", appended, tt.wantArgs)
			}
		})
	}
}

// --- executor.go: SQL-based execution with seeded data ---

func TestExecuteCommentSearchByLang(t *testing.T) {
	// filter-only comment queries fall back to SQL path (no Bleve needed)
	s := openTestStore(t)
	seedCommentData(t, s)

	q, err := ParseQuery("type:comment lang:go")
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for comment search by lang")
	}
}

func TestExecuteCommentSearchByKind(t *testing.T) {
	s := openTestStore(t)
	seedCommentData(t, s)

	q, err := ParseQuery("type:comment ckind:doc")
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for comment search by kind")
	}
}

func TestExecuteCommentSearchByFile(t *testing.T) {
	s := openTestStore(t)
	seedCommentData(t, s)

	q, err := ParseQuery("type:comment file:main.go")
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for comment search by file")
	}
}

func TestExecutePRSearch(t *testing.T) {
	s := openTestStore(t)
	seedPRData(t, s)

	q, err := ParseQuery("type:pr auth")
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for PR search")
	}
	found := false
	for _, r := range results {
		if r.Number == 42 {
			found = true
		}
	}
	if !found {
		t.Error("expected to find PR #42")
	}
}

func TestExecutePRSearchByState(t *testing.T) {
	s := openTestStore(t)
	seedPRData(t, s)

	q, err := ParseQuery("type:pr state:open")
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.State != "open" {
			t.Errorf("expected state=open, got %q", r.State)
		}
	}
}

func TestExecuteIssueSearch(t *testing.T) {
	s := openTestStore(t)
	seedIssueData(t, s)

	q, err := ParseQuery("type:issue memory")
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for issue search")
	}
}

func TestExecuteIssueSearchByAuthor(t *testing.T) {
	s := openTestStore(t)
	seedIssueData(t, s)

	q, err := ParseQuery("type:issue author:alice state:open")
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Author != "alice" {
			t.Errorf("expected author=alice, got %q", r.Author)
		}
	}
}

// --- translate.go: helper function edge cases ---

func TestResolveRevRef(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "refs/heads/main"},
		{"develop", "refs/heads/develop"},
		{"refs/heads/main", "refs/heads/main"},
		{"refs/tags/v1.0", "refs/tags/v1.0"},
	}
	for _, tt := range tests {
		got := resolveRevRef(tt.input)
		if got != tt.want {
			t.Errorf("resolveRevRef(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestResolveLimit(t *testing.T) {
	tests := []struct {
		input int
		want  int
	}{
		{0, 20},
		{1, 1},
		{100, 100},
	}
	for _, tt := range tests {
		got := resolveLimit(tt.input)
		if got != tt.want {
			t.Errorf("resolveLimit(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestConditionsSQL(t *testing.T) {
	tests := []struct {
		input []string
		want  string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"AND x = 1"}, "\n  AND x = 1"},
		{[]string{"AND x = 1", "AND y = 2"}, "\n  AND x = 1\n  AND y = 2"},
	}
	for _, tt := range tests {
		got := conditionsSQL(tt.input)
		if got != tt.want {
			t.Errorf("conditionsSQL(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestOrMatchClauseEdgeCases(t *testing.T) {
	// all empty groups should return "1=1"
	p := newParamCollector(false)
	got := orMatchClause("col", []string{"", ""}, p)
	if got != "1=1" {
		t.Errorf("orMatchClause with all empty groups = %q, want 1=1", got)
	}

	// single group (no parens wrapping)
	p2 := newParamCollector(false)
	got2 := orMatchClause("col", []string{"foo"}, p2)
	if containsStr(got2, "(") {
		t.Errorf("single group should not have parens: %q", got2)
	}
}

func TestEscapeGlobChars(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"foo", "foo"},
		{"Array[Int]", "Array[[]Int]"},
		{"no brackets", "no brackets"},
	}
	for _, tt := range tests {
		got := escapeGlobChars(tt.input)
		if got != tt.want {
			t.Errorf("escapeGlobChars(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParamCollector(t *testing.T) {
	p := newParamCollector(true)
	ph1 := p.add("val1")
	if ph1 != "?1" {
		t.Errorf("first placeholder = %q, want ?1", ph1)
	}
	ph2 := p.add("val2")
	if ph2 != "?2" {
		t.Errorf("second placeholder = %q, want ?2", ph2)
	}
	if len(p.params) != 2 || p.params[0] != "val1" || p.params[1] != "val2" {
		t.Errorf("params = %v", p.params)
	}
	if !p.caseSensitive {
		t.Error("expected caseSensitive=true")
	}
}

func TestPatternMatchClauseCaseSensitiveNoWild(t *testing.T) {
	// case-sensitive without wildcards uses GLOB with * wrapping + bracket escape
	p := newParamCollector(true)
	got := patternMatchClause("col", "foo", p)
	if !containsStr(got, "GLOB") {
		t.Errorf("expected GLOB for case-sensitive: %q", got)
	}
	if !hasParam(p.params, "*foo*") {
		t.Errorf("expected *foo* param, got %v", p.params)
	}
}

func TestAddRevFilterDefaultBranches(t *testing.T) {
	// empty rev should add both main and master
	p := newParamCollector(false)
	var conditions []string
	addRevFilter(p, &conditions, "")
	if len(conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(conditions))
	}
	if !containsStr(conditions[0], "IN") {
		t.Errorf("expected IN clause: %s", conditions[0])
	}
	if !hasParam(p.params, "refs/heads/main") || !hasParam(p.params, "refs/heads/master") {
		t.Errorf("expected main and master refs: %v", p.params)
	}
}

func TestAddRevFilterCustomBranch(t *testing.T) {
	p := newParamCollector(false)
	var conditions []string
	addRevFilter(p, &conditions, "feature-x")
	if !containsStr(conditions[0], "r.name =") {
		t.Errorf("expected equality clause: %s", conditions[0])
	}
	if !hasParam(p.params, "refs/heads/feature-x") {
		t.Errorf("expected feature-x ref: %v", p.params)
	}
}

// --- translate.go: diff select:file variant ---

func TestTranslateDiffSelectFile(t *testing.T) {
	tq := mustTranslate(t, "type:diff select:file streaming")
	if !containsStr(tq.SQL, "SELECT DISTINCT d.path") {
		t.Errorf("sql missing SELECT DISTINCT d.path: %s", tq.SQL)
	}
	if !containsStr(tq.SQL, "ORDER BY d.path") {
		t.Errorf("sql missing ORDER BY d.path: %s", tq.SQL)
	}
}

// --- translate.go: code select:repo variant ---

func TestTranslateCodeSelectRepo(t *testing.T) {
	tq := mustTranslate(t, "select:repo foo")
	if !containsStr(tq.SQL, "SELECT DISTINCT rp.name") {
		t.Errorf("sql missing SELECT DISTINCT rp.name: %s", tq.SQL)
	}
	if !containsStr(tq.SQL, "ORDER BY rp.name") {
		t.Errorf("sql missing ORDER BY rp.name: %s", tq.SQL)
	}
}

// --- translate.go: callers/callees regex error ---

func TestTranslateCallersRegex(t *testing.T) {
	q := &ParsedQuery{
		IsRegex: true,
		Filters: Filters{Calls: "handle.*"},
	}
	tq, err := Translate(q)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(tq.SQL, "REGEXP") {
		t.Errorf("expected REGEXP in callers SQL: %s", tq.SQL)
	}
}

func TestTranslateCalleesRegex(t *testing.T) {
	q := &ParsedQuery{
		IsRegex: true,
		Filters: Filters{CalledBy: "main.*"},
	}
	tq, err := Translate(q)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(tq.SQL, "REGEXP") {
		t.Errorf("expected REGEXP in callees SQL: %s", tq.SQL)
	}
}

func TestTranslateCommitRegex(t *testing.T) {
	q := &ParsedQuery{
		SearchTerms: []string{"fix.*auth"},
		Type:        SearchTypeCommit,
		IsRegex:     true,
	}
	tq, err := Translate(q)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(tq.SQL, "REGEXP") {
		t.Errorf("expected REGEXP in commit SQL: %s", tq.SQL)
	}
}

// --- translate.go: commit with message filter ---

func TestTranslateCommitWithMessageFilter(t *testing.T) {
	tq := mustTranslate(t, "type:commit message:refactor author:alice")
	if !containsStr(tq.SQL, "c.message LIKE") {
		t.Errorf("sql missing c.message LIKE: %s", tq.SQL)
	}
	if !hasParamContaining(tq.Params, "refactor") {
		t.Errorf("missing refactor param: %v", tq.Params)
	}
}

// --- query.go: parse comment-kind alias ---

func TestParseCommentKindAlias(t *testing.T) {
	q, err := ParseQuery("comment-kind:doc TODO")
	if err != nil {
		t.Fatal(err)
	}
	if q.Filters.CommentKind != "doc" {
		t.Errorf("commentKind = %q, want doc", q.Filters.CommentKind)
	}
}

// --- query.go: patterntype:keyword ---

func TestParsePatterntypeKeyword(t *testing.T) {
	q, err := ParseQuery("patterntype:keyword foo bar")
	if err != nil {
		t.Fatal(err)
	}
	if q.IsRegex {
		t.Error("keyword patterntype should not be regex")
	}
}

func TestParsePatterntypeInvalid(t *testing.T) {
	_, err := ParseQuery("patterntype:fuzzy foo")
	if err == nil {
		t.Fatal("expected error for invalid patterntype")
	}
}

// --- helper to seed comment data ---

func seedCommentData(t *testing.T, s *store.Store) {
	t.Helper()
	stmts := []string{
		`INSERT INTO repos (id, name, path) VALUES (1, 'github.com/test/repo', '/tmp/repo')`,
		`INSERT INTO commits (id, repo_id, hash, author, message, timestamp) VALUES (1, 1, 'abc1234567', 'alice', 'initial commit', 1700000000)`,
		`INSERT INTO refs (id, repo_id, name, commit_id) VALUES (1, 1, 'refs/heads/main', 1)`,
		`INSERT INTO blobs (id, content_hash, language, parsed) VALUES (1, 'hash1', 'go', 1)`,
		`INSERT INTO file_revs (id, commit_id, path, blob_id) VALUES (1, 1, 'main.go', 1)`,
		`INSERT INTO comments (id, blob_id, text, kind, line, end_line, col, end_col) VALUES (1, 1, 'TODO: fix this later', 'todo', 10, 10, 1, 25)`,
		`INSERT INTO comments (id, blob_id, text, kind, line, end_line, col, end_col) VALUES (2, 1, 'Package main provides the entry point', 'doc', 1, 1, 1, 40)`,
	}
	for _, stmt := range stmts {
		if _, err := s.Exec(stmt); err != nil {
			t.Fatalf("seed comment: %s: %v", stmt, err)
		}
	}
}

func seedPRData(t *testing.T, s *store.Store) {
	t.Helper()
	stmts := []string{
		`INSERT INTO pull_requests (id, number, title, body, author, state, updated_at, url) VALUES (1, 42, 'Add auth migration', 'Implements OAuth flow', 'alice', 'open', 1700200000, 'https://github.com/test/repo/pull/42')`,
		`INSERT INTO pull_requests (id, number, title, body, author, state, updated_at, url) VALUES (2, 43, 'Fix parser bug', 'Corrects edge case', 'bob', 'closed', 1700100000, 'https://github.com/test/repo/pull/43')`,
		`INSERT INTO pr_comments (id, pr_id, author, body) VALUES (1, 1, 'bob', 'LGTM, nice auth work')`,
	}
	for _, stmt := range stmts {
		if _, err := s.Exec(stmt); err != nil {
			t.Fatalf("seed PR: %s: %v", stmt, err)
		}
	}
}

func seedIssueData(t *testing.T, s *store.Store) {
	t.Helper()
	stmts := []string{
		`INSERT INTO issues (id, number, title, body, author, state, updated_at, url) VALUES (1, 100, 'memory leak in parser', 'OOM after 1000 requests', 'alice', 'open', 1700200000, 'https://github.com/test/repo/issues/100')`,
		`INSERT INTO issues (id, number, title, body, author, state, updated_at, url) VALUES (2, 101, 'Add dark mode', 'Would be nice', 'bob', 'closed', 1700100000, 'https://github.com/test/repo/issues/101')`,
		`INSERT INTO issue_comments (id, issue_id, author, body) VALUES (1, 1, 'bob', 'Can reproduce the memory issue')`,
	}
	for _, stmt := range stmts {
		if _, err := s.Exec(stmt); err != nil {
			t.Fatalf("seed issue: %s: %v", stmt, err)
		}
	}
}

// containsStr is a simple alias to avoid import of strings in test assertions
func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || findSubstring(s, sub))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
