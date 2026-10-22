#!/bin/sh
# Connects the run's repo to the eval team through the public CLI, the state a
# user is in after `ox init`.
set -eu
. "$(dirname "$0")/eval-team.sh"
ox init --no-input --yes --endpoint https://sageox.ai --team "$EVAL_TEAM" >/dev/null
