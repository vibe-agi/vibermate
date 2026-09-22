#!/usr/bin/env bash
set -euo pipefail

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
flutter_directory="$(cd "${script_directory}/.." && pwd)"
geometry_temp="$(mktemp -d /tmp/vibermate-window-geometry.XXXXXX)"
geometry_binary="${geometry_temp}/window-geometry-tests"
trap 'rm -f -- "$geometry_binary"; rmdir -- "$geometry_temp"' EXIT
xctest_platform="$(xcrun --sdk macosx --show-sdk-platform-path)/Developer"

# Run the same XCTest cases as RunnerTests, without launching the application,
# the runtime, or touching a user's saved window/preferences.
xcrun swiftc -D WINDOW_GEOMETRY_STANDALONE \
  -F "${xctest_platform}/Library/Frameworks" \
  -I "${xctest_platform}/usr/lib" -L "${xctest_platform}/usr/lib" \
  -Xlinker -rpath -Xlinker "${xctest_platform}/Library/Frameworks" \
  -Xlinker -rpath -Xlinker "${xctest_platform}/Library/PrivateFrameworks" \
  -Xlinker -rpath -Xlinker "${xctest_platform}/usr/lib" \
  "${flutter_directory}/macos/Runner/WorkbenchWindowGeometry.swift" \
  "${flutter_directory}/macos/RunnerTests/WindowGeometryTests.swift" \
  -o "${geometry_binary}"
"${geometry_binary}"
