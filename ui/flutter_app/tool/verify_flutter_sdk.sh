#!/usr/bin/env bash

set -euo pipefail

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=flutter-sdk.env
source "${script_directory}/flutter-sdk.env"

if ! command -v flutter >/dev/null 2>&1; then
  echo "Flutter ${VIBERMATE_FLUTTER_VERSION} is required" >&2
  exit 69
fi

version_output="$(flutter --version --machine)"
if ! grep -Fq "\"frameworkVersion\": \"${VIBERMATE_FLUTTER_VERSION}\"" <<<"${version_output}" ||
  ! grep -Fq "\"frameworkRevision\": \"${VIBERMATE_FLUTTER_REVISION}\"" <<<"${version_output}"; then
  echo "Flutter SDK does not match ${VIBERMATE_FLUTTER_VERSION}@${VIBERMATE_FLUTTER_REVISION}" >&2
  exit 70
fi

echo "Flutter SDK verified: ${VIBERMATE_FLUTTER_VERSION}@${VIBERMATE_FLUTTER_REVISION}"
