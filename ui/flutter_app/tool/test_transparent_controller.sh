#!/usr/bin/env bash
set -euo pipefail
script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
flutter_directory="$(cd "${script_directory}/.." && pwd)"
controller_temp="$(mktemp -d /tmp/vibermate-transparent-controller.XXXXXX)"
controller_binary="${controller_temp}/controller-tests"
trap 'rm -f -- "$controller_binary"; rmdir -- "$controller_temp"' EXIT
xctest_platform="$(xcrun --sdk macosx --show-sdk-platform-path)/Developer"
xcrun swiftc -D TRANSPARENT_CONTROLLER_STANDALONE \
  -F "${xctest_platform}/Library/Frameworks" \
  -I "${xctest_platform}/usr/lib" -L "${xctest_platform}/usr/lib" \
  -Xlinker -rpath -Xlinker "${xctest_platform}/Library/Frameworks" \
  -Xlinker -rpath -Xlinker "${xctest_platform}/Library/PrivateFrameworks" \
  -Xlinker -rpath -Xlinker "${xctest_platform}/usr/lib" \
  "${flutter_directory}/macos/Runner/TransparentProxyController.swift" \
  "${flutter_directory}/macos/RunnerTests/TransparentProxyControllerTests.swift" \
  -o "${controller_binary}"
"${controller_binary}"
