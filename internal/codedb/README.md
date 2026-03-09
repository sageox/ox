# codedb

Local code search engine integrated into ox. Indexes git repositories into SQLite + Bleve and supports Sourcegraph-style queries.

## Packages

- **codedb** — Facade (`Open`, `Search`, `RawSQL`, `IndexRepo`)
- **index** — Git clone/fetch via go-git, commit walking, blob dedup, Bleve indexing
- **search** — Query parser, execution planner (SQL/Bleve/Intersect), SQL translator
- **store** — SQLite + Bleve storage layer (schema, migrations, convenience methods)
- **symbols** — Symbol/ref types and parser interface (stub — no CGO tree-sitter)
- **language** — File extension → language detection

## Query syntax

```text
spawn                          # bare text search
lang:rust file:*.rs fn         # filters
type:symbol Runtime            # symbol search
type:commit author:alice       # commit search
type:diff streaming            # diff search
calls:groupby                  # call graph
/err\d+/                       # regex
foo OR bar                     # disjunction
```

## CLI

```bash
ox codedb index <url>          # clone + index a repo
ox codedb search <query>       # search indexed code
ox codedb sql <sql>            # raw SQL against the DB
```

Data lives in `~/.local/share/sageox/codedb/` (XDG).

## Benchmarks

Run with `bash internal/codedb/bench.sh`. Only the default branch (main/master) is indexed.

| Metric | sageox/ox | ylow/SFrameRust | tokio-rs/tokio |
|---|---|---|---|
| Commits | 118 | 195 | 4,415 |
| Blobs | 1,729 | 662 | 18,689 |
| **Index (s)** | 9 | 4 | 82 |
| **Re-index (s)** | 1 | 0 | 49* |
| SQLite DB (MB) | 1 | 1 | 7 |
| Bleve FTS (MB) | 89 | 55 | 629 |
| Git repos (MB) | 7 | 3 | 20 |
| Total (MB) | 96 | 57 | 655 |
| Search latency (ms) | ~68 | ~76 | ~68 |

\* tokio re-index time is dominated by `git fetch` network I/O (48.8s); actual re-index work is 0ms.

Search queries tested: `spawn`, `type:symbol Runtime`, `lang:go func`, `type:diff streaming`, `file:main.go func` (median of 3 iterations).
