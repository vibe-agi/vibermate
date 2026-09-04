#!/usr/bin/env bash

set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 <version> <flutter-web-root> <output-directory>" >&2
  exit 64
fi
if [[ "$(uname -s)" != "Linux" ]]; then
  echo "Linux distributions must be assembled on Linux" >&2
  exit 69
fi

version="$1"
web_root="$2"
output_directory="$3"
if [[ ! "${version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "version must be a three-part release number" >&2
  exit 64
fi

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(cd "${script_directory}/../.." && pwd)"
web_root="$(cd "${web_root}" && pwd)"
mkdir -p "${output_directory}"
output_directory="$(cd "${output_directory}" && pwd)"

if [[ ! -f "${web_root}/index.html" || -L "${web_root}" || -L "${web_root}/index.html" ]]; then
  echo "Flutter Web release output is unavailable" >&2
  exit 66
fi
if find "${web_root}" -type l -print -quit | grep -q .; then
  echo "Flutter Web release output must not contain symbolic links" >&2
  exit 66
fi

source_epoch="$(git -C "${repository_root}" show -s --format=%ct HEAD)"
if [[ ! "${source_epoch}" =~ ^[0-9]+$ ]]; then
  echo "source commit time is unavailable" >&2
  exit 70
fi

build_root="$(mktemp -d "${TMPDIR:-/tmp}/vibermate-linux-build.XXXXXX")"
cleanup() {
  rm -rf -- "${build_root}"
}
trap cleanup EXIT

archives=()
for target in amd64:x86_64 arm64:arm64; do
  go_arch="${target%%:*}"
  asset_arch="${target#*:}"
  bundle_name="ViberMate_${version}_linux_${asset_arch}"
  bundle_root="${build_root}/${bundle_name}"
  archive="${output_directory}/${bundle_name}.tar.gz"
  if [[ -e "${archive}" ]]; then
    echo "refusing to replace existing archive: ${archive}" >&2
    exit 73
  fi
  mkdir -p "${bundle_root}"
  (
    cd "${repository_root}"
    CGO_ENABLED=0 GOOS=linux GOARCH="${go_arch}" \
      go build -buildvcs=true -trimpath -tags vibermate_native_secrets \
        -o "${bundle_root}/vibermate" ./cmd/vibermate
    CGO_ENABLED=0 GOOS=linux GOARCH="${go_arch}" \
      go build -buildvcs=true -trimpath -tags vibermate_native_secrets \
        -o "${bundle_root}/vibermated" ./cmd/vibermated
  )
  cp -R "${web_root}" "${bundle_root}/vibermate-web"
  cp "${repository_root}/LICENSE" "${bundle_root}/LICENSE"
  chmod 0755 "${bundle_root}/vibermate" "${bundle_root}/vibermated"
  tar \
    --sort=name \
    --mtime="@${source_epoch}" \
    --clamp-mtime \
    --owner=0 \
    --group=0 \
    --numeric-owner \
    --mode='u+rwX,go+rX,go-w' \
    -C "${build_root}" \
    -czf "${archive}" \
    "${bundle_name}"
  archives+=("$(basename "${archive}")")
done

checksums="${output_directory}/SHA256SUMS-linux"
if [[ -e "${checksums}" ]]; then
  echo "refusing to replace existing checksum file: ${checksums}" >&2
  exit 73
fi
(
  cd "${output_directory}"
  sha256sum "${archives[@]}" > "$(basename "${checksums}")"
)
printf 'Built %s\n' "${archives[@]}" "$(basename "${checksums}")"
