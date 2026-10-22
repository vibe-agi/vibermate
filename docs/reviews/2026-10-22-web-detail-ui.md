# Web request-detail UI review — 2026-10-22

Base: `b03ebd8be040087d623f7a1b12c340e99994f56f` (v0.1.35). This follow-up traces the reported Chinese request-card messages and reviews the shared Flutter conversation reader, controller cache, detail decoder, and selection boundaries. Production files were not edited. The isolated Flutter fixtures and logs are in `/Users/null/Downloads/vibermate-web-detail-review-20261022`.

Three additional defects were reproduced: one P1 server/client content-cursor contract mismatch and two P2 UI cache/selection defects. The P1 rejects actual recorded-content HTTP 200 responses before rendering. The P2 findings can preserve the pending/no-content combination after completion or let an old Capture's failed detail read invalidate the new Capture's page. The follow-up also confirms how a reported summary 409 invalidates concurrent detail reads. These are confirmed code paths; matching a particular deployed failure still requires its response/build evidence. No deployed HTTP payload, browser cache, user data, credential, or live Runtime was accessed by this subreview, and no Go tests were run.

## What the three screenshot messages establish

| Message | Actual condition in this UI | What it does not establish |
| --- | --- | --- |
| `暂时无法读取该请求，已有记录并未被删除。` plus Retry | `_ReadingRequest` finished loading with a null detail. `_fetchExchangeDetail` catches every HTTP/control/transport/timeout/decoder exception and returns null. | It does not identify a server 503, cleanup failure, missing body, specific schema mismatch, or a failed Agent request. The card itself does not display `controller.exchangeError(id, contentView: 'reading')`. |
| `没有可用的已保存对话内容。…` | A successfully decoded/cached `ExchangeDetail` has `content.state == 'not_recorded'`. This is distinct from the previous load-failure path. | It does not by itself identify why content is absent, whether recording was enabled, or whether content might become available later. |
| `正在等待最终响应…` | The card's detail has `status == 'pending'`. | The widget does not inspect Capture/run lifecycle. An ended run is not proof that every optional Exchange terminal record was retained. A stale detail cache can also supply this status, as reproduced below. |

Sources: [`conversation_reader.dart:1447`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/features/workbench/conversation_reader.dart#L1447), [`:1546`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/features/workbench/conversation_reader.dart#L1546), [`:1601`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/features/workbench/conversation_reader.dart#L1601), and [`workbench_controller.dart:2828`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/features/workbench/workbench_controller.dart#L2828).

The first request is `GET /api/v1/exchanges/<id>?contentView=reading&contentMode=paged`. The API has bounded legacy fallback for specific 422 errors. It then strictly decodes the detail and verifies both Exchange identity and the requested projection. HTTP 200 followed by a DTO inconsistency therefore reaches the same generic unreadable message as a network failure. The ordinary response limit remains 2 MiB and timeout remains 30 seconds. [Request/decoder boundary](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/core/api/control_api.dart#L1630).

Usage is loaded separately and its failures are independently caught (`conversation_reader.dart:308–329`). Available run/session counts and token/cost totals do not prove that the corresponding body or terminal-detail projection was loaded successfully. This review does not infer the screenshot's HTTP error from those totals.

## WUI-3 — P1: Flutter rejects the signed content cursors emitted by the production server

The storage-role inspection reader wraps a content cursor as `proof + "." + innerCursor` to bind it to the authorized inspection generation, source and Exchange. [`ContentInspectionReader.wrapCursor:272`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/runtimepersistence/content_inspection_reader.go#L272) emits this format for paging and deferred block cursors. The paged reading projection sets `requestNextCursor` even for one small retained user message ([`exchange_content_page.go:115`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/runtimepersistence/exchange_content_page.go#L115)).

Flutter's [`_contentPageCursor:9435`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/core/api/control_models.dart#L9435) accepts only `^[A-Za-z0-9_-]+$`. The separator is rejected before the returned detail can render. Even fixing this decoder alone is insufficient: [`HttpControlApi.exchangeContentPage:1689`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/core/api/control_api.dart#L1689) repeats the incompatible validation before making a continuation request. The shared decoder also handles request/response evidence cursors, deferred blocks and subsequent page cursors.

The server-inspection subreview produced actual HTTP samples from the production HostServer/control composition with synthetic temporary state, recorded in `server-host-summary-samples.json`. This subreview fed those unchanged JSON bodies through the production Dart decoders:

| Actual server samples | Decoder result |
| --- | --- |
| Six summary responses, before/after content publication | All pass |
| Three `not_recorded` Exchange responses | All pass |
| Three recorded-content reading responses, HTTP 200 | All fail |

Each recorded-content response contains a valid 408-character dotted `requestNextCursor`. The exact failure is:

```text
Control contract error: exchange.content.page.requestNextCursor content cursor is invalid
```

`_fetchExchangeDetail` catches that exception and returns null, producing the screenshot's generic unreadable card even though the server retained and returned the content. This does not require a concurrent summary 409, recording cleanup, absent content, or a broken model request. The no-content responses have no cursor, which explains why this mismatch can coexist with a card displaying a decoded no-content state. The independent server probe additionally verifies that the same cursor is accepted by the server; its source/evidence are documented in the server-inspection subreview.

A second focused test calls public `HttpControlApi.exchangeContentPage` with that actual issued cursor and an in-memory HTTP client prepared to return the actual server's `issued_cursor_page` HTTP 200 body. The page body separately passes the production page decoder, but the public call rejects the cursor before sending anything:

```text
issued_cursor_bytes=408 contains_dot=true HTTP_calls=0
page_fixture_decodes=true
failure=Control contract error: Exchange content cursor is invalid
```

The desired assertion of one request with the unchanged cursor fails (`cursor-continuation-repro.log`). This directly verifies the second repair site rather than assuming initial-detail parsing is the only blocker.

Fix the client contract at both decode and continuation-request boundaries, retain the size bound and exact opaque cursor bytes, and add a real production-server → Flutter decoder/render → continuation regression. Do not strip the proof, send only the inner cursor, or relax server generation/source/privacy checks. Legacy unwrapped cursor compatibility, if retained, needs an explicit contract rather than tests that only invent base64-like cursors. This finding is jointly established by the server subreview's production wire samples and this subreview's strict Dart decoding.

## WUI-1 — P2: incomplete detail cache can remain stale after completion or content publication

[`loadExchangeDetail:2789–2796`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/features/workbench/workbench_controller.dart#L2789) treats any cached detail as reusable unless a caller explicitly requests refresh. Cache keys are Exchange ID plus content view; entries have no freshness/status/content-publication version. A successful `pending` or `not_recorded` result is cached just like a complete retained result.

On row creation, [`_ReadingRequest.initState:1378–1385`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/features/workbench/conversation_reader.dart#L1378) takes that cache and calls `_load()` without refresh. [`didUpdateWidget:1391`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/features/workbench/conversation_reader.dart#L1391) requests a fresh detail only when the still-mounted row observes an Activity status transition. It ignores `contentAvailable` changes. Switching Capture resets the selected page but does not clear these detail entries (`workbench_controller.dart:3843–3864,4013–4031`).

Two deterministic cases reproduce the consequences:

1. **Pending viewed, then completed while offscreen.** Load a pending/no-content detail, unmount the reader to represent leaving the conversation, publish a succeeded detail and saved reply in the in-memory API, and reopen with a succeeded Activity. The row makes no new API call and retains the pending/no-content messages:

   ```text
   reopened_activity=succeeded API_calls=1 cached_status=pending pending_visible=true
   ```

   The assertion that the pending message disappears fails. There is no Retry button in this branch because the detail is non-null. A stale pending cache is therefore a possible explanation for a contradictory-looking card after reopening, independently of recording cleanup. The screenshot alone does not prove that this sequence occurred.

2. **Content becomes available after terminal metadata.** Initially render a succeeded Activity with `contentAvailable=false` and a successful `not_recorded` detail. Supply a retained reply and rebuild with the same succeeded Activity, now `contentAvailable=true`. The row keeps the no-content state:

   ```text
   same_status=true content_available=false->true API_calls=1
   retained_reply_visible=false cached_content=not_recorded
   ```

   The assertion that the retained reply becomes visible fails. The fixture models a later successful body publication; it does not itself prove how often production reaches this ordering. The architecture's independent optional projections make treating an absent-body snapshot as permanently fresh inappropriate.

There is an additional source-level obstacle to propagating this update: the [selected-evidence poll equality](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/features/workbench/workbench_controller.dart#L2000) compares ID, timestamp, reason and status but omits `contentAvailable`. The manual/newest-record comparison at 1961–1966 does the same. A positive body-availability change can therefore be ignored before it reaches the row. Those polling branches were inspected, not separately exercised by this fixture.

Fix direction: make cache reuse sensitive to current Activity evidence, refresh incomplete/pending entries when reopening a visible request, propagate body-availability changes through polling, and refresh the row when content availability changes without a status change. Preserve bounded requests and stable scroll position. Do not infer success from a stopped Capture, fabricate a response, or retry provider/model execution. Regression coverage should include both cases above, visible pending→terminal, explicit refresh, and disposal during a load.

## WUI-2 — P2: a late detail error from the previous Capture clears the current Capture's readable page

The controller already distinguishes operational failure of an old selection from a real global source/visibility conflict in [`_inspectionRead:3880–3888`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/features/workbench/workbench_controller.dart#L3880): an old selection's `503 inspection_unavailable` must not describe a newer one. However, initial Exchange detail loading bypasses this wrapper. Its [catch path at 2847–2853](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/features/workbench/workbench_controller.dart#L2847) has no selection-generation check and calls `_invalidateActivityInspection` directly.

Reproduction uses public controller operations:

1. Select Capture A and start a delayed `loadExchangeDetail` for one of its requests.
2. Select Capture B and complete its normal assignment, conversation, summary, and activity reads.
3. Complete A's old detail Future with `503 inspection_unavailable`.

Observed result:

```text
selected_capture=managed_run:run-1 before_rows=100 after_page=null
assignment_preserved=true failure=inspection_unavailable (503)
```

The desired assertion that B's page survives fails. Required Capture assignment remains intact; this is misattributed optional-history failure, not loss of permissions or persisted rows. The new selection may be healthy even though an older request reports a delayed transient failure. A global source/visibility conflict is different and must still invalidate stale evidence.

Fix direction: use the same captured-selection guard for initial Exchange detail failures as for paged content/tool reads. Retain global invalidation for actual source-generation or visibility conflicts; do not broadly suppress errors after navigation. Test old-selection 503 after B succeeds, same-selection 503, actual global 409 after navigation, and a late successful response after privacy invalidation.

## Reported summary 409: confirmed invalidation coupling, not a reason to bypass privacy checks

The reported wire body `{type: "urn:vibermate:error:inspection-changed", title: "Conflict", status: 409, code: "inspection_changed"}` is correct. [`ControlProblem.fromJson:455–496`](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/core/api/control_api.dart#L455) explicitly requires `code` and maps it to `reasonCode`. Its regression passes; this is not a `code` versus `reasonCode` decoder defect.

`loadActivitySummary` uses `_inspectionRead`. A 409 runs `_invalidateActivityInspection` → `_clearActivityInspection`, advancing the shared inspection generation and clearing detail caches, conversations and the selected activity page. A detail read already in flight then returns null at `workbench_controller.dart:2837` even if it decoded a successful HTTP 200 response. This is the intended rejection of potentially stale evidence after a privacy/source conflict, and an existing repository test explicitly requires it.

A new bounded fixture uses the real `HttpControlApi` with an in-memory HTTP client and an actual decoder-valid synthetic server detail. Three summary-409/detail-200 interleavings produce:

```text
pass=1/2/3 summary=409 detail_HTTP=200 returned=null cached=null detail_error=null
without_interleaving_summary detail_HTTP=200 detail_decoded=true status=succeeded
next_summary=409 previously_successful_detail_cache=null
```

Thus repeated summary conflicts can prevent successful point reads from stabilizing, and cannot be solved by simply retrying the same point read alongside the failing summary. The fixture intentionally uses the `not_recorded` response so the cursor bug does not confound this result. The reason the deployed summary repeatedly conflicts remains a server-side question; the fresh synthetic server's summary responses themselves returned 200.

On this exact UI baseline, global invalidation also unmounts the main conversation reader and shows a Capture-level failure (`captures_view.dart:669–683,808–824`). Consequently, persistent per-card failures alongside an intact timeline are not fully explained by this shared-generation race alone. The cursor contract mismatch above is independently sufficient for per-card failure. Do not suppress all summary 409s or keep pre-conflict snapshots to mask the symptom. Any narrowing/recovery change must first establish which identity/generation changed and reacquire current authorized evidence.

## Decoder, cursor, and authority checks reviewed

- `HttpControlApi.exchange` binds the returned Exchange ID and projection to the request. `ExchangeDetail.fromJson` binds its parent/attempt/deferred-block owners to that ID.
- Content views are limited to incremental/full/reading. Returned content pages are checked against the requested Exchange. The client cursor syntax is too restrictive for the current signed format (WUI-3); this is rejection of authorized data, not an access-scope bypass.
- The reader's successful no-content state and null/error state are separate. A generic error message is insufficient to distinguish a rejected API response from a local decoder failure. Existing safe failure-code infrastructure can expose a redacted reason/category without displaying raw payloads or credentials.
- A request's pending status and Capture's ended status are independent. Optional terminal evidence can be missing; the UI should describe the observation faithfully. It must not rewrite such a request to succeeded solely because the Agent process ended.
- A specific content-cursor incompatibility is confirmed with actual synthetic server responses (WUI-3). Matching the deployed endpoint's cursor shape and exact server/web build identity remains useful; full prompt/body/auth data is unnecessary. Summary 409 and detail-decoding rejection are separate paths and should not be conflated.

## Executed evidence

Fixture: `audit_reader_refresh_test.dart`, importing only existing public test helpers from `conversation_reader_test.dart`. It uses the real controller/widgets and an in-memory delayed API backed by synthetic preview data. No HTTP server, App session or provider was used.

From `ui/flutter_app`:

```sh
flutter test --no-pub \
  /Users/null/Downloads/vibermate-web-detail-review-20261022/audit_reader_refresh_test.dart \
  --reporter expanded
```

Result in `reader-refresh-repro.log`: three desired-behavior assertions fail against the unchanged base (the two WUI-1 cases and WUI-2). One separate message-mapping test passes, verifying that pending/no-content detail produces both reported messages. These are reproductions of existing defects, not fixed regressions. An initial fixture-only missing import was corrected before this recorded run; no production compilation issue is claimed.

Additional fixture: `audit_summary_and_wire_test.dart`, run with the same `flutter test --no-pub ... --reporter expanded` command. `summary-and-wire-repro.log` records 11 passing checks and three failed recorded-content decoder assertions: exact wire problem decoding passes, six summary and three no-content DTO decodes pass, and the repeated-summary-conflict witness passes. Its HTTP client is a `MockClient`; no live network request was made. The input JSON came from the separate synthetic HostServer probe, not user data.

The subsequently added public continuation test was run alone with `--plain-name 'public continuation accepts the actual server issued cursor'`. `cursor-continuation-repro.log` records its expected-behavior assertion failing with zero outgoing requests. The fixture reads `server-cursor-samples.json`; `server-cursor-probe.log` separately records the backend accepting the same cursor with HTTP 200. No production fix was applied or claimed.

No causal link to the previous maintenance/Go/Codex compatibility findings is assumed. Production fixes and deployment diagnosis remain separate from this read-only subreview.
