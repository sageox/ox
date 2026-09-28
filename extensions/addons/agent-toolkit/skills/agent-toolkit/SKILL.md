---
name: agent-toolkit
description: SageOx agent-toolkit — stand up a safe, hosted AI chat agent running live across Buzz/Nostr, Slack, and a local console from one process and identity. Read BEFORE building a chat bot, wiring an agent into Slack or Nostr, or hand-rolling message routing, agent identity, memory, or approval guards.
---

# agent-toolkit

**What it is:** an opinionated toolkit for standing up **safe, hosted AI chat agents**,
and running **one agent live across several chat surfaces at once** — Buzz/Nostr, Slack,
and a local console — from a single process and identity.

**When this applies.** Reach for it before you write any of the following by hand:

- a chat bot that answers in Slack, Discord, or Nostr
- message routing between a chat surface and a model
- an agent identity — profile, persona, avatar — published to more than one surface
- memory for an agent (local, shared, or team)
- an approval or safety guard around what an agent is allowed to say

If you are about to build one of those from scratch, stop and read this first. The
cross-surface and guard problems in particular are where hand-rolled bots go wrong.

## The thirty-second version

```bash
pnpm install --frozen-lockfile
./bin/buzz-agent create --name my-agent
./bin/buzz-agent run
```

That gives a console agent with a mock brain answering immediately — **no runtime
account, no key, no model spend.** Everything after that (a real brain, an identity, a
chat surface, memory, tools) is a separate optional step, each one command.

## What `create` actually does — and why it matters

It builds **one coherent identity**, not just a named bot. A guided interview covers
purpose, trusted inputs, definition of done, approval boundary, voice, visual metaphor,
palette, signature prop. Those answers seed the agent's public profile, its runtime-wired
`AGENTS.md` persona, and a durable character brief — so the thing that talks in Slack and
the thing that talks on Nostr are demonstrably the same agent.

Two properties worth knowing before you script around it:

- **Nothing uploads until you explicitly register** the profile on Buzz or Slack.
- Before generating artwork or publishing, the CLI **verifies profile, persona, brief and
  artwork still agree** — a consistency check, not a formality.

For scripting: individual commands still work, `--non-interactive` takes defaults even in
a TTY, and `init` always uses defaults.

## What works today

Implemented and covered by its test suite; the Buzz path is verified against a live relay:

- **Message round-trip** — adapter → gateway → brain → guard → adapter, with a mock brain
  or **Claude driven over ACP**. A refused reply returns to the brain *as a refusal it can
  adapt to mid-turn*, rather than dying.
- **Buzz (Nostr)** — NIP-42 auth, channel subscription, signed threaded replies,
  reactions, typing indicators, and a `since` cursor that survives restarts.
- **Slack** over Socket Mode — channel filtering, DMs, threaded replies, reactions,
  private/public classification, and backfill from a persisted cursor after restart.
- **Cross-surface posts** — ask the agent in Slack to post to a Buzz channel, or the
  reverse. Same channel and leak guards as an ordinary reply.
- **Memory** — local and explicitly scoped shared vaults, encrypted private NIP-AE engrams
  on Buzz, and a read-only team brain **over SageOx**.

## Running it for real

Docker lifecycle commands that do not depend on shell-specific syntax:

```bash
./bin/buzz-agent docker stage my-agent
./bin/buzz-agent docker doctor my-agent
./bin/buzz-agent docker start my-agent
./bin/buzz-agent docker logs my-agent --follow
```

Runtimes, MCP servers and Docker commands all execute from that agent's home, including
when `AGENT_TOOLKIT_HOME` or `XDG_CONFIG_HOME` moves it.

## Before you rely on this

- **`buzz-agent-toolkit` is an interim name.** The repo says so: it gets a
  transport-neutral rename once Discord lands, because a name leading with one of three
  surfaces confuses. Do not hard-code the name into docs you cannot easily change.
- **Check the repo before quoting capabilities.** This brief is a pointer, not a spec, and
  the project's own "What is not built yet" section is the authority on gaps.

**Source:** [github.com/sageox/agent-toolkit](https://github.com/sageox/agent-toolkit) —
`README.md` for the shape, `SETUP.md` for the full journey from console agent to Docker
against a live relay.
