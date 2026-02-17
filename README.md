<!-- doc-audience: human -->
# SageOx CLI (ox)

[![docs: ai-human-docs](https://raw.githubusercontent.com/rsnodgrass/ai-human-docs/main/badges/ai-human-docs.svg)](https://github.com/rsnodgrass/ai-human-docs)

SageOx is agentic context infrastructure — we call it the hivemind. It makes architectural and product intent persistent and automatically available across humans and agents.

This initial version is intended for AI-native teams — teams that build products almost exclusively through prompts.

Sessions, ledgers, and team knowledge ensure your AI coworkers understand your project's patterns, security requirements, and architectural decisions from the start, making agentic engineering multiplayer by default.

## Demo

![SageOx CLI Demo](demo/demo.gif)

## Install the CLI

```bash
$ git clone https://github.com/sageox/ox.git && cd ox
$ make build && make install
# set up the ox location using export PATH
```

## Set up ox in your repo

```bash
# cd into your code repo (e.g. ~/src/my-project)
$ cd ~/src/my-project
$ ox login

# one time setup, done ONCE per repo
$ ox init
# commit the changes in your repo, e.g. git commit -a -m 'SageOx init'

$ ox doctor
# ox doctor --fix may be needed in the alpha stage
$ ox status
# will give you the location of the team context and ledger repos
```

## Go to [sageox.ai](https://sageox.ai) - Setup team

Go into your newly created team in SageOx and invite your coworkers by copying the invite in the upper right, displayed in Team Overview.

<!-- TODO: add screenshot of team invite UI -->

## Record discussions

Team discussions impacting the product are captured and transcribed in the app and the context is automatically available to Claude.

<!-- TODO: add screenshot of transcription UI -->

## Capture sessions

`ox-session` capture the conversation between a developer and Claude so the decisions, patterns, and reasoning become available to the rest of the team.

```bash
$ pwd
/home/me/src/my-project
$ claude
/ox-session-start
<implement fizz buzz>
/ox-session-stop
```

<!-- TODO: add screenshot of session viewer -->

Test it by having a different developer ask Claude about a decision made during the captured session.

## How it works

1. **ox init** creates a `.sageox/` directory with shared team context for your project
2. **ox integrate** sets up hooks so your AI coworker automatically loads context at session start
3. Your AI coworker receives team context, security conventions, and architectural patterns
4. Coworkers (human and AI) share context through ledgers and team knowledge

## How Ox Fits In

**Skills give agents hands. Ox gives agents taste and judgment.**

| Source | Teaches | Example |
|--------|---------|---------|
| Coding agent (skills & plugins) | **HOW** to do things | "Run `terraform plan` before apply" |
| SageOx (team context) | **WHY** to make decisions | "Use spot instances for batch jobs" |
| SageOx (team context) | **STYLE** conventions | "Prefix all resources with env name" |
| SageOx (team context) | **WHEN** to act | "Review infra on cloud file changes" |

## Supported AI Agents

SageOx integrates with popular AI coding agents:

- **Claude Code** - Automatic session hooks via `.claude/` configuration
- **Cursor** - Integration via rules and context files
- **Other agents** - Any agent that reads `AGENTS.md` or supports hooks

Run `ox integrate` to set up the integration for your preferred agent.

## Configuration

SageOx looks for configuration in:

1. CLI flags (`--verbose`, `--quiet`, `--json`)
2. Environment variables (`OX_*` prefix)
3. Config file (`.sageox/config.yaml`)
