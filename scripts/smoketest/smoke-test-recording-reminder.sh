#!/usr/bin/env bash
# smoke-test-recording-reminder.sh - Verify recording reminder whispers are produced and delivered
#
# Tests the full recording reminder pipeline:
#   1. Prime an agent (registers with daemon, starts recording)
#   2. Send heartbeats to simulate prompt activity
#   3. Wait for the daemon's RecordingReminderSource to tick (~1 min)
#   4. Pull whispers via ox agent <id> whisper
#   5. Assert the recording reminder appears
#
# NOTE: This test takes up to 90s because the whisper source ticks every 60s.
# Run independently or as an optional slow test — not included in the default
# smoke test suite for CI.
#
# Usage: SAGEOX_ENDPOINT=... SMOKE_TEST_WORKDIR=... OX=./bin/ox bash smoke-test-recording-reminder.sh
#
# Required environment variables (set by smoke-test.sh):
#   SAGEOX_ENDPOINT - API endpoint
#   SMOKE_TEST_WORKDIR - Temp directory for test
#   OX - Path to ox binary

set -euo pipefail

: "${SAGEOX_ENDPOINT:?SAGEOX_ENDPOINT is required}"
: "${SMOKE_TEST_WORKDIR:?SMOKE_TEST_WORKDIR is required}"
: "${OX:?OX is required}"

TEST_REPO="$SMOKE_TEST_WORKDIR/test-init-repo"

if [[ ! -d "$TEST_REPO/.sageox" ]]; then
    echo "error: test-init-repo not initialized (run init test first)"
    exit 1
fi

cd "$TEST_REPO"

# ensure recording is enabled
$OX config set session_recording auto 2>/dev/null || true
$OX config set recording_reminder on 2>/dev/null || true

echo "Step 1: Prime agent to start recording..."

set +e
prime_output=$(AGENT_ENV=claude-code $OX agent prime 2>&1)
prime_exit=$?
set -e

if [[ $prime_exit -ne 0 ]]; then
    echo "error: ox agent prime failed (exit $prime_exit)"
    echo "$prime_output"
    exit 1
fi

agent_id=$(echo "$prime_output" | jq -r '.agent_id // empty')
if [[ -z "$agent_id" ]]; then
    echo "error: no agent_id in prime output"
    echo "$prime_output"
    exit 1
fi

echo "  agent_id=$agent_id"

echo "Step 2: Send heartbeats to simulate prompt activity..."

# send 3 heartbeats to exceed the Claude Code threshold (>= 2)
for i in 1 2 3; do
    AGENT_ENV=claude-code $OX agent "$agent_id" heartbeat 2>/dev/null || true
    sleep 0.5
done

echo "  sent 3 heartbeats"

echo "Step 3: Wait for recording reminder source to produce whisper..."
echo "  (source ticks every 60s, polling whispers every 5s for up to 90s)"

reminder_found=false
for attempt in $(seq 1 18); do
    set +e
    whisper_output=$(AGENT_ENV=claude-code $OX agent "$agent_id" whisper 2>&1)
    set -e

    if echo "$whisper_output" | grep -qiE "Recording (to|this session)"; then
        reminder_found=true
        echo "  reminder found on attempt $attempt"
        break
    fi

    # also check whisper history (doesn't advance cursor, catches already-delivered)
    set +e
    history_output=$(AGENT_ENV=claude-code $OX agent "$agent_id" whisper history 2>&1)
    set -e

    if echo "$history_output" | grep -qiE "Recording (to|this session)"; then
        reminder_found=true
        echo "  reminder found in history on attempt $attempt"
        break
    fi

    sleep 5
done

if [[ "$reminder_found" != "true" ]]; then
    echo "error: recording reminder whisper not delivered after 90s"
    echo "last whisper output: $whisper_output"
    echo "last history output: $history_output"
    exit 1
fi

echo "Step 4: Validate whisper content..."

# first-fire content names the destination ledger and offers a learn-more link
combined_output="${whisper_output}${history_output}"
if ! echo "$combined_output" | grep -qiE "Recording to .* ledger|teammates can read|sageox.ai/rec"; then
    echo "error: whisper content missing expected fields"
    echo "$combined_output"
    exit 1
fi

echo "  content validated"
echo "recording reminder smoke test passed"
exit 0
