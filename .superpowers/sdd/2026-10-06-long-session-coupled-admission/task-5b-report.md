# Task5B — candidate Runtime/Pipeline integration

Status: owned implementation and final scoped verification GREEN at the replacement freeze; awaiting independent task review. No production default/capacity activation or release claim.

BASE `08d359f04e395d0d603fc71523b4b1c9ad81804c`; branch `fix/long-session-hotfix-20261006`; isolated worktree `/Users/null/Code/github/vibe-agi/vibermate/.worktrees/long-session-hotfix`. One source/compiler/test/formatter owner, no subagents. Cached offline Go1.26.8 and existing Flutter3.41.5/Dart3.11.3 only. No dependencies, live App9666, user data/accounts/trust, other worktree source or release tooling changed.

## Frozen source and exact final commands

Evidence directory: `task5b-evidence.7kDQQd/` beside this report. `final-owned.sha256` includes46 owned Go files and two owned UI test/fixture files; all new files are included. SHA256 `932968f1426d03565069e66653beeffbc66c561a08b90901f031f40c45773924`. Full internal/UI source archive `final-source.tar.gz`: `f032bd0e33e53c235da9de8eee6c298b96f10ace2073f9266aae8a266d978fc3`. `final.patch`: `a6115f7e9bcf947a491df20c5b96aee0037919ceda26b21f7a9abe17d4f77129`. Formatting and diff whitespace checks passed before this freeze.

Normal command (original handle32578, terminal exit0, `final-normal-39.log`):

```sh
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /Users/null/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.8.darwin-arm64/bin/go test -p 1 ./internal/accountselector ./internal/messagetransform ./internal/protocolpath ./internal/anthropicchat ./internal/openairesponses ./internal/responseschat ./internal/exchangecontent ./internal/exchange ./internal/loopbackproxy ./internal/productruntime ./internal/desktopcontrol ./internal/runtimecontrol ./internal/desktophost ./internal/serverhost -count=1 -timeout=10m
```

All13 packages with tests passed. `runtimecontrol` has no test files; its constructor propagation compiled, not an invented test PASS.

Race command and scope fixed before launch:

```sh
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /Users/null/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.8.darwin-arm64/bin/go test -race -p 1 -parallel 1 ./internal/accountselector ./internal/messagetransform ./internal/anthropicchat ./internal/responseschat ./internal/exchangecontent ./internal/exchange ./internal/loopbackproxy ./internal/productruntime ./internal/desktopcontrol -run '^(TestBodyAdmission.*|TestBodyLease.*|TestResourceEnvelopeBoundariesAndOverflow|TestIndependentResponseReservationCoversEscapedSupportedText|TestCandidate.*|TestAdmittedBodyCleanupJoinsEnteredClose|TestChatStream.*|TestTransformedChatAdmission.*|TestDetailWriter.*|TestSanitizeTextPrefilter.*|TestBodyExport.*|TestContextExport.*|TestScriptExport.*|TestTransformRetained.*|TestSelectorPrimitive.*|TestRuntimeCandidate.*|TestPublishedSelector.*|TestToolDecision.*|TestStreaming.*|TestPendingTerminal.*|TestStreamCancellation.*|TestDryRun.*|TestProductRuntime.*Shutdown.*|TestProductRuntimeAppliesInternalShutdownDeadline|TestTurn.*|TestPipeline.*|TestPolicyLimits.*|TestRequestUserAgentOverride.*|TestExecution.*|TestExported.*|TestCompileRejects.*|TestOriginalResponsesAcceptsCanceledReadAfterProvenTerminal|TestTransformedOriginalResponsesAcceptsCanceledReadAfterProvenTerminal|TestManagedChatGPTStreamingPreservesSSEAfterPolicyApproval|TestManagedChatGPTHeaderlessStreamStillRequiresToolApproval|TestUnmodelledResponsesOutputItemsPassOnlyThroughTheToolGate)$' -count=1 -v -timeout=10m
```

Race deliberately selects new mutable ownership/admission/bridge/read/writer/lifecycle contracts and affected existing tool, stream, selector, dry-run and cancellation contracts. Long4111-item Runtime/Store/script cases are normal evidence; the same real Runtime helper has two/three-item Source-drain and approval controls for race. No4111 race claim and no rerun of the unchanged384-page/compression/D-crash campaigns. Race result/handle will be recorded below.

Final UI commands use the exact Go-produced candidate HTTP payload fixture, served by the owned test-only Python adapter. The adapter base64-decodes the actual successful HTTP bodies; it does not manufacture a semantic response or parse/normalize deep Arguments. The producer verifies real Runtime Manager/Store/full/paged HTTP and the consumer traverses real HttpControlApi in VM/Chrome.

```sh
flutter --no-version-check test --no-pub test/candidate_content_http_test.dart test/content_page_api_test.dart test/content_wire_contract_test.dart --dart-define=TASK5B_HTTP_ENDPOINT=<owned-loopback-fixture>
flutter --no-version-check test --no-pub --platform chrome test/candidate_content_http_test.dart --dart-define=TASK5B_HTTP_ENDPOINT=<owned-loopback-fixture>
flutter --no-version-check analyze --no-pub test/candidate_content_http_test.dart
dart format --output=none --set-exit-if-changed test/candidate_content_http_test.dart
```

## First real vertical path and preserved failures

First RED: `TestRuntimeLongSessionCompleteTail`, real Runtime accounts/Store and production Pipeline constructor with only the external HTTP transport redirected to a private server.4111 items,4,147,107B, distinctive complete final sentinel. Original handle61266 exit1: `invalid_client_request at $: message count is invalid`, upstream calls0. `red-source.tar.gz` includes tracked source AND the new test before RED; SHA256 `f83a962e6e49c4cb544641f22e6e1babcf668a8d2c6d6d7c3d0e8bc620ded906`.

After codec/Source wiring, checkpoints02/03 forwarded once but failed real recording at the unchanged2s observer deadline (handles99549/36782, exit1). CPU profile04 (handle74028 exit1) attributed85.93% sampled CPU to sanitizeText regex matching. The narrow fix adds necessary ASCII-byte predicates before the unchanged ordered regex replacements; no redaction, canonical bytes, hash, schema or deadlines changed.

First complete GREEN05: handle68346 exit0; same4111/4,147,107B fixture, upstream once, full Store count and exact tail, no logged diagnostic; source `checkpoint-05-source.tar.gz`, log `checkpoint-05.log`. This was an intermediate checkpoint, not a full acceptance/performance claim. Strengthened07/09/19/26/34 check distinct fixed-width items, every upstream/stored item, exact no-op bytes, persisted response ID/text/known usage and diagnostic count0, managed/original credentials and actual Original Destination.

Other first failures are preserved, not relabeled:

| Log | Original handle / exit | Meaning |
|---|---|---|
| chat-red |26588 /1|Discarded no-op event JSON exhausted cumulative budget at event8.|
| http-gate-10 |26452 /1|Fixture tried heartbeat before real Capture Attach; fixed using Attach, no timeout change.|
| deep-http-12 |38031 /1|Compile setup: unused net/http import.|
| causal-depth-13 |77381 /1|HTTP422 was correct: test combined contentCursor with contentView. Separate real FinalSourceDrain subtest passed.|
| ui16 first archive command |foreground /1|Wrong archive working directory; Flutter did not run. Corrected absolute archive path.|
| matrix-20 |17725 /1|Off fixture incorrectly retained nonzero retention settings.|
| matrix-21 |29460 /1|Synthetic Chat endpoint used an invalid custom realm identity.|
| cross-chat-22 |19525 /1|Official realm did not authorize synthetic Chat protocol. Fixed via real custom endpoint realm==endpoint ID and real Account creation.|
| bridge-red-23 |61077 /1|23,246,744B Unicode Go conversion before body check; context getter visited twice by recursive Export.|
| lease-copy-red-28 |90177 /1|Copying exported BodyLease duplicated refund state. Fixed with shared private state.|
| script-cancel-31 |70230 /1|Test repeatedly invoked stop-the-world Stack(all=true) during real Source recording, causing its unchanged deadline to expire. No production recording fallback/deadline change.|
| transform-overflow-red-36 |89803 /1|Header fields×fields multiplication overflowed before the final sum check.|
| shutdown-red-37 |5173 /1|Generic cleanup continued into Source/Store after body Drain timed out while an owner was still live.|

32 (82452/0) waited for the real Source handoff before stack observation;33 (23222/0) additionally throttled observation at10ms after root's pinned-runtime ruling. Actual VM stack presence, not elapsed time, proves script entry.33's selector matched only the script test; actual Original Destination cases ran in34.

Intermediate successful handles: chat-check01=50056/0; compile06=22846/0 (most packages no tests selected);07=55704/0; affected-normal08=38605/0; writer-chat09=46000/0; http-gate11=62962/0; deep14=42579/0; deep fixture15=48399/0; VM16=36142/0; Chrome17=75760/0; drain18=30484/0; admission19=94497/0; bridge-matrix24=96276/0 (accountselector had no matching tests); bridge-selector25=92630/0 (actual full selector and transform package tests); envelope26=21350/0; boundaries27=97421/0; lease-encoder29=8441/0; cancellation30=42140/0; approval/original34=12518/0; control/writer35=36823/0; shutdown/overflow38=87673/0.

## Ownership and interfaces

- Runtime copies/validates ResourcePolicy before setup and owns one BodyAdmission shared by Handler and Pipeline. Nil candidate wiring preserves legacy constructors/default guards.
- Acquire reserves one fixed complete slot before semantic body read, constructor body copy or Raw scope. One shared wake channel, no per-waiter registry/worker, partial upgrade wait, busy response or occupancy timeout. Controls/auxiliaries bypass the model gate.
- Closed WithBodyLease reserves before NewClientRequest body copy. Shared private lease state prevents value-copy duplication; Pipeline checks exact issuing gate and claims one execution. Caller release cannot refund a live claim. Final observers complete before Pipeline finish; Handler Raw Finalize and entered cancellation Close callback join precede caller release.
- Complete supported input is decoded once before Account selection/acquisition, with admitted semantics reused by start observation/selector/execution. Mapping/Source feasibility occur before acquisition. Candidate Original Destination propagates input failures. Post-transform provider bytes use the actual backend's typed admission seam; no opaque/lexical-only fallback.
- Source observer→NewSourceWithin→Manager.RecordSource→Store.PutSource is synchronous under an immutable borrow. Store, Manager, observer and desktop/server control constructors receive identical copied limits. Candidate reads validate actual typed representations Within; Manager uses bounded presentation folding.
- Chat uses event-local JSON scratch and monotone retained deltas for identities, text, tool cells/arguments, reasoning, usage extension and report notices. TranslationReportBuilder preserves ordered notices. No refund/reset or total-wire cap was introduced. Actual Responses encoder/terminal ownership is tested.
- The closed detail writer counts/preflights before HTTP headers and streams Request/Response via independent raw-leaf writers. Ordinary collections encode one bounded entry at a time. Full content remains full; paged strings/metadata/block_bytes preserve deep leaves and the client's existing2MiB limit. Real post-header write/cancel failure aborts transport.

## Phase-envelope proof and limitations

`resource_envelope.go` derives checked logical response headroom separately from live phases. If P is the admitted response payload occurrence sum, the existing four-step sanitizer F(n)≤7n and sanitized JSON bound6F(3J)+13J≤139J yield retained≤139P+4096B and canonical≤139P+32768B+metadata syntax. Response.Validate bounds all block/top-level extensions together; B≤4096+2×256. Agent identifiers, canonical syntax, evidence entries and metadata are included. Pre-send checks request cost plus this independent reserve, one possible response node, exact per-message≤16384 physical slots and≤4096B agent encoding. No full canonical/history allocation follows from these logical totals.

Source scratch is a maximum over sequential leaves: ordinary29T+4096, function343J+4096 and2qJ+4096 cells, extension92E+8192; J/text/extension clip to their existing individual protocol bounds. These correspond to Source.reserveBlockScratch/extensionViews and MeasureJSON's cell definition. Direct semantic inputs remain within copied finite limits.

Live record phase adds leaf scratch, one32MiB physical row, bounded encoder destination (<two rows), driver copy and bounded transcript/borrowed descriptors. Request envelope takes branch maxima for active decoder/selector/transform/record phases plus simultaneously retained wire/IR owners. Response reserves Feed framing/batch, current scratch, retained decoder/client encoder state, output buffers and terminal/capture/approval copies independently of request slack. It never sums serial discarded SSE preparations or whole historical allocation. Source comments identify the concrete owning representations and checked coefficients.

Compiled transform retention is charged by actual byte ownership before NewTurn/body copy: every saved request input/output, bounded header graph, context and per-Turn metadata. No arbitrary transform count cap; no incremental wait while holding a lease. Every arithmetic product/sum is checked. The old1GiB test placeholders were replaced by derived envelopes plus explicit finite test transform headroom; no production policy has been selected.

These are non-VM application-owned representation bounds, NOT an RSS/allocator/process hard limit. Goja's operator heap and bridge-triggered internal enumeration/trap snapshots remain explicit measured VM scratch under the approved operator-script trust model. Host conversion now bounds primitive UTF16→UTF8, keys, header/context aggregate growth and never recursively Exports an unvalidated object graph. Captured intrinsic/key extraction remains inside the existing script deadline; selector's own non-configurable accountId is checked before conversion. No Goja fork/upgrade/process isolation or extended deadline.

Control graph/output ownership is separate from Source logical currencies and the model pool. Graph counts are preflighted from admitted blocks/agent occurrences before maps/slices; strings borrow the projection except bounded relationship keys. The synchronous HTTP operation owns Store projection/presentation clones, graph, count pass and bounded leaf output until completion/cancellation. No whole huge output buffer is allocated.

## Shutdown budget versus actual cleanup

Candidate Runtime separates the existing overall-budget notification from physical shutdownDone. The same existing executeShutdown goroutine performs cleanup once. At body Drain timeout it reports DeadlineExceeded/StopFailed promptly, retains Source/Store, and waits for actual owners in that same goroutine. After owner drain, dependent cleanup uses WithoutCancel on the exhausted cleanup context (no new deadline) so resources are not leaked merely because the notification budget expired. Other component operation/observer deadlines remain unchanged. The budget callback is stopped/joined before normal cancel and final publication; shutdownErr is published only before physical shutdownDone. Concurrent/repeated calls and late release are tested; the generic cleanupStack and nil-candidate path remain unchanged.

## Mandatory test mapping

| Contract | Tests / evidence |
|---|---|
| Complete real request and record/read |TestRuntimeLongSessionCompleteTail, TestRuntimeLongSessionPolicyMatrix; final normal39.|
| Response/history actual approval |TestRuntimeLongSessionResponseApprovalKeepsHistoryInert; pending real durable approval contains only new_call, no tool bytes before actual DecideApproval; history_call stays inert.|
|4active/4unread, HTTP1/2, Raw/bypass |TestBodyAdmissionHTTPFourActiveFourUnreadWaiters; actual auxiliary HTTP and real attached Capture heartbeat.|
| Actual control HTTP under all slots |TestCandidateRealControlHTTPBypassesFourOccupiedModelSlots uses Runtime's real proxy and control application.|
| Cancel/read/constructor failures |TestBodyAdmissionHTTPCancellationAndReadConstructorFailures, both transports; valid follow-up proves capacity is not leaked.|
| Real final Source, script, finalizer, Close |TestRuntimeLongSessionFinalSourceDrain; TestRuntimeLongSessionScriptCancellationDrainsOwnedSlot; TestBodyAdmissionHTTPRetainsSlotThroughDownstreamFinalize; TestAdmittedBodyCleanupJoinsEnteredClose.|
| Foreign/reused/released/copied leases |TestBodyAdmissionRejectsForeignAndDuplicateConstruction; TestBodyLeaseValueCopiesShareReleaseAndExecutionState; TestBodyAdmissionCallerReleaseCannotRefundExecutingLease.|
| Shutdown actual dependencies |TestCandidateShutdownDeadlineKeepsDependenciesUntilBodyOwnerDrains; TestCandidateNormalShutdownDoesNotSignalBudgetExpiry; existing Runtime deadline/idempotence/durability tests.|
| Envelope at/−1/overflow and large escaped response |TestResourceEnvelopeBoundariesAndOverflow; TestIndependentResponseReservationCoversEscapedSupportedText; TestTransformRetainedEnvelopeRejectsArithmeticOverflow.|
| Transformed Chat tail/unknown charge |TestTransformedChatAdmissionValidatesCompleteTailAndUnknownCost plus real cross_chat_body_edit/cross_chat_invalid_tail Runtime cases.|
| Chat scratch versus growth |TestChatStreamEventScratchDoesNotAccumulate (split/batched); TestChatStreamRetainedGrowthConsumesCreditBeforeAppend (text/notices/reasoning/arguments); TestCandidateChatScratchWithRetainingResponsesEncoder.|
| Host bridge bounds and old parity |export_admission_test, selection_admission_test, full transform/selector25 plus final normal; Unicode, cycle, getter, proxy, poisoned globals, aggregate limits, membership/interruption.|
| Deep response→next history / HTTP failures |TestCandidateDeepResponseAndNextHistoryThroughCompleteAndPagedHTTP, TestDetailWriterPreservesOrdinaryBytesAndDeepLeaf; actual full/paged HTTP and post-header abort/pre-header error.|
| Owned client navigation |candidate_content_http_test.dart, actual VM/Chrome HttpControlApi over Go-produced response bytes; no parsing of nested Argument strings.|

## Remaining gates and staged concerns

Task6 whole-Runtime peak memory/latency, representative VM scratch, supported-domain capacity selection and concurrent control safety remain required. Task7 production activation, pinned official client continuation, upgrade/backup/restart/rollback acceptance and the formal release gates remain required. No default switch, publication or full-Goal completion is claimed.

The explicitly unadopted oversized ClientIdentity/technical-list redesign and corrupt metadata preallocation seam remain tracked by root's full Goal. This task preserves complete identity/list contracts and ordinary bytes; it does not claim those independent issues solved or waive them.

Task6/7 and independent review remain unverified later gates; they are not substituted by the results below.

## Final verification continuation

Initial final race40, original handle66759, terminal exit1: a real race in BodyAdmission.Acquire's lock-free `changed==nil` zero-value check against notify/BeginShutdown's locked channel replacement. Only this gate read moved under the existing mutex. All other selected packages passed; the nine-package command as a whole FAILED and is not relabeled a pass. No timeout changed.

The initial freeze is preserved. Replacement `final2-owned.sha256` SHA256: `a9806bd6c2842f9d71c73def7de4ce0b79cc905613bae4170a12ad14e24a4300`; `final2-source.tar.gz`: `f26875ca2bcd1e6210b978dec9de33c7e18961989e821ff62c9eba473b0418a9`. Only resource_admission.go changed; its hash is `7ef911ad7ddffe2f81803729aacf515a0fa1fa666a6cfb2d89d353e84cb7a553`. The tracked-only patch hash is unchanged because that new file is bound by the archive and explicit manifest, not git diff's tracked-file output.

Direct gate regression41: original63603, exit0, `go test -race ./internal/exchange -run '^TestBodyAdmissionFourSlotsWaitCancelAndShutdown$' -count=1 -v` with the pinned offline prefix above.

Affected replacement normal42: original78479, terminal exit0. Exact command is the same pinned offline prefix plus `test -p 1 ./internal/exchange ./internal/loopbackproxy ./internal/productruntime ./internal/desktopcontrol -count=1 -timeout=10m`.

Affected replacement race command is fixed before launch: same pinned offline prefix plus `test -race -p 1 -parallel 1 ./internal/exchange ./internal/loopbackproxy ./internal/productruntime ./internal/desktopcontrol`, the identical full `-run` regular expression recorded above, and `-count=1 -v -timeout=10m`. Unaffected accountselector/messagetransform/anthropicchat/responseschat/exchangecontent selected-race results remain their source-bound40 results, not a claim that all their tests ran.

Replacement race43: original74541, terminal exit0, all four selected packages passed with no race warning. Combined final evidence is the five unaffected selected-package results from40 plus the four corrected selected-package results from43; the failed40 command itself remains FAIL. All selected packages matched tests. The normal long4111 cases and the small real Runtime race controls retain their distinct stated scopes.

Final Go HTTP producer44: original5456, exit0, same frozen source and real complete/paged HTTP failure tests. It wrote a new `final2-http-fixture.json`, SHA256 `c5812b7e177d4e9c2e3de02fc09466ce88c809a75d6751f8c00bd0ed264ece4b`; the older fixture was preserved. The exact producer command is the pinned prefix with `TASK5B_HTTP_FIXTURE=<this-worktree>/.superpowers/sdd/2026-10-06-long-session-coupled-admission/task5b-evidence.7kDQQd/final2-http-fixture.json` and `test ./internal/desktopcontrol -run '^TestCandidateDeepResponseAndNextHistoryThroughCompleteAndPagedHTTP$' -count=1 -v`.

The original fixture server59159/PID82453 was explicitly terminated after its earlier UI gates and polled to terminal143. A new owned server87430 loaded44's new file at startup and served `http://127.0.0.1:57186`; this avoids stale in-memory fixture data. Final VM45 (22835/0) passed six tests across candidate_content_http_test.dart, content_page_api_test.dart and content_wire_contract_test.dart. Final Chrome46 (64216/0) executed and passed the candidate navigation test. Both commands above used `--dart-define=TASK5B_HTTP_ENDPOINT=http://127.0.0.1:57186`, with `CI=true FLUTTER_SUPPRESS_ANALYTICS=true DART_SUPPRESS_ANALYTICS=true`. No candidate test was skipped. Analysis47 (67251/0) found no issues. Final Dart format check was foreground exit0, one file/zero changes.

Server87430/PID44900 was then explicitly terminated and polled to terminal143. Both143 exits are intentional synthetic-service cleanup, not test failures. All source/compiler/test/formatter/service handles are now0.

Final source checksum verification against final2-owned.sha256 was quiet exit0, and diff whitespace verification passed. Every saved log is individually hashed in `final-evidence-logs.sha256` (manifest SHA256 `793785c3211b645c4180e7cad81af95bf6dffc76831b9783d84f55efebff216a`). Source archives, intermediate failures and original tool handles remain preserved. The contemporaneous first-count/Chat/bridge/value-copy/overflow/shutdown RED archives include their new tests. Some development runs used the recorded edit/tool sequence rather than an additional independent archive immediately before the command; they are intermediate evidence, not substituted for the exact final freezes.

Self-review covered lease-copy authority, lock ordering, observer/Handler/Close ownership, deferred dependency cleanup, precredential complete input/mapping, provider-dialect readmission, immutable Source lifetime, nil legacy behavior, independent response headroom, checked arithmetic, closed HTTP serialization and the scoped metadata/VM limitations. The concrete defects found during this pass have preserved RED→GREEN evidence. No release or practical capacity assertion follows from the conservative symbolic test policy.

Commit identity is supplied in the final handoff; this report and the48 owned source/test files are the only intended staged paths.

## Owned source inventory

- `internal/accountselector/selection_admission_test.go`
- `internal/accountselector/selector.go`
- `internal/anthropicchat/chat_stream_ownership_test.go`
- `internal/anthropicchat/messages_path.go`
- `internal/anthropicchat/path.go`
- `internal/anthropicchat/provider_admission.go`
- `internal/anthropicchat/provider_admission_test.go`
- `internal/anthropicchat/stream.go`
- `internal/desktopcontrol/activity_views.go`
- `internal/desktopcontrol/candidate_control_gate_test.go`
- `internal/desktopcontrol/candidate_depth_http_test.go`
- `internal/desktopcontrol/detail_writer.go`
- `internal/desktopcontrol/detail_writer_test.go`
- `internal/desktopcontrol/handler.go`
- `internal/desktopcontrol/router_integration_test.go`
- `internal/desktophost/host.go`
- `internal/exchange/content_admission.go`
- `internal/exchange/contracts.go`
- `internal/exchange/dry_run.go`
- `internal/exchange/pipeline.go`
- `internal/exchange/resource_admission.go`
- `internal/exchange/resource_admission_test.go`
- `internal/exchange/resource_envelope.go`
- `internal/exchangecontent/canonical.go`
- `internal/exchangecontent/manager.go`
- `internal/exchangecontent/page_manager.go`
- `internal/exchangecontent/sanitize_prefilter_test.go`
- `internal/exchangecontent/types.go`
- `internal/loopbackproxy/body_admission_http_test.go`
- `internal/loopbackproxy/body_close_internal_test.go`
- `internal/loopbackproxy/handler.go`
- `internal/loopbackproxy/handler_integration_test.go`
- `internal/messagetransform/engine.go`
- `internal/messagetransform/export_admission.go`
- `internal/messagetransform/export_admission_test.go`
- `internal/productruntime/account_operation_test.go`
- `internal/productruntime/body_shutdown_test.go`
- `internal/productruntime/builders.go`
- `internal/productruntime/long_session_test.go`
- `internal/productruntime/options.go`
- `internal/productruntime/runtime.go`
- `internal/protocolpath/path.go`
- `internal/responseschat/path.go`
- `internal/responseschat/responses_path.go`
- `internal/responseschat/stream_ownership_test.go`
- `internal/runtimecontrol/application.go`
- `ui/flutter_app/test/candidate_content_http_test.dart`
- `ui/flutter_app/test/fixtures/candidate_content_http_server.py`

## Task5B fix round1 — independent review findings

Fix BASE `fdf0fa4dfd5d2ec6c94833527ff53d39318fe29a` (reviewed product source66f72e3 plus release-plan documentation only). Both Important findings in task-5b-review.md addressed; independent re-review remains pending. Owned source is exactly `internal/messagetransform/{engine.go,export_admission_test.go}` and `internal/exchange/{dry_run.go,dry_run_test.go}`. No dependency, default/capacity, compression policy, deadline, Runtime/Store/UI or release-tooling source changed; no subagents or live services/data were used.

Primitive-wrapper parity: recognize only pinned Goja's native internal Number/Boolean classes before the primitive export switch. Its primitiveValueObject.export returns the scalar payload directly, ignoring own graph properties; ClassName is not the script-controlled Symbol.toStringTag. This restores Number/Boolean values (including false, fractions and negative zero) without invoking poisoned valueOf/toString/Symbol.toPrimitive, traversing wrapper cycles/getters, or restoring recursive graph Export. Existing finite-number, structure, Unicode, getter/proxy and cancellation guards remain. String wrappers retain rejection; boxed BigInt retains its pre-existing empty Object view.

Candidate Original Destination dry-run now uses the same post-transform bounded content decoding and actual backend admission as execution. Changed logical bodies fail immediately on supported semantic/resource errors; it does not fall back to validating original bytes. Nil candidate wiring is unchanged. The sole model slot is occupied during the new dry-run tests, proving dry-run remains separate control work.

Evidence directory: `task5b-fix1-evidence.0HJEyV/`.

| Run | Original handle / terminal exit | Actual result | SHA256 log |
|---|---|---|---|
|red-01|51549 /1|Both real findings reproduced; also exposed incorrect new-test expectations for boxed BigInt and gzip script behavior.|310a677418144a32724798bb3e9bb907b903374445ee6143ed885c849fc51019|
|red-02|71820 /1|Number/Boolean still failed and invalid-tail/resource dry-run still succeeded before fixes. BigInt control corrected; gzip empty-statement expectation still wrong.|e5475f0b55cf7fdbbfb83cbba1557324a07b6690329e89e5c7a78654845ff721|
|green-03|27233 /1|Both product fixes passed their targeted assertions, but the gzip test expectation still failed; overall FAIL despite filename.|7fbdd6a1163dbb46e1aa34ccd5de9784f38941f96efbf9f0c335f1dc11c1f6eb|
|green-04|10829 /0|Wrapper parity and dry-run controls passed after correcting test-only expectations.|a5ef2cba71d4ada5f417dc867d4bd39a7ed02cdec32f1842c5e9176d1e6439aa|
|final-normal-05|92589 /0|Both complete affected packages passed on frozen fix, including malformed transformed encoding control.|482c87f9dbfac0323d88953119401f3c7a41889c7db3abc6f757f3048f51d946|
|final-race-06|44803 /0|Both packages matched and passed the named bridge/dry-run selection; no race warning.|0dd62ab64e30282659bfbb5ed0aba4468f6e166279159bbfd5bbb412741902a7|

The genuine RED archives include both new behavioral tests before production fixes: `red-source.tar.gz` SHA256 `37342fe69ec12ab3de0da46c4b0d8a72e7a02f0a14be0c0ba93251f360ea1f7f`; corrected-expectation `red2-source.tar.gz` SHA256 `80120a8a4a3610aee187f82cc9c1f6587f6b739ec9dbf9638354c418b4a094c8`. The gzip distinction was verified against unchanged applyRequestMessageTransform: no configured Request program preserves compressed wire; an active empty-statement or header-only Request script returns logical encoding. Tests now assert each behavior separately; production compression was not altered.

Final four-file manifest `final-source.sha256`: `d5fe764b9314c2004ae317533889eee5bd120ae41432cac61cc5eb430e854a9c`; archive `final-source.tar.gz`: `75f0a6062caaab10065301d5e5bf299995b0d364dd4888fc846d06ed0c8456bf`; patch `final.patch`: `6fa4023b85031f15f8c701fae212fb9586a212f90d2bcc223ae9b1749f96e49a`. Archive includes go.mod/go.sum and all four owned source/test files. All four manifest entries were rechecked after final tests; gofmt -l was empty and git diff --check passed.

All runs used the pinned offline prefix recorded above. RED01/02 and GREEN03/04 used `test -p 1 ./internal/messagetransform ./internal/exchange -run '^(TestContextPrimitiveWrapperParity|TestCandidateOriginalDryRunReadmissionParity|TestLegacyOriginalDryRunKeepsExistingTransformContract)$' -count=1 -v`.

Final normal command:

```sh
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /Users/null/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.8.darwin-arm64/bin/go test -p 1 ./internal/messagetransform ./internal/exchange -count=1 -timeout=5m
```

Final race selection was fixed before launch:

```sh
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /Users/null/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.8.darwin-arm64/bin/go test -race -p 1 -parallel 1 ./internal/messagetransform ./internal/exchange -run '^(TestContext.*|TestScriptExport.*|TestBodyExport.*|TestTransformRetained.*|TestTurnPreservesValidUnicodeBody|TestTurnDistinguishesReplacementCharacterFromUnpairedSurrogates|TestTurnStillRejectsInvalidUTF8Input|TestTurnRejectsInvalidOutputsAndLeavesInputImmutable|TestExecution.*|TestExportedGetter.*|TestCompileRejects.*|TestPolicyLimitsScriptAndBodySizes|TestDryRun.*|TestCandidateOriginalDryRunReadmissionParity|TestLegacyOriginalDryRunKeepsExistingTransformContract)$' -count=1 -v -timeout=5m
```

New controls cover valid/invalid wrapper parity, ignored poisonous own properties/coercion, non-finite numbers, candidate invalid final role, unknown-field resource expansion, invalid transformed encoding, valid edited tail, unconfigured gzip byte preservation, configured empty/header-only script behavior, real Execute/dry-run digest/refusal parity, and nil legacy wiring. Existing overflow/getter/Unicode/cycle/proxy/aggregate/cancellation controls passed in the scoped race selection. No unchanged Runtime4111, Store, UI/Chrome, compression or fuzz campaign was repeated.

Self-review and staged-diff inspection: the production diff is confined to safe wrapper classification and the missing candidate original dry-run guard; no general export/decoder abstraction or new authority was added. The initial staging command returned exit1 with the existing ignored-parent warning for the report; the four source files and report were present in the index, then the exact owned report was explicitly force-added and staged checks passed. No source or test failure was hidden by that staging correction. No remaining issue found within these two findings. Handles0; the owned GREEN fix commit is identified in the handoff. Task6/7 and release gates remain unchanged.
