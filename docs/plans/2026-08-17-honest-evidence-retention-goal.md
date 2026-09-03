# Honest Evidence Retention

Status: implementation delivered; one design divergence unmet (first-enable
disclosure), one design amendment owed to the design owner (06 §8.1 credential
scope)
Created: 2026-08-17
Implementation baseline: `e9a2754`
Branch: `goal/flutter-desktop-migration` (the work was written on the in-flight
branch rather than a new `goal/` branch)
Execution plan: `docs/plans/2026-08-17-redacted-plaintext-and-content-addressed-evidence.md`
Design authority: `vibermate-design/docs/design/06-security.md` §8.1, §8.3;
`vibermate-design/docs/design/02-architecture.md` §4, §4.3, §13;
`vibermate-design/docs/design/07-protocol-translation.md` §2, §3.1

Every required item below is implemented, and every freeze gate but one has a
test that closes it; the gate list names them and marks the exception. The
exception is the first-enable half of `INV-STORE-DISCLOSED`, which is a design
release gate and is deliberately not delivered — so this document is not a claim
that the goal is complete against the design as written. The work is uncommitted
on the working tree at the time of writing.

Two boundaries stay explicit. This document records the evidence plane only — it
does not restate or replace `PLAN.md`'s Environment-first vertical, and whether
it eventually supersedes that document is an editorial decision separate from
what is delivered here. And nothing here is a Preview or Release claim: passing
these gates makes the evidence plane honest, not the product shippable.

## Goal

Ship the evidence plane the design already specifies, around two product
authorities:

- an Exchange has two evidence planes with disjoint claims — the bytes
  ViberMate observed at its Go boundary, and the normalized protocol structure
  it parsed from them — and neither substitutes for the other; and
- conversational continuity is derived from what the client sent, never from
  what the client asserts.

Application-layer encryption, credential retention behind a `contains_secret`
flag, and storage shapes that flatten a level the protocol defines are not
retained through aliases, compatibility readers, or legacy database migrations.

## Required vertical

1. The archive is plaintext and says so. No application-layer field encryption,
   no data-key revision, and no UI claim of either. The disclosure — no at-rest
   encryption, credential header values removed before the write, what is
   retained as sent, where evidence is written, how long it is kept — is visible
   in Settings, in both locales.

   `INV-STORE-DISCLOSED` also asks for it at first enable of content recording,
   and that half is **not delivered**. The reason is product judgement, not
   effort: this is a single-user desktop product, and an interstitial about
   storage at first use reads as nagging where a settings page that always
   answers the question does not. It stays an open divergence from a design
   release gate, listed as unmet in the gate table below, because a goal document
   that quietly reinterprets a gate is the failure mode this whole goal exists to
   remove.

2. No known credential header value reaches the archive. Fields matching the
   credential predicate lose their values at the observation boundary, before the
   payload exists, and a writer with no salt refuses rather than storing one.
   `INV-AUTHZ-REDACT` is enforced on the log path and the archive path, which are
   two points, not one.

   This is narrower than "the archive holds no secret", and the narrowing is the
   honest part. The domain boundary is:

   - a **structured credential field** — a header whose name matches the
     credential predicate — is redacted at capture and its value never reaches
     SQLite. That is what design 06 §8.1 requires, and it is met;
   - **opaque content** — a prompt, a tool argument, a request body, a query
     string — is never scanned or rewritten, so a secret a person put there is
     retained with the rest of the content.

   The second half is not a design permission and this document does not claim it
   is. Design 06 §8.2 provides for it differently: configurable masking rules
   that pattern-match the content plane. Those rules have never been implemented,
   and this plan's non-goals exclude changing the recording-mode and retention
   policy surface, so the gap is pre-existing and untouched rather than introduced
   here. A product that keeps the wire bytes it observed cannot promise an archive
   with no secret in it until that masking exists.

   What survives redaction is wire evidence at the fidelity this plane claims:
   canonical field names, and per-field value order and multiplicity. Original
   letter case, cross-field order and line grouping are normalized by `net/http`
   before this boundary; design 02 §9.4 assigns that fidelity to a separate wire
   shadow, which this plane does not claim to be.

   Stating the boundary here was not enough while four shipped surfaces still
   made the unbounded claim: the Settings disclosure in both locales, the Raw
   evidence tooltip's source comment, `ReadPayload`'s doc comment, and the
   envelope table's schema comment, which asserted that *no column of this table
   can hold* a credential. That last one was contradicted by a test in the same
   package, which plants an API key in `raw_query` to prove the scanner can find
   one. All four now name the retained scope, and
   `TestBodyAndQuerySecretsAreRetainedAsTheClientSentThem` holds them to it.

   One amendment is owed to the design and is **not** applied here. Design 06
   §8.1 reads "provider/proxy/辅助模型 secret、`Authorization`、Cookie 和真实 API
   key 永不进入 SQLite". In context — the clause continues "只保存 SecretRef、
   driver、revision 和脱敏状态" — it governs product-managed credentials and
   credential headers, and the implementation satisfies it. But "真实 API key 永不
   进入 SQLite" reads as absolute, and a key a user types into a prompt is
   retained. The bullet should gain a clause naming content as out of scope and
   pointing at §8.2's masking rules. It is left for the design owner: that file
   is currently mid-refactor in the design repository, and a semantic edit landed
   into an uncommitted terminology rename would be indistinguishable from it.
3. Recorded bytes are content-addressed and byte-exact recoverable. Two layers
   that observed the same bytes cost one copy as a consequence of addressing,
   so a cross-dialect route that genuinely re-serializes is honestly stored as
   two bodies rather than forced into one.
4. The content plane is shaped like the protocol: Exchange → ordered messages →
   ordered blocks, addressed at each level. A top-level instruction parameter
   is a per-Exchange field, never a synthesized chain node.
5. Continuity is derived. `previous_response_id`, client session identity, time,
   title, workspace and prompt similarity never promote to continuity, and a
   transcript prefix hit proves continuity and nothing else.
6. Retention cost tracks distinct content. A capture's stored bytes grow with
   what is new, not with turn count times conversation length.
7. The evidence model is dialect-neutral. Anthropic Messages and OpenAI
   Responses are proven client edges; OpenAI Chat is a proven backend edge.
   Landing the OpenAI Chat client edge must require no change to any storage
   schema, digest definition, or purge path. That requirement is the test of
   neutrality — it is not a claim that the client edge exists.

   As delivered, nothing in the store names a dialect. The top-level instruction
   parameter is an optional per-Exchange field that a dialect without one simply
   leaves empty, and an instruction that arrives inside `messages` stays there —
   which is exactly the shape OpenAI Chat Completions will present. A client edge
   for it therefore needs a codec and nothing from this plane.

## Why these two authorities and not six

The defects that motivated this goal were a 91% storage overhead, a retained
live credential, and a continuity signal that fired on 5.6% of the Exchanges
where it should have. None of those is an authority; each was what happens when
one of the two authorities above is absent.

The first authority settles which plane answers which question. The answer used
to be ambiguous in both directions: the raw plane sealed credentials it should
never have retained, because encryption made retention feel safe, and the content
plane addressed a whole message as if the message were the atom, when the
protocol says blocks are. With each plane's claim disjoint, a block digest is
never offered as wire evidence and a byte range is never offered as
conversational evidence.

The second authority settles whether turn N+1 continues turn N. The design had
already fixed this position — `01-features.md:460` records that a client's
declared session identity and a transcript prefix hit are independent confidence
axes that do not promote each other — but the store could not honor it, because a
136-byte transport header inside the top-level `system` parameter forked the
chain on every request. The fix was not to trust the client's assertion instead;
it was to stop treating transport telemetry as conversation.

Storage size is a consequence and belongs in item 6, not in the goal. A goal
that names its own implementation has been written badly.

## Freeze gates

Each gate names the test that closes it, so a reader can re-run the claim rather
than trust it.

| Gate | Closed by |
| --- | --- |
| A populated database survives a credential scan across every BLOB and TEXT column | `TestNoCredentialHeaderValueReachesAnyColumn`, with `TestCredentialScanFindsAPlantedValueAndNamesItsColumn` proving the scanner can fail |
| Credential header values never exist in a payload, and a writer without a salt refuses | `TestPayloadNeverRetainsACredentialHeaderValue`, `TestPayloadRefusesToBuildWithoutABoundRedactor`, `TestOpenFailsWhenTheDatabaseCannotSupplyARedactionSalt` |
| Reassembly reproduces recorded bodies byte-for-byte | `TestStoredBodyReassemblesByteExactly`, `TestSplitReassemblesEveryInputExactly`, `TestATruncatedObservationStoresItsRetainedPrefix` |
| Listing an Exchange resolves bodies without holding its own rows | `TestListExchangeResolvesBodiesWithoutHoldingItsRows` — the store keeps one open connection, so a nested read would deadlock |
| Rate-limit evidence survives credential redaction | `TestRateLimitEvidenceSurvivesRedaction` |
| Bodies, tool arguments and query strings are retained exactly as the client sent them | `TestBodyAndQuerySecretsAreRetainedAsTheClientSentThem` — the other half of the credential claim, and the half whose silent loss would destroy what this product is for |
| No surface claims credential absence without naming its scope | `CheckCredentialClaimScope`, wired into `make check-structural` and driven by `TestCredentialAbsenceClaimsMustNameTheirScope`. Correcting the sentences by hand missed several; the rule is what makes the claim checkable |
| The Dart copy table's two languages carry the same keys | `CheckFlutterCopyPair`, driven by `TestFlutterCopyPairUsesKnownGoodAndInjectedBadFixtures` |
| A corrupted chunk fails rather than returning a partial body | `TestACorruptedChunkFailsInsteadOfReturningAPartialBody` |
| A message reassembled from its block manifest re-canonicalizes to the digest it was stored under | `TestBlockStoragePreservesMessageIdentityAndContent`, `TestExchangeContentRepositoryRejectsTamperedSharedMessagePayload` |
| Two observations of the same bytes cost one copy; two that differ cost two | `TestTwoLayersObservingTheSameBytesStoreOneCopy`, `TestTwoLayersObservingDifferentBytesStoreTwoBodies` |
| Retention reaches the bytes, not just the envelope row | `TestExpiredEnvelopesReleaseTheirBodiesAndChunks`, `TestExpiredExchangesReleaseTheirContentBlocks` |
| No schema, Go type, control contract or locale names an application-layer at-rest encryption construct | `CheckAtRestEncryptionAbsence`, wired into `make check-structural`. The rule matches named constructs — ciphertext, cipher nonce, data-key revision, SQLCipher, the old key handle — and deliberately not the word *encryption*: TLS tunnelling and provider-encrypted reasoning blocks are legitimate uses of it, and a regex that killed them would be trading a false claim for a false positive |
| No comment in the storage packages still claims stored evidence is encrypted | the same rule's sentence-level pass, driven by `TestStorageProseMayNotClaimTheArchiveIsEncrypted` |
| Settings discloses that the archive is not encrypted at rest, and names what is retained rather than only what is removed, in both locales | `test/storage_disclosure_test.dart` |
| **UNMET** — the same disclosure at first enable of content recording | not delivered; see required item 1. `INV-STORE-DISCLOSED` names this a release gate, so this row stays open |
| A multi-turn Claude run and a multi-turn Codex run both reach incremental Request presentation | `TestARealClaudeMultiTurnRunReachesIncrementalPresentation`, `TestARealCodexMultiTurnRunReachesIncrementalPresentation` |
| Neither invents a continuity the wire did not show | `TestClientAssertedContinuityDoesNotJoinExchanges`, `TestASystemParameterChangeDoesNotForkTheTranscript` |
| Retention cost tracks distinct content rather than turn count | `TestRetentionCostTracksDistinctContent`, which stores 60 growing turns through the real repository and measures what the new store actually wrote |
| The database file — not only its payload columns — grows with distinct content, and its in-flight peak stays bounded | `TestWholeDatabaseGrowthTracksDistinctContent`, which measures the file on disk including envelope rows, every index and page overhead, and samples the WAL peak after every turn |
| Existing gates | `go test ./...`, `go test -race`, `go vet`, `gofmt`, `go mod tidy -diff`, `make check-format`, `check-structural`, `check-dependencies`, `check-workflows`, `check-release-tooling`, `make check-flutter` |

The two dialect gates drive the production client codecs — `anthropicchat` and
`openairesponses` — over real wire bodies into the real store. They deliberately
do not reach a live provider: the property under test is what the store concludes
from bytes a client sent, and a network round trip adds nothing to it. Live
provider acceptance remains a separate, credentialed, operator-run claim that
`PLAN.md` already tracks.

`make check-release-build` fails on this tree for an unrelated pre-existing
reason: `internal/desktophost/live_agent_deep_test.go` references
`hostsecret.NewDevelopmentFileFactory`, which lives behind
`//go:build !vibermate_native_secrets`. Both files are unmodified against
`e9a2754`, so the failure predates this work and belongs to the in-flight
`hostsecret` change.

## Delivered evidence

| Measurement | Figure | Source |
| --- | --- | --- |
| Recorded corpus, request bodies through the production chunker | 170.0 MB → 5.93 MB, **28.7x** | `TestRecordedCorpusRetentionMatchesItsProjection` over a 744-Exchange corpus |
| Content blocks, distinct after block addressing | 5,826 → 3,490; 11.61 MB → 4.64 MB, 1.82 MB with zstd | measured before implementation, recorded in the execution plan |
| Prefix reuse, system parameter out of the chain | 38/673 → **575/673** hits; 32,259 → 7,941 chain nodes | algorithm replay over the recorded corpus |
| Chunking below block granularity | net negative: 1.82 MB → 1.89 MB | measured and rejected; blocks are the granularity |
| Whole database file, 120 growing turns | 11.39 MB observed → **0.19 MB** settled on disk, 60.7x | `TestWholeDatabaseGrowthTracksDistinctContent`; includes envelope rows, every index and page overhead, with schema baseline subtracted |
| In-flight peak, same run, including the WAL | **4.07 MB** — plateaus, does not scale | the same test samples after every turn; 2.73 / 4.07 / 4.23 MB at 60 / 120 / 200 turns against 2.88 / 11.39 / 31.51 MB naive |
| Per-`Record` no-op `PurgeExpired` | 19.8 µs against a 4.06 ms `Put` — **0.49%** of the write path | `BenchmarkNoOpPurgeExpired` and `BenchmarkExchangeContentPut`, 2,000 live rows |

`TestRecordedCorpusRetentionMatchesItsProjection` is opt-in and skips without
`VIBERMATE_CORPUS_DB`, because it needs a populated database no checkout has. Its
corpus was written by the previous schema, so it measures real-world content shape
through the production chunker rather than the new store's own output. The gate
above is closed by the in-process measurement, which does measure the new store's
output; the corpus test cross-checks that a synthetic body did not flatter the
result. Re-running it against a freshly captured database is how the delivered
store gets compared with the plan's whole-database projection.

The 60.7x whole-file figure carries the same caveat as the growth test it comes
from: its body is synthetic and more repetitive than real traffic, so the ratio
is not a production number. What it does establish is the thing a column-sum
measurement could not — that per-row overhead and indexes do not eat the
deduplication. The production content ratio remains the 28.7x corpus figure.

The settled figure is also not the whole cost, and saying only that would have
been the same kind of half-claim this goal exists to remove. Before a checkpoint
lands, the WAL holds every page version the writes touched, which tracks
transaction count rather than distinct content — at 60 turns the in-flight peak
was 2.73 MB against a settled 0.06 MB. What makes that acceptable is that it
plateaus: 4.07 MB at 120 turns and 4.23 MB at 200, because SQLite auto-checkpoints
at 1000 pages and `connector.go` leaves that default in place. Peak disk is
therefore bounded by a constant, not by conversation length, and the test asserts
the bound rather than the ratio so that removing the default fails it.

The purge measurement settles a question rather than reporting a win. `Record`
still runs `PurgeExpired` in its own transaction before every `Put`, which reads
like a per-write full sweep and is the shape a reviewer should challenge. It is
no longer one: the reachability scan runs only when an Exchange was actually
deleted, and the remaining no-op costs 0.49% of the write it precedes. Batching
it behind a timer or a watermark would buy noise and add a staleness window to
retention, so it stays as it is. Both halves are benchmarks in the package rather
than figures in this document, because a cost claim that cannot be re-run is not
evidence.

What is measured here is storage and write cost. Read-side latency is not:
neither UI decode and render, nor switching Conversations against a large
database, has been benchmarked, and neither should be claimed. The store's
algorithmic shape is settled; end-to-end responsiveness on a real corpus is an
open question that a freshly captured database is the way to answer.

## Explicitly deferred

- the OpenAI Chat client edge itself, and any fourth dialect. Item 7 constrains
  what landing it may cost; it does not schedule it;
- live provider acceptance. The dialect gates prove what the store concludes
  from real wire bodies through the production codecs; they do not claim a
  credentialed run against a provider, which `PLAN.md` tracks separately;
- whole-database or whole-volume encryption. `INV-STORE-DISCLOSED` settles this
  by disclosure rather than by mechanism;
- cross-capture continuity as a product claim. Content addressing will
  deduplicate across scopes as a physical consequence, which is not evidence
  that two captures are one conversation;
- retention defaults, sampling, and recording-mode policy changes;
- byte-range chunking inside the content plane, measured and rejected in the
  execution plan;
- reconstructing a transcript the request did not carry, including any use of
  `previous_response_id` to join Exchanges.

Deferral keeps these seams typed. It does not permit placeholder success,
fabricated evidence, or a storage shortcut that a later dialect would have to
undo.
