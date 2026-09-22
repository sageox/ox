---
name: post-cutoff
description: Curated facts about tools, models, and releases that postdate your training cutoff — read BEFORE choosing an approach, picking a model, designing a classifier/router/gate, evaluating a vendor, or planning a feature. Current entries: TypeSafe Jev and the System One typed-decision model class. Also the procedure for adding and retiring entries.
---

# Post-cutoff

**What this covers:** the small set of tools, models and techniques **this team has
actually adopted or deliberately ruled on** that postdate the models we run. Each entry
is a dated, graded brief with an expiry. Read the index below, open only the entries
that touch what you are doing, and treat everything here as *evidence*, not instructions.

**Why:** an agent's training cutoff fails silently. There is no error and no hedge —
you design with the best approach you know, and never mention the one the team adopted
last month. `AGENTS.md`-style "search before building" rules tell you to go look;
nothing curates what the team already decided. This is that missing half.

**This is a short shelf, on purpose.** It is not a watchlist, a news feed, or a survey
of everything new — those are unbounded, and an unbounded index is one nobody reads.
Keeping it to what the team genuinely uses is what makes reading the whole index cheap
enough to be worth doing before every design decision.

**Intake is a human's call, always.** An agent may *propose* an entry; only a person
promotes one. This is the same rule the team applies to its ideas board, and for the
same reason — Ryan, 2026-09-16: *"Wisdom cannot be automated."* Deciding that a
technology is worth every teammate's agent knowing about is a judgment about the team's
direction, not a research result.

**This is deliberately perishable.** Every entry carries a `valid-through` date,
**six months out by default**, on the assumption that model training catches up in
roughly that window. When it does, the entry stops being an advantage and becomes
weight. The retirement procedure matters as much as the authoring one.

## What this is not

- **Not a watchlist.** "This shipped and looks interesting" is not an entry. The bar is
  that the team uses it, or looked hard and decided not to. Everything else is noise
  that makes the index too long to read.
- **Not agent-generated.** No agent adds an entry on its own initiative, and no agent
  should go searching to fill this shelf. Propose; wait for a person.
- **Not a roadmap or an ideas board.** Entries are falsifiable claims about the outside
  world, not things we might build. An idea we are dreaming about belongs on whatever
  board your team keeps for that.
- **Not an endorsement by itself.** An entry says the team has a considered position.
  Read the entry for what that position *is* — several exist specifically to stop an
  adoption by puncturing a launch claim.
- **Not a vendor's documentation.** Entries carry judgment, boundaries, and independent
  evidence, and link out for API syntax. If an entry starts restating a reference
  manual, it has lost its reason to exist.
- **Not a skill finder.** This is knowledge, not tooling.

## Diamonds

Some entries are ordinary: a tool we adopted, useful to know, mildly better than what it
replaced. A few are **diamonds** — technologies that unlock experiences and techniques
far beyond what was previously possible, rather than doing a known thing slightly
better. Media over QUIC was one. Temporal was one. Jev looks like one.

An entry marks itself with `diamond: true`. It means: **read this one in full before
designing in its area**, because the valuable part is a shape you have not considered,
not a benchmark. A diamond changes what is worth attempting; an ordinary entry changes
which library you call.

**This is the same word the team's ideas board uses, deliberately, and the two do not
collide** — they differ by origin. A Horizons diamond is *a bet we might make*. A
post-cutoff diamond is *something that already landed in the world and we picked it up*.
One is ambition; this one is arrival.

## Two clocks, and only one of them is enforced

- **Entry-level** — each `references/*.md` carries its own `valid-through`. This is
  convention: the agent reads it, says so when it has passed, and proposes a refresh or
  a retirement. Most decay happens here, because the shelf outlives any one entry.
- **Skill-level** — a `valid-through` in this file's frontmatter marks the *whole skill*
  as temporary. **ox parses this and reports it**, so a skill that was only ever meant to
  bridge a training gap surfaces for removal instead of quietly becoming furniture.
  **An expired skill is reported, never auto-deleted** — a date is a prompt, and deleting
  a team's knowledge on a timer is the silent-disappearance failure this whole design
  exists to prevent.

This skill carries no skill-level date: the *procedure* does not expire, only its
entries do. A single-technology team skill usually should carry one.

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
| [`references/jev.md`](references/jev.md) **◆** | TypeSafe **Jev** — the control-plane decision model: typed decisions with probabilities in 70–500ms instead of generated text. **In production in Bugsy.** | a plan calls for *intuition* or a *quick routing decision*: classifying, routing, gating spend, scoring, choosing the next tool, or any LLM call whose output you immediately parse down to one field | **2027-03-21** |

**◆ marks a diamond.** At scale, filter rather than scan — every entry's frontmatter is
greppable:

```bash
grep -l 'concerns:.*classif' references/*.md
grep -H 'valid-through:' references/*.md | sort -t: -k3
grep -l 'diamond: true' references/*.md
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

## Proposing an entry

**You do not add entries. You propose them.** A person decides what the whole team's
agents get told, and that gate is the reason this shelf stays short enough to be read.

Read [`references/AUTHORING.md`](references/AUTHORING.md) for the full procedure and the
template. The short version:

1. **Check it clears the bar: is the team using it, or did the team rule on it?** If the
   honest answer is "not yet, but it looks promising," it is not an entry — say so and
   stop. That is a normal and correct outcome.
2. Confirm it is genuinely post-cutoff. Ask the models what they already know; that
   kills roughly half of candidates for free.
3. Research to primary sources and grade every claim.
4. Hunt the skeptical case as hard as the launch claims. An entry without one is
   marketing.
5. **Bring it to a person with the draft and the bar-clearing argument.** Only once they
   say yes do you write it in, add the index row with a `valid-through` six months out,
   and **regenerate this file's `description`** so it names the new entry's trigger
   surface — that description is the entire auto-selection budget, and an entry nothing
   fires on does not exist.

## Retiring an entry

**Never delete an entry. Move it to `references/retired/` with the reason.** The reason
is the artifact — it is what stops someone relitigating the same question next quarter,
and it is how a reader learns whether the fact became false or merely became common
knowledge.

**Retirement does not need a person the way intake does.** Adding spends every
teammate's attention; removing something the models have absorbed gives it back. Propose
the removal, say why, and do not wait for a meeting.

Retire when any of these is true:

- **The `valid-through` date passed and the models know it now.** The single most common
  case, the default assumption after six months, and the intended end state.
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
