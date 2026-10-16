# Capture portability fixes

The user approved fixing the 2026-10-16 review findings, test first.

## Required behavior
- Preserve Codex's native provider, stored-login and default-origin selection. Exported API-key presence is not an authentication-mode oracle. Explicit existing base-URL inputs remain supported.
- Capture creation must not fail because a display label is long or contains surrounding whitespace.
- A recovered/replaced client MachineID must not reuse a Login Session bound to another machine. Missing, stale and server-rejected remote sessions must request remote login.
- Keep the existing strict v1 login document readable by both previous and fixed CLIs. The authenticated current-session response is the machine-binding authority: bounded preflight compares its MachineID with the current local identity on run/profile/status/CA. Do not add a redundant persisted binding or schema fields. In-flight status reads may return their authenticated snapshot during concurrent logout/new login, but must never rewrite, resurrect or overwrite stored sessions; resource requests independently authenticate again.
- Support the user-requested compatibility file `~/.vibermate/login-sessions.json` for the default CLI state directory: when the canonical login store does not exist, validate and atomically migrate the complete compatible document into the canonical private store. If both files are absent, a read/open leaves the canonical file absent so a compatibility file supplied later can be recognized. An existing canonical store always wins, including an empty store after logout. Never resurrect a removed session by falling back per target, mask corrupt canonical data, or bypass expiry/server/machine checks.
- HTTP target scheme/default port must be independent of TLS on the Runtime Server connection. CONNECT must preserve already-buffered tunnel bytes.
- Client Relay termination must cancel pending dials and join both copy directions without stranded writes.
- Network establishment, Capture preparation, remote authentication, client capability-probe failure and unsupported capability are distinct errors. Public diagnostics must not expose secrets or arbitrary server response content.
- Capability probing remains bounded and preserves process isolation; a slow probe is not evidence of unsupported flags.
- Investigate and resolve the existing Git launch snapshot test failure without removing its useful metadata/secret-redaction coverage.
- User added that doctor is misleading. Make it a truthful staged diagnostic, including a real credential-free CONNECT authentication-rejection probe through the production Client Relay/Runtime transport. Validate optional explicit Environment availability; report failures with actionable known reasons and clearly mark untested Capture creation, authorized forwarding, native client behavior and upstream inference. Do not equate healthy control/session checks or an expected407 entry probe with end-to-end readiness. No Capture/policy/model action is part of doctor; ordinary connection audit of the probe is permitted.

## Scope and constraints
The accepted test seams are the CLI execute/Launcher.Run outcomes, real Runtime Server login/profile/Capture operations, HTTP/CONNECT sockets through the production handler, and Client Relay socket/lifecycle behavior. Fixtures may replace external network peers and use synthetic credentials. Each implementation slice starts with a failing behavior test and ends with its focused green test.

Preserve the selected Environment, account/link authority, exact executable verification, TLS trust and machine matching checks. Do not rewrite or copy real Codex credentials, synthesize native identities, or weaken interception admission. For native-default Codex routing, retain the Environment's declared authority and let existing request admission match observed origins; unknown configuration must never manufacture new managed authority. The native TUI may continue requiring its own local login.

No automatic deployment, production-account changes, OS trust/proxy changes, unbounded retry, or unrelated redesign. Existing direct-only Runtime Server egress is outside this correction. All code work is in the isolated task worktree.

## Evidence and baseline
Base: a49e1ab1212e49b25f5d1ee1fad29b0eb94587a6.
Installed diagnostic baseline: 1419a51eb636332ad5ef33404f53913d10ac533c; relevant reviewed code is identical.
Review reproductions:
- /tmp/vibermate-relay-review.V7nBlj: HTTP/TLS, omitted port, CONNECT buffering and relay tests.
- /tmp/vibermate-capture-contract-review.f1D6ea: real server identity recovery and cwd labels.
- /var/folders/bv/787rl3c141l2d3mpmtw976zh0000gn/T/vibermate-capture-diagnostic-skbnwgn4/source: error-classification and CLI login tests.
- /tmp/vibermate-origin-review.py and /tmp/vibermate-auth-tui-review.py: isolated native Codex 0.163.0 evidence; no inference request left the rejecting local proxy.

Baseline run: servertransport, clienttarget, capturecontrol, clientadapter and cmd/vibermate passed. runlauncher failed in capability probes and Git snapshot; probe cases passed a serial rerun while Git snapshot still failed. Do not report a green baseline.

