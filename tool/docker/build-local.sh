#!/usr/bin/env bash
set -euo pipefail

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(cd "${script_directory}/../.." && pwd)"
flutter_bin="${VIBERMATE_FLUTTER_BIN:-flutter}"
source "${repository_root}/ui/flutter_app/tool/flutter-sdk.env"

sdk_version="$("${flutter_bin}" --version --machine)"
if [[ "${sdk_version}" != *"\"frameworkRevision\": \"${VIBERMATE_FLUTTER_REVISION}\""* ]]; then
  echo "Flutter ${VIBERMATE_FLUTTER_VERSION}@${VIBERMATE_FLUTTER_REVISION} is required" >&2
  exit 70
fi
target_arch="$(docker version --format '{{.Server.Arch}}')"
case "${target_arch}" in
  amd64|arm64) ;;
  *) echo "Docker architecture must be amd64 or arm64" >&2; exit 64 ;;
esac

(
  cd "${repository_root}/ui/flutter_app"
  "${flutter_bin}" pub get --enforce-lockfile
  "${flutter_bin}" build web --release --no-pub
)

cd "${repository_root}"
mkdir -p dist/docker/vibermate-web
CGO_ENABLED=0 GOOS=linux GOARCH="${target_arch}" \
  go build -buildvcs=true -trimpath -tags vibermate_native_secrets \
    -o dist/docker/vibermated ./cmd/vibermated
CGO_ENABLED=0 GOOS=linux GOARCH="${target_arch}" \
  go build -buildvcs=true -trimpath -tags vibermate_native_secrets \
    -o dist/docker/vibermate ./cmd/vibermate
cp -R ui/flutter_app/build/web/. dist/docker/vibermate-web/
cp LICENSE dist/docker/LICENSE
cp LICENSE dist/docker/vibermate-web/LICENSE
cp THIRD_PARTY_LICENSES.md dist/docker/THIRD_PARTY_LICENSES.md
docker compose build --build-arg "VIBERMATE_SOURCE_REVISION=$(git rev-parse HEAD)"
