# Capture portability implementation plan

> **For agentic workers:** Use superpowers:subagent-driven-development to implement each task and review it before the next task. Each slice uses red → green.

**Goal:** Fix the approved remote Capture, proxy protocol, native Codex selection and diagnostic defects with behavioral regressions.

**Architecture:** Keep the existing control and proxy contracts. Correct native-selection assumptions and state/lifecycle consistency at their owning interfaces; do not add a parallel launcher or replace the proxy.

**Tech Stack:** Go 1.26.9, net/http, net/tls, existing SQLite-backed Runtime Server fixtures, macOS Codex CLI for optional isolated native verification.

**Spec:** docs/superpowers/specs/2026-10-16-capture-portability-fixes.md

## Global Constraints
- TDD: one failing public-behavior regression, observe failure, minimal implementation, focused pass, then next slice.
- Work only in this task worktree. Do not read real credential payloads or modify installed applications, OS trust, user logins, or production servers.
- Preserve Environment/account authority, exact executable verification, TLS trust, machine binding, and child process isolation.
- Never turn unknown native Codex configuration into invented target/auth facts or new managed authority.
- Public diagnostics contain only bounded known reason/stage data, not raw credentials, server bodies, arbitrary headers or child output.
- Use existing tests/fixtures and synthetic local peers; no model requests to external services.
- Do not implement general HTTP_PROXY support, change release versions, or deploy.
- One implementation worker at a time; task review after each commit. Run focused tests during the loop and affected package suites once at task completion.

### Task 1: Correct HTTP destination handling and CONNECT buffering

**Files:** internal/loopbackproxy/handler.go, existing HTTP/CONNECT test fixtures, a focused regression test file.
**Interfaces:** Production HTTP handler accepts normal absolute-form HTTP requests and authenticated CONNECT on both plaintext and TLS Runtime Server transports.
- [ ] Adapt the review's TestReviewCapturedHTTPOverHTTPSProxy into a permanent regression through the actual handler. The same synthetic request must return 200 with either outer transport; observe TLS case returning421 first.
- [ ] Derive target scheme from an absolute request URL before considering transport TLS; fix both serveInner and requestScheme. Retain all authority/admission checks.
- [ ] Add omitted-port HTTP case: Host127.0.0.1 and Host127.0.0.1:80 must be equivalent; observe former421. Make authorityMatches derive omitted port from ProviderOrigin's scheme, keeping invalid authorities rejected.
- [ ] Add CONNECT+ClientHello coalesced-write regression (plus ordinary handshake). Observe buffered bytes lost. Preserve buffered.Reader after Hijack for intercepted and blind tunnel paths, while keeping buffered.Writer response flushing correct.
- [ ] Run go test ./internal/loopbackproxy -count=1 and focused race tests for the new cases. Commit the task and record red/green output.

### Task 2: Keep remote Login Session and MachineID consistent

**Files:** internal/workspaceidentity/identity.go and tests; internal/capturerun/types.go and public Create path regressions; internal/serverconnection/login_store.go and tests; internal/clientpath/path.go; internal/runlauncher/remote.go, remote_login.go, environments.go, remote_logout.go, client_ca.go and a shared login-store opening helper plus remote integration tests as needed.
**Interfaces:** LoginRemote, InspectRemote, Launcher.Environments and Launcher.Run; unchanged strict v1 local login format readable by both previous and fixed CLIs.
- [ ] Bring the review's real server identity recovery scenario into a permanent integration test. Start/login/true succeeds; chmod only machine-identity.json0600→0644; profiles/status must not claim the mismatched session healthy; launch must return ErrRemoteLoginRequired before creating an unusable Capture. Observe current503 mismatch first.
- [ ] Preserve the strict v1 saved record shape. Authenticate the current-session response and compare its MachineID directly with the current local identity on each bounded run/profile/status/CA preflight, for both newly created and existing records. Runtime Server is the binding authority. Reject mismatches with remote-login-required; do not persist a duplicate binding, weaken server matching or silently log in. Add RED/GREEN compatibility against the previous strict v1 record consumer so existing CLI installations can continue reading the file.
- [ ] Since preflight is read-only, concurrent logout/newer login must not cause any store write, resurrection or overwrite. An in-flight authenticated status read may return its observed snapshot; later resource requests independently reauthenticate. Keep old-session logout revocation possible even if local machine identity has changed.
- [ ] Cover unchanged identity, identity recovery, expired/revoked sessions, legacy records and explicit re-login restoring normal launch. Keep status/profile behavior consistent with run.
- [ ] Add the requested default-path compatibility from `~/.vibermate/login-sessions.json`. First RED: only that file exists and has the current strict login-document schema, default CLI login lookup cannot find it. Implement a single atomic migration only when the canonical store file is absent, preserving all valid records and writing private permissions. Existing canonical data wins even when the selected server is absent or expired. Never hide invalid canonical data via fallback; never read a real home fallback when an explicitly supplied unrelated test/custom StateDirectory is used. Test migration, canonical priority, malformed/incompatible legacy document, expiry, unchanged source file, logout followed by reopen without resurrection, and concurrent initialization. Apply the same shared opener to run/profile/status/CA/logout/login paths. This compatibility is newly requested; do not claim the git history proves that path was previously a release default.
- [ ] Add valid cwd names with121ASCII bytes, non-ASCII byte boundaries and surrounding whitespace; observe current invalid-label failure. Normalize/truncate only the display label, preserving the exact canonical-path HMAC and non-empty fallback.
- [ ] Real-launch RED additionally exposed capturerun.validateAbsolutePath rejecting an otherwise valid absolute cwd ending in spaces via generic text trimming. Allow space characters in filesystem path values without trimming actual paths, while retaining original byte-length, UTF-8/control/NUL, absolute and clean-path checks. Keep generic identity/text validation unchanged; verify through Capture Create and the real launch-label matrix.
- [ ] Run affected workspaceidentity/serverconnection/runlauncher remote and serverhost integration tests, including native-secrets build. Commit with red/green evidence.

### Task 3: Drain Client Relay connections correctly

**Files:** internal/runlauncher/remote.go, remote_relay_test.go or a focused relay regression file.
**Interfaces:** Client Relay socket and Close lifecycle; real local stalled TLS peer or deterministic external net.Conn peer.
- [ ] Add server-read-EOF while outbound server.Write blocks until close. Assert the peer is closed and forwarding lifecycle completes, not an internal map size. Observe current stuck write.
- [ ] Once authoritative server direction finishes, close/deadline the server before waiting for its reverse copier; preserve normal bidirectional streaming and client half-close behavior.
- [ ] Add stalled-TLS-connect shutdown case. Observe Close waits for the dial timeout. Give relay a cancellable owner context and cancel before joining connections/workers.
- [ ] Verify no deadlocks/races on shutdown or add-after-close, and retain existing streaming tests. Run relay focused race and runlauncher suite. Commit red/green evidence.

### Task 4: Preserve actionable remote failure classifications

**Files:** internal/runlauncher/launcher.go, control.go and typed error tests; cmd/vibermate/main.go and CLI tests; locales/en-US.json, locales/zh-CN.json and other existing catalogs if present.
**Interfaces:** Launcher.Run and CLI execute outcomes/messages.
- [ ] Reproduce actual net/http dial timeout with a still-live Capture context; it must be classified as connection failure, not Capture preparation timeout. Preserve actual operation-deadline classification and cancellation.
- [ ] Reproduce remote run with no saved login and remote401 with a saved token. Both must request remote login; remote connection failures must not point to a local Desktop App.
- [ ] Preserve known environment/client-target errors, and expose safe bounded HTTP status plus known reason for other server create rejections. Unknown/server-controlled strings and synthetic credential/body sentinels must not reach diagnostics.
- [ ] Correct generic timeout wording for remote operation; preserve child exit-status and cancellation contracts.
- [ ] Run cmd/vibermate, locales and affected runlauncher error/control tests. Commit red/green evidence.

### Task 5: Preserve Codex native target and authentication choices

**Files:** internal/runlauncher/invocation.go, environment.go, launcher.go/codex_isolation.go if required; internal/clienttarget/target.go; existing clienttarget/captureassignment/runlauncher integration tests; docs/capability-support.md if its supported behavior needs precision.
**Interfaces:** Native client's emitted request destination, Launcher.Run child argv/env, Profile.Resolve and Capture assignment admission.
- [ ] Add behavior tests showing stored API-key auth with no exported key must not be forced to ChatGPT, tokens must retain native ChatGPT selection, and explicit existing Base URL inputs remain supported.
- [ ] Remove forced model_provider/openai_base_url defaults derived from exported key presence. Preserve explicit supported baseURL settings and caller-provided provider configuration. Keep only proven transport-compatibility flags.
- [ ] For Codex without an established explicit target, Profile.Resolve must leave native destination unknown and retain the Environment's declared boundary; request admission still controls exact observed origins. Do not infer ChatGPT merely from absent key variables or create new authority for unconfigured custom providers.
- [ ] Preserve Codex's own authentication inputs. Remove placeholder-induced mode switching for native default routing; do not read/write/copy auth.json or synthesize credentials. Native tool permission and server-side managed account replacement remain intact. Do not promise login-free TUI operation.
- [ ] Keep verified legacy0.145 independent startup and modern no-daemon isolation working with the reduced recipe. Do not weaken rejection of remote/shared backends.
- [ ] Update only assertions whose former behavior was the defect. Add checks for retained auth/provider inputs, explicit target mismatch, no authority widening and no secret values crossing control.
- [ ] Use isolated native Codex with synthetic auth and a rejecting loopback proxy to verify effective destinations where available; record exact version. Run affected packages and native-secrets build. Commit red/green evidence.

### Task 6: Distinguish capability probe failures and fix baseline metadata regression

**Files:** internal/runlauncher/codex_isolation.go and integration tests; CLI/locales for a typed probe error; internal/servertransport/transport.go and transport tests; internal/runlauncher/git_snapshot.go or its test only after identifying cause.
**Interfaces:** Launcher.Run isolation result and Launch Context metadata.
- [ ] Add a compatible help fixture that responds after the former1.5s budget but within a bounded30s probe budget. Observe false unsupported before changing code.
- [ ] Return a distinct typed, sanitized probe-failure error for start failure, timeout/overflow/nonzero; a successful help without the capability remains unsupported. Preserve caller cancellation and process-group cleanup. Use deterministic controlled peers/parent contexts instead of long arbitrary sleeps where possible.
- [ ] Fix the baseline TestGitLaunchSnapshot fixture using the established cause: Apple /usr/bin/git initializes xcrun state for a fresh HOME and took4.888s in an isolated reproduction, while xcrun-resolved /Applications/Xcode.app/Contents/Developer/usr/bin/git took0.009s. The test deliberately changes HOME, so use the active developer Git executable for that macOS fixture (and its test-scoped PATH) rather than exercising Apple's cold shim inside a1s optional-metadata budget. Keep production's bounded optional-metadata semantics, actual Git assertions and secret-redaction coverage. On other platforms keep the available native Git. Do not increase production deadlines or delete assertions to hide the fixture issue.
- [ ] User steering: raise the Runtime Server connection-establishment cap from5s to30s in both ordinary transport and TLS trust probes. Respect a shorter explicitly supplied operation/parent deadline; preserve the existing short heartbeat/attach/finish budgets and90s Capture-creation budget. First RED through a real delayed loopback TLS peer: handshake completes after the old5s cap but before30s and must succeed via production Dial/probe interfaces. Retain short explicit deadline and cancellation coverage. No blanket timeout multiplier or retry changes.
- [ ] Run the runlauncher package and relevant CLI/locales tests, focused race for subprocess cleanup. Commit red/green evidence.

### Task 7: Make doctor verify and explain the proxy entry path

**Files:** cmd/vibermate/status.go/main.go and focused CLI tests; internal/runlauncher/doctor.go and local/real-server tests; existing loopbackclient pinned stream seam, serverconnection trust verifier, servertransport/relay and remote inspection interfaces only as needed; both locales; deployment/CLI documentation.
**Interfaces:** doctor report and exit status; production Client Relay -> selected Runtime Server transport -> CONNECT handler; InspectRemote and authorized Environment catalog.
- [ ] First RED: a synthetic Runtime accepts valid authenticated current-session/control requests but rejects CONNECT with405; current doctor incorrectly reports success. New doctor must report a failed proxy-entry check and exit nonzero.
- [ ] Add a30s-bounded proxy-entry probe through the same Client Relay and servertransport.Dial path used by captured traffic. Send one credential-free CONNECT to fixed authority vibermate-doctor.invalid:443; expect407 with the existing exact reasonCode proxy_authentication_required. The production handler rejects this before any upstream dial/admission. Never send the Runtime User bearer as proxy auth; never accept an unauthenticated200 as PASS. This checks the real entry path, not authorized forwarding or upstream model availability. Normal server connection audit records may be created by this probe; it must create no Capture, change no policy or trust decision, and send no model request.
- [ ] Doctor must not enroll first-use TLS trust, including a copied saved login without a saved trust decision. Reuse the existing PinStore/transport verifier in read-only trust mode for all doctor TLS connections (control, proxy and optional Environment catalog), enforced atomically during verification rather than a racy precheck. Existing saved trust remains authoritative; without a record, allow platform-root verification without persisting a decision, and reject unknown private issuers with trust-not-configured. Preserve ordinary login/status enrollment behavior. Test missing file/target/deleted record with an untrusted private chain: no trust enrollment or bearer transmission. Test trusted platform-root verification without a record succeeds without writing one; existing saved trust still works. Directory/lock preparation is permitted, trust-decision mutation is not.
- [ ] Test positive plaintext/TLS Runtime entry paths using real production handler/server fixtures; verify no Capture or upstream call. Add valid-control/broken-CONNECT, unrelated407/HTML/malformed/trailing/oversized response, stalled peer and cancellation cases. Bound response header/body reads as well as time; never print raw response content or credential/header values. Join relay/dial/read cleanup.
- [ ] Report known stages honestly: selected Runtime address and application-direct transport policy, control/session/machine validation, proxy-entry result and optional explicit Environment availability. Use30s diagnostic operation budgets, reusing existing secure transport and authenticated session checks; preserve existing status command behavior. Distinguish DNS/TCP/TLS failures where typed evidence exists, otherwise use an honest transport failure rather than inventing a completed check.
- [ ] Extend doctor only with optional --env <id> (no inferred Environment). Query the authorized active catalog to check the requested Environment; no Capture creation. If not selected, clearly mark that selection-specific check unperformed. Validate flags/duplicates and retain status grammar.
- [ ] Every failure includes a bounded known reason and useful next action (server/network/TLS, login, identity, CONNECT ingress, Environment permissions). Use localized output. Cancellation returns130, not a false health failure.
- [ ] Explicitly mark Capture creation, authorized proxy forwarding, native client login/startup, and upstream/model inference NOT CHECKED. A407 probe must be labeled proxy-entry reachability/auth rejection, never full proxy readiness. Apply honest scope wording to local doctor too; retain its actual runtime/storage/root-trust checks.
- [ ] Tests must show each displayed PASS corresponds to an executed successful check and any failed check gives nonzero exit. Print local and server build/API information already available without claiming untested catalog compatibility.
- [ ] Run CLI/locales, diagnostic/remote/serverhost integration tests and focused lifecycle race checks; commit with red/green evidence.

### Task 8: Integrate, verify and review the complete fix

**Files:** accumulated task changes and this plan's test evidence ledger.
**Interfaces:** full fixed CLI + Runtime Server behavior and repository checks.
- [ ] Review each task's code and evidence before marking it complete. Record any necessary scope decisions.
- [ ] Run affected Go packages together, targeted race tests for state and relay concurrency, go vet and native-secrets builds for CLI/daemon. Run repository structural checks and locale checks.
- [ ] Run broader go test ./... once after targeted checks pass, investigate failures introduced by this change, and report unrelated environmental limits precisely.
- [ ] Request independent whole-branch review, fix confirmed findings via TDD, reverify affected checks.
- [ ] Make the finished changes available in the user's working checkout without overwriting unrelated edits; retain reviewable commits/worktree evidence. No push/deploy.

