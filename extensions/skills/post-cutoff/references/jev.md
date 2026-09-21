---
name: jev
concerns: classification, routing, scoring, gating, ranking, relevance, latency, llm-cost, typed-decisions
valid-through: 2027-03-21
reviewed: 2026-09-21
diamond: true
adopted: bugsy
---

# Jev / "System One" models — typed decisions instead of generated text

**Valid through 2027-03-21.** Jev launched 2026-09-15. Pricing, rate limits, access
model, and SDK coverage are the volatile claims — re-verify those before relying on
them. The mechanism and the boundary below are stable.

**We use this.** Jev is in production in **Bugsy**, the fleet's log-triage agent. This
entry is not a survey of something interesting; it is the team's working knowledge of a
tool already in the stack.

**This is a diamond.** A technology that unlocks experiences and techniques well beyond
what was previously possible, rather than doing a known thing slightly better. Media
over QUIC was one. Temporal was one. Read a diamond's entry in full before designing in
its area — the valuable part is usually a shape you have not considered, not a benchmark.

## The shape — read this even if you never call Jev

**A large fraction of LLM calls in a mature codebase are not generation. They are
decisions wearing a generation costume.** You send a prompt, get back text, parse it
into a boolean or an enum, and throw the rest away. You pay frontier prices and
frontier latency for a value that has, at most, a few bits of information in it.

Three tells that a call site is decision-shaped:

1. **You parse the output down to one field** — a `{"worth": bool}`, a `"yes"/"no"`, a
   label from a fixed set, a 1–5 score.
2. **`max_tokens` is tiny.** A call capped at 20 or 24 tokens is a classifier.
3. **You wrote a validator** that rejects anything outside a set you already knew.

And one inverse tell, which is where the *new* capability is rather than the saving:
**a hand-rolled heuristic whose comment explains that it exists because a model was
too slow or too expensive.** Those comments are a map of the decisions a team already
wanted to make well and could not afford to. They are worth grepping for.

Four design moves follow, and **all four are worth making whether or not you ever
adopt a specific vendor**:

- **Put a cheap gate in front of expensive generation.** If a summarizer is invoked and
  its output discarded whenever some `skip` flag comes back true, the skip decision
  should be its own cheap call. You are currently paying the expensive model to tell
  you it had nothing to say.
- **Type the decision at the seam.** Make the interface `(value T, confidence float64)`
  rather than `(text string)`. That is the abstraction that lets you swap the
  implementation — to a small model, a classifier, or back to a frontier LLM — without
  touching the caller.
- **Put a confidence band on it, and calibrate the band against your own labels.**
  Not the vendor's.
- **Make abstention a first-class outcome, distinct from a negative one.** This is the
  failure a typed-decision model actively invites, because its output is a *value* and
  a value has no way to say "I could not tell". The moment an input the decision
  depends on is missing, "cannot evaluate" collapses into "no" — and downstream that
  reads as a confident verdict nobody checked.

  Worked example, found in this codebase rather than imagined: a skill-distribution
  path resolved a repository's identity and passed it to a filter. When the identity
  could not be resolved the filter returned "does not apply" — the same answer it gives
  for a genuine non-match — so every targeted item silently vanished, and a retirement
  pass would have deleted them as un-published. The fix was not a better filter; it was
  giving the unknown case its own state, which suppressed removals while still serving
  everything that needed no identity at all. A calibrated probability gives you that
  band for free, but only if you *spend* it: pick an abstain range and route it
  somewhere, or you have bought calibration and thrown it away.

The rest of this entry is about one product. The four moves above outlive it.

## The one-line model: a control plane, not a brain

**Jev is the control-plane decision model. Claude, Codex and friends are the reasoning
and generation models.** Jev does not generate paragraphs — it makes typed decisions
that software can use directly. Hold that split and the fit question stops being "is Jev
smart enough" and becomes "is this a decision, or is it thinking".

```
                    ┌──────────────┐
event / task ──────►│     JEV      │
                    │ fast decision│
                    └──────┬───────┘
                           │
             ┌─────────────┼──────────────┐
             ▼             ▼              ▼
          ignore       deterministic    expensive
                         operation       reasoning
                            │              │
                            │        Claude / Codex /
                            │        Gemini / etc.
                            │              │
                            └──────┬───────┘
                                   ▼
                              result/state
                                   │
                                   ▼
                              ┌─────────┐
                              │   JEV   │
                              │ next?   │
                              └─────────┘
```

The loop matters as much as the entry point: Jev decides what to spend on, the expensive
model does the thinking, and Jev decides what happens next — so the reasoning model is
invoked only where reasoning is the product.

## When to reach for it — the classification

**Use this table during planning.** If a step in your design appears in the top half,
stop and consider Jev before writing another small LLM call.

| Operation | Fit | Why |
|---|---|---|
| Which agent or tool should handle this? | **Excellent** | bounded `choice` |
| Is this task complete? | **Excellent** | `noul` + confidence |
| Continue / retry / escalate / stop? | **Excellent** | small `choice` |
| Is this failure environmental or a real product bug? | **Excellent** | classification |
| Which subsystem owns this issue? | **Excellent** | routing |
| Is this log event worth investigating? | **Excellent** | cheap, very high volume |
| Should an agent wake up at all? | **Untried** | shape fits, but no published implementation exists — the nearest analogue is a live act-vs-idle gate |
| Which model should handle this task? | **Excellent** | model routing |
| Does this issue duplicate another? | **Good** | `noul` or `score` |
| Does this PR satisfy rubric criterion X? | **Good** | independent per-criterion decisions |
| Generate a fix | **No** | needs generation |
| Debug a complicated failure | **No** | System-2 reasoning |
| Write or review code | **No** | generative |
| Plan an implementation | **No** | multi-step reasoning |

**The research trigger, stated plainly:** when a plan calls for *intuition* or a *quick
routing decision*, evaluate Jev. When it calls for *reasoning*, do not.

## Field-tested patterns, with numbers

Published, independently-measured uses. Grades: **A** = the project's own code/docs,
**B** = independent practitioner with methodology.

| Pattern | Shape | Measured | Grade |
|---|---|---|---|
| **Gate an agent's "done" claim** at the stop hook | 3 `noul` + 1 `choice` | AUROC **0.976** vs 0.777 for a wording baseline; 346ms p50; $0.00005/question | A/B |
| **Pre-execution tool-call risk gate** (allow / ask / deny) | `choice` + `noul` + `score` | 3 holds per 1,000 calls over 18,075 guarded calls; rule violations **6 → 0** across 150 paired runs | A/B |
| **Model / tier routing** | `choice` over tiers | **95.0% vs 73.75%** tier match against a Haiku classifier; 5.43x faster; **96% cheaper** | B |
| **Replace an LLM judge at 100% sampling** | `noul`/`score` per rubric item | **91.5% agreement** with Claude Fable 5.1 at **$160/M graded answers vs $33,000/M**; separately 98.3% vs a trained classifier's 98.4% on 18,514 emails | B |
| **Live-stream salience — act now vs wait** | `noul is_command` + `noul complete` + silence timer | **300–550ms** per decision, **$0.00008**/request, 2–4 requests per spoken sentence | A |
| **Per-sentence scoring of a live transcript** | 5 `noul` per sentence | 90-minute session: 1,191 calls, 5,955 answers, **$0.0497 total**, ~0.4s median | A |
| **Browser/UI agent navigation** | two `choice` heads, one round trip | CDP calls **1,092 → 101** (~91% fewer); median 9.45s → 7.09s | A |
| **Mobile QA assertions without an LLM judge** | `choice` over enumerated actions | full test run **14s**, **$0.0023** | B |
| **Context compaction** (keep / truncate / drop) | 2 `noul` per tool-call pair | pattern clear; **no benchmarks published** — the author's own guidance is to use something else if reduction is under 25% | A pattern / unmeasured |
| **Bulk row filtering** | `noul` battery + `score` | **1,500+ rows/sec**, ~$4.20 per 100M tokens | A |
| **Log/event triage** | `noul` + `score` + `choice`, one call per collapsed batch | design published; **no accuracy figures** | A pattern / unmeasured |

**Our mural case has a published analogue.** `jev-canvas` does live voice → canvas with
exactly the shape we would need: a `noul` for "is this actionable", a second `noul` for
"is the utterance finished", a `choice` for what/where, and a ~900ms silence wait before
committing. At 300–550ms and $0.00008 a decision, the economics of deciding *when* to
draw on a live mural are already demonstrated by someone else.

## Gotchas that will bite you

These are not in the launch material and you would not guess them. Each is independently
measured.

- **Option position changes the answer.** First-position accuracy **88%** vs
  fourth-position **57.4%** across 24 permutations of the same question. Randomize or
  fix option order deliberately, and never let it carry meaning.
- **Questions in one request cannot consume each other's answers.** Batching is free but
  the questions must be *independent*. In one study the prerequisite answers were right
  and the final dependent action wrong in **25 of 25 calls** — a failure that looks like
  reasoning and is not. Chain across requests, or fold the logic into your own code.
- **It cannot abstain.** There is no "I don't know"; it returns the least-wrong option.
  The abstain band is yours to build out of the probability — see the design moves above.
  This is the single most important consequence of the typed-output design.
- **Lost in the middle.** A fact placed mid-state was found **1 of 6** times versus 6 of
  6 at the start or end. Put the decisive material at an edge.
- **It cannot extract values.** The API rejects anything that is not `noul`/`choice`/
  `score` — no names, amounts, dates or IDs. Propose candidates and validate with a
  `noul` instead.
- **Graded relevance ranking is conditional.** Passes 6/6 calibration gates on one
  corpus and fails 4/6 on another; two-decimal quantization creates ties. Verify on
  *your* data before ranking with it.
- **Never let it make a permission decision.** State is not treated as hostile; injection
  in a ticket body moves the answer. Enforce authorization in code, before the call.

## Named candidates, not yet ruled on

Carried so the next person starts here rather than from scratch:

- **Agent-loop control** — next tool/subagent, and continue/retry/escalate/stop.
- **Context compaction and curation** — deciding what stays in a window.
- **Browser agents for testing** — typed assertions and navigation choices instead of an
  LLM judge (our Attest-shaped work).
- **Real-time topic extraction and salience** — deciding *when* something in a live
  conversation is worth acting on, e.g. when and what to draw on a live mural. **Start
  from the `jev-canvas` precedent above rather than from scratch.**
- **Model routing** — picking the model per task.

## What it actually is

TypeSafe AI shipped **Jev** on 2026-09-15, naming the category "System One models." It
is not a small LLM. It generates no text at all.

You POST a `state` (a string, JSON object, or array of text) plus named `questions`, and
get back typed values with probability distributions in a single parallel pass. There
are exactly **three** question primitives and the list is closed:

| Primitive | You supply | You get back |
|---|---|---|
| `noul` | instructions, optional true/false criteria | a probability, 0–1 |
| `choice` | instructions, a map of ≤255 options → descriptions | the winning option, the full distribution, a confidence |
| `score` | instructions, 2–10 **ordered** level descriptions | a probability-weighted score, the distribution, a confidence |

There is no ranking type, no struct type, no multi-label type. You assemble those from
primitives in your own code. Text only — images, audio and video are explicitly
unsupported. 64k tokens per request, of which 32k is state plus the longest question.

**Batch your questions.** The docs are explicit that additional questions barely change
response time and cost only their own tokens. Ten questions in one request, never ten
requests.

Trained with what TypeSafe calls RLCD — reinforcement learning for calibrated decisions
— which they contrast with RLHF and RLVR. No paper, no model card, no parameter count,
no weights. Community reimplementations circulating under other names are **not** Jev;
do not cite their architecture as though it were.

## What the claims mean

| Claim | Grade | What it actually means |
|---|---|---|
| "Mathematically cannot hallucinate" | A (vendor) | **It cannot emit a value outside the option set you supplied.** The output head *is* the type, so type errors are impossible by construction. This says nothing about whether the chosen value is correct. A confidently wrong boolean is still wrong. |
| "Calibrated" | A (vendor) | Qualitative only — **TypeSafe publishes no ECE, no Brier score, no reliability diagram.** The `confidence` field is not even a model output; it is a concentration statistic derived from the distribution, and the docs say you are "never locked into our definition." |
| "40–200x faster" (homepage: 193.6x / 444.6x) | A as a *claim* | Vendor-constructed: their workflows, built by their model-capabilities team, timed from their laptops, against LLMs wrapped in their own comparison shim. **All of this is disclosed by TypeSafe, to their credit**, along with the note that the gains are "on the higher end of real world gains." |
| Independent speed/cost | B | ~5x faster and ~27x cheaper than Haiku 4.5 on one published benchmark; 5–18x reported by one adopter. **Plan with 3–11x faster and 20–100x cheaper** — that is where every independent measurement lands. |
| $0.042 / M input tokens, output free | A | Confirmed on the pricing page. No documented free tier. TypeSafe concedes it "can't prove the pricing isn't subsidized." |
| "Not trained on customer requests or responses" | A | Documented and unambiguous. **Zero data retention is a separate, enterprise-only commitment under DPA** — it is not the default. |
| Accuracy | B | On TypeSafe's own four-workflow eval it lands **at or below** frontier models. Accuracy is explicitly not the pitch. |

### The independent evidence, which is the important part

- **Calibration degrades out of distribution, and the error flips sign by question
  type.** A published study (methodology and raw responses open) found ECE 0.024 on a
  public benchmark likely in training data, but on unseen rule-generated tickets:
  **0.74 average confidence at 44.7% accuracy** on a policy task. Nouls came out
  *under*confident (temperature ~0.66); choice and score came out *over*confident
  (~3.3–3.4). **A single global confidence threshold is therefore unsafe** — calibrate
  per question type against your own labels, and prefer raw max-probability to the
  derived `confidence` field.
- **On a 2,000-email phishing benchmark: Jev 62.6% accuracy, Claude Haiku 4.5 81.3%
  — and a plain regex baseline 91.6%.** Task shape dominates. One practitioner reported
  40.7% on bank-transaction classification and called it unusable for that purpose.
- **Prompt injection works.** TypeSafe's own limitations page concedes the model
  "doesn't treat data as hostile by default." If your `state` contains user- or
  customer-authored text, this is the material risk — not type errors.
- **One adopter found it 10–20x *more* expensive than a cheap frontier model** for email
  classification, and slightly less accurate. The cost pitch depends entirely on the
  ratio of state size to decision value.

### TypeSafe's own limitations page is the best thing they published

Read it before designing anything. It concedes: literal interpretation of scoping words
and negations; **"not a calculator"** (counting and numeric precision unreliable);
**dates read as text, not ordered quantities** (duration and comparison math
unreliable); degraded multi-hop and double-negative handling; **accuracy falls as the
state grows with content unrelated to the decision**; no adversarial robustness; and no
structural invariants — `P(noul)` and `1 − P(not_noul)` are not comparable.

## Where it applies — and where it does not

**Good fit:** intent routing · relevance and re-ranking · spend gates in front of
expensive work · guardrails on another model's output · confidence-gated escalation to
a human or a bigger model · passage filtering · hierarchical classification by walking a
tree of `choice` calls in your own code.

**Bad fit — and these will tempt people, because their prompts look structured:**

- **Anything whose output is prose.** Summaries, explanations, rewrites, code. It
  generates no text; this is not a limitation to work around, it is the design.
- **Multi-turn tool loops.** If your pipeline recovers from a bad answer by feeding the
  error back for the model to re-read, a non-conversational model structurally cannot
  participate.
- **Decisions whose *reasoning* is the product.** You get a number, never a rationale.
  For audited, regulated, or human-actionable verdicts — "this merge is unsafe
  because…" — the explanation is often the deliverable, and losing it makes the verdict
  unactionable.
- **Arithmetic, counting, and date math.** Their own page says so.
- **Anything where a cheap deterministic rule already wins.** See the regex beating it
  at 91.6%. Measure the dumb baseline first.

## What it costs

$0.042/M input, output free, ~239ms measured median. Against a fast frontier tier that
is roughly 1/25th the input price; against a *reasoning* tier on a call you only needed
one bit from, the saving is larger and the latency win is the bigger prize.

**The saving is only real where the state is small.** Cost scales with input tokens, so
a decision over a 30k-token state is not cheap just because the answer is one bit. The
wins concentrate where the state is compact and the call count is high.

## Adopting it

- **Access:** early access as of 2026-09-15; the docs imply open console signup and it
  is already served through several AI gateways, which is the lowest-friction path. Not
  GA in any formal sense — no published SLA, no region list, and rate limits documented
  as changing without notice. A vendor status page does exist (status.typesafe.ai,
  covering `api.typesafe.ai` and `console.typesafe.ai`), but a status page reports
  availability; it does not commit to any.
- **SDKs: Python and JavaScript/TypeScript only. No official Go SDK and none
  announced.** The wire format is three JSON shapes with no streaming and no tool loop,
  so writing a small client is a proven path rather than a gamble — `agent-beacon` is a
  Go project that simply POSTs to `/v1/systemone` and does fine. Community `typesafe-go`
  and `typesafe-java` clients also exist, unaffiliated.
- **Pin the version.** `jev-latest` is a moving alias. Pin an explicit version anywhere
  decisions are cached, persisted, or compared over time.
- **Errors:** 401 and 422 are your bug — fail loud, do not retry. 429 and 529 retry with
  backoff and honor `Retry-After`.
- **Set an aggressive per-attempt deadline.** If the claim is sub-second, a 10s client
  default means a hung call blocks your hot path for 10s. Budget total, including
  retries, against whatever your caller's budget actually is.
- **A fallback is not optional.** No SLA, a public outage report already exceeding a
  default retry budget, and a vendor status page whose own 90-day history shows
  repeated short API outages — 99.86% for `api.typesafe.ai` as of 2026-09-20. Every
  question needs a deterministic default — the conservative branch — plus a circuit
  breaker.
- **Read the terms before any user or customer content goes over the wire.** No-training
  is the documented default; **no-retention is not** and is gated behind an enterprise
  agreement.
- **TypeSafe publishes its own agent skill** for coding agents. Use it for API syntax.
  This entry deliberately does not duplicate it.

## Could not establish

- Architecture: parameter count, layer count, whether it is encoder-only. No paper, no
  model card. Descriptions of it as "an encoder with a classification head" are
  inference, not disclosure.
- Any vendor-published calibration metric.
- What drives the 70–500ms range. State size is the likely dominant factor, inferred
  from the vendor's note that extra questions are nearly free.
- Formal GA status. The launch release says waitlist; the quickstart implies open
  signup; gateways serve it now. Unreconciled.
- SLA, uptime commitment, region availability. The status page publishes a measured
  record, not a commitment, and names no regions.
- Whether failed requests are billed.
- Minimums or committed-spend tiers.
- Any independent reproduction of the headline speed multiples.
- Whether adapter fine-tuning exists. Several secondary sources claim it; the vendor's
  own docs say the opposite. **Treat the fine-tuning claim as false.**

## Sources

Vendor (A): typesafe.ai launch post and manifesto; docs.typesafe.ai — API reference,
models, primitives, state, confidence, SDKs, legal, model-jaggedness, agent skill;
status.typesafe.ai — 90-day availability history for `api.typesafe.ai` and
`console.typesafe.ai`.
Secondary (B): TechCrunch 2026-09-18; The Register 2026-09-16; DataCamp; DCVC funding
announcement 2026-09-15; Vercel and Cloudflare gateway documentation; published
independent OOD-calibration and phishing benchmarks with open methodology; Hacker News
launch and follow-up threads. All accessed 2026-09-20.
