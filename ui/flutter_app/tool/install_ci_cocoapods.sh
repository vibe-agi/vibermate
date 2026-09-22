#!/usr/bin/env bash

set -euo pipefail

if [[ "${RUNNER_OS:-}" != "macOS" || -z "${RUNNER_TEMP:-}" || -z "${GITHUB_ENV:-}" || -z "${GITHUB_PATH:-}" ]]; then
  echo "CocoaPods CI setup requires a macOS GitHub Actions environment" >&2
  exit 64
fi

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cocoapods_version="$(awk '$1 == "COCOAPODS:" { print $2 }' "${script_directory}/../macos/Podfile.lock")"
if [[ ! "${cocoapods_version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Podfile.lock must pin one exact CocoaPods version" >&2
  exit 65
fi

# Do not inherit a newer runner-installed gem: Flutter invokes `pod` by PATH,
# and a newer CocoaPods can rewrite checksums even for unchanged dependencies.
cocoapods_ruby_bin="$(brew --prefix ruby)/bin"
export PATH="${cocoapods_ruby_bin}:${PATH}"
cocoapods_root="$(mktemp -d "${RUNNER_TEMP%/}/vibermate-cocoapods.XXXXXX")"
export GEM_HOME="${cocoapods_root}"
export GEM_PATH="${cocoapods_root}"
gem install cocoapods --version "${cocoapods_version}" --no-document
test "$("${cocoapods_root}/bin/pod" --version)" = "${cocoapods_version}"

printf 'GEM_HOME=%s\nGEM_PATH=%s\n' "${cocoapods_root}" "${cocoapods_root}" >> "${GITHUB_ENV}"
printf '%s\n%s/bin\n' "${cocoapods_ruby_bin}" "${cocoapods_root}" >> "${GITHUB_PATH}"
echo "Installed locked CocoaPods ${cocoapods_version} in an isolated gem directory"
