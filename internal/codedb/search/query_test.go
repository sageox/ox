package search

import (
	"strings"
	"testing"
)

func TestTokenize(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"foo bar baz", []string{"foo", "bar", "baz"}},
		{`lang:rust "foo bar" baz`, []string{"lang:rust", `"foo bar"`, "baz"}},
		{"repo:SFrame file:*.rs fn", []string{"repo:SFrame", "file:*.rs", "fn"}},
		{"-file:test foo", []string{"-file:test", "foo"}},
		{"", nil},
		{"  foo   bar  ", []string{"foo", "bar"}},
		{`foo "bar baz`, []string{"foo", `"bar baz"`}},
	}
	for _, tt := range tests {
		got := tokenize(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("tokenize(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("tokenize(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestParseBareSearch(t *testing.T) {
	q, err := ParseQuery("foo bar")
	if err != nil {
		t.Fatal(err)
	}
	if q.SearchPattern() != "foo bar" {
		t.Errorf("pattern = %q", q.SearchPattern())
	}
	if q.Type != SearchTypeCode {
		t.Errorf("type = %v", q.Type)
	}
}

func TestParseWithFilters(t *testing.T) {
	q, err := ParseQuery("lang:rust file:*.rs process_data")
	if err != nil {
		t.Fatal(err)
	}
	if q.SearchPattern() != "process_data" {
		t.Errorf("pattern = %q", q.SearchPattern())
	}
	if q.Filters.Lang != "rust" {
		t.Errorf("lang = %q", q.Filters.Lang)
	}
	if q.Filters.File != "*.rs" {
		t.Errorf("file = %q", q.Filters.File)
	}
}

func TestParseTypeSymbol(t *testing.T) {
	q, err := ParseQuery("type:symbol lang:rust SFrame")
	if err != nil {
		t.Fatal(err)
	}
	if q.Type != SearchTypeSymbol {
		t.Errorf("type = %v", q.Type)
	}
	if q.SearchPattern() != "SFrame" {
		t.Errorf("pattern = %q", q.SearchPattern())
	}
}

func TestParseTypeDiff(t *testing.T) {
	q, err := ParseQuery("type:diff author:ylow streaming")
	if err != nil {
		t.Fatal(err)
	}
	if q.Type != SearchTypeDiff {
		t.Errorf("type = %v", q.Type)
	}
	if q.Filters.Author != "ylow" {
		t.Errorf("author = %q", q.Filters.Author)
	}
}

func TestParseTypeCommit(t *testing.T) {
	q, err := ParseQuery("type:commit before:2026-01-01 after:2025-06-01 refactor")
	if err != nil {
		t.Fatal(err)
	}
	if q.Type != SearchTypeCommit {
		t.Errorf("type = %v", q.Type)
	}
	if q.Filters.Before != "2026-01-01" {
		t.Errorf("before = %q", q.Filters.Before)
	}
	if q.Filters.After != "2025-06-01" {
		t.Errorf("after = %q", q.Filters.After)
	}
}

func TestParseNegatedFile(t *testing.T) {
	q, err := ParseQuery("-file:test foo")
	if err != nil {
		t.Fatal(err)
	}
	if q.Filters.NegFile != "test" {
		t.Errorf("neg_file = %q", q.Filters.NegFile)
	}
}

func TestParseCount(t *testing.T) {
	q, err := ParseQuery("count:50 foo")
	if err != nil {
		t.Fatal(err)
	}
	if q.Filters.Count != 50 {
		t.Errorf("count = %d", q.Filters.Count)
	}
}

func TestParseSelectSymbolKind(t *testing.T) {
	q, err := ParseQuery("type:symbol select:symbol.function foo")
	if err != nil {
		t.Fatal(err)
	}
	if q.Filters.Select != SelectSymbolKind {
		t.Errorf("select = %v", q.Filters.Select)
	}
	if q.Filters.SelectKind != "function" {
		t.Errorf("selectKind = %q", q.Filters.SelectKind)
	}
}

func TestParseRev(t *testing.T) {
	q, err := ParseQuery("rev:develop foo")
	if err != nil {
		t.Fatal(err)
	}
	if q.Filters.Rev != "develop" {
		t.Errorf("rev = %q", q.Filters.Rev)
	}
}

func TestParseQuotedPhrase(t *testing.T) {
	q, err := ParseQuery(`lang:rust "foo bar"`)
	if err != nil {
		t.Fatal(err)
	}
	if q.SearchPattern() != "foo bar" {
		t.Errorf("pattern = %q", q.SearchPattern())
	}
}

func TestParseUnknownTypeError(t *testing.T) {
	_, err := ParseQuery("type:bogus foo")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Unknown search type") && !strings.Contains(err.Error(), "unknown search type") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseInvalidCountError(t *testing.T) {
	_, err := ParseQuery("count:abc foo")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "positive integer") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseNegationUnsupported(t *testing.T) {
	_, err := ParseQuery("-count:5 bar")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "negation not supported") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseUnknownFilterIsSearchTerm(t *testing.T) {
	q, err := ParseQuery("http://example.com foo")
	if err != nil {
		t.Fatal(err)
	}
	if q.SearchPattern() != "http://example.com foo" {
		t.Errorf("pattern = %q", q.SearchPattern())
	}
}

func TestParseLangAlias(t *testing.T) {
	q, err := ParseQuery("l:python foo")
	if err != nil {
		t.Fatal(err)
	}
	if q.Filters.Lang != "python" {
		t.Errorf("lang = %q", q.Filters.Lang)
	}
}

func TestParseRegex(t *testing.T) {
	q, err := ParseQuery(`/err\d+/`)
	if err != nil {
		t.Fatal(err)
	}
	if !q.IsRegex {
		t.Error("expected IsRegex")
	}
	if q.SearchPattern() != `err\d+` {
		t.Errorf("pattern = %q", q.SearchPattern())
	}
}

func TestParseRegexWithFilters(t *testing.T) {
	q, err := ParseQuery(`lang:rust /fn\s+\w+/`)
	if err != nil {
		t.Fatal(err)
	}
	if !q.IsRegex {
		t.Error("expected IsRegex")
	}
	if q.Filters.Lang != "rust" {
		t.Errorf("lang = %q", q.Filters.Lang)
	}
}

func TestParseEmptyRegexError(t *testing.T) {
	_, err := ParseQuery("//")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "empty regex") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseOR(t *testing.T) {
	q, err := ParseQuery("foo OR bar")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.SearchTerms) != 2 || q.SearchTerms[0] != "foo" || q.SearchTerms[1] != "bar" {
		t.Errorf("terms = %v", q.SearchTerms)
	}
}

func TestParseORThreeGroups(t *testing.T) {
	q, err := ParseQuery("foo OR bar OR baz")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.SearchTerms) != 3 {
		t.Errorf("terms = %v", q.SearchTerms)
	}
}

func TestParseORMultiWord(t *testing.T) {
	q, err := ParseQuery("error handling OR panic recovery")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.SearchTerms) != 2 || q.SearchTerms[0] != "error handling" || q.SearchTerms[1] != "panic recovery" {
		t.Errorf("terms = %v", q.SearchTerms)
	}
}

func TestParsePatterntypeRegexpWithORError(t *testing.T) {
	_, err := ParseQuery("patterntype:regexp foo OR bar")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "cannot be combined with OR") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseRepoAtRevision(t *testing.T) {
	q, err := ParseQuery("repo:myrepo@develop foo")
	if err != nil {
		t.Fatal(err)
	}
	if q.Filters.Repo != "myrepo" {
		t.Errorf("repo = %q", q.Filters.Repo)
	}
	if q.Filters.Rev != "develop" {
		t.Errorf("rev = %q", q.Filters.Rev)
	}
}

func TestParseCalls(t *testing.T) {
	q, err := ParseQuery("calls:groupby")
	if err != nil {
		t.Fatal(err)
	}
	if q.Filters.Calls != "groupby" {
		t.Errorf("calls = %q", q.Filters.Calls)
	}
	if !q.HasEmptyPattern() {
		t.Error("expected empty pattern")
	}
}

func TestParseCalledBy(t *testing.T) {
	q, err := ParseQuery("calledby:groupby")
	if err != nil {
		t.Fatal(err)
	}
	if q.Filters.CalledBy != "groupby" {
		t.Errorf("calledby = %q", q.Filters.CalledBy)
	}
}

func TestParseReturns(t *testing.T) {
	q, err := ParseQuery("returns:BatchIterator")
	if err != nil {
		t.Fatal(err)
	}
	if q.Filters.Returns != "BatchIterator" {
		t.Errorf("returns = %q", q.Filters.Returns)
	}
}

func TestParseCaseYes(t *testing.T) {
	q, err := ParseQuery("case:yes foo")
	if err != nil {
		t.Fatal(err)
	}
	if !q.Filters.Case {
		t.Error("expected case sensitive")
	}
}

func TestParseCaseDefault(t *testing.T) {
	q, err := ParseQuery("foo")
	if err != nil {
		t.Fatal(err)
	}
	if q.Filters.Case {
		t.Error("expected case insensitive by default")
	}
}

func TestParseTypePR(t *testing.T) {
	q, err := ParseQuery("type:pr auth migration")
	if err != nil {
		t.Fatal(err)
	}
	if q.Type != SearchTypePR {
		t.Errorf("type = %v, want pr", q.Type)
	}
	if q.SearchPattern() != "auth migration" {
		t.Errorf("pattern = %q", q.SearchPattern())
	}
}

func TestParseTypeIssue(t *testing.T) {
	q, err := ParseQuery("type:issue memory leak")
	if err != nil {
		t.Fatal(err)
	}
	if q.Type != SearchTypeIssue {
		t.Errorf("type = %v, want issue", q.Type)
	}
	if q.SearchPattern() != "memory leak" {
		t.Errorf("pattern = %q", q.SearchPattern())
	}
}

func TestParseTypePRWithState(t *testing.T) {
	q, err := ParseQuery("type:pr state:open author:alice")
	if err != nil {
		t.Fatal(err)
	}
	if q.Type != SearchTypePR {
		t.Errorf("type = %v, want pr", q.Type)
	}
	if q.Filters.State != "open" {
		t.Errorf("state = %q, want open", q.Filters.State)
	}
	if q.Filters.Author != "alice" {
		t.Errorf("author = %q, want alice", q.Filters.Author)
	}
}

func TestParseTypeIssueWithFilters(t *testing.T) {
	q, err := ParseQuery("type:issue state:closed before:2026-01-01 count:10 bug")
	if err != nil {
		t.Fatal(err)
	}
	if q.Type != SearchTypeIssue {
		t.Errorf("type = %v, want issue", q.Type)
	}
	if q.Filters.State != "closed" {
		t.Errorf("state = %q", q.Filters.State)
	}
	if q.Filters.Before != "2026-01-01" {
		t.Errorf("before = %q", q.Filters.Before)
	}
	if q.Filters.Count != 10 {
		t.Errorf("count = %d", q.Filters.Count)
	}
	if q.SearchPattern() != "bug" {
		t.Errorf("pattern = %q", q.SearchPattern())
	}
}

// ── Complex Regex Parsing ──────────────────────────────────────────────────────

func TestParseRegex_EscapedCharacters(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		pattern string
	}{
		{"backslash_d", `/err\d+/`, `err\d+`},
		{"backslash_w", `/fn\s+\w+/`, `fn\s+\w+`},
		{"backslash_n", `/line1\nline2/`, `line1\nline2`},
		{"dot_star", `/foo.*bar/`, `foo.*bar`},
		{"caret_dollar", `/^func\s/`, `^func\s`},
		{"character_class", `/[A-Z][a-z]+/`, `[A-Z][a-z]+`},
		{"negated_class", `/[^0-9]+/`, `[^0-9]+`},
		{"alternation", `/error|warn|fatal/`, `error|warn|fatal`},
		{"quantifiers", `/a{2,5}b?c+/`, `a{2,5}b?c+`},
		{"groups", `/(func|method)\s+(\w+)/`, `(func|method)\s+(\w+)`},
		{"lookahead_syntax", `/foo(?=bar)/`, `foo(?=bar)`},
		{"non_greedy", `/.*?end/`, `.*?end`},
		{"unicode_class", `/\p{L}+/`, `\p{L}+`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := ParseQuery(tc.input)
			if err != nil {
				t.Fatalf("ParseQuery(%q): %v", tc.input, err)
			}
			if !q.IsRegex {
				t.Error("expected IsRegex")
			}
			if q.SearchPattern() != tc.pattern {
				t.Errorf("pattern = %q, want %q", q.SearchPattern(), tc.pattern)
			}
		})
	}
}

func TestParseRegex_WithAllSearchTypes(t *testing.T) {
	types := []struct {
		prefix     string
		searchType SearchType
	}{
		{"type:code", SearchTypeCode},
		{"type:symbol", SearchTypeSymbol},
		{"type:commit", SearchTypeCommit},
		{"type:diff", SearchTypeDiff},
	}
	for _, tc := range types {
		t.Run(tc.prefix, func(t *testing.T) {
			q, err := ParseQuery(tc.prefix + ` /test\d+/`)
			if err != nil {
				t.Fatalf("ParseQuery: %v", err)
			}
			if !q.IsRegex {
				t.Error("expected IsRegex")
			}
			if q.Type != tc.searchType {
				t.Errorf("type = %v, want %v", q.Type, tc.searchType)
			}
			if q.SearchPattern() != `test\d+` {
				t.Errorf("pattern = %q", q.SearchPattern())
			}
		})
	}
}

func TestParseRegex_PatterntypeWithFilters(t *testing.T) {
	q, err := ParseQuery(`patterntype:regexp lang:go file:*.go func\s+Test`)
	if err != nil {
		t.Fatal(err)
	}
	if !q.IsRegex {
		t.Error("expected IsRegex from patterntype:regexp")
	}
	if q.Filters.Lang != "go" {
		t.Errorf("lang = %q", q.Filters.Lang)
	}
	if q.Filters.File != "*.go" {
		t.Errorf("file = %q", q.Filters.File)
	}
	if q.SearchPattern() != `func\s+Test` {
		t.Errorf("pattern = %q", q.SearchPattern())
	}
}

func TestParseRegex_SlashInMiddleNotRegex(t *testing.T) {
	// path/to/file should NOT be parsed as regex
	q, err := ParseQuery("path/to/file")
	if err != nil {
		t.Fatal(err)
	}
	if q.IsRegex {
		t.Error("path/to/file should NOT be parsed as regex")
	}
}

func TestParseRegex_SingleSlashNotRegex(t *testing.T) {
	q, err := ParseQuery("/")
	if err != nil {
		t.Fatal(err)
	}
	if q.IsRegex {
		t.Error("single slash should not be regex")
	}
}

func TestParseUnknownTypeErrorIncludesPRIssue(t *testing.T) {
	_, err := ParseQuery("type:bogus foo")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "pr") || !strings.Contains(err.Error(), "issue") {
		t.Errorf("error should mention pr and issue: %v", err)
	}
}
