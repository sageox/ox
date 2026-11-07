#!/bin/sh
# Gives the run the caller's existing `ox login`. A personal login reaches every
# team its owner belongs to, so tests using it pin the eval team in their setup
# or checks. Copies access tokens only, so a run can never rotate the caller's
# real refresh token, and refuses tokens that would expire mid-run.
set -eu
real_home=$(eval echo "~$(id -un)")
src="$real_home/.config/sageox/auth.json"
dest_dir="${XDG_CONFIG_HOME:-$HOME/.config}/sageox"
test -s "$src" || { echo "no ox login at $src: run 'ox login' first" >&2; exit 1; }
# outside a firegoat run HOME is the caller's own: never rewrite the real login
if [ "$(cd "$real_home" && pwd -P)" = "$(cd "$HOME" 2>/dev/null && pwd -P)" ]; then
  echo "refusing to copy ox login onto itself (HOME is the caller's real home)" >&2
  exit 1
fi
mkdir -p "$dest_dir"
python3 - "$src" "$dest_dir/auth.json" <<'PY'
import json, os, sys
from datetime import datetime, timedelta, timezone

src, dest = sys.argv[1], sys.argv[2]
if os.path.exists(dest) and os.path.samefile(src, dest):
    sys.exit('refusing to copy ox login onto itself')
# runs time out at 900s; leave room for setup and checks
min_expiry = datetime.now(timezone.utc) + timedelta(minutes=20)
auth = json.load(open(src))
for endpoint, token in auth.get('tokens', {}).items():
    expires = token.get('expires_at', '')
    if endpoint == 'https://sageox.ai' and (not expires or datetime.fromisoformat(expires.replace('Z', '+00:00')) < min_expiry):
        sys.exit(f'ox login for {endpoint} expires before the run ends: run any ox command (or ox login) to refresh it, then retry')
    token.pop('refresh_token', None)
    token.pop('session_token', None)
with open(dest, 'w') as out:
    json.dump(auth, out)
PY
chmod 600 "$dest_dir/auth.json"
