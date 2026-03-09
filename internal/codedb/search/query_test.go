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
