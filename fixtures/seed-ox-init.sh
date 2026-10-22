#!/bin/sh
# Connects the run's repo to the firegoat eval team through the public CLI, the
# state a user is in after `ox init`. TEAM must name that team explicitly
# (eval --var TEAM=<id>); there is no fallback to the caller's own teams.
set -eu
: "${TEAM:?set the firegoat eval team: eval --var TEAM=<team id>}"
ox init --no-input --yes --endpoint https://sageox.ai --team "$TEAM" >/dev/null
