# Redacted Plaintext and Content-Addressed Evidence

Status: implemented; verification pending fresh capture evidence
Created: 2026-08-17
Implementation baseline: `e9a2754`
Goal: `docs/plans/2026-08-17-honest-evidence-retention-goal.md`
Design authority: `vibermate-design/docs/design/06-security.md` §8.1, §8.3;
`vibermate-design/docs/design/02-architecture.md` §4, §4.3, §13;
`vibermate-design/docs/design/07-protocol-translation.md` §2, §3.1

## Objective

The raw evidence plane encrypts what the design forbids encrypting, retains
what the design requires redacting, and stores the same conversation once per
turn instead of once. These are one problem, not three: the encryption is what
made storing a live `Authorization` header feel acceptable, and it is also what
makes the stored bytes incompressible and undedupable.

This plan is not a storage optimization. It returns `rawevidence` to the
design it was supposed to implement. The size reduction is a consequence of
that correction, not its goal.

Two content-plane defects are included because they are the same class of
error and share the fix window. The protocol's structure is
`Exchange → ordered messages → ordered blocks`, and `exchangecontent` flattens
a level at each end of it: the top-level `system` parameter is synthesized into
the front of the message list, and blocks are collapsed into a single message
payload. The first destroys 94% of achievable prefix reuse and makes the
incremental Request inspector permanently degrade to a full checkpoint; the
second stores a whole message again whenever one block of it changes. Neither
is fixed by interpreting content — both are fixed by restoring a level the
protocol already defines.

Nothing is released. There is no migration obligation and none is created.

**Schema changes are made in `00001_runtime_schema.sql` in place**, consistent
with `PLAN.md`'s refusal of legacy compatibility migrations. Existing
development databases are discarded, including the 740 MB capture corpus this
plan was measured against; that is an accepted cost, and captures are recreated
afterwards. One read-only copy of the corpus is kept until the verification
section has been executed, because it is the only evidence base for the figures
here and re-deriving them costs more than recapturing traffic does.

## Measured baseline

Source: `~/Library/Application Support/io.vibermate.desktop/runtime.db`,
740,028,416 bytes, 764 Exchanges, 3,055 raw envelopes, 18 capture scopes.
Measured on a read-only copy; the queries are in the appendix so every figure
here can be re-derived rather than trusted.

| Object | Size | Share | What it holds |
| --- | --- | --- | --- |
| `runtime_raw_evidence_envelopes` | 673.1 MB | 91.0% | 3,055 AEAD payloads |
| — `client_ingress` ciphertext | 324.6 MB | 43.9% | 764 request bodies |
| — `provider_egress` ciphertext | 324.6 MB | 43.9% | the same 764 bodies again |
| — response layer ciphertext | 19.6 MB | 2.6% | 1,527 response bodies |
| `runtime_exchange_content_messages` | 13.2 MB | 1.8% | 2,569 deduplicated messages |
| `runtime_exchange_content_transcripts` + indexes | 11.7 MB | 1.6% | 34,286 chain nodes |

Table sizes are `dbstat` page totals; the indented rows are
`SUM(length(ciphertext))` per layer and sum to 668.8 MB, the remaining 4.3 MB
being metadata columns and page overhead.

Four facts follow from the same table.

1. **The distinct information is 13.2 MB.** The content plane already
   content-addresses every message. It is the proven lower bound for the same
   conversations that the raw plane spends 649 MB on.

2. **`provider_egress` is byte-identical to `client_ingress` in this corpus.**
   `body_sha256` is equal for 764 of 764 Exchanges. This is a property of the
   routes that produced it, not a general one: both are same-dialect inspect
   routes — `route.claude-inspect` to `api.anthropic.com/v1/messages` (733) and
   `route.codex-inspect` to `chatgpt.com/backend-api/codex/responses` (31). A
   cross-dialect route re-serializes the request and the two bodies genuinely
   differ. See the protocol coverage section.

3. **Stored size is 1.339x the real body size** on both request layers —
   exactly base64's 4/3 ratio. `Payload.Body` is a `[]byte` field inside a
   struct that is `json.Marshal`ed before sealing, so every body is
   base64-expanded first. 164.4 MB of the database is that expansion.

4. **Growth is quadratic per capture.** One managed run, `run.WyMg…`, holds
   693 turns and 238.0 MB of request bodies. Turn 1 is 0 KB; turn 661 is
   615 KB. Every turn stores the whole history again.

Three further measurements drive the credential and content-plane work.

5. **1,590 envelopes (659.5 MB) carry `contains_secret = 1`.** `Payload` seals
   headers and body into one blob, so `Authorization` and `x-api-key` values
   are retained verbatim inside it.

6. **706 of 744 Exchanges inherit nothing** (`inherited_message_count = 0`),
   and there are 676 distinct depth-1 chain nodes. Every 8 KB system prompt is
   stored again per turn — 5.4 MB of the 11.6 MB of message payloads — and
   32,212 request-message chain nodes are written across all captures where
   almost all of them should have been inherited. The verification section
   quantifies the recoverable share by replaying the real matching algorithm.

7. **40.1% of stored content blocks are duplicates of another stored block.**
   5,826 blocks reduce to 3,490 distinct ones; 11.61 MB of message payloads
   reduce to 4.64 MB of distinct block payloads. The system parameter is the
   largest single case but not the only one — the message payload is addressed
   as a whole, so any message that differs from another in one block is stored
   in full twice.

## Read-only design authority

- `06-security.md:928` — "`store` 使用 `modernc.org/sqlite`；不引入 SQLCipher、
  加密 VFS 或应用层字段加密。"
- `06-security.md:936` — `INV-STORE-DISCLOSED` is a release gate: "无数据库
  静态加密" must be visible in first-run and settings, and "实现和 UI 不能再
  显示『正文已加密』或『轮换数据密钥』."
- `06-security.md:934`, `:950`, `:1258` — `INV-AUTHZ-REDACT`: credentials are
  redacted **before** the log and archive writes, on every path; provider and
  proxy secrets, `Authorization`, `Cookie` and real API keys never enter
  SQLite.
- `02-architecture.md:197–203` — the Exchange storage structure may be
  changed, provided that request/response/stream/transport remain separable,
  raw wire and parsed IR can coexist, every inference points back at evidence,
  and credentials are redacted before the archive write.
- `02-architecture.md:994` — `RawEnvelope` holds header/body/event
  **references**, bounded, with schema revision and truncation.
- `02-architecture.md:1838` — `original_passthrough` builds the upstream
  request **from the retained client `RawEnvelope`**. The egress body is
  derived from the ingress envelope; it is not a second independent
  observation.
- `02-architecture.md:2094` — `store`: bounded buffers, retention, redaction,
  migration and purge; secrets do not enter the database.
- `01-features.md:460` — a transcript prefix hit proves conversational
  continuity and nothing else. It is a continuity signal, not a storage
  mechanism.

## What is currently wrong

### 1. Application-layer field encryption exists

`runtime_raw_evidence_envelopes` carries `encryption_key_revision`,
`cipher_nonce` and `ciphertext`; `rawevidence/manager.go:33` binds
`secret://vibermate/raw-evidence-key.v1`. `encryption_key_revision` is the
"轮换数据密钥" concept that `INV-STORE-DISCLOSED` names explicitly as
forbidden. The key lives in the same SecretStore the README already describes
as "plaintext-equivalent at rest", so the construct buys nothing against the
threat model it appears to address, while making the payload incompressible
and undedupable.

### 2. Credentials are retained rather than redacted

`rawevidence/manager.go:801-803` computes `containsSecret` from
`HeaderContainsSecret()` and stores the header values unchanged. The flag then
travels through `desktopcontrol/raw_evidence.go:55` to
`ui/flutter_app/lib/features/workbench/conversation_timeline.dart:1857`, which
renders a badge for it.

The product has a UI control whose purpose is to announce a state
`INV-AUTHZ-REDACT` says must never exist. Redaction is not a new requirement
introduced by removing encryption; it is the half of the invariant that was
skipped because encryption was substituted for it.

### 3. An unchanged egress body is stored a second time

`loopbackproxy/handler.go:1063` and `providertransport/client.go:344` each seal
a full payload unconditionally. On a same-dialect route the design derives the
egress request from the retained ingress envelope, and the data agrees: 764 of
764 bodies match by `body_sha256`, differing only in headers.

The defect is that storage cannot express this. Sealing produces two unrelated
ciphertexts whether the bodies are equal or not, so an unchanged body costs a
second full copy and a genuinely re-serialized cross-dialect body costs the same
— the store has no way to say which case it is in. Content addressing makes the
distinction fall out of the bytes; see protocol coverage.

### 4. Body bytes are base64-expanded before storage

`payloadOf` builds `Payload{Version, Headers, Trailers, Body, Frames}` and
`Payload.Marshal` JSON-encodes it, so `Body []byte` becomes base64. Encryption
then prevents any downstream recovery of the 33% expansion.

### 5. The top-level `system` parameter is flattened into the transcript

`exchangecontent/types.go:692-704`:

```go
messages := make([]Message, 0, len(request.Messages)+1)
if len(request.System) > 0 {
    messages = append(messages, Message{
        Role: "system", Blocks: blockViews(request.System, full),
    })
}
```

Both dialects keep `system` / `instructions` as top-level request parameters,
and the IR keeps them separate — `protocolcore.Request.System` is its own
field. `anthropicchat/request_decode.go:701-704` states the principle the
codec follows for instructions that arrive *inside* the message list:

> An instruction can arrive inside the message list rather than only as the
> top-level system parameter. […] keeps it where it was: hoisting it to the
> front would change what the model was told and when.

`requestView` does the hoist the codec refuses to do. The consequence is
measurable because real clients put per-request transport telemetry in that
parameter. Claude Code sends, as `system[0]`:

```text
x-anthropic-billing-header: cc_version=2.1.233.015; cc_entrypoint=cli;
cch=531dc; cc_is_subagent=true; cc_prev_req=req_011Ce61u5QXtY5SEgneZ28zC;
```

`cch` and `cc_prev_req` change on every request. Diffing four stored 8 KB
system messages shows this block as the only difference; the other two blocks
are byte-identical. A 136-byte transport header therefore becomes chain depth 1
and forks the entire transcript on every turn.

Un-flattening needs no content interpretation, no per-client heuristic, and no
change to any recorded byte. It restores the position the wire protocol
already assigns.

### 6. Blocks are flattened into the message payload

`runtime_exchange_content_messages` stores one canonical JSON payload per
message and addresses it by the digest of the whole thing. A message is an
ordered list of blocks in every dialect the product supports, and blocks are
what the codec produces, what `availability: omitted` applies to, and what the
timeline renders. Collapsing them means one changed block rewrites an entire
message: 5,826 blocks are stored as 2,569 whole payloads where only 3,490
blocks are distinct.

This is the same error as item 5 at the level below. The store implements
`Exchange → messages` and stops; the protocol continues to `messages → blocks`.

## Protocol coverage

Three dialects are in scope by design — `07-protocol-translation.md` §2 is
explicit that Chat Completions and Responses are two dialects, not one "OpenAI
API". Their implementation status is not symmetric:

| Dialect | Client edge | Backend edge |
| --- | --- | --- |
| Anthropic Messages | `anthropicchat` | — |
| OpenAI Responses | `openairesponses` | — |
| OpenAI Chat Completions | none; `/v1/chat/completions` is registered as `OpenAIChatUnsupportedID` | `anthropicchat`, `ProviderRelativePath = "chat/completions"` |

The codec pairs are `anthropic-messages-to-openai-chat` and
`openai-responses-to-openai-chat`. `ClientProtocolOpenAIChat` exists in the
Environment enum as a typed seam with no client codec behind it.

Three consequences bear on this plan.

**Cross-dialect routes do not produce equal bodies.** When both edges are the
same dialect and nothing is rewritten, `07-protocol-translation.md` §2 keeps the
original HTTP message shadow — which is why this corpus shows 764 of 764 equal.
When the edges differ, the egress body is a re-serialization and shares only its
long text runs with the ingress body. Content addressing is correct in both
cases, and an explicit "egress references ingress" link would have been wrong in
the second; this is why invariant 4 is phrased as a consequence rather than a
guarantee. The within-layer quadratic sharing, which is the larger effect, is
unaffected.

**Phase 4 applies to two of the three dialects, and that is correct rather than
partial.** Anthropic `system` and OpenAI Responses `instructions` are top-level
parameters, so hoisting them into the message list was always wrong. OpenAI Chat
has no such parameter: its system instruction genuinely is `messages[0]` with
role `system` or `developer`. When that client edge lands, the decoder must leave
it in the message list, exactly as `request_decode.go:701-704` already requires
for instruction messages arriving inline. If such a client varies that message
per request, the chain forks and incremental presentation degrades — and that is
the honest result, because the client did change the first message of the
conversation. Phase 5 keeps the storage cost bounded regardless, since the
stable blocks are still shared. **Hoisting `messages[0]` into `System` to
recover a prefix hit would reintroduce the exact defect this plan removes.**

**Message shape is dialect-dependent; block shape is far less so.**
`07-protocol-translation.md` §3.1 records that Anthropic may interleave text,
image and tool_result inside one user message where OpenAI Chat splits them into
several. Message-level addressing therefore shares nothing across dialects,
while block-level addressing partly can. This is an additional argument for
Phase 5, and it fixes what a block digest means: it identifies **normalized**
content, not wire bytes. A codec revision that changes normalization produces
new digests and degrades sharing until purge, which is acceptable — but no
consumer may treat a block digest as evidence about the wire. Wire bytes live in
the raw plane, and that separation is the point.

## Required invariants

1. No application-layer encryption, encrypted VFS or data-key revision exists
   in the runtime database or its control surface. The absence of at-rest
   encryption is disclosed where `INV-STORE-DISCLOSED` requires it.
2. No known credential header value reaches SQLite. A field matching the
   credential predicate has its value replaced at capture by a salted digest and
   a byte length, before the record leaves the observation boundary. What is
   preserved is canonical field names plus per-field value order and
   multiplicity — not original case or cross-field order, which `net/http`
   normalizes before this boundary. This does not extend to bodies: a secret in
   a prompt, a tool argument or a query string is content, and design 06 §8.1
   permits retaining it.
3. Every stored body is byte-exact recoverable. Reassembly from its manifest
   reproduces bytes whose SHA-256 equals the recorded `body_sha256`; a
   mismatch is an error, never a partial result.
4. Two observations of the same bytes cost one stored copy, as a consequence of
   content addressing rather than a rule in the writer. Two observations that
   genuinely differ — a cross-dialect route re-serializing the request, a
   plugin rewriting it — are stored as two bodies, because they are two facts.
   No code path may assume the egress body equals the ingress body.
5. Total stored bytes for one capture grow with new content, not with turn
   count times conversation length.
6. The transcript chain covers exactly the messages the client sent in
   `messages` / `input`. A per-request top-level instruction parameter is
   recorded as a per-Exchange field and is never synthesized into a chain node;
   a dialect that has no such parameter contributes none.
7. Base matching consumes only the messages the request carried. A
   client-declared continuity claim — `previous_response_id`, a session
   identifier, time, title, workspace, or prompt similarity — is recorded as
   protocol evidence and never joins two Exchanges into one transcript.
8. The recorded conversation content is unchanged by this plan. No block is
   dropped, rewritten, reordered or reclassified.
9. A message digest remains `SHA256(canonical_json(message))`. Storing a
   message as a block manifest changes where its bytes live, never what its
   identity is, so no transcript node or recorded digest changes meaning.
10. Each plane addresses content at exactly one granularity: blocks in the
    content plane, byte ranges in the raw plane. Neither borrows the other's
    unit, and neither borrows the other's claim: a block digest identifies
    normalized content and is never wire evidence, and a chunk digest
    identifies bytes and is never conversational evidence.
11. Retention and purge remain authoritative. An expired Exchange releases its
    bodies, chunks and blocks; each survives exactly as long as something
    references it. Reachability is recomputed only when an expiry actually
    removed a row, because `Record` purges on every stored Exchange and an
    unconditional sweep would reintroduce a per-turn scan of all retained
    content.

## Non-goals

- Whole-database or whole-volume encryption. `INV-STORE-DISCLOSED` settles
  this: the product discloses that the database is not encrypted at rest.
- Interpreting, extracting or normalizing any conversation content, including
  the transport telemetry that motivated item 5. Un-flattening is sufficient
  and it changes nothing that is recorded.
- Changing retention defaults, sampling, or the recording-mode policy surface.
- Cross-capture deduplication policy. Chunks and blocks are content-addressed
  and will deduplicate across scopes as a physical consequence; no product
  claim about cross-capture continuity is made, and
  `findStoredTranscriptBase` keeps its existing managed-run-only restriction.
- Byte-range chunking inside the content plane. Measured and rejected in
  Phase 5; blocks are the granularity.

## Bottom-up implementation

Each phase is independently correct and independently verifiable. Phases 1 and
2 must land together or in this order; shipping 2 before 1 would put live
credentials in a plaintext column.

### Phase 1 — Redact credentials at the observation boundary

- [x] Extend `rawevidence.HeaderField` so a field can carry either values or
      redaction evidence: `{Name, Values []string, Redacted []RedactedValue}`
      where `RedactedValue{Digest [32]byte, Bytes int}`. A field never carries
      both.
- [x] Redact inside `payloadOf`, before the payload exists — not in the
      repository, not in the projection. `HeaderContainsSecret`'s existing
      predicate selects the fields; it changes from a flag source to the
      redaction rule.
- [x] Derive the digest as `HMAC-SHA256(salt, value)` with a per-database
      random 32-byte salt stored in a runtime meta row. The salt is not a
      secret and is not a SecretStore reference: `INV-STORE-DISCLOSED` already
      states the database is not protected at rest. Its only purpose is to stop
      a redacted digest from being matched against an external corpus, which
      matters for low-entropy `Proxy-Authorization: Basic` values. It does not
      protect against an adversary holding this database, and the plan does not
      claim it does.
- [x] Replace the boolean with the evidence it should always have been.
      `contains_secret` becomes `redacted_credential_fields`: the ordered list
      of header field names that were redacted, empty when none were. The
      control-plane field and the Flutter model follow, and
      `conversation_timeline.dart:1857` changes from a badge announcing that a
      secret is stored to one naming which fields were removed. A boolean that
      means "a credential is in here" has no correct value after Phase 1.
- [x] Add a negative fixture that scans every BLOB and TEXT column of a
      populated runtime database for known credential shapes and fails on a
      hit. This is the standing guard for `INV-AUTHZ-REDACT`, not a one-time
      check.

### Phase 2 — Remove application-layer encryption

- [x] Drop `encryption_key_revision` and `cipher_nonce`, and replace
      `ciphertext` with a plaintext `payload` BLOB holding the same marshalled
      `Payload`. The `CHECK` constraints that tie `payload_state` to nonce and
      ciphertext lengths are rewritten against the new column. Phase 2 must
      leave a working store on its own, so the body keeps a home here until
      Phase 3 moves it; the base64 expansion survives this phase and is removed
      by Phase 3, not by this one.
- [x] Remove the ciphertext vocabulary from every Go and Dart type that carries
      it.
- [x] Remove `rawEvidenceKeyReference` and the `secretstore` dependency from
      `rawevidence`. `rawevidence.Options` loses `Secrets`.
- [x] Rename the reveal path to what it now is: an audited read of stored
      evidence. `runtime_raw_evidence_reveal_audits` keeps its purpose —
      it audits access, which was never the same thing as decryption.
- [x] Add the `INV-STORE-DISCLOSED` disclosure to first-run and settings, and
      add a structural check that no UI string claims content is encrypted or
      that a data key can be rotated.

### Phase 3 — Separate headers from body and content-address the body

- [x] Split the payload. Headers, trailers and frames are bounded structured
      metadata and move to their own JSON column. The body becomes a reference.
      This removes the base64 expansion by construction.
- [x] Add the chunk store:

      ```sql
      CREATE TABLE runtime_evidence_chunks(
        digest BLOB PRIMARY KEY NOT NULL CHECK(length(digest) = 32),
        plain_bytes INTEGER NOT NULL CHECK(plain_bytes > 0),
        codec TEXT NOT NULL CHECK(codec IN('identity', 'zstd')),
        payload BLOB NOT NULL
      ) STRICT;

      CREATE TABLE runtime_evidence_bodies(
        body_sha256 BLOB PRIMARY KEY NOT NULL CHECK(length(body_sha256) = 32),
        body_bytes INTEGER NOT NULL CHECK(body_bytes >= 0),
        chunk_manifest BLOB NOT NULL
      ) STRICT;
      ```

      `chunk_manifest` is the ordered concatenation of 32-byte chunk digests.
      An envelope references `runtime_evidence_bodies` by the `body_sha256` it
      already records, so the ingress and egress rows of one Exchange share the
      body row and its manifest with no writer-side special case. Invariant 4
      falls out of the schema.

      The envelope reference is nullable and constrained to follow
      `payload_state`: `captured` and `truncated` require a body row,
      `metadata_only` and `unavailable` require none. `body_sha256` stays where
      it is as observation evidence; the new column is a separate nullable
      foreign key, because an envelope with no payload records an all-zero
      digest today and must not be made to point at a body row.
- [x] Implement FastCDC in `internal/evidencechunk`: gear-hash content-defined
      chunking, 2 KiB minimum, 8 KiB average, 64 KiB maximum, normalized
      chunking. Roughly 150 lines with a table constant; the repository's
      dependency discipline makes an in-tree implementation preferable to a
      new module.

      Content-defined boundaries are required rather than a longest-common-
      prefix split. The volatile region is inside the `system` parameter, whose
      byte position within the request body is client-controlled and not
      observable to us. A prefix split fails completely when the variance is
      early; CDC resynchronizes after any local difference regardless of where
      it occurs. This is why `restic`, `borg` and `casync` use it.
- [x] Compress each chunk with zstd via `klauspost/compress`, already a direct
      dependency. Store `codec = 'identity'` when compression does not help, so
      an incompressible chunk is never stored larger than its input.
- [x] Reassemble on read, then verify the concatenation against
      `body_sha256` before returning anything. Truncated observations keep
      `digest_scope = 'observed_prefix'` and the manifest covers exactly the
      observed prefix. `unavailable` observations have no body row.
- [x] Extend purge: after expired envelopes are deleted, delete unreferenced
      bodies, then unreferenced chunks — the same shape as the existing
      transcript purge in `exchange_content_repository.go:547-576`.
- [x] **Never check-then-skip a chunk insert.** `rawevidence` writes through a
      batched asynchronous writer, so a writer that finds a chunk already
      present and skips the insert can have that chunk purged before its
      envelope commits, leaving a dangling reference. Every chunk is written
      with an unconditional upsert that also proves the existing payload
      matches, reusing the pattern already in `putStoredMessage`:
      `ON CONFLICT(digest) DO UPDATE SET digest = excluded.digest WHERE
      payload = excluded.payload`, with a `RowsAffected() == 1` check. The
      envelope insert and its chunk inserts share one transaction.
- [x] Bound the manifest in the schema. `DefaultMaximumBodyBytes` is 16 MiB, so
      a 2 KiB minimum chunk admits at most 8,192 chunks and a 256 KiB manifest.
      The column carries `CHECK(length(chunk_manifest) % 32 = 0 AND
      length(chunk_manifest) BETWEEN 32 AND 262144)`, so a truncated or
      misaligned manifest cannot be stored at all.
- [x] Note for reviewers: the compression codec is not part of any identity.
      Digests are computed over plaintext, so the zstd level or implementation
      may change later without invalidating a single stored digest.

### Phase 4 — Stop flattening the top-level instruction parameter

- [x] Give `exchangecontent.Request` its own `System []Block` field and delete
      the synthesized message in `requestView`. Instruction messages that
      arrive *inside* `messages` keep their position, exactly as
      `request_decode.go:701-704` requires.
- [x] Add `system_message_digest` to `runtime_exchange_contents`, nullable,
      referencing `runtime_exchange_content_messages`. The system content is
      stored verbatim and content-addressed as before; only its position in the
      chain changes.
- [x] Build the transcript chain from `request.Messages` alone. A dialect with
      no top-level instruction parameter contributes no system field and keeps
      its instruction message inside the chain; see protocol coverage.
- [x] Assert in code and in test that base matching reads only
      `request.Messages`. `openai_responses.previous_response_id` is already
      recorded as protocol evidence and stripped from egress
      (`NoticePreviousResponseIDNotForwarded`); it must never become an input to
      `findStoredTranscriptBase`. `01-features.md:460` makes client-declared
      session identity and a transcript prefix hit independent confidence axes,
      and joining Exchanges by the client's claim would fabricate a transcript
      the store never observed.
- [x] Move the `cc_is_subagent=true` read in
      `agentconversation/projection.go:684` to `request.System`. It is the only
      consumer that reads that block today and it must not start reading
      `messages[0]` instead.
- [x] Extend the control contract and the Flutter model with the per-request
      system field, and render it as a per-request section rather than a chat
      turn. `ExchangeRequest.messages` no longer silently contains it.
- [x] Verify the incremental Request inspector now reaches
      `RequestPresentationIncremental` on a real multi-turn capture. Today it
      cannot: 706 of 744 Exchanges inherit nothing.

### Phase 5 — Content-address the content plane at block granularity

Phases 4 and 5 correct the same class of error. The protocol's structure is
`Exchange → ordered messages → ordered blocks`. The store implements the first
level as the transcript chain and collapses the second: a message is addressed
by the digest of its whole canonical payload, so two messages differing in one
block are stored in full twice. Phase 4 stops a parameter from being flattened
into the message list; Phase 5 stops blocks from being flattened into the
message payload. After both, the storage model is isomorphic to the protocol.

Measured on the corpus: 5,826 blocks reduce to 3,490 distinct ones, and 11.61 MB
of message payloads become 4.64 MB of distinct blocks plus 0.33 MB of manifests.
With per-block zstd the distinct blocks are 1.82 MB.

- [x] Add the block store and rebuild the message row around a manifest:

      ```sql
      CREATE TABLE runtime_exchange_content_blocks(
        digest BLOB PRIMARY KEY NOT NULL CHECK(length(digest) = 32),
        plain_bytes INTEGER NOT NULL CHECK(plain_bytes > 0),
        codec TEXT NOT NULL CHECK(codec IN('identity', 'zstd')),
        payload BLOB NOT NULL
      ) STRICT;

      CREATE TABLE runtime_exchange_content_messages(
        digest BLOB PRIMARY KEY NOT NULL CHECK(length(digest) = 32),
        role TEXT NOT NULL,
        agent_json BLOB,
        block_manifest BLOB NOT NULL
      ) STRICT;
      ```

- [x] **Keep the message digest definition exactly as it is**:
      `SHA256(canonical_json(message))`. Only its storage changes. Every
      transcript node, base match and stored digest keeps its current meaning,
      and Phase 5 becomes invisible to Phases 1–4. A message row is read by
      reassembling role, agent context and blocks, re-marshalling canonically,
      and requiring the result to hash to the stored digest — the same
      round-trip guarantee `decodeStoredMessage` gives today, applied one level
      down.
- [x] Compress each block with zstd, `codec = 'identity'` when compression does
      not help, matching the Phase 3 chunk rule.
- [x] Extend purge to release unreferenced blocks after unreferenced messages,
      continuing the existing chain in
      `exchange_content_repository.go:547-576`.

**Blocks are the stopping point, and this is measured rather than assumed.**
Chunking below block granularity was simulated on the same corpus:

| | Result |
| --- | --- |
| block dedup + per-block zstd | **1.82 MB** |
| block dedup + CDC(1 KiB) + per-chunk zstd | 1.89 MB |

Going further is net negative. Small chunks lose the compression context that
makes zstd effective on a whole block, and the extra manifest exceeds the extra
sharing. The protocol's atom turns out to be the storage optimum as well, so
the semantic plane keeps one addressing scheme, one integrity check and one GC
path. Byte-range chunking stays in the raw plane, where the unit of stability
genuinely is a byte range and no protocol structure is available.

## Verification

Correctness first, size second.

- **Byte-exactness.** A property test over recorded and synthetic bodies —
  empty, 1 byte, exactly at each CDC boundary parameter, 16 MB, incompressible
  random, and highly repetitive — asserting that reassembly reproduces the
  input exactly and that a corrupted chunk produces an error rather than a
  partial body.
- **No credential in the database.** The Phase 1 negative fixture, run against
  a database populated by the real composition tests.
- **No encryption surface.** A structural check that the schema, Go types,
  control contract and locales contain no data-key, nonce or ciphertext
  vocabulary, matching the repository's existing structural negative fixtures.
- **Deduplication is a property, not a special case.** Two tests, not one.
  A same-dialect route: record one Exchange through the real pipeline and
  assert the second layer adds zero chunk rows, without the writer knowing
  which layer it is on. A cross-dialect route
  (`anthropic-messages-to-openai-chat`): assert two distinct bodies are stored,
  each byte-exact, and that nothing reports them as one observation.
- **Client-asserted continuity never joins Exchanges.** Record two OpenAI
  Responses Exchanges where the second carries `previous_response_id` naming
  the first and an `input` that is not a superset of the first's transcript.
  Assert `inherited_message_count = 0`, that the identifier is present as
  protocol evidence, and that no projection presents the pair as one
  transcript.
- **Prefix reuse.** Replay a recorded multi-turn capture and assert
  `inherited_message_count > 0` for the append-only turns.
- **Recorded content is unchanged.** Compare the projected conversation before
  and after Phase 4 for a recorded capture: the same blocks, in the same order,
  with the same text; only their placement in the request/transcript split
  differs.
- **Message identity is unchanged by Phase 5.** For every message in a recorded
  capture, reassembling it from its block manifest and re-marshalling
  canonically reproduces the stored digest. A message stored before Phase 5 and
  the same message stored after it have the same digest, so the transcript
  chain is untouched.
- **Existing gates.** `make check`, `make check-flutter-macos`,
  `go test -race -count=1 ./...`, `go vet`, `go mod tidy -diff`,
  `go mod verify`.

The Phase 4 effect was simulated against the recorded database before this
plan was written, by replaying the real base-matching algorithm over the 673
Exchanges of `run.WyMg…` with and without the synthesized system node:

| | Prefix hits | Inherited messages | Chain nodes written |
| --- | --- | --- | --- |
| Today (system in chain) | 38 / 673 | 1,429 | 32,259 |
| System out of chain | 575 / 673 | 25,075 | 7,941 |

Node counts include the response node each Exchange appends, so they are not
directly comparable to the 32,212 request-message figure in the baseline, which
spans all 744 Exchanges.

The simulated "today" figure reproduces the 38 Exchanges that the stored
`inherited_message_count` column independently reports, which is what makes the
projected figure trustworthy. The 98 remaining misses are new subagent
conversations and compaction checkpoints — correct behavior, not residual loss.

## Expected outcome

| Plane | Now | After | Basis |
| --- | --- | --- | --- |
| Raw evidence | 673.1 MB | ~12 MB | **Measured with the production chunker**: 170 MB of reconstructed request bodies across 744 Exchanges become 5.39 MB of 1,763 distinct chunks plus 0.54 MB of manifests, a 28.7x ratio. The real 242.4 MB additionally carries tool schemas and the system parameter, both more repetitive than what was measured, so the ratio holds or improves. Responses add ~11 MB of less repetitive input. base64 and the egress copy are removed by construction. |
| Transcript nodes | 11.7 MB | ~3 MB | 32,259 → 7,941 nodes measured on the largest capture |
| Message and block payloads | 13.2 MB | ~2.2 MB | 11.61 MB → 4.64 MB distinct blocks → 1.82 MB with zstd, plus 0.33 MB of manifests |
| Remainder | ~42 MB | ~42 MB | untouched |
| **Total** | **740 MB** | **~59 MB** | |

The raw-plane figure is measured by `TestRecordedCorpusRetentionMatchesItsProjection`
in `internal/runtimepersistence`, an opt-in test that runs the production
`evidencechunk.Split` and zstd over a real corpus. Two limits remain and neither
is hidden: the bodies are reconstructed from stored transcripts rather than read
from the sealed originals, which cannot be decrypted without the key this plan
deletes; and the corpus contains only same-dialect routes, where the egress body
is free. A deployment using cross-dialect routes stores a second, genuinely
different body per Exchange and lands higher — correctly so.

Per-turn cost changes from marshalling and hashing the entire history plus two
AEAD seals over ~870 KB, to hashing the body once and storing only new chunks.
The read path stops re-marshalling and re-hashing every message of a transcript
on each detail view.

The size estimate is a projection from measured inputs, not a guarantee. It is
recorded here so the implementation can be checked against it.

## Accepted bounded costs

- **`PurgeExpired` still opens a transaction on every recorded Exchange.**
  `exchangecontent.Manager.Record` calls it per turn. The reachability sweeps are
  now gated on an actual expiry, so the per-turn work is one indexed range probe
  that usually finds nothing plus a commit — bounded and small, but not free. The
  quadratic sweep is gone; the per-turn commit is not. Throttling the call or
  carrying a next-expiry watermark is the named follow-up, and until it lands this
  plan claims that storage growth and the quadratic purge are fixed, not that
  per-turn cost is optimal.
- **A query-string credential would still reach a plaintext column.** Found
  while building the Phase 1 scanner: `raw_query` is stored as safe metadata,
  so `?api_key=…` would land in plaintext. No dialect this product supports
  uses query-string authentication, and invariant 2 is about the credential
  fields the design names, which are header-shaped. The Phase 1 scanner is
  pointed at exactly this column with a planted value, so the guard exists and
  fails loudly if a future dialect introduces the vector. Redacting query
  parameters is deliberately not attempted here: it would rewrite recorded wire
  evidence against a freeform grammar, and it is not needed yet.
- **Chunk manifests are O(body size) per envelope.** At an 8 KiB average, the
  693-turn capture spends about 1.4 KB per envelope and under 1 MB in total for
  its manifests. Bounded and measured; no manifest chaining is introduced.
- **Tool schemas remain absent from the content plane.** `requestView` reduces
  tools to name and namespace. This plan does not change that. The raw plane
  keeps the full schemas and will now deduplicate them across turns.

## Appendix — reproducing the baseline

Against a read-only copy of a populated runtime database.

```sql
-- Table shares (facts 1 and 6)
SELECT name, ROUND(SUM(pgsize)/1048576.0, 1) mb, SUM(ncell) cells
  FROM dbstat GROUP BY name ORDER BY SUM(pgsize) DESC;

-- Per-layer stored size and the base64 expansion (facts 3 and 4)
SELECT layer,
       ROUND(SUM(body_bytes)/1048576.0, 1)          real_body_mb,
       ROUND(SUM(length(ciphertext))/1048576.0, 1)  stored_mb,
       ROUND(1.0*SUM(length(ciphertext))/SUM(body_bytes), 3) inflation
  FROM runtime_raw_evidence_envelopes
 WHERE payload_state = 'captured' GROUP BY layer;

-- The egress body is the ingress body (fact 2)
SELECT (i.body_sha256 = e.body_sha256) same_body, COUNT(*) n
  FROM runtime_raw_evidence_envelopes i
  JOIN runtime_raw_evidence_envelopes e
    ON e.exchange_id = i.exchange_id AND e.layer = 'provider_egress'
 WHERE i.layer = 'client_ingress' GROUP BY same_body;

-- Credentials retained (fact 5)
SELECT contains_secret, COUNT(*) n,
       ROUND(SUM(length(ciphertext))/1048576.0, 1) mb
  FROM runtime_raw_evidence_envelopes GROUP BY contains_secret;

-- Prefix reuse is not happening (fact 6)
SELECT CASE WHEN inherited_message_count = 0 THEN 'no reuse'
            WHEN inherited_message_count >= request_message_count - 1
                 THEN 'near-perfect'
            ELSE 'partial' END bucket,
       COUNT(*) n,
       SUM(request_message_count - inherited_message_count) nodes_added
  FROM runtime_exchange_contents GROUP BY bucket;

-- Why: the chain forks at depth 1 on nearly every turn
SELECT COUNT(*) nodes, COUNT(DISTINCT message_digest) messages
  FROM runtime_exchange_content_transcripts WHERE depth = 1;

-- Quadratic growth within one capture (fact 4)
WITH t AS (SELECT ROW_NUMBER() OVER (ORDER BY watermark) rn, body_bytes
             FROM runtime_raw_evidence_envelopes
            WHERE layer = 'client_ingress' AND scope_id = :scope)
SELECT rn, ROUND(body_bytes/1024.0) kb FROM t WHERE rn % 60 = 1;
```

The Phase 5 granularity decision and the raw-plane projection were both
simulated in Python over the same copy, with a gear-hash CDC (2 KiB / 8 KiB /
64 KiB, normalized) and zstd level 9:

- Block granularity: canonicalize every block of every stored message, address
  by SHA-256, and sum the distinct payloads — with and without a further
  CDC pass at 1 KiB and 4 KiB averages, each variant then compressed. The
  result recorded in Phase 5 is that the further pass costs more than it saves.
- Raw plane: reconstruct each Exchange's request body by concatenating its
  transcript messages in chain order, then chunk, deduplicate and compress.
  This under-represents the real body — it omits tool schemas, the system
  parameter and the JSON envelope, 170 MB against a measured 242.4 MB — and
  those omissions are the *most* repetitive parts, so the 20x ratio it produces
  is a floor rather than a best case.

The Phase 4 projection replays the real base-matching algorithm rather than
approximating it: for each Exchange of one scope in recorded order, walk the
message-digest sequence of its request, find the longest prefix equal to a
previously recorded `expected` sequence in the same scope, and count what would
be inherited — once with the synthesized system node present and once without.
The run is validated by requiring that the "with" case reproduce the 38
Exchanges the stored `inherited_message_count` column independently reports.
