# Novice Agent Usability Review

Status: complete
Created: 2026-08-31
Completed: 2026-09-01
Frozen baseline: `7e8efc438de3b1417cab5302676e2b36187c0007`
Branch: `feature/runtime-execution-policy`

## Goal

Determine whether a developer with basic terminal experience, but no proxy or
ViberMate domain knowledge, can capture, route, and diagnose Claude Code or
Codex traffic without being taught the product's implementation vocabulary.

The review first produces repeatable expert-walkthrough evidence. Human
completion rates are reported only after real target participants perform the
same frozen tasks. Simulated behavior is never labelled user observation.

P0 and P1 findings are fixed test-first and retested against the same task.
P2 and P3 findings are recorded but do not expand this goal by default.

## Target participant

The primary participant:

- has used Claude Code or Codex for ordinary development work;
- can install and run a command in a terminal;
- understands URLs and API tokens at a practical level;
- does not know MITM, CONNECT, wire dialects, protocol adapters, revisions,
  frozen snapshots, route aggregates, or ViberMate's data model; and
- wants the Agent to keep working first, then wants evidence when it fails.

Server operators, network-security specialists, and JavaScript policy authors
are secondary users. Their controls must remain available without becoming
prerequisites for the primary participant's first successful Capture.

## Questions and falsifiable hypotheses

| ID | Question | Passing hypothesis |
| --- | --- | --- |
| H1 | Can a new user tell whether ViberMate is ready? | Without documentation, the participant identifies ready or unavailable state and the next action in 2 minutes. |
| H2 | Can a new user capture useful traffic without configuration? | The participant starts `vibermate run -- codex` or `-- claude`, sends one prompt, and finds the Exchange within 5 minutes. |
| H3 | Can a new user connect a supplied AI service? | Given only a service URL, supported client protocols, Bearer token, and model ID, the participant completes one successful request within 12 minutes. |
| H4 | Does the routing model remain understandable? | After H3, the participant can explain in their own words what the account belongs to, where an Environment sends traffic, and what happens to an unmapped model. |
| H5 | Can failure evidence lead to recovery? | For each seeded failure, the participant identifies which boundary rejected the request and one correct next action within 3 minutes. |
| H6 | Can a remote client connect without server-side coaching? | Given a host, username, and password, the participant logs in, starts an Agent, and locates its Capture within 8 minutes. |
| H7 | Are advanced controls progressively disclosed? | Proxy and JavaScript policies can be found on request, but are not mistaken for required first-run steps. |

These times are product targets, not claims about population performance.

## Bias controls

1. Every run uses the exact baseline or records the replacement commit.
2. Task prompts describe outcomes and supplied facts. They do not name the
   control to click or use hidden domain terms such as `Route`.
3. The observer follows one hint ladder and records every hint.
4. A task cannot be called successful after the observer performs an action.
5. Expert inference, reproduced product behavior, and human observation are
   separate evidence classes.
6. Chinese and English runs use equivalent facts and success conditions.
7. Claude Code and Codex order is counterbalanced across human participants.
8. Secrets are fixture values. Recordings never contain a real credential.

## Fixed environments

| Environment | Purpose |
| --- | --- |
| Empty local macOS profile | First launch, bundled daemon, CLI installation, zero-config Capture, and empty states. |
| Seeded local macOS profile | Endpoint, Account, Environment, mapping, Capture evidence, and advanced-control discovery. |
| Local provider fixture | Exact `/v1/models`, Bearer and X-Api-Key assertions, opaque model IDs, streaming responses, and deterministic failures. |
| Headless HTTP Runtime Server | Web management, Runtime User creation, login, warning comprehension, remote run, and attribution. |
| 1180×760 desktop | Primary desktop information architecture and full-width editing. |
| 390×760 viewport | Minimum supported narrow interaction, overflow, keyboard, and action reachability. |

The seeded data contains one multi-protocol Endpoint, two accounts with visibly
different authentication transports, one direct Environment, one routed
Environment, one successful Capture, and failures for 401, 422, 503, and
Runtime unavailable. It contains no supplier-specific inference.

## Evidence classes

- **Observed**: a real target participant performed the task; the recording and
  task sheet identify the event.
- **Reproduced**: an automated test or repeatable walkthrough demonstrated the
  product behavior on the frozen baseline.
- **Inferred**: an expert predicts difficulty from a heuristic or cognitive
  walkthrough; it remains unvalidated by participants.

Every finding records exactly one primary evidence class and may link supporting
classes. An inferred issue cannot cite a participant failure rate.

## Hint ladder

| Level | Observer action | Result classification |
| --- | --- | --- |
| 0 | No help. | Independent success when completed. |
| 1 | Restate the desired outcome without naming UI or CLI controls. | Assisted success; one hint. |
| 2 | Name the product area, for example “look at runtime status”. | Assisted success; two hints. |
| 3 | Name the exact control or command. | Task failure for independent-completion metrics. |
| 4 | Observer performs an action or task is abandoned. | Task failure. |

## Tasks

### T1 — Establish readiness

Participant prompt:

> Open ViberMate and decide whether it is ready to capture an Agent. If it is
> not ready, find the safest next action.

Success:

- identifies local App versus remote Server context;
- distinguishes ready, starting, and unavailable;
- finds a useful recovery action without reading raw internal errors; and
- does not create an Endpoint or Environment merely to make the runtime ready.

Record: time, first click, wrong destinations, help opened, CLI command used,
and interpretation of `status` or `doctor` output.

### T2 — Capture the first Agent request

Participant prompt:

> Start Codex through ViberMate with its normal account, send “hello”, and show
> the exact request and response in the App.

Repeat with Claude Code for the counterbalanced half of participants.

Success:

- uses the zero-configuration Original Destination path;
- sees the new Capture without manually refreshing or restarting the App;
- locates the correct Conversation, Turn, Exchange, model, and response;
- understands that “System Transparent” still captures supported Agent APIs;
  and
- can tell whether content was retained or only metadata exists.

### T3 — Connect a supplied upstream service

Participant prompt:

> Use this service for both Claude Code and Codex. It supports Anthropic
> Messages and OpenAI Responses, uses the supplied Bearer token, and exposes the
> supplied opaque model IDs. Keep each Agent's normal request format.

The prompt supplies only the fixture URL, token, protocols, and model IDs. It
does not say “create Endpoint”, “create Account”, or “publish Environment”.

Success:

- creates one multi-protocol upstream service rather than duplicate services;
- creates an account under that service and selects Bearer transport;
- verifies live model discovery at the exact normalized `/v1/models` URL;
- creates or edits one Environment and routes both client protocols;
- selects only an account owned by the chosen upstream service;
- checks impact and publishes successfully; and
- completes one Claude Code and one Codex request.

### T4 — Create an exact model mapping

Participant prompt:

> When Codex requests model A, send model B to the supplied service. Leave every
> other requested model unchanged.

Success:

- distinguishes client-requested models from live upstream models;
- creates exactly `A → B` with no series or supplier inference;
- can manually enter opaque B when discovery is unavailable;
- saves, previews, publishes, and reopens the mapping; and
- explains unmatched passthrough behavior.

### T5 — Diagnose seeded failures

For each case, the participant receives only the failed Agent interaction:

1. wrong authentication transport or credential (`401`);
2. invalid control request while previewing (`422`);
3. upstream model discovery unavailable (`503`);
4. malformed Agent exchange before any provider attempt;
5. local or remote Runtime unavailable.

Success:

- names the rejecting boundary without blaming an unrelated component;
- finds the retained request, response, or zero-attempt evidence;
- identifies one safe corrective action;
- does not expose a credential while copying diagnostics; and
- distinguishes retryable failure from configuration change.

### T6 — Connect a remote Agent

Participant prompt:

> Connect this machine to the supplied ViberMate Server using the supplied
> username and password. Start Codex normally and show which user, device,
> workspace, session, and model consumed the request.

Success:

- completes one-time login and later run without server approval;
- understands the unencrypted HTTP warning before submitting a password;
- can distinguish login failure from Runtime unavailability;
- finds the remote Capture and usage attribution; and
- can log out or identify how access is revoked.

### T7 — Find optional controls

Participant prompt:

> Show where you would configure a SOCKS5/DoH network exit, redact a local path
> before a request, and choose an account with JavaScript.

Success:

- finds proxy profiles in Settings and published automation in Automations;
- understands that an Environment freezes published revisions;
- does not expect JavaScript to expose account credentials;
- can locate sample testing and before/after evidence; and
- does not believe these controls are required for T2.

## Measurements

Each task sheet records:

| Field | Definition |
| --- | --- |
| Completion | independent, assisted, failed, or not applicable |
| Time on task | prompt shown until success or stop |
| Errors | actions that move away from the task or create invalid state |
| Backtracks | returning to a previously visited screen or command after uncertainty |
| Hints | highest hint level and total hints |
| Recovery time | failure first visible until a correct next action is stated |
| SEQ | participant rating from 1 “very difficult” to 7 “very easy” |
| Teach-back | accurate, partially accurate, or inaccurate explanation of the relevant model |
| Evidence | screenshot, screen timestamp, CLI transcript, test name, or exact reproduction |

At the end of a human session, collect SUS once and ask what the participant
believes ViberMate changed in their Agent request. SUS is not collected during
expert walkthroughs.

## Severity and prioritization

| Severity | Rule |
| --- | --- |
| P0 | Core request cannot complete, the UI causes credential exposure/data loss, or the product silently claims success while traffic is not governed as shown. |
| P1 | A core task requires level-3 help, two or more participants fail at the same point, or a wrong mental model leads to unsafe routing or unrecoverable diagnosis. |
| P2 | The task completes but exceeds twice its target, repeatedly backtracks, or optional complexity obscures the next action. |
| P3 | Cosmetic or wording inconsistency with no material task effect. |

Each finding also records frequency (`n/N` where human evidence exists),
recoverability (self, hint, external help), and confidence (high, medium, low).
Severity is not calculated from an opaque numeric score.

## Execution order

1. Verify the baseline and record exact App, daemon, CLI, Flutter, Go, Claude
   Code, and Codex versions used by the walkthrough.
2. Build the empty and seeded local profiles with fixture-only credentials.
3. Run T1–T7 as a cognitive walkthrough in Chinese at 1180×760, recording every
   decision point and system response.
4. Repeat reachability and layout checks in English and at 390×760; repeat only
   protocol-specific task steps for the second Agent.
5. Convert every reproduced defect into the smallest public-seam failing test.
6. Produce the initial evidence ledger and P0–P3 backlog before changing source.
7. Fix only P0/P1, one vertical at a time, and rerun the original task after
   each fix.
8. Pilot the frozen human script with one participant; revise ambiguous task
   wording, not the product, before the remaining sessions.
9. Run 6–8 target participants. Report raw counts and medians; do not claim
   population significance from this sample.
10. Re-run the same tasks on the final candidate and compare completion, time,
    hints, SEQ, and teach-back.

## Fix gates

A P0/P1 fix is complete only when:

- the original evidence remains linked;
- a test fails on the baseline and passes on the fix where automation is
  possible;
- the public error or success state tells the user the next action;
- Chinese and English copy express the same authority;
- 1180×760 and 390×760 remain operable;
- no advanced capability becomes a first-run prerequisite; and
- the relevant Go/Flutter focused tests, full suites, structural checks, and
  packaged macOS build pass.

## Deliverables

- this frozen protocol;
- an information-architecture and terminology inventory;
- a task evidence ledger with reproduction links;
- a ranked P0–P3 usability backlog;
- before/after evidence for every P0/P1 fix;
- remaining boundaries and unvalidated hypotheses; and
- a directly launchable macOS App for retest.

## Exit criteria

The goal is complete when all reproduced P0/P1 findings are fixed and retested,
the automated gates pass, the App is built, and any human-only hypotheses are
explicitly left as awaiting participants rather than reported as proven.
