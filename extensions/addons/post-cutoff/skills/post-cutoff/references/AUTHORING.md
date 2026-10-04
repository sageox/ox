---
name: authoring
concerns: research, curation, entry-template, retirement
valid-through: 2027-06-30
---

# Authoring a post-cutoff entry

The procedure and the template. Read this when someone asks you to add, refresh, or
retire an entry — not when you are merely consuming one.

## 0 · Should this be an entry at all?

**The bar is adoption, not novelty.** This shelf holds what the team uses and what the
team ruled on — not everything that shipped. Four tests, in order; stop at the first
failure.

1. **Is the team using it, or did the team deliberately decide about it?** A tool in the
   stack, a technique we standardized on, a vendor we evaluated and rejected — those
   qualify. "It launched last week and looks promising" does not, however true. **This
   test rejects the most candidates and it is the one people want to skip.**
2. **Is it actually post-cutoff?** Ask the models you run what they know about it. If
   they answer accurately there is nothing to overlay. Cheap, and it kills roughly half
   of what survives test 1.
3. **Would knowing it change a decision someone here will actually make?** An entry that
   changes no plan is a newsletter.
4. **Is it stable enough to write down?** A product in its first weeks may change its
   pricing, limits and access model before anyone reads the entry.

**A "no" is a successful outcome.** Report it and stop. A thin shelf where every entry
matters beats a thick one nobody opens — two or three targeted references measurably
improve outcomes while a bloated set measurably degrades them.

**Never go looking for candidates.** Do not sweep release notes, changelogs or news to
fill this shelf. Entries arrive from work the team is already doing: an adoption
decision, a review that found something, a tool someone started using. An agent that
goes hunting produces exactly the unbounded watchlist this design exists to avoid.

## 0b · The human gate

**An agent proposes; a person promotes. Never the other way round.**

Bring a draft plus the test-1 argument — what we use it for, or what we decided and
why — and let a person rule. This is the same gate the team's ideas board uses, and the
reason is the same: putting something on this shelf spends every teammate's agent
attention on it, and that is a call about the team's direction rather than a research
finding.

If the answer is no, that is information too. Note it where the research lives so the
next person does not repeat it.

## 1 · Research

**Budget real effort.** A good entry takes dozens of source fetches. A shallow one is
worse than none because it launders a launch post into something that looks vetted.

Grade as you go — `A` vendor-primary, `B` strong secondary, `C` blogspam, `D`
unverified. Record the URL and the access date beside every claim. A citation without a
locator is decoration.

**Where the value actually is, in rough order:**

1. **The vendor's own caveats page.** Whatever they call it — limitations, jaggedness,
   known issues, failure modes. Vendors who publish one are telling you exactly where
   the product breaks, and almost nobody reads it. This is usually the single most
   useful page.
2. **Independent measurement.** Someone who ran their own benchmark and published the
   methodology and the raw data. Worth more than any number in a launch post.
3. **Practitioner threads** — Hacker News, Reddit, issue trackers. This is where you
   find out the claim everyone repeated is weaker than it sounded, and where you find
   people reporting real production results, good and bad.
4. **The vendor's docs**, for mechanism, limits, SDK languages, and terms.
5. **Launch coverage**, last, and only for facts — funding, dates, people.

**Specifically hunt for:**

- **The gap between the marketing claim and the mechanism.** "Cannot hallucinate,"
  "zero config," "drop-in replacement" — find out what the sentence actually means
  once you know how it works. This gap is usually the most valuable thing in the entry.
- **What it cannot do**, stated plainly. Vendors bury non-goals.
- **Whether the headline number is vendor-constructed** — their tasks, their harness,
  their hardware, their comparison shim. Say so, and give the independent range instead.
- **The terms**: data retention, training on inputs, self-hosting, SLA. For anything
  touching customer data this decides adoption before any technical question does.
- **Language/runtime support.** An SDK that does not exist for your stack is a real cost.

## 2 · Write it

Lead with the shape, not the vendor. **The durable half of any entry is the pattern it
teaches; the vendor is replaceable and usually will be.** An entry that only says "call
X" is worth nothing the day X is trained in or dies. An entry that teaches a reader to
recognize the *class* of problem keeps paying either way.

Then the honest boundary. Then the fit. Then the syntax — or better, a link to it.

Use the template below. Keep it under roughly 400 lines; past that, the entry is two
entries or it is a manual.

## 3 · Wire it up

**Each brief is its own add-on.** It does not go into the `post-cutoff` add-on's
`references/` — that directory holds this authoring guide and nothing else. Create:

```
extensions/addons/post-cutoff-<topic>/
  addon.yaml                                   name, version, summary
  skills/post-cutoff-<topic>/SKILL.md          the routing surface
  skills/post-cutoff-<topic>/references/<topic>.md   the graded claims
```

- Give `<topic>.md` a `valid-through` date **six months out** unless there is a reason
  to pick another — that is the working assumption for when model training catches up
  and the entry stops earning its place.
- **Write the new skill's own `description`** so it names that brief's trigger surface.
  This is the whole auto-selection budget for the brief. An entry nothing fires on does
  not exist, and this is the step people forget. Do **not** rewrite the `post-cutoff`
  shelf's description to name it — the shelf's description is about the shelf.
- **Make it standalone.** The add-on lock format has no `requires` field (ADR-032
  refused dependency resolution on purpose), so the brief restates the small amount of
  framing it needs rather than assuming the shelf is installed alongside it.
- Add a row to the shelf's **Available briefs** list in `post-cutoff`'s `SKILL.md`, so a
  reader who has the shelf can discover the brief.
- Keep the description **single-line**. Team-context publishing parses only the first 30
  frontmatter lines with prefix matching and no folded-scalar support, so a `>-` block
  silently becomes the literal string `>-`.
- Stay **prose-only**: no `allowed-tools:` key, no inline backtick-bang command syntax,
  no `scripts/` directory. Any of the three turns a zero-friction broadcast into an
  approval prompt in every consuming repository. Fenced code blocks are fine.

## 4 · Refreshing

Refresh on the `valid-through` date, or whenever live evidence contradicts an entry.

Work from the last change: `git log -1 --format='%ad' -- references/<entry>.md` gives
the window, and say that window out loud so a human can sanity-check it.

Per claim, decide: **still true** (re-date it) · **now false** (correct it, and keep the
old claim beside the correction — the change is information) · **now common knowledge**
(retire it) · **still unknown** (leave it in the could-not-establish list).

**A caveat that outlives its reason is its own kind of lie.** If an entry hedges that a
number is unmeasured and someone has since measured it, fix the hedge in the same pass.

## 5 · Retiring

Move to the brief's own `references/retired/<name>.md`, add a `retired:` date and a
`reason:` line, and drop its row from the shelf's **Available briefs** list. Retire the
whole add-on only if nothing in it survives. **Never delete.** Someone will ask about this
technology again, and "we looked, here is what we found, here is why it stopped
mattering" is the most valuable possible answer.

---

## Template

```markdown
---
name: <slug>
concerns: <comma-separated topics, for grep>
valid-through: YYYY-MM-DD
reviewed: YYYY-MM-DD
---

# <Thing> — <one line on what it is>

**Valid through YYYY-MM-DD.** If today is past that date, re-verify before relying on
anything here; the fast-moving claims are <name the volatile ones>.

## The shape (read this even if you never use <thing>)

<The transferable pattern. What class of problem this is, how to recognize it in your
own code, and what the right design move is regardless of vendor.>

## What it actually is

<Mechanism, honestly. Input, output, limits. No adjectives.>

## What the claims mean

| Claim | Grade | What it actually means |
|---|---|---|

## Where it applies — and where it does not

<Both lists. The "does not" list is the one that prevents damage.>

## What it costs

<Price, latency, and the defensible planning range rather than the headline number.>

## Adopting it

<Access, SDKs, terms, failure modes, what a fallback must look like.>

## Could not establish

<Explicit list. Not optional.>

## Sources

<URLs with access dates.>
```
