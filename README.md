# SageOx CLI (`ox`)

[![docs: ai-human-docs](https://raw.githubusercontent.com/rsnodgrass/ai-human-docs/main/badges/ai-human-docs.svg)](https://github.com/rsnodgrass/ai-human-docs)

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

## The loop

```mermaid
flowchart LR
    REC["Record at sageox.ai<br/>discussions, decisions"]
    KB["Knowledge Bubbles<br/>team context plus ledger"]
    PRIME["ox agent prime<br/>loads context"]
    WORK["AI Coworker session"]
    CAP["Session captured<br/>to the Ledger"]
    REC --> KB --> PRIME --> WORK --> CAP --> KB
```

Record a discussion once. It's distilled into **Knowledge Bubbles** — your team's
shared memory. The next time anyone primes a coding session, that context flows
in. The session itself is captured back to the **Ledger**, so the next coworker
inherits it too. Context compounds instead of evaporating.

## Works with your coding agent

`ox` is agent-agnostic. The same recorded context is primed into, and queryable
from, whichever agent your team uses.

| Agent | Prime context | Record sessions | Murmur (coordinate) | Query past work |
|---|:---:|:---:|:---:|:---:|
| **Claude Code** | ✅ | ✅ | ✅ | ✅ |
| **Codex CLI** | ✅ | ✅ | ✅ | ✅ |
| **Cursor** | ✅ | ✅ | ➖ | ✅ |
| **Windsurf / Cline / Copilot / Aider** | ✅ | ➖ | ➖ | ✅ |

✅ supported · ➖ planned. Claude Code is the primary, most-tested target.

<sub>Agent names are trademarks of their respective owners; `ox` is compatible
with, not affiliated with, them.</sub>

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
