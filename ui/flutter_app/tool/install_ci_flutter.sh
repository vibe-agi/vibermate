#!/usr/bin/env bash

set -euo pipefail

if [[ "$#" -lt 1 || "$#" -gt 2 ]]; then
  echo "usage: $0 <empty-destination> [macos|web]" >&2
  exit 64
fi

destination="$1"
precache_target="${2:-macos}"
script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=flutter-sdk.env
source "${script_directory}/flutter-sdk.env"

if [[ -z "${destination}" || "${destination}" != /* || -e "${destination}" ]]; then
  echo "Flutter destination must be an absent absolute path" >&2
  exit 64
fi
if [[ "${precache_target}" != "macos" && "${precache_target}" != "web" ]]; then
  echo "Flutter precache target must be macos or web" >&2
  exit 64
fi

git clone \
  --branch "${VIBERMATE_FLUTTER_VERSION}" \
  --depth 1 \
  --single-branch \
  https://github.com/flutter/flutter.git \
  "${destination}"

observed_revision="$(git -C "${destination}" rev-parse HEAD)"
if [[ "${observed_revision}" != "${VIBERMATE_FLUTTER_REVISION}" ]]; then
  echo "Flutter tag resolved to unexpected revision ${observed_revision}" >&2
  exit 70
fi

"${destination}/bin/flutter" config --no-analytics
"${destination}/bin/flutter" precache "--${precache_target}"
echo "Installed Flutter ${VIBERMATE_FLUTTER_VERSION} at ${destination}"
