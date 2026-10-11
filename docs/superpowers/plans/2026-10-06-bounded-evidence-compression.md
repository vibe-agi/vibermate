# Bounded Evidence Compression Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove machine-core-count-dependent evidence compressor workspace growth without changing logical evidence, compression level, on-disk codecs or data compatibility.

**Architecture:** The existing shared zstd factory remains the single compression module for Raw and content blocks. Explicitly bound its encoder worker pool while retaining BestCompression, existing identity fallback and exact plaintext digest semantics. Tests cross the real factory and actual repositories; no new production interface or configuration surface.

**Tech Stack:** Go1.26.8/module1.26.0, existing klauspost/compress v1.19.1, SQLite schema1.

**Spec:** `docs/superpowers/specs/2026-10-06-long-session-hotfix-design.md`

## Global Constraints

- Work only in `.worktrees/long-session-hotfix`, branch `fix/long-session-hotfix-20261006`; preserve original and production-readiness worktrees and their edits.
- No user App, database, Account, trust, external network, installed-client or release operations. Synthetic private data only.
- Keep zstd.SpeedBestCompression, identity fallback, old zstd decoding, plaintext digests, retention and all schema/record/count/wire limits unchanged.
- No compression disabling, new dependency, public option, production test hook, global cache framework or general scheduler.
- One source implementer and compiler/test process at a time; preserve first failures, original handles, frozen source hashes and actual terminal exits.
- Go prefix: `env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly /usr/local/go/bin/go`.
- This is a necessary resource fix within the approved long-session hotfix, not removal of4096 or permission to publish a partial branch.

## Established evidence

Real isolated Store calibration atf874da6 saved3998→4000 messages/~3.98MB, read every message and166pages, and derived the correct3999-message prefix. First Put allocated1,008,710,696B; whole diagnostic MaxRSS792,739,840B. That does not prove all this allocation grows with history: the compressor's cold start had not been separated.

The actual pinned v1.19.1 NewWriter defaults concurrency to runtime.GOMAXPROCS; first EncodeAll initializes every Best encoder. A separate real compressor experiment onGOMAXPROCS18,4000~1KiB blocks, measured current954,018,224B allocation versus56,877,360B with the same Best level and concurrency1. Both stored402,803B and every block decoded byte-identically. This supports the specific factory change, not a general compression-ratio or whole-process memory bound. Source/report evidence remains in the prior plan's ignored diagnostics; do not rerun those historical modes as a setup ritual.

### Task 1: Bound the real shared evidence encoder and preserve durable roundtrips

**Files:**
- Modify `internal/runtimepersistence/evidence_body.go`: only mustZstdWriter's construction options/comment.
- Create `internal/runtimepersistence/evidence_compression_resources_test.go`: cold-start allocation, actual roundtrip/concurrency and private Store reopen coverage.

**Interfaces:** Consume the existing private `mustZstdWriter() *zstd.Encoder`, existing shared `bodyEncoder`, `Store.ExchangeContentRepository()` and Raw repository test helpers. Produce no new product interface; the existing factory still returns the same compatible encoder.

- [x] **Step1: Establish a genuine real-factory allocation RED.** Use a subprocess of the test binary so no other test/goroutine contributes allocations or observes a changed GOMAXPROCS. A test-owned helper flag selects the child. Inside the child set runtime.GOMAXPROCS(8), construct synthetic input before sampling, forceGC, snapshot runtime.MemStats, then call the actual `mustZstdWriter()` and EncodeAll for4000 deterministic~1KiB blocks. Keep outputs alive; sample before any decoder verification. Assert allocated bytes≤96MiB, a generous fixture-only regression boundary separating one measured Best workspace from the eight implicit workspaces. This is NOT a production request budget.

```go
// Within the isolated helper process, no t.Parallel:
runtime.GOMAXPROCS(8)
input := []byte(`{"kind":"text","text":"` + strings.Repeat("x", 900) + `"}`)
runtime.GC()
var before, after runtime.MemStats
runtime.ReadMemStats(&before)
encoder := mustZstdWriter()
output := make([][]byte, 4000)
for i := range output { output[i] = encoder.EncodeAll(input, nil) }
runtime.ReadMemStats(&after)
allocated := after.TotalAlloc-before.TotalAlloc
if allocated > 96<<20 { t.Fatalf("bounded evidence compression allocated %d bytes", allocated) }
runtime.KeepAlive(output)
```

Parent uses exec.CommandContext with a30-second deadline, same test binary, exact helper test selection and an appended task-specific environment flag; never modify HOME/CODEX_HOME or user state. Report child output on failure without secrets. Every child is reaped. A compile/setup failure is not the required RED. The current factory must actually exceed this generous bound; if not, inspect source/options/fixture instead of tightening the assertion to force failure.

Run and preserve RED before production edits:

```sh
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly /usr/local/go/bin/go test -count=1 -run '^TestEvidenceCompressionColdWorkspaceIsBounded$' -v ./internal/runtimepersistence
```

- [x] **Step2: Make the single factory correction.** Retain the existing level and add exactly the bounded encoder-concurrency option:

```go
encoder, err := zstd.NewWriter(
    nil,
    zstd.WithEncoderLevel(zstd.SpeedBestCompression),
    zstd.WithEncoderConcurrency(1),
)
```

Document that EncodeAll remains safe for concurrent callers but the expensive workspace count is fixed, not coupled to machine CPU count. Do not change the shared decoder, fallback or storage format. Rerun the original test unchanged for GREEN.

- [x] **Step3: Verify actual content and Raw compatibility.** In the new test file exercise8 concurrent callers of a real bounded factory with per-caller distinct payloads; store encoded results and decode after callers join, proving all bytes and SHA256 identities equal original input. Include empty input, text, binary bytes and representative small/large synthetic blocks; each payload is≤1MiB and total input≤8MiB. The factory has no context interface; don't claim aborting compression mid-frame.

```go
decoded, err := bodyDecoder.DecodeAll(encoded, make([]byte, 0, len(want)))
if err != nil || !bytes.Equal(decoded, want) { t.Fatal("evidence compressor changed plaintext") }
if sha256.Sum256(decoded) != sha256.Sum256(want) { t.Fatal("plaintext digest changed") }
```

Use existing actual temporary Store helpers for one Raw envelope body and one content record, with repeated data that genuinely stores aszstd and an incompressible small payload that exercisesidentity fallback. Persist, close, reopen, read and compare complete bytes/records and expiry behavior; use precise SQL inspection only to prove which existing codec was exercised. No mocked repository or fake codec. Keep raw/content ownership separate, using their existing authorized test fixtures. If existing tests already cover a specific end-to-end property, cite and include them in the final whole-package run; do not duplicate assertion-only copies without a missing behavior.

- [x] **Step4: Freeze both paths and run covering gates.** Focused regressions first, then the whole runtimepersistence package once and a bounded focused race run. Leave all existing Raw/content/reopen/hash/retention tests intact.

```sh
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly /usr/local/go/bin/go test -count=1 -run '^TestEvidenceCompression' -v ./internal/runtimepersistence
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly /usr/local/go/bin/go test -count=1 ./internal/runtimepersistence
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly /usr/local/go/bin/go test -race -count=1 -run '^TestEvidenceCompression' -v ./internal/runtimepersistence
git diff --check
```

No opt-in live/packaged/client flags. Preserve failures and original handles; do not start another suite because an observation window expired. Report allocation figures separately under race; the same96MiB regression boundary must remain enabled. Do not claim whole-repository or long-session-release coverage.

- [x] **Step5: Commit and request independent review.** Stage only the two owned paths after inspecting the staged diff. Commit `fix: bound shared evidence compression workspaces`. Record exact dispatch BASE/HEAD, source hashes, every RED/GREEN command/handle/result, first setup failures, raw logs, clean status and remaining limitations in the task report. Controller dispatches one independent spec+quality reviewer; implementer never spawns agents. The full coupled request/record implementation remains mandatory afterward.

## Plan self-review

One task owns both the existing factory option and its behavior tests. The tests exercise the actual factory, not a re-created options list; decoder roundtrip and repository checks preserve hashes/formats. The96MiB number is explicitly a test-case discriminator with an eight-core helper, not a new product capacity claim. The user-approved scope covers this necessary resource correction without changing data or permissions. No new execution-mode confirmation is needed; continue using one implementer and independent review.

## Verified unit closure — 2026-10-06

Owned commit `2c3b87343aab7322df005f1c24eb06fe8e135479`, exact BASE `ccc476755702e6e4930b3be8fdc28814819a08b4`. Independent spec/quality review Approved, no Critical/Important/Minor findings. Root read the complete report/review, all five raw logs and verified both frozen source/five log hashes. Actual factory RED426,375,608B→GREEN56,966,960B; race56,972,280B. Four focused tests, whole runtimepersistence and scoped race passed on frozen source.

Source-matched synthetic Store replay after this change still reconstructs all4000 messages and166pages, with3999-message incremental prefix. Whole-child peak decreased from792,739,840B to189,988,864B and first Put cumulative allocation from1,008,710,696B to107,503,344B; wall-time samples3.46s/3.48s do not establish a speed gain. These are isolated Store results, not whole Pipeline/concurrency capacity or4096 acceptance. All count/storage/wire limits remain unchanged; continue the required coupled plan before publication.
