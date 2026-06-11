# Touchpoints — Feature → ox Subcommand / SageOx API Map

Informational reference for the eventual runner: which feature files exercise
which `ox` subcommands and which SageOx cloud touchpoints sit behind them. This
is a contract-level map, not an exhaustive call graph — it names the surfaces a
runner would drive and assert against, in user-facing terms.

When the runner ships, a hermetic SageOx API double stands in for the cloud
touchpoints (device-code OAuth, the CLI repos/kb endpoints, team-context and
ledger git remotes, and search), and a scratch git repo per scenario stands in
for the developer's working tree — the test-fixture equivalent of "Given Devon
has initialized the repository".

## Install

| Feature | ox surface | SageOx touchpoint |
|---|---|---|
| install/build-and-install.feature | `make build`, `make install`, `ox`, `ox version`, `ox status` | None (build + PATH + first-run guidance; cloud only when a command needs it) |

## Auth

| Feature | ox surface | SageOx touchpoint |
|---|---|---|
| auth/login.feature | `ox login` (with endpoint selection / `--endpoint`) | Device-code OAuth (request code, poll for token), user-info, CLI repos endpoint for git-credential sync |
| auth/headless-login.feature | `ox login` in a non-TTY / no-browser shell | Same device-code OAuth; no browser launch; configured-endpoint fallback on a network error |
| auth/logout.feature | `ox logout`, `ox status` | Local credential removal; status reflects the cleared session |

## Onboarding

| Feature | ox surface | SageOx touchpoint |
|---|---|---|
| onboarding/init-repo.feature | `ox init` (re-run safe), commit `.sageox/` | Endpoint wiring; team-context + ledger git remotes; prime-marker + hook injection into the working tree |
| onboarding/status.feature | `ox status` (`--json`) | Auth check; initialization + sync state; daemon health |
| onboarding/doctor.feature | `ox doctor` (detect + auto-fix) | Endpoint normalization; marker/hook/ledger/credential repair; deferred-sync reassurance |

## Priming

| Feature | ox surface | SageOx touchpoint |
|---|---|---|
| priming/prime-session.feature | `ox agent prime` (re-prime after compaction/clear, refresh) | Team-context load; agent-ID issuance; session-recording start; one-time transparency notice |

## Session Recording

| Feature | ox surface | SageOx touchpoint |
|---|---|---|
| session-recording/auto-record.feature | `ox agent session start` / stop (via prime + session end), recovery | Capture to the Ledger; finalize + summarize; offline capture then deferred sync |
| session-recording/pause-resume.feature | `ox agent session pause` / resume / abort | Paused-interval exclusion; excluded-duration report; abort discards |
| session-recording/list-and-view.feature | `ox session list`, `ox session view`, `ox session download` | Ledger session listing; stub-to-local fetch; render in the reader's format |

## Plan Enrichment

| Feature | ox surface | SageOx touchpoint |
|---|---|---|
| plan-enrichment/enrich-while-drafting.feature | `ox plan enrich` (JSON default); plan-mode hint (once per entry) | Local-only signal computation (collision / prior-art / expert-routing); no LLM or network call |
| plan-enrichment/render-and-present.feature | `ox plan render --open` (headless prints path); plan-exit nudge | Self-contained HTML render; SageOx attribution conditional on enrichment; capture to Ledger |
| plan-enrichment/review-loop.feature | `ox plan review <slug>`, `ox plan list` | Served render + inline feedback; auto-reload; open-item tracking; approval |

## Murmur

| Feature | ox surface | SageOx touchpoint |
|---|---|---|
| murmur/publish-wip.feature | `ox murmur` (topic, importance, files), `ox murmur pause` / resume | Publish to the repo/team scope; size limit; durable outbox queue when the daemon is down |
| murmur/whisper-delivery.feature | (consuming side, in-session) | Daemon relays a published murmur as a whisper into other coworkers' sessions; importance preserved; expiry after a day |

## Team Context

| Feature | ox surface | SageOx touchpoint |
|---|---|---|
| team-context/team-ctx.feature | `ox agent team-ctx [slug]`, `ox coworker list` / load | Distilled team discussions/decisions/conventions; expert-coworker load |
| team-context/query.feature | `ox query` (`--local`, `--json`) | Search across discussions, decisions, docs, prior sessions; offline cached-Ledger search |

## Code Intelligence

| Feature | ox surface | SageOx touchpoint |
|---|---|---|
| code-intelligence/code-search.feature | `ox code search` (compact default, fuller form, `ox code index`) | Code + git-history index; not-ready guidance |
| code-intelligence/insights.feature | `ox code insights` (`--json`) | Hotspots, recent activity, open PRs, contention flagging |

## Knowledge Bubbles

| Feature | ox surface | SageOx touchpoint |
|---|---|---|
| knowledge-bubbles/list-bubbles.feature | `ox kb list` (`--type`, inspect, resolve path) | kb API + legacy team-context + legacy ledger merge; per-type filter; on-disk path resolution |

## Upgrade

| Feature | ox surface | SageOx touchpoint |
|---|---|---|
| upgrade/upgrade-cli.feature | `ox upgrade` (`--json`), `ox version` | Install-method detection (Homebrew / go install / manual); latest-version check |
| upgrade/version-mismatch.feature | `ox doctor`, `ox version` | Behind-version detection + upgrade pointer; post-upgrade compatibility repair; wrapper refresh |

## Test-fixture touchpoints (no production equivalent)

When a runner exists, these are the setup levers a scenario would use to
establish a "Given" without hacking real state — the equivalent of the SageOx
device-corpus admin routes. They are placeholders to be defined alongside the
runner:

| Fixture lever | Used by | Effect |
|---|---|---|
| Seed a contended area (recent activity + open PR) | code-intelligence/insights.feature, plan-enrichment/enrich-while-drafting.feature | Make enrichment/insights report a collision |
| Seed prior sessions/discussions on a topic | team-context/query.feature, session-recording/auto-record.feature | Make recall and prior-art signals fire |
| Make the configured endpoint unreachable | auth/headless-login.feature, murmur/publish-wip.feature | Drive the network-error and daemon-down fallbacks |
| Age a murmur past its expiry | murmur/whisper-delivery.feature | Verify expired murmurs are not whispered |
