#!/usr/bin/env bash
# Query npm/Go registries for available versions of AI coding agents
# Usage: ./agent-versions.sh <agent-name> [count]
# Example: ./agent-versions.sh claude-code 5

set -euo pipefail

# Check for required argument
if [ $# -eq 0 ]; then
    echo "Usage: $0 <agent-name> [count]" >&2
    echo "Example: $0 claude-code 5" >&2
    echo "Available agents: claude-code, codex, gemini, amp, pi, opencode, code-puppy" >&2
    exit 1
fi

agent=$1
count=${2:-5}

case "$agent" in
  claude-code)
    npm view @anthropic-ai/claude-code versions --json | jq -r ".[-${count}:][]"
    ;;
  codex)
    npm view @openai/codex versions --json | jq -r ".[-${count}:][]"
    ;;
  gemini)
    npm view @google/gemini-cli versions --json | jq -r ".[-${count}:][]"
    ;;
  amp)
    npm view @sourcegraph/amp versions --json | jq -r ".[-${count}:][]"
    ;;
  pi)
    npm view @mariozechner/pi-coding-agent versions --json | jq -r ".[-${count}:][]"
    ;;
  opencode)
    # For Go modules, use go list to get available versions
    go list -m -versions github.com/opencode-ai/opencode 2>/dev/null | tr ' ' '\n' | tail -"$count"
    ;;
  code-puppy)
    npm view codepuppy versions --json | jq -r ".[-${count}:][]"
    ;;
  *)
    echo "Unknown agent: $agent" >&2
    echo "Available agents: claude-code, codex, gemini, amp, pi, opencode, code-puppy" >&2
    exit 1
    ;;
esac