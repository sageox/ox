#!/usr/bin/env bash
# Run the OxfsCore swift-testing suite.
#
# On a Command Line Tools-only box, swift-testing's Testing.framework and
# lib_TestingInterop.dylib are not on SwiftPM's default search/rpath, so we
# add them. On a full-Xcode box those paths don't exist and the toolchain
# finds Testing itself, so we fall back to a plain `swift test`.
set -euo pipefail
cd "$(dirname "$0")/.."

DEV="$(xcode-select -p)"
FW="$DEV/Library/Developer/Frameworks"
INTEROP="$DEV/Library/Developer/usr/lib"

FLAGS=()
if [ -d "$FW/Testing.framework" ]; then
    FLAGS=(
        -Xswiftc -F"$FW"
        -Xlinker -F"$FW"
        -Xlinker -rpath -Xlinker "$FW"
        -Xlinker -rpath -Xlinker "$INTEROP"
    )
fi

if [ "${#FLAGS[@]}" -eq 0 ]; then
    swift test "$@"
else
    swift test "${FLAGS[@]}" "$@"
fi
exec ./scripts/test-oxdirtest.sh
