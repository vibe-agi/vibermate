# VLESS / UDP Functional Implementation Plan

> **For agentic workers:** Use superpowers:subagent-driven-development for bounded implementation and review. Follow the user's latest functional-first ordering; reviews and broad test preparation must not block unrelated feature development.

**Goal:** Make ViberMate's own SOCKS5 TCP/UDP listener work through REALITY + Vision, with usable VLESS configuration and direct-domain routing.

**Architecture:** A managed official Xray process sits behind one internal Module; ViberMate retains ingress authentication, target routing, profile selection and lifecycle. Keep the current main Go toolchain. Build one working vertical path before extending the quality matrix.

**Tech Stack:** Existing Go1.26.8/Flutter pins; pinned official Xray process, initially v26.3.27 subject to actual binary/interop evidence.

**Spec:** `docs/superpowers/specs/2026-10-09-vless-udp-functional-design.md`.

## Global Constraints

- User's 2026-10-09 instruction overrides the earlier scheduling rule that all GlobalTask6 cases must finish before another feature starts. It does not authorize fake support claims or losing data/secrets.
- One product source writer; existing review may finish read-only in parallel. Preserve all existing WIP and the released long-session fix for later integration.
- Use private test listeners/data/keys and owned subprocesses; no current App/9666/account/trust/system proxy changes. Capture credentials and VLESS secrets remain private.
- REALITY + Vision and ViberMate-owned UDP ASSOCIATE are required. Core-only UDP or ordinary TLS is not a substitute.
- No unconfigured direct fallback. Existing account/flow/global selection still chooses the frozen profile.
- This is a short delivery-slice plan per user direction. Write focused behavior tests while implementing actual interfaces; do not make a comprehensive hypothetical matrix a prerequisite for source work.

## Task 1: Real engine path and owned process adapter

**Files:** current plan's private `core-working-path-report.md` and harness; then new `internal/vlesscore/{client,config,socks,datagram}.go` and focused tests.

**Interface:** Adapter owns an immutable configuration and process. TCP returns a normal `net.Conn`; datagrams retain packet boundaries and the fixed destination; Close stops and joins owned resources. Keep core flags/JSON behind this Module.

- [ ] Verify an official pinned binary, then run local REALITY server/client with synthetic keys and owned TLS/TCP/UDP targets.
- [ ] Send and receive actual TCP bytes and UDP datagrams. Record commands, versions, hashes and terminal process cleanup; core-only proof is labeled as such.
- [ ] Put that already-working path behind the production adapter with one success and one wrong-configuration/Close behavior test; no main toolchain upgrade or custom crypto.

## Task 2: ViberMate SOCKS5 ingress and actual UDP round trip

**Files:** new `internal/socksproxy/{server,auth,association,packet}.go` and focused tests; `internal/productruntime` composition; relevant `desktophost`/`serverhost` listener options.

**Interface:** Consumes the adapter and existing Capture/network authority. Own TCP control plus UDP association; no unauthenticated public relay or model/tool classification of UDP.

- [ ] Implement username/password negotiation with existing capture credentials, CONNECT and UDP ASSOCIATE.
- [ ] Bind packets to the authenticated control peer/association; preserve destination and reply framing; close association on control EOF and Runtime shutdown.
- [ ] Run a real SOCKS5 client through ViberMate to the Task1 REALITY + Vision server and UDP echo. Keep this as the feature's vertical test, not only codec/helper tests.

## Task 3: Selected profile, secret storage and domain routing

**Files:** `internal/egressnetwork/{policy,dialer}.go` plus focused domain/datagram routing files; `internal/egressprofile` manager/repository consumers; existing secret store and Runtime composition.

**Interface:** Existing immutable resolved profile, plus private credential reference and comparable normalized domain rules. Target resolution is shared between TCP and datagrams.

- [ ] Import supported VLESS fields into private engine configuration; publish only redacted profile metadata and secret reference.
- [ ] Select VLESS from existing account/flow/global rules for actual HTTP/CONNECT traffic and the new SOCKS listener.
- [ ] Match `github.com` and `*.github.com` before DNS. Matching traffic uses Runtime system DNS + Direct; other traffic uses the selected resolver + exit. Prove both with real observers/targets.

## Task 4: User configuration and developer-usable version

Task4A owner configuration UI now executes under `2026-10-10-vless-configuration-ui.md` after Task3B's reviewed shutdown correction. It implements the existing REALITY import/edit/Direct-domain flow; ordinaryTLS/customCA, managedkernel andHost/bootstrap/launcher delivery plus the real assembled proof below remain required Task4 work, not implicitly completed by the UI slice.

Task4A is now reviewed/approved within its stated limits. Task4B begins with `2026-10-10-vless-tls-core.md`: real ordinaryTLS/configuration-localCA transport proof in the existing core Module, followed by private profile/control/editor integration. Primary `tls-ca-integration-facts.md` constrains trust modes and rejects an unsupported skip toggle; no systemtrust changes or engine-date bypass.

Execution recovery2026-10-10: Task4A, TLS/core, TLS private profile/control, TLS editor, native patched input and reusable kernel producer (including fix1) are independently Approved. Their scoped Minors and evidence limits stay in their individual ledgers, not new fix loops. `2026-10-10-vless-kernel-packaging.md` Task1 has real Universal/Linux assemblies and30GREENtests; review found onefailure-diagnostic gap, fix1queued. Sole source/testowner is `vless_host_bootstrap` under `2026-10-10-vless-host-bootstrap.md`, implementing independent trustedhelperdiscovery/Desktoploopbackstartup; afteritsfreeze, resumepackagingfix thenTask2realpackageinventories. No completed editor/core/producer work is redispatched. ActualSOCKSaddress/ManualCapture userconnectiondelivery andassembledproofremain required. ExistingremoteHTTPrelay isnot aUDPtunnel. Do not retunecompletedlifecycletimeouts. Stable0.1.25 source4e89 isdelivered onGitHub/Homebrew; laterintegratethosefixes withoutdisturbingWIP.

**Files:** owner-control egress routes; Flutter control DTOs, egress editor/settings and bilingual copy; deployment/capability docs.

- [ ] Add VLESS import/edit, redacted node summary, REALITY/Vision fields, applicable TLS/custom-CA settings and direct-domain list.
- [ ] Wire the real Runtime mutation/readback; show actionable errors and actual listening address. Do not change users' active configuration automatically.
- [ ] Build and run the assembled feature with isolated settings. Demonstrate TCP, ViberMate-owned UDP and the domain override from the actual configuration path.

## Task 5: Consolidated hardening and scoped release

Only after the vertical functionality works, consolidate credential/redaction, malformed packets, association isolation, revocation, cancellation, platform reachability, resource/performance and installation/source checks. Resolve actual observed bugs first. Preserve unexplored boundaries rather than inventing passing evidence. Reconcile other production-goal work and the released hotfix at an owned checkpoint; select a fresh release version rather than overwrite v0.1.24.

## Self-review

Task1 supplies the real adapter forTask2; Task3 connects routing/configuration andTask4 makes it usable. Task2 is indispensable because upstream Xray SOCKS is not ViberMate ingress. Task5 is explicitly after functional integration. Full production-goal scope remains, but its previous exhaustive scheduling no longer blocks this feature's development.
