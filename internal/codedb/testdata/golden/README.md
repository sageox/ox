# Golden Artifacts Corpus — codedb

Reference corpus for behavioral verification during optimization passes.
**Do not regenerate except when an intentional behavior change is made.**

## Purpose

Freezes the exact output of all pure, deterministic codedb functions at the
time of corpus creation. Any optimization that changes these outputs introduces
a regression — the golden tests will fail and force a deliberate review.

## Regenerating

When behavior is *intentionally* changed:

```bash
REGENERATE=1 go test ./internal/codedb/search/ ./internal/codedb/language/ ./internal/codedb/comments/ -run TestGolden
```

Then review the diff before committing. Every golden change is a behavioral
change — it must be intentional.

## Coverage

| Package | Test | Files | What Is Frozen |
|---------|------|-------|----------------|
| `search` | `TestGoldenTokenize` | 14 | Token slices from query string inputs |
| `search` | `TestGoldenParseQuery` | 48 | Full `ParsedQuery` struct (all filter fields, type, regex flag, OR groups) |
| `search` | `TestGoldenTranslate` | 72 | Exact SQL text + bound params for all 9 search types |
| `search` | `TestGoldenPlan` | 25 | `ExecutionPlan` strategy (bleve_only / intersect / sql_only) + SQL + limit |
| `language` | `TestGoldenDetect` | 56 | File extension → language name mapping |
| `comments` | `TestGoldenExtract` | 50 | Extracted `Comment` structs (text, kind, line/col positions) per language |

**Total: 265 golden files**

## Adding New Cases

1. Add the input to the test table in `*_test.go`
2. Run with `REGENERATE=1` to capture the initial output
3. Inspect the new golden file — confirm the output is correct
4. Commit test + golden file together

## Location

```
internal/codedb/search/testdata/golden/
  tokenize/     14 files — tokenize() output
  parse/        48 files — ParseQuery() struct output
  translate/    72 files — Translate() SQL + params output
  plan/         25 files — Plan() ExecutionPlan output

internal/codedb/language/testdata/golden/
  56 files — Detect() language string output

internal/codedb/comments/testdata/golden/
  50 files — Extract() Comment slice output
```
