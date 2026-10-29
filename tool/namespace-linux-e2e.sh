#!/usr/bin/env bash
# End-to-end check of Linux transparent capture (ADR 0024) with release
# binaries: no build tags, no emulator, no root.
#
# The real vibermated (Desktop daemon) runs in a throwaway HOME, and
# vibermate run --transparent starts each Agent in its own user, network and
# mount namespace. The Agents carry no proxy settings; their TCP and UDP reach
# the Runtime only through the run's ingress, and the Runtime records each
# connection under the run.
#
# Requires unprivileged user namespaces, /dev/net/tun, curl, python3 and
# outbound HTTPS. Opt-in; never part of make check.
# Usage: tool/namespace-linux-e2e.sh [https://example.com]
set -euo pipefail

repository="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
target_url="${1:-https://example.com}"
target_host="$(python3 -c 'import sys, urllib.parse; print(urllib.parse.urlsplit(sys.argv[1]).hostname)' "${target_url}")"
work="$(mktemp -d "${TMPDIR:-/tmp}/vibermate-namespace-e2e.XXXXXX")"
home="${work}/home"
mkdir -p "${home}/e2e"
failures=0

log() { printf '\n== %s\n' "$*"; }
pass() { printf '  PASS %s\n' "$*"; }
fail() {
  printf '  FAIL %s\n' "$*"
  failures=$((failures + 1))
}

cleanup() {
  set +e
  pkill -f "${work}/bin/" 2>/dev/null
  [[ -n "${echo_pid:-}" ]] && kill "${echo_pid}" 2>/dev/null
  [[ -n "${http_pid:-}" ]] && kill "${http_pid}" 2>/dev/null
  if [[ "${failures}" -eq 0 ]]; then
    rm -rf "${work}"
  else
    echo "Logs kept in ${work}"
  fi
}
trap cleanup EXIT

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

# vibermate and the daemon find each other through HOME's cache directory.
in_home() { (cd "${home}" && HOME="${home}" XDG_CACHE_HOME= XDG_CONFIG_HOME= "$@"); }
run_transparent() { in_home "${work}/bin/vibermate" run --transparent -- "$@"; }
run_id() { cat "${home}/e2e/$1.id" 2>/dev/null || true; }

log "Build release binaries"
(
  cd "${repository}"
  go build -o "${work}/bin/vibermate" ./cmd/vibermate
  go build -o "${work}/bin/vibermated" ./cmd/vibermated
)

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
  --remote-server-listen=127.0.0.1:0 >"\${HOME}/e2e/daemon.log" 2>&1
EOF
chmod 755 "${work}/start-daemon.sh"
HOME="${home}" XDG_CACHE_HOME= setsid -f "${work}/start-daemon.sh" >/dev/null 2>&1
descriptor_ready() { grep -q '"bootstrapNonce"' "${home}/e2e/bootstrap.jsonl" 2>/dev/null; }
wait_for "daemon published its bootstrap descriptor" 60 descriptor_ready || exit 1
read -r base_url nonce < <(python3 - "${home}/e2e/bootstrap.jsonl" <<'EOF'
import json, sys
for line in open(sys.argv[1]):
    record = json.loads(line)
    if "bootstrapNonce" in record:
        print(record["baseUrl"], record["bootstrapNonce"])
EOF
)
session="$(curl -sS -X POST -H "Authorization: Bootstrap ${nonce}" "${base_url}/api/v1/auth/sessions")"
read_token="$(python3 -c 'import json, sys; s = json.loads(sys.argv[1]); print(s.get("readToken") or s.get("read_token") or "")' "${session}")"
[[ -n "${read_token}" ]] && pass "exchanged the bootstrap nonce for an App session" || fail "App session exchange: ${session}"
daemon_port="${base_url##*:}"

# connections_for TEXT RUN_ID: how many recorded connections mention TEXT
# under the run's own ingress.
connections_for() {
  curl -sS -H "Authorization: Bearer ${read_token}" -H "Origin: vibermate://desktop" \
    "${base_url}/api/v1/connections?limit=200" >"${work}/connections.json" || return 1
  python3 - "${work}/connections.json" "$1" "$2" <<'EOF'
import json, sys
data = json.load(open(sys.argv[1]))
items = data.get("items", data if isinstance(data, list) else [])
text, run = sys.argv[2], sys.argv[3]
print(sum(1 for item in items if text in json.dumps(item) and run and run in str(item.get("ingressId", ""))))
EOF
}

# Recording is asynchronous, so a check polls briefly before failing.
expect_recorded() {
  local text="$1" run="$2" what="$3"
  recorded() { (($(connections_for "${text}" "${run}" || echo 0) > 0)); }
  wait_for "Runtime recorded ${what} under run ${run}" 10 recorded
}

log "The Agent connects by name with no proxy settings and is captured"
probe='echo "$VIBERMATE_CAPTURE_RUN_ID" > "$HOME/e2e/$0.id"
env | grep -qi "^https\?_proxy=" && echo proxy-set
grep -q "^nameserver 198.18.0.2" /etc/resolv.conf || echo resolver-not-redirected
curl -sS -o /dev/null -w "%{http_code}" --max-time 20 "$1"'
if output="$(run_transparent sh -c "${probe}" single "${target_url}" 2>"${work}/single.err")"; then
  [[ "${output}" =~ ^[23][0-9][0-9]$ ]] && pass "captured curl succeeded (${output})" || fail "captured curl: ${output}"
else
  fail "captured run failed: ${output} $(cat "${work}/single.err")"
fi
expect_recorded "${target_host}" "$(run_id single)" "the connection to ${target_host}"

log "Concurrent runs are attributed separately"
run_transparent sh -c "${probe}" concurrent-a "${target_url}" >"${work}/a.out" 2>&1 &
first=$!
run_transparent sh -c "${probe}" concurrent-b "${target_url}" >"${work}/b.out" 2>&1 &
second=$!
wait "${first}" && wait "${second}" && pass "both concurrent runs succeeded" || fail "a concurrent run failed: $(cat "${work}/a.out" "${work}/b.out")"
run_a="$(run_id concurrent-a)"
run_b="$(run_id concurrent-b)"
[[ -n "${run_a}" && -n "${run_b}" && "${run_a}" != "${run_b}" ]] && pass "two distinct Capture Runs" || fail "run ids: '${run_a}' '${run_b}'"
for run in "${run_a}" "${run_b}"; do expect_recorded "${target_host}" "${run}" "its own connection"; done

log "A child that leaves the Agent's session and process group is still captured"
run_transparent sh -c 'echo "$VIBERMATE_CAPTURE_RUN_ID" > "$HOME/e2e/orphan.id"
setsid sh -c "sleep 2; curl -sS -o /dev/null -w %{http_code} --max-time 20 $0 > \$HOME/e2e/orphan.code" &
sleep 6' "${target_url}" >/dev/null 2>&1 || true
orphan_done() { test -s "${home}/e2e/orphan.code"; }
wait_for "orphaned curl finished" 30 orphan_done
code="$(cat "${home}/e2e/orphan.code" 2>/dev/null || true)"
[[ "${code}" =~ ^[23] ]] && pass "orphaned curl succeeded (${code})" || fail "orphaned curl: ${code}"
expect_recorded "${target_host}" "$(run_id orphan)" "the orphan's connection"

log "UDP goes through the Runtime, never around it"
host_ip="$(ip -4 -o addr show scope global | awk '{print $4}' | cut -d/ -f1 | head -1)"
python3 - "${host_ip}" >"${work}/udp-echo.log" 2>&1 <<'EOF' &
import socket, sys
server = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
server.bind((sys.argv[1], 0))
print(server.getsockname()[1], flush=True)
while True:
    payload, peer = server.recvfrom(2048)
    print("from", peer[0], flush=True)
    server.sendto(payload, peer)
EOF
echo_pid=$!
echo_ready() { test -s "${work}/udp-echo.log"; }
wait_for "UDP echo listening on ${host_ip}" 5 echo_ready
echo_port="$(head -1 "${work}/udp-echo.log")"
reply="$(run_transparent python3 -c '
import os, socket, sys
open(os.path.expanduser("~/e2e/udp.id"), "w").write(os.environ["VIBERMATE_CAPTURE_RUN_ID"])
client = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
client.settimeout(10)
client.sendto(b"ping over udp", (sys.argv[1], int(sys.argv[2])))
print(client.recvfrom(100)[0].decode())' "${host_ip}" "${echo_port}" 2>/dev/null || true)"
[[ "${reply}" == "ping over udp" ]] && pass "UDP echo answered the Agent" || fail "UDP reply: '${reply}'"
expect_recorded "${host_ip}" "$(run_id udp)" "the UDP flow to ${host_ip}"

log "The Agent's localhost is its own"
if run_transparent python3 -c '
import socket, sys
socket.create_connection(("127.0.0.1", int(sys.argv[1])), timeout=3)' "${daemon_port}" 2>/dev/null; then
  fail "the Agent reached the daemon on host loopback"
else
  pass "host loopback (daemon port ${daemon_port}) is unreachable from the namespace"
fi

log "Capture fails closed when the launcher dies"
rm -f "${home}/e2e/closed.code"
in_home setsid -f "${work}/bin/vibermate" run --transparent -- sh -c \
  "sleep 3; curl -sS -o /dev/null -w %{http_code} --max-time 10 ${target_url} > \$HOME/e2e/closed.code 2>&1; echo \" exit=\$?\" >> \$HOME/e2e/closed.code" \
  >"${work}/closed.out" 2>&1
launcher_started() { pgrep -f "${work}/bin/vibermate run --transparent -- sh -c sleep 3" >/dev/null; }
wait_for "launcher started" 10 launcher_started
sleep 1
pkill -9 -f "${work}/bin/vibermate run --transparent -- sh -c sleep 3" || true
sleep 12
code="$(cat "${home}/e2e/closed.code" 2>/dev/null || true)"
if [[ -z "${code}" ]]; then
  pass "the Agent ended with its launcher"
elif [[ "${code}" == *"exit=0"* ]]; then
  fail "the Agent reached ${target_host} after its launcher died: ${code}"
else
  pass "the Agent's connection failed once its launcher died (${code//$'\n'/ })"
fi

log "A Runtime Server: capture through --server, and the capture scope (ADR 0025)"
server_data="${work}/server-data"
"${work}/bin/vibermated" server --data-dir "${server_data}" --listen 127.0.0.1:0 \
  >"${work}/server.out" 2>"${work}/server.err" </dev/null &
server_ready() { test -s "${work}/server.out"; }
wait_for "Runtime Server ready" 60 server_ready || exit 1
server="$(python3 -c 'import json, sys; print(json.loads(open(sys.argv[1]).readline())["listenAddress"])' "${work}/server.out")"
password="e2e-$(python3 -c 'import secrets; print(secrets.token_hex(16))')"
recovery_key="$("${work}/bin/vibermated" server recovery-key --data-dir "${server_data}")"
python3 - "http://${server}" "${recovery_key}" "${password}" >"${work}/server-session.json" <<'PY'
import json, sys, urllib.request
body = json.dumps({"schema": "vibermate-web-setup-v1", "recoveryKey": sys.argv[2],
                   "username": "owner", "password": sys.argv[3]}).encode()
request = urllib.request.Request(sys.argv[1] + "/api/v1/server/web-setup", data=body,
                                 headers={"Content-Type": "application/json"}, method="POST")
print(urllib.request.urlopen(request).read().decode())
PY
server_token() { python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))[sys.argv[2]])' "${work}/server-session.json" "$1"; }

# publish_environment ID SCOPE_JSON: publish a minimal Environment with a
# capture scope (or none, when SCOPE_JSON is null).
publish_environment() {
  python3 - "http://${server}" "$(server_token writeToken)" "$1" "$2" <<'PY'
import json, sys, urllib.request
origin, token, environment, scope = sys.argv[1:]
draft = {"expectedDraftRevision": 0, "name": environment, "state": "active",
         "clientEndpoints": [], "pluginBindings": [], "budgetPolicy": {"id": "", "revision": 0},
         "contentRecording": {"mode": "off", "retentionDays": 0}, "launchEnvironment": {}}
if json.loads(scope) is not None:
    draft["captureScope"] = json.loads(scope)
def call(method, path, revision, key, body=None):
    request = urllib.request.Request(origin + path, data=body, method=method, headers={
        "Authorization": "Bearer " + token, "Content-Type": "application/json",
        "If-Match": str(revision), "Idempotency-Key": key})
    urllib.request.urlopen(request).read()
call("PUT", f"/api/v1/environments/{environment}/draft", 0, f"e2e-{environment}-draft-0001", json.dumps(draft).encode())
for action in ("preview", "publish"):
    call("POST", f"/api/v1/environments/{environment}/draft/actions/{action}", 1, f"e2e-{environment}-{action}-0001")
PY
}
publish_environment plain null && pass "published Environment 'plain' without a capture scope"
publish_environment scoped "{\"rules\":[{\"id\":\"this-machine\",\"kind\":\"prefix\",\"value\":\"${host_ip}/32\"}]}" &&
  pass "published Environment 'scoped' keeping ${host_ip} off ViberMate"

printf 'owner\n%s\n' "${password}" | in_home "${work}/bin/vibermate" login --server "http://${server}" >"${work}/login.out" 2>&1 &&
  pass "logged in to the Runtime Server" || fail "login: $(cat "${work}/login.out")"

# server_connections TEXT RUN: recorded connections on the Server mentioning
# TEXT under the run.
server_connections() {
  curl -sS -H "Authorization: Bearer $(server_token readToken)" \
    "http://${server}/api/v1/connections?limit=200" >"${work}/server-connections.json" || return 1
  python3 - "${work}/server-connections.json" "$1" "$2" <<'PY'
import json, sys
items = json.load(open(sys.argv[1])).get("items", [])
print(sum(1 for item in items if sys.argv[2] in json.dumps(item) and sys.argv[3] and sys.argv[3] in str(item.get("ingressId", ""))))
PY
}
run_server() { local environment="$1"; shift; in_home "${work}/bin/vibermate" run --server "http://${server}" --env "${environment}" --transparent -- "$@"; }

python3 - "${host_ip}" >"${work}/http-service.log" 2>&1 <<'PY' &
import http.server, sys
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.end_headers(); self.wfile.write(b"from this machine")
    def log_message(self, *args): pass
server = http.server.HTTPServer((sys.argv[1], 0), Handler)
print(server.server_address[1], flush=True)
server.serve_forever()
PY
http_pid=$!
http_ready() { test -s "${work}/http-service.log"; }
wait_for "HTTP service listening on ${host_ip}" 5 http_ready
service="http://${host_ip}:$(head -1 "${work}/http-service.log")/"

output="$(run_server plain sh -c "${probe}" server-plain "${target_url}" 2>"${work}/server-plain.err" || true)"
[[ -n "${output}" ]] &&
  [[ "${output}" =~ ^[23] ]] && pass "captured through the Server (${output})" || fail "captured through the Server: ${output} $(cat "${work}/server-plain.err")"
server_recorded() { (($(server_connections "${target_host}" "$(run_id server-plain)" || echo 0) > 0)); }
wait_for "the Server recorded ${target_host} under run $(run_id server-plain)" 10 server_recorded

# The service exists only on this machine's network. Through a Server it is
# refused (a Server does not connect to private addresses for a client); kept
# off ViberMate by the scope, it is reached directly.
bypass_probe='echo "$VIBERMATE_CAPTURE_RUN_ID" > "$HOME/e2e/$0.id"; curl -sS --max-time 10 "$1"'
body="$(run_server scoped sh -c "${bypass_probe}" service-scoped "${service}" 2>"${work}/service-scoped.err" || true)"
[[ "${body}" == "from this machine" ]] && pass "scoped: the Agent reached ${service} directly" ||
  fail "scoped: ${body} $(cat "${work}/service-scoped.err")"
body="$(run_server plain sh -c "${bypass_probe}" service-plain "${service}" 2>"${work}/service-plain.err" || true)"
[[ "${body}" != "from this machine" ]] && pass "plain: the Server refused ${service}, as it should for a client's private address" ||
  fail "plain: a Server reached the client's private service"
sleep 2
scoped_seen="$(server_connections "${host_ip}" "$(run_id service-scoped)" || echo error)"
[[ "${scoped_seen}" == "0" ]] && pass "scoped: the Server recorded nothing for ${host_ip} (it left directly)" ||
  fail "scoped: the Server recorded ${scoped_seen} connection(s) to ${host_ip}"
plain_recorded() { (($(server_connections "${host_ip}" "$(run_id service-plain)" || echo 0) > 0)); }
wait_for "plain: the Server recorded the refused attempt under its run" 10 plain_recorded

udp_reply="$(run_server scoped python3 -c '
import os, socket, sys
open(os.path.expanduser("~/e2e/udp-scoped.id"), "w").write(os.environ["VIBERMATE_CAPTURE_RUN_ID"])
client = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
client.settimeout(10)
client.sendto(b"direct over udp", (sys.argv[1], int(sys.argv[2])))
print(client.recvfrom(100)[0].decode())' "${host_ip}" "${echo_port}" 2>/dev/null || true)"
[[ "${udp_reply}" == "direct over udp" ]] && pass "scoped UDP answered directly" || fail "scoped UDP reply: '${udp_reply}'"
sleep 2
udp_seen="$(server_connections "${host_ip}" "$(run_id udp-scoped)" || echo error)"
[[ "${udp_seen}" == "0" ]] && pass "scoped: the Server recorded no UDP flow" || fail "scoped: the Server recorded ${udp_seen} UDP flow(s)"
kill "${http_pid}" 2>/dev/null || true

log "Result"
if ((failures == 0)); then
  echo "All Linux namespace-capture end-to-end checks passed."
else
  echo "${failures} check(s) failed."
  exit 1
fi
