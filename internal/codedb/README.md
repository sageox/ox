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

```
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

```
ox codedb index <url>          # clone + index a repo
ox codedb search <query>       # search indexed code
ox codedb sql <sql>            # raw SQL against the DB
```

Data lives in `~/.local/share/sageox/codedb/` (XDG).
