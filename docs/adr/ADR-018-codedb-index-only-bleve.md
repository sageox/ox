# ADR-018: Index-Only Bleve — Drop Stored Content & Highlighting in CodeDB

**Status**: Proposed (needs Ryan — changes data-access ergonomics)
**Date**: 2026-05-25

## Context

CodeDB's on-disk and runtime memory footprint is dominated by Bleve. Real
measurements:

| Repo (ledger cache) | bleve | sqlite | total codedb |
|---|---|---|---|
| largest (`repo_019c6d2e…`) | **2.0 GB** | 156 MB | 3.0 GB |
| `repo_019d56e0…` | 529 MB | 67 MB | 1.9 GB (+1.3 GB ledger sub-store) |
| ox repo (fresh index) | 438 MB | 56 MB | 534 MB |

A heap profile of a fresh ox-repo index (harness:
`internal/codedb/index/memory_profile_test.go`, build tag `memprofile`) shows the
parse phases peak at **1.1–1.5 GB** live heap, and the process retains a
**~1.67 GB `heapSys`** high-water afterward.

**Why Bleve is so large:** every Bleve index (code/diff/comment) uses the default
`bleve.NewIndexMapping()`, whose text fields have `Store=true`. So the **full
source text, full diff text, and full comment text are stored inside Bleve** —
in addition to living in git (code), SQLite (`comments.text`), or being
regenerable (diffs). The stored copy exists for exactly one purpose: producing
the ANSI **highlight fragment** that becomes a search result's snippet.

**But CodeDB's consumers are AI coworkers, not humans.** The agent-facing search
path already throws the highlighting away:

```go
// cmd/ox/code.go:204
snippet := stripANSIEscapes(r.Content)   // generate ANSI … then strip it
snippet = compactSnippet(snippet, 120)   // plain ≤120-char snippet is all agents get
```

So we pay (a) the compute to highlight on every search and (b) ~74% of the Bleve
index to store content — to produce an ANSI string we immediately discard.

**Measured savings** (`TestBleveStoredContentCost`, 1707 ox `.go` files):

```
stored (default)            = 39.9 MB
index-only (Store=false, no term vectors) = 10.3 MB
saving = 74%
```

Extrapolated: ox bleve 438 MB → ~115 MB; the 2 GB repo → ~520 MB.

## Decision

Switch CodeDB's Bleve content fields to **index-only** (`Store=false`,
`IncludeTermVectors=false`), stop generating ANSI highlighting, and **re-derive
the plain snippet from the source** at search time. As a bonus, populate real
**line numbers** for code hits (today `assembleCodeHit` leaves `Line=0`; the
snippet is the only locator).

### Mapping change (`internal/codedb/store/store.go`)

Replace the bare `bleve.NewIndexMapping()` for the code/comment (and possibly
diff — see open questions) indexes with a mapping whose `content` field is
index-only:

```go
m := bleve.NewIndexMapping()
ft := bleve.NewTextFieldMapping()
ft.Store = false
ft.IncludeTermVectors = false
dm := bleve.NewDocumentMapping()
dm.AddFieldMappingsAt("content", ft)
m.DefaultMapping = dm
```

### Search read-path change

```mermaid
flowchart LR
  Q[query] --> B[Bleve match: doc id + score]
  B --> E[SQL enrich: path, lang, repo  (already batched)]
  E --> S{snippet source}
  S -->|code| G[read git blob, locate match → line + ±ctx]
  S -->|comment| C[comments.text from SQLite]
  S -->|diff| D[regenerate from old/new blob OR keep stored]
  G --> R[Result: file, line, plain snippet]
  C --> R
  D --> R
```

- **code**: Bleve no longer returns content. Read the blob (existing
  `repoPool`/git path), locate the match (literal scan or regex from the parsed
  query), return the matching line + small context, set `Line`.
- **comment**: snippet comes from `comments.text` (already the fallback in
  `assembleCommentHit`).
- **diff**: diff text currently lives **only** in Bleve (the `diffs` SQLite table
  is metadata-only). Either regenerate from `old_blob_id`/`new_blob_id` or leave
  the diff index stored for now (open question).

### Reindex / versioning

A mapping change only affects newly written docs; existing indexes keep their
stored segments until rebuilt. Add a **bleve mapping version** marker; on open,
if the stored version is older, trigger a rebuild via the existing self-heal /
reindex path. Realizing the 74% on existing caches is a one-time rebuild cost.

## Consequences

**Positive**
- ~74% smaller code/comment Bleve indexes → large on-disk + RSS reduction
  (≈2 GB → ≈0.5 GB on the biggest repo).
- No more highlight compute per search; no ANSI bytes shipped to agents.
- Code hits gain real `file:line` (more actionable than a fragment).

**Negative / risk**
- **+1 blob read + match-scan per result** at search time (≤ limit results, in
  process, with warm git packfiles — negligible, but non-zero).
- **Diff** needs a regeneration path or stays stored (partial win).
- **One-time reindex** to shrink existing stores.
- Highlighting is gone for any future human TTY consumer (acceptable: CodeDB is
  agent-facing; a human path could re-highlight the re-derived snippet cheaply).
- **Data-access ergonomics change** (search result `Content`/`Line` semantics) —
  the reason this is an ADR for Ryan.

## Alternatives considered

1. **Keep term vectors, drop stored content.** Bleve can highlight from term
   vectors without the full stored field. Saves less (term vectors are bulky),
   keeps highlight compute. Rejected: smaller win, keeps dead ANSI path.
2. **Store content only for diff, index-only for code/comment.** Pragmatic middle
   ground that avoids diff regeneration. Viable as phase 1.
3. **Do nothing; attack peak RAM only** (chunked streaming + `FreeOSMemory`).
   Independent, complementary — does not reduce on-disk/steady Bleve size.

## Related work (not part of this ADR)

Profile-driven memory targets tracked separately: (1) chunked streaming
prefetch/extract/insert to bound parse-phase peak (~1.5 GB → chunk size);
(2) `debug.FreeOSMemory()`/`GOMEMLIMIT` to return the heap high-water to the OS;
(3) prune low-value SQLite indexes (`idx_comments_kind`, redundant `symbol_refs`
index). These need no data-access review.

## Open questions for review

1. **Diff**: regenerate from blobs, or keep the diff index stored (phase the
   change)?
2. **Snippet at all?** Is `file:line` + symbol enough for agents, or is the
   ≤120-char snippet worth the per-result blob read?
3. **Reindex trigger**: piggyback on the existing mapping/self-heal version, or an
   explicit `ox code index --rebuild`?
4. Sign-off on the search-result schema change (`Content` now re-derived plain
   text; `Line` populated for code).
