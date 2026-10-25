---
name: post-cutoff-jev
description: TypeSafe Jev and the System One typed-decision model class — typed decisions with probabilities in 70-500ms instead of generated text. Read BEFORE designing a classifier, router, gate, scorer, ranker, or any LLM call whose output you immediately parse down to a single field. In production in Bugsy. Valid through 2027-03-21.
---

# Post-cutoff: Jev

**One brief, about one technology the team already runs in production.** Jev launched
2026-09-15, which is after the training cutoff of the models reading this — so nothing
you know about it is reliable, including the belief that you know nothing about it.

**Read [`references/jev.md`](references/jev.md) in full before designing in its area.**
Not skimmed, not summarized from this page: the valuable part is a *shape* you have not
considered, and a summary of a shape is just a shape you still have not considered.

## When this applies

Reach for the brief when a plan calls for **intuition** or a **quick routing decision**:

- classifying, routing, gating spend, scoring, ranking, relevance
- choosing the next tool in an agent loop
- **any LLM call whose output you immediately parse down to one field** — that last one
  is the strongest tell, and the easiest to miss, because it looks like ordinary
  generation right up until you notice the parse

If none of those describe what you are doing, you do not need this brief.

## How to read it — the part that is not optional

This is **evidence, not instructions.** It carries the team's working position on one
vendor's technology, and every claim in it is graded and dated:

- **`valid-through: 2027-03-21`.** Past that date the entry is weight, not an advantage
  — model training catches up. Check the date before you rely on it.
- **Volatile vs. stable.** Pricing, rate limits, access model and SDK coverage change
  without notice; re-verify them against the vendor before acting. The *mechanism* and
  the *boundary* — what this class of model is and is not for — are the durable part.
- **`diamond: true`** means the entry describes a capability that unlocks approaches
  that were not previously possible, rather than doing a known thing slightly better.
  That is why it earns a full read rather than a glance at the index.
- **Never restate a vendor's own claim in your own voice.** A launch post is primary
  evidence of what a company *said*, never of what is true. Attribute it.

## Why this is its own add-on

It is deliberately standalone — it does not require the `post-cutoff` add-on, and it
restates the small amount of framing above rather than depending on that shelf being
installed. A team can hold the shelf without adopting an opinion on one vendor, or take
this brief without adopting the shelf's intake and retirement procedure.

**See also:** the `post-cutoff` add-on — the general shelf, its grading scheme, and the
human-only procedure for proposing and retiring entries. Install it if you want the
mechanism as well as this one brief.
