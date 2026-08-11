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

codesign --verify --deep --strict "${app}"
echo "Verified ${mode} Flutter App: ${app}"
