#!/usr/bin/env bash
# Builds the self-contained Web workbench: CanvasKit and the engine's fallback
# fonts are part of the output, so the browser never loads a third-party origin.
# Extra arguments go to `flutter build web` (for example --dart-define or -o).
set -euo pipefail

cd "$(dirname "$0")/.."
output="build/web"
arguments=("$@")
for ((index = 0; index < ${#arguments[@]}; index++)); do
  case "${arguments[index]}" in
    -o | --output) output="${arguments[index + 1]}" ;;
    --output=*) output="${arguments[index]#--output=}" ;;
  esac
done

flutter build web --release --no-web-resources-cdn "$@"
node tool/web_fonts.mjs sync "${output}"
