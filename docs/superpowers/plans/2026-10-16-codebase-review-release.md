# Codebase Review Corrections and Release Implementation Plan

> **For agentic workers:** Use superpowers:subagent-driven-development for scoped source tasks, test-first vertical slices and independent reviews. Root owns Git, remote release operations and final qualification. One implementation writer at a time.

**Goal:** Correct all confirmed codebase-review blockers, qualify the resulting source and publish the next verified release.

**Architecture:** Restore responsibility at existing interfaces: Capture authority owns revocation, proxy actions own sockets, request plans own actual destinations, Runtime owns cleanup ordering, and the plugin owns its supported catalog contract. Avoid broad redesign or new proxy/auth stacks.

**Tech Stack:** Go1.26.9, Flutter3.41.5/Dart3.11.3, Node22.23.1, existing protected GitHub/macOS/Linux/Homebrew release tooling.

**Spec:** docs/superpowers/specs/2026-10-16-codebase-release-design.md

## Global Constraints

- Each behavior correction starts with an observed failing public-interface regression, then minimal implementation and focused GREEN.
- No real credentials, provider/model calls, app/data resets, OS trust/proxy changes or production server deployment.
- Preserve exact account/origin/Environment authority, TLS/target-access checks, strict compatible schemas and data ownership. No migration or dependency upgrade is planned.
- One source writer; read-only reviews may run independently. Heavy suites are coordinated and not blindly repeated.
- No release with unresolved Important/Critical findings. No bypass of protected release gates or publication of unqualified bytes.

### Task 1: Restore Paseo catalog compatibility

**Files:** integrations/paseo/shared/contracts.ts; integrations/paseo/server/integration.test.ts and existing catalog tests.
**Interfaces:** profiles(connection), profileSchema and picker RPC outputs; current Go profiles JSON and supported v0.1.23 items.
- [ ] Reproduce current catalog rejection through profiles() with true and false permission flags.
- [ ] Add the explicit optional boolean to the strict item schema; preserve prior id/name-only catalogs, unknown-field/type/duplicate rejection and first-binding behavior.
- [ ] Run pinned Node22 typecheck and complete plugin tests; review/commit with RED/GREEN evidence.

### Task 2: Drain OAuth pending rotation during normal Runtime shutdown

**Files:** internal/productruntime/runtime.go and focused lifecycle integration tests; docs/adr/0021-one-schema-revision-without-migrations.md only for a clearly marked current-status clarification.
**Interfaces:** actual Runtime.Shutdown and codexoauth.Manager.Shutdown/SecretStore CAS lifecycle.
- [ ] Adapt the state review's synthetic rotation/failed-store recovery into a permanent lifecycle regression. Recovered storage must save revision2; still-locked storage must surface its error. No private cleanup-entry counting.
- [ ] Register credential-manager shutdown in successful startup cleanup at the correct dependency order, preserving rollback and preventing new/in-flight refresh work from escaping drainage. Use the existing dependency injection seam or a narrow justified test seam for the closed token peer; no real OAuth endpoints.
- [ ] Run affected runtime/account/OAuth checks and focused lifecycle race. Clarify obsolete ADR status without erasing history. Review/commit.

### Task 3: Preserve actual origin for auxiliary and opaque operations

**Files:** internal/environment/snapshot.go and request-plan tests; internal/originaltransport request/client interfaces and tests; internal/loopbackproxy/handler.go and proxy integration regressions; exact consumers required by typed target change.
**Interfaces:** immutable RequestPlan actual destination and originaltransport.NewRequest; canonical origin remains protocol identity only.
- [ ] Adapt /tmp/vibermate-core-review-20261016/alias_test.go to a permanent real CONNECT/catalog regression; observe original credentials addressed to canonical rather than actual origin.
- [ ] Expose the immutable original-operation destination for all applicable plan modes, including opaque requests alongside managed model Routes. Use a type that preserves actual scheme/host/port and avoids double base-path rewriting; do not derive target from mutable headers or current Endpoint state.
- [ ] Verify custom HTTPS/nondefault port and supported HTTP aliases, exact original path/query/auth origin, managed-route auxiliary behavior and rejection of unknown/out-of-scope operations. Preserve TLS and target-access enforcement.
- [ ] Run affected environment/originaltransport/proxy/runtime contracts and review/commit the typed-interface change.

### Task 4: Own cleartext forwarding cancellation and drainage

**Files:** internal/loopbackproxy/handler.go and focused real-socket tests; existing connection/action ownership helpers only as needed.
**Interfaces:** proxy action lifetime, request cancellation, raw upstream stream and Runtime Shutdown/Drain.
- [ ] Adapt /tmp/vibermate-core-review-20261016/cleartext_test.go; prove stalled response headers retain an action/egress lease after cancellation and bounded shutdown.
- [ ] Attach the socket to its action immediately, propagate request/Runtime cancellation through blocked write/read/body operations, and join entered cancellation callbacks before audit/lease release. Keep streaming through the existing branch.
- [ ] Cover blocked upload, stalled headers/body and shutdown, asserting joined handlers, upstream EOF, terminal audit and released leases; run focused race plus affected proxy tests. Review/commit.

### Task 5: Revoke retained proxy authority

**Files:** internal/captureassignment manager/activity interfaces; capturerun/manualcapture/runtimeuser authority and persistence activity implementations as needed; productruntime revocation composition; loopbackproxy connection ownership; focused real Runtime integration tests.
**Interfaces:** BeginRequest current authority, authoritative Capture/session state, revocation callbacks and active intercepted/blind connections.
- [ ] Adapt /tmp/vibermate-core-review-20261016/revocation_test.go: manual Revoke/managed Finish times semantic/managed Account Read on a genuinely reused connection; confirm all four RED rows.
- [ ] Revalidate current authority before every new operation, covering Capture lifetime/manual credential revision and originating User/Login Session state. Extend revocation ownership to ordinary proxy connections and blind tunnels without periodic per-connection database polling or parallel auth authority. One Runtime-owned shared deadline mechanism may revalidate through the same authority at effective expiry and react to post-commit changes. Lookup failure denies new operations.
- [ ] Add logout/disable/password reset, manual rotation, expiry, H2 and blind-tunnel regressions. Cover heartbeat renewal/shortening, registration versus invalidation and stale deadline callbacks. Prove no new upstream send/Account lease after revocation and joined teardown, preserving unaffected captures/new credential generations and legitimate in-flight policy semantics.
- [ ] Run relevant authority/runtime/server/proxy contracts and targeted race; independent architecture review before commit acceptance.

### Task 6: Freeze release identity and review report

**Files:** existing pubspec/product_version/distribution-policy/workflow version consumers and corresponding identity fixtures; docs/releases/v0.1.29.md; docs/reviews/2026-10-16-codebase-release.md; docs/module-map.md only for the new retained-authority ownership.
**Interfaces:** coherent candidate version0.1.29+31 and existing identity-binding test; release notes and source-bound audit coverage.
- [ ] Root rechecks latest main/tag/release/tap; settle identity before edits.
- [ ] Existing identity test RED with intermediate pubspec change, then complete exact consumers and GREEN. No unrelated numeric/schema changes.
- [ ] Record user-visible fixes, all audit findings and dispositions, qualification limits and actual source identity without claiming publication early. Run release-tooling/workflow contracts on pinned tools; review/commit.
- [ ] Update the existing module map to distinguish shared validity/change facts, live admission/retained lifetime ownership, and immutable Capture routing assignment. Preserve the existing terminology and unrelated module entries.

### Task 7: Qualify and integrate the frozen candidate

**Files:** no preauthorized additional product change; current workflow checks and evidence.
- [ ] One final cross-task review verifies closure of the five blockers and architectural contracts. Confirmed new issues receive scoped TDD correction and re-review.
- [ ] Push candidate branch/open normal PR and run existing CI on the exact source, including full Go/race/crash/response/native security, Flutter/Chrome/Paseo and packages. Reuse already valid evidence, preserve every first failure and real job handle.
- [ ] Verify actual startup upgrade/private restore and bundled helper/installed runtime qualification via existing tests and candidate artifacts. Use isolated owned data, no user-app reset.
- [ ] Merge only qualified source through normal workflow; verify resulting product tree matches reviewed candidate and record full merged SHA.

### Task 8: Publish and independently verify

- [ ] Dispatch protected macOS candidate workflow from main at the exact merged SHA. Follow original jobs through unsigned/sign/notary/installed-evidence/public gates; do not impersonate designated reviewer approval.
- [ ] Publish only final qualified immutable macOS assets, then same-tag Linux assets via the existing workflow. Update the isolated Homebrew tap using final public DMG hash; preserve any user's tap changes.
- [ ] Independently download/check published assets, version/hash/source identity and Brew metadata/CI. Synchronize the original source checkout safely and preserve complete review/validation evidence. Report actual release URL and any limits.
