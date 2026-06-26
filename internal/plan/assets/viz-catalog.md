<!-- ox plan visualization catalog. One pattern per "## <id>" block.
     Each block: a `use:` line (when to reach for it), a `why:` line (the
     cognitive payoff — what it lets the reader grasp faster), then one or more
     copy-paste snippets. Snippets use the scaffold's CSS variables / classes
     (assets/scaffold.css) so they render design-faithfully and theme with the
     page. Surfaced progressively via `ox plan viz [id]` — the agent lists the
     catalog cheaply, then pulls only the patterns it needs. Goal: aid human
     understanding and cut cognitive load (Tufte: maximize data-ink, minimize
     chrome). Keep snippets minimal and self-contained — no external JS.
     Compose/extend freely: stack snippets in the plan markdown to frame a base
     widget — e.g. a heading above a `partition-map` and a `callout` below it. The
     renderers own the widget body; you own the surrounding layout, so the base
     components extend without any renderer change. -->

## sequence-diagram
use: an ordered call/response path that crosses components, services, or async boundaries — when "in what order, how many round-trips" is the question.
why: shows ordering and latency a flowchart can't; ≤4-5 participants keeps it legible.
```mermaid
sequenceDiagram
  participant C as Client
  participant A as API
  participant D as DB
  C->>A: request
  A->>D: query
  D-->>A: rows
  A-->>C: response
```

## budget-sequence
use: a latency or cost budget across an ordered call path — show where each slice of the budget goes and the lever that buys it ("warm claim under 1s p99").
why: a bare sequence diagram shows order; budget bands (`Note over`) plus a paired stage/budget/lever table show WHERE the time goes and WHAT to pull — the reviewer approves the budget, not just the topology. Pair the diagram (the path) with the table (the levers); neither alone makes the budget reviewable.
```mermaid
sequenceDiagram
  participant C as Caller
  participant W as Workflow
  participant R as Renderer
  participant S as S3 plus CDN
  C->>W: start stream
  W->>R: claim warm slot (HRW wfid to pod)
  Note over W,R: deterministic route, ~0ms
  R->>R: navigate to brand-new URL (shared shell cached)
  Note over R: route delta only, SSR paint-instant, under 600ms
  R->>S: write first partial segment + manifest
  Note over R,S: LL-HLS partial, under 300ms
  S-->>C: manifest ready
  Note over C,S: total budget under 1s p99
```
```text
| stage                          | budget  | lever                                |
|--------------------------------|---------|--------------------------------------|
| route to warm pod              | ~0ms    | consistent-hash (ADR-040 #1)         |
| navigate to new URL (route Δ)  | <400ms  | generic warm pool + warm shell cache |
| first painted frame            | <300ms  | paint-instant pages                  |
| first partial segment+manifest | <300ms  | LL-HLS partials                      |
```

## dependency-graph
use: topology, not order — "what depends on / connects to what", to reveal coupling, a contended boundary, or blast radius.
why: a graph makes coupling visible at a glance; a list buries it.
```mermaid
flowchart LR
  CLI["ox plan"] --> ENR["Enrich"]
  ENR --> DET["detectors"]
  ENR --> RET["retrievers"]
  CLI --> RND["RenderHTML"]
```

## state-machine
use: a lifecycle, connection/session model, or retry/backoff — anything with modes and time-bounded transitions.
why: states + labeled transitions compress a paragraph of "if in X then Y after Zs" into one picture.
```mermaid
stateDiagram-v2
  [*] --> Open
  Open --> InProgress: claim
  InProgress --> Open: drop
  InProgress --> Done: complete
  Done --> [*]
```

## swimlane-timeline
use: phases, rollout, or relative-effort sequencing across workstreams (NOT calendar dates) — a "build sequence" showing what's foundational, what unblocks the goal, and what's deferred to scale. Also a before/after comparison of how a lifecycle behaves over time — e.g. a timeout/timer model with resets (the "two-knob clock"): one lane per scenario, abutting timer-span bars, and ↺/✂ event markers.
why: lanes + bars show what runs when and in parallel; the robust default for "when, in what order, how long". Add a labeled gate (◆) for the milestone that matters, a bottom axis of phase columns, and a one-line caption stating the unit — that's what turns a bar chart into an at-a-glance build plan. For a comparison, give each scenario its own lane and use a `.mark` for a point event (↺ reset, ✂ kill, → continues) and an `.anno` for a one-line per-lane caption.
```html
<div class="swim">
  <div class="lane"><span class="lane-name">Foundation</span><div class="track"><span class="bar" style="left:0;width:18%;background:var(--copper)">measure</span><span class="bar" style="left:20%;width:14%;background:var(--sage)">fix pool</span></div></div>
  <div class="lane"><span class="lane-name">Claim &lt;1s</span><div class="track"><span class="bar" style="left:38%;width:18%;background:var(--teal)">pre-warm</span><span class="bar" style="left:58%;width:16%;background:var(--teal)">paint</span><span class="bar" style="left:76%;width:18%;background:var(--teal)">partials</span></div></div>
  <div class="lane"><span class="lane-name">Scale</span><div class="track"><span class="bar" style="left:38%;width:18%;background:var(--copper)">cost dials</span><span class="bar" style="left:58%;width:16%;background:var(--copper)">deep pool</span><span class="bar" style="left:76%;width:18%;background:var(--copper)">multi-replica</span></div></div>
  <div class="lane axis"><span class="lane-name"></span><div class="track"><span class="tick" style="left:9%">measure</span><span class="tick" style="left:27%">pool fixed</span><span class="tick" style="left:47%">claim &lt;1s ◆</span><span class="tick" style="left:66%">deep pool</span><span class="tick" style="left:85%">100k scale</span></div></div>
</div>
<p class="dim">Relative effort, not calendar. ◆ = the gate where the goal becomes meetable.</p>
```
```html
<!-- before/after comparison: a timeout/timer lifecycle with resets (the "two-knob clock") -->
<div class="swim">
  <div class="lane"><span class="lane-name">Before</span><div class="track"><span class="bar" style="left:0;width:60%;background:var(--red)">one run · 6h cap</span><span class="mark" style="left:62%;color:var(--red)">✂</span><span class="anno" style="left:64%;color:var(--red)">blank · seq → 0</span></div></div>
  <div class="lane"><span class="lane-name">After · healthy</span><div class="track"><span class="bar" style="left:0;width:30%;background:var(--sage)">run timer</span><span class="mark" style="left:31%">↺</span><span class="bar" style="left:33%;width:30%;background:var(--sage)">run timer</span><span class="mark" style="left:64%">↺</span><span class="bar" style="left:66%;width:30%;background:var(--sage)">run timer</span></div></div>
  <div class="lane axis"><span class="lane-name"></span><div class="track"><span class="anno" style="left:0">renderer adopted across each ↺ · lives indefinitely →</span></div></div>
  <div class="lane"><span class="lane-name">After · wedged</span><div class="track"><span class="bar" style="left:0;width:50%;background:var(--amber)">stuck run · run timer 4h</span><span class="anno" style="left:52%;color:var(--amber)">wfid clears · re-cast works</span></div></div>
  <div class="lane axis"><span class="lane-name"></span><div class="track"><span class="tick" style="left:0">0h</span><span class="tick" style="left:25%">2h</span><span class="tick" style="left:50%">4h</span><span class="tick" style="left:75%">6h</span><span class="tick" style="left:100%">8h</span></div></div>
</div>
<p class="dim">One lane per scenario · ↺ reset, ✂ kill · the healthy run refreshes before its timer fires; the wedged run can't outlive it.</p>
```

## gantt
use: a CALENDAR-ACCURATE schedule with real dates (only when real dates exist).
why: a date axis anchors commitments; never use numeric `dateFormat X` (renders a meaningless axis).
```mermaid
gantt
  dateFormat YYYY-MM-DD
  axisFormat %b %d
  section Build
  Renderer      :2026-06-09, 3d
  Catalog       :2026-06-11, 2d
```

## sparkline
use: a trend inline in a sentence or a table cell — "p99 latency ▁▂▃▅▇ 240ms".
why: shows a whole series in the space of a word; maximum data-ink, zero chrome (Tufte's word-sized graphic).
```html
<svg class="spark" viewBox="0 0 80 20" preserveAspectRatio="none" aria-label="trend">
  <polyline points="0,18 16,14 32,15 48,8 64,9 80,2" fill="none" stroke="var(--sage)" stroke-width="1.5" vector-effect="non-scaling-stroke"/>
</svg>
```

## small-multiples
use: compare the same chart across many series/items — per-service error rates, per-core load.
why: one shape repeated lets the eye spot the outlier instantly; a single combined chart hides it.
```html
<div class="multiples">
  <figure><figcaption>api</figcaption><svg class="spark" viewBox="0 0 60 18" preserveAspectRatio="none"><polyline points="0,16 20,10 40,12 60,4" fill="none" stroke="var(--sage)" stroke-width="1.5" vector-effect="non-scaling-stroke"/></svg></figure>
  <figure><figcaption>web</figcaption><svg class="spark" viewBox="0 0 60 18" preserveAspectRatio="none"><polyline points="0,6 20,7 40,5 60,6" fill="none" stroke="var(--teal)" stroke-width="1.5" vector-effect="non-scaling-stroke"/></svg></figure>
  <figure><figcaption>db</figcaption><svg class="spark" viewBox="0 0 60 18" preserveAspectRatio="none"><polyline points="0,4 20,8 40,14 60,17" fill="none" stroke="var(--red)" stroke-width="1.5" vector-effect="non-scaling-stroke"/></svg></figure>
</div>
```

## before-after
use: a change to a structure, API, or shape — show the old and the new side by side.
why: adjacency makes the delta obvious; the reader diffs with their eyes, not by reading prose.
```html
<div class="ba">
  <div class="ba-col"><h4>Before</h4><pre><code>render: skill-only (Claude)</code></pre></div>
  <div class="ba-col"><h4>After</h4><pre><code>render: ox binary (any agent)</code></pre></div>
</div>
```

## decision-matrix
use: weigh options against criteria, or a verification/impact matrix.
why: a grid with colored verdict cells answers "which wins / what passes" in one scan. Verdict cells (yes/no/✓/✗) are auto-colored by the renderer.
```text
| Option        | Cross-agent | Tokens | Consistent |
|---------------|-------------|--------|------------|
| skill render  | no          | high   | no         |
| binary render | yes         | low    | yes        |
```

## heatmap-table
use: dense numeric comparison where magnitude matters — latency by endpoint × percentile.
why: shading encodes magnitude so the hot cell pops without the reader parsing every number (Tufte density).
```html
<table class="heat">
  <tr><th>endpoint</th><th>p50</th><th>p99</th></tr>
  <tr><td>/plan</td><td class="h1">12</td><td class="h2">40</td></tr>
  <tr><td>/render</td><td class="h2">38</td><td class="h4">210</td></tr>
</table>
```

## cost-telemetry-table
use: per-stage cost/telemetry where some stages are reducible — name the cost, the telemetry field it's measured by, and whether it can be cut, then pair the table with ONE callout stating the conclusion.
why: the table establishes the numbers (and where they come from, so the reviewer can verify against prod); a leading or trailing `TL;DR` callout states the load-bearing decision ("every reducible row is reduced by not doing it on the request path") so the reviewer gets the conclusion before parsing cells. Pair the table with the callout — the table is evidence, the callout is the read.
```html
<table class="heat">
  <tr><th>cold-spawn stage</th><th>~cost</th><th>telemetry field</th><th>reducible?</th></tr>
  <tr><td>new Chromium context</td><td>500-800ms</td><td>worker-pool</td><td>yes — pre-create</td></tr>
  <tr><td>load heavy cast page</td><td>800-1500ms</td><td>navigate_ms</td><td>yes — pre-warm + SSR</td></tr>
  <tr><td>paint handshake</td><td>300-600ms</td><td>paint_ms</td><td>yes — paint-instant</td></tr>
  <tr><td>ffmpeg init + first segment</td><td>300-500ms</td><td>first_frame_ms</td><td>yes — partials</td></tr>
  <tr><td><strong>cold total</strong></td><td class="h4"><strong>2.0-3.6s</strong></td><td></td><td></td></tr>
</table>
<div class="tldr"><span class="tldr-tag">TL;DR</span><p>Every reducible row is reduced by <em>not doing it on the request path</em> — i.e. pre-warming. Pre-warming is the mechanism, not an optimization.</p></div>
```

## device-mockup
use: the plan changes something the user sees — show the resulting UI state, don't describe it.
why: a faithful mockup conveys the experience instantly; annotate in user language, never implementation detail.
```html
<div class="device">
  <div class="device-screen">
    <div class="eyebrow">ox plan</div>
    <strong>Plan rendered ✓</strong>
    <p class="dim">Self-contained HTML · opened in your browser</p>
  </div>
</div>
```

## callout
use: the one thing the reader must not miss — the decision, the blocker, the biggest risk.
why: a single bordered lede answers "do I approve, what do I watch" before any scrolling. A leading `TL;DR` block and a `Risks` section are auto-styled by the renderer.
```html
<div class="tldr"><span class="tldr-tag">TL;DR</span><p>Ship the binary renderer; the only risk is Mermaid CDN availability at view time.</p></div>
```

## rollout-dag
use: a multi-phase rollout / PR sequence where order and blocking matter — "P3 gates P5, P4 ∥ P5". The single most common plan structure.
why: prose sequencing ("ship WS-1 first, then WS-2…") forces the reader to reconstruct the critical path; a DAG shows what blocks what and what runs in parallel in one glance. Mark the critical path.
```mermaid
flowchart LR
  P1["P1 data model"] --> P2["P2 machine API"]
  P2 --> P3["P3 migration<br/>(gates P5)"]
  P2 --> P4["P4 producer"]
  P3 --> P5["P5 unified rail"]
  P4 --> P5
```

## file-impact-map
use: the "files changed" section — show scope, change type (new/edit/delete), and which subsystem, not just a flat list.
why: a flat `| file | change |` table hides scope and blast radius; grouping by subsystem with color-coded change type lets the reviewer judge "are we touching auth AND db AND web — is that the right footprint?" in seconds.
param: {"files":[{"path":"internal/plan/render.go","change":"new|edit|delete|read","scope":"sm|md|lg","note":"…"}]}
```html
<!-- generate with: ox plan viz render file-impact-map --data files.json -->
<ul class="ftree">
  <li class="grp">internal/plan
    <ul><li><span class="chg new">new</span> render.go <span class="sc lg">lg</span></li>
        <li><span class="chg edit">edit</span> enrich.go <span class="sc sm">sm</span></li></ul></li>
</ul>
```

## risk-matrix
use: a Risks section — rank risks by severity with the mitigation, instead of an undifferentiated bullet list.
why: scattered "do NOT…" / "watch out…" prose buries the load-bearing unknown; a severity-sorted, color-coded matrix puts the blocker on top and pairs each risk with its mitigation so the reviewer knows what to watch.
param: {"risks":[{"title":"…","severity":"blocker|high|medium|low","category":"data|perf|security|ux","mitigation":"…"}]}
```html
<!-- generate with: ox plan viz render risk-matrix --data risks.json -->
<table class="riskm">
  <tr><th>Risk</th><th>Sev</th><th>Mitigation</th></tr>
  <tr class="sev-blocker"><td>CDN unreachable at view time</td><td>■ blocker</td><td>vendor mermaid locally</td></tr>
</table>
```

## stat-cards
use: headline metrics or before→after numbers — token budget, latency, size, counts.
why: a row of big-number cards with a delta and an up/down trend makes the impact land instantly; numbers buried in prose ("~60KB, down from 120KB") don't.
param: {"cards":[{"label":"render size","value":"44KB","delta":"-63%","trend":"down","intent":"good|bad|warn|neutral"}]}
```html
<!-- generate with: ox plan viz render stat-cards --data stats.json -->
<div class="statrow">
  <div class="stat good"><div class="sv">44KB</div><div class="sl">render size</div><div class="sd">▼ -63%</div></div>
</div>
```

## bar-chart
use: compare a handful of labeled magnitudes — cost per component, calls per minute, lines per file.
why: bars encode magnitude pre-attentively; a column of numbers does not. Use ONE color for the series — length is the channel; a rainbow fights the "which is biggest" read. Set a bar's `color` only to flag the outlier/over-budget one. Keep ≤8 bars.
param: {"title":"cost / hr","unit":"$","bars":[{"label":"topic detector","value":0.036},{"label":"refresher","value":0.012,"color":"red"}]}
```html
<!-- generate with: ox plan viz render bar-chart --data bars.json -->
<div class="barc"><div class="bar-row"><span class="bl">topic detector</span><span class="bt"><span class="bf" style="width:90%;background:var(--sage)"></span></span><span class="bv">$0.036</span></div></div>
```

## partition-bar
use: a memory / disk / flash layout with a FEW partitions (≤8) where the SHARE each takes is the story — "the two OTA slots are 75% of flash". Proportion-first.
why: a 100%-wide stacked bar encodes share pre-attentively — the dominant slices read instantly; a paired table carries the exact offsets/sizes the bar can't. Linear and honest about size. One color per category, not a rainbow. Segments grow in (staggered) and reveal a hover tooltip; for MANY partitions, or when offset-order / per-row annotation matters more than share, use `partition-map`.
param: {"title":"16 MB flash","total":16384,"unit":"KB","partitions":[{"label":"ota_0","size":6144,"offset":"0x20000","color":"sage","flag":"SIGNED"},{"label":"ota_1","size":6144,"offset":"0x620000","color":"sage"},{"label":"spiffs","size":2944,"offset":"0xD20000","color":"teal"},{"label":"model","size":1024,"offset":"0xC20000","color":"violet"},{"label":"system","size":128,"color":"slate"}]}
```html
<!-- prefer the param renderer: ox plan viz render partition-bar --data parts.json
     (--i staggers the grow-in; .pm-tip is the hover tooltip — both pure CSS) -->
<figure class="pbar-fig"><figcaption>16 MB flash</figcaption><div class="pbar"><span class="pseg" style="--i:0;width:37.5%;background:var(--sage)"><span class="pseg-lbl">ota_0</span><span class="pm-tip"><b>ota_0</b><span class="pm-tip-k">6144 KB · 37.5%</span><span class="pm-tip-flag">SIGNED</span></span></span><span class="pseg" style="--i:1;width:18%;background:var(--teal)"><span class="pseg-lbl">spiffs</span><span class="pm-tip"><b>spiffs</b><span class="pm-tip-k">2944 KB · 18%</span></span></span></div></figure>
```

## partition-map
use: a full memory / disk / flash layout with MANY partitions, or when OFFSET ORDER and per-row annotation (flags, notes) matter more than share — the vertical address-space view.
why: rows in offset order mirror the address space; a LOG-scaled size rail keeps 4 KB partitions visible next to 6 MB ones (true linear would render the small ones <1px — dishonest by omission) while the big ones still read as dominant. The rail is labeled "log" so no false linear proportion is implied — use `partition-bar` for true share. Per-row offset + flags + a one-line note annotate without crowding; hover reveals a tooltip with the full note + share; `"proposed":true` dashes/mutes an uncommitted row. Set a row's `"group"` to interleave a section divider (e.g. committed rows, then a "PROPOSED SECURE ADDITIONS" block). Frame the whole figure by stacking a heading above and a `callout` below — the renderer owns the rows, you compose the chrome.
param: {"title":"Rev B flash","unit":"KB","partitions":[{"label":"bootloader","size":32,"offset":"0x000000","color":"slate","flag":"SIGNED","note":"Secure Boot root"},{"label":"nvs","size":20,"offset":"0x009000","color":"slate","note":"WiFi creds, pairing"},{"label":"ota_0","size":6144,"offset":"0x020000","color":"sage","flag":"SIGNED","note":"firmware slot A · frozen at 0x20000"},{"label":"ota_1","size":6144,"offset":"0x620000","color":"sage","note":"rollback target"},{"label":"model","size":1024,"offset":"0xC20000","color":"teal","note":"wake-word model"},{"label":"spiffs","size":2944,"offset":"0xD20000","color":"teal"},{"label":"ds_key","size":4,"color":"violet","note":"encrypted device key","group":"PROPOSED SECURE ADDITIONS","proposed":true}]}
```html
<!-- prefer the param renderer: ox plan viz render partition-map --data parts.json
     (--i staggers the row fade-in + rail fill; .pm-tip is the hover tooltip — pure CSS) -->
<figure class="pmapv"><figcaption>Rev B flash <span class="pmapv-rk">size · log scale</span></figcaption><div class="pmapv-row" style="--i:0"><span class="pm-dot" style="background:var(--sage)"></span><span class="pmapv-off pm-mono">0x020000</span><span class="pmapv-nm"><b>ota_0</b><span class="pm-flag">SIGNED</span><small>firmware slot A</small></span><span class="pmapv-rail"><i style="width:100%;background:var(--sage)"></i></span><span class="pmapv-sz pm-mono">6144 KB</span><span class="pm-tip"><b>ota_0</b><span class="pm-tip-k">@ 0x20000</span><span class="pm-tip-k">6144 KB · 37.5%</span><span class="pm-tip-note">firmware slot A</span></span></div><div class="pmapv-group">PROPOSED SECURE ADDITIONS</div><div class="pmapv-row proposed" style="--i:1"><span class="pm-dot" style="background:var(--violet)"></span><span class="pmapv-off pm-mono">TBD</span><span class="pmapv-nm"><b>ds_key</b><small>encrypted device key</small></span><span class="pmapv-rail"><i style="width:10%;background:var(--violet)"></i></span><span class="pmapv-sz pm-mono">4 KB</span><span class="pm-tip"><b>ds_key</b><span class="pm-tip-flag prop">PROPOSED</span><span class="pm-tip-note">encrypted device key</span></span></div></figure>
```

## data-model
use: a schema or entity relationship — new tables/columns, foreign keys, cardinality.
why: an ER diagram shows where the FK points and which table owns the unique index — a column list in prose does not. Use Mermaid erDiagram.
```mermaid
erDiagram
  PLAN ||--o{ ANNOTATION : has
  PLAN { string slug PK string topic }
  ANNOTATION { string section string type string why }
```

## coverage-matrix
use: a test plan — show which seam is covered at which layer, and where the gaps are.
why: a list of `go test` commands doesn't reveal whether a risky seam is tested; a seam × layer grid with verdict cells exposes the gap (the empty cell) at a glance. Verdict cells auto-color in the render.
```text
| seam | unit | integration | e2e |
|---|---|---|---|
| decide logic | yes | — | — |
| opt-out gate | no | — | yes |
```

## flag-rollout-matrix
use: a feature-flag rollout — env × stage with the percentage at each step.
why: flag posture ("dev-on, test-100%, prod-0% until review") scattered in prose is error-prone; an env × stage grid shows the whole rollout arc and the gates in one view.
param: {"envs":["dev","test","prod"],"stages":["merge","dogfood","ramp"],"cells":{"prod":{"merge":"0%","dogfood":"0%","ramp":"10→100%"}}}
```html
<!-- generate with: ox plan viz render flag-rollout-matrix --data flags.json -->
<table class="heat"><tr><th>env</th><th>merge</th><th>ramp</th></tr><tr><td>prod</td><td class="h1">0%</td><td class="h3">10→100%</td></tr></table>
```

## cost-waterfall
use: a per-action cost/budget that accumulates — token spend, $/hr by component.
why: a stacked/segmented bar with a running total shows where the cost concentrates and which lever to pull, better than line-item arithmetic in prose.
param: {"unit":"$/hr","items":[{"name":"topic detector","value":0.036},{"name":"refresher","value":0.001}]}
```html
<!-- generate with: ox plan viz render cost-waterfall --data cost.json -->
<div class="barc"><div class="bar-row"><span class="bl">topic detector</span><span class="bt"><span class="bf" style="width:97%;background:var(--copper)"></span></span><span class="bv">$0.036</span></div></div>
```

## decision-grid
use: a multi-option / multi-expert review — options scored across review lenses.
why: long per-lens prose paragraphs hide the ranking; an option × lens grid with colored verdict cells shows where each option wins and where the lenses disagree, at a glance.
```text
| option | ops | security | code-reuse |
|---|---|---|---|
| native | yes | yes | no |
| hybrid | yes | yes | yes |
```

## ox-annotation
use: an inline reference (an ADR, decision, PR, prior session) you want to annotate with the team context behind it — the calm SageOx way to footnote a decision in-place.
why: a branded inline marker + hover beats inventing a circled-`i`/number glyph: it tells the reader "SageOx has context on this" without a verdict, and matches the OX marker ox already injects elsewhere — one consistent affordance instead of ad-hoc ones. Note: `ox plan render` already auto-injects this marker on references it surfaced team context for (e.g. an ADR in the bundle), so reach for this pattern only for references ox did NOT surface; never hand-author an "enriched by SageOx" credit — the render owns the footer credit.
```html
<span class="ox-annot" title="SageOx surfaced: ADR-051 Consent tooling — bundled voiceprint + recording consent; flagged the weaker BIPA position.">ADR-051 <svg class="ox-annot-mark" aria-hidden="true"><use href="#ox-ico-d" class="ico-d"></use><use href="#ox-ico-l" class="ico-l"></use></svg></span>
```

## donut
use: a part-of-whole proportion with a few slices — cost share, test pass/fail, time split. One total, broken down.
why: a ring reads share pre-attentively and frees the center for the headline total; a paired legend carries exact values + percentages so it survives grayscale. Keep ≤6 slices — beyond that a bar-chart reads cleaner.
param: {"title":"test outcomes","unit":"","slices":[{"label":"pass","value":182,"color":"sage"},{"label":"fail","value":12,"color":"red"},{"label":"skip","value":24,"color":"slate"}]}
```html
<!-- prefer the param renderer: ox plan viz render donut --data donut.json
     ox computes each slice's arc sweep (value/total·360) + the legend shares. -->
<figure class="donut"><div class="donut-body"><svg class="donut-svg" viewBox="0 0 140 140"><circle cx="70" cy="70" r="54" fill="none" stroke="var(--sage)" stroke-width="26" stroke-dasharray="254 85" transform="rotate(-90 70 70)"/></svg><ul class="donut-leg"><li><span class="vsw" style="background:var(--sage)"></span><span class="donut-lab">pass</span><span class="donut-val">182 · 83.5%</span></li></ul></div></figure>
```

## radar
use: compare a few options across multiple criteria — score each alternative on the same axes to see the shape of its strengths.
why: overlaid polygons make "which option is strong where" a single shape comparison instead of a table scan; ≤3 series and a per-series line dash keep it legible without relying on color.
param: {"title":"approach fit","axes":["speed","safety","cost","reuse"],"max":5,"series":[{"label":"native","values":[4,5,2,3],"color":"sage"},{"label":"hybrid","values":[3,4,4,5],"color":"copper"}]}
```html
<!-- prefer the param renderer: ox plan viz render radar --data radar.json
     ox computes each axis spoke angle + the per-series polygon points. -->
<figure class="radar"><div class="radar-body"><svg class="radar-svg" viewBox="0 0 240 232"><polygon class="radar-series" points="120,40 196,116 120,180 44,116" style="stroke:var(--sage);fill:var(--sage)"/></svg></div></figure>
```

## quadrant
use: a two-axis tradeoff scatter — impact vs effort, value vs risk — placing each item in a quadrant so the act-now corner is obvious.
why: a 2×2 turns "which to do first" into spatial position; the top-corner items pop without reading a single number, and labels keep it readable in grayscale.
param: {"title":"what to build first","x_label":"effort","y_label":"impact","points":[{"label":"donut","x":2,"y":8,"color":"sage"},{"label":"sankey","x":8,"y":6,"color":"copper"}]}
```html
<!-- prefer the param renderer: ox plan viz render quadrant --data quad.json
     ox normalizes x/y to the plot box and splits the 2×2 at the midlines. -->
<figure class="quad"><svg class="quad-svg" viewBox="0 0 300 224"><rect class="quad-box" x="50" y="14" width="238" height="182"/><line class="quad-mid" x1="169" y1="14" x2="169" y2="196"/><circle class="quad-pt" cx="98" cy="50" style="fill:var(--sage)"/></svg></figure>
```

## treemap
use: a proportional hierarchy where area encodes size — code by package, spend by category, storage by bucket. Size at a glance.
why: a squarified treemap encodes magnitude as area (the dominant block IS the dominant cost) far denser than a bar list; a legend carries exact sizes so slivers stay readable.
param: {"title":"repo by package","unit":"KB","items":[{"label":"internal/plan","size":120,"color":"sage"},{"label":"cmd/ox","size":80,"color":"copper"},{"label":"internal/lfs","size":40,"color":"teal"}]}
```html
<!-- prefer the param renderer: ox plan viz render treemap --data tmap.json
     ox runs the squarified layout so each cell's AREA is proportional to size. -->
<figure class="tmap"><svg class="tmap-svg" viewBox="0 0 320 200" preserveAspectRatio="none"><g class="tmap-cell"><rect x="0" y="0" width="200" height="200" style="fill:var(--sage)"/><text class="tmap-lab" x="6" y="15">internal/plan</text></g></svg></figure>
```

## sankey
use: flow magnitude across stages — where tokens, cost, traffic, or users move and split between steps. Conserved quantity, staged.
why: ribbon width encodes the magnitude flowing along each path, so the dominant route and the leaks read instantly; a node-and-arrow flowchart shows topology but hides the amounts.
param: {"title":"token budget","unit":"tok","nodes":[{"name":"prompt","color":"sage"},{"name":"tools","color":"copper"},{"name":"output","color":"teal"}],"links":[{"from":"prompt","to":"tools","value":1200},{"from":"prompt","to":"output","value":800},{"from":"tools","to":"output","value":1000}]}
```html
<!-- prefer the param renderer: ox plan viz render sankey --data sankey.json
     ox layers the DAG, sizes nodes by max(in,out), and sets ribbon width ∝ value. -->
<figure class="sankey"><svg class="sankey-svg" viewBox="0 0 360 240"><path class="sankey-link" d="M67 22 C140 22 140 22 213 22 L213 70 C140 70 140 70 67 70 Z" style="fill:var(--sage)"/><rect class="sankey-node" x="56" y="22" width="11" height="94" style="fill:var(--sage)"/></svg></figure>
```

## chord
use: symmetric coupling between entities — which modules, files, or people interact, and how strongly. Who-touches-what.
why: arcs sized by total coupling and ribbons sized by pairwise strength reveal the tightly-bound cluster a dependency list buries; the circular layout shows mutual relationships a left-to-right graph distorts.
param: {"title":"module coupling","labels":["api","db","auth","ui"],"matrix":[[0,8,3,2],[8,0,1,0],[3,1,0,4],[2,0,4,0]]}
```html
<!-- prefer the param renderer: ox plan viz render chord --data chord.json
     ox sizes each node arc by its total coupling and each chord by pairwise strength. -->
<figure class="chord"><div class="chord-body"><svg class="chord-svg" viewBox="0 0 260 260"><path class="chord-arc" d="M130 22 A108 108 0 0 1 226 86 L214 92 A96 96 0 0 0 130 34 Z" style="fill:var(--sage)"/></svg></div></figure>
```

## line-chart
use: a quantity over a continuous axis — a trend, growth curve, or latency/throughput over time; especially a before-vs-after comparison, or a value that climbs toward a limit and resets on a cadence (a sawtooth: a bounded history/buffer/log). Reach for it when "how does it move over time, and where's the ceiling" is the question.
why: axes plus a dashed threshold line make the limit and the headroom explicit, and overlaid series (each with its own line dash) put two regimes side by side — "before: climbs to the wall and gets cut; after: sawtooths safely under it" — in one read; a sparkline shows a trend but has no axis, no threshold, and no comparison.
param: {"title":"workflow history growth","x_label":"hours","y_label":"history events","x_max":8,"y_max":20000,"threshold":{"at":8000,"label":"8k cap","color":"amber"},"x_ticks":[{"at":0,"label":"0h"},{"at":2,"label":"2h"},{"at":4,"label":"4h"},{"at":6,"label":"6h"},{"at":8,"label":"8h"}],"y_ticks":[{"at":8000,"label":"8k"},{"at":20000,"label":"20k"}],"series":[{"label":"before — unbounded","color":"red","marker":true,"points":[{"x":0,"y":0},{"x":6,"y":20000,"note":"✂ 6h cap"}]},{"label":"after — reset every 8k","color":"sage","marker":true,"points":[{"x":0,"y":0},{"x":2.5,"y":8000},{"x":2.5,"y":1000,"note":"↺"},{"x":5,"y":8000},{"x":5,"y":1000,"note":"↺"},{"x":7.5,"y":8000}]}]}
```html
<!-- prefer the param renderer: ox plan viz render line-chart --data line.json
     ox scales both axes, projects each point to pixels, places the threshold, and draws the legend. -->
<figure class="linec"><svg class="linec-svg" viewBox="0 0 300 224"><line class="linec-axis" x1="52" y1="14" x2="52" y2="190"/><line class="linec-axis" x1="52" y1="190" x2="288" y2="190"/><line class="linec-thresh" x1="52" y1="119.6" x2="288" y2="119.6" style="stroke:var(--amber)"/><polyline class="linec-series" points="52,190 288,14" style="stroke:var(--red)"/><polyline class="linec-series" points="52,190 126,120 126,181 199,120 199,181 273,120" style="stroke:var(--sage)" stroke-dasharray="5 3"/></svg><ul class="linec-leg"><li><span class="vsw" style="background:var(--red)"></span>before</li><li><span class="vsw" style="background:var(--sage)"></span>after</li></ul></figure>
```
