# ADR-024: Retrieval Is Agentic & Tool-Driven, Not Vector RAG

**Status**: Accepted — *retrospective* (documents an already-shipped architecture and makes its rationale explicit)
**Date**: 2026-06-16
**Deciders**: SageOx Engineering

## Context

ox ships **zero embedding models, vector databases, or semantic indexes in the binary.** Every
piece of local retrieval is lexical and tool-driven:

- `internal/ledgersearch/` — in-memory **grep** with term-frequency scoring over recent
  session summaries, murmurs, and plans.
- `internal/codedb/search/` — **BM25/Bleve** full-text search over code, comments, and diffs
  (index-only; see ADR-018).

The only semantic / `knn` path is **server-side**, behind the cloud `ox query` API
(`POST /api/v1/query`, modes `bm25` / `knn` / `hybrid`), invoked as an opt-in fallback over
the *stable* team-discussion corpus.

This is the *agent-as-retriever / just-in-time context loading* pattern: retrieval is a
**behavior of the agent** (a tool call it decides to make), not a pre-built index sitting
upstream of it. The agent gets tools and pointers; it fetches what it needs, when it needs it.

The decision was made implicitly and is recorded only in fragments — ADR-006 (four
context-delivery layers, no single index), ADR-018 (index-only Bleve, lexical), ADR-021 (notes
a "retrieval ceiling: no semantic/vector index yet" as a *limitation*), and the
`userpromptsubmit-jit-discovery` spec. This ADR names the principle once and records *why*, so
the next proposal to "add a local vector index for better recall" has a canonical answer to
point at.

### External corroboration

The frontier-coding industry converged on this pattern for **code**; ox applies the same
pattern to **team knowledge** (sessions, ledger, team context):

- **Claude Code dropped vector RAG for grep.** Its creator (Boris Cherny, *Latent Space*,
  May 2025): agentic search "outperformed everything. By a lot, and this was surprising… it's
  also simpler and doesn't have the same issues around security, privacy, staleness, and
  reliability." Anthropic later formalized the approach as *just-in-time context loading*
  ("Effective context engineering for AI agents," Sept 2025).
- **Amazon, "Keyword search is all you need"** (AAAI 2026): a tool-use agent reached **94.5%
  of RAG faithfulness** with *zero* vector store, beating RAG outright on some corpora.
- **Search-R1** (arXiv:2503.09516): training the retrieval *policy* with RL beat RAG by **24%
  relative**. Once retrieval is a tool call, it becomes a learnable policy — an embedding index
  is not.

The reference: *"AI Agents Don't Need Vector Search Anymore: Inside the Agentic Search Stack
Replacing RAG in 2026"* (Abdullah Grewal, May 2026), which catalogs the shift and its
benchmarks.

### The four reasons, mapped onto ox

| Reason | How it applies to ox |
|---|---|
| **Accuracy** | An agent iterating over `ox query` / `ox session list` / `ox code search` refines its query, follows references, and self-corrects — strictly more than a single embedding lookup can. |
| **Freshness** | The daemon keeps the team-context clone and codedb current via git pull; retrieval reads the live filesystem. No "index lag" between an edit and what the agent sees. |
| **Security / privacy** | No embedded copy of a customer's proprietary discussions or code sits in foreign infrastructure. Local retrieval is gated by filesystem ACLs only — the index *is* the source. |
| **Reliability** | No embedding model to drift, no vector DB to fall over, no re-embedding pipeline to lag. grep and Bleve just work. |

## Decision

**Retrieval is agentic and tool-driven. ox ships no vector index in the binary. Semantic search,
where it exists, lives behind the cloud API as an opt-in fallback.**

This splits cleanly into three load-bearing rules.

### 1. Local retrieval is lexical and tool-driven, never vector

`ledgersearch` (grep) and `codedb` (BM25/Bleve) are the local retrieval surface. The ox binary
never ships or builds an embedding index over a customer's repo, ledger, or team context.
ADR-018 (index-only Bleve) is the concrete instantiation for code: the inverted index is kept;
no stored vectors, no embeddings.

### 2. Just-in-time context loading, not a pre-loaded blob

`ox agent prime` injects a small, cacheable foundation — instructions, an intent→command
guidance table, and the `<consult-first>` routing block — **not** the team's knowledge dumped
into the context window. Agents fetch on demand: the `<consult-first>` cues ("user referenced
recent work" → `ox session list`; "metric/cost change" → `ox query`) route each intent to the
right corpus. MCP tools (`ox_ctx`, `ox_q`, `ox_murmur`) expose retrieval as discrete,
on-demand tool calls. This is ADR-006's fallback-layer model and the
`userpromptsubmit-jit-discovery` spec in practice.

### 3. Semantic / vector lives behind the cloud API only

`ox query`'s `knn` / `hybrid` modes run **server-side**, over the *stable* team-discussion
corpus, as an opt-in fallback (local-default; cloud is opt-in). This is consistent with two
independent principles:

- The article's own decision framework (§14): use agentic search for **evolving / code /
  exact-match** corpora; vector RAG is fine for **stable factual knowledge bases**. Code and the
  ledger drift every commit; team discussions are comparatively stable.
- ox's own rule — *keep the secret sauce behind the SageOx cloud APIs* (`CLAUDE.md`).
  Embeddings, if ever warranted, belong cloud-side, not shipped in the binary where they become
  a staleness and liability surface on the customer's machine.

## Consequences

### Positive

- **Leaner binary and deployment.** No embedding model, no vector DB, no re-indexing pipeline
  to install, run, or secure on the customer's machine.
- **Zero index-liability surface for proprietary data.** There is no second, weakly-governed
  copy of a customer's code or discussions anywhere — the strongest part of the
  security/privacy story, and the easiest to clear in compliance review.
- **Real-time freshness.** Retrieval reflects the current filesystem; no index lag.
- **A canonical answer to "should we add a local vector index?"** — for drifting or sensitive
  corpora (code, ledger), no. The rationale and citations live here.
- **Reusable design-rationale and positioning ammunition** via the external citations.

### Negative / honest limits

- **Pure-lexical local recall is weaker on genuinely semantic / synonym queries** (e.g. "retry
  policy" when the code says `backoff` / `requeue` / `circuit_breaker`). Mitigated by agent
  iteration (issue several queries and synthesize) and the cloud-semantic fallback.
- **Token cost and latency of agentic loops.** An iterative retrieve-refine loop spends more
  than one precomputed lookup. Mitigated by ox's `--local` zero-network default, the 100ms
  hard timeout on the JIT discovery hook, and prompt caching.
- A chunked / semantic **cloud** index over the team-discussion corpus remains a legitimate
  fast-follow (already noted in ADR-021). A **local** one does not — that is the line this ADR
  draws.

## Alternatives considered

- **Ship a local vector index of the ledger / codedb (rejected).** Adds staleness (every commit
  invalidates part of it), the index-as-liability surface, and an embedding model to drift —
  the exact reasons Anthropic pulled vector search out of Claude Code. The recall upside does
  not pay for the operational and security cost on a customer's machine.
- **Make semantic search the default retrieval path (rejected).** Violates the
  local-default / zero-network posture and the security model. Semantic is an opt-in cloud
  fallback, not the floor.

## References

- Abdullah Grewal, *"AI Agents Don't Need Vector Search Anymore: Inside the Agentic Search Stack
  Replacing RAG in 2026"* (May 2026) — the survey this ADR responds to.
- Boris Cherny & Cat Wu, *Latent Space* podcast (May 2025) — Claude Code's removal of vector RAG.
- Anthropic, *"Effective context engineering for AI agents"* (Sept 2025) — defines just-in-time
  context loading and tool-design principles.
- Subramanian et al., *"Keyword Search Is All You Need"* (AAAI 2026, arXiv:2602.23368).
- Jin et al., *"Search-R1"* (arXiv:2503.09516) — RL-trained retrieval policy beating RAG.
- ADR-006 (context-fallback-layers), ADR-018 (index-only Bleve), ADR-021 (`ox plan` —
  context, not inference) — the internal decisions this umbrella ties together.
- `docs/specs/userpromptsubmit-jit-discovery.md` — JIT local retrieval on user prompt.
