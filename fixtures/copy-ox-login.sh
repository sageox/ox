#!/bin/sh
# Gives the run the caller's existing `ox login`. Only for tests whose writes are
# pinned to the eval team by the seed step (the agent never picks a team): a
# personal login reaches every team its owner belongs to. Copies access tokens
# only, so a run can never rotate the caller's real refresh token.
set -eu
real_home=$(eval echo "~$(id -un)")
src="$real_home/.config/sageox/auth.json"
test -s "$src" || { echo "no ox login at $src: run 'ox login' first" >&2; exit 1; }
dest="${XDG_CONFIG_HOME:-$HOME/.config}/sageox"
mkdir -p "$dest"
python3 - "$src" "$dest/auth.json" <<'PY'
import json, sys
auth = json.load(open(sys.argv[1]))
for token in auth.get('tokens', {}).values():
    token.pop('refresh_token', None)
    token.pop('session_token', None)
with open(sys.argv[2], 'w') as out:
    json.dump(auth, out)
PY
chmod 600 "$dest/auth.json"
