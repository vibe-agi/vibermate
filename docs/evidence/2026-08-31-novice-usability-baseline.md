# Novice Usability Baseline

Date: 2026-08-31
Source baseline: `7e8efc438de3b1417cab5302676e2b36187c0007`
Protocol: `docs/plans/2026-08-31-novice-agent-usability-review.md`
Evidence state: expert walkthrough and automated retest complete; no human
participant results claimed

## Frozen walkthrough inventory

| Component | Version or identity |
| --- | --- |
| Source | `7e8efc438de3b1417cab5302676e2b36187c0007` |
| Packaged App | `0.1.0` (`CFBundleVersion` 1) |
| macOS | 26.5.2 (25F84) |
| Flutter | 3.41.5, framework `2c9eb20739` |
| Go | 1.25.13 darwin/arm64 |
| Codex CLI | 0.151.0 |
| Claude Code | 2.1.251 |

Packaged binary SHA-256 identities:

- `vibermate`: `5005e2deca525a1dd4a070a523bb475aa3023cf586e8610a5702c104888b4598`
- `vibermated`: `bb431bc33a559241b8e5296c16a9c7dbed65b786890f365795d7c2a460fe5588`

## Isolation and test-state contract

The review reuses existing product seams; it does not add a usability-only
runtime or inspect the operator's live database.

| Evidence need | Existing seam | Isolation rule |
| --- | --- | --- |
| Empty, seeded, loading, and error UI | `PreviewControlApi` | Use deterministic fixture objects in widget tests. No filesystem or credential access. |
| Real App-to-daemon Control API | `DesktopRuntime.start` | Pass fresh absolute `cacheDirectory` and `dataDirectory` beneath a system temporary directory; close the daemon and remove the directory after the run. |
| Packaged CLI discovery and managed run | `DesktopRuntime.start(homeDirectory: ...)` | Give both daemon and CLI the same temporary `HOME`; never use the observer's default cache or Application Support paths. |
| Persistence and provider failures | Existing Go integration fixtures | Use `t.TempDir()` databases and loopback provider listeners with fixture-only credentials. |
| Remote login and run | Headless Runtime Server fixture | Use a temporary server data directory, loopback listener, disposable Runtime User, and disposable client state directory. |

The existing Flutter live-runtime test already proves the critical isolation
path by launching the real packaged daemon with temporary cache/data paths and
by launching the packaged CLI with a temporary `HOME`. The review extends that
pattern only where a task needs reproducible evidence. It will not redirect the
installed App to a temporary profile or kill an operator-owned daemon.

Reproduction on 2026-09-01:

```text
flutter test test/live_runtime_test.dart
  + real daemon with temporary cache/data
  + Control API authority and fresh Runtime state
  + packaged CLI with shared temporary HOME
  + managed run produces a Capture
  + unexpected daemon exit is observable
result: 4/4 passed
```

The first attempted run pointed the environment variables at
`ui/flutter_app/dist` instead of the repository `dist` directory. It failed
before product startup with “sidecar unavailable”, was classified as observer
setup error, and is excluded from product findings.

The deterministic Preview fixture was then exercised through the three UI
suites that cover the seeded walkthrough state:

```text
flutter test test/workbench_behavior_test.dart \
  test/environment_editing_test.dart \
  test/code_library_view_test.dart
result: 86/86 passed
```

This covers seeded Captures, Endpoint-owned Accounts, multi-protocol routes,
model discovery failure with manual mapping, Environment draft/impact/publish/
reopen, Proxy Profile revisions, ordered transforms, Account Selectors,
starter examples, Chinese narrow layouts, and settings navigation. Three
existing `tester.tap` calls emitted non-fatal hit-test warnings while their
tests still passed. They are test-harness reliability debt, not evidence that a
participant could or could not activate the corresponding control, and are not
counted as product findings.

## Current task architecture

```text
App shell
├── Traffic
│   ├── Captures
│   └── Connections
├── Insights (Runtime Server management only)
│   └── Team insights
├── Configuration
│   ├── Traffic policies
│   ├── Upstream services
│   └── Automations
└── Settings
    ├── General
    ├── User management (Runtime Server management only)
    └── Proxy Profiles
```

The Settings page is a separate global destination. It does not retain the
Configuration task tabs. That hierarchy is covered by
`workbench navigation is grouped into three user task areas`.

The CLI exposes these primary entry points:

```text
vibermate help
vibermate status [--server host]
vibermate doctor [--server host]
vibermate login --server host
vibermate logout --server host
vibermate run [--server host] [--env environment-id] -- command
vibermate capture create ...
```

## First-success dependency paths

Zero-configuration local Capture:

```text
open App
  -> runtime ready
  -> Terminal command available
  -> vibermate run -- codex|claude
  -> system_transparent / Original Destination
  -> Capture
  -> Conversation
  -> Turn or independent Exchange
  -> raw and semantic evidence
```

Configured upstream Capture:

```text
Configuration / Upstream services
  -> create multi-protocol Endpoint
  -> create Endpoint-owned Account with exact auth transport
Configuration / Traffic policies
  -> create Environment
  -> add exact client protocol and origin
  -> choose Original Destination or upstream Endpoint
  -> choose only an Account owned by that Endpoint
  -> optionally add exact A -> B model mappings
  -> check impact
  -> publish
Terminal
  -> vibermate run --env environment-id -- codex|claude
```

Remote Capture:

```text
start Runtime Server
  -> open Web management UI
  -> authenticate with owner-only admin access key
  -> Settings / User management
  -> create Runtime User
client machine
  -> vibermate login --server host
  -> acknowledge HTTP risk before credential entry
  -> vibermate run --server host -- codex|claude
server workbench
  -> Capture attribution
  -> Team insights by Runtime User / Workspace / Session / model
```

## Vocabulary transitions

| User-facing area | Entity or command exposed inside it | Cognitive transition to test |
| --- | --- | --- |
| Traffic policies | Environment, Client Flow, Route, revision | Whether a user understands this is the reusable policy selected by `--env`. |
| Upstream services | Endpoint, Account, credential type | Whether a user understands one service may support several protocols and each Account belongs to one Endpoint. |
| Automations | Collection, transform, Account Selector, revision | Whether optional code remains optional and why publishing is required. |
| Captures | Conversation, Session, Turn, Exchange, Attempt | Whether the evidence hierarchy helps diagnosis instead of exposing storage structure. |
| Connections | Approval, connection, egress, rule | Whether incoming capture admission is distinguished from provider egress. |
| Settings / Proxy Profiles | Network exit Profile and revision | Whether the user can configure once and select a frozen revision later. |
| CLI `--env` | Environment ID | Whether CLI wording matches the “Traffic policies” navigation label. |

## Initial evidence ledger

These are candidates for the walkthrough, not final severity decisions.

### U-001 — Empty Captures does not offer the first successful action

- Evidence: **reproduced from UI composition**.
- Surface: `CapturesView` empty directory.
- Current behavior: the empty state renders only “No captures yet” / “还没有
  Capture。” with no detail or action. The Terminal setup and launch commands
  live under Settings.
- Risk hypothesis: a first-time user lands on the product's default page but
  receives no instruction connecting the App to `vibermate run`.
- Validate with: T2, first click, time to first command, hint level.

### U-002 — Product labels and entity names require silent translation

- Evidence: **reproduced copy**, usability impact **inferred**.
- Current behavior: navigation says “Traffic policies” while its primary action
  is “New Environment” and the CLI requires `--env`; “Upstream services” opens
  dialogs named Endpoint; Automations immediately introduces Collection,
  transform, Account Selector, and revision.
- Risk hypothesis: labels are individually accurate but make a novice learn two
  names for each object before completing the custom-upstream path.
- Validate with: T3/T4 teach-back and navigation backtracks.

### U-003 — Missing Account recovery leaves the Environment editor

- Evidence: **reproduced from UI composition**.
- Current behavior: a selected Endpoint without a ready Account disables adding
  the upstream route and says to create an Account under the Endpoint. The
  editor has no action that opens Upstream services; the user must leave the
  in-progress Environment editor, create the Account, then return.
- Risk hypothesis: the dependency is explained but the recovery path interrupts
  the task and may lose the user's place.
- Validate with: T3, whether the participant starts with Traffic policies,
  highest hint, abandoned draft, and time to recovery.

### U-004 — HTTP login warning ignores the selected CLI language

- Evidence: **reproduced source behavior**.
- Current behavior: `executeRemoteLogin` prints the HTTP credential warning as a
  hard-coded English sentence before reading credentials; other help, status,
  doctor, and transport descriptions use the locale catalog.
- Risk hypothesis: the warning occurs at the correct safety boundary but may not
  be understood by the Simplified Chinese target participant.
- Validate with: T6 warning teach-back. Safety meaning, not sentence recall, is
  the success criterion.

### U-005 — Advanced code names appear before their purpose is learned

- Evidence: **reproduced copy**, usability impact **inferred**.
- Current behavior: Automations exposes Collection, Message transform, Account
  Selector, published revision, test format, Sample Turn, and protocol-specific
  examples on one product surface.
- Counter-evidence: built-in examples and empty-state directions are present;
  selecting an example creates an editable copy rather than publishing it.
- Validate with: T7 discovery and the false-belief check that JavaScript is
  required for T2/T3.

## Claims not yet permitted

- No task completion rate or SEQ/SUS score exists.
- No item is yet a human-observed P0 or P1.
- Source inspection does not establish that a user will misunderstand a term.
- Existing widget coverage establishes operability, not learnability.
