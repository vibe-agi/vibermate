#!/usr/bin/env bash

set -euo pipefail

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
flutter_directory="$(cd "${script_directory}/.." && pwd)"
repository_root="$(cd "${flutter_directory}/../.." && pwd)"
target="universal-apple-darwin"
release_directory="${flutter_directory}/build/distribution/${target}/release"
app_directory="${release_directory}/bundle/macos"
app_bundle="${app_directory}/ViberMate.app"
r0_directory="${release_directory}/r0-dist"
expected_developer_directory="/Applications/Xcode_16.2.app/Contents/Developer"

if [[ "$#" -ne 0 ]]; then
  echo "the Flutter macOS distribution builder accepts no arguments" >&2
  exit 64
fi
if [[ "$(uname -s)" != "Darwin" || "$(uname -m)" != "arm64" ]]; then
  echo "the admitted distribution host is macOS arm64" >&2
  exit 69
fi
if [[ "${DEVELOPER_DIR:-}" != "${expected_developer_directory}" ]]; then
  echo "DEVELOPER_DIR is not the admitted Xcode 16.2 installation" >&2
  exit 69
fi
if [[ -n "$(git -C "${repository_root}" status --porcelain=v1 --untracked-files=all)" ]]; then
  echo "a distribution build requires a clean Git worktree" >&2
  exit 65
fi

"${script_directory}/verify_flutter_sdk.sh"
(
  cd "${flutter_directory}"
  flutter clean
  flutter pub get
)
if [[ -n "$(git -C "${repository_root}" status --porcelain=v1 --untracked-files=all)" ]]; then
  echo "Flutter dependency resolution changed the admitted source" >&2
  exit 65
fi
(
  cd "${flutter_directory}"
  flutter build macos \
    --release \
    --build-name=0.1.0 \
    --build-number=1
)

source_app="${flutter_directory}/build/macos/Build/Products/Release/ViberMate.app"
if [[ ! -d "${source_app}" || -e "${release_directory}" ]]; then
  echo "Flutter did not produce one fresh release App" >&2
  exit 70
fi

temporary_directory="$(mktemp -d "${TMPDIR:-/tmp}/vibermate-flutter-distribution.XXXXXX")"
cleanup() {
  case "${temporary_directory}" in
    "${TMPDIR:-/tmp}"/vibermate-flutter-distribution.*)
      rm -rf -- "${temporary_directory}"
      ;;
    *)
      echo "refusing to clean an unexpected distribution temporary path" >&2
      return 1
      ;;
  esac
}
trap cleanup EXIT

clang="$(xcrun --find clang)"
build_slice() {
  local command_name="$1"
  local go_architecture="$2"
  local clang_architecture="$3"
  local output="$4"
  (
    cd "${repository_root}"
    CGO_ENABLED=1 \
      GOARCH="${go_architecture}" \
      GOENV=off \
      GOFLAGS= \
      GOOS=darwin \
      GOWORK=off \
      CC="${clang}" \
      CGO_CFLAGS="-arch ${clang_architecture} -mmacosx-version-min=14.0" \
      CGO_CXXFLAGS="-arch ${clang_architecture} -mmacosx-version-min=14.0" \
      CGO_LDFLAGS="-arch ${clang_architecture} -mmacosx-version-min=14.0" \
      MACOSX_DEPLOYMENT_TARGET=14.0 \
      go build \
        -buildvcs=true \
        -trimpath \
        -tags=vibermate_native_secrets \
        -o "${output}" \
        "./cmd/${command_name}"
  )
}

sidecar_directory="${temporary_directory}/sidecars"
mkdir -p "${sidecar_directory}"
for command_name in vibermate vibermated; do
  arm64_slice="${sidecar_directory}/${command_name}.arm64"
  x86_64_slice="${sidecar_directory}/${command_name}.x86_64"
  universal_sidecar="${sidecar_directory}/${command_name}"
  build_slice "${command_name}" arm64 arm64 "${arm64_slice}"
  build_slice "${command_name}" amd64 x86_64 "${x86_64_slice}"
  lipo -create \
    "${arm64_slice}" \
    "${x86_64_slice}" \
    -output "${universal_sidecar}"
  if [[ "$(lipo -archs "${universal_sidecar}")" != "x86_64 arm64" &&
    "$(lipo -archs "${universal_sidecar}")" != "arm64 x86_64" ]]; then
    echo "${command_name} is not a two-slice universal sidecar" >&2
    exit 70
  fi
  chmod 755 "${universal_sidecar}"
done

mkdir -p "${app_directory}"
ditto --norsrc --noextattr --noacl --noqtn -X "${source_app}" "${app_bundle}"
macos_directory="${app_bundle}/Contents/MacOS"
for command_name in vibermate vibermated; do
  destination="${macos_directory}/${command_name}"
  if [[ -e "${destination}" ]]; then
    echo "Flutter bundle unexpectedly contains ${command_name}" >&2
    exit 70
  fi
  /usr/bin/install -m 0755 \
    "${sidecar_directory}/${command_name}" \
    "${destination}"
  codesign --force --sign - "${destination}"
done

VIBERMATE_RELEASE_REQUIRE_CLEAN=1 \
  node "${script_directory}/desktop_build_manifest.mjs" \
    --app="${app_bundle}" \
    --repository-root="${repository_root}" \
    --target="${target}"
codesign --force --sign - "${app_bundle}"

for code_path in \
  "${app_bundle}/Contents/MacOS/vibermate-desktop" \
  "${app_bundle}/Contents/MacOS/vibermate" \
  "${app_bundle}/Contents/MacOS/vibermated" \
  "${app_bundle}/Contents/Frameworks/App.framework/Versions/A/App" \
  "${app_bundle}/Contents/Frameworks/FlutterMacOS.framework/Versions/A/FlutterMacOS"; do
  architectures="$(lipo -archs "${code_path}")"
  if [[ "${architectures}" != "x86_64 arm64" &&
    "${architectures}" != "arm64 x86_64" ]]; then
    echo "distribution code object is not universal: ${code_path}" >&2
    exit 70
  fi
done
"${script_directory}/verify_macos_app.sh" "${app_bundle}" live
if [[ "$(/usr/libexec/PlistBuddy -c 'Print :LSMinimumSystemVersion' "${app_bundle}/Contents/Info.plist")" != "14.0" ]]; then
  echo "distribution App does not require the admitted macOS 14 baseline" >&2
  exit 70
fi

# R0 source-traceability consumes a flattened, symlink-free view of every
# Flutter-owned runtime member. The signed App transfer remains authoritative;
# this directory exists only so SBOM generation cannot omit framework code or
# Flutter assets merely because they live behind framework symlinks.
mkdir -p \
  "${r0_directory}/App.framework" \
  "${r0_directory}/FlutterMacOS.framework"
/usr/bin/install -m 0755 \
  "${app_bundle}/Contents/Frameworks/App.framework/Versions/A/App" \
  "${r0_directory}/App.framework/App"
/usr/bin/install -m 0755 \
  "${app_bundle}/Contents/Frameworks/FlutterMacOS.framework/Versions/A/FlutterMacOS" \
  "${r0_directory}/FlutterMacOS.framework/FlutterMacOS"
ditto --norsrc --noextattr --noacl --noqtn -X \
  "${app_bundle}/Contents/Frameworks/App.framework/Versions/A/Resources" \
  "${r0_directory}/App.framework/Resources"
ditto --norsrc --noextattr --noacl --noqtn -X \
  "${app_bundle}/Contents/Frameworks/FlutterMacOS.framework/Versions/A/Resources" \
  "${r0_directory}/FlutterMacOS.framework/Resources"
if [[ -n "$(find "${r0_directory}" -type l -print -quit)" ]]; then
  echo "R0 Flutter runtime input contains a symbolic link" >&2
  exit 70
fi
if [[ -n "$(git -C "${repository_root}" status --porcelain=v1 --untracked-files=all)" ]]; then
  echo "source changed during the Flutter distribution build" >&2
  exit 65
fi

printf 'Built unsigned Flutter distribution candidate: %s\n' "${app_bundle}"
