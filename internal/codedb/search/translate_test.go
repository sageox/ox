package search

import (
	"strings"
	"testing"
)

func mustParse(t *testing.T, input string) *ParsedQuery {
	t.Helper()
	q, err := ParseQuery(input)
	if err != nil {
		t.Fatalf("ParseQuery(%q): %v", input, err)
	}
	return q
}

func mustTranslate(t *testing.T, input string) *TranslatedQuery {
	t.Helper()
	q := mustParse(t, input)
	tq, err := Translate(q)
	if err != nil {
		t.Fatalf("Translate(%q): %v", input, err)
	}
	return tq
}

func hasParam(params []string, val string) bool {
	for _, p := range params {
		if p == val {
			return true
		}
	}
	return false
}

func hasParamContaining(params []string, sub string) bool {
	for _, p := range params {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}

func TestTranslateCodeBasic(t *testing.T) {
	tq := mustTranslate(t, "process_data")
	if tq.SearchType != SearchTypeCode {
		t.Errorf("type = %v", tq.SearchType)
	}
	if !strings.Contains(tq.SQL, "code_search(?1)") {
		t.Errorf("sql missing code_search: %s", tq.SQL)
	}
	if !strings.Contains(tq.SQL, "LIMIT 20") {
		t.Errorf("sql missing LIMIT 20: %s", tq.SQL)
	}
	if tq.Params[0] != "process_data" {
		t.Errorf("params[0] = %q", tq.Params[0])
	}
	if !hasParamContaining(tq.Params, "refs/heads/main") {
		t.Errorf("missing main ref param: %v", tq.Params)
	}
}

func TestTranslateCodeWithFilters(t *testing.T) {
	tq := mustTranslate(t, "lang:rust file:*.rs count:10 foo")
	if !strings.Contains(tq.SQL, "b.language =") {
		t.Errorf("sql missing language filter: %s", tq.SQL)
	}
	if !strings.Contains(tq.SQL, "LIMIT 10") {
		t.Errorf("sql missing LIMIT 10: %s", tq.SQL)
	}
	if tq.Params[0] != "foo" {
		t.Errorf("params[0] = %q", tq.Params[0])
	}
}

func TestTranslateCodeNegFile(t *testing.T) {
	tq := mustTranslate(t, "-file:test foo")
	if !strings.Contains(tq.SQL, "NOT") {
		t.Errorf("sql missing NOT: %s", tq.SQL)
	}
	if !strings.Contains(tq.SQL, "fr.path LIKE") {
		t.Errorf("sql missing LIKE: %s", tq.SQL)
	}
}

func TestTranslateCodeRepoFilter(t *testing.T) {
	tq := mustTranslate(t, "repo:SFrame foo")
	if !strings.Contains(tq.SQL, "repos rp") {
		t.Errorf("sql missing repos join: %s", tq.SQL)
	}
	if !strings.Contains(tq.SQL, "rp.name LIKE") {
		t.Errorf("sql missing rp.name LIKE: %s", tq.SQL)
	}
}

func TestTranslateCodeSelectFile(t *testing.T) {
	tq := mustTranslate(t, "select:file foo")
	if !strings.Contains(tq.SQL, "SELECT DISTINCT fr.path") {
		t.Errorf("sql missing SELECT DISTINCT fr.path: %s", tq.SQL)
	}
	if strings.Contains(tq.SQL, "cs.score") {
		t.Errorf("sql should not contain cs.score: %s", tq.SQL)
	}
}

func TestTranslateCodeCustomRev(t *testing.T) {
	tq := mustTranslate(t, "rev:develop foo")
	if !hasParam(tq.Params, "refs/heads/develop") {
		t.Errorf("missing develop ref: %v", tq.Params)
	}
}

func TestTranslateCodeEmptyPatternError(t *testing.T) {
	q := mustParse(t, "lang:rust")
	_, err := Translate(q)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "requires a search pattern") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestTranslateCodeSubstringMatch(t *testing.T) {
	tq := mustTranslate(t, "file:csv foo")
	if !strings.Contains(tq.SQL, "LIKE") {
		t.Errorf("sql missing LIKE: %s", tq.SQL)
	}
	if !hasParam(tq.Params, "%csv%") {
		t.Errorf("missing csv param: %v", tq.Params)
	}
}

func TestTranslateCodeGlobMatch(t *testing.T) {
	tq := mustTranslate(t, "file:*.rs foo")
	if !strings.Contains(tq.SQL, "lower(fr.path) GLOB") {
		t.Errorf("sql missing lower GLOB: %s", tq.SQL)
	}
	if !hasParam(tq.Params, "*.rs") {
		t.Errorf("missing *.rs param: %v", tq.Params)
	}
}

func TestTranslateDiffBasic(t *testing.T) {
	tq := mustTranslate(t, "type:diff streaming")
	if tq.SearchType != SearchTypeDiff {
		t.Errorf("type = %v", tq.SearchType)
	}
	if !strings.Contains(tq.SQL, "diff_search(?1)") {
		t.Errorf("sql missing diff_search: %s", tq.SQL)
	}
	if tq.Params[0] != "streaming" {
		t.Errorf("params[0] = %q", tq.Params[0])
	}
}

func TestTranslateDiffWithAuthor(t *testing.T) {
	tq := mustTranslate(t, "type:diff author:ylow streaming")
	if !strings.Contains(tq.SQL, "c.author") {
		t.Errorf("sql missing c.author: %s", tq.SQL)
	}
	if !hasParamContaining(tq.Params, "ylow") {
		t.Errorf("missing ylow param: %v", tq.Params)
	}
}

func TestTranslateDiffWithDates(t *testing.T) {
	tq := mustTranslate(t, "type:diff before:2026-01-01 after:2025-06-01 streaming")
	if !strings.Contains(tq.SQL, "c.timestamp <") {
		t.Errorf("sql missing timestamp <: %s", tq.SQL)
	}
	if !strings.Contains(tq.SQL, "c.timestamp >") {
		t.Errorf("sql missing timestamp >: %s", tq.SQL)
	}
	if !strings.Contains(tq.SQL, "strftime") {
		t.Errorf("sql missing strftime: %s", tq.SQL)
	}
}

func TestTranslateDiffEmptyPatternError(t *testing.T) {
	q := mustParse(t, "type:diff")
	_, err := Translate(q)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "requires a search pattern") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestTranslateCommitBasic(t *testing.T) {
	tq := mustTranslate(t, "type:commit refactor")
	if tq.SearchType != SearchTypeCommit {
		t.Errorf("type = %v", tq.SearchType)
	}
	if !strings.Contains(tq.SQL, "commits c") {
		t.Errorf("sql missing commits c: %s", tq.SQL)
	}
	if !strings.Contains(tq.SQL, "c.message LIKE") {
		t.Errorf("sql missing c.message LIKE: %s", tq.SQL)
	}
}

func TestTranslateCommitAuthorOnly(t *testing.T) {
	tq := mustTranslate(t, "type:commit author:ylow")
	if !strings.Contains(tq.SQL, "c.author") {
		t.Errorf("sql missing c.author: %s", tq.SQL)
	}
}

func TestTranslateCommitNoFiltersError(t *testing.T) {
	q := mustParse(t, "type:commit")
	_, err := Translate(q)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "requires a search pattern") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestTranslateSymbolBasic(t *testing.T) {
	tq := mustTranslate(t, "type:symbol process_data")
	if tq.SearchType != SearchTypeSymbol {
		t.Errorf("type = %v", tq.SearchType)
	}
	if !strings.Contains(tq.SQL, "symbols s") {
		t.Errorf("sql missing symbols s: %s", tq.SQL)
	}
	if !strings.Contains(tq.SQL, "s.name LIKE") {
		t.Errorf("sql missing s.name LIKE: %s", tq.SQL)
	}
}

func TestTranslateSymbolWithLang(t *testing.T) {
	tq := mustTranslate(t, "type:symbol lang:rust SFrame")
	if !strings.Contains(tq.SQL, "b.language =") {
		t.Errorf("sql missing b.language: %s", tq.SQL)
	}
}

func TestTranslateSymbolKindFilter(t *testing.T) {
	tq := mustTranslate(t, "type:symbol select:symbol.function lang:rust foo")
	if !strings.Contains(tq.SQL, "s.kind =") {
		t.Errorf("sql missing s.kind: %s", tq.SQL)
	}
	if !hasParam(tq.Params, "function") {
		t.Errorf("missing function param: %v", tq.Params)
	}
}

func TestTranslateSymbolNoFiltersError(t *testing.T) {
	q := mustParse(t, "type:symbol")
	_, err := Translate(q)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "requires a search pattern") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestTranslateCallsBasic(t *testing.T) {
	tq := mustTranslate(t, "calls:groupby")
	if tq.SearchType != SearchTypeSymbol {
		t.Errorf("type = %v", tq.SearchType)
	}
	if !strings.Contains(tq.SQL, "symbol_refs sr") {
		t.Errorf("sql missing symbol_refs: %s", tq.SQL)
	}
	if !strings.Contains(tq.SQL, "sr.ref_name LIKE") {
		t.Errorf("sql missing sr.ref_name LIKE: %s", tq.SQL)
	}
	if !strings.Contains(tq.SQL, "sr.kind = 'call'") {
		t.Errorf("sql missing sr.kind call: %s", tq.SQL)
	}
	if !strings.Contains(tq.SQL, "s.kind = 'function'") {
		t.Errorf("sql missing s.kind function: %s", tq.SQL)
	}
	if !hasParam(tq.Params, "%groupby%") {
		t.Errorf("missing groupby param: %v", tq.Params)
	}
}

func TestTranslateCalledByBasic(t *testing.T) {
	tq := mustTranslate(t, "calledby:groupby")
	if tq.SearchType != SearchTypeSymbol {
		t.Errorf("type = %v", tq.SearchType)
	}
	if !strings.Contains(tq.SQL, "FROM symbols s") {
		t.Errorf("sql missing FROM symbols s: %s", tq.SQL)
	}
	if !strings.Contains(tq.SQL, "symbol_refs sr ON sr.symbol_id = s.id") {
		t.Errorf("sql missing join: %s", tq.SQL)
	}
	if !strings.Contains(tq.SQL, "s.name LIKE") {
		t.Errorf("sql missing s.name LIKE: %s", tq.SQL)
	}
	if !hasParam(tq.Params, "%groupby%") {
		t.Errorf("missing groupby param: %v", tq.Params)
	}
}

func TestTranslateCallsOverridesType(t *testing.T) {
	tq := mustTranslate(t, "type:commit calls:foo")
	if tq.SearchType != SearchTypeSymbol {
		t.Errorf("type = %v", tq.SearchType)
	}
	if !strings.Contains(tq.SQL, "symbol_refs sr") {
		t.Errorf("sql missing symbol_refs: %s", tq.SQL)
	}
}

func TestTranslateReturnsBasic(t *testing.T) {
	tq := mustTranslate(t, "returns:BatchIterator")
	if tq.SearchType != SearchTypeSymbol {
		t.Errorf("type = %v", tq.SearchType)
	}
	if !strings.Contains(tq.SQL, "s.return_type LIKE") {
		t.Errorf("sql missing s.return_type LIKE: %s", tq.SQL)
	}
	if !strings.Contains(tq.SQL, "s.kind IN ('function', 'method')") {
		t.Errorf("sql missing kind IN: %s", tq.SQL)
	}
	if !hasParam(tq.Params, "%BatchIterator%") {
		t.Errorf("missing param: %v", tq.Params)
	}
}

func TestTranslateReturnsOverridesType(t *testing.T) {
	tq := mustTranslate(t, "type:commit returns:i32")
	if tq.SearchType != SearchTypeSymbol {
		t.Errorf("type = %v", tq.SearchType)
	}
}

func TestTranslateSymbolCaseSensitive(t *testing.T) {
	tq := mustTranslate(t, "type:symbol case:yes SFrame")
	if !strings.Contains(tq.SQL, "s.name GLOB") {
		t.Errorf("sql missing s.name GLOB: %s", tq.SQL)
	}
	if !hasParam(tq.Params, "*SFrame*") {
		t.Errorf("missing *SFrame* param: %v", tq.Params)
	}
}

func TestTranslateSymbolCaseInsensitive(t *testing.T) {
	tq := mustTranslate(t, "type:symbol case:no SFrame")
	if !strings.Contains(tq.SQL, "s.name LIKE") {
		t.Errorf("sql missing s.name LIKE: %s", tq.SQL)
	}
	if !hasParam(tq.Params, "%SFrame%") {
		t.Errorf("missing %%SFrame%% param: %v", tq.Params)
	}
}

func TestTranslateCodeRegex(t *testing.T) {
	tq := mustTranslate(t, `/err\d+/`)
	if !strings.Contains(tq.SQL, "code_search(?1, 'regex')") {
		t.Errorf("sql missing regex vtab: %s", tq.SQL)
	}
	if tq.Params[0] != `err\d+` {
		t.Errorf("params[0] = %q", tq.Params[0])
	}
}

func TestTranslateDiffRegex(t *testing.T) {
	tq := mustTranslate(t, "type:diff /TODO.*/")
	if !strings.Contains(tq.SQL, "diff_search(?1, 'regex')") {
		t.Errorf("sql missing regex vtab: %s", tq.SQL)
	}
}

func TestTranslateSymbolRegexError(t *testing.T) {
	q := mustParse(t, "type:symbol /foo.*/")
	_, err := Translate(q)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "only supported for code and diff") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestTranslateCodeOR(t *testing.T) {
	tq := mustTranslate(t, "foo OR bar")
	if !strings.Contains(tq.SQL, "code_search(?1)") {
		t.Errorf("sql missing code_search: %s", tq.SQL)
	}
	if tq.Params[0] != "foo OR bar" {
		t.Errorf("params[0] = %q", tq.Params[0])
	}
}

func TestTranslateSymbolOR(t *testing.T) {
	tq := mustTranslate(t, "type:symbol foo OR bar")
	if !strings.Contains(tq.SQL, "(s.name LIKE") {
		t.Errorf("sql missing s.name LIKE: %s", tq.SQL)
	}
	if !strings.Contains(tq.SQL, " OR ") {
		t.Errorf("sql missing OR: %s", tq.SQL)
	}
	if !hasParam(tq.Params, "%foo%") {
		t.Errorf("missing foo param: %v", tq.Params)
	}
	if !hasParam(tq.Params, "%bar%") {
		t.Errorf("missing bar param: %v", tq.Params)
	}
}

func TestTranslateCommitOR(t *testing.T) {
	tq := mustTranslate(t, "type:commit fix OR refactor")
	if !strings.Contains(tq.SQL, "(c.message LIKE") {
		t.Errorf("sql missing c.message LIKE: %s", tq.SQL)
	}
	if !strings.Contains(tq.SQL, " OR ") {
		t.Errorf("sql missing OR: %s", tq.SQL)
	}
}

func TestTranslateNegRepo(t *testing.T) {
	tq := mustTranslate(t, "-repo:unwanted foo")
	if !strings.Contains(tq.SQL, "repos rp") {
		t.Errorf("sql missing repos: %s", tq.SQL)
	}
	if !strings.Contains(tq.SQL, "NOT") {
		t.Errorf("sql missing NOT: %s", tq.SQL)
	}
}

func TestTranslateNegLang(t *testing.T) {
	tq := mustTranslate(t, "-lang:python foo")
	if !strings.Contains(tq.SQL, "b.language !=") {
		t.Errorf("sql missing b.language !=: %s", tq.SQL)
	}
	if !hasParam(tq.Params, "python") {
		t.Errorf("missing python param: %v", tq.Params)
	}
}

func TestTranslateBothRepoAndNegRepo(t *testing.T) {
	tq := mustTranslate(t, "repo:myorg -repo:test foo")
	count := strings.Count(tq.SQL, "repos rp")
	if count != 1 {
		t.Errorf("should have exactly 1 repos join, got %d: %s", count, tq.SQL)
	}
}

func TestTranslateCallsWithLang(t *testing.T) {
	tq := mustTranslate(t, "calls:par_iter lang:rust")
	if !strings.Contains(tq.SQL, "b.language =") {
		t.Errorf("sql missing b.language: %s", tq.SQL)
	}
	if !hasParam(tq.Params, "rust") {
		t.Errorf("missing rust param: %v", tq.Params)
	}
}

func TestTranslateCallsWithFile(t *testing.T) {
	tq := mustTranslate(t, "calls:groupby file:*.rs")
	if !strings.Contains(tq.SQL, "lower(fr.path) GLOB") {
		t.Errorf("sql missing lower GLOB: %s", tq.SQL)
	}
	if !hasParam(tq.Params, "*.rs") {
		t.Errorf("missing *.rs param: %v", tq.Params)
	}
}

func TestTranslateCalledByWithCount(t *testing.T) {
	tq := mustTranslate(t, "calledby:groupby count:10")
	if !strings.Contains(tq.SQL, "LIMIT 10") {
		t.Errorf("sql missing LIMIT 10: %s", tq.SQL)
	}
}

func TestTranslateReturnsWithLang(t *testing.T) {
	tq := mustTranslate(t, "returns:String lang:rust")
	if !strings.Contains(tq.SQL, "s.return_type LIKE") {
		t.Errorf("sql missing return_type: %s", tq.SQL)
	}
	if !strings.Contains(tq.SQL, "b.language =") {
		t.Errorf("sql missing language: %s", tq.SQL)
	}
}

func TestTranslateNegAuthorDiff(t *testing.T) {
	tq := mustTranslate(t, "type:diff -author:bot streaming")
	if !strings.Contains(tq.SQL, "NOT") {
		t.Errorf("sql missing NOT: %s", tq.SQL)
	}
	if !hasParamContaining(tq.Params, "bot") {
		t.Errorf("missing bot param: %v", tq.Params)
	}
}

func TestTranslateNegMessageCommit(t *testing.T) {
	tq := mustTranslate(t, "type:commit -message:WIP author:alice")
	if !strings.Contains(tq.SQL, "NOT") {
		t.Errorf("sql missing NOT: %s", tq.SQL)
	}
	if !hasParamContaining(tq.Params, "WIP") {
		t.Errorf("missing WIP param: %v", tq.Params)
	}
}

func TestTranslateCaseSensitiveWithGlobPattern(t *testing.T) {
	tq := mustTranslate(t, "type:symbol case:yes file:*.RS lang:rust foo")
	if !strings.Contains(tq.SQL, "fr.path GLOB") {
		t.Errorf("sql missing fr.path GLOB: %s", tq.SQL)
	}
	if !hasParam(tq.Params, "*.RS") {
		t.Errorf("missing *.RS param: %v", tq.Params)
	}
}

func TestTranslateCaseInsensitiveWithGlobPattern(t *testing.T) {
	tq := mustTranslate(t, "type:symbol case:no file:*.RS lang:rust foo")
	if !strings.Contains(tq.SQL, "lower(fr.path) GLOB") {
		t.Errorf("sql missing lower GLOB: %s", tq.SQL)
	}
	if !hasParam(tq.Params, "*.rs") {
		t.Errorf("missing *.rs param: %v", tq.Params)
	}
}

func TestTranslateRepoAtRevision(t *testing.T) {
	tq := mustTranslate(t, "repo:myrepo@develop foo")
	if !hasParamContaining(tq.Params, "myrepo") {
		t.Errorf("missing myrepo param: %v", tq.Params)
	}
	if !hasParam(tq.Params, "refs/heads/develop") {
		t.Errorf("missing develop ref: %v", tq.Params)
	}
}
