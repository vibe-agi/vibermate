#!/usr/bin/env bash
# End-to-end check of transparent capture's shared Go path on Linux.
#
# A root emulator (cmd/vibermate-transparent-emulator) stands in for the
# macOS Network Extension: nftables redirects one test user's outbound IPv4
# TCP to it, and it speaks the broker protocol the extension speaks. The real
# vibermated (Desktop daemon) and vibermate run --transparent, built with the
# vibermate_transparent_emulation tag, run as that user. This is not Linux
# transparent capture (ADR 0024 plans a network namespace for that) and it
# does not exercise the Swift provider or the macOS process readers.
#
# Requires passwordless sudo, nftables and outbound HTTPS. Opt-in; never part
# of make check. Usage: tool/transparent-linux-e2e.sh [https://example.com]
set -euo pipefail

repository="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
target_url="${1:-https://example.com}"
target_host="$(python3 -c 'import sys, urllib.parse; print(urllib.parse.urlsplit(sys.argv[1]).hostname)' "${target_url}")"
test_user="vmagent-e2e"
table="vibermate_e2e"
listen_port=15001
work="$(mktemp -d /tmp/vibermate-transparent-e2e.XXXXXX)"
chmod 755 "${work}"
failures=0

log() { printf '\n== %s\n' "$*"; }
pass() { printf '  PASS %s\n' "$*"; }
fail() {
  printf '  FAIL %s\n' "$*"
  failures=$((failures + 1))
  diagnose
}

diagnose() {
  set +e
  echo "  -- live processes of uid ${uid:-?}:"
  local pid
  for pid in $(pgrep -U "${uid:-0}" 2>/dev/null); do
    [[ "$(awk '{print $3}' "/proc/${pid}/stat" 2>/dev/null)" == "Z" ]] && continue
    printf '     %s %s %s\n' "${pid}" "$(awk '{print $3}' "/proc/${pid}/stat" 2>/dev/null)" \
      "$(tr '\0' ' ' <"/proc/${pid}/cmdline" 2>/dev/null | cut -c1-120)"
  done
  echo "  -- emulator log tail:"
  sudo -n tail -5 "${work}/emulator.log" 2>/dev/null | sed 's/^/     /'
  set -e
}

as_user() { sudo -n runuser -u "${test_user}" -- "$@"; }

cleanup() {
  set +e
  sudo -n nft delete table ip "${table}" 2>/dev/null
  sudo -n pkill -f "${work}/bin/vibermate-transparent-emulator" 2>/dev/null
  [[ -n "${uid:-}" ]] && sudo -n pkill -9 -U "${uid}" 2>/dev/null
  if [[ "${failures}" -eq 0 ]]; then
    sudo -n rm -rf "${work}"
  else
    echo "Logs kept in ${work}"
  fi
}
trap cleanup EXIT

# Live (not zombie) pids of the test user matching an exact command line.
user_pids() {
  local pid
  for pid in $(pgrep -U "${uid}" -f "$1" || true); do
    [[ "$(awk '{print $3}' "/proc/${pid}/stat" 2>/dev/null)" != "Z" ]] && echo "${pid}"
  done
  return 0
}

state() { awk '{print $3}' "/proc/$1/stat" 2>/dev/null || echo gone; }

# The Agent of the run whose command ends with $1: the launcher's child.
# Command lines may carry an emulator prefix (Rosetta), so match the launcher
# and take its child rather than matching the Agent's own command line.
agent_of() {
  local launcher
  launcher="$(user_pids "vibermate run --transparent -- $1\$" | head -1)"
  [[ -n "${launcher}" ]] && pgrep -P "${launcher}" | head -1
  return 0
}

wait_for() {
  local description="$1" seconds="$2"
  shift 2
  local deadline=$((SECONDS + seconds))
  until "$@"; do
    if ((SECONDS >= deadline)); then
      fail "${description} (waited ${seconds}s)"
      return 1
    fi
    sleep 0.2
  done
  pass "${description}"
}

state_is() { [[ "$(state "$1")" == "$2" ]]; }

log "Build tagged binaries and the emulator"
(
  cd "${repository}"
  go build -tags vibermate_transparent_emulation -o "${work}/bin/vibermate" ./cmd/vibermate
  go build -tags vibermate_transparent_emulation -o "${work}/bin/vibermated" ./cmd/vibermated
  go build -o "${work}/bin/vibermate-transparent-emulator" ./cmd/vibermate-transparent-emulator
)
chmod -R 755 "${work}/bin"

log "Prepare ${test_user}"
id "${test_user}" >/dev/null 2>&1 || sudo -n useradd --create-home --shell /bin/bash "${test_user}"
uid="$(id -u "${test_user}")"
home="$(getent passwd "${test_user}" | cut -d: -f6)"
# A reused uid may still own processes from an earlier run.
sudo -n pkill -9 -U "${uid}" 2>/dev/null || true
sleep 1
sudo -n rm -rf "${home}/.cache/io.vibermate.desktop" "${home}/e2e"
as_user mkdir -p "${home}/e2e"

log "Start vibermated as the Desktop App would"
cat >"${work}/start-daemon.sh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
cache="\${HOME}/.cache/io.vibermate.desktop"
data="\${HOME}/e2e/data"
mkdir -p "\${cache}" "\${data}"
chmod 700 "\${cache}" "\${data}"
exec 3>"\${HOME}/e2e/bootstrap.jsonl"
exec 0< <(sleep infinity)
exec "${work}/bin/vibermated" --app-cache-dir="\${cache}" --data-dir="\${data}" \\
  --webview-origin=vibermate://desktop --bootstrap-fd=3 --parent-lifetime-fd=0 \\
  --remote-server-listen=127.0.0.1:19666 >"\${HOME}/e2e/daemon.log" 2>&1
EOF
chmod 755 "${work}/start-daemon.sh"
as_user bash -c "cd ~ && setsid -f '${work}/start-daemon.sh' >/dev/null 2>&1"
descriptor_ready() { sudo -n grep -q '"flowPort"' "${home}/e2e/bootstrap.jsonl" 2>/dev/null; }
wait_for "daemon published its bootstrap descriptor with flow rendezvous" 60 descriptor_ready || exit 1
read -r flow_port base_url nonce < <(sudo -n python3 - "${home}/e2e/bootstrap.jsonl" "${work}/flow-secret" <<'EOF'
import json, sys
for line in open(sys.argv[1]):
    record = json.loads(line)
    if "flowPort" in record:
        with open(sys.argv[2], "w") as secret:
            secret.write(record["flowSecret"])
        print(record["flowPort"], record["baseUrl"], record["bootstrapNonce"])
EOF
)
sudo -n chmod 600 "${work}/flow-secret"
session="$(curl -sS -X POST -H "Authorization: Bootstrap ${nonce}" "${base_url}/api/v1/auth/sessions")"
read_token="$(python3 -c 'import json, sys; s = json.loads(sys.argv[1]); print(s.get("readToken") or s.get("read_token") or "")' "${session}")"
[[ -n "${read_token}" ]] && pass "exchanged the bootstrap nonce for an App session" || fail "App session exchange: ${session}"

start_emulator() {
  sudo -n bash -c "nohup '${work}/bin/vibermate-transparent-emulator' --flow-port=${flow_port} \
    --flow-secret-file='${work}/flow-secret' $* >>'${work}/emulator.log' 2>&1 &"
  wait_for "emulator liveness stream open" 10 grep -q 'liveness stream open' "${work}/emulator.log"
}
stop_emulator() {
  sudo -n pkill -f "${work}/bin/vibermate-transparent-emulator" || true
  local deadline=$((SECONDS + 10))
  while pgrep -f "${work}/bin/vibermate-transparent-emulator" >/dev/null && ((SECONDS < deadline)); do sleep 0.1; done
  : >"${work}/emulator.log.tmp"
  sudo -n bash -c "cat '${work}/emulator.log' >> '${work}/emulator.history.log'; : > '${work}/emulator.log'"
}

run_transparent() { as_user bash -c "cd ~ && '${work}/bin/vibermate' run --transparent -- $*"; }

log "A run refuses to start without an active extension"
if output="$(run_transparent true 2>&1)"; then
  fail "run started with no extension: ${output}"
else
  [[ "${output}" == *"extension"* ]] && pass "refused, naming the extension" || fail "refused for another reason: ${output}"
fi

log "A run starts once the extension is active"
start_emulator
if output="$(run_transparent true 2>&1)"; then pass "run of 'true' exited 0"; else fail "run failed: ${output}"; fi

log "Losing the extension pauses the Agent; its return resumes it"
# Background runs get their own session: a hangup from the short-lived
# runuser shell would otherwise end the Agent.
as_user bash -c "cd ~ && setsid -f '${work}/bin/vibermate' run --transparent -- sleep 45 >e2e/pause.out 2>e2e/pause.err"
agent_started() { [[ -n "$(agent_of 'sleep 45')" ]]; }
wait_for "Agent started" 15 agent_started
agent="$(agent_of 'sleep 45')"
stop_emulator
wait_for "Agent ${agent} stopped (T) after the extension went away" 5 state_is "${agent}" T
start_emulator
wait_for "Agent ${agent} running again after the extension returned" 10 state_is "${agent}" S
sudo -n grep -q 'paused' "${home}/e2e/pause.err" && sudo -n grep -q 'resumed' "${home}/e2e/pause.err" &&
  pass "run printed the pause and resume notices" || fail "notices missing: $(sudo -n cat "${home}/e2e/pause.err")"
sudo -n kill "${agent}" 2>/dev/null || true

log "Capture that does not return stops the run"
as_user bash -c "cd ~ && setsid -f bash -c \"'${work}/bin/vibermate' run --transparent -- sleep 120; echo exit=\\\$? > e2e/giveup.code\" >e2e/giveup.out 2>e2e/giveup.err"
giveup_started() { [[ -n "$(agent_of 'sleep 120')" ]]; }
wait_for "Agent started" 15 giveup_started
stop_emulator
giveup_finished() { sudo -n test -s "${home}/e2e/giveup.code"; }
wait_for "run gave up within the 30-second recovery window" 45 giveup_finished
code="$(sudo -n cat "${home}/e2e/giveup.code" 2>/dev/null || true)"
[[ "${code}" != "exit=0" && -n "${code}" ]] && pass "run failed (${code})" || fail "run result: ${code}"
start_emulator --listen=127.0.0.1:${listen_port}

log "Redirect ${test_user}'s outbound IPv4 TCP to the emulator"
# Priority -160 runs after conntrack (-200) but before route-hook chains such
# as a sing-box egress queue (mangle + 1), which would otherwise take the
# connection before the default NAT priority (-100) sees it.
sudo -n nft -f - <<EOF
table ip ${table} {
  chain output {
    type nat hook output priority -160; policy accept;
    meta skuid ${uid} ip daddr != 127.0.0.0/8 meta l4proto tcp redirect to :${listen_port}
  }
}
EOF
sudo -n nft list table ip "${table}" | grep -q "skuid ${uid}" && pass "redirect limited to uid ${uid}" || fail "redirect rule missing"

flow_decisions() { sudo -n grep -c "decision=$1" "${work}/emulator.log" 2>/dev/null || true; }

log "An unrelated process goes direct and is not captured"
before_direct="$(flow_decisions direct)"
code="$(as_user curl -4 -sS -o /dev/null -w '%{http_code}' --max-time 20 "${target_url}" || true)"
[[ "${code}" =~ ^[23] ]] && pass "plain curl succeeded (${code})" || fail "plain curl: ${code}"
(($(flow_decisions direct) > before_direct)) && pass "emulator sent it direct" || fail "no direct decision logged"

# connections_for HOST [RUN_ID]: how many recorded connections name HOST,
# limited to the run's own ingress when RUN_ID is given.
connections_for() {
  curl -sS -H "Authorization: Bearer ${read_token}" -H "Origin: vibermate://desktop" \
    "${base_url}/api/v1/connections?limit=200" >"${work}/connections.json" || return 1
  python3 - "${work}/connections.json" "$1" "${2:-}" <<'EOF'
import json, sys
data = json.load(open(sys.argv[1]))
items = data.get("items", data if isinstance(data, list) else [])
host, run = sys.argv[2], sys.argv[3]
print(sum(1 for item in items
          if host in json.dumps(item) and (not run or run in str(item.get("ingressId", "")))))
EOF
}

run_id() { sudo -n cat "${home}/e2e/$1.id" 2>/dev/null || true; }

# capture_curl NAME: a transparent run that records its Capture Run id, then
# curls the target.
capture_curl() {
  run_transparent sh -c "\"echo \\\$VIBERMATE_CAPTURE_RUN_ID > \\\$HOME/e2e/$1.id; curl -4 -sS -o /dev/null -w %{http_code} --max-time 20 ${target_url}\""
}

log "The Agent's own connection is captured under its run"
before_owned="$(flow_decisions owned)"
if output="$(capture_curl single 2>&1)"; then
  pass "captured curl succeeded (${output##*$'\n'})"
else
  fail "captured curl: ${output}"
fi
(($(flow_decisions owned) > before_owned)) && pass "emulator handed the flow to its run" || fail "no owned decision logged"
single="$(run_id single)"
recorded="$(connections_for "${target_host}" "${single}" || echo 0)"
((recorded > 0)) && pass "Runtime recorded ${recorded} connection(s) to ${target_host} under run ${single}" ||
  fail "Runtime recorded no connection to ${target_host} under run ${single}"

log "Concurrent runs are attributed separately"
capture_curl concurrent-a >"${work}/a.out" 2>&1 &
first=$!
capture_curl concurrent-b >"${work}/b.out" 2>&1 &
second=$!
wait "${first}" && wait "${second}" && pass "both concurrent runs succeeded" || fail "a concurrent run failed: $(cat "${work}/a.out" "${work}/b.out")"
run_a="$(run_id concurrent-a)"
run_b="$(run_id concurrent-b)"
[[ -n "${run_a}" && -n "${run_b}" && "${run_a}" != "${run_b}" ]] && pass "two distinct Capture Runs" || fail "run ids: '${run_a}' '${run_b}'"
for run in "${run_a}" "${run_b}"; do
  recorded="$(connections_for "${target_host}" "${run}" || echo 0)"
  ((recorded > 0)) && pass "run ${run} has its own recorded connection" || fail "run ${run} has no recorded connection"
done

log "A child orphaned before its first connection is captured by token"
before_owned="$(flow_decisions owned)"
as_user rm -f "${home}/e2e/orphan.code"
run_transparent sh -c "\"echo \\\$VIBERMATE_CAPTURE_RUN_ID > \\\$HOME/e2e/orphan.id; sh -c '(sleep 2; curl -4 -sS -o /dev/null -w %{http_code} --max-time 20 ${target_url} > \\\$HOME/e2e/orphan.code) &'; sleep 8\"" >/dev/null 2>&1 || true
orphan_done() { sudo -n test -s "${home}/e2e/orphan.code"; }
wait_for "orphaned curl finished" 30 orphan_done
code="$(sudo -n cat "${home}/e2e/orphan.code" 2>/dev/null || true)"
[[ "${code}" =~ ^[23] ]] && pass "orphaned curl succeeded (${code})" || fail "orphaned curl: ${code}"
(($(flow_decisions owned) > before_owned)) && pass "emulator handed the orphan's flow to its run" || fail "orphan's flow was not owned"
orphan_run="$(run_id orphan)"
recorded="$(connections_for "${target_host}" "${orphan_run}" || echo 0)"
((recorded > 0)) && pass "Runtime recorded the orphan's connection under run ${orphan_run}" ||
  fail "Runtime recorded no connection under the orphan's run ${orphan_run}"
sudo -n grep -E 'decision=' "${work}/emulator.log" | tail -5 | sed 's/^/    /'

log "Result"
if ((failures == 0)); then
  echo "All transparent-capture end-to-end checks passed."
else
  echo "${failures} check(s) failed."
  exit 1
fi
