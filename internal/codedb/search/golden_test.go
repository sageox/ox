package search

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeOrCompareGolden writes got to path when REGENERATE=1, otherwise compares.
func writeOrCompareGolden(t *testing.T, dir, name string, got any) {
	t.Helper()
	path := filepath.Join("testdata", "golden", dir, name+".golden")

	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden %s: %v", name, err)
	}
	data = append(data, '\n')

	if os.Getenv("REGENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v\n  → run: REGENERATE=1 go test ./internal/codedb/search/", path, err)
	}
	if !bytes.Equal(data, want) {
		t.Errorf("golden divergence: %s/%s\ngot:\n%s\nwant:\n%s", dir, name, data, want)
	}
}

// ── tokenize ──────────────────────────────────────────────────────────────────

func TestGoldenTokenize(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"single_word", "foo"},
		{"multiple_words", "foo bar baz"},
		{"quoted_phrase", `lang:rust "foo bar" baz`},
		{"filter_colon", "repo:SFrame file:*.rs fn"},
		{"negated_filter", "-file:test foo"},
		{"leading_trailing_spaces", "  foo   bar  "},
		{"unclosed_quote", `foo "bar baz`},
		{"tab_separator", "foo\tbar"},
		{"only_spaces", "   "},
		{"unicode_words", "α β γ"},
		{"url_token", "http://example.com foo"},
		{"or_keyword", "foo OR bar"},
		{"regex_delimiters", `/err\d+/`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tokenize(tc.input)
			writeOrCompareGolden(t, "tokenize", tc.name, got)
		})
	}
}

// ── ParseQuery ────────────────────────────────────────────────────────────────

func TestGoldenParseQuery(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		// Basics
		{"empty", ""},
		{"bare_keyword", "process_data"},
		{"multi_keyword", "foo bar baz"},
		// Types
		{"type_code_explicit", "type:code foo"},
		{"type_diff", "type:diff streaming"},
		{"type_commit", "type:commit refactor"},
		{"type_symbol", "type:symbol SFrame"},
		{"type_comment", "type:comment TODO"},
		{"type_pr", "type:pr auth migration"},
		{"type_issue", "type:issue memory leak"},
		// Filters
		{"lang_filter", "lang:rust foo"},
		{"lang_alias_l", "l:python foo"},
		{"lang_alias_language", "language:go foo"},
		{"file_filter", "file:*.rs foo"},
		{"file_glob", "file:src/*.go foo"},
		{"repo_filter", "repo:SFrame foo"},
		{"repo_at_rev", "repo:myrepo@develop foo"},
		{"rev_filter", "rev:develop foo"},
		{"count_filter", "count:50 foo"},
		{"case_yes", "case:yes SFrame"},
		{"case_no", "case:no SFrame"},
		{"author_filter", "type:diff author:person-a streaming"},
		{"before_after", "type:commit before:2026-01-01 after:2025-06-01 refactor"},
		{"message_filter", "type:commit message:fix"},
		{"select_file", "select:file foo"},
		{"select_repo", "select:repo foo"},
		{"select_symbol", "select:symbol foo"},
		{"select_symbol_kind", "type:symbol select:symbol.function foo"},
		{"calls_filter", "calls:groupby"},
		{"calledby_filter", "calledby:groupby"},
		{"returns_filter", "returns:BatchIterator"},
		{"comment_kind", "type:comment ckind:doc"},
		{"state_filter", "type:pr state:open"},
		// Negations
		{"neg_file", "-file:test foo"},
		{"neg_repo", "-repo:unwanted foo"},
		{"neg_lang", "-lang:python foo"},
		{"neg_author", "type:diff -author:bot streaming"},
		{"neg_message", "type:commit -message:WIP author:person-a"},
		// Regex
		{"regex_basic", `/err\d+/`},
		{"regex_with_filter", `lang:rust /fn\s+\w+/`},
		{"patterntype_regexp", `patterntype:regexp foo`},
		// OR
		{"or_two_terms", "foo OR bar"},
		{"or_three_terms", "foo OR bar OR baz"},
		{"or_multiword", "error handling OR panic recovery"},
		// Quoted phrases
		{"quoted_phrase", `lang:rust "foo bar"`},
		{"message_quoted", `type:commit message:"fix bug"`},
		// Edge cases
		{"unknown_filter_as_term", "http://example.com foo"},
		{"combined_repo_rev", "repo:myrepo@develop foo"},
		{"combined_all_filters", "lang:go file:*.go repo:main rev:main case:yes count:5 foo"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseQuery(tc.input)
			if err != nil {
				t.Fatalf("ParseQuery(%q): %v", tc.input, err)
			}
			writeOrCompareGolden(t, "parse", tc.name, got)
		})
	}

	// Error cases: queries that must return a parse error.
	errorCases := []struct {
		name  string
		input string
	}{
		{"invalid_type", "type:unknown_type foo"},
		{"invalid_count", "count:abc foo"},
	}
	for _, tc := range errorCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseQuery(tc.input)
			if err == nil {
				t.Fatalf("ParseQuery(%q): expected error, got nil", tc.input)
			}
		})
	}
}

// ── Translate ─────────────────────────────────────────────────────────────────

func TestGoldenTranslate(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		// Code search
		{"code_basic", "process_data"},
		{"code_with_lang", "lang:rust foo"},
		{"code_with_file_glob", "file:*.rs foo"},
		{"code_with_file_substr", "file:csv foo"},
		{"code_with_repo", "repo:SFrame foo"},
		{"code_with_neg_repo", "-repo:unwanted foo"},
		{"code_both_repo_and_neg_repo", "repo:myorg -repo:test foo"},
		{"code_with_neg_file", "-file:test foo"},
		{"code_with_neg_lang", "-lang:python foo"},
		{"code_with_rev", "rev:develop foo"},
		{"code_select_file", "select:file foo"},
		{"code_select_repo", "select:repo foo"},
		{"code_with_count", "count:5 foo"},
		{"code_case_sensitive", "case:yes SFrame"},
		{"code_case_bracket_escape", "type:symbol case:yes Array[Int]"},
		{"code_with_or", "foo OR bar"},
		{"code_regex", `/err\d+/`},
		{"code_patterntype_regexp", "patterntype:regexp foo"},
		{"code_repo_at_rev", "repo:myrepo@develop foo"},
		{"code_all_filters", "lang:go file:src/*.go repo:main -repo:test rev:main case:no count:10 foo"},
		// Diff search
		{"diff_basic", "type:diff streaming"},
		{"diff_with_author", "type:diff author:person-a streaming"},
		{"diff_with_neg_author", "type:diff -author:bot streaming"},
		{"diff_with_dates", "type:diff before:2026-01-01 after:2025-06-01 streaming"},
		{"diff_select_file", "type:diff select:file streaming"},
		{"diff_regex", "type:diff /TODO.*/"},
		{"diff_with_file", "type:diff file:*.rs streaming"},
		// Commit search
		{"commit_basic", "type:commit refactor"},
		{"commit_author_only", "type:commit author:person-a"},
		{"commit_with_author", "type:commit author:person-a refactor"},
		{"commit_with_message", "type:commit message:fix"},
		{"commit_with_quoted_message", `type:commit message:"fix bug"`},
		{"commit_with_neg_message", "type:commit -message:WIP author:person-a"},
		{"commit_with_dates", "type:commit before:2026-01-01 after:2025-06-01 refactor"},
		{"commit_with_or", "type:commit fix OR refactor"},
		{"commit_with_repo", "type:commit repo:myrepo refactor"},
		// Symbol search
		{"symbol_basic", "type:symbol process_data"},
		{"symbol_with_lang", "type:symbol lang:rust SFrame"},
		{"symbol_with_file", "type:symbol file:*.rs foo"},
		{"symbol_select_function", "type:symbol select:symbol.function lang:rust foo"},
		{"symbol_case_sensitive", "type:symbol case:yes SFrame"},
		{"symbol_case_insensitive", "type:symbol case:no SFrame"},
		{"symbol_with_or", "type:symbol foo OR bar"},
		{"symbol_case_sensitive_file_glob", "type:symbol case:yes file:*.RS lang:rust foo"},
		{"symbol_case_insensitive_file_glob", "type:symbol case:no file:*.RS lang:rust foo"},
		// Returns (overrides type)
		{"returns_basic", "returns:BatchIterator"},
		{"returns_with_lang", "returns:String lang:rust"},
		{"returns_overrides_type", "type:commit returns:i32"},
		// Calls
		{"calls_basic", "calls:groupby"},
		{"calls_with_lang", "calls:par_iter lang:rust"},
		{"calls_with_file", "calls:groupby file:*.rs"},
		{"calls_overrides_type", "type:commit calls:foo"},
		// Calledby
		{"calledby_basic", "calledby:groupby"},
		{"calledby_with_count", "calledby:groupby count:10"},
		{"calledby_with_lang", "calledby:groupby lang:go"},
		// Comment search
		{"comment_basic", "type:comment TODO"},
		{"comment_ckind_doc", "type:comment ckind:doc"},
		{"comment_with_lang", "type:comment lang:go TODO"},
		{"comment_with_file", "type:comment file:*.go TODO"},
		// PR search
		{"pr_basic", "type:pr auth migration"},
		{"pr_with_author", "type:pr author:person-a fix"},
		{"pr_with_state_open", "type:pr state:open"},
		{"pr_with_state_closed", "type:pr state:closed"},
		{"pr_empty_pattern_with_state", "type:pr state:open"},
		{"pr_with_count", "type:pr count:5 state:open"},
		{"pr_with_dates", "type:pr before:2026-01-01 after:2025-06-01 fix"},
		{"pr_with_or", "type:pr auth OR migration"},
		// Issue search
		{"issue_basic", "type:issue memory leak"},
		{"issue_with_state", "type:issue state:closed"},
		{"issue_with_author", "type:issue author:person-b bug"},
		{"issue_empty_pattern_with_state", "type:issue state:open"},
		{"issue_with_dates", "type:issue before:2026-01-01 after:2025-06-01 bug"},
		{"issue_with_or", "type:issue bug OR crash"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := ParseQuery(tc.input)
			if err != nil {
				t.Fatalf("ParseQuery(%q): %v", tc.input, err)
			}
			got, err := Translate(q)
			if err != nil {
				t.Fatalf("Translate(%q): %v", tc.input, err)
			}
			writeOrCompareGolden(t, "translate", tc.name, got)
		})
	}
}

// ── Plan ──────────────────────────────────────────────────────────────────────

func TestGoldenPlan(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		// Code: BleveOnly (no metadata filters)
		{"code_bleve_only", "foo"},
		{"code_bleve_only_multiword", "foo bar"},
		{"code_bleve_only_regex", `/err\d+/`},
		// Code: Intersect (metadata filters present)
		{"code_intersect_lang", "lang:go foo"},
		{"code_intersect_file", "file:*.rs foo"},
		{"code_intersect_repo", "repo:myrepo foo"},
		{"code_intersect_neg_lang", "-lang:python foo"},
		{"code_intersect_rev", "rev:develop foo"},
		{"code_intersect_select_file", "select:file foo"},
		// Diff: BleveOnly
		{"diff_bleve_only", "type:diff streaming"},
		// Diff: Intersect
		{"diff_intersect_author", "type:diff author:person-a streaming"},
		{"diff_intersect_file", "type:diff file:*.rs streaming"},
		{"diff_intersect_dates", "type:diff before:2026-01-01 streaming"},
		// SQL-only strategies
		{"commit_sql_only", "type:commit refactor"},
		{"symbol_sql_only", "type:symbol SFrame"},
		{"pr_sql_only", "type:pr fix"},
		{"issue_sql_only", "type:issue bug"},
		{"calls_sql_only", "calls:groupby"},
		{"calledby_sql_only", "calledby:groupby"},
		{"returns_sql_only", "returns:BatchIterator"},
		// Comment: BleveOnly
		{"comment_bleve_only", "type:comment TODO"},
		// Comment: Intersect (has metadata filter)
		{"comment_intersect_lang", "type:comment lang:go TODO"},
		{"comment_intersect_ckind", "type:comment ckind:doc"},
		// Limit propagation
		{"code_custom_limit", "count:5 foo"},
		{"default_limit", "foo"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := ParseQuery(tc.input)
			if err != nil {
				t.Fatalf("ParseQuery(%q): %v", tc.input, err)
			}
			got, err := Plan(q)
			if err != nil {
				t.Fatalf("Plan(%q): %v", tc.input, err)
			}
			writeOrCompareGolden(t, "plan", tc.name, got)
		})
	}
}
