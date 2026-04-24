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
// # Safety model
//
// Some transforms here are lossy. The package is therefore OFF by default
// in upstream callers and gated behind explicit opt-in. Regardless of
// config, tokenstrip never mutates:
//
//   - user turns (intent signal is sacred)
//   - assistant prose OUTSIDE <thinking> blocks (the answer to the user)
//   - tool_name, tool_input, tool_mark.brief (summarizer scaffolding)
//   - header entries (session metadata)
//
// Stop-word removal and synonym substitution only touch text inside
// <thinking>...</thinking> blocks on assistant entries. NFC normalization,
// zero-width stripping, and whitespace canonicalization are lossless enough
// to apply to assistant content globally (but still skip user turns).
//
// # Streaming
//
// Compress is single-pass over r, bounded memory, tolerant of oversized
// entries (>64KB). Unknown top-level JSON fields on each entry round-trip
// via map[string]json.RawMessage so downstream consumers keep whatever
// schema extensions upstream added.
package tokenstrip
