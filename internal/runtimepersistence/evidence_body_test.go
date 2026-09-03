package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

func bodyForTest(seed byte, size int) []byte {
	body := make([]byte, size)
	for index := range body {
		body[index] = byte(index*31) ^ seed
	}
	return body
}

func TestStoredBodyReassemblesByteExactly(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.RawEvidenceRepository()

	for name, body := range map[string][]byte{
		"empty":    {},
		"one byte": {0x5A},
		"small":    bodyForTest(0x11, 900),
		"multi":    bodyForTest(0x22, 300<<10),
		"repetitive": bytes.Repeat(
			[]byte("the same line over and over again\n"), 6000,
		),
	} {
		record := rawEvidenceRecordForTest(
			"writer-body-"+name, 1, rawevidence.LayerClientIngress,
			body, []byte(`{"version":1,"headers":[]}`),
		)
		record.EnvelopeID = "envelope-" + name
		record.WriterID = "writer-body-" + name
		record.ExchangeID = "exchange-" + name
		record.Body = body
		record.BodyBytes = int64(len(body))
		if len(body) == 0 {
			record.BodySHA256 = sha256.Sum256(nil)
		}
		if err := repository.AppendBatch(
			context.Background(),
			[]rawevidence.StoredEnvelope{record},
			time.Now().UTC(),
		); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		loaded, err := repository.GetEnvelope(
			context.Background(), record.EnvelopeID,
		)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(loaded.Body, body) {
			t.Fatalf("%s: body round-trip lost bytes (%d in, %d out)",
				name, len(body), len(loaded.Body))
		}
	}
}

// Two layers of one Exchange that observed the same bytes must cost one stored
// copy. This has to fall out of content addressing rather than a rule in the
// writer, because a cross-dialect route genuinely re-serializes the request and
// must then be stored as two bodies.
func TestTwoLayersObservingTheSameBytesStoreOneCopy(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.RawEvidenceRepository()

	body := bodyForTest(0x33, 200<<10)
	metadata := []byte(`{"version":1,"headers":[]}`)

	ingress := rawEvidenceRecordForTest(
		"writer-shared.1", 1, rawevidence.LayerClientIngress, body, metadata,
	)
	ingress.Body = body
	ingress.BodyBytes = int64(len(body))
	if err := repository.AppendBatch(
		context.Background(),
		[]rawevidence.StoredEnvelope{ingress},
		time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}
	chunksAfterFirst := countRows(t, store, "runtime_evidence_chunks")
	bodiesAfterFirst := countRows(t, store, "runtime_evidence_bodies")
	if chunksAfterFirst == 0 || bodiesAfterFirst != 1 {
		t.Fatalf("chunks=%d bodies=%d after the first layer",
			chunksAfterFirst, bodiesAfterFirst)
	}

	egress := ingress
	egress.EnvelopeID = "writer-shared.2"
	egress.Watermark = 2
	egress.Layer = rawevidence.LayerProviderEgress
	egress.Body = bytes.Clone(body)
	if err := repository.AppendBatch(
		context.Background(),
		[]rawevidence.StoredEnvelope{egress},
		time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, store, "runtime_evidence_chunks"); got != chunksAfterFirst {
		t.Fatalf("second layer added %d chunks", got-chunksAfterFirst)
	}
	if got := countRows(t, store, "runtime_evidence_bodies"); got != 1 {
		t.Fatalf("bodies = %d, want 1 shared row", got)
	}
}

// A cross-dialect route re-serializes the request, so the two layers genuinely
// differ and must be stored as two bodies. Forcing them into one would be a lie.
func TestTwoLayersObservingDifferentBytesStoreTwoBodies(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.RawEvidenceRepository()

	metadata := []byte(`{"version":1,"headers":[]}`)
	anthropic := bodyForTest(0x44, 120<<10)
	openaiChat := bodyForTest(0x55, 130<<10)

	ingress := rawEvidenceRecordForTest(
		"writer-cross.1", 1, rawevidence.LayerClientIngress,
		anthropic, metadata,
	)
	ingress.Body = anthropic
	ingress.BodyBytes = int64(len(anthropic))
	egress := rawEvidenceRecordForTest(
		"writer-cross.2", 2, rawevidence.LayerProviderEgress,
		openaiChat, metadata,
	)
	egress.Body = openaiChat
	egress.BodyBytes = int64(len(openaiChat))

	if err := repository.AppendBatch(
		context.Background(),
		[]rawevidence.StoredEnvelope{ingress, egress},
		time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, store, "runtime_evidence_bodies"); got != 2 {
		t.Fatalf("bodies = %d, want 2 distinct rows", got)
	}
	loadedIngress, err := repository.GetEnvelope(
		context.Background(), ingress.EnvelopeID,
	)
	if err != nil {
		t.Fatal(err)
	}
	loadedEgress, err := repository.GetEnvelope(
		context.Background(), egress.EnvelopeID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loadedIngress.Body, anthropic) ||
		!bytes.Equal(loadedEgress.Body, openaiChat) {
		t.Fatal("cross-dialect bodies were not stored independently")
	}
}

// A corrupted chunk must fail the read. Returning what could still be
// reassembled would hand a caller a body that is not the body that was observed,
// which is worse than reporting the loss.
func TestACorruptedChunkFailsInsteadOfReturningAPartialBody(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.RawEvidenceRepository()

	body := bodyForTest(0x77, 300<<10)
	record := rawEvidenceRecordForTest(
		"writer-corrupt.1", 1, rawevidence.LayerClientIngress,
		body, []byte(`{"version":1,"headers":[]}`),
	)
	record.Body = body
	record.BodyBytes = int64(len(body))
	if err := repository.AppendBatch(
		context.Background(),
		[]rawevidence.StoredEnvelope{record},
		time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetEnvelope(
		context.Background(), record.EnvelopeID,
	); err != nil {
		t.Fatalf("the body did not round-trip before corruption: %v", err)
	}

	// Rewrite one chunk with different bytes of the same length, so every length
	// check passes and only the digest can catch it.
	if _, err := store.database.ExecContext(
		context.Background(),
		`UPDATE runtime_evidence_chunks
		 SET payload = zeroblob(plain_bytes), codec = 'identity'
		 WHERE rowid = (SELECT MIN(rowid) FROM runtime_evidence_chunks)`,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := repository.GetEnvelope(
		context.Background(), record.EnvelopeID,
	); err == nil {
		t.Fatal("a corrupted chunk was accepted")
	}
}

// Retention has to reach the bytes, not just the envelope row. A chunk survives
// exactly as long as some body names it, and a body exactly as long as some
// envelope references it.
func TestExpiredEnvelopesReleaseTheirBodiesAndChunks(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.RawEvidenceRepository()

	body := bodyForTest(0x66, 250<<10)
	observed := time.Unix(1_790_000_000, 0).UTC()
	expiring := rawEvidenceRecordForTest(
		"writer-expiring.1", 1, rawevidence.LayerClientIngress,
		body, []byte(`{"version":1,"headers":[]}`),
	)
	expiring.Body = body
	expiring.BodyBytes = int64(len(body))
	expiring.ObservedAt = observed
	expiring.ExpiresAt = observed.Add(time.Hour)

	if err := repository.AppendBatch(
		context.Background(),
		[]rawevidence.StoredEnvelope{expiring},
		observed,
	); err != nil {
		t.Fatal(err)
	}
	if countRows(t, store, "runtime_evidence_bodies") != 1 ||
		countRows(t, store, "runtime_evidence_chunks") == 0 {
		t.Fatal("the first append did not store the body")
	}

	// A later append purges expired envelopes, and the bytes must go with them.
	unrelated := rawEvidenceRecordForTest(
		"writer-later.1", 2, rawevidence.LayerClientDownstream,
		nil, []byte(`{"version":1,"headers":[]}`),
	)
	unrelated.ExchangeID = "exchange-later"
	unrelated.BodyBytes = 0
	unrelated.BodySHA256 = sha256.Sum256(nil)
	unrelated.ObservedAt = observed.Add(2 * time.Hour)
	unrelated.ExpiresAt = observed.Add(72 * time.Hour)
	if err := repository.AppendBatch(
		context.Background(),
		[]rawevidence.StoredEnvelope{unrelated},
		observed.Add(2*time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, store, "runtime_evidence_bodies"); got != 0 {
		t.Fatalf("bodies = %d after expiry, want 0", got)
	}
	if got := countRows(t, store, "runtime_evidence_chunks"); got != 0 {
		t.Fatalf("chunks = %d after expiry, want 0", got)
	}
}

func countRows(t *testing.T, store *Store, table string) int {
	t.Helper()
	var count int
	if err := store.database.QueryRowContext(
		context.Background(), `SELECT COUNT(*) FROM `+table,
	).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

// A truncated observation retains a prefix of a message whose full digest is
// known. The stored body is therefore addressed by the digest of the prefix while
// body_sha256 still records the whole message, and the two must not be conflated.
func TestATruncatedObservationStoresItsRetainedPrefix(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.RawEvidenceRepository()

	whole := bodyForTest(0x88, 400<<10)
	prefix := whole[:120<<10]
	record := rawEvidenceRecordForTest(
		"writer-truncated.1", 1, rawevidence.LayerProviderResponse,
		whole, []byte(`{"version":1,"headers":[]}`),
	)
	record.Body = prefix
	record.BodyBytes = int64(len(whole))
	record.DigestScope = rawevidence.DigestFull
	record.PayloadState = rawevidence.PayloadTruncated
	record.PayloadReason = "response_body_limit"

	if err := repository.AppendBatch(
		context.Background(),
		[]rawevidence.StoredEnvelope{record},
		time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.GetEnvelope(
		context.Background(), record.EnvelopeID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded.Body, prefix) {
		t.Fatalf("retained prefix = %d bytes, want %d",
			len(loaded.Body), len(prefix))
	}
	if loaded.BodyBytes != int64(len(whole)) {
		t.Fatalf("BodyBytes = %d, want the whole message %d",
			loaded.BodyBytes, len(whole))
	}
	if loaded.BodySHA256 != sha256.Sum256(whole) {
		t.Fatal("the observed full-message digest was replaced by the prefix digest")
	}
}

// ListExchange must not read a body while its own rows are open. The store keeps
// one open connection, so a nested query would wait for a connection this call is
// holding and never return. Earlier fixtures stored no body, so the digest was
// NULL and the nested read was skipped — which is exactly why the deadlock hid.
func TestListExchangeResolvesBodiesWithoutHoldingItsRows(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.RawEvidenceRepository()

	metadata := []byte(`{"version":1,"headers":[]}`)
	first := bodyForTest(0x91, 150<<10)
	second := bodyForTest(0x92, 90<<10)
	ingress := rawEvidenceRecordForTest(
		"writer-list.1", 1, rawevidence.LayerClientIngress,
		first, metadata,
	)
	ingress.Body = first
	ingress.BodyBytes = int64(len(first))
	response := rawEvidenceRecordForTest(
		"writer-list.2", 2, rawevidence.LayerProviderResponse,
		second, metadata,
	)
	response.Body = second
	response.BodyBytes = int64(len(second))
	if err := repository.AppendBatch(
		context.Background(),
		[]rawevidence.StoredEnvelope{ingress, response},
		time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}

	// Deliberately no deadline: a deadline would turn a deadlock into a timeout
	// error and hide which failure occurred.
	type result struct {
		records []rawevidence.StoredEnvelope
		err     error
	}
	done := make(chan result, 1)
	go func() {
		records, err := repository.ListExchange(
			context.Background(), ingress.ExchangeID,
		)
		done <- result{records, err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if len(got.records) != 2 {
			t.Fatalf("records = %d, want 2", len(got.records))
		}
		if !bytes.Equal(got.records[0].Body, first) ||
			!bytes.Equal(got.records[1].Body, second) {
			t.Fatal("ListExchange returned the wrong bodies")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ListExchange never returned; it read a body while holding its rows")
	}
}

// Startup recovery is the path that expires the most rows at once, and it deletes
// envelopes in its own transaction. Once the reachability sweep became conditional
// on an AppendBatch deletion, nothing reclaimed what recovery removed: the next
// batch's own DELETE finds nothing, so the sweep never fires again and cleartext
// bodies outlive their retention deadline.
func TestStartupRecoveryReleasesTheBytesItExpires(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.RawEvidenceRepository()

	observed := time.Unix(1_790_000_000, 0).UTC()
	body := bodyForTest(0x99, 200<<10)
	record := rawEvidenceRecordForTest(
		"writer-recovery.1", 1, rawevidence.LayerClientIngress,
		body, []byte(`{"version":1,"headers":[]}`),
	)
	record.Body = body
	record.BodyBytes = int64(len(body))
	record.ObservedAt = observed
	record.ExpiresAt = observed.Add(time.Hour)
	if err := repository.AppendBatch(
		context.Background(),
		[]rawevidence.StoredEnvelope{record},
		observed,
	); err != nil {
		t.Fatal(err)
	}
	if countRows(t, store, "runtime_evidence_chunks") == 0 {
		t.Fatal("the append stored no chunks")
	}

	recovery, err := repository.BeginWriterSession(
		context.Background(),
		rawevidence.WriterSession{
			WriterID:             "writer-after-restart",
			StartedAt:            observed.Add(48 * time.Hour),
			MaximumUnflushedTime: 100 * time.Millisecond,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.PurgedExpiredEnvelopes != 1 {
		t.Fatalf("purged = %d, want 1", recovery.PurgedExpiredEnvelopes)
	}
	if got := countRows(t, store, "runtime_evidence_bodies"); got != 0 {
		t.Fatalf("bodies = %d after recovery, want 0", got)
	}
	if got := countRows(t, store, "runtime_evidence_chunks"); got != 0 {
		t.Fatalf(
			"chunks = %d after recovery; cleartext bytes outlived their retention",
			got,
		)
	}
}

// The database is deliberately unprotected at rest, so a corrupt or tampered row
// is reachable by design. plain_bytes reaches make() as a capacity, so an absurd
// value has to be refused before the allocation, not by the length check that
// runs after it.
func TestAnAbsurdStoredLengthIsRefusedBeforeAllocating(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.RawEvidenceRepository()

	body := bodyForTest(0xA1, 40<<10)
	record := rawEvidenceRecordForTest(
		"writer-absurd.1", 1, rawevidence.LayerClientIngress,
		body, []byte(`{"version":1,"headers":[]}`),
	)
	if err := repository.AppendBatch(
		context.Background(),
		[]rawevidence.StoredEnvelope{record},
		time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}

	for _, tamper := range []string{
		`UPDATE runtime_evidence_chunks SET plain_bytes = 4611686018427387904`,
		`UPDATE runtime_evidence_bodies SET plain_bytes = 4611686018427387904`,
	} {
		if _, err := store.database.ExecContext(
			context.Background(), tamper,
		); err != nil {
			// A schema bound refusing the write is the strongest outcome.
			continue
		}
		if _, err := repository.GetEnvelope(
			context.Background(), record.EnvelopeID,
		); err == nil {
			t.Fatalf("an absurd plain_bytes was accepted by %q", tamper)
		}
	}
}

// A large manifest exercises the reachability sweep at a scale where its shape
// matters: carrying the manifest through the recursion instead of the body key
// is quadratic in chunks, and this body produces enough of them for that to be
// the difference between milliseconds and seconds.
func TestPurgeReclaimsALargeManifestedBody(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.RawEvidenceRepository()

	observed := time.Unix(1_790_000_000, 0).UTC()
	// Non-repeating, so the chunks are distinct and the manifest is genuinely
	// long. bodyForTest cycles every 256 bytes, which deduplicates to one chunk.
	body := make([]byte, 4<<20)
	state := uint32(0x2545F491)
	for index := range body {
		state = state*1664525 + 1013904223
		body[index] = byte(state >> 24)
	}
	expiring := rawEvidenceRecordForTest(
		"writer-large.1", 1, rawevidence.LayerClientIngress,
		body, []byte(`{"version":1,"headers":[]}`),
	)
	expiring.ObservedAt = observed
	expiring.ExpiresAt = observed.Add(time.Hour)
	if err := repository.AppendBatch(
		context.Background(),
		[]rawevidence.StoredEnvelope{expiring},
		observed,
	); err != nil {
		t.Fatal(err)
	}
	var manifestEntries int
	if err := store.database.QueryRowContext(
		context.Background(),
		`SELECT length(chunk_manifest) / 32 FROM runtime_evidence_bodies`,
	).Scan(&manifestEntries); err != nil {
		t.Fatal(err)
	}
	if manifestEntries < 256 {
		t.Fatalf("manifest = %d entries, want one large enough to matter",
			manifestEntries)
	}

	recovery, err := repository.BeginWriterSession(
		context.Background(),
		rawevidence.WriterSession{
			WriterID:             "writer-large-after",
			StartedAt:            observed.Add(48 * time.Hour),
			MaximumUnflushedTime: 100 * time.Millisecond,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.PurgedExpiredEnvelopes != 1 {
		t.Fatalf("purged = %d, want 1", recovery.PurgedExpiredEnvelopes)
	}
	if got := countRows(t, store, "runtime_evidence_chunks"); got != 0 {
		t.Fatalf("chunks = %d after expiry, want 0", got)
	}
}
