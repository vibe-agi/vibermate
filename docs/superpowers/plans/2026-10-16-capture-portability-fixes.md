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

**Files:** internal/workspaceidentity/identity.go and tests; internal/serverconnection/login_store.go and tests; internal/runlauncher/remote.go, remote_login.go, environments.go and remote integration tests as needed.
**Interfaces:** LoginRemote, InspectRemote, Launcher.Environments and Launcher.Run; backwards-readable local login store.
- [ ] Bring the review's real server identity recovery scenario into a permanent integration test. Start/login/true succeeds; chmod only machine-identity.json0600→0644; profiles/status must not claim the mismatched session healthy; launch must return ErrRemoteLoginRequired before creating an unusable Capture. Observe current503 mismatch first.
- [ ] Bind newly saved remote logins to their MachineID and detect mismatch on use. Preserve legacy login records: establish their binding through the authenticated current-session response before upgrading the saved record, rather than assuming it matches the current machine. Reject mismatches with remote-login-required; do not weaken server matching or silently log in.
- [ ] Cover unchanged identity, identity recovery, expired/revoked sessions, legacy records and explicit re-login restoring normal launch. Keep status/profile behavior consistent with run.
- [ ] Add valid cwd names with121ASCII bytes, non-ASCII byte boundaries and surrounding whitespace; observe current invalid-label failure. Normalize/truncate only the display label, preserving the exact canonical-path HMAC and non-empty fallback.
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

**Files:** internal/runlauncher/codex_isolation.go and integration tests; CLI/locales for a typed probe error; internal/runlauncher/git_snapshot.go or its test only after identifying cause.
**Interfaces:** Launcher.Run isolation result and Launch Context metadata.
- [ ] Add a compatible help fixture that responds after the former1.5s budget but within a bounded10s probe budget. Observe false unsupported before changing code.
- [ ] Return a distinct typed, sanitized probe-failure error for start failure, timeout/overflow/nonzero; a successful help without the capability remains unsupported. Preserve caller cancellation and process-group cleanup. Use deterministic controlled peers/parent contexts instead of long arbitrary sleeps where possible.
- [ ] Diagnose the baseline TestGitLaunchSnapshot failure. Reproduce the actual cause before fixing; retain known-good metadata and secret-redaction assertions and optional-metadata failure semantics. Do not blindly increase deadlines or remove assertions.
- [ ] Run the runlauncher package and relevant CLI/locales tests, focused race for subprocess cleanup. Commit red/green evidence.

### Task 7: Integrate, verify and review the complete fix

**Files:** accumulated task changes and this plan's test evidence ledger.
**Interfaces:** full fixed CLI + Runtime Server behavior and repository checks.
- [ ] Review each task's code and evidence before marking it complete. Record any necessary scope decisions.
- [ ] Run affected Go packages together, targeted race tests for state and relay concurrency, go vet and native-secrets builds for CLI/daemon. Run repository structural checks and locale checks.
- [ ] Run broader go test ./... once after targeted checks pass, investigate failures introduced by this change, and report unrelated environmental limits precisely.
- [ ] Request independent whole-branch review, fix confirmed findings via TDD, reverify affected checks.
- [ ] Make the finished changes available in the user's working checkout without overwriting unrelated edits; retain reviewable commits/worktree evidence. No push/deploy.

