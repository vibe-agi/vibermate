#!/usr/bin/env bash
set -euo pipefail

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
flutter_directory="$(cd "${script_directory}/.." && pwd)"
proxy_temp="$(mktemp -d /tmp/vibermate-transparent-proxy.XXXXXX)"
trap 'rm -rf -- "$proxy_temp"' EXIT
xctest_platform="$(xcrun --sdk macosx --show-sdk-platform-path)/Developer"

# Exercises the real loopback protocol without installing a system extension
# or changing the machine's network configuration.
xcrun swiftc -swift-version 5 -target "$(uname -m)-apple-macosx14.0" \
  -F "${xctest_platform}/Library/Frameworks" \
  -I "${xctest_platform}/usr/lib" -L "${xctest_platform}/usr/lib" \
  -Xlinker -rpath -Xlinker "${xctest_platform}/Library/Frameworks" \
  -Xlinker -rpath -Xlinker "${xctest_platform}/Library/PrivateFrameworks" \
  -Xlinker -rpath -Xlinker "${xctest_platform}/usr/lib" \
  "${flutter_directory}/macos/TransparentProxy/FlowBroker.swift" \
  "${flutter_directory}/macos/TransparentProxy/TCPFlowRelay.swift" \
  "${flutter_directory}/macos/TransparentProxy/TransparentProxyProvider.swift" \
  "${script_directory}/test_transparent_proxy.swift" \
  -lbsm -o "${proxy_temp}/protocol-tests"
"${proxy_temp}/protocol-tests"

# Compile the complete system-extension executable, including its real entry
# point, against the macOS SDK. Do not run it from this test.
xcrun swiftc -swift-version 5 -warnings-as-errors -application-extension \
  -target "$(uname -m)-apple-macosx14.0" -module-name TransparentProxy \
  "${flutter_directory}/macos/TransparentProxy/FlowBroker.swift" \
  "${flutter_directory}/macos/TransparentProxy/TCPFlowRelay.swift" \
  "${flutter_directory}/macos/TransparentProxy/TransparentProxyProvider.swift" \
  "${flutter_directory}/macos/TransparentProxy/main.swift" \
  -framework NetworkExtension -framework Network -lbsm \
  -o "${proxy_temp}/io.vibermate.desktop.transparent-proxy"
plutil -lint "${flutter_directory}/macos/TransparentProxy/Info.plist" \
  "${flutter_directory}/macos/TransparentProxy/TransparentProxy.entitlements"
