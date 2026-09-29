#!/usr/bin/env bash
# Fails unless a Web workbench root loads nothing from a third-party origin:
# CanvasKit is local and every engine fallback font is bundled and verified.
set -euo pipefail

if [[ $# -ne 1 || ! -d "$1" ]]; then
  echo "usage: $0 <web root>" >&2
  exit 64
fi
web_root="$1"
bootstrap="${web_root}/flutter_bootstrap.js"
if [[ ! -f "${web_root}/canvaskit/canvaskit.wasm" ]] ||
  ! grep -q '"useLocalCanvasKit":true' "${bootstrap}" ||
  ! grep -q 'fontFallbackBaseUrl: "fonts/"' "${bootstrap}"; then
  echo "Web workbench is not self-contained: ${web_root}" >&2
  exit 70
fi
node "$(dirname "${BASH_SOURCE[0]}")/web_fonts.mjs" verify "${web_root}"
