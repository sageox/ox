#!/usr/bin/env bash
# Detect "release drift": internal/version/version.go bumped and merged to
# main, but no corresponding GitHub release was ever tagged/published. Left
# unnoticed, GET /releases/latest (and the update-notify chain that reads it)
# keeps pointing at the old version even though version.go says otherwise.
#
# Usage:
#   scripts/check-release-drift.sh [--json]
#
# Exit codes:
#   0  version.go <= latest published release (in sync, or a release is
#      in flight ahead of what version.go on main currently says)
#   1  drift: version.go > latest published release (a version bump was
#      merged but never tagged/published)
#   2  error: could not determine current or latest version (gh missing/
#      unauthenticated, version.go line unparsable, API call failed for a
#      reason other than "no releases exist yet")
#
# A brand-new repo with zero published releases is NOT drift — there is
# nothing to have forgotten to publish — so that case exits 0.
#
# DRIFT_LATEST_OVERRIDE (env var): when set, used as the "latest published
# release" value instead of calling gh/the GitHub API at all. This is a test
# hook for local/manual demonstration of the drift path — not a normal usage
# mode, and intentionally undocumented outside this comment and the workflow.

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

REPO="sageox/ox"
JSON_OUTPUT=0

for arg in "$@"; do
  case "$arg" in
    --json)
      JSON_OUTPUT=1
      ;;
    *)
      echo "Unknown argument: $arg" >&2
      echo "Usage: $0 [--json]" >&2
      exit 2
      ;;
  esac
done

if [ ! -f "internal/version/version.go" ]; then
  echo "Error: must run from repository root (internal/version/version.go not found)" >&2
  exit 2
fi

# Same grep/sed extraction style as scripts/check-versions.sh. Guarded with
# `|| true` so a no-match under `set -o pipefail` doesn't abort the script
# before we can report a clear parse error below.
CURRENT=$(grep 'Version.*=' internal/version/version.go | sed 's/.*"\(.*\)".*/\1/') || true
CURRENT="${CURRENT#v}"

if [ -z "$CURRENT" ]; then
  echo "Error: could not parse Version from internal/version/version.go" >&2
  exit 2
fi

if [ -n "${DRIFT_LATEST_OVERRIDE:-}" ]; then
  # Test hook — see header comment. Skips gh/API entirely.
  LATEST="${DRIFT_LATEST_OVERRIDE#v}"
else
  if ! command -v gh >/dev/null 2>&1; then
    echo "Error: gh CLI is not installed" >&2
    exit 2
  fi
  if ! command -v jq >/dev/null 2>&1; then
    echo "Error: jq is not installed" >&2
    exit 2
  fi
  if ! gh auth status >/dev/null 2>&1; then
    echo "Error: gh CLI is not authenticated (run 'gh auth login')" >&2
    exit 2
  fi

  # releases/latest already excludes drafts/prereleases by GitHub's own
  # semantics, but we double-check explicitly below rather than trusting
  # that implicitly.
  API_STDERR=$(mktemp)
  if API_OUTPUT=$(gh api "repos/${REPO}/releases/latest" 2>"$API_STDERR"); then
    API_STATUS=0
  else
    API_STATUS=$?
  fi
  API_ERR=$(cat "$API_STDERR")
  rm -f "$API_STDERR"

  if [ "$API_STATUS" -ne 0 ]; then
    if echo "$API_ERR" | grep -qi "HTTP 404\|Not Found"; then
      # Brand-new repo, zero releases published yet. Not drift.
      LATEST=""
    else
      echo "Error: failed to query latest release: $API_ERR" >&2
      exit 2
    fi
  elif ! echo "$API_OUTPUT" | jq -e . >/dev/null 2>&1; then
    echo "Error: unexpected (non-JSON) response from gh api releases/latest" >&2
    exit 2
  else
    IS_DRAFT=$(echo "$API_OUTPUT" | jq -r '.draft // false')
    IS_PRERELEASE=$(echo "$API_OUTPUT" | jq -r '.prerelease // false')
    if [ "$IS_DRAFT" = "true" ] || [ "$IS_PRERELEASE" = "true" ]; then
      echo "Error: releases/latest unexpectedly returned a draft or prerelease" >&2
      exit 2
    fi
    LATEST_TAG=$(echo "$API_OUTPUT" | jq -r '.tag_name')
    LATEST="${LATEST_TAG#v}"
  fi
fi

# Portable version compare via `sort -V`; correctly treats equal versions as
# not-greater (checked first, before consulting sort's tiebreak ordering).
version_gt() {
  [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -n1)" = "$1" ]
}

# Validate both values against the release-version grammar before comparing or
# emitting JSON: a malformed value could make `sort -V` report the wrong drift
# state, and a value with JSON-significant characters could corrupt --json.
VERSION_RE='^[0-9]+\.[0-9]+\.[0-9]+([-.+][0-9A-Za-z.-]+)?$'
if ! [[ "$CURRENT" =~ $VERSION_RE ]]; then
  echo "Error: current version '$CURRENT' is not a valid release version" >&2
  exit 2
fi
if [ -n "$LATEST" ] && ! [[ "$LATEST" =~ $VERSION_RE ]]; then
  echo "Error: latest release '$LATEST' is not a valid release version" >&2
  exit 2
fi

DRIFT=false
if [ -n "$LATEST" ] && version_gt "$CURRENT" "$LATEST"; then
  DRIFT=true
fi

if [ "$JSON_OUTPUT" -eq 1 ]; then
  if [ -z "$LATEST" ]; then
    printf '{"current":"%s","latest":null,"drift":%s}\n' "$CURRENT" "$DRIFT"
  else
    printf '{"current":"%s","latest":"%s","drift":%s}\n' "$CURRENT" "$LATEST" "$DRIFT"
  fi
else
  echo "Current version (version.go): $CURRENT"
  if [ -z "$LATEST" ]; then
    echo -e "${YELLOW}No published releases found yet -- nothing to compare against.${NC}"
  else
    echo "Latest published release: v$LATEST"
  fi

  if [ "$DRIFT" = "true" ]; then
    echo -e "${RED}Drift detected: version.go ($CURRENT) is ahead of the latest published release (v$LATEST)${NC}"
    echo "A version bump was merged to main but never tagged/published as a GitHub release."
  else
    echo -e "${GREEN}OK: version.go ($CURRENT) is in sync with the latest published release${NC}"
  fi
fi

if [ "$DRIFT" = "true" ]; then
  exit 1
fi
exit 0
