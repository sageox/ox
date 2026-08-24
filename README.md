# SageOx CLI (`ox`)

**The hivemind for human–agent teams.** SageOx makes your team's decisions,
conventions, and architectural intent persistent — and loads them automatically
into every coding session, human or AI.

Today, that context lives in scattered places: a meeting nobody recorded, a Slack
thread three weeks deep, the head of whoever wrote the code. So humans repeat
themselves and AI coworkers drift — rebuilding the same lost context every
session. `ox` closes that gap. Decisions become shared memory; every session
starts with the full picture instead of from zero.

**Ask your coding agent what the team already figured out — even if it happened
in a different agent, on a different machine, days ago.**

![One ox session: cross-agent recall, murmurs, and a team-enriched plan](demo/demo.gif)

One session, every moment that matters: it **recalls** a teammate's Codex work
and your own prior Claude Code session, **coordinates** by murmuring and noticing
a teammate already in the same files, and proposes a plan **enriched** with
collisions, prior art, and who owns the area. All of it because every session is
recorded to a shared, queryable **Ledger**.

## Latest Project Activity

![SageOx — latest project activity](https://www.sageox.ai/api/v1/public/mural/vio0F88wcSd9grV3piwmisVtNA_rWF-ta8gpGJ_tf1g)

## Works with your coding agent

`ox` is agent-agnostic. The same recorded context is primed into, and queryable
from, whichever agent your team uses.

| Agent | Prime context | Record sessions | What fires it |
|---|:---:|:---:|---|
| **Claude Code** | ✅ | ✅ | hooks — session, prompt, tool, and compaction |
| **Codex CLI** | ✅ | ✅ | hooks — session, prompt, and tool |
| **Gemini CLI** | ✅ | ✅ | hooks — tool and stop |
| **Droid** | ✅ | ✅ | hooks — tool and stop |
| **OpenCode** | ✅ | ✅ | plugin — session start |
| **Amp** | ✅ | ✅ | plugin records; `AGENTS.md` primes |
| **Pi** | ✅ | ✅ | `AGENTS.md` |
| **Aider** | ✅ | ✅ | `CONVENTIONS.md` |
| **Goose** | ✅ | ✅ | hooks — session, prompt, tool, and tool-failure |
| Cursor · Windsurf · Cline · Copilot · Kiro | ✅ | ➖ | instruction file |

✅ shipped · ➖ planned. Claude Code is the primary, most-tested target; see
[agent compatibility](docs/guides/agent-compatibility.md) for the per-agent detail.

`ox query`, `ox murmur`, and `ox status` are plain CLI commands. They work from
any agent — including ones with no adapter — as long as `ox` is on `PATH`.

### Orchestrators

`ox` also detects the harness running your agents, and adapts what it tells them:

- [**Block Buzz**](https://github.com/block/buzz) — spawns agents through its
  `buzz-acp` ACP harness
- [**Conductor**](https://conductor.build) · [**Gas City**](https://github.com/gastownhall/gascity)
  · [**OpenClaw**](https://github.com/openclaw/openclaw)

Detection is config-independent — `ox` walks the process ancestry rather than
trusting an environment variable, so it works even when the harness sets nothing.
Under Buzz, `ox agent prime` emits an extra directive: `buzz-acp` does not fire
the agent lifecycle hooks that push whispers into a turn, so the agent is told to
pull teammates' signals by hand instead of silently missing them.

<sub>Agent and orchestrator names are trademarks of their respective owners; `ox`
is compatible with, not affiliated with, them.</sub>

## Install

**Quick install (macOS / Linux / FreeBSD):**

```bash
curl -sSL https://raw.githubusercontent.com/sageox/ox/main/scripts/install.sh | bash
```

**From source:**

```bash
git clone https://github.com/sageox/ox.git && cd ox
make build && make install
```

Verify with `ox version`.

## Quickstart

```bash
cd ~/src/my-project     # your code repo
ox login                # authenticate with sageox.ai

ox init                 # one-time per repo: creates .sageox/
git add .sageox/ && git commit -m "initialize SageOx"

ox doctor               # diagnose (ox doctor --fix auto-repairs)
ox status               # setup, sync state, and your Knowledge Bubbles
```

That's the whole setup. From here, **session capture is automatic** — when an AI
coworker runs `ox agent prime` at the start of a session, team context loads in
and the session is recorded to the Ledger when it ends. No manual start/stop
ritual.

Then just ask your AI coworker things it couldn't have known on its own:

- *"What did we decide about the daemon design, and who worked on it?"*
- *"Draft a plan from this week's SageOx team discussions."*
- *"Show me an effective prompt a teammate used on this repo recently."*

## What you get

| Capability | Command | Learn more |
|---|---|---|
| Team context + repo memory as **Knowledge Bubbles** | `ox status`, `ox kb list` | [jit-discovery](docs/guides/jit-discovery.md) |
| Query past sessions, discussions, and code — across agents and machines | `ox query "..."` | [query](docs/reference/query.mdx) |
| Auto-recorded coworker sessions (**Ledger**) | `ox agent prime`, `ox session list` | [session capture](docs/architecture/session-capture-architecture.md) |
| Real-time coordination signals between coworkers | `ox murmur "..."` | — |
| Team-context-enriched implementation plans | `ox plan enrich`, `ox plan render` | [plan](docs/reference/plan) |
| Planning-relevant code insights (hotspots, contention) | `ox code insights` | — |
| Load an expert AI coworker into context | `ox coworker load <name>` | — |
| Diagnose and auto-fix your setup | `ox doctor --fix` | [doctor](docs/reference/doctor.mdx) |

## How it works

`ox init` writes a `.sageox/` directory that ties the repo to your team. From
then on, `ox agent prime` injects team context — conventions, security
requirements, architectural decisions, prior sessions — into each AI coworker
before it writes a line. Coworkers, human and AI, share that context through the
**Team Context** and the per-repo **Ledger**.

The payoff is multiplayer by default. When a discussion, an implementation
session, and the resulting code all carry the same shared context, a reviewer
opening the PR has the full story — the original reasoning, the session that
built it, and the diff — without chasing anyone down.

## Configuration

SageOx reads configuration from, in order:

1. CLI flags (`--verbose`, `--quiet`, `--json`)
2. Environment variables
3. Config file (`.sageox/config.yaml`)

## Legal

- [Privacy Policy](https://sageox.ai/privacy)
- [Terms of Service](https://sageox.ai/terms)
- [Acceptable Use Policy](https://sageox.ai/acceptable-use)

## Tools we love

We build `ox` in great company. These are the tools we rely on — and love —
every day. Gratitude to the teams behind them, and to the wider developer
community.

<p align="center">
  <a href="https://socket.dev" title="Socket — supply-chain security"><img src="docs/assets/logos/socket.svg" height="26" alt="Socket"></a>
  &nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;
  <a href="https://sageox.ai" title="SageOx — agentic context infrastructure"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/assets/logos/sageox-dark.svg"><img src="docs/assets/logos/sageox-light.svg" height="26" alt="SageOx"></picture></a>
  &nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;
  <a href="https://www.coderabbit.ai" title="CodeRabbit — AI code review"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/assets/logos/coderabbit-dark.svg"><img src="docs/assets/logos/coderabbit-light.svg" height="22" alt="CodeRabbit"></picture></a>
  &nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;
  <a href="https://www.greptile.com" title="Greptile — AI codebase review"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/assets/logos/greptile-dark.png"><img src="docs/assets/logos/greptile-light.png" height="26" alt="Greptile"></picture></a>
  &nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;
  <a href="https://charm.sh" title="Charm — delightful tools for the command line"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/assets/logos/charm-dark.svg"><img src="docs/assets/logos/charm-light.svg" height="24" alt="Charm"></picture></a>
</p>

<p align="center">
  <sub><a href="https://socket.dev">Socket</a> · <a href="https://sageox.ai">SageOx</a> · <a href="https://www.coderabbit.ai">CodeRabbit</a> · <a href="https://www.greptile.com">Greptile</a> · <a href="https://charm.sh">Charm</a></sub>
</p>
