# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`ox recap` — the concrete answer to "what value am I getting from SageOx?"** — a personalized, receipts-not-vibes report that points at the specific team knowledge which reached your work, in prose, never a vanity dashboard. It answers on two axes: the **temporal** one that a team of one gets on day one (your own recorded sessions are now searchable memory you can reload instead of re-explaining your codebase; decisions you captured resurface instead of being re-litigated; plans SageOx enriched flagged collisions with your own open work before you wrote code), and the **social** one a team adds on top (the Constitution, glossary, and team discussions that reached your sessions, quoted by name with the session as the receipt). Every claim carries a receipt — an artifact path, a session name, a plan slug, a commit SHA — and the report never invents time-saved or dollar figures. When value is still ramping it says so plainly and prescribes the two or three moves that start generating it. Called by an AI coworker it emits a JSON evidence bundle plus guidance to narrate the prose; in a bare terminal it prints an honest summary. Read-only, offline, no LLM in the CLI.

### Changed

- **Priming is dramatically cheaper on re-prime without losing any steering** — `ox agent prime` is called repeatedly in a session (start, after compaction, after `/clear`), and each call used to re-inject the full ~4,460-token preamble. A returning re-prime within the same context window now emits only what changed (new murmurs, new sessions, ledger status) and skips the static instructions the agent already holds — roughly 590 tokens instead of 4,460. The full preamble is still delivered in full on the first prime and on every `/clear` or `/compact` (the exact moments the agent's context was wiped), guarded by a required-directive conformance test so no steering guidance can be silently dropped. The three longest guidance blocks were also tightened to keep their command and trigger while moving the rationale to `ox guide`, and prime now records its own injected-token cost so the budget is measurable.

- **`ox plan` HTML render is offered far more assertively — and never opens a browser without your say-so** — when a plan touches real team context (collisions with open work, prior art, expert routes), leaving plan mode now renders the SageOx team-context-optimized HTML in the background immediately, so the artifact is ready the instant you want it, and the nudge leads with what a hand-authored plan would drop. Opening the browser always waits on an explicit yes via a question to you first — a structural guarantee, not a convention. Trivial plans stay silent so the prompt is never noise.

### Improved

- **Plans are written for the reader who approves them.** Every AI coworker now drafts a plan with the decision up top — the shape, the tradeoffs, and the risk you need to approve it in ten minutes — and moves the exact files, edits, and gotchas into an "Implementation notes" section at the end, for the coworker that builds it. The rendered plan collapses that detail into an appendix you never have to open, so a ten-minute review actually takes ten minutes.

- **`ox doctor` now flags a recording whose session link disagrees with the recording itself** — the one state that silently breaks links already written into your commits and pull requests. Reported, never repaired automatically: which link is correct depends on what has already been shared, so doctor names the conflict and leaves the call to you.

### Fixed

- **Every recording keeps one permanent session link, even when its upload has to be retried.** A retried or recovered upload could assign a second link to a session that already had one, so the references written into commit trailers, pull requests, and plans stopped resolving — and two copies of the same session could disagree about which one it was.

- **Ledger sync no longer stalls on machines that sign git commits.** If your git config requires a passphrase to sign, sync could stop partway with no terminal available to answer the prompt, leaving work stranded locally.

## [0.12.0] - 2026-07-18

Decision Records get first-class team context, and three real reliability gaps in sync and search are closed for good.

### Added

- **Universal session links on every PR and commit** — recordings now mint their stable `ses_` ID the moment they start (not at stop), so every artifact carries the durable `https://sageox.ai/c/<session-id>` conversation link from the first minute: commit trailers (`SageOx-Session:`, written by the existing prepare-commit-msg hook), the PR-body last line (a one-line verbatim directive in prime output — squash merges now land the trailer in main's history), and rendered plan footers. Aborting a recording countermands the directive and stops new links immediately; the existing `attribution.session` setting turns the whole surface off. Crashed sessions keep their links stable — the daemon reuses the start-minted ID from the raw-header carrier instead of inventing a new one. Legacy recordings and historical name-based URLs stay valid.

- **`ox decision enrich` — team context for Decision Records** — creating or updating an ADR/DDR now starts from the team's actual history instead of a blank page. Run it with `--topic` before drafting (related decisions, the corpus's numbering and template conventions, prior sessions, ready-to-paste citations) or `--file` before editing (code drift since the decision's date, amendment anchors, and any reference that no longer resolves — the "cited decision #9 that exists in no document" failure class is caught before commit, not after). Zero LLM or network cost; ox never edits the DR — the agent authors every word, and every citation ox emits is one it just verified. DRs are discovered zero-config from conventional dirs (`docs/adr`, `docs/decisions`, …) or the committed `decision.paths` setting.
- **Plans now tie back to the decisions that shaped them** — `ox plan enrich` surfaces this repo's own Decision Records relevant to a plan, and the rendered plan marks their mentions inline (a subtle marker, context — never a verdict). Running `ox decision enrich` in this repo immediately surfaced nine duplicated ADR numbers our own corpus had accumulated unnoticed.
- **Every coding agent is primed for decision hygiene** — in repos that keep DRs, `ox agent prime` teaches the consult-and-credit contract: enrich before drafting, credit teammates by name with verifiable refs, amend Accepted decisions with dated markers instead of rewriting history, and keep vendor credit subtle (invisible source refs + the scored commit trailer; at most two visible SageOx credits per DR).
- **`ox doctor` catches a broken Decision Records setup** — a typo'd or invalid `decision.paths` entry used to fail silently and quietly turn off enrichment; `ox doctor` now flags it before it costs you a wasted `ox decision enrich` run.
- **Find your team's decisions fast in code search** — `ox code search --decisions` narrows results to just this repo's ADRs/DDRs, and every decision-record hit is now labeled wherever it shows up in search so you can spot it at a glance.
- **`ox session prune` clears out local-only sessions you no longer need** — removes finalized recordings that were never pushed to the ledger, keeping your local session store tidy. `--all` also clears paused, canceled, ghost, and orphaned sessions; `--dry-run` previews first; `--force` skips the confirmation prompt. Sessions already on the ledger are never touched — use `ox session remove <name>` for those.
- **`ox plan enrich --topic`** — consult team context before you've written a word of the plan (collision, prior-art, expert-routing), the same pre-draft pattern `ox decision enrich` already has. Add `--files` once you know which files are involved for open-PR/expert-ownership signals too.
- **`ox session stop --current`** and **`ox session list --repo <path>`** — stop your own recording without knowing its agent ID, and list another repo's sessions without `cd`-ing there first.

### Changed

- **Fresher skills and rules on every `ox init` / `ox doctor --fix`** — includes a new `ox-session-review` skill for auditing session quality, a reorganized `ox-plan` skill, and a new rule that points every agent to where your team's shared conventions actually live.

### Fixed

- **Sync no longer wedges permanently if you have git commit signing turned on** — ox commits non-interactively (session finalize, murmurs, ledger housekeeping) and can't answer a signing passphrase prompt, so a global `commit.gpgsign` setting used to make every one of those commits fail forever with no visible error outside `ox doctor` — sync would just quietly pile up. ox now disables signing on its own commits only (your own repos keep signing normally) and self-heals any ledger or team context already stuck this way.
- **`ox code search` recovers on its own from a rare false alarm** — under heavy concurrent use it could occasionally report a scary, permanent-looking index corruption error even though ox already knows how to repair it; it now retries briefly first, so the self-heal kicks in instead of the false alarm.
- **`ox doctor` no longer skips checks because the daemon wasn't running** — it now starts the daemon first, the same way `ox sync` already does, so checks like stuck-session detection actually run instead of being silently skipped.
- **`ox doctor --gc` no longer hangs** — the built-in repair for a stuck ledger used to wait on a signal that could take minutes to arrive but gave up after a tiny fraction of a second, every time. It now starts the repair in the background immediately and tells you where to check on progress, instead of just failing.
- **`ox query` no longer drops you mid-session** — a background credential refresh that already covered most commands didn't cover `ox query`, so a long session could suddenly demand you log in again even though you never logged out. It refreshes the same way every other command does now.
- **A ledger that's genuinely diverged from the team now recovers instead of staying stuck** — when your local session history and the team's have truly split (not just fallen behind), ox used to retry the same failing sync forever with no way out short of asking for help. It now recognizes the difference, warns you sooner the longer it goes unresolved, and repairs itself automatically once it's been stuck a few hours — any work it recovers along the way is left for you to review, never auto-committed.
- **`ox distill` no longer discards a whole run over one bad day** — a single failed summary (a rate limit, a hiccup) used to throw away everything the run had already produced. It now keeps what succeeded and only reports the part that didn't.
- **`ox doctor` now catches a PR that's missing its collaboration credit before it merges** — commits already get it automatically, but the PR description didn't, and a description becomes the permanent record once merged. A new check flags the gap while there's still time to fix it.
- **`ox agent tasks done`/`cancel` now accepts `--result`/`--reason`** — these were documented and fully wired end to end but silently rejected by the CLI; completion and cancellation notes now actually save.
- **Clearer errors when an agent ID looks like something else** — pasting a session name (from `ox session list`) where an agent ID belongs now says so directly, instead of a generic "unknown command."
- **No more repeated keychain access prompts** — checking whether the OS keychain works used to create a throwaway test entry and immediately delete it, every time, which never let macOS remember your "Always Allow" choice. It now checks the real credential entry instead, so access, once granted, stays granted.

### Security

- **Dependency security patch** — updated `golang.org/x/crypto`, `x/net`, `x/sys`, `x/term`, `x/text`, and `goldmark` to close reachable CVEs in code paths ox actually exercises.

## [0.11.1] - 2026-07-01

Review feedback on plans is sacred — this release makes sure none of it can be lost, no matter what happens to the review server, the browser tab, or the plan itself.

### Fixed

- **The review page now tells you the moment feedback stops being saved** — if the review server exits (idle timeout, Ctrl-C, a crash), the page immediately shows a clear "offline — feedback is NOT being saved" banner with the exact restart command to copy, instead of looking live while submissions silently fail. Unsent marks stay safely in the browser and are restored on reconnect.
- **Restarting a review picks up exactly where it left off** — `ox plan review` serves each plan at a stable address, so the tab you already had open reconnects on its own (marks intact) when you restart, and re-running the command against an already-open review reuses the running one instead of starting a stranded twin.
- **Reloading the plan while the server is down still shows the plan** — the page keeps a local copy of itself, so a reload lands in clearly-marked disconnected mode instead of a browser error screen.
- **Your marks follow the plan as it changes** — when an AI coworker updates a plan, open review notes are re-anchored onto the content they referred to, so a reworded heading no longer detaches your comment. Notes whose content truly disappeared stay visibly open and keep appearing in every digest — nothing is ever dropped.
- **Ask about review feedback later** — reviewer notes on plans now appear in local search, so "what did Sam flag on the auth plan?" finds the reviewer's actual words, not just the plan.
- **Two coworkers resolving review items at the same time no longer lose one of the updates.**

## [0.11.0] - 2026-07-01

### Added

- **Publish a plan as a Claude Code Artifact** — `ox plan render --artifact` exports a plan as a single self-contained page that renders right inside Claude Code with no network access: diagrams are inlined and nothing loads from a CDN. Its prior-art links still work, so a shared plan stays a jumping-off point back into your team's history.
- **Live human review on a plan, before any code is written** — `ox plan review` now waits for your reviewers and shows their feedback the moment it lands, so an AI coworker can pause for a real decision instead of guessing. Every note is saved with the plan.
- **Plans render charts, not just tables** — line, bar, area, and scatter charts now appear inline in a rendered plan, so a plan full of numbers shows the trend at a glance.
- **Quicker to navigate a plan** — prior-art references (related PRs and past sessions) are clickable links back into your Ledger, and code blocks are syntax-highlighted.

### Changed

- **Sharper plans from every AI coworker, not just Claude** — plan rendering now coaches any agent (Claude, Codex, Gemini) to add a diagram or UI sketch where prose was hiding one, and to fold that in while the plan is being written. It also stops asking for a diagram when a chart already tells the story.
- **HTML plans open the right way on their own** — ask for an HTML plan and ox opens the polished, team-context-aware view instead of a hand-rolled one-off (set `SAGEOX_PLAN_HTML` to opt out).
- **Smarter visual suggestions and cleaner layouts** — the renderer points out where a visual would help based on what the plan says, and packs busy sections into tidier layouts.
- **Full dependency license transparency** — ox now ships a `THIRD_PARTY_NOTICES.md` listing every dependency and its license.

### Fixed

- **Background sync no longer gets stuck on a broken rebase** — if a synced repo's git state wedges (even the rare "zombie" case git can't unwind on its own), the daemon now clears it automatically and `ox doctor` repairs it, so your team's history keeps flowing instead of silently stalling.
- **`ox sync --team` shows accurate per-team status** — real results and details for each team, instead of one lumped-together summary.
- **`--title` and `--json` now work when importing documents and media** — set a title on import, and get the new recording's id back in `--json` output.
- **Clear message when an access token has expired** — token checks no longer hang or retry in silence; ox tells you to sign in again.
- **No stray telemetry errors after you log out** — ox stops sending background telemetry (and the errors that came with it) once there's no active session.

## [0.10.1] - 2026-06-15

### Added

- **ox installs its skills and commands into Codex and Droid, not just Claude Code** — capability injection now follows an explicit two-layer model with a cross-agent conformance test, so the portable "floor" (prime, consult-first recall, plan enrichment, session review, cart lifecycle) reaches every supported agent, and the per-agent ergonomic surfaces can no longer silently drift or orphan.
- **`ox import --kb <slug>` brings media and video-URL import to Knowledge Bubbles** — import now reaches bubbles at parity with team contexts (mutually exclusive with `--team`), streaming large media files through a presigned upload so they never load fully into memory.
- **`ox code prs` ranks indexed pull requests for triage** — a new deterministic view (no LLM, no live API) that surfaces the most-stalled open PRs first, each row carrying age, idle-days, comment/reviewer/discussant counts, and labels (`--sort age|activity`, `--state`). Backed by ADR-019 phase-1 resolved `symbol_edges`, which also make `ox code calls` / `calledby` more accurate.
- **Knowledge Bubbles now appear in `ox daemon status`** — the daemon reports every bubble it has synced to disk (slug, type, freshness), plus a badge showing whether *this* daemon keeps them fresh or a sibling daemon does. Previously the daemon synced bubbles silently with no way to see what was happening.
- **`ox doctor` gained knowledge-bubble repo health checks at parity with ledgers and team contexts** — detects bubbles the cloud lists but that were never cloned, bubbles wedged in a stuck merge/rebase (which silently block sync for every project), and bubbles whose sparse-checkout dropped `.sageox`. Repairs route through the daemon so they never collide with an in-flight sync.

### Changed

- **Knowledge Bubbles are now first-class in daemon status output** — the JSON `bubbles[]` array and a human-readable "Knowledge Bubbles" section make it clear which bubbles are local, fresh, and who is responsible for syncing them.
- **Rendered plans own their SageOx brand surfaces** — `ox plan render` now emits the OX icon, a single subtle corner wordmark, and deterministic inline reference markers itself, so agents no longer hand-roll look-alike branding that drifts or duplicates. A marker means "SageOx has context on this," never a verdict — whether a plan aligns with or amends a decision stays the agent's call.

### Fixed

- **`ox daemon` no longer holds one open file descriptor per tracked file** — the recursive fsnotify/kqueue watcher opened a descriptor for every tracked file *and* directory, so FD usage scaled with repo size (≈11k on a large repo — enough that a few daemons together approached half the machine's FD table). It's replaced by lightweight `git status` polling that holds zero watch descriptors, honors `.gitignore` for free, and feeds the exact same downstream change pipeline. `ox doctor` also gained an absolute FD-pressure ceiling so a watcher-class regression trips regardless of the shell's raised limit.
- **`ox daemon status` reports garbage-collection state correctly** — GC timestamps now persist to disk, so a daemon restart no longer reads every workspace as "gc due" and needlessly reclones it once per hour. The misleading "gc in 6d ago" / "(last 3d ago ago)" wording is fixed.

## [0.10.0] - 2026-06-11

### Added

- **Cross-agent HTML plans with a human review loop** — `ox plan` renders a team-context-enriched plan as a self-contained HTML page for *any* coding agent, not just Claude Code, backed by a catalog of reusable visualizations (timelines, collision maps, sequence diagrams). A new review loop (`ox plan review`) serves the plan for inline human feedback, so a reviewer can shape the approach *before* any code is written — and the feedback is captured back to the ledger.
- **Just-in-time `ox plan enrich` hint while drafting** — during plan mode (Claude Code), `ox` nudges the agent to fold deterministic team context (collisions, prior art, expert routing) into the plan *while it is being drafted*, so enrichment lands in the first draft a human sees rather than being bolted on after.

### Changed

- **Natively aligned Knowledge Bubbles in `ox status`** — the bubble listing now uses a label-column layout with owner and bubble names aligned in their own column, so the section reads as a clean table rather than ragged, variable-width rows.

### Security

- **Adapter integrity, a single redaction chokepoint, and environment + clone hardening (ADR-022)** — installed AI adapters are integrity-verified before use, every redaction path now funnels through one chokepoint that is far harder to bypass, and both environment-variable handling and team-context clones were hardened against credential leakage and untrusted-prompt injection.

### Fixed

- **`ox agent tasks` no longer panics when called with no subcommand** — invoking `tasks` bare previously sliced an empty argument list and crashed; it now guards the slice and prints usage.

## [0.9.1] - 2026-06-08

### Added

- **`ox plan` — team-context-enriched implementation plans** — turns a plan an AI coworker drafts into one that knows where the team is going. `ox plan` annotates each part of a plan with deterministic, locally-computed signals (zero model tokens): **collisions** (a file you're about to touch is in a teammate's open PR — or one they murmured about minutes ago, before any commit exists), **prior art** (a teammate already planned or did this — surfaced from the ledger), and **expert routing** (who owns this area, with cited evidence, so you ask the right person). The plan-mode agent then authors the judgment calls — does this align with or conflict with a team decision — reasoning over a context bundle ox assembles. ox makes no model call itself; the inference cost lands in plan mode where you already expect it. Finalized plans are saved to the team ledger (`data/plans/`) as first-class, searchable artifacts, so today's plan becomes tomorrow's prior art. On Claude Code, a renderer skill turns the enriched plan into a beautiful self-contained HTML page for fast human review; other agents get the same enrichment via guidance and the `ox plan` CLI. Configure with `plan.save` and `plan.html` (`off`/`recommend`/`always`), or `SAGEOX_PLAN_HTML` per-run.

### Changed

- **`ox status` Knowledge Bubbles, denser and more useful** — the bubble section used to print a bare count (`9 (8 team, 1 repo)`) and then repeat every team again under "Other Team Contexts" with a full filesystem path on each row. It's now an owner-grouped listing that matches the rest of `ox status`: each owner is a row (`@slug` + display name, names aligned in a column) with its knowledge bubbles as indented sub-fields — the bubble type as the label, a compact color-coded freshness status as the value (`✓ 2h`, `⚠ 6 uncommitted`). Owners are separated by whitespace (cards that grow as owners gain more bubbles), the shared on-disk prefix is printed once, and slugs are never truncated. Since nearly every bubble is private, private is the silent default and only **PUBLIC** bubbles are flagged (bold, in the public accent color). Add `--verbose`/`-v` to reveal the opaque IDs and full paths.

## [0.9.0] - 2026-05-28

### Added

- **Pause and resume sessions** — `ox session pause` and `ox session resume` let you suspend an in-progress recording and pick it back up later without losing context, with a proper session lifecycle (active → paused → resumed → stopped) underneath. Long-running work that spans breaks, meetings, or machine restarts is now captured as one coherent session instead of fragmenting.
- **Ephemeral mode for throwaway environments** — set `OX_EPHEMERAL=1` to run ox in a capability-based mode tuned for short-lived sandboxes (Codespaces, CI, dev containers): no daemon assumptions, session finalize syncs inline, and Codespaces is now detected reliably. The old per-command `--ephemeral` flag is deprecated (a flag on a single command silently drifted back to non-ephemeral on the next invocation in the same shell) — set the environment variable instead.
- **Personal access token auth via `SAGEOX_TOKEN`** — non-interactive environments can authenticate by exporting a SageOx PAT in `SAGEOX_TOKEN`, no browser login required. ox warns on stderr as the token nears expiry so automation doesn't fail silently.
- **Faster clones on large repos** — code-search and ledger clones now support shallow, partial (blobless), and shared-alternates fetching, cutting both wall-clock time and disk for big histories.
- **Performance metrics in `ox doctor` and the daemon** — `ox doctor` now reports timing for its checks and the daemon exposes per-subsystem performance counters, making slow setups diagnosable instead of mysterious.
- **`/security-review` pipeline** — a diff-scoped, two-tier (deterministic OSS scanners + AI hunter/validator) security review you can run on demand. Never blocks merge; surfaces input-handling bugs, redaction-bypass risks, daemon IPC authz holes, and supply-chain issues. See `security/README.md`.
- **Durable session commit + PR/issue linkage** — sessions commit atomically and maintain a reverse index linking each session to the PRs and issues it touched, with stale-URL repair. Past work is now discoverable from the PR/issue side, not just the session list.
- **Knowledge Bubble as a workspace primitive** (ADR-017) — the resolver, config, and file-locking foundation for treating personal/team/repo knowledge ("bubbles") as first-class workspaces.
- **Customer-facing env-var namespace convention** — sageox-mono ADR-047 ("Customer-Facing Env Var Namespace") is the canonical home for the rule that customer-facing SageOx env vars use `SAGEOX_*` (product/auth/network identity) and `OX_*` is reserved for CLI-local behavior flags. The legacy customer-facing `OX_TOKEN` / `OX_ENDPOINT` names are removed; `internal/auth/env_naming_test.go` guards against re-introduction. The matching sageox-mono ADR-046 ("Credential Classes and Principal Normalization") is now Accepted with companion sections D7-D10 covering the PAT validation contract, principal `AuthMethod`, customer-facing surface, and cryptographic-separation targets.
- **Local recall on every prompt** — the UserPromptSubmit hook prepends `ox query --local` recall, local-only by default (ADR-018).
- New `hooks.userpromptsubmit.cloud_query` config key (default `off`) opts the UserPromptSubmit hook into a parallel SageOx cloud query. When enabled, prompt content is redacted via the session secrets pipeline before any byte leaves the machine, and the cloud path silently degrades to local-only if `ox login` has not run. `ox doctor` reports the effective value and the privacy/recall tradeoff.

### Changed

- **AI adapters stop fast on terminal errors** — when a host agent hits an unrecoverable condition (e.g. a rate-limit/quota wall), the adapter detects it and stops promptly instead of retrying into the same wall.
- **Daemon team discovery relaxed from every 5 minutes to hourly** — less background chatter and CPU for a signal that changes rarely.

**`ox session audit` and `ox session redact` now require an explicit scope**

Bare invocation used to silently hydrate and scan the entire ledger — a multi-minute LFS Batch fetch that could process hundreds of sessions without the operator's consent. The command now refuses to run without one of:

- `--session <name>` (repeatable) — limit to specific sessions
- `--since <date>` / `--until <date>` — half-open lexicographic window against the ISO-prefixed session name (e.g. `--since 2026-04-01`)
- `--all` — explicit opt-in to the full-ledger sweep

`--all` is mutually exclusive with the narrowing flags. Mistyped `--session` names error before any hydration begins, so a typo no longer triggers minutes of unnecessary LFS fetch followed by an error. The full-ledger sweep that used to fire on bare `ox session redact` is preserved verbatim under `--all`.

### Fixed

**`ox doctor` redaction-debt guidance now points at a command that exists and works**

The 0.8.1 `ledger-redaction-debt` doctor check told the user to run `ox session redact <session>` for interactive cleanup of a quarantined session. No such positional surface existed — cobra silently dropped the positional and scanned the entire ledger. The fix is two-part:

1. The doctor message now emits a copy-pasteable `ox session redact --session <name>` command for each quarantined session (up to five, with a "+N more" hint above that).
2. `ox session redact --session <name>` now also walks `.sageox/cache/quarantine/<name>/` for the targeted sessions. For JSONL quarantine, it redacts at the quarantine path via the canonical chokepoint, moves the file back to `sessions/<name>/`, appends a `RedactionPass` to `meta.json`, and removes the debt marker on success. Non-JSONL quarantine is listed as "manual scrub required" — the chokepoint applies the raw-writer redaction stack and expects JSONL.

Before this PR, the doctor warning pointed at a command that couldn't help: the forward path (`prepush_autoredact.quarantineUnredactableFindings`) had moved the bytes OUT of `sessions/<name>/`, and the backward path only walked `sessions/`. Recovery required manually inspecting, scrubbing, and moving files back. Now `ox doctor` → copy-paste → done.

**Daemon and sync reliability**

- The daemon no longer deletes its IPC socket file when a superseded instance shuts down, so a freshly-started daemon stays reachable instead of leaving the CLI unable to connect.
- Code-search self-heals a corrupt bleve `_mapping` automatically and falls back to SQL-only insights, instead of failing the query outright.
- Observability exports now attach a fresh JWT Bearer token per OTLP request, fixing dropped telemetry once the initial token expired on long-running daemons.
- Ledger pushes that wedged in a "U" (unmerged) state are now surfaced and audited rather than silently stalling, and the summary push no longer writes to a `/tmp` scratch path.

### Security

**Redaction-debt markers are now validated against path-traversal**

The quarantine integration above reads `.sageox/cache/redaction-debt/<session>.json` markers to locate quarantined bytes. An attacker with write access to that directory could previously craft a marker whose `quarantine_paths[].to` pointed outside the ledger (e.g. `../../../tmp/owned.jsonl`); when the operator next ran `ox session redact`, the marker-driven `os.Rename(quarantineAbs, inPlaceAbs)` would overwrite arbitrary files reachable by the operator's UID. Threat model is narrow — the attacker already needs ledger write — but it escalates "ledger-writer" to "arbitrary-file-writer at operator UID."

The fix rejects any marker whose `session_name` contains a path separator or `..`, whose marker filename doesn't match the embedded `session_name`, or whose `quarantine_paths` entries aren't direct children of `sessions/<sess>/` (source) and `.sageox/cache/quarantine/<sess>/` (destination). Defense-in-depth `safeRelativeUnder` checks at the `os.Rename` / `os.Open` call sites block any path that slips past the marker-shape guard.

Closes #608.

**`.claude/settings.json` no longer rewritten on every session**

Before this fix, ox could silently rewrite a user's `.claude/settings.json` on every Claude Code lifecycle event (via the daemon's 30-minute autofix tick, and on session start when the hook set drifted). The rewrite came from running the file through `encoding/json`'s defaults: literal `<`, `>`, `&` inside a permission rule got escaped to `<`, `>`, `&`; hand-written `\uXXXX` source escapes were decoded to literal runes; trailing newlines were stripped; and indentation inside opaque blocks like `permissions` was normalized to two-space. Each rewrite produced bytes that drifted from on-disk on the *next* pass too, so the file churned in a loop even when no content had actually changed.

The fix replaces the encoder with one that has `SetEscapeHTML(false)` and preserves a trailing newline, and switches the "already canonical?" guard from byte equality (which the previous tests proved was satisfiable in lockstep with the encoder's own output but never against real user content) to a combination of strict-shape detection plus semantic content comparison. Result: doctor and the daemon autofix now leave user-authored files alone if Claude Code can read them, and only rewrite when the on-disk hooks shape is one Claude Code actually rejects.

Regression tests seed adversarial inputs that would have failed the byte-equal guard on every pass — literal HTML characters in permission rules, tab indentation, trailing newlines — and assert the file is byte-identical across two consecutive checks.

## [0.8.1] - 2026-05-12

### Fixed

**Pre-push credential gate no longer blocks routine pushes**

0.8.0 introduced a pre-push secret scanner that scanned every file in the push range and refused the push on any finding. In practice this had two failure modes:

1. The scanner ran against `data/github/**` PR/Issue caches. PR titles, bodies, and comments often contain text that matches credential heuristics (sample `Authorization: Bearer` snippets, phrases like "STS session key", and other public bytes already on GitHub). `ox doctor` reconcile failed with *"Push refused: 3 credential pattern(s) detected in 2 file(s)"* pointing at JSON the user did not author. The recovery message named `ox session audit` / `ox session redact` — commands that only operate on `sessions/`, leaving the user unable to follow the instructions for paths outside `sessions/`.

2. Even after scoping, a single session with a finding the chokepoint had missed would refuse the entire push, holding up every other session and unrelated commit.

The gate is now scoped + recoverable + never-blocking:

- **Scoped:** the scanner only inspects paths under `sessions/`. `data/github/**` (verbatim cache of bytes already public on GitHub), `kb/**` (user-curated), and `team-context/**` (user-authored markdown) are intentionally out of scope. The companion writer-side redactor that 0.8.0 wired into `WriteGitHubPR` / `WriteGitHubIssue` is unwired for the same reason.
- **Auto-recovers:** on a finding in a session's JSONL, the gate rewrites the file in place through the canonical chokepoint, appends a `RedactionPass` audit-trail entry to the session's `meta.json`, amends the holding commit, and re-scans. The push then proceeds with scrubbed bytes.
- **Quarantines what can't be auto-redacted:** findings in non-JSONL session paths (notes, summaries, transcripts) are moved to `.sageox/cache/quarantine/<session>/` — bytes preserved verbatim on disk — and dropped from the holding commit. The rest of the push proceeds normally; other sessions and unrelated commits sync as before.
- **Surfaces persistent state in doctor:** a new `ledger-redaction-debt` check reports every quarantined session with the affected detectors and next-step recovery commands. The check is read-only; recovery is a deliberate user gesture (`ox session redact`, or manually inspect + restore from `.sageox/cache/quarantine/`).
- **Never blocks:** the gate always returns nil. Recovery errors are logged, not propagated.

`OX_ALLOW_SECRETS=1` still short-circuits the recovery pipeline for explicit "publish as-is" overrides.

New tests pin the scope contract, the auto-redact happy path, the quarantine path with data preservation, and the doctor surface.

## [0.8.0] - 2026-05-12

### Added

**Modular team rules with first-class context-budget accounting**
- Team rules now live as one-file-per-concern under `<team-context>/agents/rules/<topic>.md` (subdirectories supported, walked recursively). Mirrors the muscle memory of Claude Code's `.claude/rules/` and Cursor's `.cursor/rules/`, scaled up to team scope. Frontmatter spec covers `name`, `description`, `repos`, `audience`, `visibility`, `status`, `from-discussion`. `visibility: always` rules are inlined in `ox agent prime`; `visibility: indexed` rules emit a catalog entry only and the agent reads them on demand. Backward-compat fallback to `coworkers/rules/` for any teams that adopted that location early.
- `ox agent prime` XML now reports a `<context-budget>` block split by content source (sageox / team / project). The split lets SageOx be measured on its own tool overhead instead of conflating it with team-authored content. It flows through every layer: per-prime budget, per-heartbeat per-source aggregation, daemon-side cumulative tracking, and `ox agent list`'s per-source footer. The schema is open — adding a new content source takes one new constant in `internal/prime/types.go` plus tagging emit sites.
- New `<rule-promotion-guidance>` block in prime XML proactively coaches AI coworkers to ask before publishing a project-local rule team-wide ("this looks like it could apply to your whole team — want me to also add it under `<team-context>/agents/rules/`?"). Default to asking; never silently publish.
- New `<team-rules-budget>` block reports the running token cost of `always`-tier rules so teams self-regulate rule-library size.
- Regression-test guard on minimal-prime SageOx overhead (currently ~600 tokens, ceiling 1500). A future change that quietly adds 5K of `<instructions>` blocks itself on review.

**Bundled topical guides via `ox guide`**
- New `ox guide [topic]` reads from `//go:embed`'d markdown — no internet required, no docs-site dependency. Five starter guides ship: `team-rules`, `agents-md`, `team-context`, `murmur-vs-rule`, `getting-started`. `--raw` flag emits unrendered markdown for AI agents that prefer plain text.
- `ox init`, `ox import`, `ox murmur`, and `ox agent team-ctx` --help now cross-reference the relevant guide so users discover them in context.
- Prime XML's commands table includes a new "learn how to do something in ox" row pointing at `ox guide [topic]`.

**Adapter rule installation under `.claude/rules/sageox/` namespace**
- `cmd/ox-adapter-claude-code` now installs a second rule alongside the canonical `.claude/rules/ox.md`: `.claude/rules/sageox/use-team-context.md` — a "MORE RULES → here" pointer that teaches the agent to discover team rules in their canonical home rather than syncing every rule into every cloned repo. No mirror semantics, no conflict resolution, no per-adapter sync coverage gap. The `sageox/` namespace reserves room for future SageOx-installed rules without polluting `.claude/rules/` with `ox-feature1.md`, `ox-feature2.md`, ... siblings.
- `cmd/ox-adapter-droid` mirrors the same pattern under `.factory/rules/sageox/`.
- `handleUninstallRules` walks `sageox/` and removes only ox-stamped files (preserves user-authored content), then cleans up the empty namespace dir. Works around an agentx-v0.1.10 limitation where `ExtractCommandHash` only inspects the first line and misses files with frontmatter.

**Rules-support scaffolding for the remaining adapters**
- New `rules.go` files for `ox-adapter-codex`, `ox-adapter-amp`, `ox-adapter-aider`, `ox-adapter-gemini`, `ox-adapter-opencode`, and `ox-adapter-pi` — each documenting the May 2026 state of that agent's rules surface. None of these agents has a Claude-Code-style modular *behavioral* rules directory today (Codex's `.codex/rules/` is for Starlark execution policies, not behavioral content). The handlers are stub no-ops, NOT wired into `main.go`, and the adapters do NOT advertise `CapRulesInstaller`. When upstream adds modular rules, flipping the wiring on is a 3-line change per adapter.

### Changed

**Reference docs regenerated**
- `docs/reference/` is now in sync with current cobra command definitions. Adds `guide.mdx`, `session/repair-meta-summary.mdx`, and `session/token-optimize.mdx`. Drops a stale `distill.mdx` that was never registered as a root command.

**Adapter ergonomics**
- The Amp adapter now records sessions via a user-global `ox-bridge` plugin. No per-repo configuration needed — install once and every Amp session in every cloned repo is captured automatically.
- `adapter-pi` now detects its host agent's identity from the `PI_CODING_AGENT` environment variable instead of fragile process-name heuristics.
- `--format=json` is now accepted as a hidden alias for `--json` across the CLI, so scripts written against either flag work everywhere.

### Fixed

**Daemon CPU & resource hygiene**
- Eliminated four recurring hot-loop CPU patterns that could pin a core under steady-state idle. Affected paths: failed session-upload retry, project-watcher tear-down, IPC reconnect, and friction-event drain.
- Closed a file-descriptor leak that occurred when the daemon ended up watching a directory that turned out to be gitignored. Long-running daemons no longer accumulate FDs proportional to gitignored-subdir churn.

**Doctor accuracy**
- Credential checks now run after the post-EEQI bootstrap so doctor no longer flags freshly-rotated credentials as missing on the very next run; user-facing guidance was also corrected to point at the right remediation command.
- Doctor scan gained correct session scoping, automatic hydration of LFS-stub recordings, catalog-identity verification, and an append-only redaction trail so previously-redacted content stays redacted across re-scans.
- `ox doctor --force-session-uploads` now actually re-uploads past failed sessions instead of being a silent no-op.

**Session reliability**
- `ox session stop` now writes the prompt + pointer commit inline so finalize is atomic. Previously the two writes could interleave with daemon work and leave a session half-committed for up to a minute.

**Security & redaction**
- Additional credential-redaction patterns close gaps in friction-event sanitization and team-context git-URL handling. Strengthened path-traversal, auth, and LFS size-bound checks per the latest internal review.

**Code search resilience**
- `codedb` self-heals a corrupt bleve sub-index without forcing a full reindex. On large repos this drops recovery time from "several hours" to "2–5 minutes."

[0.9.1]: https://github.com/sageox/ox/releases/tag/v0.9.1
[0.9.0]: https://github.com/sageox/ox/releases/tag/v0.9.0
[0.8.0]: https://github.com/sageox/ox/releases/tag/v0.8.0

## [0.7.2] - 2026-05-04

### Added

**Session summarization is now configurable and observable**
- New `agent.summarizer` setting picks who runs the LLM that summarizes a session at stop. `inline` (default) runs it in the calling agent's already-warm prompt cache — cheap, but blocks the user for ~30–120s. `delegated` runs it in the daemon as a background subprocess — non-blocking, but pays the full input-token cost on every stop. `off` skips LLM summarization. `cloud` is reserved for future SageOx cloud-side summarization.
- The legacy `SAGEOX_ASYNC_SESSION_UPLOAD` and `OX_SESSION_INLINE_SUMMARY` env vars still work for one release as deprecation aliases, with a warning pointing at `ox config set agent.summarizer`.
- `ox session stop` now finalizes automatically when you exit Claude Code. Previously the SessionEnd hook fired but had no handler, leaving recordings stranded in the cache for up to 24 hours until the daemon's anti-entropy sweep noticed.
- New `summarization` telemetry event captures input/output tokens, model, duration, and quality score for every delegated summarization call. When the LLM-as-judge runs, its tokens piggyback on the same event.

### Changed

**Cheaper session summarization on the delegated path**
- Delegated summarization now defaults to Claude Haiku 4.5 instead of inheriting the user's local default (typically Sonnet). The summarization task is structured JSON extraction over a fixed schema — well within Haiku's capabilities and 5–15× cheaper. `OX_SUMMARY_MODEL` overrides the default.
- The summary-input optimizer slims tool entries to `{type:"tool_mark", description:"...", count?:N}`. Agent-authored `description` strings (Bash, Agent, Task, WebFetch, ...) are kept because they're already a one-line statement of intent and ideal as `key_action` candidates; tool name, raw inputs, and outputs are dropped. Tool calls without a description (Edit, Read, Write, Glob, Grep, ...) drop entirely — assistant prose names those actions reliably. Adjacent calls with the same description collapse via `count` (typical: a polling loop). On a realistic 300-entry session this is roughly an 80% byte/token reduction over the previous shape.

[0.7.2]: https://github.com/sageox/ox/releases/tag/v0.7.2

## [0.7.1] - 2026-05-03

### Fixed

**Daemon reliability**
- File watchers no longer leak a file descriptor per project file under long uptimes — the per-file handles in `ProjectWatcher` (fsnotify userspace mirror) are now released on directory teardown (#580).
- `ox murmur` file-change notifications now respect `.gitignore`, so build artifacts and editor temp files no longer spam teammates (#581).

**Session UX**
- Sessions whose meta entry was missing a title (rendered as "Summary unavailable" in the UI) are now repaired automatically by the daemon (#578).

[0.7.1]: https://github.com/sageox/ox/releases/tag/v0.7.1

## [0.7.0] - 2026-05-01

### Added

**Globally unique session recording IDs**
- Every session recording now carries a stable `ses_<UUIDv7>` identifier in `meta.json`, independent of path or name. Renames, moves, and re-imports no longer change identity.
- Legacy recordings without the field get a deterministic `ses_<UUIDv5>` derived from `(repo_id, session_name)` via the `EffectiveSessionID()` accessor — client and server compute the same value byte-for-byte, so no backfill is required.
- `ox doctor --fix-slug=session-ids` opt-in backfill persists the deterministic value into `meta.json` for cleaner ledgers.
- Adapter coverage: ses_ IDs are stamped by ox core for sessions captured by every adapter (Claude Code, Aider, Amp, Codex, Droid, Gemini, OpenCode, Pi).

**Session summary quality**
- New evaluation harness scores summary richness against a curated 18-session golden corpus, catching distiller regressions before release.
- LLM judge wired into the daemon for live summary validation; richness checks block stub or empty summaries from reaching the ledger.
- Tokenstrip is now on by default, reducing recording sizes without losing detail.
- Streaming compressor and `ox session token-optimize` shrink recordings for long-running agents.

**Ledger resilience epic**
- Multi-writer safety: structural protections against concurrent CLI/daemon writes corrupting `meta.json` or losing summary fields.
- Daemon LLM tier and autofix scheduler proactively repair corrupted or missing artifacts.
- `meta.json` manifest now carries an explicit `Storage` tag (`lfs` vs `git`) per file (ADR-016), preventing silent demotion of git-stored summaries to LFS pointers.

**Session UX**
- `ox session list` shows session titles by default; agent-context invocations default to JSON output.

### Fixed

**Session recording**
- Regenerate now hydrates LFS-stub raw.jsonl files instead of producing stub summaries.
- Regenerate writes to the canonical ledger path for team sessions instead of the local cache.
- Validator errors no longer leak into user-visible `meta.title` or `meta.summary`.
- `meta.json.title` is populated alongside `summary` so list views render correctly (previously 91/155 sessions on the ox team's ledger shipped with empty titles).
- Session content readers unified behind `openSessionContent` to enforce the cache-only invariant — hydrated bytes never overwrite the in-place LFS pointer.
- Closed an autostash race where the LFS pointer rewrite could be lost during commit.

**Daemon**
- Whisper SQLite handles are properly closed and child watches recursively unwatched, eliminating a file-descriptor leak under long uptimes.

**Init and doctor**
- `ox doctor --fix` now restores missing `ox-*` slash commands (the `claude-commands` check was previously registered but never run).
- `ox init` no longer offers Claude Code twice when an external adapter is already installed.

[0.7.0]: https://github.com/sageox/ox/releases/tag/v0.7.0

## [0.6.4] - 2026-04-22

### Fixed

**Session recording**
- Claude Code session recording was producing header-only `raw.jsonl` files with no turn entries — the one-shot adapter path didn't wire its `ReadFromOffset` handler, so every `PostToolUse` hook no-op'd. Turns are captured again.
- Silent recording failures now surface as errors instead of producing an empty session file.
- Sessions the LLM scores 0 are discarded rather than uploaded as empty entries.

**AI coworker isolation**
- `AGENTS.md` no longer leaks one coworker's active-recording context into another's view — each coworker sees only its own stamp, preventing accidental cross-agent contamination
- Recording-reminder whispers now reach only their intended recipient instead of fanning out to every coworker on the team
- `ox agent prime` no longer falls back to attributing a session to the sole active-recording agent when the real ID can't be resolved, which previously credited the wrong coworker

**Daemon correctness**
- Fixed a data race during scheduler shutdown where an in-flight clone trigger could panic `sync: WaitGroup is reused before previous Wait has returned`
- Fixed a race between two concurrent GC reclones on the same workspace that could destroy each other's in-flight artifacts
- Fixed GC reclone of a repo with an empty working tree — previously the captured diff deleted all restored files; now the empty tree is treated as corruption and the remote content is restored cleanly
- Added a 30-second lock-mtime heartbeat during GC so long reclones don't have their lock reclaimed by the stale-lock watchdog

### Changed
- Claude Code stamp prefix and rewrite rules updated with team-first framing in team-aware repos
- `ox init` and related installers now require the `claude` binary (the `primaryEnv` fallback has been removed from SageOx ClawHub skills)
- ClawHub ox install is now pinned to a specific tarball with an embedded sha256 instead of a floating reference

### Added
- Session-capture architecture documentation

[0.6.4]: https://github.com/sageox/ox/releases/tag/v0.6.4

## [0.6.3] - 2026-04-13

### Added

**Multi-agent init and teammate discovery**
- `ox init` now presents a multi-select agent prompt so teams can onboard multiple AI coworkers at once
- `ox agent prime` surfaces teammate names and credits SageOx throughout sessions

**Session history and distillation**
- `ox distill history list`, `show`, and `since` commands for browsing distilled session knowledge
- Unique entry IDs (`eid`) added to raw.jsonl session entries for reliable deduplication
- `ox distill --quiet` suppresses stdout for non-interactive use

**Observability**
- OpenTelemetry tracing with per-command trace context and W3C `traceparent` headers on CLI HTTP requests
- Per-task OTel trace contexts in the daemon
- Enriched CLI spans for better production debugging

**Daemon event hooks**
- Extensible hook system for daemon events, enabling automation on session lifecycle changes

**PR review workflow**
- New `/monitor-pr` skill drives open pull requests to green by triaging CI failures and review threads

**Feature flags**
- Layered feature flag resolver with disk-cached remote settings
- Daemon polling, IPC handler, and CLI startup wired for flags

**Attribution**
- Conditional commit attribution based on SageOx contribution score
- Unified attribution model removes OAuth gate from session start
- Current user identity (`you=`, `you_aliases=`) passed to agents so they distinguish their own prior work from teammate contributions
- Periodic recording reminder whispers from the daemon

**Other**
- OpenClaw SageOx skills and `clawhub-skill-lint` for community skill quality
- Server-side token validation and twinapi digital twin for auth
- Ledger migration system for legacy GitHub data filenames
- `ox config` surfaces `attribution.commit` and `attribution.pr` settings
- TUI dashboard redesigned with section-based layout
- Built-in adapters extracted to external binaries with adapter registry and CLI management
- Agentx bumped to v0.1.5 for Gemini support and flexible version detection
- Release testing playbook documentation

### Changed
- Team timezone feature removed — UTC hardcoded everywhere for consistency
- `make` targets quiet by default; `V=1` for verbose output
- Distilled facts now use UUID7 filenames for time-sortable ordering
- Pure-Go LFS architecture documented; `git-lfs` binary dependency fully removed

### Fixed
- Attribution prompts no longer credit the current user's own prior work as a teammate contribution
- Vulnerable dependencies bumped (4 Dependabot alerts)
- `DirtyOverlayDebouncer` stale-timer race in daemon resolved
- Session recording: prevent empty sessions from being committed, resolve symlinks before file lookup, prevent agent ID orphaning
- Session upload: resolve all three causes of upload failure; skip LFS stubs in detect loop
- Distill: carry source links through summary citations, apply lookback window to extraction phase, validate summary content against agent meta-output contamination
- Auth: flock-based locking prevents `auth.json` TOCTOU race; credential wipe on null tokens prevented
- Daemon: close leaked file descriptors in workspace scanning
- Doctor: commit migration changes to ledger, restore `FixLevelAuto`, skip adapter warnings for absent CLIs, restore github-data-migration check
- Ledger: handle rename/rename conflicts in rebase auto-resolve, prevent multi-node GitHub data conflicts and comment loss
- Agent: accept session subcommand flags and pick adapter deterministically
- Murmur: surface diagnostics when list is empty, reduce token noise in file-change output
- Distill: pick latest snapshot per PR/issue in GitHub indexer, drop mtime filter on session facts
- OpenClaw skills: enforce 24h window and dedupe state, trim prose, clarify install choice
- Legacy string-format hooks handled; `ox agent prime` made idempotent
- Flaky `TestGetExpired` tempdir cleanup race fixed

[0.6.3]: https://github.com/sageox/ox/releases/tag/v0.6.3

## [0.6.2] - 2026-04-05

### Added

**Agent rules via adapter protocol**
- External adapters can now install, check, and uninstall modular rule files for their agent (e.g., `.claude/rules/ox.md`, `.factory/rules/ox.md`)
- New `rules_installer` capability and `install-rules` / `check-rules` / `uninstall-rules` adapter subcommands
- Claude Code and Droid adapters ship ox behavioral guidance (command reference, session recording, murmuring, attribution) as agent-native rule files
- `ox init` installs rules via adapters; `ox doctor` detects missing/stale rules via adapter diagnostics; `ox uninstall` removes them
- Rules content is version-stamped with downgrade guards via agentx `RulesManager`

### Changed
- Rules management moved from direct agentx calls in the ox CLI to the adapter protocol — each adapter owns how rules are written for its agent
- `DiagnoseParams` now includes `Version` field so adapters can detect stale rules

[0.6.2]: https://github.com/sageox/ox/releases/tag/v0.6.2

## [0.6.1] - 2026-04-02

### Fixed
- Session push failures no longer cascade-block LFS uploads or destroy cached session data
- Daemon anti-entropy now correctly recovers fully-finalized and raw-only cache sessions
- Auth no longer crashes when distilling memory with a nil token

[0.6.1]: https://github.com/sageox/ox/releases/tag/v0.6.1

## [0.6.0] - 2026-03-30

### Added

**Murmur & whisper — team communication for AI coworkers**
- AI coworkers can now publish work-in-progress updates to teammates via `ox murmur`
- Whisper delivery via `UserPromptSubmit` hook and active pull keeps coworkers in sync
- User-level config for pause/resume control, nudge tracking, and whisper budgets
- Daemon handles file writes and commits via IPC, keeping the CLI stateless
- `ox murmur list` shows recent murmurs; `ox murmur status` shows delivery state

**Pure-Go tree-sitter symbol extraction**
- Code search now extracts symbols (functions, classes, types) using a pure-Go tree-sitter implementation
- No CGo dependency — works everywhere ox builds

**New commands**
- `ox upgrade` — self-update with daemon whisper broadcast to notify active coworkers
- `ox teams` — discover and list your teams from the CLI
- `ox glance` — session-based team activity feed with file contention detection

**Import improvements**
- Audio and video MIME type detection for `ox import`
- URL-based video import with progress tracking and `ox import list`

**Distillation pipeline**
- Per-stage guidance files with progressive disclosure
- Unified JSONL fact schema across all fact sources
- GitHub activity assembled into event clusters for alignment feed
- Session summary facts extracted into the distill pipeline

**Infrastructure**
- sqlc typed SQL for whisper and codedb stores
- Self-healing rebase pipeline with manifest-driven conflict resolution rules
- Self-healing for codedb infrastructure failures (daemon auto-recovers corrupted indexes)
- PAT liveness validation in `ox doctor` and `ox status`
- DB maintenance scheduler and whisper resilience in daemon
- Session `--summary` flag for `ox session regenerate`

### Changed

- 5.5x faster code search indexing; symbol index build time reduced by 90%
- Agent selector replaces boolean config: choose `auto`, `none`, `claude`, or `codex`
- Default sync intervals adjusted: 60s ledger, 15s team context
- Resummary uses local daemon instead of server-side API
- Notifications consolidated into whisper pipeline with stdout XML delivery
- Shared `PushWithRetry` primitive and `pkg/sessionsummary` for cross-repo use
- Structural cleanup: god files split, IPC service interface extracted, legacy code removed
- Visual progressive disclosure for video discussions
- Keyframe content types aligned with server vision pipeline
- Codecov Test Analytics added to scheduled coverage workflow

### Fixed

- **Session recording reliability**: pre-start leak, cross-env cache path split, decoupled from auth, token refresh, `files_changed` populated in summary.json, concurrent agent URL disambiguation, `StartOffset` capture on session start, stop marker no longer leaks into user repository, process tree walk captures correct agent PID instead of transient bash PID
- **Auth resilience**: capture `refresh_token` from JWT exchange, handle missing refresh tokens, auto-repair revoked PATs, login no longer blocks on token refresh failure
- **CodeDB stability**: prevent CLI hang when daemon is indexing, detect and report empty index, fast fail when worktree disappears, prevent projectRoot oscillation across worktrees, break perpetual indexing loop from freshness race and bleve lock timeout, skip indexing when ledger not yet cloned
- **Ledger sparse-checkout**: sparse-checkout init no longer wipes codedb cache on sync, `.sageox` added to sparse-checkout cone, staged files protected from `sparse-checkout set`
- **Data safety**: LFS data loss prevention on push failure, dead force-push code path removed
- Doctor handles push 403 errors, local remote credential injection, and uses `version.Full()` for daemon version comparison
- Daemon uses registry-aware IPC client everywhere; CWD inheritance bug fixed
- Daemon log entries now include PID and project path in sync warnings
- Endpoint normalizer prepends `https://` to bare hostnames
- GitHub sync rebuilds state from disk to prevent cold-start hang; PR commits preserved on replay
- System credential helpers suppressed during PAT liveness probe
- Stale daemons killed before starting new ones to prevent orphan accumulation
- Session abort search and stale agent ID resolution
- Default to auto-record for ox-initialized repos
- Friction telemetry re-queues events on flush failure (frictionax v0.1.2)

[0.6.0]: https://github.com/sageox/ox/releases/tag/v0.6.0

## [0.5.1] - 2026-03-16

### Added

- `ox agent session abort <session-name> --force` aborts orphaned, ghost, or stale sessions by name with partial name resolution

### Changed

- Faster code search and indexing via buffer reuse, optimized parsing, and in-memory blob caching
- Daemon notification deduplication is now O(1) instead of O(n)
- LFS upload/download reuses a shared HTTP client for connection pooling

### Fixed

- Session recording ParentPID now tracks the long-lived agent process instead of the transient hook process, preventing sessions from appearing as orphans immediately after startup
- Hook safety-net recording call no longer fails with "path cannot be empty" after prime subprocess completes
- `ox logout --force` now correctly skips confirmation prompt for scripted/non-interactive use
- `ox status` always shows ledger provisioning status, even when ledger isn't configured locally
- JWT exchange errors during authentication handled more securely with cleaner error messages
- Stale Personal Access Tokens automatically removed from git remote URLs on logout
- Race condition in `ox doctor` git connectivity check fixed (used `context.WithTimeout` instead of manual goroutine)

## [0.5.0] - 2026-03-15

### Added

**Session anti-entropy**
- Daemon automatically detects and recovers interrupted sessions with quality scoring
- Progressive disclosure hints guide coworkers toward session health actions

**Incremental session recording**
- Sessions record incrementally via hooks with unified artifacts
- Session lifecycle consolidated into a canonical state machine for reliability
- Timing metrics and async upload via daemon

**Session maintenance commands**
- `ox session remove` deletes sessions from the ledger
- `/ox-session-review` skill with auto-fix for stale commands

**GitHub PR/issue sync**
- Daemon automatically syncs GitHub PRs and issues into the local code search index
- GitHub token fallback for environments without explicit configuration

**Expert coworker agents**
- `ox coworker list` and `ox coworker load <name>` surface specialized agents (go-pro, code-reviewer, test-architect, etc.) directly in prime context

**Distillation**
- Local pipeline distills session observations into persistent team memory via `memory/GUIDE.md`
- Local pipeline distills team discussions into structured facts with file-based output
- Per-day bucketing, UUID7 filenames, content-based timestamps

**Team context change notifications**
- Daemon notifies when team context updates arrive from remote

**Code insights agent detection**
- `ox code insights` auto-detects agent context and returns JSON output with prime hints


### Changed

- `ox agent prime` and session commands switch to Claude recommended XML output format
- **One daemon per repo** — Daemon identity tied to `repo_id` for isolation across projects
- **Daemon self-restart** — Daemon automatically restarts on version mismatch
- **go-git v6** — CodeDB upgraded from go-git v5 to v6 with comprehensive regression tests
- **Hooks in shared settings** — ox hooks now install to `.claude/settings.json` instead of per-project
- **Agent parent PID tracking** — Instant liveness detection via parent process
- **Parallel team context sync** — Faster sync with parallel fetches and improved health display
- **External packages** — frictionax and agentx migrated to standalone packages
- **Deprecated events.jsonl removed** — Session artifacts simplified

### Fixed

- Auto-repair missing LFS pointers that block ledger push
- Session recovery writes atomically to prevent corrupted raw.jsonl
- Live PIDs never incorrectly considered stale
- Ghost session classification accuracy improved
- Non-blocking search indexing status checks prevent daemon stalls
- Team context search actually executes (was silently skipped due to stale source check)
- Wrong team context selection in multi-team repos prevented
- CodeDB moved to `.sageox/cache/` (out of ledger root)
- IPC timeouts increased for daemon status queries and heartbeat detection
- Agent list works correctly across worktrees
- Legacy cache paths scanned and updated for current layout
- UTC normalization for time comparisons fixes daemon status contradictions
- Bulk cleanup of stale empty recording stubs
- Daemon GC lock acquisition distinguishes lock-exists from other errors
- Hook command made reachable from dispatcher
- CodeDB bypasses go-git extension rejection for repos with unsupported extensions

[0.5.0]: https://github.com/sageox/ox/releases/tag/v0.5.0

## [0.4.1] - 2026-03-12

### Fixed

**`ox session list` no longer silently returns empty**
- Shows which repo was searched when no sessions are found (name + repo ID)
- Tells you when the ledger is unavailable and suggests `ox doctor --fix`
- Shows current directory when run outside a SageOx project
- Debug logging (`-v`) now surfaces why the ledger was skipped

### Added

**`ox session list --json`**
- Structured JSON output for AI coworkers, including `repo_name`, `repo_id`, and `ledger_available`

[0.4.1]: https://github.com/sageox/ox/releases/tag/v0.4.1

## [0.4.0] - 2026-03-09

### Added

**Local code search (CodeDB)**
- Agents can search your codebase locally via a built-in code search engine
- Integrated with the daemon for background indexing and worktree support
- Compact inline results surfaced in `ox status`
- [See how CodeDB came together in just a few days](https://www.youtube.com/watch?v=ODMZyEU3Bz8)

**`ox query` command**
- New top-level command for querying team knowledge directly from the CLI

### Changed
- Daemon preserves uncommitted changes during blue-green GC reclone
- Daemon logs colorized with semantic colors and compact timestamps

### Fixed
- LFS stub files correctly detected during session recording
- Agent-specific recording state prevents cross-agent interference in multi-agent scenarios

[0.4.0]: https://github.com/sageox/ox/releases/tag/v0.4.0

## [0.3.0] - 2026-03-06

### Added

**Semantic search**
- Agents can search over team knowledge via the CLI

**Document import (`ox import`)**
- Import documents into team context
- `--team` flag for explicit team targeting

**Session improvements**
- `ox session regenerate` to re-generate session summaries on demand
- Multi-session status with inflight recording detection
- Workspace path and branch shown in session status
- Redesigned HTML viewer with narrative timeline and semantic phases

**Improvements**
- Various prime improvements to enable better discovery of context
- Sync reliability improvements
- Sync staleness detection and warnings
- All team contexts surfaced to agents with slug-based lookup
- Doctor warnings made actionable for non-technical users
- Agent support tiers and scorecard specs
- Daemon status redesigned with actionable CTAs
- Consolidated environment variables for config overrides
- User-defined REDACT.md rules for filtering sensitive content from sessions
- Metadata improvements and sandbox safety fixes
- Initial work towards supporting Codex

### Fixed
- Codex integration silently absorbing errors and creating empty session files
- Squash merge stomping that lost changes
- Doctor false warnings after fresh `ox init`
- Sparse checkout: `--sparse` on all git add calls, `--autostash` on pulls
- Stale cache paths not rewritten to ledger after prune
- Session start after clear + abort lifecycle edge cases
- RecordFlush cooldown reset on empty buffers
- Duplicate repo detection during `ox init`
- Doctor/status output improved when run outside a git repo
- Daemon startup visibility and performance
- File I/O hardening, clone recovery, and credential safety

[0.3.0]: https://github.com/sageox/ox/releases/tag/v0.3.0

## [0.2.0] - 2026-02-24

### Added

**Redesigned `ox doctor` with timeline TUI**
- Visual timeline showing check progress and results
- Auto-sync ledger health checks detect drift before it causes problems
- Doctor recovery options for common failure modes

**Version update notifications**
- `ox status` and `ox agent prime` notify when a newer release is available
- Update check runs via daemon cache — no extra network calls in the CLI hot path

**Smarter AI coworker context**
- `ox agent prime` now includes user and agent tips for better session guidance
- Intent-to-command guidance field helps coworkers discover the right `ox` command
- Team docs progressive disclosure — coworkers get relevant team context without flooding their context window
- Team instruction files emitted directly into agent context

**Session abort command**
- `ox session abort` discards a session without uploading, useful for throwaway explorations

**Orchestrator detection**
- Detects orchestration layers (e.g., multi-agent setups) via `X-Orchestrator` header
- Improved Amp agent detection accuracy

**Cleaner status output**
- `.sageox/` symlink paths shown as short relative paths instead of full XDG paths
- Repo-specific team context highlighted across `ox` commands

### Changed
- Ledger checkout moved to user data directory (XDG-compliant, keeps repo clean)
- Session HTML compacted — tool calls are collapsed, duration/tool-count noise removed
- Git safety primitives extracted into `internal/gitutil` for reuse
- Daemon sync uses ls-remote pre-check and exponential backoff for resilience
- Better agent ID error messages with diagnostic guidance
- `ox init` now shows `ox sync` as step 2 in next-steps output

### Fixed
- Ghost sessions no longer appear after onboarding
- Session summaries now generated from push-summary for accuracy
- Tool noise filtered from session summarization
- Project-level hook settings checked correctly during install detection
- Team context discoverable without waiting for daemon sync
- Stale PAT in git remote URLs fixed on login/logout
- Daemon config cache no longer clobbers ledger path
- System-injected content classified correctly in raw session data
- Fresh checkout failures in `ox doctor` resolved
- Credential token refresh separated from team discovery in daemon
- Cloud Code project hash uses dashes instead of underscores

[0.2.0]: https://github.com/sageox/ox/releases/tag/v0.2.0

## [0.1.1] - 2026-02-19

### Added
- Pre-built binaries for 6 platforms (curl one-liner install)
- Ed25519 artifact signing

### Changed
- Daemon liveness uses socket-ping instead of flock
- All API calls are endpoint-aware

### Fixed
- `ox sync` now surfaces daemon errors instead of silent success (#9)
- `ox status` crash on empty ledger repos
- `ox doctor --fix` discovers uncloned team contexts
- Git credentials masked in error output

## [0.1.0] - 2026-02-18

Initial public release of the SageOx CLI (`ox`).

### Highlights

- **Session recording**: Capture, view, and export human-AI coding sessions with HTML and Markdown output
- **Team discussion**: Record and transcribe team conversations so arch decisions and product context flows automatically to agents
- **Background daemon**: Automatic git sync for ledgers and team contexts with self-healing clone recovery

[0.1.1]: https://github.com/sageox/ox/releases/tag/v0.1.1
[0.1.0]: https://github.com/sageox/ox/releases/tag/v0.1.0
