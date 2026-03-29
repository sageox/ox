package comments

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeOrCompareGolden(t *testing.T, name string, got []Comment) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".golden")

	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden %s: %v", name, err)
	}
	data = append(data, '\n')

	if os.Getenv("REGENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v\n  → run: REGENERATE=1 go test ./internal/codedb/comments/", path, err)
	}
	if !bytes.Equal(data, want) {
		t.Errorf("golden divergence: %s\ngot:\n%s\nwant:\n%s", name, data, want)
	}
}

func TestGoldenExtract(t *testing.T) {
	cases := []struct {
		name   string
		lang   string
		source string
	}{
		// Unsupported / empty
		{
			name:   "unsupported_language",
			lang:   "json",
			source: `{"key": "value"}`,
		},
		{
			name:   "unknown_language",
			lang:   "brainfuck",
			source: `++ [ - ]`,
		},
		{
			name:   "empty_source",
			lang:   "go",
			source: "",
		},
		{
			name:   "only_whitespace",
			lang:   "go",
			source: "   \n\t\n   ",
		},

		// Go — c-family
		{
			name: "go_line_comment",
			lang: "go",
			source: `package main

// doThing performs the operation.
func doThing() {}
`,
		},
		{
			name: "go_doc_comment_triple_slash",
			lang: "go",
			source: `// Package util provides utilities.
//
/// EnhancedDoc is a doc comment.
func EnhancedDoc() {}
`,
		},
		{
			name: "go_block_comment",
			lang: "go",
			source: `/* this is a
multi-line block comment */
var x = 1
`,
		},
		{
			name: "go_doc_block_comment",
			lang: "go",
			source: `/** This is a JavaDoc-style doc comment */
func doThing() {}
`,
		},
		{
			name: "go_comment_inside_string_not_extracted",
			lang: "go",
			source: `var s = "this // is not a comment"
// this is a comment
`,
		},
		{
			name: "go_comment_inside_backtick_not_extracted",
			lang: "go",
			source: "var s = `this /* is not */ a comment`\n// this is a comment\n",
		},
		{
			name: "go_multiple_comments",
			lang: "go",
			source: `// first comment
// second comment
// third comment
`,
		},
		{
			name: "go_eof_no_newline",
			lang: "go",
			source: `// comment at end of file with no newline`,
		},
		{
			name: "go_empty_line_comment_ignored",
			lang: "go",
			source: `//
// non-empty
//
`,
		},
		{
			name: "go_unicode_comment",
			lang: "go",
			source: `// Привет мир — Unicode comment
func hello() {}
`,
		},
		{
			name: "go_interleaved_code_and_comments",
			lang: "go",
			source: `package main

// Package comment
import "fmt"

// main runs the program.
func main() {
	// Print greeting
	fmt.Println("hello")
}
`,
		},

		// Python — hash-family
		{
			name: "python_hash_comment",
			lang: "python",
			source: `# regular comment
x = 1
`,
		},
		{
			name: "python_hash_doc_comment",
			lang: "python",
			source: `## This is a doc comment in hash style
x = 1
`,
		},
		{
			name: "python_triple_double_docstring",
			lang: "python",
			source: `def foo():
    """This is a docstring."""
    pass
`,
		},
		{
			name: "python_triple_single_docstring",
			lang: "python",
			source: `def bar():
    '''Another docstring.'''
    return 42
`,
		},
		{
			name: "python_comment_in_string_not_extracted",
			lang: "python",
			source: `x = "# not a comment"
# real comment
`,
		},
		{
			name: "python_multiline_triple_docstring",
			lang: "python",
			source: `def baz():
    """
    Multi-line docstring.
    Spans several lines.
    """
    pass
# trailing comment
`,
		},
		{
			name: "python_eof_no_newline",
			lang: "python",
			source: `# comment without trailing newline`,
		},

		// JavaScript/TypeScript — c-family
		{
			name: "js_line_comment",
			lang: "javascript",
			source: `// single line comment
const x = 1;
`,
		},
		{
			name: "js_block_comment",
			lang: "javascript",
			source: `/* block comment */
const y = 2;
`,
		},
		{
			name: "js_jsdoc_comment",
			lang: "javascript",
			source: `/** @param {string} name */
function greet(name) {}
`,
		},
		{
			name: "ts_line_doc_comment",
			lang: "typescript",
			source: `/// <reference types="node" />
const x: number = 1;
`,
		},
		{
			name: "js_comment_in_template_literal_not_extracted",
			lang: "javascript",
			source: "const s = `// not a comment`;\n// real comment\n",
		},

		// Rust — c-family
		{
			name: "rust_line_comment",
			lang: "rust",
			source: `// A Rust comment
fn main() {}
`,
		},
		{
			name: "rust_doc_comment",
			lang: "rust",
			source: `/// Documentation comment
pub fn process() {}
`,
		},
		{
			name: "rust_block_comment",
			lang: "rust",
			source: `/* block comment */
let x = 1;
`,
		},

		// SQL — sql-family (-- line, /* block */)
		{
			name: "sql_line_comment",
			lang: "sql",
			source: `-- this is a SQL comment
SELECT 1;
`,
		},
		{
			name: "sql_block_comment",
			lang: "sql",
			source: `/* SQL block comment */
SELECT 2;
`,
		},

		// Haskell — nested block comments
		{
			name: "haskell_line_comment",
			lang: "haskell",
			source: `-- Haskell line comment
main = putStrLn "hello"
`,
		},
		{
			name: "haskell_block_comment",
			lang: "haskell",
			source: `{- block comment -}
main = putStrLn "hello"
`,
		},
		{
			name: "haskell_nested_block_comment",
			lang: "haskell",
			source: `{- outer {- inner -} still outer -}
main = return ()
`,
		},

		// OCaml — (* *) block only
		{
			name: "ocaml_block_comment",
			lang: "ocaml",
			source: `(* OCaml block comment *)
let x = 1
`,
		},

		// Erlang — % line only
		{
			name: "erlang_line_comment",
			lang: "erlang",
			source: `% Erlang comment
main() -> ok.
`,
		},

		// HTML/XML — <!-- --> only
		{
			name: "html_block_comment",
			lang: "html",
			source: `<!-- HTML comment -->
<div>content</div>
`,
		},
		{
			name: "xml_block_comment",
			lang: "xml",
			source: `<!-- XML comment -->
<root/>
`,
		},

		// Shell — hash-family
		{
			name: "shell_comment",
			lang: "shell",
			source: `#!/bin/bash
# Shell comment
echo hello
`,
		},

		// Ruby — hash-family
		{
			name: "ruby_comment",
			lang: "ruby",
			source: `# Ruby comment
def foo; end
`,
		},

		// Edge: escaped chars in string
		{
			name: "go_escaped_quote_in_string",
			lang: "go",
			source: `var s = "he said \"hello\" // not a comment"
// real comment
`,
		},

		// Edge: comment on last line, file ends with newline
		{
			name: "go_comment_last_line_with_newline",
			lang: "go",
			source: `func main() {}
// final comment
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Extract(tc.source, tc.lang)
			writeOrCompareGolden(t, tc.name, got)
		})
	}
}
