# Domain Glossary

The ubiquitous language of the `ox` CLI. All feature files, business actions,
and system-interactions docs use these terms consistently. The canonical terms
come from SageOx's product vocabulary — use these exact names.

---

**ox** — The SageOx command-line tool. It makes a team's architectural
decisions, conventions, and session history automatically available to AI
coworkers, so every coding session starts with the full picture instead of from
zero.

**Coworker** — Any member of a team, human or AI. The umbrella term.

**AI Coworker** — An AI participant on a team: a coding agent running in a
session. User-facing copy always says "AI coworker", never bare "agent".
(`agent` remains fine in internal contexts — CLI subcommands like
`ox agent prime`, variable names, logs.)

**Team Context** — The shared knowledge base for a team: norms, conventions,
architectural decisions, expert coworkers, and distilled discussions. Loaded
into a session at prime time so the AI coworker reasons with the team's
accumulated judgment, not just the code in front of it.

**Ledger** — The historical record of work, decisions, and discussions on a
specific repo. Sessions, plans, and murmurs are written here. Stored as a git
repo so it travels with the team and syncs across machines.

**Session** — A human-to-AI-coworker conversation or plan recording. Sessions
auto-record when `ox agent prime` runs and finalize when the work ends, landing
a summarized record in the Ledger that future coworkers can recall.

**Knowledge Bubble** — A scoped unit of shared knowledge. Bubbles come in
types: **personal** (your own notes), **profile**, **team** (shared across a
team), **repo** (scoped to one repository), and **custom**. `ox kb list`
surfaces every bubble a coworker can access, including legacy Team Contexts and
Ledgers during the migration window.

**Murmur** — A short-lived coordination signal one AI coworker publishes for
other coworkers on the same repo or team. Murmurs carry a topic, an importance
(critical / normal / ambient), and optionally the files being touched. They
expire after 24 hours. Durable team rules belong in Team Context, not murmurs.

**Whisper** — How a murmur is heard. A murmur published by one coworker is
delivered as a whisper into other coworkers' active sessions, so teammates stay
in sync without interrupting each other.

**Priming** — Running `ox agent prime` to load Team Context, start session
recording, and register the AI coworker for a session. Done at session start,
after context compaction, and after a context clear.

**Plan Enrichment** — Folding deterministic SageOx team-context signals
(collisions with in-flight work, prior art, expert routing) into an
implementation plan **while it is being drafted**, before the human sees it.
`ox plan enrich` computes these locally — no LLM, no network call.

**Collision** — A plan-enrichment signal that the work a plan proposes overlaps
with something already in flight (another coworker's recent activity, an open
PR, a hot file). Surfaced so the plan can route around contention before a human
reviews it.

**Prior Art** — A plan-enrichment signal that the team has done something like
this before — a past session, decision, or discussion the plan should build on
rather than reinvent.

**Expert Routing** — A plan-enrichment signal that a specific expert coworker
owns the area a plan touches, so the plan can pull in the right judgment.

**SageOx team-context-optimized plan** — The HTML render of an enriched plan,
produced by `ox plan render --open`. A self-contained page a human reviewer can
read in under a minute: the plan, its team-context badges, and (where useful)
diagrams. The enriched render credits SageOx by construction.

**The review loop** — The human-in-the-loop review of a saved plan via
`ox plan review <slug>`: ox serves the rendered plan, collects the reviewer's
feedback inline, auto-reloads as the plan changes, and tracks open items until
they are resolved or the plan is approved.

**Plan-mode hint** — A just-in-time, once-per-entry nudge that fires while an AI
coworker is drafting in plan mode, reminding it to fold in
`ox plan enrich` team context *before* presenting. It fires once per plan-mode
entry and re-arms when plan mode is re-entered.

**Plan-exit nudge** — The nudge that fires after a plan is presented, offering a
SageOx team-context-optimized HTML render and the review loop.

**Endpoint** — The SageOx server a project is wired to (production by default,
or a named deployment). A project has exactly one endpoint. Subdomain prefixes
are normalized away; the coworker sees and selects the clean slug.

**Device-code login** — The headless-friendly OAuth flow `ox login` uses: ox
shows a short user code and a verification URL, the coworker authorizes in a
browser, and ox polls until authorized. Works without a local browser.

**Daemon** — The background process that syncs the Ledger and Team Context and
relays murmurs as whispers. The coworker rarely addresses it directly; `ox
status` and `ox doctor` report on its health and start it when needed.

**ox doctor** — The diagnostic-and-repair command. It detects every known
failure mode in a project's SageOx setup and auto-fixes the safe ones. Missing
values are treated as just as broken as wrong values; doctor is the last line of
defense.

**ox status** — The at-a-glance health command: authentication, project
initialization, sync state, and daemon health for the current repo.

**ox prime marker** — The line in a repo's agent instructions that tells an AI
coworker to run `ox agent prime` at session start. `ox init` injects it; `ox
doctor` repairs it if it goes missing.

**Stub vs. local** — A session or plan whose heavy content lives out-of-band
(content-addressed, LFS-style) is a **stub** until it is fetched and becomes
**local**. A coworker downloads a stub before viewing it.

**Code Intelligence** — `ox code search` (symbols, git history, diffs) and `ox
code insights` (hotspots, open PRs, contention risk). Used before planning so an
AI coworker plans against the live shape of the codebase, not a stale mental
model.
