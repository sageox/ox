#!/usr/bin/env bash
# check-kbconfig-drift.sh
#
# Verifies internal/api/kbconfig/{types.go,constants.go,defaults.go,resolve.go}
# stay in sync with sageox-mono's packages/kb/config.go. ox vendors the canonical
# KBConfig Go types (no Go module link to mono is possible — see bead ox-jzlm),
# so drift here turns into wire-format mismatches against the mono service.
# The ResolveEffectiveMode safety-inversion logic is privacy-critical; drift in
# that function is a security bug.
#
# Run on every PR touching internal/api/kbconfig/* and weekly via cron.
#
# Exit codes:
#   0  parity (ok to merge)
#   1  drift detected (fix vendored copy or update allowlist)
#   2  setup error (mono path not found, files missing, etc.)
#
# Configuration:
#   MONO_REPO  path to a sageox-mono checkout. Defaults to ../sageox-mono
#              relative to the ox repo root.

set -uo pipefail

ox_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
mono_repo="${MONO_REPO:-${ox_root}/../sageox-mono}"

# Resolve mono path. The conductor-style mono checkout may be nested one
# level deeper (a sibling workspace directory containing the actual repo);
# probe both shapes.
mono_config=""
for candidate in \
    "${mono_repo}/packages/kb/config.go" \
    "${mono_repo}"/*/packages/kb/config.go; do
    if [[ -f "$candidate" ]]; then
        mono_config="$candidate"
        break
    fi
done

if [[ -z "$mono_config" ]]; then
    echo "ERROR: could not locate packages/kb/config.go under MONO_REPO=${mono_repo}" >&2
    echo "Set MONO_REPO to a sageox-mono checkout root." >&2
    exit 2
fi

ox_types="${ox_root}/internal/api/kbconfig/types.go"
ox_constants="${ox_root}/internal/api/kbconfig/constants.go"
ox_defaults="${ox_root}/internal/api/kbconfig/defaults.go"
ox_resolve="${ox_root}/internal/api/kbconfig/resolve.go"

for f in "$ox_types" "$ox_constants" "$ox_defaults" "$ox_resolve"; do
    if [[ ! -f "$f" ]]; then
        echo "ERROR: missing ox file: $f" >&2
        exit 2
    fi
done

echo "Comparing:"
echo "  mono: $mono_config"
echo "  ox:   internal/api/kbconfig/"
echo

# Drift accumulator. Stays 0 on parity, set to 1 on any mismatch.
drift=0

# Extract struct field declarations (field name + type, ignoring tags) for a
# named struct from a Go file. The matcher is intentionally regex-based and
# tolerant of formatting — it normalises whitespace, drops struct tags, drops
# pure-comment lines, and emits "field type" pairs sorted alphabetically so
# field-order changes don't trigger drift.
#
# Portable awk only (no 3-arg match): we rely on POSIX awk plus a small bit
# of shell glue. BSD awk on macOS does not support gawk's match(s, re, arr)
# 3-arg form, so we capture the struct name via a literal-prefix check
# instead.
extract_struct_fields() {
    local file="$1"
    local struct_name="$2"
    awk -v target="$struct_name" '
        # Inline empty struct on a single line: "type Foo struct{}" — emit
        # nothing (zero fields) and immediately leave the context. Must come
        # before the multi-line opener so the opener does not match first.
        $0 ~ ("^type[[:space:]]+" target "[[:space:]]+struct[[:space:]]*\\{[[:space:]]*\\}") {
            in_struct = 0
            next
        }
        $0 ~ ("^type[[:space:]]+" target "[[:space:]]+struct[[:space:]]*\\{[[:space:]]*$") {
            in_struct = 1
            next
        }
        # any *other* type declaration ends the previous struct context
        /^type[[:space:]]+[A-Za-z_][A-Za-z0-9_]*[[:space:]]+struct/ {
            in_struct = 0
            next
        }
        in_struct && /^\}/ { in_struct = 0; next }
        in_struct {
            line = $0
            # strip leading/trailing whitespace
            sub(/^[[:space:]]+/, "", line)
            sub(/[[:space:]]+$/, "", line)
            # skip blank lines and pure-comment lines
            if (line == "" || line ~ /^\/\//) next
            # drop struct tags (backtick-delimited)
            sub(/`[^`]*`/, "", line)
            # drop trailing line comments
            sub(/[[:space:]]*\/\/.*$/, "", line)
            # collapse internal whitespace
            gsub(/[[:space:]]+/, " ", line)
            sub(/[[:space:]]+$/, "", line)
            if (line != "") print line
        }
    ' "$file" | sort
}

compare_struct() {
    local struct_name="$1"
    local mono_fields ox_fields
    mono_fields=$(extract_struct_fields "$mono_config" "$struct_name")
    ox_fields=$(extract_struct_fields "$ox_types" "$struct_name")

    # SystemConfig has an intentional ox-side addition: ConfigCommittedAt is
    # tolerated on read but absent from mono. Strip it from ox's view before
    # comparing so the rest of the struct still gets a strict diff. Add new
    # whitelisted entries here with a justification comment.
    if [[ "$struct_name" == "SystemConfig" ]]; then
        ox_fields=$(grep -v -F 'ConfigCommittedAt *time.Time' <<<"$ox_fields" || true)
    fi

    if [[ "$mono_fields" == "$ox_fields" ]]; then
        printf '  OK   %-26s (%d fields)\n' "$struct_name" "$(printf '%s\n' "$mono_fields" | grep -c '.' || true)"
        return 0
    fi

    printf '  DRIFT %s\n' "$struct_name"
    echo "    mono fields:"
    printf '      %s\n' "${mono_fields:-<none>}"
    echo "    ox fields (after allowlist):"
    printf '      %s\n' "${ox_fields:-<none>}"
    echo "    diff (mono → ox):"
    diff <(printf '%s\n' "$mono_fields") <(printf '%s\n' "$ox_fields") | sed 's/^/      /'
    drift=1
}

echo "Struct field parity:"
compare_struct "KBConfig"
compare_struct "FeaturesConfig"
compare_struct "MurmursConfig"
compare_struct "SessionRecordingConfig"
compare_struct "SystemConfig"
echo

# Constant parity. Mono's constants live in config.go; ox's live in
# constants.go. Compare value strings only — the names are stable and the
# point is to catch a renamed slug ("auto" → "automatic" would silently
# break the wire format).
compare_const_values() {
    local label="$1"
    local mono_pat="$2"
    local ox_pat="$3"
    local mono_vals ox_vals
    mono_vals=$(grep -E "^[[:space:]]*${mono_pat}" "$mono_config" \
        | sed -E 's/.*=[[:space:]]*"([^"]+)".*/\1/' | sort -u)
    ox_vals=$(grep -E "^[[:space:]]*${ox_pat}" "$ox_constants" \
        | sed -E 's/.*=[[:space:]]*"([^"]+)".*/\1/' | sort -u)
    if [[ "$mono_vals" == "$ox_vals" ]]; then
        printf '  OK   %-26s (%s)\n' "$label" "$(echo "$mono_vals" | paste -sd, -)"
    else
        printf '  DRIFT %s\n' "$label"
        echo "    mono: $(echo "$mono_vals" | paste -sd, -)"
        echo "    ox:   $(echo "$ox_vals" | paste -sd, -)"
        drift=1
    fi
}

echo "Constant value parity:"
compare_const_values "SessionRecording*" \
    "SessionRecording(Auto|Manual|Disabled)[[:space:]]+=" \
    "SessionRecording(Auto|Manual|Disabled)[[:space:]]+="
compare_const_values "Visibility*" \
    "Visibility(Private|Team)[[:space:]]+=" \
    "Visibility(Private|Team)[[:space:]]+="
echo

# KB type values. Mono declares them as typed constants ("KBType = " in
# types.go), ox vendors them as untyped string constants. Compare values.
mono_types_file="$(dirname "$mono_config")/types.go"
if [[ -f "$mono_types_file" ]]; then
    mono_kb_types=$(grep -E '^[[:space:]]*KBType(Personal|Profile|Team|Repo|Custom|Channel)[[:space:]]+KBType[[:space:]]*=' "$mono_types_file" \
        | sed -E 's/.*=[[:space:]]*"([^"]+)".*/\1/' | sort -u)
    ox_kb_types=$(grep -E '^[[:space:]]*KBType(Personal|Profile|Team|Repo|Custom|Channel)[[:space:]]+=' "$ox_constants" \
        | sed -E 's/.*=[[:space:]]*"([^"]+)".*/\1/' | sort -u)
    if [[ "$mono_kb_types" == "$ox_kb_types" ]]; then
        printf 'KB type parity: OK (%s)\n' "$(echo "$mono_kb_types" | paste -sd, -)"
    else
        echo "KB type parity: DRIFT"
        echo "  mono: $(echo "$mono_kb_types" | paste -sd, -)"
        echo "  ox:   $(echo "$ox_kb_types" | paste -sd, -)"
        drift=1
    fi
else
    echo "WARN: $mono_types_file not found; skipping KB-type comparison"
fi
echo

# ResolveEffectiveMode safety-inversion check. Extract the function body
# from both files and compare the key invariant: the "either side disabled
# wins" check must be present and listed FIRST. Pattern-match rather than
# token-diff because formatting may differ slightly across copies.
check_resolve_invariant() {
    local label="$1"
    local file="$2"
    local local_drift=0
    # Pull lines between "func ResolveEffectiveMode" and the matching close brace.
    local body
    body=$(awk '
        /^func ResolveEffectiveMode\(/ { in_fn = 1; depth = 0 }
        in_fn {
            print
            n = gsub(/\{/, "{")
            depth += n
            n = gsub(/\}/, "}")
            depth -= n
            if (depth == 0 && /\}/) { in_fn = 0 }
        }
    ' "$file")
    if [[ -z "$body" ]]; then
        echo "  ERROR $label: ResolveEffectiveMode not found ($file)"
        drift=1
        return
    fi
    # The veto must reference both userMode and kbMode with == SessionRecordingDisabled.
    if ! grep -qE 'userMode[[:space:]]*==[[:space:]]*SessionRecordingDisabled' <<<"$body"; then
        echo "  DRIFT $label: missing userMode == SessionRecordingDisabled check"
        local_drift=1
    fi
    if ! grep -qE 'kbMode[[:space:]]*==[[:space:]]*SessionRecordingDisabled' <<<"$body"; then
        echo "  DRIFT $label: missing kbMode == SessionRecordingDisabled check"
        local_drift=1
    fi
    # The veto must come before the precedence "userMode != \"\"" check.
    # Track BOTH veto patterns (userMode/kbMode) separately and validate the
    # LAST veto occurrence against the precedence check — otherwise a config
    # with the first veto above precedence and a second veto below it would
    # still pass, even though the second one is in the wrong place.
    local user_veto_line kb_veto_line prec_line last_veto_line
    user_veto_line=$(grep -nE 'userMode[[:space:]]*==[[:space:]]*SessionRecordingDisabled' <<<"$body" | head -1 | cut -d: -f1)
    kb_veto_line=$(grep -nE 'kbMode[[:space:]]*==[[:space:]]*SessionRecordingDisabled' <<<"$body" | head -1 | cut -d: -f1)
    last_veto_line=$(printf '%s\n%s\n' "$user_veto_line" "$kb_veto_line" | grep -E '^[0-9]+$' | sort -n | tail -1)
    prec_line=$(grep -nE 'userMode[[:space:]]*!=[[:space:]]*""' <<<"$body" | head -1 | cut -d: -f1)
    if [[ -n "$last_veto_line" && -n "$prec_line" && "$last_veto_line" -ge "$prec_line" ]]; then
        echo "  DRIFT $label: safety inversion (veto) is below precedence check; ORDER IS SEMANTICS"
        local_drift=1
    fi
    if (( local_drift == 0 )); then
        echo "  OK   $label: veto-first + precedence intact"
    else
        drift=1
    fi
}

echo "ResolveEffectiveMode safety-inversion:"
check_resolve_invariant "mono" "$mono_config"
check_resolve_invariant "ox  " "$ox_resolve"
echo

# DefaultSessionRecordingMode per-type defaults. Pull the switch arms from
# both files and compare which constants land in which return.
extract_default_arms() {
    local file="$1"
    awk '
        /^func DefaultSessionRecordingMode/ { in_fn = 1; next }
        in_fn && /^\}/ { in_fn = 0; next }
        in_fn { print }
    ' "$file" | grep -E 'KBType|return Session' | tr -s ' \t' ' '
}

mono_arms=$(extract_default_arms "$mono_config")
ox_arms=$(extract_default_arms "$ox_defaults")
if [[ "$mono_arms" == "$ox_arms" ]]; then
    echo "DefaultSessionRecordingMode arms: OK"
else
    echo "DefaultSessionRecordingMode arms: DRIFT"
    diff <(echo "$mono_arms") <(echo "$ox_arms") | sed 's/^/  /'
    drift=1
fi
echo

if (( drift == 0 )); then
    echo "Result: PARITY — vendored kbconfig is in sync with mono."
    exit 0
fi

echo "Result: DRIFT DETECTED — update internal/api/kbconfig/* to match mono," >&2
echo "       or update the allowlist in this script with justification." >&2
exit 1
