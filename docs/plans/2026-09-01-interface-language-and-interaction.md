# Interface Language and Interaction

Status: completed
Created: 2026-09-01
Baseline commit: `7e8efc438de3b1417cab5302676e2b36187c0007`
Working tree: preserve the existing uncommitted novice-usability changes

## Goal

Make the macOS App and Web management surface faithful, clear, and composed:

- **Faithful**: a label states the real object, authority, effect, and lifetime.
- **Clear**: a developer with basic terminal experience can predict the next
  action without learning implementation names first.
- **Composed**: controls with the same role share an anchor, size, rhythm,
  state, and responsive behavior.

This is not a visual rewrite. Existing domain authority, public APIs, Flutter
widgets, JavaScript sandbox, and immutable revisions remain the source of
truth.

## Evidence boundary

The following are the public seams for vertical tests. Tests must observe these
seams instead of private helper names or copied constants.

| Concern | Public seam | Required evidence |
| --- | --- | --- |
| Visible language and interaction | `WorkbenchShell`, top-level views, and public editor dialogs | Rendered text, enabled action, focus order, semantics, and resulting navigation at 1180 px and 390 px in English and Simplified Chinese |
| Message-transform examples | Code Library gallery -> public editor -> Control API publish/read | Exact UTF-8 source survives create, edit, publish, and reopen; every example executes in the real Goja test sandbox |
| Account-selection examples | Code Library gallery -> public account-selection editor -> Control API publish/read | Exact UTF-8 source survives create, publish, and reopen; all four examples select the expected exact Account ID or fail closed |
| Protocol behavior | `POST /api/v1/message-transforms/actions/test` and `POST /api/v1/account-selectors/actions/test` | Anthropic Messages, OpenAI Responses, and OpenAI Chat; streaming and non-streaming response fixtures; exact request/response bytes |
| Turn annotations | Real transform test endpoint and next-request cleanup path | A signed annotation is visible in the current Turn and absent from the next outbound request without deleting user text |
| Offline protection | `offlinehold.RuntimeCoordinator` plus the existing live acceptance tracer | Production queue/byte boundaries, release concurrency, cancellation/recovery interleaving, repeated state cycles, and race-clean execution |
| Translation integrity | `AppCopy` as rendered by public widgets | Both languages contain the same keys and placeholders; no visible lookup key or unintended implementation error is rendered |

## User-facing vocabulary

Internal Go, API, database, and evidence names do not change. The primary UI
name appears first; a technical name appears only where it identifies a real
wire or evidence field.

| Meaning | English UI | Simplified Chinese UI | Avoid in primary UI |
| --- | --- | --- | --- |
| Reusable JavaScript area | Script library | 脚本库 | Code Library, 自动化代码集合 |
| Header/body request or response logic | Message transform | 消息变换 | Transform Policy, generic “变换” |
| JavaScript account decision | Account selection rule | 账号选择规则 | Account Selector, 账号选择器 |
| Fixed account decision | Fixed account | 固定账号 | selector mode |
| Immutable published value | Published revision / rN | 已发布版本 / rN | “保存” when publication occurs |
| Runtime routing object | Traffic policy | 流量策略 | Environment as the only visible explanation |
| Stable technical identity | Environment ID | Environment ID | translating or silently changing the ID |
| Remote AI origin and protocols | Upstream service | 上游服务 | Endpoint as the primary page name |
| Account owned by one upstream service | Upstream account | 上游账号 | credential, provider account |
| Reusable SOCKS5/DNS choice | Network exit profile | 网络出口方案 | Profile mixed into Chinese prose |
| Observed Agent run | Capture | 运行记录（Capture） on first use, then 运行记录 | Session, conversation |
| One dialogue round | Turn | 对话轮次（Turn） on first use, then 对话轮次 | request when a Turn has several calls |
| One retained API boundary | API call / Exchange in evidence detail | API 调用 / 证据详情中的 Exchange | Turn, billing event |
| ViberMate login identity | Runtime user | 运行用户 | upstream account |
| User project context | Workspace | 工作区 | project when the value is the client Workspace identity |
| Queueing external work for disconnection | Offline protection | 断网保护 | “保持网络”, which reads as staying online |

Capitalized protocol and product names such as Anthropic Messages, OpenAI
Responses, OpenAI Chat, Codex, Claude, JavaScript, SOCKS5, and DNS remain exact
proper names. IDs and code properties remain exact and are never translated.

## Action grammar

1. Name the object and the result: “New message transform”, not “New”.
2. “Save draft” changes a mutable draft only. “Publish new revision” creates
   the immutable value that a Traffic policy can freeze.
3. If one action creates and publishes, say “Create and publish”. Never label
   that action only “Save changes”.
4. Creating from a built-in example makes an editable private copy. The action
   is “Create from example” / “以此新建”, not the ambiguous “Use example”.
5. “Apply to next Turn” affects only a future Turn. It never implies that the
   current Turn changes.
6. Destructive actions name the exact object. Confirmation text states what is
   deleted, what is retained, and whether recovery is possible.
7. A failure message has three parts in this order: what failed; what remained
   unchanged; the next safe action. Raw exceptions belong under Technical
   details and never replace the message.
8. A status describes the current state; a button describes the transition.
   For example, “Online” is a state and “Prepare to disconnect” is an action.
9. Empty states explain why the area is empty and offer the normal next action.
   Optional or advanced actions must not appear as the required first step.

## Interaction hierarchy

- The left rail chooses a product area.
- A page-level task strip chooses sibling pages within that area. It spans the
  available width and is anchored left in both languages.
- Settings owns its own left-aligned page tabs and does not retain the
  Configuration task strip above it.
- A two-choice mode inside one page uses the existing compact segmented
  control, is left-aligned, and does not impersonate page navigation.
- A primary action changes server state. Secondary actions inspect, test, copy,
  or cancel. One surface has at most one visually primary action at a time.
- Dialog identity, authority notice, editable content, validation, and footer
  actions remain in that reading order. The footer stays reachable at the
  minimum supported viewport.
- Default collections are implementation storage. With one collection, no
  collection control or tree level is shown. Multiple real root collections
  may be shown without adding folders or a file-tree model.

## Visual grammar

- Reuse `ViberMetrics`, `ViberSpacing`, `CompactSegmentedControl`, shared form
  fields, notices, status pills, and editor chrome. Do not add a design-system
  package or editor dependency.
- Page-level tabs use one height, full-width background, left anchor, and the
  same selected-state treatment. Page-internal segmented controls use equal
  segment widths but remain compact.
- Inputs and dropdowns use the shared control height. Checkboxes retain a
  native square hit target and align to the first text baseline of their row.
- Example cards in the same row have equal height. Their descriptions reserve
  the same content region and their action is pinned to the bottom.
- JavaScript shown as code uses the existing token palette. Read-only code is
  selectable and copyable; ordinary prose never uses the monospace treatment.
- Narrow layouts stack content in reading order without horizontal clipping.
  Fixed-height code previews may fade only after exposing a copy/open action.
- Light and dark themes preserve the same hierarchy and do not encode state by
  color alone.

## Current-state audit

The status below is derived from the current working tree, not from earlier
screenshots.

| ID | Surface | Current evidence | Required change | Status |
| --- | --- | --- | --- | --- |
| ILA-001 | Shell / Settings | Settings omits the unrelated Configuration task strip and retains its own page tabs. | Preserve with a public widget regression. | completed; public shell regression passed |
| ILA-002 | Page task strip | Product task navigation uses the native full-width, left-anchored tab treatment at wide and narrow widths. | Make the strip fill the width and verify its first tab anchor at wide and narrow widths. | completed; wide and narrow widget regressions passed |
| ILA-003 | Script-library names | Heading, kind switch, menu, empty state, editor, and Traffic policy use Script library, Message transform, and Account selection rule. | Use Script library, New message transform, and New account selection rule consistently in heading, kind switch, menu, empty state, editor, and Environment. | completed; bilingual public widget regressions passed |
| ILA-004 | Example creation | Built-in examples create editable private copies and do not publish before the explicit publish action. | Rename the action to Create from example / 以此新建 and verify no publication occurs before the final action. | completed; create/edit/publish/reopen path passed |
| ILA-005 | Publication | Editor actions and completion feedback distinguish opening, testing, and publishing an immutable revision. | Distinguish opening an editor from publishing an immutable revision; completion feedback names the new revision. | completed; Control API publish/read contract passed |
| ILA-006 | Example cards | Grid rows equalize visible cards, pin actions, and render selectable source through the JavaScript palette. | Preserve exact source bytes and equal action anchors at wide and narrow widths. | completed; public widget and exact-source contracts passed |
| ILA-007 | Read-only source | Read-only JavaScript is highlighted, selectable, copyable, and exposes one accessible copy action. | Expose one accessible copy action without adding an editor dependency. | completed; no editor dependency added |
| ILA-008 | Default collection | Storage is created automatically and the picker remains hidden when only one collection exists. | Preserve the empty first-use path so “collection” is not a prerequisite concept. | completed; public empty-state contract passed |
| ILA-009 | Script errors | Stable failures render a recoverable primary message; raw exceptions appear only under Technical details. | Map stable failures to useful primary copy and retain the raw value only under Technical details. | completed; load and startup failure regressions passed |
| ILA-010 | Startup error | Runtime startup fallback comes from `AppCopy` with equivalent recovery language in both catalogs. | Move the fallback to `AppCopy` and render equivalent recovery language. | completed; catalog and public shell regressions passed |
| ILA-011 | Mixed terminology | Ordinary Chinese prose uses the vocabulary above; exact implementation identities remain only in technical fields and evidence. | Keep exact terms only where technically necessary; otherwise use the vocabulary above and explain the technical name once. | completed; final visible-text audit passed |
| ILA-012 | Offline protection wording | Feature, state, and transition are distinct: Offline protection / Online / Prepare to disconnect / Safe to disconnect / Resume online, with equivalent Chinese. | Separate feature, state, and transition: 断网保护 / 联网运行 / 准备断网 / 可安全断网 / 恢复联网. | completed; bilingual state/action regressions passed |
| ILA-013 | Offline production limits | Tests exercise 256/257 requests, 64 MiB/+1 byte, release window 8, repeated cancellation/resume cycles, and both valid race orderings. | Add exact 256/257 and 64 MiB/+1 boundaries, full release window, cancellation/recovery interleaving, repeated cycles, and race evidence. Do not claim long-soak certification. | completed; package tests and `-race -count=10` passed |
| ILA-014 | Protocol labels | Three protocol names are duplicated as hard-coded visible strings in multiple editors. | Keep the proper names exact; route them through one existing presentation helper only if the first behavior change needs it. No speculative registry. | accepted debt |
| ILA-015 | Bilingual integrity | Both catalogs contain 1,115 keys with identical placeholders; required lookups assert in debug and optional open-enum lookups use an explicit nullable path. | Preserve catalog parity in a runnable check and ensure public surfaces cannot render a missing lookup key in either language. | completed; `AppCopy` contract passed |
| ILA-016 | Cross-page consistency | Captures, Usage, Traffic policies, Upstream services, Connections, Script library, and Settings follow the shared vocabulary and action grammar. | Apply this grammar in vertical user tasks, then run one final cross-page audit rather than a blind string rewrite. | completed; cross-page bilingual audit passed |
| ILA-017 | Widget-test trust | Public Workbench interactions target real tappable controls, and hit-test warnings are fatal for the suite. | Make hit-test warnings fatal for the relevant regressions and interact through the real tappable public control before using them as completion evidence. | completed; 68 behavior tests passed with fatal warnings enabled |

## Baseline checks

| Check | Result on 2026-09-01 |
| --- | --- |
| English/Chinese catalog keys | 1,115 / 1,115; no missing or duplicate keys |
| English/Chinese placeholders | no mismatch |
| Full Flutter suite | 257 passed; 5 external-runtime/package-dependent tests skipped by their declared preconditions |
| Workbench behavior suite | 68 passed with hit-test warnings fatal |
| Script-library suite | 22 passed |
| Exact built-in source through public Script-library UI | 9 message-transform protocol variants and 4 account-selection rules passed; publish/reopen preserves exact source |
| Real packaged Runtime contracts | 9 message-transform protocol variants and 4 account-selection rules passed through Control API and Goja; signed Turn-time annotations are injected once, removed before the next request, and preserve ordinary content |
| Offline protection | default 256-request and 64-MiB boundaries passed; `go test -race -count=10 ./internal/offlinehold` passed |
| Go and Flutter static gates | `go test ./...` and `flutter analyze` passed |
| Repository structure and formatting | `go run ./cmd/repositorycheck` and `git diff --check` passed |
| Packaged App boundary | live Web/macOS build succeeded; 5/5 live daemon, Goja, CLI identity, discovery, and failure-observation tests passed |

## Vertical implementation order

1. Freeze the public seams above and the bilingual vocabulary.
2. Make built-in examples exact executable contracts before changing their
   presentation.
3. Close the complete Script library path: empty -> example -> edit -> test ->
   publish -> reopen -> select in a Traffic policy.
4. Fix shared navigation and error presentation at their common source.
5. Apply the same action and status grammar to remaining top-level tasks.
6. Prove Offline protection boundaries independently of its wording changes.
7. Run the complete language, theme, viewport, keyboard, semantics, Go, Flutter,
   structure, and packaged-App gates.

## Explicit non-goals

- No Monaco or other editor dependency.
- No VS Code-style file tree, nested folders, collection manager, wizard, tour,
  plugin system, or new design framework.
- No provider inference, account fallback, script persistence across Turns, or
  broad script capability expansion.
- No claim of human completion rates without real target participants and no
  claim of long-running soak coverage without a recorded run.
