# Novice Agent Task Walkthrough

Date: 2026-09-01
Baseline: `7e8efc438de3b1417cab5302676e2b36187c0007`
Protocol: `docs/plans/2026-08-31-novice-agent-usability-review.md`
Perspective: developer who uses Codex or Claude Code but has no proxy or
ViberMate domain knowledge

This is an expert cognitive walkthrough with reproduced product evidence. It
is not a human participant session, so completion rates, task times, hints,
SEQ, and SUS are intentionally absent.

## T1 — Establish readiness

### Decision inventory

| User question | Product evidence | Result |
| --- | --- | --- |
| Is the live Runtime healthy? | The App title bar renders the Runtime state and a ready state; the real temporary Runtime reports ready/healthy. | Pass. |
| What if no local Runtime exists? | `vibermate status` and `doctor` both say to open the App, wait for readiness, then run `doctor`. | Pass. |
| Can a Chinese terminal user get the same next action? | With POSIX locale precedence respected (`LC_ALL=zh_CN.UTF-8`), help/status/doctor render equivalent Chinese guidance. | Pass. |
| What if App bootstrap itself fails? | The bootstrap surface prints `_failure.toString()` and offers only Retry. A missing packaged sidecar therefore exposes its absolute path but does not say that Retry cannot restore it or whether to rebuild/reinstall. | Fail for recovery guidance. |

### Reproduction

- Fresh temporary `HOME`, English: help/status/doctor produced the first-run
  commands and one next action.
- Fresh temporary `HOME`, Simplified Chinese: the same three commands produced
  equivalent Chinese meaning.
- Real packaged daemon, temporary cache/data: ready/healthy state passed in
  `live_runtime_test.dart`.
- Prior product-owner screenshot reproduced the missing-sidecar presentation:
  raw packaged path plus Retry, with no corrective action. The current packaged
  artifact contains the sidecar, so this is a failure-path finding rather than
  a claim that every first launch fails.

### Finding U-006 — Bootstrap failure does not distinguish retryable from repair-required

- Primary evidence: **reproduced source behavior**; supporting uncontrolled
  observation from the product owner.
- Severity: **P1**, medium confidence, frequency unknown.
- Why: Runtime bootstrap is a core-task boundary. For a missing packaged
  executable, Retry deterministically repeats the same failure, while the only
  corrective actions are rebuild/reinstall. The UI presents neither fact.
- Retest: seed a missing-sidecar failure in Chinese and English; verify one
  repair-oriented explanation and keep Retry only for plausibly transient
  failures.

Remediation evidence:

- Red: `missing packaged sidecar gives repair guidance instead of retry`
  failed because the repair message was absent.
- Green: the desktop platform boundary maps the missing executable to the
  stable `desktop_sidecar_unavailable` reason; the App renders equivalent
  Chinese and English repair guidance, hides the absolute path, and omits the
  ineffective Retry action.
- Mapping check: `missing desktop sidecar maps to a stable repair-required
  error` passes against an explicit nonexistent daemon path.

## T2 — Capture the first Agent request

### Decision inventory before any Capture exists

| Visible control or message | Novice interpretation |
| --- | --- |
| “No captures yet.” / “还没有 Capture。” | Confirms absence but provides no cause or next action. |
| Capture search | Cannot create evidence. |
| Add-link icon | Suggests “add/capture something” but opens the advanced Manual Capture path, not the normal Codex/Claude launch path. |
| Global Settings icon | Contains Terminal command management, but the empty state does not connect the task to that destination. |
| `vibermate help` in a terminal | Gives the exact correct commands, but the App does not expose or copy them from the point of need. |

The real isolated live test proves that, once invoked, the packaged command and
temporary Runtime create a managed Capture. The failure is therefore discovery
of the first action, not the capture mechanism.

### Finding U-001 — Empty Captures hides the product's primary first action

- Primary evidence: **reproduced UI composition and live-runtime behavior**.
- Severity: **P1**, medium confidence, human frequency unknown.
- Why: the default surface offers an adjacent advanced action but not the
  zero-configuration command that completes the core task. The necessary
  action exists already in CLI help and Settings; no new onboarding system is
  required.
- Smallest expected remedy: put the existing Codex/Claude launch commands (or
  a direct link to their existing Settings panel) in the empty state. Do not
  add a wizard, tour, checklist, or duplicate command authority.
- Retest: at 1180×760 and 390×760, English and Chinese, verify that the empty
  state identifies the normal command, distinguishes Manual Capture, and lets
  keyboard/screen-reader users reach the next action.

Remediation evidence:

- Red: `empty Captures leads a novice to the normal Agent launch path` failed
  because the normal launch explanation and action were absent.
- Green: the same test passes at 1180×760 in English and 390×760 in Chinese.
  The action navigates to the existing Terminal command authority; no new
  onboarding state or duplicate install behavior was added.

## Current checkpoint

- T1 readiness: passes in healthy and CLI-unavailable states; the reproduced
  repair-required bootstrap failure is now remediated and retested.
- T2 execution after command: passes in the isolated real Runtime.
- T2 first-action discovery: failed on the baseline and now passes the English
  wide and Chinese narrow regression checks.
- No request was sent to a real AI provider and no operator data was read.

## T3 — Connect a supplied upstream service

### Reproduced path

```text
Configuration / Upstream services
  -> Add Endpoint
  -> display name + canonical upstream URL
  -> select Anthropic Messages and OpenAI Responses
  -> Create Endpoint
  -> Add Account under the selected Endpoint
  -> select exact Bearer transport and save the fixture token once
Configuration / Traffic policies
  -> New Environment
  -> add Anthropic client flow to that Endpoint and its owned Account
  -> add OpenAI Responses client flow to the same Endpoint and Account
  -> review impact -> publish
```

Reproduced checks:

- `Endpoint-owned Account can be created, rotated, and safely deleted` passed.
- `one multi-protocol Endpoint can join independent client flows` passed.
- `internal/modelcatalog`, `internal/providertransport`, and
  `internal/desktopcontrol` passed against loopback fixtures, including the
  exact `/v1/models` path and credential transport boundary.
- The Environment account dropdown contains only ready Accounts owned by its
  selected Endpoint.

Result: the happy path is operable and its ownership rules are visible. No P0
or P1 was reproduced.

### Finding U-002 — Area labels and entity names still require translation

- Evidence: **reproduced terminology**, task impact **inferred**.
- Provisional severity: **P2**, medium confidence, awaiting human teach-back.
- Transition: “Upstream services” → Endpoint → Account, then “Traffic
  policies” → Environment → Client Flow → Route. Each term is internally
  consistent, but a novice must learn the mapping during one task.
- Do not fix yet: renaming individual labels without participant evidence could
  make the runtime/API vocabulary less consistent rather than more usable.

### Finding U-003 — Account recovery interrupts an Environment draft

- Evidence: **reproduced UI composition**, frequency unknown.
- Provisional severity: **P2**, medium confidence.
- If the user starts in Traffic policies and selects an Endpoint with no ready
  Account, the editor explains the dependency but cannot create the Account.
  The user must leave the local draft, create the Account under Upstream
  services, and return.
- Counter-evidence: starting from the supplied service URL naturally maps to
  the Upstream services area, and that happy path creates the Account before
  the Environment. A nested Account editor or cross-page draft system is not
  justified without observed failures.

## T4 — Exact A to B model mapping

The model dialog exposes two independent authorities:

- A: client-requested model catalog from `models.dev` for the exact client
  protocol;
- B: live catalog from the selected Endpoint's normalized `/v1/models`, using
  the selected Endpoint-owned Account transport.

`Environment edit reviews impact and publishes only Endpoint-owned Account
authority` passed the following sequence: select A, select B, add an opaque
manual B value, save mappings, review impact, publish, reopen, and recover the
exact values. `Endpoint discovery failure explains authentication and keeps
manual mappings usable` also passed: authentication failure identifies the
selected transport and retains manual entry.

Result: exact mapping and unmatched passthrough are explicit and operable. No
P0 or P1 was reproduced. The non-fatal Flutter hit-test warnings on two dropdown
test finders remain test-harness debt and are not counted as participant
failure evidence.

## T5 — Diagnose seeded failures

### Boundary matrix

| Failure | Stable evidence | User-visible recovery | Result |
| --- | --- | --- | --- |
| Endpoint authentication rejected | The model-catalog adapter converts upstream `401/403` into `model_catalog_authentication_rejected`; no provider body or credential crosses the control boundary. | The model dialog names the Endpoint Account boundary, identifies its exact Header transport, and preserves manual model entry. | Pass. |
| Account Selector preview rejected (`422`) | The control problem retains the credential-free compile/runtime detail from the local sandbox. | The editor now says to repair the JavaScript or sample values and displays the safe diagnosis; it no longer leads with `Control problem 422`. | Pass after P1 fix. |
| Endpoint model discovery unavailable (`503`) | The control API keeps the stable `model_catalog_unavailable` reason and status. | The model dialog names Endpoint discovery as unavailable and keeps exact manual model entry usable. | Pass. |
| Malformed Agent exchange | Production records `unsupported_client_input`, a structural client field/path, and zero provider attempts. | The failed Turn now says that ViberMate rejected the request before contacting upstream and points to the request field/client protocol. | Pass after P1 fix. |
| Runtime unavailable | CLI status/doctor and App bootstrap are covered by T1. Missing packaged binaries require repair; transient connection failures remain retryable. | The user gets the correct open/rebuild/reinstall/retry action for the observed boundary. | Pass after U-006. |

Copying retained raw evidence continues to redact the `Authorization` value;
`a Capture Conversation preserves boundaries and expands real Exchange
evidence` verifies that neither the UI nor copied payload contains the Bearer
secret.

### Finding U-007 — Failed Turn exposed a code but not a causal boundary

- Primary evidence: **reproduced UI behavior and production reason taxonomy**.
- Severity: **P1**, medium confidence, human frequency unknown.
- Baseline behavior: `Provider timeout · 504 · upstream` exposed technical
  fragments but no safe correction. The Preview fixture also used
  `provider_timeout`, which is not a production `exchange.ReasonCode`.
- Red: `failed Turn explains the failing boundary and next action` and
  `malformed Agent exchange reports zero upstream attempts` failed because the
  boundary/action copy and the local zero-attempt example were absent.
- Green: failed Turn details now retain the production reason, status, and
  structural location while adding one plain-language boundary and corrective
  action. The Preview fixture uses `provider_response_idle` and
  `unsupported_client_input`, with provider attempts present only for the
  upstream case.

### Finding U-008 — Account Selector preview surfaced the HTTP envelope

- Primary evidence: **reproduced widget behavior**, supported by the owner's
  earlier screenshot of `Control problem 422: account_selector_test_failed`.
- Severity: **P1**, high confidence for the defect, human frequency unknown.
- Why: the response already contains a bounded, credential-free JavaScript
  diagnosis, but the editor made the user translate an internal reason and HTTP
  status before learning what to change.
- Red: `Account Selector sample failure preserves safe diagnosis` failed on the
  raw exception string.
- Green: the same public editor seam gives one corrective action and retains
  the safe compile/runtime detail. Unexpected Runtime failures receive a
  separate Runtime-status recovery message rather than arbitrary exception
  text.

Reproduced validation:

- `go test ./internal/exchange ./internal/loopbackproxy ./internal/desktopcontrol`
- `Endpoint discovery failure explains authentication and keeps manual mappings usable`
- `Control problem retains an actionable safe detail`
- the three focused red-to-green Flutter checks named above

## T6 — Connect a remote Agent

### Reproduced path

```text
Server App / Settings / User management
  -> create one Runtime User
  -> inspect the concrete IP-or-host connect target
  -> copy vibermate login --server <target>
client machine
  -> receive the HTTP warning before entering credentials
  -> log in once
  -> run vibermate doctor --server <same target>
  -> run vibermate run --server <same target> -- codex|claude
Server App / Insights
  -> select Runtime User
  -> inspect Workspace, model, and Client Session dimensions
```

Reproduced checks:

- `390px Settings gives Runtime User management its own tab` keeps the login
  and both run commands fully visible at the narrow review width.
- `TestRemoteLoginCommandPromptsAndPersistsWithoutEchoingSecrets` (the
  command package login path) verifies that the HTTP warning is printed before
  credential input, neither password nor token is printed, the password is not
  persisted, and the session file is owner-only (`0600`).
- `TestStatusAndDoctorVerifyTheRemoteRuntimeUserSession` verifies the stored
  session against the same Server and returns the exact next run command.
- `TestRemoteLauncherRunsChildWithoutLocalDesktopDaemon` and
  `TestRemoteLauncherRunsOverExplicitUnencryptedHTTP` exercise the real remote
  launcher over TLS and explicit HTTP.
- `TestServerHostAuthenticatesServerCreatedRuntimeUserOverExplicitHTTP`
  verifies that the Server freezes Runtime User, Login Session, device, machine,
  and Workspace attribution into the Capture and usage projection.
- The launcher sends bounded OS, OS version, architecture, home directory,
  local user, and IANA time-zone metadata on both local and remote run paths.
- `usage drill-down shows one grouping dimension at a time` verifies that the
  narrow UI reaches Workspace, exact requested→upstream model, and native
  Client Session evidence without rendering all dimensions at once.

Result: the one-time login and subsequent run are operational without a local
desktop daemon, transport risk is shown before the password boundary, and the
requested user/device/Workspace/session/model attribution is retained and
reachable. No P0 or P1 was reproduced. The need to move from Settings to
Insights is a normal task boundary, not a reproduced navigation failure.

## T7 — Progressive disclosure and cross-entry consistency

### Network exit

The reusable authority is singular and visible:

```text
Settings / Proxy Profiles
  -> publish exact direct or SOCKS5 + DNS revision
Traffic policies / Environment
  -> select that published revision
Capture
  -> retain the frozen revision used by the Turn
```

`390px Settings publishes reusable network Profile revisions` passed creation
of SOCKS5 plus DoH-through-proxy, publication, editing to revision two, and
reading immutable revision one. `internal/egressprofile` and
`internal/environment` also passed. The built-in direct/system-DNS profile is
read-only, so the first successful Capture does not require proxy knowledge.

### Automations

The empty state exposes code before taxonomy: Hide local paths, Show Turn time,
Adjust system prompt, and four Account Selector examples. Choosing one creates
an ordinary editable copy; the user is not asked to understand or create a
collection first. Blank remains available without becoming the primary call to
action.

The retained workflow is:

```text
example or copied Capture sample
  -> edit request/response JavaScript or Account decision
  -> run one local sample Turn
  -> publish immutable revision
Environment
  -> choose ordered Transform revisions or one Account Selector revision
Capture
  -> keep using the frozen revision until the operator applies a newer one
```

Reproduced checks:

- all seven `code_library_view_test.dart` scenarios pass at 390px/820px,
  including empty examples, real editable source, safe sample metadata,
  immutable revisions, and fixed actions below a scrollable editor;
- `Raw Transform evidence copies one complete attempt to Code Library` carries
  an exact local sample from Capture into Automations;
- `390px Environment freezes one ordered Code Library revision` preserves r1
  after r2 is published; and
- `internal/messagetransform`, `internal/accountselector`, and
  `internal/environment` pass the runtime ordering and fail-closed contracts.

### Navigation boundary

`workbench navigation is grouped into three user task areas` confirms that
Traffic, Insights, and Configuration expose only their own secondary tabs.
Opening Settings removes the Configuration strip completely; returning to
Configuration restores it. This directly guards the previously reported
confusing state where Settings still appeared under “Traffic policies / Upstream
services / Automations”.

Result: advanced proxy and JavaScript capabilities are optional, centrally
managed, and bound by immutable revision rather than duplicated in every
Environment. No new P0/P1 was reproduced, so no additional wizard, file tree,
pipeline framework, or collection-management surface is justified.

## Cross-cutting language, width, and accessibility verification

The Flutter copy catalog contains 1,110 English keys and 1,110 Simplified
Chinese keys with identical key sets and no duplicate keys. The following
public-surface checks pass:

- keyboard navigation and screen-reader authority remain explicit;
- the 390px Chinese Conversation, network-rule, Environment, Account Header,
  launch-variable, Message Transform, storage-disclosure, and remote-login
  surfaces complete without overflow;
- 20 Runtime Users remain searchable and ranked at 390px; and
- failed Turn and Account Selector recovery guidance retain the safe technical
  diagnosis in both English and 390px Chinese layouts.

### Finding U-004 — HTTP login warning ignored the selected CLI language

- Primary evidence: **reproduced CLI behavior**.
- Severity: **P1**, high confidence for the defect, frequency unknown.
- Baseline behavior: with `LC_ALL=zh_CN.UTF-8`, the safety warning shown before
  a Runtime User password was still hard-coded English even though help,
  status, doctor, and subsequent error guidance used the Chinese catalog.
- Red: `TestRemoteLoginHTTPWarningUsesSelectedCLILanguage` observed the English
  warning under the Chinese locale.
- Green: the login boundary now renders one catalog key through the existing
  POSIX locale detector. Both the English warning-before-read contract and the
  Chinese warning test pass; no credential is read before the warning.

No additional P0/P1 was reproduced in the cross-cutting pass. Human task
completion, time, SEQ, SUS, and terminology teach-back remain intentionally
unclaimed until target participants run the frozen protocol.

## Prioritized evidence ledger

| ID | Primary evidence | Priority | Candidate status | Remaining validation |
| --- | --- | --- | --- | --- |
| U-001 Empty Capture hid the normal Agent launch path | Reproduced | P1 | Fixed and retested | Human first-click and time to first Capture |
| U-002 Area labels require terminology translation | Inferred from reproduced copy | P2 | No product change | Human teach-back and backtracks in T3/T4 |
| U-003 Missing Account interrupts an Environment draft | Reproduced composition; impact inferred | P2 | No product change | Whether a participant actually starts in Traffic policies and abandons the draft |
| U-004 Chinese CLI received an English HTTP safety warning | Reproduced | P1 | Fixed and retested | Human warning teach-back |
| U-005 Advanced automation terms may precede their purpose | Inferred; counter-evidence reproduced | P2 | No product change | T7 false-belief check with target participants |
| U-006 Missing packaged sidecar offered an ineffective Retry | Reproduced | P1 | Fixed and retested | Bundle verification and isolated live process smoke complete; local native Keychain ACL boundary documented below |
| U-007 Failed Turn exposed codes without causal recovery | Reproduced | P1 | Fixed and retested | Human recovery statement for each seeded boundary |
| U-008 Account Selector preview led with an HTTP envelope | Reproduced | P1 | Fixed and retested | Human JavaScript correction task |

There are no reproduced P0 findings and no unresolved reproduced P1 findings.
The three P2 entries remain deliberately unimplemented: each requires target
participant behavior to distinguish a real task cost from vocabulary that is
necessary to operate the product accurately. There are no standalone P3
findings worth changing before that evidence exists.

## Expert walkthrough outcome

After the five P1 remediations, repeatable product seams complete T1–T7 in
English and Simplified Chinese, including the 390px variants named above. This
means the product mechanics and next-action states pass the expert protocol;
it does **not** establish a human completion rate, time-on-task, SEQ, SUS, or
population-level usability claim. Those fields remain blank until the planned
participant sessions use the frozen task sheet.

## Requirement-by-requirement completion audit

| Goal requirement | Authoritative evidence | Audit result |
| --- | --- | --- |
| Keep `7e8efc438de3b1417cab5302676e2b36187c0007` as the frozen baseline | Current `HEAD` is still the exact baseline; every review change remains an uncommitted worktree diff. | Proven. |
| Establish repeatable empty state, seeded state, task script, success criteria, hint ladder, and record fields | The frozen protocol defines fixtures, T1–T7, falsifiable success criteria, the 0–4 hint ladder, and completion/time/error/backtrack/hint/recovery/SEQ/teach-back/evidence fields. Preview, temporary-directory, loopback-provider, and remote-server seams reproduce the required states without operator data. | Proven. |
| Walk through App, Web, CLI, and remote Runtime Server from a novice Agent-user perspective | T1–T7 above cover readiness, zero-configuration Capture, Endpoint/Account/Environment, exact A-to-B mapping, failure diagnosis, remote login/run, Settings/Proxy Profiles, and Automations. | Proven as expert walkthrough and reproduced behavior; not mislabelled as human observation. |
| Cover Simplified Chinese, English, desktop, and 390px | The cross-cutting section names the paired locale, viewport, keyboard, accessibility, and 20-user checks; the full Flutter suite passes. | Proven. |
| Separate observed, reproduced, and inferred evidence and rank P0–P3 | The protocol defines the classes and severity rules; the ledger records zero reproduced P0, five reproduced P1, and three P2 hypotheses awaiting participants. | Proven. |
| Record completion, time, errors, backtracks, hints, SEQ, and teach-back without inventing user data | The frozen task sheet contains every field. No target participant session occurred, so human-only values remain explicitly unclaimed, as required by the protocol's exit criteria. | Proven for the review; human population evidence remains future work. |
| Fix only reproduced P0/P1 test-first and rerun the same tasks | U-001, U-004, U-006, U-007, and U-008 each link red/green evidence and their task-level retest. No inferred P2 triggered a product change. | Proven. |
| Run full source, structure, Go, Flutter, native macOS, and build gates | The final validation section below records the successful gates and the exact local Keychain boundary separately. | Proven with the documented host-state caveat. |
| Produce a directly launchable macOS retest candidate | `/dist/ViberMate.app` exists, passes the live bundle verifier and strict recursive signature verification, and contains the packaged CLI and daemon. The real-process flow passes 4/4 with isolated secrets. | Proven; this developer account's stale ad-hoc Keychain ACL is not altered by the review. |
| Avoid unsupported visual redesign, commits, and pushes | The diff is limited to the five evidenced recovery/discovery fixes, their tests and locale keys, plus review documents. `HEAD` remains the baseline. | Proven. |

## Final build and validation evidence

The source and repository gates passed after the five P1 fixes:

- `make check-format`, `go mod tidy -diff`, the repository structure check,
  `go vet ./...`, and `go test ./...`;
- Flutter 3.41.5 `flutter analyze` and the full widget suite: 240 passed, with
  four explicitly opt-in live tests skipped by their environment guard;
- seven native macOS Runner tests, release tooling, native-secret cross-builds,
  workflow pinning, and Actionlint; and
- the release web build plus a 49.2 MB macOS App containing signed CLI and
  daemon sidecars.

The resulting `/dist/ViberMate.app` passes the repository's live bundle
verifier and strict recursive `codesign` verification. Its packaged CLI and
daemon SHA-256 values are respectively
`db84ca86a72534b4822c12c4677246e4a46ff6622ffebb086d44849197e9eb8f` and
`fd643ee6982a682d7cf3e2acf4a495e1bfba8ac5c33d353ccefd7e9c15defb86`.
`git diff --check` is clean and no `vibermated` process remains after the run.

### Native Keychain validation boundary

The canonical packaged live smoke reaches `runtime_starting`, then this local
machine blocks in `SecItemCopyMatching` while reading the fixed client
annotation signing key. The existing Keychain item was created by an older
ad-hoc-signed development build; each local ad-hoc build has a different
designated requirement, so macOS does not grant the new daemon access to that
old item. The item and its secret were not read, deleted, or modified during
this review.

To separate product flow from that machine-local ACL, the same live test was
rerun with the existing development file-backed secret store in an isolated
temporary home. All four real-process checks passed: daemon plus Control API,
the exact packaged CLI path, CLI discovery producing a Capture, and unexpected
daemon-exit observation. The temporary fixture directory was then moved to
Trash.

An additional opt-in native-secret test also reproduced a pre-existing,
unrelated integrity defect: `TestConcurrentKeychainReplaceIsPhysicalCAS`
allows both concurrent replacements to report success on this macOS. The
ordinary Keychain store/read/revision guard test passes. No per-process mutex
was added merely to satisfy the current test because it would not provide the
cross-process physical compare-and-swap guarantee named by the contract. This
requires a separate security/persistence decision and is not evidence against
the novice-usability changes in this review.
