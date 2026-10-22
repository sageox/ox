#!/bin/sh
# Runs in build:, as the caller. Makes the eval team hold the content team-recall
# asks about: this repo's public daemon ADR, imported through the public CLI.
# Skips the import when the doc is already there (re-importing adds a commit).
set -eu
. "$(dirname "$0")/eval-team.sh"
team_dir=$(ox team list --json | python3 -c '
import json, sys
teams = json.load(sys.stdin)["teams"]
print(next((t["path"] for t in teams if t["team_id"] == sys.argv[1]), ""))' "$EVAL_TEAM")
test -n "$team_dir" || { echo "not a member of eval team $EVAL_TEAM: run ox login as a member" >&2; exit 1; }
# data/ is kept out of the local checkout, so ask git, not the filesystem
if git -C "$team_dir" ls-tree -r --name-only HEAD -- data/docs | grep -q '/adr-002-daemon-architecture/'; then
  exit 0
fi
ox import docs/adr/002-daemon-architecture.md --team "$EVAL_TEAM" --title "ADR-002: Daemon architecture" >/dev/null
