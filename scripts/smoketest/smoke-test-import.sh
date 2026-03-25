#!/usr/bin/env bash
# smoke-test-import.sh - Test ox import with a video file
#
# Tests the full import pipeline: LFS upload, metadata creation, cloud notification,
# and (when backend supports it) video processing with keyframes/transcript/summary.
#
# Required environment variables (set by smoke-test.sh):
#   SAGEOX_ENDPOINT - API endpoint
#   SMOKE_TEST_WORKDIR - Temp directory for test
#   OX - Path to ox binary
#
# Optional environment variables:
#   SAGEOX_CI_TEAM_ID - Team ID to import into (skip if not set)

set -euo pipefail

: "${SAGEOX_ENDPOINT:?SAGEOX_ENDPOINT is required}"
: "${SMOKE_TEST_WORKDIR:?SMOKE_TEST_WORKDIR is required}"
: "${OX:?OX is required}"

if [[ -z "${SAGEOX_CI_TEAM_ID:-}" ]]; then
    echo "Skipping: SAGEOX_CI_TEAM_ID not set"
    echo "Set SAGEOX_CI_TEAM_ID to enable import test"
    exit 0
fi

# ---------------------------------------------------------------------------
# Step 1: Generate a synthetic test video with ffmpeg
# ---------------------------------------------------------------------------

TEST_VIDEO="$SMOKE_TEST_WORKDIR/smoke-test-video.mp4"

if command -v ffmpeg &>/dev/null; then
    echo "Generating 5-second synthetic test video..."
    ffmpeg -y -f lavfi -i "testsrc=duration=5:size=320x240:rate=10" \
           -f lavfi -i "sine=frequency=440:duration=5" \
           -c:v libx264 -preset ultrafast -pix_fmt yuv420p \
           -c:a aac -b:a 64k \
           "$TEST_VIDEO" 2>/dev/null
else
    echo "error: ffmpeg not found, cannot generate test video"
    exit 1
fi

if [[ ! -f "$TEST_VIDEO" ]]; then
    echo "error: failed to generate test video"
    exit 1
fi

VIDEO_SIZE=$(wc -c < "$TEST_VIDEO" | tr -d ' ')
echo "Test video generated: $TEST_VIDEO ($VIDEO_SIZE bytes)"

# ---------------------------------------------------------------------------
# Step 2: Import the video file
# ---------------------------------------------------------------------------

echo "Importing video to team $SAGEOX_CI_TEAM_ID..."

set +e
import_output=$($OX import "$TEST_VIDEO" --team "$SAGEOX_CI_TEAM_ID" --force 2>&1)
import_exit=$?
set -e

if [[ $import_exit -ne 0 ]]; then
    echo "error: ox import failed with exit code $import_exit"
    echo "$import_output"
    exit 1
fi

# check for panics
if echo "$import_output" | grep -qi "panic\|goroutine\|stack trace"; then
    echo "error: ox import produced a panic"
    echo "$import_output"
    exit 1
fi

echo "Import output: $import_output"

# extract the import path from output (e.g., "Path: data/docs/2026/03/24/smoke-test-video")
import_path=$(echo "$import_output" | sed -n 's/.*Path: //p' || true)
if [[ -z "$import_path" ]]; then
    echo "error: could not extract import path from output"
    echo "$import_output"
    exit 1
fi

echo "Import path: $import_path"

# ---------------------------------------------------------------------------
# Step 3: Verify local artifacts in the team context repo
# ---------------------------------------------------------------------------

# find the team context directory
TC_BASE="$HOME/.local/share/sageox"

# normalize endpoint to a directory slug (strip https://, trailing slashes)
ep_slug=$(echo "$SAGEOX_ENDPOINT" | sed 's|https://||;s|http://||;s|/$||')

TC_DIR="$TC_BASE/$ep_slug/teams/$SAGEOX_CI_TEAM_ID"

if [[ ! -d "$TC_DIR" ]]; then
    echo "warn: team context directory not found at $TC_DIR"
    echo "Skipping local artifact verification (daemon may not have synced)"
else
    DOC_DIR="$TC_DIR/$import_path"

    echo "Checking local artifacts in $DOC_DIR..."

    # metadata.json must exist
    if [[ ! -f "$DOC_DIR/metadata.json" ]]; then
        echo "error: metadata.json not found in $DOC_DIR"
        exit 1
    fi

    # validate metadata.json fields
    meta_content_type=$(jq -r '.content_type' "$DOC_DIR/metadata.json")
    if [[ "$meta_content_type" != "video/mp4" ]]; then
        echo "error: metadata.json content_type is '$meta_content_type', expected 'video/mp4'"
        exit 1
    fi

    meta_source_oid=$(jq -r '.source_oid' "$DOC_DIR/metadata.json")
    if [[ -z "$meta_source_oid" || "$meta_source_oid" == "null" ]]; then
        echo "error: metadata.json source_oid is missing"
        exit 1
    fi

    meta_source_size=$(jq -r '.source_size' "$DOC_DIR/metadata.json")
    if [[ "$meta_source_size" -le 0 ]]; then
        echo "error: metadata.json source_size is $meta_source_size, expected > 0"
        exit 1
    fi

    echo "  metadata.json: content_type=$meta_content_type, source_size=$meta_source_size ✓"

    # LFS pointer file must exist
    LFS_FILE="$DOC_DIR/smoke-test-video.mp4"
    if [[ ! -f "$LFS_FILE" ]]; then
        echo "error: LFS pointer file not found at $LFS_FILE"
        exit 1
    fi

    # validate it's an LFS pointer (starts with "version https://git-lfs.github.com/spec/v1")
    if ! head -1 "$LFS_FILE" | grep -q "version https://git-lfs.github.com/spec/v1"; then
        echo "error: $LFS_FILE is not a valid LFS pointer"
        exit 1
    fi

    echo "  LFS pointer: valid ✓"
fi

# ---------------------------------------------------------------------------
# Step 4: Verify backend video processing (when supported)
# ---------------------------------------------------------------------------

# Check if the recording appeared in --list (indicates backend processed it)
echo "Checking recordings list for processed video..."

set +e
list_output=$($OX import --list --team "$SAGEOX_CI_TEAM_ID" --json 2>&1)
list_exit=$?
set -e

if [[ $list_exit -ne 0 ]]; then
    echo "warn: ox import --list failed (exit $list_exit), skipping processing verification"
    echo "$list_output"
else
    # look for a recording with title matching our video
    rec_id=$(echo "$list_output" | jq -r '.recordings[] | select(.title | test("smoke.test.video"; "i")) | .id' 2>/dev/null || true)

    if [[ -z "$rec_id" ]]; then
        echo "info: no recording found for imported video (backend may not process file imports yet)"
        echo "  This is expected until the video/* dispatcher route is deployed (see ox-ulyi)"
    else
        echo "Recording found: $rec_id"

        # poll status until ready or timeout (5 minutes)
        echo "Waiting for processing to complete..."
        TIMEOUT=300
        ELAPSED=0
        INTERVAL=5
        FINAL_STATUS=""

        while [[ $ELAPSED -lt $TIMEOUT ]]; do
            set +e
            status_output=$($OX import --status "$rec_id" --team "$SAGEOX_CI_TEAM_ID" --json 2>&1)
            status_exit=$?
            set -e

            if [[ $status_exit -ne 0 ]]; then
                echo "warn: status check failed, retrying..."
                sleep "$INTERVAL"
                ELAPSED=$((ELAPSED + INTERVAL))
                continue
            fi

            current_status=$(echo "$status_output" | jq -r '.status' 2>/dev/null || echo "unknown")

            if [[ "$current_status" == "ready" ]]; then
                FINAL_STATUS="$status_output"
                echo "Processing complete ✓"
                break
            elif [[ "$current_status" == "failed" ]]; then
                echo "error: video processing failed"
                echo "$status_output" | jq '.' 2>/dev/null || echo "$status_output"
                exit 1
            fi

            echo "  status: $current_status (${ELAPSED}s / ${TIMEOUT}s)"
            sleep "$INTERVAL"
            ELAPSED=$((ELAPSED + INTERVAL))
        done

        if [[ -z "$FINAL_STATUS" ]]; then
            echo "error: video processing did not complete within ${TIMEOUT}s"
            exit 1
        fi

        # ---------------------------------------------------------------
        # Assert processing_steps all completed
        # ---------------------------------------------------------------
        for step in upload transcribe summarize commit; do
            step_status=$(echo "$FINAL_STATUS" | jq -r ".processing_steps.${step}.status // \"missing\"")
            if [[ "$step_status" != "completed" && "$step_status" != "complete" ]]; then
                echo "error: processing_steps.$step status is '$step_status', expected 'completed'"
                exit 1
            fi
        done
        echo "  processing_steps: all completed ✓"

        # ---------------------------------------------------------------
        # Assert duration > 0
        # ---------------------------------------------------------------
        duration=$(echo "$FINAL_STATUS" | jq -r '.duration // 0')
        duration_ok=$(echo "$FINAL_STATUS" | jq '.duration != null and .duration > 0')
        if [[ "$duration_ok" != "true" ]]; then
            echo "error: duration is $duration, expected > 0"
            exit 1
        fi
        echo "  duration: ${duration}s ✓"

        # ---------------------------------------------------------------
        # Verify git discussion artifacts (if team context is local)
        # ---------------------------------------------------------------
        if [[ -d "$TC_DIR" ]]; then
            # pull latest to get discussion folder
            (cd "$TC_DIR" && git pull --rebase --autostash 2>/dev/null || true)

            # find the discussion folder by listing discussions/ sorted by date
            DISC_DIR=$(find "$TC_DIR/discussions" -maxdepth 1 -mindepth 1 -type d 2>/dev/null | sort -r | head -1)

            if [[ -n "$DISC_DIR" && -d "$DISC_DIR" ]]; then
                echo "Checking discussion artifacts in $DISC_DIR..."

                # transcript.vtt
                if [[ -f "$DISC_DIR/transcript.vtt" ]]; then
                    vtt_size=$(wc -c < "$DISC_DIR/transcript.vtt" | tr -d ' ')
                    if [[ $vtt_size -gt 0 ]]; then
                        echo "  transcript.vtt: ${vtt_size} bytes ✓"
                    else
                        echo "error: transcript.vtt is empty"
                        exit 1
                    fi
                else
                    echo "error: transcript.vtt not found in discussion folder"
                    exit 1
                fi

                # metadata.json (discussion metadata, not import metadata)
                if [[ -f "$DISC_DIR/metadata.json" ]]; then
                    disc_rec_id=$(jq -r '.recording_id // empty' "$DISC_DIR/metadata.json")
                    if [[ -n "$disc_rec_id" ]]; then
                        echo "  metadata.json: recording_id=$disc_rec_id ✓"
                    else
                        echo "warn: metadata.json missing recording_id"
                    fi
                else
                    echo "error: metadata.json not found in discussion folder"
                    exit 1
                fi

                # keyframes.json
                if [[ -f "$DISC_DIR/keyframes.json" ]]; then
                    kf_count=$(jq -r '.keyframe_count // 0' "$DISC_DIR/keyframes.json")
                    kf_array_len=$(jq -r '.keyframes | length' "$DISC_DIR/keyframes.json")
                    if [[ $kf_count -gt 0 && $kf_array_len -gt 0 ]]; then
                        echo "  keyframes.json: keyframe_count=$kf_count, array_len=$kf_array_len ✓"

                        # verify each keyframe has s3_key and timestamp_seconds
                        bad_kf=$(jq '[.keyframes[] | select(.s3_key == null or .s3_key == "" or .timestamp_seconds == null)] | length' "$DISC_DIR/keyframes.json")
                        if [[ $bad_kf -gt 0 ]]; then
                            echo "error: $bad_kf keyframes missing s3_key or timestamp_seconds"
                            exit 1
                        fi
                        echo "  keyframes: all have s3_key + timestamp_seconds ✓"
                    else
                        echo "error: keyframes.json has keyframe_count=$kf_count, array_len=$kf_array_len"
                        exit 1
                    fi
                else
                    echo "error: keyframes.json not found in discussion folder"
                    exit 1
                fi

                # summary.md
                if [[ -f "$DISC_DIR/summary.md" ]]; then
                    sm_size=$(wc -c < "$DISC_DIR/summary.md" | tr -d ' ')
                    if [[ $sm_size -gt 0 ]]; then
                        echo "  summary.md: ${sm_size} bytes ✓"
                    else
                        echo "error: summary.md is empty"
                        exit 1
                    fi
                else
                    echo "error: summary.md not found in discussion folder"
                    exit 1
                fi

                # summary.json
                if [[ -f "$DISC_DIR/summary.json" ]]; then
                    sj_version=$(jq -r '.schema_version // 0' "$DISC_DIR/summary.json")
                    sj_chapters=$(jq -r '.chapters | length' "$DISC_DIR/summary.json")
                    if [[ $sj_version -ge 1 ]]; then
                        echo "  summary.json: schema_version=$sj_version, chapters=$sj_chapters ✓"
                    else
                        echo "error: summary.json schema_version=$sj_version, expected >= 1"
                        exit 1
                    fi
                else
                    echo "error: summary.json not found in discussion folder"
                    exit 1
                fi

                # annotations.json (optional — may not exist for short videos)
                if [[ -f "$DISC_DIR/annotations.json" ]]; then
                    ann_version=$(jq -r '.schema_version // 0' "$DISC_DIR/annotations.json")
                    echo "  annotations.json: schema_version=$ann_version (optional, present) ✓"
                else
                    echo "  annotations.json: not present (optional, ok for short videos)"
                fi
            else
                echo "warn: no discussion folder found in $TC_DIR/discussions/"
                echo "  Backend may not have committed artifacts yet"
            fi
        fi
    fi
fi

echo ""
echo "ox import smoke test passed"
exit 0
