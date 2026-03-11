<!-- doc-audience: ai -->

# Adding a New Language to codedb

How to add support for a new programming language in codedb's indexing and search pipeline.

## Overview

Language support spans three layers, each building on the previous:

```
1. Language Detection  (required)     extension -> language name
2. Comment Extraction  (recommended)  language -> comment syntax family
3. Symbol Extraction   (future)       language -> AST symbols (requires tree-sitter/CGO)
```

Adding a language to layer 1 is always the first step. Layers 2 and 3 are independent of each other.

## Step 1: Language Detection

**File:** `internal/codedb/language/detect.go`

Add a case to the `switch ext` block mapping file extension(s) to a canonical language name.

**Rules:**
- Language name must be lowercase, single word (e.g., `"rust"`, `"python"`, `"csharp"`)
- Multiple extensions can map to the same language (e.g., `"cpp", "cc", "cxx"` all return `"cpp"`)
- Return `""` for unrecognized extensions (the default case handles this)
- Extensions are already lowercased and dot-stripped before the switch

**Example — adding Nim:**
```go
case "nim", "nims":
    return "nim"
```

**Test:** `internal/codedb/language/detect_test.go`

Add entries to the table-driven test:
```go
{"main.nim", "nim"},
{"config.nims", "nim"},
```

Run: `go test ./internal/codedb/language/...`

## Step 2: Comment Extraction

**Package:** `internal/codedb/comments/`

Comment extraction uses a character-level scanner configured by "syntax families" — groups of languages that share the same comment delimiters.

### 2a: Check if an existing family covers the language

Most languages fit an existing family:

| Family | Line prefix | Block open/close | Languages |
|--------|------------|------------------|-----------|
| C-family | `//` | `/* */` | go, c, cpp, java, javascript, typescript, tsx, jsx, rust, swift, kotlin, scala, csharp, dart, php, zig, protobuf, css |
| Hash | `#` | (none) | python, ruby, shell, r, perl, elixir, yaml, toml |
| HTML | (none) | `<!-- -->` | html, xml, markdown |
| SQL | `--` | `/* */` | sql |
| Lua | `--` | `--[[ ]]` | lua |
| Haskell | `--` | `{- -}` | haskell |
| OCaml | (none) | `(* *)` | ocaml |
| Erlang | `%` | (none) | erlang |

If the new language uses the same comment syntax as an existing family, just add it to that family's language list.

**File:** `internal/codedb/comments/families.go`

**Example — Nim uses `#` line comments and `#[ ]#` block comments:**

Nim doesn't exactly match the hash family (different block syntax). Two options:

1. **If block comments aren't important yet**, add `"nim"` to the hash family's language list (line comments will work; block comments won't be extracted).
2. **If block comments matter**, create a new family.

### 2b: Create a new comment family (if needed)

Define a new `Family` in `families.go`:

```go
var nimFamily = Family{
    LinePrefix: "#",
    BlockOpen:  "#[",
    BlockClose: "]#",
}
```

Register it in the `FamilyForLanguage` function:

```go
case "nim":
    return &nimFamily
```

### 2c: String literal handling

The comment scanner must skip string literals to avoid false positives. If the new language has unusual string syntax (e.g., raw strings, heredocs, interpolated strings), the scanner's string-tracking state machine may need updating.

Common string syntaxes already handled:
- Double quotes `"..."` with `\` escape (most languages)
- Single quotes `'...'` (most languages)
- Backtick strings `` `...` `` (Go, JavaScript)
- Triple-quoted strings `"""..."""` / `'''...'''` (Python)
- Template literals `` `...${expr}...` `` (JavaScript/TypeScript)

If the language has a string syntax not in this list, add handling to the scanner in `extract.go`.

### 2d: Test

**File:** `internal/codedb/comments/extract_test.go`

Add table-driven test cases:

```go
{
    name: "nim line comment",
    lang: "nim",
    source: "let x = 1 # set x\nlet y = 2\n",
    want: []Comment{
        {Text: "set x", Kind: "line", Line: 1, Col: 13},
    },
},
{
    name: "nim block comment",
    lang: "nim",
    source: "#[ multi\nline ]#\nlet x = 1\n",
    want: []Comment{
        {Text: "multi\nline", Kind: "block", Line: 1, EndLine: 2, Col: 1},
    },
},
{
    name: "nim string not a comment",
    lang: "nim",
    source: `let s = "# not a comment"` + "\n",
    want:   nil,
},
```

Run: `go test ./internal/codedb/comments/...`

## Step 3: Symbol Extraction (future)

Symbol extraction requires tree-sitter (CGO), which is currently disabled. The interface is defined but stubbed.

**When tree-sitter is enabled**, adding a language requires:

1. Add the tree-sitter grammar dependency to `go.mod`
2. Write tree-sitter query files (`.scm`) in `internal/codedb/symbols/queries/<language>.scm`
3. Register the language parser in `symbols.go`
4. Add `"<language>"` to `SupportedLanguages()`

This is not yet actionable. The stub is at `internal/codedb/symbols/symbols_stub.go`.

## Checklist

```
[ ] Extension(s) added to language/detect.go
[ ] Test case(s) added to language/detect_test.go
[ ] Comment family assigned or created in comments/families.go
[ ] String literal edge cases considered for scanner
[ ] Comment extraction tests added to comments/extract_test.go
[ ] go test ./internal/codedb/... passes
[ ] make lint passes
```

## Files Reference

| File | Purpose |
|------|---------|
| `internal/codedb/language/detect.go` | Extension-to-language mapping |
| `internal/codedb/language/detect_test.go` | Detection tests |
| `internal/codedb/comments/families.go` | Comment syntax families |
| `internal/codedb/comments/extract.go` | Comment scanner |
| `internal/codedb/comments/extract_test.go` | Extraction tests |
| `internal/codedb/symbols/symbols.go` | Symbol types (future) |
| `internal/codedb/symbols/symbols_stub.go` | Current no-op stub |
