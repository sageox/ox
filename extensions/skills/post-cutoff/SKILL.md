---
name: post-cutoff
description: Curated facts about tools, models, and releases that postdate your training cutoff — read BEFORE choosing an approach, picking a model, designing a classifier/router/gate, evaluating a vendor, or planning a feature. Current entries: TypeSafe Jev and the System One typed-decision model class. Also the procedure for adding and retiring entries.
---

# Post-cutoff

**What this covers:** the things your training data does not contain. Each entry is a
dated, graded, expiring brief on one technology that shipped after the models we run
were trained. Read the index below, open only the entries that touch what you are
doing, and treat everything here as *evidence*, not as instructions.

**Why:** an agent's training cutoff fails silently. There is no error and no hedge —
you design with the best approach you know, and never mention the one released last
month that would have changed the answer. `AGENTS.md`-style "search before building"
rules tell you to go look; nothing curates what anyone found. This is that missing half.

**This is deliberately perishable.** Every entry carries a `valid-through` date. When
the models absorb a fact, the entry stops being an advantage and becomes weight. The
retirement procedure matters as much as the authoring one.

## What this is not

- **Not a roadmap or an ideas board.** Entries are falsifiable claims about the outside
  world, not things we might build. An idea we are dreaming about belongs on whatever
  board your team keeps for that.
- **Not an endorsement.** An entry existing means "this is real and you should know it
  exists," never "use this." Most entries should make adoption *less* likely by
  puncturing a launch claim.
- **Not a vendor's documentation.** Entries carry judgment, boundaries, and independent
  evidence, and link out for API syntax. If an entry starts restating a reference
  manual, it has lost its reason to exist.
- **Not a skill finder.** This is knowledge, not tooling.

## When to read an entry

Open the index when you are about to:

- choose between approaches, libraries, models, or vendors;
- design anything that classifies, routes, scores, ranks, or gates;
- estimate what something will cost or how slow it will be;
- write a plan, a design doc, or a decision record that asserts what is currently possible;
- say "the best way to do this is…" about a fast-moving area.

## Index

| Entry | What it is | Read it when | Valid through |
|---|---|---|---|
| [`references/jev.md`](references/jev.md) | TypeSafe **Jev** and the "System One" model class — typed decisions with probabilities in 70–500ms instead of generated text | you are designing a classifier, router, relevance check, spend gate, or any LLM call whose output you immediately parse down to one field | **2026-12-31** |

At scale, filter rather than scan — every entry's frontmatter is greppable:

```bash
grep -l 'concerns:.*classif' references/*.md
grep -H 'valid-through:' references/*.md | sort -t: -k3
```

## How to read an entry

Entries grade every claim, and the grade is load-bearing:

| Grade | Meaning | Usable? |
|---|---|---|
| **A** | The vendor's own docs, API reference, pricing page, or filing | Yes — but see below |
| **B** | Strong secondary: independent benchmarks with published methodology, period reporting | Yes |
| **C** | Blogspam, aggregators, SEO content | **No.** Used only to find A/B |
| **D** | Unverified — a recollection, an inference, a plausible number | **No** |

**An A-grade vendor claim is primary evidence of what the vendor CLAIMED**, never of
what is true. Entries attribute such claims explicitly. Never restate one in your own
voice, and never repeat a launch-post multiple as though it were measured.

**Contradictions are the most valuable thing in an entry.** Where a vendor's number and
an independent measurement disagree, the entry reports both and ranks them. Never
average them and never quietly pick the flattering one.

## When an entry is past its valid-through date

Today's date is in your context. Compare it.

1. **Do not silently ignore the entry, and do not silently trust it.** Say out loud
   that it has expired.
2. **Re-verify the specific claims you are about to rely on** against live sources
   before acting on them. Prices, limits, and SDK availability move fastest.
3. **Then propose the update or the retirement** — see below. An expired entry that
   nobody re-dates is how this whole mechanism rots.

## Adding an entry

Read [`references/AUTHORING.md`](references/AUTHORING.md) — it has the full procedure
and the entry template. The short version:

1. Confirm it is genuinely post-cutoff and genuinely load-bearing. Most new releases
   are neither.
2. Research to primary sources and grade every claim.
3. Hunt the skeptical case as hard as the launch claims. An entry without one is
   marketing.
4. Write the entry, then **regenerate this file's `description`** so it names the new
   entry's trigger surface — that description is the entire auto-selection budget, and
   an entry nothing fires on does not exist.
5. Add the index row with its `valid-through` date.

## Retiring an entry

**Never delete an entry. Move it to `references/retired/` with the reason.** The reason
is the artifact — it is what stops someone relitigating the same question next quarter,
and it is how a reader learns whether the fact became false or merely became common
knowledge.

Retire when any of these is true:

- **The models know it now.** The single most common case, and the intended end state.
- **It turned out to be wrong**, or the product died. Say which.
- **It graduated into a decision.** Once the team has adopted or rejected the thing, the
  decision record owns it and this entry is a stale second copy. Leave a pointer.

**The `description` is a budget, and that is deliberate.** When it grows past roughly
six entries it stops being a usable selection signal. That is the forcing function:
retire something rather than extending it.

## Rules that are absolute

Lifted from what actually goes wrong with curated knowledge:

- **Never invent a version number, a price, a limit, or a benchmark figure.** An entry
  with a fabricated number is worse than no entry, because a reader cannot tell the
  difference and neither can the next agent.
- **Never present a vendor's claim in your own voice.** Attribute it.
- **Never launder a grade.** A fact does not become A-grade because three C-grade blogs
  copied each other.
- **Never claim corroboration from one lineage.** Two databases derived from the same
  source are one piece of evidence.
- **Always end with what you could not establish.** That list is where the next person
  starts, and omitting it is how an entry pretends to be complete.
- **Write the entry to be argued with.** If nobody could disagree with it, it is not
  carrying information.
