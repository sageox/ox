package comments

import (
	"testing"
)

func TestExtractCFamily(t *testing.T) {
	tests := []struct {
		name   string
		lang   string
		source string
		want   []Comment
	}{
		{
			name:   "line comment",
			lang:   "go",
			source: "x := 1 // set x\n",
			want: []Comment{
				{Text: "set x", Kind: "line", Line: 1, EndLine: 1, Col: 8, EndCol: 16},
			},
		},
		{
			name:   "block comment",
			lang:   "go",
			source: "/* hello world */\n",
			want: []Comment{
				{Text: "hello world", Kind: "block", Line: 1, EndLine: 1, Col: 1, EndCol: 18},
			},
		},
		{
			name:   "multiline block comment",
			lang:   "java",
			source: "/* line one\n   line two */\n",
			want: []Comment{
				{Text: "line one\n   line two", Kind: "block", Line: 1, EndLine: 2, Col: 1, EndCol: 15},
			},
		},
		{
			name:   "doc comment block",
			lang:   "java",
			source: "/** JavaDoc */\n",
			want: []Comment{
				{Text: "* JavaDoc", Kind: "doc", Line: 1, EndLine: 1, Col: 1, EndCol: 15},
			},
		},
		{
			name:   "doc comment line",
			lang:   "rust",
			source: "/// doc line\n",
			want: []Comment{
				{Text: "/ doc line", Kind: "doc", Line: 1, EndLine: 1, Col: 1, EndCol: 13},
			},
		},
		{
			name:   "multiple comments",
			lang:   "go",
			source: "// first\n// second\n",
			want: []Comment{
				{Text: "first", Kind: "line", Line: 1, EndLine: 1, Col: 1, EndCol: 9},
				{Text: "second", Kind: "line", Line: 2, EndLine: 2, Col: 1, EndCol: 10},
			},
		},
		{
			name:   "comment after code",
			lang:   "go",
			source: "x := 1\n// why: because reasons\ny := 2\n",
			want: []Comment{
				{Text: "why: because reasons", Kind: "line", Line: 2, EndLine: 2, Col: 1, EndCol: 24},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Extract(tt.source, tt.lang)
			assertComments(t, got, tt.want)
		})
	}
}

func TestExtractHashFamily(t *testing.T) {
	tests := []struct {
		name   string
		lang   string
		source string
		want   []Comment
	}{
		{
			name:   "python line comment",
			lang:   "python",
			source: "x = 1  # set x\n",
			want: []Comment{
				{Text: "set x", Kind: "line", Line: 1, EndLine: 1, Col: 8, EndCol: 15},
			},
		},
		{
			name:   "python doc comment",
			lang:   "python",
			source: "## Module docs\n",
			want: []Comment{
				{Text: "# Module docs", Kind: "doc", Line: 1, EndLine: 1, Col: 1, EndCol: 15},
			},
		},
		{
			name:   "ruby comment",
			lang:   "ruby",
			source: "# frozen_string_literal: true\n",
			want: []Comment{
				{Text: "frozen_string_literal: true", Kind: "line", Line: 1, EndLine: 1, Col: 1, EndCol: 30},
			},
		},
		{
			name:   "shell comment",
			lang:   "shell",
			source: "#!/bin/bash\n# configure\n",
			want: []Comment{
				{Text: "!/bin/bash", Kind: "line", Line: 1, EndLine: 1, Col: 1, EndCol: 12},
				{Text: "configure", Kind: "line", Line: 2, EndLine: 2, Col: 1, EndCol: 12},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Extract(tt.source, tt.lang)
			assertComments(t, got, tt.want)
		})
	}
}

func TestExtractHTMLFamily(t *testing.T) {
	tests := []struct {
		name   string
		lang   string
		source string
		want   []Comment
	}{
		{
			name:   "html comment",
			lang:   "html",
			source: "<!-- header -->\n<div></div>\n",
			want: []Comment{
				{Text: "header", Kind: "block", Line: 1, EndLine: 1, Col: 1, EndCol: 16},
			},
		},
		{
			name:   "multiline html comment",
			lang:   "xml",
			source: "<!-- first\n     second -->\n",
			want: []Comment{
				{Text: "first\n     second", Kind: "block", Line: 1, EndLine: 2, Col: 1, EndCol: 16},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Extract(tt.source, tt.lang)
			assertComments(t, got, tt.want)
		})
	}
}

func TestExtractSQLFamily(t *testing.T) {
	got := Extract("-- select all\nSELECT * /* inline */ FROM t;\n", "sql")
	assertComments(t, got, []Comment{
		{Text: "select all", Kind: "line", Line: 1, EndLine: 1, Col: 1, EndCol: 14},
		{Text: "inline", Kind: "block", Line: 2, EndLine: 2, Col: 10, EndCol: 22},
	})
}

func TestExtractLuaFamily(t *testing.T) {
	got := Extract("-- single\n--[[ block\ncomment ]]\nx = 1\n", "lua")
	assertComments(t, got, []Comment{
		{Text: "single", Kind: "line", Line: 1, EndLine: 1, Col: 1, EndCol: 10},
		{Text: "block\ncomment", Kind: "block", Line: 2, EndLine: 3, Col: 1, EndCol: 11},
	})
}

func TestExtractHaskellNested(t *testing.T) {
	got := Extract("{- outer {- inner -} still outer -}\n", "haskell")
	assertComments(t, got, []Comment{
		{Text: "outer {- inner -} still outer", Kind: "block", Line: 1, EndLine: 1, Col: 1, EndCol: 36},
	})
}

func TestExtractOCaml(t *testing.T) {
	got := Extract("(* ocaml comment *)\nlet x = 1\n", "ocaml")
	assertComments(t, got, []Comment{
		{Text: "ocaml comment", Kind: "block", Line: 1, EndLine: 1, Col: 1, EndCol: 20},
	})
}

func TestExtractErlang(t *testing.T) {
	got := Extract("% erlang comment\n-module(test).\n", "erlang")
	assertComments(t, got, []Comment{
		{Text: "erlang comment", Kind: "line", Line: 1, EndLine: 1, Col: 1, EndCol: 17},
	})
}

func TestStringLiteralAvoidance(t *testing.T) {
	tests := []struct {
		name   string
		lang   string
		source string
	}{
		{
			name:   "double-quoted string with //",
			lang:   "go",
			source: "s := \"http://example.com\"\n",
		},
		{
			name:   "single-quoted string with #",
			lang:   "python",
			source: "s = 'not # a comment'\n",
		},
		{
			name:   "backtick string with //",
			lang:   "go",
			source: "s := `// not a comment`\n",
		},
		{
			name:   "triple-double-quoted string with #",
			lang:   "python",
			source: "s = \"\"\"contains # hash\"\"\"\n",
		},
		{
			name:   "triple-single-quoted string with #",
			lang:   "python",
			source: "s = '''contains # hash'''\n",
		},
		{
			name:   "template literal with //",
			lang:   "javascript",
			source: "s = `url is // foo`\n",
		},
		{
			name:   "escaped quote in string",
			lang:   "go",
			source: "s := \"she said \\\"// hello\\\"\"\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Extract(tt.source, tt.lang)
			if len(got) != 0 {
				t.Errorf("expected no comments, got %d: %+v", len(got), got)
			}
		})
	}
}

func TestEdgeCases(t *testing.T) {
	t.Run("EOF without newline", func(t *testing.T) {
		got := Extract("// no newline", "go")
		if len(got) != 1 {
			t.Fatalf("expected 1 comment, got %d", len(got))
		}
		if got[0].Text != "no newline" {
			t.Errorf("text = %q, want %q", got[0].Text, "no newline")
		}
	})

	t.Run("empty comment", func(t *testing.T) {
		got := Extract("//\n", "go")
		if len(got) != 0 {
			t.Errorf("expected 0 comments for empty //, got %d", len(got))
		}
	})

	t.Run("unsupported language", func(t *testing.T) {
		got := Extract("{\"key\": \"value\"}", "json")
		if got != nil {
			t.Errorf("expected nil for json, got %+v", got)
		}
	})

	t.Run("empty source", func(t *testing.T) {
		got := Extract("", "go")
		if len(got) != 0 {
			t.Errorf("expected 0 comments, got %d", len(got))
		}
	})

	t.Run("comment with only whitespace", func(t *testing.T) {
		got := Extract("//   \n", "go")
		if len(got) != 0 {
			t.Errorf("expected 0 comments for whitespace-only, got %d", len(got))
		}
	})
}

func TestUnsupportedLanguage(t *testing.T) {
	got := Extract("some content", "unknown_lang")
	if got != nil {
		t.Errorf("expected nil for unknown language, got %+v", got)
	}
}

func TestSupportedLanguages(t *testing.T) {
	langs := SupportedLanguages()
	if len(langs) == 0 {
		t.Fatal("SupportedLanguages returned empty")
	}
	// spot check a few
	found := map[string]bool{}
	for _, l := range langs {
		found[l] = true
	}
	for _, want := range []string{"go", "python", "rust", "html", "sql"} {
		if !found[want] {
			t.Errorf("missing language: %s", want)
		}
	}
}

func assertComments(t *testing.T, got, want []Comment) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("comment count: got %d, want %d\ngot:  %+v\nwant: %+v", len(got), len(want), got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Text != w.Text {
			t.Errorf("[%d] text: got %q, want %q", i, g.Text, w.Text)
		}
		if g.Kind != w.Kind {
			t.Errorf("[%d] kind: got %q, want %q", i, g.Kind, w.Kind)
		}
		if g.Line != w.Line {
			t.Errorf("[%d] line: got %d, want %d", i, g.Line, w.Line)
		}
		if g.EndLine != w.EndLine {
			t.Errorf("[%d] end_line: got %d, want %d", i, g.EndLine, w.EndLine)
		}
		if g.Col != w.Col {
			t.Errorf("[%d] col: got %d, want %d", i, g.Col, w.Col)
		}
		if g.EndCol != w.EndCol {
			t.Errorf("[%d] end_col: got %d, want %d", i, g.EndCol, w.EndCol)
		}
	}
}
