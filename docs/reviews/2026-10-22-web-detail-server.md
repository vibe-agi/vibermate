# Web request-detail server review — 2026-10-22

**Confirmed P1: successful recorded-detail responses contain signed content cursors that the current Flutter contract rejects.** This reproduces with a small ordinary request through the production inspection readers and HTTP handler. It is shared by Web and Desktop when they use these readers; it is not a Server-only database-layout defect. The separately observed repeated summary `409 inspection_changed` remains unexplained at the server boundary and must not be attributed to cleanup without more evidence.

Reviewed source: `b03ebd8be040087d623f7a1b12c340e99994f56f` (v0.1.35). Evidence: `/Users/null/Downloads/vibermate-web-detail-review-20261022/`. All probes used temporary Runtime data and synthetic records. No deployed server, user credential, retained user data, or production source was modified.

The parent review supplied live Network evidence: `/api/v1/activities/summary?captureRunId=…` repeatedly returned `409` with `code:"inspection_changed"`, while a request to `/api/v1/exchanges/…?contentView=reading&contentMode=paged` returned a populated successful Exchange JSON object. That is consistent with a decode failure after HTTP success, but the live response's full cursor bytes were not inspected in this subreview. The causal contract failure below is independently reproduced from current production code.

**The cursor mismatch and its scope.**

The server intentionally represents a content cursor as `signedInspectionProof.innerContentCursor`. `ContentInspectionReader.wrapCursor` verifies the inner cursor, signs a binding containing the control stamp, Exchange ID and inner cursor, adds the selected source ordinal, and joins the two Base64URL components with a dot. It enforces a 2,048-byte total ceiling. The corresponding read path requires the dot, verifies the proof, and pins the selected source; this is not an accidental punctuation character. See `internal/runtimepersistence/content_inspection_reader.go:207–277` and `internal/runtimepersistence/inspection_cursor.go:26–80`.

For the ordinary reading view, `GetPagedProjection` emits `requestNextCursor` even for the single small user message in this reproduction (`internal/runtimepersistence/exchange_content_page.go:110–132`). `wrapProjection` then wraps that cursor before the handler serializes it (`internal/runtimepersistence/content_inspection_reader.go:297–316`). Flutter decodes the entire detail before rendering its text, and `_contentPageCursor` permits only `^[A-Za-z0-9_-]+$`, excluding the required separator (`ui/flutter_app/lib/core/api/control_models.dart:9430–9438`). `HttpControlApi.exchangeContentPage` independently rejects the same valid dotted cursor before sending a continuation request (`ui/flutter_app/lib/core/api/control_api.dart:1682–1697`).

| Server output | Production wrapping | Flutter consumer affected |
| --- | --- | --- |
| `content.page.requestNextCursor` | `wrapProjection`, lines 309–313 | `ExchangeContentPaging.fromJson`, `control_models.dart:9494` |
| `content.page.requestEvidenceNextCursor`, `responseEvidenceNextCursor` | Same loop | `ExchangeContentPaging.fromJson`, lines 9495–9504 |
| `deferred.cursor` in request system blocks, messages, or response blocks | `wrapBlocks` / `wrapMessages`, lines 279–307 | `DeferredExchangeContent.fromJson`, lines 9447–9457 |
| Content-page `nextCursor`, `canonicalCursor` | `GetContentPage`, lines 346–350 | `ExchangeContentPage.fromJson`, lines 9754–9755 |
| Deferred blocks/messages inside a content page | `GetContentPage`, lines 351–358 | Same deferred decoder; continuation request guard also rejects the cursor |
| Deferred tool-result content | `getToolResults` retains the issued content cursor in the deferred block, `internal/desktopcontrol/tool_results.go:143–150` | `ToolResultPage.fromJson` delegates to the affected content-block/deferred decoder, `control_models.dart:8846–8856` |

The **tool-results page's own top-level `nextCursor` is a different format**: it Base64URL-encodes a JSON continuation containing its activity/content coordinates, with a 128 KiB ceiling (`internal/desktopcontrol/tool_results.go:102–119`). Its external cursor remains a single Base64URL component, so its current top-level regex is not the dotted-content bug. Nested deferred content remains affected. Do not broaden unrelated cursor validators indiscriminately.

Preserve the signed wrapper and send it unchanged. Removing the proof would break source/visibility/Exchange binding and is not a valid correction. The minimal compatibility change must align the content-cursor consumers and outbound guard with the actual server grammar, retain the length/owner checks, and preserve support for the older unwrapped form only where that compatibility is intentional. Server verification remains authoritative.

**Reproduction and evidence.**

`server-summary-probe_test.go` is an external Go overlay test in the existing `desktopcontrol_test` package. It starts the complete production Runtime with current-schema SQLite stores, creates a valid recording scope, writes one completed Exchange activity, and builds the actual `runtimecontrol.New` application with client-local identity resolution disabled. It requests summary and reading/paged detail three times without content, then writes a small retained request/response and repeats the same reads. The HostServer variant changes only the test Runtime host contract. The final run also sends the server-issued cursor back unchanged.

| Check | Result |
| --- | --- |
| Shared composition using Desktop host contract | Twelve summary/detail requests returned 200; test passed in 1.57 s (`server-summary-probe.log`). |
| Same composition using **Server** host contract | Twelve requests returned 200; test passed in 1.50 s (`server-host-summary-probe.log`). |
| Recorded detail size | **2,063 bytes**, including a valid **408-character dotted cursor**. The 2 MiB client body ceiling is not involved. |
| Server-issued cursor sent unchanged to the page endpoint | **200**, 215-byte response; final test passed in 1.46 s (`server-cursor-probe.log`, `server-cursor-samples.json`). |
| Production Flutter decoder applied to the actual HostServer samples | All six summary objects and all three `not_recorded` details passed. All three recorded details failed with `exchange.content.page.requestNextCursor content cursor is invalid` (`summary-and-wire-repro.log`, `audit_summary_and_wire_test.dart`, run by the UI review agent and inspected here). |

The Go command was `go test -count=1 -overlay <server-host-summary-overlay.json> -run '^TestAuditSharedSummaryAndDetailSteadyState$' -v ./internal/desktopcontrol`, with the evidence output path supplied through `VIBERMATE_WEB_DETAIL_EVIDENCE`. The overlay maps only test files; production readers, record selection, serializer and HTTP handler are unchanged. `server-host-summary-samples.json` preserves the exact bodies used by the Flutter decoder.

This establishes a failure for ordinary valid recorded content, not a contrived oversized record. A `not_recorded` detail has no content cursor and therefore passes the same decoder, explaining why a mixed collection can show unavailable cards beside a card saying no saved content. The UI's catch-all null-detail handling then renders the generic unavailable banner; see the separate UI review for its cache and error-state findings.

Existing ordinary Flutter API tests use synthetic cursor strings such as `cursor` and `complete` (`ui/flutter_app/test/content_page_api_test.dart:50–57,100–106`). The Go-produced deep-content Flutter test is opt-in and skips when `TASK5B_HTTP_ENDPOINT` is absent (`ui/flutter_app/test/candidate_content_http_test.dart:8,57–60`). A regression using an actual current inspection-reader response is needed: testing the Go serializer and Dart decoder independently did not enforce this shared grammar.

**Server read path and data roles.**

Both hosts call `runtimecontrol.New`; it supplies the same Activity, Content, Egress and capture-observation readers and the Runtime's content limits (`internal/runtimecontrol/application.go:63–95`, `internal/serverhost/host.go:248–252`, `internal/desktophost/host.go:269`). Server ordinarily has no client-local identity resolver, but point detail identity lookup uses persisted identity or exact retained protocol evidence, not those local resolvers (`internal/conversationprojection/indexer.go:341–381`). No alternate Server detail database or serializer was found.

The recording catalog is built from the storage layout's explicitly declared archives and current `observations.db`, with the current source last; control state supplies visibility and capture fences (`internal/productruntime/recording_storage.go:96–129`, `internal/productruntime/runtime.go:362–375`). The client supplies an Exchange ID, not a database locator. A first detail read selects the visible lifecycle record and content root. A continuation additionally binds to the source/version/control stamp through the signed cursor. Activity selection prefers a retained completed event over a started event; content selection prefers a terminal response copy and never fills a missing graph from another source (`internal/runtimepersistence/activity_inspection_reader.go:143–205`, `internal/runtimepersistence/content_inspection_reader.go:122–199`).

`getExchange` brackets the independent readers with inspection checks, reads the activity, egress and requested content projection, validates frozen ownership, optionally attaches identity, preflights the DTO, checks expiry/visibility again, and writes JSON (`internal/desktopcontrol/activity_views.go:762–878`). Summary takes a distinct path through `SummarizeExchanges` and, for a capture scope, adds `ReadCaptureObservation` plus `CheckCaptureObservation` (`internal/desktopcontrol/activity_summary.go:32–69`). Thus a successful detail and a summary conflict are not contradictory.

**The live repeated 409 is distinct from unavailable storage or absent content.**

The response mapping is explicit:

| Condition | HTTP / representation |
| --- | --- |
| Inspection visibility or source generation changed | 409 `inspection_changed` |
| Invalid inspection cursor or malformed request | 422 `invalid_request` |
| Missing/unreadable source, invalid optional mirror, other inspection failure | Usually 503 `inspection_unavailable` |
| Exchange activity absent or no longer visible | 404 `exchange_not_found` |
| Activity exists but retained content is absent, expired or not visible | 200 detail with `content.state="not_recorded"` |
| Frozen activity/content mismatch or detail projection/preflight failure | 503 `runtime_unavailable` |

Sources: `internal/desktopcontrol/inspection_response.go:43–64`, `internal/desktopcontrol/activity_views.go:778–866`, `internal/runtimepersistence/content_inspection_reader.go:186–196`. These responses do not delete records.

A summary 409 can be raised by a change between its initial and final control/source checks, catalog maintenance/source identity changes, an expiry boundary reached while reading, or the summary-specific capture selection recheck. See `internal/runtimepersistence/inspection_visibility.go:95–145`, `internal/runtimepersistence/legacy_observation.go:157–169,325–348,465–487`, `internal/runtimepersistence/activity_inspection_projection.go:230–245`, and `internal/runtimepersistence/capture_inspection_control.go:53–63`. A static missing/mismatched optional mirror instead returns **503**, not this 409 (`internal/runtimepersistence/inspection_visibility.go:150–185`).

Repeated summary 409 did **not** reproduce in the fresh steady-state Desktop or Server fixtures. No deployed source stamp, generation, capture-selection snapshot or server-side error phase was available, so the installed instance's specific 409 cause is unresolved. The user-provided successful detail response does not justify declaring the whole archive unreadable. The UI agent independently reproduced that repeated summary conflicts discard otherwise successful concurrent detail loads; that behavior is distinct from the cursor decoder failure and is documented in the UI review. No server cleanup repair or permissive inspection bypass is recommended on the present evidence.

**Why run completion, usage, content, and Exchange status can differ.**

Activity metadata and retained content are independently admitted optional observations (`internal/productruntime/recording_metadata.go:30–59,168–200`, `internal/productruntime/recording.go:491–524`). A completed activity can become readable before its content, and content admission can be refused without reversing model execution. The probe demonstrates the valid transition from `succeeded/not_recorded` to `succeeded/recorded` while the activity remains the same. This makes `contentAvailable` changes meaningful to UI cache invalidation.

Within terminal metadata persistence, usage is still attempted even when the activity write fails (`internal/productruntime/recording_metadata.go:203–245`). Capture lifecycle is separately durable. If only an Exchange's started event is retained, its detail remains `pending`; the reader does not infer an Exchange outcome from an ended Capture (`internal/runtimepersistence/activity_inspection_reader.go:143–205`, `internal/desktopcontrol/activity_views.go:465–470`). This explains possible state combinations without proving that an optional-write failure occurred in the reported run. No saved-content state or pending status alone establishes deletion.

**Result and limits.** The signed-cursor grammar mismatch is a confirmed P1 with actual production-generated JSON and actual Flutter decoder failures. The server accepted its own cursor and the payload was small. The persistent live summary 409 has a clear typed boundary but no reproduced server root cause here. The tests cover fresh current storage, shared composition, both host contracts and ordinary recorded/not-recorded content; they do not exercise the deployed reverse proxy, Web session authorization, migrated live archives, or the user's data. No production changes were made.
