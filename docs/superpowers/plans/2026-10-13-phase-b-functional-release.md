# Phase B Functional Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development for scoped source work/review, and superpowers:verification-before-completion for every candidate or delivery claim. Root owns Git, remote operations and final integration.

**Goal:** Deliver the already implemented Phase B networking and account-experience features as an installable, verified GitHub/Homebrew release, then continue the full Goal.

**Architecture:** Close the existing rules task, update one coherent candidate identity, and use the current protected release pipeline. Hosted CI validates the frozen source in clean checkouts while independent review and bounded isolated acceptance cover the actual candidate. No new feature or generic hardening project is added.

**Tech Stack:** Go1.26.9, Flutter3.41.5, Node22.23.1, locked Xray producer/materials, existing GitHub validation/signing/notary/Linux and Homebrew workflows.

**Spec:** `docs/superpowers/plans/2026-10-09-production-continuation.md` Phase B and `2026-10-05-global-default-egress.md` Task6. Existing detailed release mechanics are inventoried in `.superpowers/sdd/2026-10-09-production-continuation/b5-release-preflight-b4249020.md`; use that complete six-group checklist rather than inventing another release system. Prior stable release mechanics: `2026-10-10-login-fixes-release.md` (historical version/scope does not override this plan).

## Global Constraints

- B1–B4 and rules closure99a589a1 are reviewed/committed; do not redo them. Execute this release plan from the actual recorded metadata-task BASE, with one source writer.
- Proposed identity is0.1.26+28, not yet frozen/reserved. Actual2026-10-13 GitHub matching-refs query forv0.1.26 returned[] and remote main pubspec0.1.25+27. Before Task1, root rechecks latest release/main/proposed tag/tap; if occupied or nonmonotonic, root updates the concrete identity before dispatch rather than overwriting a release.
- Phase B scope: VLESS/REALITY+Vision, own SOCKS TCP/UDP, domain System-DNS/Direct exclusions, configuration-local upstream CA, trusted helper distribution, global/account/flow egress, five-minute shared quota/credit and nonpolluting terminal diagnostics. Preserve delivered login/long-conversation fixes and all user data.
- Full active MCP continuation, full46 clients, platform CA/devcontainer, full R1–R7/reader/data fault/60-minute/10-day qualification remain Phase C. Actual reachable permissions/egress/shared startup schema and installed package integrity remain this candidate's responsibilities.
- One source/local-heavy-test owner; read-only review and hosted CI may proceed concurrently without mutating that source. Preserve all first failures and exact source/process/artifact identities. Do not repeat unchanged completed campaigns on every continuation.
- Do not reset/restart the user's App/Codex/9666/accounts/trust/proxy or dirty worktrees/tap. Use isolated owned data and clean candidate checkouts. Never delete SDD evidence to make a check pass.
- No bypass of protected signing/notary/public-release approval, no self-approval as the user's designated reviewer, no publication of unqualified bytes. Read-only remote availability is not credential/runner/candidate qualification.

---

### Task 1: Coherent candidate identity and honest release notes

**Files:** `ui/flutter_app/pubspec.yaml`; `ui/flutter_app/lib/core/update/product_version.dart`; `tool/macos-release/macos-distribution-policy.mjs`; the three DMG literals in `.github/workflows/macos-developer-id-candidate.yml`; exact fixtures in `tool/macos-release/{macos-distribution-policy,prepare-r0-release-evidence,macos-installed-candidate-evidence}.test.mjs`; create `docs/releases/v0.1.26.md`. Additional version consumers require an actual scoped-search finding/root ruling; no blanket replacement or source feature change.

**Interfaces:** Existing `macOSDistributionPolicy`, Flutter version/build, actual workflow filenames and current release-evidence fixtures must name one candidate. Existing evidence schemas, toolchain/client versions, maximumSchema values, prior release notes and cryptographic checks do not change.

- [ ] Root confirms exact available identity and execution BASE after rules closure; worker records hashes of owned paths before editing.

- [ ] Change pubspec alone from0.1.25+27 to0.1.26+28. Preserve a genuine existing binding-test RED:

```sh
/Users/null/.local/share/fnm/node-versions/v22.23.1/installation/bin/node --test --test-name-pattern='frozen release identity is coherent' tool/macos-release/macos-distribution-policy.test.mjs
```

The existing test reads actual pubspec/workflow and should reject the intermediate mismatch. A setup/Node failure is not this RED. Do not add a source-text-only change detector.

- [ ] Complete exact identity updates and only their corresponding fixture values:

```dart
const productVersion = '0.1.26';
const productBuildNumber = 28;
```

```javascript
// Existing policy members; preserve every other member.
appBuildNumber: "28",
appVersion: "0.1.26",
diskImageFilename: "ViberMate_0.1.26_universal.dmg",
```

Update all three existing workflow DMG paths, fixture pubspec string, expected package version, archive names, bundleVersion27→28 and coherent identity assertions/regexes. Keep schema numbers and other unrelated numeric or historical values unchanged. Preserve the identity test's three-filename count and stale-version exclusion; do not relax validation to accept two versions.

- [ ] Write concise English/Chinese candidate notes with these user-visible facts:
  - Configure upstream VLESS, including REALITY+Vision; use the actual authenticated SOCKS5 listener for TCP and supported UDP.
  - Configure one direct domain per line, including localhost and wildcard subdomains; matched domains use System DNS and Direct on the Runtime machine. Domain list is not IP/CIDR and grants no target access.
  - Configure a global default; explicit account/flow exits override it, including explicit Direct. New operations inherit changes; in-flight operations keep their route and failed proxies do not fall back.
  - Shared quota/credit refresh defaults to five minutes, with immediate manual refresh and last-successful data retained on failure.
  - Short Runtime reconnect diagnostics no longer corrupt a child's interactive input. Existing account reauthorization, Web-refresh/default-port and long-session fixes remain.
  - Upstream CA trust is configuration-local; it is not the interception CA or a REALITY identity bypass. Remote raw SOCKS needs a trusted/reachable network and UDP-capable client; HTTP run relay is not a UDP tunnel.
  - Full MCP continuation/full-client matrix are not claimed complete; Windows native UDP and untested mobile/platform abilities remain explicitly unqualified. Candidate notes do not claim signed/public delivery before it exists.
  - Startup data upgrade uses the existing verified private backup/migration path; preserve accounts/config/history, no reset instructions. Do not equate public backup credential exclusions with private migration preservation.

- [ ] Self-review owned diff. Run the existing complete release-tooling and workflow-contract targets with pinned Go/Node on PATH:

```sh
env PATH=/tmp/vibermate-go-security.JcRyfL/go/bin:/Users/null/.local/share/fnm/node-versions/v22.23.1/installation/bin:/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin DEVELOPER_DIR=/Library/Developer/CommandLineTools GOTOOLCHAIN=local GOFLAGS=-mod=readonly make check-release-tooling check-workflows
git diff --check
```

Keep unique verbatim logs, per-run source hashes and real handles/exits. No full Go/Flutter/build/signing campaign in this metadata unit. Root independently reviews complete owned diff, commits and records full frozen SHA. Metadata does not prove feature or release readiness.

### Task 2: Qualify the frozen source and actual assembled candidate

**Files:** No preauthorized product change. Root uses existing workflows, test commands, release/data tools and evidence artifacts. Concrete failures enter one scoped correction with actual cause/affected consumers and preserved first output.

**Interfaces:** Input is exact metadata/rules/feature SHA and its tree; outputs are terminal CI checks, full review verdict, current shared-data/installed-runtime evidence and same-source unsigned/signed provenance. Source changes invalidate only applicable prior results but final delivered artifact must bind to the accepted final source.

- [ ] Open/update the normal scoped candidate PR and run existing validation workflow on the frozen source. Record branch, full head SHA, merge-tree SHA and actual run/job IDs. Follow actual handles to terminal; no duplicate jobs merely because a poll expires.
- [ ] Use current CI's existing clean-checkout jobs for generated/format/modules/scanner, Go/vet/contracts, race and dedicated crash/response matrices, native-secrets/cross-build/security, Flutter/native preferences/packaged App, Chrome/Web/Linux package and Paseo regressions. Preserve exact matched/skipped tests and every failure; a green job name alone is not coverage. Do not duplicate the same source-bound full suite locally just for a second pass.
- [ ] Main has read current Makefile and ci.yml. `make check-format` walks all files and local production worktree retains historical SDD snapshots; candidate-wide format/build/generated checks therefore use a clean source checkout/hosted workspace rather than deleting evidence or rewriting user caches. Keep logs outside the candidate source tree. A local ad-hoc App is not a qualified signed artifact.
- [ ] Dispatch one most-capable independent whole-candidate review covering the full branch AND the global-default six-unit requirements. Supply a complete source diff plus frozen source/test evidence, phase boundary, all deferred/parked rulings and current family matrix. One explicitly combined scope fulfills both reviews; do not duplicate two broad reviews over the same code. Resolve concrete correctness/security/data/install findings, not optional polish or Phase C additions.
- [ ] Follow the existing preflight group3 to verify actual shipped startup revision1→2 migration/private snapshot/reopen/restore using owned data and exact current source/package. Existing schema tests must compare retained accounts/config/history/secret references/approvals/once/audit, not only schema number. Public backup is separately verified and excludes secrets by design; it cannot prove private upgrade preservation.
- [ ] Follow preflight groups4–5 for locked helper/source/materials and same-source macOS/Linux packages. Bind actual candidate helper bytes to the already approved real user-entry acceptance `TestServerManualSOCKSREALITYDelivery` in `internal/serverhost/socks_vless_delivery_test.go` using explicit `VIBERMATE_TEST_XRAY_BINARY=<actual-candidate-helper>`, pinned Go, `-count=1 -v`; zero target skips. This reuses the existing test, not another feature/matrix. Preserve its exact runtime/packaged-helper qualification boundary and no real nodes/accounts.
- [ ] Record source/unsigned-assembly qualification before integration. Protected signing/notary/final installed evidence is produced in Task3 after integration because that workflow requires a merged candidate; it is not a circular premerge requirement. Candidate revision and trusted workflow tooling revision are separate coordinates. Never treat signature-only/archive-only evidence as installed behavior.

### Task 3: Integrate, publish and independently verify delivery

**Files:** Only qualified source integration, actual release assets and isolated tap Cask after candidate acceptance. No schema/data reset or user installation/restart.

- [ ] Reconfirm all applicable candidate checks/review and current main/tag/protection state, then integrate through the normal protected workflow. Record merged full SHA and source/tree binding; if merge changes product bytes, requalify the affected candidate.
- [ ] Dispatch the existing protected macOS candidate workflow from main for that exact merged SHA. When a concrete candidate reaches required signer/notary/public-release reviewer approval, present that candidate to the user; do not approve as them or remove protection.
- [ ] Verify actual R0 transfer, nested code/material hashes, signing transformation, Apple accepted notary/staple/Gatekeeper and fresh installed-candidate evidence using the existing detailed preflight commands. Bind the retained migration/restore and Runtime/helper evidence to this final source/package; final signed installation must pass before publication. Preserve signed versus unsigned hashes distinctly.
- [ ] Recheck tag/version vacancy, publish only the verified immutable GitHub asset set at the qualified source, then use the existing Linux-release workflow for the same tag. Update an isolated Homebrew tap after public DMG exists, using its final post-staple hash. Keep the user's original dirty tap untouched.
- [ ] Independently download public assets and compare names/versions/hashes to producer evidence; confirm actual Brew cask version/URL/hash and install CI. Record release URL/date/source/asset identities and tap merged commit. Only these facts close Phase B delivery.
- [ ] Report delivered features and remaining Phase C boundaries honestly, leave the full Goal active, and continue approved remaining scope. Do not mark the final ten-item Goal complete at this phase boundary.

## Plan self-review

This executes the already approved finite B5 list without reopening B1–B4 or adding performance/full46/soak prerequisites. Metadata uses an existing behavioral identity-binding test and exact consumers read from current source. Hosted clean checkouts avoid local historical snapshot pollution while preserving evidence. Current reachable startup data and actual packaged helper remain explicitly bound to this candidate. Protected reviewers remain authoritative. Planning has not changed product identity, run candidate CI, built/uploaded artifacts or published a release.
