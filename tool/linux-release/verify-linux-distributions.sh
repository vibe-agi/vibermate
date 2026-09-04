#!/usr/bin/env bash

set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <version> <distribution-directory>" >&2
  exit 64
fi
if [[ "$(uname -s)" != "Linux" ]]; then
  echo "Linux distributions must be verified on Linux" >&2
  exit 69
fi

version="$1"
distribution_directory="$(cd "$2" && pwd)"
if [[ ! "${version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "version must be a three-part release number" >&2
  exit 64
fi

for command in curl go jq sha256sum tar; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "required command is unavailable: ${command}" >&2
    exit 69
  fi
done

(
  cd "${distribution_directory}"
  sha256sum --check --strict SHA256SUMS-linux
)

verification_root="$(mktemp -d "${TMPDIR:-/tmp}/vibermate-linux-verify.XXXXXX")"
server_pid=""
cleanup() {
  if [[ -n "${server_pid}" ]] && kill -0 "${server_pid}" 2>/dev/null; then
    kill -TERM "${server_pid}" 2>/dev/null || true
    wait "${server_pid}" 2>/dev/null || true
  fi
  rm -rf -- "${verification_root}"
}
trap cleanup EXIT

for asset_arch in x86_64 arm64; do
  bundle_name="ViberMate_${version}_linux_${asset_arch}"
  archive="${distribution_directory}/${bundle_name}.tar.gz"
  if [[ ! -f "${archive}" || -L "${archive}" ]]; then
    echo "distribution archive is missing: ${archive}" >&2
    exit 66
  fi
  while IFS= read -r member; do
    case "${member}" in
      "${bundle_name}"|"${bundle_name}/"|"${bundle_name}/"*) ;;
      *)
        echo "archive member escapes the fixed bundle root: ${member}" >&2
        exit 65
        ;;
    esac
    if [[ "${member}" == /* || "${member}" == *"/../"* || "${member}" == ../* ]]; then
      echo "archive member has an unsafe path: ${member}" >&2
      exit 65
    fi
  done < <(tar -tzf "${archive}")
  tar -xzf "${archive}" -C "${verification_root}"
  bundle_root="${verification_root}/${bundle_name}"
  if [[ ! -x "${bundle_root}/vibermate" ||
        ! -x "${bundle_root}/vibermated" ||
        ! -f "${bundle_root}/vibermate-web/index.html" ||
        ! -f "${bundle_root}/LICENSE" ]]; then
    echo "distribution bundle is incomplete: ${bundle_name}" >&2
    exit 66
  fi
  if find "${bundle_root}" -type l -print -quit | grep -q .; then
    echo "distribution bundle contains a symbolic link: ${bundle_name}" >&2
    exit 65
  fi
  go version -m "${bundle_root}/vibermate" >/dev/null
  go version -m "${bundle_root}/vibermated" >/dev/null
done

case "$(uname -m)" in
  x86_64) host_arch="x86_64" ;;
  aarch64|arm64) host_arch="arm64" ;;
  *)
    echo "unsupported Linux verification architecture: $(uname -m)" >&2
    exit 69
    ;;
esac

host_root="${verification_root}/ViberMate_${version}_linux_${host_arch}"
"${host_root}/vibermate" --help >/dev/null
status_file="${verification_root}/server-status.json"
error_file="${verification_root}/server-error.log"
data_directory="${verification_root}/server-data"
"${host_root}/vibermated" server \
  --listen 127.0.0.1:0 \
  --data-dir "${data_directory}" \
  >"${status_file}" 2>"${error_file}" &
server_pid="$!"

for _ in $(seq 1 100); do
  if [[ -s "${status_file}" ]]; then
    break
  fi
  if ! kill -0 "${server_pid}" 2>/dev/null; then
    echo "packaged Runtime Server exited before becoming ready" >&2
    sed -n '1,80p' "${error_file}" >&2
    exit 70
  fi
  sleep 0.1
done

listen_address="$(jq -er '
  select(.ready == true and .scheme == "http" and .managementUi == true)
  | .listenAddress
' "${status_file}")"
admin_key_path="$(jq -er '.adminAccessKeyPath' "${status_file}")"
if [[ "$(stat -c '%a' "${admin_key_path}")" != "600" ]]; then
  echo "packaged Runtime Server admin key permissions are not owner-only" >&2
  exit 70
fi
curl --fail --silent --show-error "http://${listen_address}/" >/dev/null
kill -TERM "${server_pid}"
wait "${server_pid}"
server_pid=""
if [[ -s "${error_file}" ]]; then
  echo "packaged Runtime Server wrote an unexpected diagnostic" >&2
  sed -n '1,80p' "${error_file}" >&2
  exit 70
fi

echo "Verified Linux distributions and packaged Web Runtime startup"
