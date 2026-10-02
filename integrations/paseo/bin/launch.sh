case "$provider" in
  claude) binary=${VIBERMATE_PASEO_CLAUDE_BINARY:-claude} ;;
  codex) binary=${VIBERMATE_PASEO_CODEX_BINARY:-codex} ;;
  *) exit 2 ;;
esac

# Availability/version/auth probes have no Paseo conversation identity.
if [ -z "${PASEO_AGENT_ID:-}" ]; then
  exec "$binary" "$@"
fi

if [ -z "${VIBERMATE_PASEO_ENVIRONMENT_ID:-}" ]; then
  printf '%s\n' 'ViberMate: this conversation has no selected profile; enable and configure the Paseo plugin.' >&2
  exit 1
fi

if [ -n "${PASEO_AGENT_CWD:-}" ]; then
  cd "$PASEO_AGENT_CWD"
fi

cli=${VIBERMATE_PASEO_CLI:-vibermate}
if [ -n "${VIBERMATE_PASEO_SERVER:-}" ]; then
  exec "$cli" run --server "$VIBERMATE_PASEO_SERVER" --env "$VIBERMATE_PASEO_ENVIRONMENT_ID" -- "$binary" "$@"
fi
exec "$cli" run --env "$VIBERMATE_PASEO_ENVIRONMENT_ID" -- "$binary" "$@"
