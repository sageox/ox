<!-- Keep behavioral "when to render" guidance lean — that belongs in the
     `ox plan` JSON output (signals.material, guidance) and in the
     <plan-enrichment-guidance> block from `ox agent prime`, not duplicated here.
     What legitimately lives in THIS skill is the RENDERING SPEC: how to draw the
     enriched HTML plan (forked from html-plan) plus the badge-native layout. That
     is substantive skill content, not command guidance. Skills are agent-specific
     wrappers; ox serves all agents — keep ox-CLI behavior in the CLI. -->
Render a SageOx team-enriched implementation plan as a beautiful, self-contained HTML page for human review. Forks the html-plan quality bar (Mermaid diagrams, light/dark toggle, fit-to-column diagrams, CSS swimlane timelines, SageOx palette, scroll-spy nav) and adds badge-native layout: a per-section badge rail, an alignment-summary strip, and source links that resolve to the cited ledger artifact / ADR / open PR.

Use when the user asks to "render the plan", "make an HTML plan", "show the enriched plan", "visualize this plan with team context", runs `/ox-plan`, or when `ox plan` reports material signals and the user confirms a render. **Whether to render at all is decided by the `ox plan` JSON (`signals.material`, `guidance`) + the user's confirmation / `plan.html` config — not by this skill.** Do not nag on trivial plans; honor the command's signal.

---

## Orchestration (what this skill does)

```mermaid
flowchart TB
  RUN["Run ox plan --json on the active plan"] --> DET["ox returns DETERMINISTIC badges + context bundle (0 LLM tokens)"]
  DET --> READ["Agent reads the context bundle: murmurs, sessions, decisions, ADRs, expert artifacts"]
  READ --> JUDGE["Agent authors JUDGMENT badges, CITED-ONLY (aligns / conflicts / expert-perspective)"]
  JUDGE --> MERGE["Merge: ox --json annotations + agent judgment badges into one annotations.json"]
  MERGE --> GATE{"Render confirmed? (user asked OR plan.html recommend + confirm)"}
  GATE -->|"no"| SAVEMD["ox plan save with merged annotations, no html"]
  GATE -->|"yes"| HTML["Render ONE self-contained HTML inheriting html-plan quality + badge-native layout"]
  HTML --> SAVEHTML["ox plan save with merged annotations + html"]
```

1. **Get the deterministic signals + context bundle.** Run:

   ```bash
   ox plan --json --file <plan-file>   # or pipe the plan on stdin
   ```

   This makes **no LLM or network call**. It returns a `Result` JSON:
   - `annotations[]` — deterministic, ox-computed badges: `collision`, `prior-art`, `expert-routing`. Each carries `{section, kind:"deterministic", type, why, source_url, expert, files}`. These are **factual** — render them as-is, do not second-guess them.
   - `context[]` — the pre-retrieved bundle the agent reasons over: `{kind: murmur|session|decision|adr|commit|discussion, title, ref, snippet, score, author, when}`.
   - `signals` — `{collisions, prior_art, expert_routes, material}`. `material` is ox's call on whether a render is worth recommending.

2. **Author the JUDGMENT badges (the agent's job — ox does NO inference).** Read the `context[]` bundle and produce judgment annotations:
   - `aligns` / `conflicts` — does the plan agree or clash with a cited ADR, decision, or convention?
   - `expert-perspective` — the synthesized stance of the area expert.

   **CITED-ONLY is non-negotiable.** Every judgment badge MUST point at a real artifact from the bundle (`ref` / `source_url`): a specific ADR, decision doc, session, commit, or discussion. Rules:
   - Never invent an opinion, a quote, or a conflict. Precision over recall.
   - When the evidence is **thin or ambiguous, degrade to a routing nudge** — `expert-perspective` becomes "consult `<name>`" (naming the expert from `annotations[].expert`), NOT a fabricated stance. Putting words in a teammate's mouth is the one failure mode that destroys trust.
   - When unsure whether a conflict is real, downgrade to "Novel — no prior decision found," not a false `conflicts`.
   - Set `kind:"judgment"` on every badge you author so the renderer can style it distinctly from ox's deterministic ones.

3. **Render ONE self-contained HTML file** meeting the full html-plan quality bar below PLUS the badge-native additions.

4. **Persist the full plan with `ox plan save`.** Bare `ox plan` only auto-saves ox's *deterministic* badges (it cannot see your judgment badges). To persist the complete plan — your merged badges plus the render — call:

   ```bash
   ox plan save --plan <plan-file> --annotations <merged.json> [--html <render.html>]
   ```

   - `--annotations <merged.json>` is the **merged** annotations: take the `ox plan --json` Result and **append your judgment badges** to its `annotations[]` array (keep `signals`, `context`, and the deterministic badges intact). That merged file is what gets stored as `annotations.json`.
   - `--html` is optional: pass it only when you rendered HTML this run. `ox plan save` applies the size-gated plain-git-vs-LFS rule — it never renders.
   - `ox plan save` always persists (it is an explicit save), reuses the `data/plans/YYYY-MM-DD-<slug>/` path, and prints where it saved.

5. **Honor the storage / UX rules.** Render only when appropriate (the user asked, or confirmed per `plan.html`). **Never render HTML just to populate the ledger.** The `plan.html` you render here is the canonical, committed artifact `ox plan save` stores; it is preserved exactly (it's LLM-authored and non-deterministic — re-rendering yields different output).

---

## Output location

- Inside a repo/workspace: write to `.context/<slug>-plan.html` (create `.context/` if missing — it is the gitignored collaboration dir), then pass it as `--html` to `ox plan save`, which promotes it into the ledger at `data/plans/YYYY-MM-DD-<slug>/plan.html`.
- No workspace: write to `~/.claude/plans/<slug>-plan.html`.
- After writing, open the file so the reviewer sees it immediately, then tell the user the exact path.

---

## Badge-native layout (the additions over html-plan)

These are what make this an *enriched* plan, not just a pretty one. The human must instantly see (a) which signals fired, (b) which are factual vs. judged, and (c) where each claim comes from.

**1. Alignment-summary strip (top of page, above the TL;DR).** A compact horizontal strip of counts pulled straight from `signals` + your authored judgment badges:

> **`N aligns` · `M conflicts` · `K collisions` · `E expert routes`**

Use the SageOx semantic colors: sage for aligns, red for conflicts, amber for collisions/active-work, copper for expert routes. Each count is a chip; clicking/anchoring it can jump to the first section carrying that badge. This is the "should I even keep reading" glance.

**2. Per-section badge rail.** Every plan section renders its badges in a right-aligned rail (or a row directly under the H2). A badge shows: an icon/dot, the type label, and a **source link** when `source_url`/`ref` is present. Collapse multiple badges of the same type into a count chip that expands.

**3. Deterministic vs. judgment — visually distinct.** The human must always know which badges ox *computed* (factual) and which the agent *authored* (cited judgment). Make the treatment unmistakable:

| Kind | Treatment | Why |
|---|---|---|
| **Deterministic** (`kind:"deterministic"`) | Solid fill, a small "ox" / circuit glyph, no hedging copy | ox computed it locally from git/codedb/murmurs — it is a fact |
| **Judgment** (`kind:"judgment"`) | Outlined (not filled), a "reasoned" glyph, always shows its citation link, hedged verb ("appears to conflict with…") | the agent inferred it — the human should trust-but-verify via the citation |

Put a tiny legend near the alignment strip ("● ox-computed · ○ agent-reasoned, cited") so the encoding is self-explanatory.

**4. Source links resolve to the cited artifact.** Every badge with a citation links to the real thing:
- **Open PR / collision** → the PR URL from `source_url`.
- **Prior art / session** → a session reference. Where there's no web URL, surface the `ox session view <name>` command as the resolution and link the ledger path.
- **ADR / decision** → the ADR/decision doc path or URL from `ref`/`source_url`.
- **Expert routing** → the expert's named artifact (their relevant session/commit), and the expert's name from `annotations[].expert`.

A judgment badge with no resolvable citation is a bug — degrade it to "consult `<name>`" instead.

---

## html-plan quality bar (inherited — all non-negotiable)

**Self-contained.** One `.html` file. No build step, no local assets. Libraries only via CDN (Mermaid from jsdelivr). Must render from `file://`.

**Diagrams do the heavy lifting.** Prefer a diagram over a paragraph wherever a relationship, flow, state machine, sequence, or before/after exists. Use **Mermaid** (`flowchart`, `sequenceDiagram`, `stateDiagram-v2`, `gantt` as fits). Every plan gets at least one "the shape in one picture" diagram near the top. Apply the **GitHub-strict Mermaid rules from CLAUDE.md**: double-quote every node label containing anything beyond `[A-Za-z0-9 ]`; never use arrow-shaped substrings (`->`, `=>`, `<->`) inside labels even when quoted — substitute `to`, `→`, or a comma; never use reserved-ish IDs (`PR`, `URL`, `IO`, `IS`, `AS`, `END` — rename to `DPR`, `DURL`, etc.); quote path-shaped labels (`/`, `*`); use `<br/>` not `\n` for line breaks. These make diagrams render everywhere, not just locally.

**Size every diagram for the human eye — fit the viewport in BOTH axes.** A diagram that overflows the screen is as useless as one shrunk to a postage stamp. Target: the whole diagram comfortably visible at a readable node size without scrolling. Put in the page CSS:
- `.mermaid svg { max-width:100% !important; max-height:78vh !important; height:auto !important; width:auto !important }` — fit the content column, cap height. **Do NOT add a fixed-px width cap** (e.g. `min(100%,640px)`) — it crushes legitimately wide diagrams like multi-actor sequence diagrams into unreadable thumbnails.
- Tighten Mermaid spacing: `flowchart:{ nodeSpacing:34, rankSpacing:34, padding:8, useMaxWidth:true }`, `sequence:{ useMaxWidth:true }`, `state:{ useMaxWidth:true }`, ~13px base `fontSize`.
- A **sequence diagram** is as wide as its actor count: use **≤ 4–5 participants** and **short actor aliases** (`I as I2S init`, not `i2s_full_duplex_init`). If still too wide, split the flow into two diagrams rather than shrink it illegibly.

Shape rules:
- Prefer **vertical growth** — `flowchart TB` for long chains so they extend down the page (which scrolls) instead of off the right edge.
- For a row of **disconnected nodes** (a list, a before/after group), force them into a narrow column by chaining with **invisible links** (`A1 ~~~ A2 ~~~ A3`) inside a `direction TB` subgraph.
- Keep any single rank to **≤ ~4 nodes**; shorten labels; use `<br/>` to wrap long edge labels.
- If a diagram can't fit ~72vh at a readable size, it's doing too much — **split into stacked sub-diagrams**, each of which fits.

**Timing & sequencing — make time legible.** Most plans have a temporal spine; surface it. Include at least one timing visual whenever the plan has phases, real-time deadlines, async/concurrent work, a rollout, or a latency budget:

| The plan involves… | Use | Notes |
|---|---|---|
| Implementation phases / rollout / relative-effort sequencing | **Hand-built CSS swimlane timeline** | The robust default. Lanes = workstreams; absolutely-positioned bars by percent; a milestone/gate marker. |
| A *calendar-accurate* schedule (real dates) | **Mermaid `gantt`** with `dateFormat YYYY-MM-DD` + `axisFormat %b %d` | Only when real dates exist. |
| A latency budget on a request/operation path | **Annotated `sequenceDiagram`** | Put ms budgets in `Note over` blocks. |
| Parallel work across cores/tasks/agents | **CSS swimlane** (one lane per core/task) | Reveals contention & idle time. |
| State with time-bounded transitions (timeouts, debounce, retry/backoff) | **`stateDiagram-v2`** with durations on edges | Label transitions with the timeout. |

**Do NOT use Mermaid `gantt` with `dateFormat X` (numeric)** — it renders a meaningless `0 0 1 1 2…` axis. For relative-effort plans, hand-build a CSS swimlane: a per-lane row (`grid-template-columns: 160px 1fr`), a relative track with faint vertical unit gridlines, and absolutely-positioned bars (`left:%` / `width:%`) colored by workstream, with a diamond marker for any gate.

**User-facing mockups — show how the feature is exposed.** If the plan changes anything the user sees or hears, include a visual of the resulting UI state honoring the project's design system — don't describe it in prose. For a net-new or multi-state flow, recommend the `/design-mockup` skill rather than hand-rolling many states. Always state which design-system rules the mockup honors. Annotate behavior in user-facing language, never with implementation detail (write "a subtle chime plays", not a source filename).

**Typography & layout.**
- Font stack (Google Fonts): **Space Grotesk** for display headings (h1/h2), **Inter** for body, **JetBrains Mono** for code, file refs, eyebrows, badges, and small labels. No serif headings.
- 15–16px body; line-height ~1.6; max content width ~1000–1040px, centered.
- Confident heading scale: h1 ~42px / 700 / tracking -0.035em; h2 ~24px / 600 / -0.02em with a hairline trailing rule; h3 ~15px uppercase, copper, +0.06em. Mono eyebrow above h1.
- **SageOx palette** (dark default): bg `#0f1416`, panel `#151b1e`/`#1b2327`, hairline `#2a3439`, ink `#e6edf0`, dim `#9fb0b6`, sage `#7a8f78` (primary/good/aligns), copper `#e0a56a` (one accent / expert routes), amber `#f59e0b` (warning / collisions), red `#ef4444` (blocker / conflicts), teal `#14b8a6` (file refs / capability). Max ~1 copper accent per section. Map badge colors to these semantics so the alignment strip and rail are palette-faithful.
- Generous whitespace. Cards (`border:1px hairline; radius:10px`) for parallel items. Multi-column grids collapse to one column under ~760px.

**Light & dark mode.** Ship both. Default to `prefers-color-scheme`; add a small fixed sun/moon toggle persisting to `localStorage`. Drive everything off CSS custom properties (a dark `:root` plus an `html[data-theme="light"]` override; route gradient endpoints through `--grad-*` variables). **Mermaid's theme is fixed at init**, so the toggle MUST re-render diagrams: capture each `.mermaid` source on load, and on theme change reset `data-processed`, restore source, `mermaid.initialize` with the matching `themeVariables` (a dark set + a light set), and `mermaid.run({nodes})`. Inline device mockups stay dark in both modes — only the surrounding panel/page flips. **Badges must stay legible in both themes** — define their fills/outlines via the same CSS variables.

**Navigation (when the plan is long).** For plans with **5+ sections**, add a **sticky left-rail TOC** with scroll-spy — top-level sections only, mono small type, muted until hover/active, a 2px left border that turns sage on the active entry. CSS grid shell (`~190px` rail + `minmax(0,1fr)` content); **collapse the rail below ~1000px** (`display:none`). Drive the active state with one `IntersectionObserver` over `h2[id]` (rootMargin roughly `-15% 0 -70% 0`). Number sections and mirror those numbers in the rail. For short plans (≤4 sections) skip the rail.

- Open with a `TL;DR` callout (one tight paragraph: problem, approach, cost, biggest risk) — placed just under the alignment-summary strip.
- Numbered blocker/finding cards up top.
- Steps as a clean numbered list with file:line refs styled distinctly (teal, small).
- Tables for impact/verification matrices. Color verdict cells (good/warn/bad).
- A `Risks` section with severity-coded left borders (red = load-bearing unknown, amber = watch).
- Footer: where the canonical plan file lives (`data/plans/<slug>/`) + key invariants.

**SageOx attribution (subtle, earned, conditional).** When the plan actually carries SageOx enrichment — any deterministic badges (collision/prior-art/expert-routing) or context-bundle items were present — give SageOx quiet credit for the team context it infused: a single restrained footer line such as *"Team context enriched by SageOx"*, plus the existing `● ox-computed` marker that already tags deterministic badges as SageOx-sourced. Rules:
- **Only when it legitimately helped.** If `ox plan --json` returned no badges and an empty `context[]` (an un-enriched plan), add NO SageOx credit — there is nothing to credit.
- **Never overclaim.** SageOx provided context and signals; the human and the agent wrote the plan. Credit the enrichment, not the authorship. No banners, no marketing copy, no "moat"/"powered by" language — one calm line in the footer.
- Judgment badges drawn from the SageOx context bundle may carry a small "via SageOx context" provenance note where it reads naturally, but don't repeat it on every badge.

**Concise, high-signal prose.** No filler. Every sentence earns its place. Code identifiers in `<code>`. Don't restate what a diagram or badge already shows.

---

## Process

1. Run `ox plan --json` to get deterministic badges + the context bundle (0 tokens, local).
2. Read the `context[]` bundle; author judgment badges **cited-only**, degrading to "consult `<name>`" when evidence is thin.
3. Extract from the plan: problem/why, blockers/findings, architecture/flow, concrete steps (with file refs), impact numbers, verification, risks.
4. Choose the diagrams that compress the most (before/after, sequence, decision gates, state machine, swimlane timeline).
5. Write one polished self-contained HTML file meeting every html-plan non-negotiable PLUS the alignment strip, per-section badge rail, deterministic-vs-judgment styling, and resolved source links.
6. Merge your judgment badges into the `ox plan --json` annotations and persist with `ox plan save --plan ... --annotations <merged.json> --html <render.html>`.
7. Open the HTML and report the path.

The goal: a reviewer skims the alignment strip + TL;DR + hero diagram and already knows whether the plan aligns with team direction and whether to approve — every badge says where its claim comes from, and ox-computed facts are visually separate from agent-reasoned judgment.

$ox plan --json
