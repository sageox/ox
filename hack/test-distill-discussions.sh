#!/usr/bin/env bash
#
# Sets up a temporary project + team context with fake discussion data,
# then runs `ox distill --dry-run` to verify the discussion pipeline detects it.
#
# Usage:
#   ./hack/test-distill-discussions.sh          # dry-run only
#   ./hack/test-distill-discussions.sh --live   # full run with mock backend
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
OX_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Build ox fresh
echo "=== Building ox..."
(cd "$OX_ROOT" && go build -o "$OX_ROOT/ox-test" ./cmd/ox)

# Create temp dirs (auto-cleaned on macOS/Linux via trap)
TMPBASE=$(mktemp -d)
trap 'rm -rf "$TMPBASE" "$OX_ROOT/ox-test"' EXIT

PROJECT_DIR="$TMPBASE/project"
TC_DIR="$TMPBASE/team-context"

echo "=== Setting up temp project at $PROJECT_DIR"
echo "=== Team context at $TC_DIR"

# --- Project setup ---
mkdir -p "$PROJECT_DIR/.sageox"

# config.json (project config)
cat > "$PROJECT_DIR/.sageox/config.json" <<'EOF'
{
  "project_id": "test_project",
  "workspace_id": "test_workspace",
  "team_id": "team-test-123"
}
EOF

# config.local.toml (local config pointing to team context)
cat > "$PROJECT_DIR/.sageox/config.local.toml" <<EOF
[[team_contexts]]
team_id = "team-test-123"
team_name = "Test Team"
path = "$TC_DIR"
EOF

# init git in project dir (findProjectRoot needs .sageox/ but distill state needs it)
(cd "$PROJECT_DIR" && git init -q && git config user.name "Test" && git config user.email "test@test.com" && git config commit.gpgsign false)

# --- Team context setup ---
mkdir -p "$TC_DIR"
(cd "$TC_DIR" && git init -q && git config user.name "Test" && git config user.email "test@test.com" && git config commit.gpgsign false)

# Create an initial commit so git operations work
(cd "$TC_DIR" && touch .gitkeep && git add . && git commit -q -m "init")

# --- Discussion 1: Architecture Review ---
DISC1_DIR="$TC_DIR/discussions/2026-03-10-1423-ryan"
mkdir -p "$DISC1_DIR"

cat > "$DISC1_DIR/metadata.json" <<'EOF'
{
  "recording_id": "rec_arch_review",
  "title": "Architecture Review — API Gateway",
  "created_at": "2026-03-10T14:23:00Z",
  "user_id": "user_ryan"
}
EOF

cat > "$DISC1_DIR/summary.md" <<'EOF'
Team reviewed the API gateway architecture. Decided to move from monolith
to a gateway pattern using envoy. Key concern: latency budget for auth middleware.
EOF

cat > "$DISC1_DIR/transcript.vtt" <<'EOF'
WEBVTT

00:00:00.000 --> 00:00:08.000
<v Ryan>Let's review the API gateway proposal. The main idea is to front all services with Envoy.</v>

00:00:08.000 --> 00:00:15.000
<v Alice>I like it. What's our latency budget for the auth middleware path?</v>

00:00:15.000 --> 00:00:25.000
<v Ryan>We're targeting sub-5ms for the auth check. The JWT validation should be local, no network hop.</v>

00:00:25.000 --> 00:00:35.000
<v Bob>What about rate limiting? Should that be in the gateway too?</v>

00:00:35.000 --> 00:00:45.000
<v Ryan>Yes, rate limiting at the gateway. We'll use a token bucket per client ID.</v>

00:00:45.000 --> 00:00:55.000
<v Alice>Agreed. Let's prototype this next sprint. I'll own the Envoy config.</v>
EOF

# --- Discussion 2: Sprint Planning ---
DISC2_DIR="$TC_DIR/discussions/2026-03-11-0900-alice"
mkdir -p "$DISC2_DIR"

cat > "$DISC2_DIR/metadata.json" <<'EOF'
{
  "recording_id": "rec_sprint_planning",
  "title": "Sprint 14 Planning",
  "created_at": "2026-03-11T09:00:00Z",
  "user_id": "user_alice"
}
EOF

cat > "$DISC2_DIR/summary.md" <<'EOF'
Sprint 14 planning. Prioritized API gateway prototype, auth module refactoring,
and fixing the session timeout bug. Bob to handle the database migration.
EOF

cat > "$DISC2_DIR/transcript.vtt" <<'EOF'
WEBVTT

00:00:00.000 --> 00:00:10.000
<v Alice>Let's plan sprint 14. Top priority is the gateway prototype from yesterday's discussion.</v>

00:00:10.000 --> 00:00:20.000
<v Bob>I can take the database migration. Should be two days max.</v>

00:00:20.000 --> 00:00:30.000
<v Ryan>I'll pair with Alice on the Envoy config. We also need to fix that session timeout bug.</v>

00:00:30.000 --> 00:00:40.000
<v Alice>Good. Let's timebox the session bug to half a day. If it's deeper, we'll spike it next sprint.</v>
EOF

# --- Also create some observations so we can see the combined flow ---
OBS_DIR="$TC_DIR/memory/.observations/2026-03-11"
mkdir -p "$OBS_DIR"

cat > "$OBS_DIR/obs-001.jsonl" <<'EOF'
{"schema_version":"1","recorded_at":"2026-03-11T10:30:00Z"}
{"content":"Team decided to use Envoy as the API gateway — local JWT validation, token-bucket rate limiting per client ID"}
{"content":"Alice owns Envoy config prototype, targeting sprint 14 delivery"}
EOF

echo ""
echo "=== Running ox distill --dry-run ==="
(cd "$PROJECT_DIR" && FEATURE_MEMORY=true "$OX_ROOT/ox-test" distill --dry-run)

echo ""
echo "=== Done. Temp files at $TMPBASE ==="
echo ""
echo "To run interactively:"
echo "  export PATH=\"$OX_ROOT:\$PATH\""
echo "  cd $PROJECT_DIR"
echo "  mv $OX_ROOT/ox-test $OX_ROOT/ox-manual-test"
echo "  $OX_ROOT/ox-manual-test distill --dry-run"
