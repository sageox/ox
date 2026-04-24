// Package summaryeval is a quality-gate harness for session summaries.
//
// # What this solves
//
// Session summarization is a lossy LLM operation. Every change to the
// pipeline — swapping models, tuning prompts, adjusting pkg/tokenopt,
// adding a tokenstrip stage, landing a REDACT.md LLM redactor — can
// silently degrade summary quality in ways unit tests won't catch.
// Without a quality gate, "does the new thing make summaries worse?"
// is a flying-blind question.
//
// # Design
//
// The harness scores a candidate summary against a hand-reviewed
// reference summary using a rubric of five dimensions:
//
//   - Title fidelity — semantic match against reference title
//   - Summary coverage — keyword overlap against reference summary
//   - Key actions recall — set overlap over bullet points
//   - Outcome correctness — exact match of success|partial|failed
//   - Aha moments — count match + positional overlap
//
// Each dimension produces a 0.0–1.0 score. A weighted aggregate gives
// an overall session score. A corpus of N sessions gives an aggregate
// distribution that can be tracked over time and gated in CI.
//
// # Why deterministic scoring (v1)
//
// The scorer uses lexical metrics (Jaccard similarity, set overlap, exact
// match) rather than an LLM judge. This is a deliberate v1 choice:
//
//   - Deterministic — same input always produces same score. Regressions
//     are attributable to the thing that changed, not model variance.
//   - Fast and free — no API calls. Runs on every CI build.
//   - Self-contained — stdlib only, no Anthropic SDK dependency.
//
// Lexical metrics are approximate. A summary that paraphrases using
// different vocabulary may score lower than one that copies the reference.
// That's a known tradeoff. V2 can introduce an optional LLM-judge
// scorer on top of the same Rubric/Score shapes for cases where
// semantic equivalence matters.
//
// # Curating the golden corpus
//
// See pkg/summaryeval/CORPUS.md (or the README in the testdata directory)
// for the process: pick 20–30 diverse sessions, hand-review and polish
// summaries to a trusted reference, then run the harness to establish
// the baseline score distribution.
package summaryeval
