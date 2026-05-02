// Package tokenstrip is a streaming, token-aware compaction stage for session
// raw.jsonl streams. It sits downstream of tokenopt in the session pipeline
// and reduces token count — not bytes — via a small set of intentionally
// conservative transforms.
//
// # Why a separate package
//
// tokenopt produces a byte-reduced stream (ANSI strip, image elision,
// tool-result dedup, etc.). Those transforms save bytes but rarely save
// tokens in proportion. tokenstrip attacks the tokenizer directly:
// NFC-normalize, eliminate zero-width characters, canonicalize whitespace,
// and — strictly inside assistant <thinking> blocks — drop stop words and
// optionally substitute high-token phrases with shorter synonyms.
//
// # Safety model — precise contract by transform
//
// Some transforms here are lossy. The package is therefore OFF by default
// in upstream callers and gated behind explicit opt-in.
//
// Fields NEVER mutated, regardless of transform or config:
//
//   - header entries (session metadata)
//   - user turns, in their entirety (intent signal is sacred)
//   - tool_name, tool_input, tool_mark.brief (summarizer scaffolding)
//
// For assistant entries, the applicability depends on whether a transform
// is lossless or lossy:
//
//	Lossless transforms (apply to assistant content globally):
//	  - NFC Unicode normalization — round-trippable canonical form
//	  - Zero-width + unusual whitespace strip — information-free glyphs
//	  - Whitespace canonicalization — multiple spaces/newlines → one
//
//	Lossy transforms (apply ONLY to text inside <thinking>…</thinking>):
//	  - Stop-word removal
//	  - Synonym substitution (opt-in even when tokenstrip is enabled)
//
// This means assistant prose OUTSIDE <thinking> may see its whitespace
// canonicalized and zero-width chars removed (lossless-safe), but its
// words will never be dropped or rewritten (preserves the answer to the
// user verbatim). Assistant prose INSIDE <thinking> may additionally lose
// stop words / have synonyms substituted (lossy but scoped to reasoning).
//
// # Streaming
//
// Compress is single-pass over r, bounded memory, tolerant of oversized
// entries (>64KB). Unknown top-level JSON fields on each entry round-trip
// via map[string]json.RawMessage so downstream consumers keep whatever
// schema extensions upstream added.
package tokenstrip
