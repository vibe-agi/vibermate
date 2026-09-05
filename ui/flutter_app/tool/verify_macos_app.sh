#!/usr/bin/env bash

set -euo pipefail

if [[ "$#" -ne 2 ]]; then
  echo "usage: $0 <ViberMate.app> <live|preview>" >&2
  exit 64
fi

input_app="$1"
mode="$2"
case "${mode}" in
  live|preview) ;;
  *)
    echo "bundle mode must be live or preview" >&2
    exit 64
    ;;
esac

if [[ "${input_app}" != /* || ! -d "${input_app}" ]]; then
  echo "App path must be an absolute bundle directory" >&2
  exit 66
fi
app_directory="$(cd "$(dirname "${input_app}")" && pwd -P)"
app="${app_directory}/$(basename "${input_app}")"
if [[ ! -f "${app}/Contents/Info.plist" || -L "${app}/Contents/Info.plist" ]]; then
  echo "App Info.plist is missing or symbolic" >&2
  exit 70
fi

bundle_id="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "${app}/Contents/Info.plist")"
app_executable_name="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "${app}/Contents/Info.plist")"
if [[ "${bundle_id}" != "io.vibermate.desktop" ||
  "${app_executable_name}" != "vibermate-desktop" ]]; then
  echo "App identity is not the shared Desktop authority" >&2
  exit 70
fi

macos_directory="${app}/Contents/MacOS"
if [[ ! -d "${macos_directory}" || -n "$(find "${macos_directory}" -maxdepth 1 -type l -print -quit)" ]]; then
  echo "App executable directory is missing or contains symbolic links" >&2
  exit 70
fi

if [[ "${mode}" == "live" ]]; then
  expected_names=$'vibermate\nvibermate-desktop\nvibermated'
else
  expected_names='vibermate-desktop'
fi
observed_names="$(find "${macos_directory}" -maxdepth 1 -type f -perm -111 -exec basename {} \; | LC_ALL=C sort)"
if [[ "${observed_names}" != "${expected_names}" ]]; then
  echo "App executable members differ from the closed ${mode} layout" >&2
  printf 'observed:\n%s\n' "${observed_names}" >&2
  exit 70
fi

app_executable="${macos_directory}/vibermate-desktop"
if [[ ! -x "${app_executable}" ]]; then
  echo "Desktop executable is unavailable" >&2
  exit 70
fi

if [[ "$(/usr/libexec/PlistBuddy -c 'Print :LSMinimumSystemVersion' "${app}/Contents/Info.plist")" != "14.0" ]]; then
  echo "App must declare the supported macOS 14.0 baseline" >&2
  exit 70
fi
script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
code_paths=(
  "${app_executable}"
  "${app}/Contents/Frameworks/App.framework/Versions/A/App"
  "${app}/Contents/Frameworks/FlutterMacOS.framework/Versions/A/FlutterMacOS"
)
if [[ "${mode}" == "live" ]]; then
  code_paths+=("${macos_directory}/vibermate" "${macos_directory}/vibermated")
fi
node "${script_directory}/verify_macos_build_versions.mjs" "${code_paths[@]}"

if [[ "${mode}" == "live" ]]; then
  cli_executable="${macos_directory}/vibermate"
  daemon_executable="${macos_directory}/vibermated"
  for executable in "${cli_executable}" "${daemon_executable}"; do
    if [[ ! -x "${executable}" || "${app_executable}" -ef "${executable}" ]]; then
      echo "Live bundle executables are unavailable or alias one another" >&2
      exit 70
    fi
  done
  if [[ "${cli_executable}" -ef "${daemon_executable}" ]]; then
    echo "Packaged CLI and daemon alias one another" >&2
    exit 70
  fi
fi

web_root="${app}/Contents/Resources/vibermate-web"
if [[ "${mode}" == "live" ]]; then
  web_index="${web_root}/index.html"
  if [[ ! -d "${web_root}" || -L "${web_root}" ||
    ! -f "${web_index}" || -L "${web_index}" ]]; then
    echo "Live bundle Web management UI is unavailable or symbolic" >&2
    exit 70
  fi
  if [[ -n "$(find "${web_root}" -type l -print -quit)" ]]; then
    echo "Live bundle Web management UI contains a symbolic link" >&2
    exit 70
  fi
elif [[ -e "${web_root}" || -L "${web_root}" ]]; then
  echo "Preview bundle unexpectedly contains the Web management UI" >&2
  exit 70
fi

codesign --verify --deep --strict "${app}"
echo "Verified ${mode} Flutter App: ${app}"
