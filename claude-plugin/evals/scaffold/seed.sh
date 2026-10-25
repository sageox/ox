#!/usr/bin/env bash
# Seed one eval run's sandbox with the anonymized "Acme" fixture:
#   $PWD   <- a fresh git repo initialized for SageOx (.sageox/ present)
#   $HOME  <- fake auth + team-context checkout + local-only ledger
#
# `claude plugin eval` runs this with cwd = the run's scratch dir and
# HOME = the run's sandbox home, so nothing here can touch the developer's
# real ledger, team context, or credentials. Every path derives from $PWD
# and $HOME — never hard-code either.
#
# Idempotent and offline: no `ox` invocation, no network. The files mirror
# what `ox init` + a daemon sync would have produced (same shapes the
# hermetic Go E2E tests stage in cmd/ox/conversation_e2e_workspace_test.go).
set -euo pipefail

fixtures="$(cd "$(dirname "${BASH_SOURCE[0]}")/../fixtures" && pwd)"
repo="$PWD"
sageox_home="$HOME/.local/share/sageox"
config_home="$HOME/.config/sageox"

# Loopback port 9 (discard) has no listener, so every cloud call ox attempts
# (kb fetch, telemetry, query) fails fast with a connection refused instead
# of reaching a real SageOx host with the fake bearer below. Do NOT use a
# *.sageox.ai test host here — those resolve.
endpoint="http://127.0.0.1:9"
endpoint_slug="localhost"
repo_id="repo_acme_eval"
team_id="team_acme_eval"
team_name="Acme Engineering"
team_slug="acme-engineering"

team_dir="$sageox_home/$endpoint_slug/teams/$team_id"
ledger_dir="$sageox_home/$endpoint_slug/ledgers/$repo_id"

# --- git identity scoped to the sandbox (never the developer's) --------------
export GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME="Avery Eval" GIT_AUTHOR_EMAIL="avery@acme.example"
export GIT_COMMITTER_NAME="Avery Eval" GIT_COMMITTER_EMAIL="avery@acme.example"

# --- project repo ------------------------------------------------------------
cp -R "$fixtures/repo/." "$repo/"
mkdir -p "$repo/.sageox"
cat > "$repo/.sageox/config.json" <<JSON
{
  "config_version": "2",
  "repo_id": "$repo_id",
  "team_id": "$team_id",
  "team_name": "$team_name",
  "endpoint": "$endpoint"
}
JSON
cat > "$repo/.sageox/config.local.toml" <<TOML
[ledger]
path = "$ledger_dir"
last_sync = 0001-01-01T00:00:00Z
last_gc = 0001-01-01T00:00:00Z

[[team_contexts]]
team_id = "$team_id"
team_name = "$team_name"
slug = "$team_slug"
path = "$team_dir"
last_sync = 0001-01-01T00:00:00Z
last_gc = 0001-01-01T00:00:00Z
TOML
printf '.sageox/config.local.toml\n' > "$repo/.gitignore"
git -C "$repo" init -q
git -C "$repo" add -A
git -C "$repo" commit -q -m "Acme upload service: initial import"

# --- fake auth so endpoint resolution does not refuse to run -----------------
mkdir -p "$config_home"
chmod 700 "$config_home"
expires="$(date -u -v+1d +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '+1 day' +%Y-%m-%dT%H:%M:%SZ)"
cat > "$config_home/auth.json" <<JSON
{"tokens":{"$endpoint_slug":{"access_token":"eval-access-token","token_type":"Bearer","scope":"*","expires_at":"$expires"}}}
JSON
chmod 600 "$config_home/auth.json"

# --- team context (what the daemon would have synced) ------------------------
mkdir -p "$team_dir"
cp -R "$fixtures/team-context/." "$team_dir/"

# --- ledger: local-only, never pushed ----------------------------------------
# Sessions come from a tab-separated spec (fixtures/ledger/sessions.tsv) so
# sixteen synthetic sessions cost one readable file, not forty-eight. Each row
# becomes the three files a finalized session carries: meta.json, summary.json,
# and a header-only raw.jsonl. "YESTERDAY" is resolved at seed time so the
# recency case stays true whenever the suite runs.
mkdir -p "$ledger_dir/sessions"
yesterday="$(date -u -v-1d +%Y-%m-%dT10-15 2>/dev/null || date -u -d '1 day ago' +%Y-%m-%dT10-15)"
yesterday_iso="$(date -u -v-1d +%Y-%m-%dT10:15:00Z 2>/dev/null || date -u -d '1 day ago' +%Y-%m-%dT10:15:00Z)"
json_escape() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }
while IFS=$'\t' read -r name user agent_id created_at outcome title summary; do
  case "$name" in ''|'#'*) continue ;; esac
  name="${name/YESTERDAY/$yesterday}"
  created_at="${created_at/YESTERDAY/$yesterday_iso}"
  dest="$ledger_dir/sessions/$name"
  mkdir -p "$dest"
  t="$(json_escape "$title")"; s="$(json_escape "$summary")"
  printf '{"type":"header","metadata":{"version":"1.0","agent_type":"claude-code","agent_id":"%s","username":"%s","session_name":"%s"}}\n' \
    "$agent_id" "$user" "$name" > "$dest/raw.jsonl"
  raw_size="$(wc -c < "$dest/raw.jsonl" | tr -d ' ')"
  cat > "$dest/meta.json" <<JSON
{
  "version": "1.0",
  "session_name": "$name",
  "username": "$user",
  "agent_id": "$agent_id",
  "agent_type": "claude-code",
  "model": "claude-opus-5",
  "title": "$t",
  "created_at": "$created_at",
  "entry_count": 12,
  "summary": "$s",
  "files": {"raw.jsonl": {"storage": "git", "size": $raw_size}}
}
JSON
  cat > "$dest/summary.json" <<JSON
{
  "title": "$t",
  "summary": "$s",
  "outcome": "$outcome"
}
JSON
done < "$fixtures/ledger/sessions.tsv"
git -C "$ledger_dir" init -q
git -C "$ledger_dir" add -A
git -C "$ledger_dir" commit -q -m "seed"
