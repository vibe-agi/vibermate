#!/usr/bin/env bash
# Keep package membership identical to the aggregate short race campaign.
set -eo pipefail

fail() {
  printf 'race shard: %s\n' "$*" >&2
  exit 1
}

usage() {
  printf 'usage: bash tool/test-race-shard.sh native|persistence|persistence-migration|persistence-rest|runtime-data|runtime-product|rest [--list]\n' >&2
  exit 2
}

[[ $# -ge 1 && $# -le 2 ]] || usage
case "$1" in native|persistence|rest|persistence-migration|persistence-rest|runtime-data|runtime-product) shard=$1 ;; *) usage ;; esac
package_shard=$shard
case "$shard" in persistence-*) package_shard=persistence ;; esac
[[ $# -eq 1 || $2 == --list ]] || usage

native=github.com/vibe-agi/vibermate/internal/nativemcp
persistence=github.com/vibe-agi/vibermate/internal/runtimepersistence
runtime_data=github.com/vibe-agi/vibermate/internal/runtimedata
runtime_product=github.com/vibe-agi/vibermate/internal/productruntime

# The sentinel preserves trailing blank lines so malformed discovery fails closed.
# Inherit GOFLAGS/build environment and include race-only packages just as tests do.
discovery=$(go list -race ./... && printf '\001') || fail 'package discovery failed'
discovery=${discovery%$'\001'}
discovery=${discovery%$'\n'}
[[ -n $discovery ]] || fail 'package discovery was empty'

selected=()
seen=$'\n'
have_native=false
have_persistence=false
have_runtime_data=false
have_runtime_product=false
while IFS= read -r package; do
  [[ $package =~ ^[[:alnum:]][[:alnum:]_.~+-]*(/[[:alnum:]_.~+-]+)+$ ]] || fail "invalid discovered package: $package"
  case "/$package/" in */./*|*/../*) fail "invalid discovered package: $package" ;; esac
  case "$seen" in *$'\n'"$package"$'\n'*) fail "duplicate discovered package: $package" ;; esac
  seen+="$package"$'\n'
  case "$package" in
    "$native") have_native=true; [[ $package_shard != native ]] || selected+=("$package") ;;
    "$persistence") have_persistence=true; [[ $package_shard != persistence ]] || selected+=("$package") ;;
    "$runtime_data") have_runtime_data=true; [[ $package_shard != runtime-data ]] || selected+=("$package") ;;
    "$runtime_product") have_runtime_product=true; [[ $package_shard != runtime-product ]] || selected+=("$package") ;;
    *) [[ $package_shard != rest ]] || selected+=("$package") ;;
  esac
done <<< "$discovery"

$have_native && $have_persistence && $have_runtime_data && $have_runtime_product || fail 'package discovery is missing an isolated package'
[[ ${#selected[@]} -gt 0 ]] || fail "no packages selected for $shard"

case "$shard" in
  persistence-migration|persistence-rest)
    arguments=("${shard#persistence-}")
    [[ $# -eq 1 ]] || arguments+=(--list)
    printf '%s\n' "$discovery" | node tool/persistence-race-partition.mjs "${arguments[@]}"
    exit $?
    ;;
esac

if [[ $# -eq 2 ]]; then
  printf '%s\n' "${selected[@]}"
  exit 0
fi

printf 'Race shard %s: %s packages\n' "$shard" "${#selected[@]}"
printf '%s\n' "${selected[@]}"
exec go test -race -short -p=1 "${selected[@]}" -count=1 -timeout=60m
