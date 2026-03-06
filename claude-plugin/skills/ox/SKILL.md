---
name: ox
description: SageOx team context and session recording for AI coworkers. Use when working in a repository that has .sageox/ initialized, when you need team conventions, or when recording coding sessions to a shared ledger.
---

# SageOx (ox) - Team Context for AI Coworkers

ox is a CLI that gives AI coworkers shared team context: conventions, architectural decisions, session recording, and collaborative knowledge that persists across sessions and teammates.

## When to Use

- **Starting work**: Run `ox agent prime` to load team context (conventions, norms, decisions)
- **Recording sessions**: `ox agent $AGENT_ID session start` / `ox agent $AGENT_ID session stop` to capture work to the project ledger
- **Checking health**: `ox status` for auth/sync/daemon state, `ox doctor` for diagnostics
- **Initializing**: `ox init` to set up SageOx in a new repository

## Key Concepts

- **Team Context** - shared conventions, norms, and architectural decisions (versioned)
- **Ledger** - historical record of sessions, decisions, and discussions
- **Session** - a recorded human-to-AI coworker conversation
- **AI Coworker** - an AI participant on a team (not "agent" in user-facing output)

## Essential Commands

| Command | Purpose |
|---------|---------|
| `ox agent prime` | Load team context for this session |
| `ox agent $AGENT_ID session start` | Begin recording a session |
| `ox agent $AGENT_ID session stop` | Stop recording and push to ledger |
| `ox status` | Check auth, sync, daemon health |
| `ox doctor` | Run diagnostic checks |
| `ox init` | Initialize SageOx in a repository |
| `ox conventions` | Get verified team coding standards |
| `ox session list` | List recent sessions from ledger |

## Requirements

Install the ox CLI: `brew install sageox/tap/ox` or visit https://sageox.ai/install
