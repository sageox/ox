#!/usr/bin/env bash
# Decrypt SOPS-encrypted secrets and output docker -e flags.
#
# Usage:
#   docker run $(./test-envs/decrypt-env.sh) ox-test-image ...
#
# Outputs:
#   -e ANTHROPIC_API_KEY=sk-... -e OPENAI_API_KEY=sk-... etc.
#
# If no age key is found, outputs nothing (graceful fallback for
# external contributors and environments without secrets access).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOPS_AGE_KEY_FILE="${SOPS_AGE_KEY_FILE:-${HOME}/.config/sageox/test-age-key.txt}"

if [ ! -f "$SOPS_AGE_KEY_FILE" ]; then
    echo "# No age key found at $SOPS_AGE_KEY_FILE -- running without API keys" >&2
    exit 0
fi

export SOPS_AGE_KEY_FILE

SECRETS_FILE="${SCRIPT_DIR}/secrets.enc.yaml"
if [ ! -f "$SECRETS_FILE" ]; then
    echo "# No encrypted secrets file at $SECRETS_FILE -- running without API keys" >&2
    exit 0
fi

sops -d "$SECRETS_FILE" | yq -r '.api_keys | to_entries[] | "-e " + .key + "=" + .value'
