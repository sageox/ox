#!/bin/sh
# Mutating tests must only ever reach the dedicated firegoat eval team, never a
# production team. A personal login can reach every team its owner belongs to,
# so these tests run only on a team token (oxt_), which is scoped to one team:
# create it for the eval team and export it as SAGEOX_TOKEN before eval.
set -eu
case "${SAGEOX_TOKEN:-}" in
  oxt_*) ;;
  *) echo "SAGEOX_TOKEN must be a team token (oxt_) for the firegoat eval team; refusing to run against a personal login" >&2; exit 1 ;;
esac
