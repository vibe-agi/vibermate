# Long-session Linear Notice Accumulation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove quadratic translation-notice copying from existing request processing as the first necessary implementation unit of the approved stable long-session hotfix.

**Architecture:** Preserve immutable TranslationReport at existing caller interfaces; a zero-value owned TranslationReportBuilder accumulates through two methods and snapshots once on return. Existing real codecs use the builder only where input-length loops otherwise copy the growing report repeatedly. This changes resource behavior, not wire, validation, permission or record semantics.

**Tech Stack:** Go1.26.8 (module go1.26.0), existing Go codecs and tests, no dependencies.

**Spec:** `docs/superpowers/specs/2026-10-06-long-session-hotfix-design.md`

## Global Constraints

- Stable baseline `c61f8c6b5e2196da8e99010c169d4e3c17797933`; work only in `.worktrees/long-session-hotfix`, branch `fix/long-session-hotfix-20261006`.
- No development-branch MCP/schema2/global-egress code, original-worktree writes, user App/DB/trust/account access, network fixtures or release action in this plan.
- Keep the immutable TranslationReport contract, including exact notice order, codes, paths and partial reports on failures.
- No report deduplication, omission, pooling, unsafe sharing or skipped validation.
- This unit leaves4096/16MiB/32MiB and all record/storage guards unchanged. Its completion is not a4096 fix or release readiness claim.
- One source implementer and one compiler/test job at a time. Use `env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly /usr/local/go/bin/go` for all Go commands; capture original terminal results and unique direct logs.
- Use only synthetic input. Preserve first setup/compile failures separately from genuine behavioral RED. Do not loosen an allocation assertion merely because it catches the old behavior.
- No new generic adapter, registry, pool or heap-limiter framework. Place owned notice behavior in protocolcore beside the existing immutable type.

## File structure and interfaces

| File | Responsibility |
| --- | --- |
| `internal/protocolcore/translation_report_builder.go` | Owned ordered accumulation and immutable snapshots; no transport or policy. |
| `internal/protocolcore/translation_report_builder_test.go` | Aliasing, order, independent snapshots, empty inputs. |
| `internal/openairesponses/request_decode.go` | Use accumulation in decodeClientRequest and decodeInclude's variable-length loops; preserve every return's original report prefix. |
| `internal/openairesponses/request_notice_growth_test.go` | Real strict/compatible decode order, error prefix and allocation-growth regression. |
| `internal/anthropicchat/request_decode.go` | Use accumulation in decodeClientRequest, decodeSystem and decodeMessage's variable-length loops. |
| `internal/anthropicchat/request_encode.go` | Use accumulation in EncodeProviderRequest and messageEncodingReport loops. |
| `internal/anthropicchat/request_notice_growth_test.go` | Actual nested cache/citation notices, error prefix and Chat-encode ordering/growth. |

Existing `TranslationReport.Merge` remains an immutable interface. It may stay unchanged: eliminate growing-prefix callers, not globally change return ownership. Fixed-size helper merges are not a reason to rewrite unrelated files.

### Task 1: Remove request-length notice amplification without semantic changes

**Files:** Create/modify only the seven paths in the table above. Read unchanged `protocolcore/types.go`, `openairesponses/request_decode_test.go`, `anthropicchat/codec_test.go`, `anthropicchat/unknown_field_test.go`, and `responseschat/responses_path.go` to preserve the actual contracts. Do not edit them to relax existing assertions.

**Interfaces:**

```go
// New module surface; zero value is usable. A builder is owned by one call,
// not copied after use and not shared across goroutines.
type TranslationReportBuilder struct { notices []TranslationNotice }
func (builder *TranslationReportBuilder) Append(report TranslationReport)
func (builder *TranslationReportBuilder) Build() TranslationReport
```

Consumes existing `TranslationReport`, `TranslationNotice`, `NewTranslationReport`, actual codec request/response signatures. Produces the same immutable reports for existing callers. Append preserves all input notices in order, including duplicates; Build copies for an immutable snapshot. Reusing the builder afterward or mutating Notices() output cannot affect previous reports. Empty inputs contribute nothing. No mutable exported slice or release/reset method.

- [ ] **Step1: Write and run the real-code allocation regression before product edits.** In the new Responses test file, build deterministic N=512 and N=2048 valid assistant items, each with `id`, `phase:"commentary"` and a short output_text, followed by a user tail sentinel counted withinN. Use actual DecodeClientRequest and DecodeCompatibleClientRequest separately. Warm up each codec; measure the allocations of a fresh decode, not fixture construction, via a serial test helper. Each decode must succeed under the unchanged4096 guard and have exactly the independently expected notices; no error-return shortcut.

```go
func measuredBytes(t *testing.T, run func()) uint64 {
    t.Helper()
    runtime.GC()
    var before, after runtime.MemStats
    runtime.ReadMemStats(&before)
    run()
    runtime.ReadMemStats(&after)
    return after.TotalAlloc - before.TotalAlloc
}
// In a non-parallel focused test, with fixtures/codecs already created:
small := measuredBytes(t, decode512)
large := measuredBytes(t, decode2048)
if large > 6*small+(8<<20) {
    t.Fatalf("request notice allocation grows faster than bounded linear work: small=%d large=%d", small, large)
}
```

Assert every item notice code/path/order using a hand-derived pattern such as item i's identity then phase; assert tail text and complete message count. Verify which kinds actually emit notices before deriving fixture expectations. For error preservation, insert an invalid role at a known late position and require exactly the preceding valid items' notices; the invalid item's not-yet-appended report must not appear. Test both strict and compatible decode. Do not mark syntax/missing-symbol failure as this RED.

Run focused real-behavior RED:

```sh
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly /usr/local/go/bin/go test -count=1 -parallel=1 -run '^TestResponsesRequestNotice' -v ./internal/openairesponses
```

Preserve measured bytes and genuine growth failure. If this exact assertion does not fail on stable source, investigate fixture/report production and bring the evidence to the controller; do not introduce an artificial always-failing expectation.

- [ ] **Step2: Add the builder ownership tests before its implementation.** Missing new symbols are setup evidence, not the behavioral RED fromStep1. Use literal notices A/B/C and prove input slices, returned notice slices and separate built snapshots are independent.

```go
seed := []TranslationNotice{{Code: NoticeMessageItemIdentityNotForwarded, Path: "$.input[0].id"}}
input := NewTranslationReport(seed...)
var builder TranslationReportBuilder
builder.Append(input)
first := builder.Build()
seed[0].Path = "mutated caller input"
observed := first.Notices()
observed[0].Path = "mutated returned slice"
builder.Append(NewTranslationReport(TranslationNotice{Code: NoticeMessagePhaseNotProjected, Path: "$.input[0].phase"}))
second := builder.Build()
wantFirst := []TranslationNotice{{Code: NoticeMessageItemIdentityNotForwarded, Path: "$.input[0].id"}}
wantSecond := []TranslationNotice{
    {Code: NoticeMessageItemIdentityNotForwarded, Path: "$.input[0].id"},
    {Code: NoticeMessagePhaseNotProjected, Path: "$.input[0].phase"},
}
if !slices.Equal(first.Notices(), wantFirst) || !slices.Equal(input.Notices(), wantFirst) {
    t.Fatal("caller mutation or later append changed an immutable report")
}
if !slices.Equal(second.Notices(), wantSecond) {
    t.Fatal("builder lost or reordered notices")
}
```

Add a many-append case with all exact entries and multiple snapshots; the first snapshot must not change after later appends. Use literal expected entries for the small ownership cases, not a second invocation of the builder under test.

- [ ] **Step3: Implement the minimal accumulator and integrate Responses.** The core shape is:

```go
func (builder *TranslationReportBuilder) Append(report TranslationReport) {
    builder.notices = append(builder.notices, report.notices...)
}
func (builder *TranslationReportBuilder) Build() TranslationReport {
    return NewTranslationReport(builder.notices...)
}
```

In each selected codec function, accumulate all notices in one owned builder and call Build at existing return points. A common mechanical mapping is:

```go
var report protocolcore.TranslationReportBuilder
report.Append(itemReport)
// On an existing early error:
return protocolcore.Request{}, report.Build(), err
// On success:
return request.Clone(), report.Build(), nil
```

Seed Anthropic's initial unknownReport with Append, never drop it. Do not Build after each loop iteration or call Notices() on the accumulated prefix; either would recreate growing-prefix copies. Preserve when the current item gets appended relative to errors exactly. Do not alter input validation or protocol-specific fields while converting the report carrier.

Run focused GREEN and protocolcore ownership tests:

```sh
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly /usr/local/go/bin/go test -count=1 -parallel=1 -run 'TestResponsesRequestNotice|TestTranslationReportBuilder' -v ./internal/protocolcore ./internal/openairesponses
```

- [ ] **Step4: Preserve the same contract across existing Anthropic/Chat request loops.** Add actual-code tests for512/2048 Anthropic messages each with text cache_control, nested512/2048 cache-bearing system/message blocks within existing limits, and Chat encoding of valid history messages containing tool calls with item identity notices. Exercise decode, encode and late-error report prefixes through actual existing public codec interfaces; don't assert a fake encoder or a hand-built report as codec evidence.

```go
// Synthetic Anthropic message shape, no network/provider call:
message := json.RawMessage(`{"role":"user","content":[{"type":"text","text":"synthetic","cache_control":{"type":"ephemeral"}}]}`)
// Root has model:"fixture", max_tokens:64 and the complete messages array.
// Cache notices must name $.messages[i].content[0].cache_control in order.
```

Before changing these loops, preserve genuine allocation-growth REDs using the same serial measurement pattern, or document already-linear actual behavior for a selected path. Convert only the selected variable-length functions; keep fixed-size reasoning helpers and response streaming outside scope. Test native-compatible and cross-dialect behavior without changing their supported input sets. Run the actual `EncodeProviderRequest` output through JSON decoding and check all output messages/tool entries and the exact loss report, not just report length.

```sh
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly /usr/local/go/bin/go test -count=1 -parallel=1 -run '^TestAnthropicRequestNotice' -v ./internal/anthropicchat
```

- [ ] **Step5: Freeze the seven-file source and run covering verification.** First focused final regressions, then affected whole packages once, then the bounded named regressions under race. No unrelated whole-repo/Flutter tests in this unit.

```sh
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly /usr/local/go/bin/go test -count=1 ./internal/protocolcore ./internal/openairesponses ./internal/anthropicchat ./internal/responseschat
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly /usr/local/go/bin/go test -race -count=1 -parallel=1 -run 'TestResponsesRequestNotice|TestAnthropicRequestNotice|TestTranslationReportBuilder' ./internal/protocolcore ./internal/openairesponses ./internal/anthropicchat
git diff --check
```

Report memory-ratio assertions under race separately; preserve a failure instead of disabling them. The unit's result is lower allocation growth plus identical semantics, not peak-memory certification. Record actual elapsed/allocated bytes for512/2048/4000 and errors unchanged at4111;4111 must still hit the existing guard because removing it belongs to the next coupled admission/record unit.

- [ ] **Step6: Commit only this unit and hand off for independent review.** Stage only the seven named owned files, inspect the staged diff, then commit `fix: accumulate request translation notices linearly`. The report records exact BASE/HEAD, all paths, original RED and final GREEN commands/output, actual measurements and first failures, remaining limits, and handles=0. No commit-amend/reset or other-owner files. Controller generates one exact BASE..HEAD review package for spec and quality review; the implementation author does not spawn reviewers.

## Plan self-review and handoff

The single batched task owns the shared builder and all chosen consumers, avoiding independently dispatched changes to the same request files. The two-method interface matches every consumer; immutable reports, partial failures, order and full semantic output have actual tests. Existing4096/record guards remain deliberately unchanged until the next required implementation unit; this plan cannot be the release endpoint. Root records the source/interface preflight table in this plan's SDD ledger, then executes with one fresh implementer and an independent reviewer, as already requested by the user. No additional execution-mode approval is needed.
