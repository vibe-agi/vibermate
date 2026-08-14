#!/usr/bin/env bash

set -euo pipefail

mode="${1:-live}"
script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
flutter_directory="$(cd "${script_directory}/.." && pwd)"
repository_root="$(cd "${flutter_directory}/../.." && pwd)"
distribution_directory="${repository_root}/dist"
verify_app="${script_directory}/verify_macos_app.sh"

"${script_directory}/verify_flutter_sdk.sh"

case "${mode}" in
  live)
    destination="${distribution_directory}/ViberMate.app"
    ;;
  preview)
    destination="${distribution_directory}/ViberMate-Preview.app"
    ;;
  *)
    echo "usage: $0 [live|preview]" >&2
    exit 64
    ;;
esac

cd "${flutter_directory}"
# A bundle from the other mode can leave authority-bearing sidecars or preview
# assets in Flutter's incremental product directory. Every packaged build must
# therefore begin from a closed empty output, even for ordinary local use.
flutter clean
flutter pub get

if [[ "${mode}" == "preview" ]]; then
  flutter build macos --release --dart-define=VIBERMATE_PREVIEW=true
else
  (
    cd "${repository_root}"
    go build -buildvcs=true -trimpath -tags vibermate_native_secrets \
      -o "${flutter_directory}/build/vibermate" \
      ./cmd/vibermate
    go build -buildvcs=true -trimpath -tags vibermate_native_secrets \
      -o "${flutter_directory}/build/vibermated" \
      ./cmd/vibermated
  )
  flutter build macos --release
  app_bundle="${flutter_directory}/build/macos/Build/Products/Release/ViberMate.app"
  macos_directory="${app_bundle}/Contents/MacOS"
  app_executable_name="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "${app_bundle}/Contents/Info.plist")"
  if [[ "${app_executable_name}" != "vibermate-desktop" ]]; then
    echo "unexpected private App executable: ${app_executable_name}" >&2
    exit 70
  fi
  app_executable="${macos_directory}/${app_executable_name}"
  cli_executable="${macos_directory}/vibermate"
  daemon_executable="${macos_directory}/vibermated"
  if [[ ! -x "${app_executable}" || -e "${cli_executable}" ]]; then
    echo "App executable and packaged CLI do not have distinct bundle paths" >&2
    exit 70
  fi
  ditto \
    "${flutter_directory}/build/vibermate" \
    "${cli_executable}"
  ditto \
    "${flutter_directory}/build/vibermated" \
    "${daemon_executable}"
  chmod 755 "${cli_executable}" "${daemon_executable}"
  if [[ "${app_executable}" -ef "${cli_executable}" ]]; then
    echo "App executable and packaged CLI resolve to the same file" >&2
    exit 70
  fi
  codesign --force --sign - "${cli_executable}"
  codesign --force --sign - "${daemon_executable}"
  case "$(uname -m)" in
    arm64)
      desktop_target="aarch64-apple-darwin"
      ;;
    x86_64)
      desktop_target="x86_64-apple-darwin"
      ;;
    *)
      echo "unsupported macOS build architecture: $(uname -m)" >&2
      exit 70
      ;;
  esac
  node "${script_directory}/desktop_build_manifest.mjs" \
    --app="${app_bundle}" \
    --repository-root="${repository_root}" \
    --target="${desktop_target}"
  # Nested Flutter frameworks and Go sidecars already carry their own ad-hoc
  # signatures. Sign only the outer bundle here so embedding the manifest does
  # not silently rewrite the nested bytes that manifest records.
  codesign --force --sign - "${app_bundle}"
fi

source_app="${flutter_directory}/build/macos/Build/Products/Release/ViberMate.app"
"${verify_app}" "${source_app}" "${mode}"
mkdir -p "${distribution_directory}"

case "${destination}" in
  "${repository_root}/dist/ViberMate.app"|"${repository_root}/dist/ViberMate-Preview.app")
    if [[ -e "${destination}" ]]; then
      rm -rf -- "${destination}"
    fi
    ;;
  *)
    echo "refusing to replace unexpected destination: ${destination}" >&2
    exit 70
    ;;
esac

ditto "${source_app}" "${destination}"
"${verify_app}" "${destination}" "${mode}"
echo "Built ${destination}"
