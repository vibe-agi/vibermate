package runtimepersistence

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

func TestRecordingDoesNotDependOnRetentionMaintenance(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "retention-failure.db"))
	defer shutdownTestStore(t, store)
	ctx := context.Background()
	now := time.Now().UTC()
	repository := store.ExchangeContentRepository()
	expired := blockRecordFixture(t, "expired", now.Add(-48*time.Hour), nil, "old content")
	expired.ExpiresAt = now.Add(-time.Hour)
	if err := repository.Put(ctx, expired); err != nil {
		t.Fatal(err)
	}
	// An external storage fault affecting deletion must not consume the recording
	// operation or its budget. Reads must still hide expired evidence.
	if _, err := store.database.Exec(`CREATE TRIGGER unavailable_retention BEFORE DELETE ON runtime_exchange_contents
	 BEGIN SELECT RAISE(ABORT, 'synthetic retention failure'); END`); err != nil {
		t.Fatal(err)
	}
	manager, err := exchangecontent.New(ctx, exchangecontent.Options{
		Repository: repository, Clock: exchangecontent.SystemClock{},
	})
	if err != nil {
		t.Fatalf("recorder startup depended on garbage collection: %v", err)
	}
	defer manager.Shutdown(ctx)
	live := blockRecordFixture(t, "live", now, nil, "new content")
	if err := manager.Record(ctx, live); err != nil {
		t.Fatalf("recording depended on garbage collection: %v", err)
	}
	if _, err := manager.Get(ctx, live.ExchangeID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Get(ctx, expired.ExchangeID); !errors.Is(err, exchangecontent.ErrNotFound) {
		t.Fatalf("expired evidence is visible: %v", err)
	}
	if err := store.MaintainExpired(ctx, now); err == nil {
		t.Fatal("fixture did not exercise the separate maintenance failure")
	}
}

func TestRawRecordingDoesNotDependOnRetentionMaintenance(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "raw-retention-failure.db"))
	defer shutdownTestStore(t, store)
	ctx := context.Background()
	now := time.Now().UTC()
	repository := store.RawEvidenceRepository()
	expired := rawEvidenceRecordForTest("writer.1", 1, rawevidence.LayerClientIngress,
		[]byte("expired"), []byte(`{"version":1,"headers":[]}`))
	expired.ObservedAt, expired.ExpiresAt = now.Add(-48*time.Hour), now.Add(-time.Hour)
	if err := repository.AppendBatch(ctx, []rawevidence.StoredEnvelope{expired}, expired.ObservedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.Exec(`CREATE TRIGGER unavailable_retention BEFORE DELETE ON runtime_raw_evidence_envelopes
	 BEGIN SELECT RAISE(ABORT, 'synthetic retention failure'); END`); err != nil {
		t.Fatal(err)
	}
	live := rawEvidenceRecordForTest("writer.2", 2, rawevidence.LayerClientIngress,
		[]byte("live"), []byte(`{"version":1,"headers":[]}`))
	live.ObservedAt, live.ExpiresAt = now, now.Add(time.Hour)
	if err := repository.AppendBatch(ctx, []rawevidence.StoredEnvelope{live}, now); err != nil {
		t.Fatalf("raw recording depended on garbage collection: %v", err)
	}
	if _, err := repository.GetEnvelope(ctx, live.EnvelopeID); err != nil {
		t.Fatal(err)
	}
	if err := store.MaintainExpired(ctx, now); err == nil {
		t.Fatal("fixture did not exercise the separate maintenance failure")
	}
}

func TestExpiredRawEvidenceCannotBeReadWhileMaintenanceIsPending(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "expiry-read.db"))
	defer shutdownTestStore(t, store)
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	clock := &proxyClientClock{now: now}
	manager, err := rawevidence.Open(ctx, rawevidence.Options{
		Repository: store.RawEvidenceRepository(), Random: rand.Reader, Clock: clock, Config: rawevidence.DefaultConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown(ctx)
	record := rawEvidenceRecordForTest("writer.1", 1, rawevidence.LayerClientIngress,
		[]byte("body"), []byte(`{"version":1,"headers":[]}`))
	record.ObservedAt, record.ExpiresAt = now, now.Add(time.Hour)
	if err := store.RawEvidenceRepository().AppendBatch(ctx, []rawevidence.StoredEnvelope{record}, now); err != nil {
		t.Fatal(err)
	}
	request := rawevidence.RevealRequest{EnvelopeID: record.EnvelopeID, ActorID: "test-owner"}
	if _, err := manager.Reveal(ctx, request); err != nil {
		t.Fatal("live evidence was not readable", err)
	}
	clock.Set(record.ExpiresAt)
	if records, err := manager.ListExchange(ctx, record.ExchangeID); err != nil || len(records) != 0 {
		t.Errorf("expired metadata remained visible: records=%d error=%v", len(records), err)
	}
	if _, err := manager.Reveal(ctx, request); !errors.Is(err, rawevidence.ErrEnvelopeNotFound) {
		t.Errorf("expired body remained readable: %v", err)
	}
	if _, err := store.RawEvidenceRepository().GetEnvelope(ctx, record.EnvelopeID); err != nil {
		t.Fatal("test requires pending physical maintenance", err)
	}
}

// growingConversationBody models what an Agent actually sends: the whole
// conversation on every turn, with a per-request telemetry header at the front
// that changes each time.
func growingConversationBody(turn int) []byte {
	var body strings.Builder
	fmt.Fprintf(
		&body,
		`{"system":[{"text":"x-anthropic-billing-header: cch=%05d; cc_prev_req=req_%04d;"},`+
			`{"text":"You are an interactive agent that helps with software engineering tasks. `+
			`Be precise about evidence and never claim what you did not observe."}],"messages":[`,
		turn*7919%100000, turn,
	)
	for index := 1; index <= turn; index++ {
		if index > 1 {
			body.WriteString(",")
		}
		fmt.Fprintf(
			&body,
			`{"role":"user","text":"question %d: %s"},`+
				`{"role":"assistant","text":"answer %d: %s"}`,
			index, strings.Repeat("request detail ", 40),
			index, strings.Repeat("response detail ", 60),
		)
	}
	body.WriteString(`]}`)
	return []byte(body.String())
}

// TestRetentionCostTracksDistinctContent is the measurement the goal's sixth
// required item asks for. A capture's stored bytes must grow with what is new,
// not with turn count times conversation length.
//
// The naive cost is what the store used to pay: every turn's whole body, kept
// verbatim. The assertion is deliberately loose — the point is the shape of the
// curve, not a particular constant.
//
// The logged ratio is not the production figure and must not be quoted as one.
// This body is synthetic and more repetitive than real traffic. The measured
// figure comes from a recorded corpus through
// TestRecordedCorpusRetentionMatchesItsProjection.
func TestRetentionCostTracksDistinctContent(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.RawEvidenceRepository()
	observed := time.Unix(1_790_000_000, 0).UTC()

	const turns = 60
	var naive int64
	for turn := 1; turn <= turns; turn++ {
		body := growingConversationBody(turn)
		naive += int64(len(body))
		record := rawEvidenceRecordForTest(
			fmt.Sprintf("writer-growth.%d", turn),
			uint64(turn),
			rawevidence.LayerClientIngress,
			body,
			[]byte(`{"version":1,"headers":[]}`),
		)
		record.WriterID = "writer-growth"
		record.ExchangeID = fmt.Sprintf("exchange-growth-%d", turn)
		record.Body = body
		record.BodyBytes = int64(len(body))
		record.ObservedAt = observed.Add(time.Duration(turn) * time.Second)
		record.ExpiresAt = record.ObservedAt.AddDate(0, 0, 30)
		if err := repository.AppendBatch(
			context.Background(),
			[]rawevidence.StoredEnvelope{record},
			record.ObservedAt,
		); err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
	}

	var chunkBytes, manifestBytes int64
	if err := store.database.QueryRowContext(
		context.Background(),
		`SELECT COALESCE(SUM(length(payload)), 0) FROM runtime_evidence_chunks`,
	).Scan(&chunkBytes); err != nil {
		t.Fatal(err)
	}
	if err := store.database.QueryRowContext(
		context.Background(),
		`SELECT COALESCE(SUM(length(chunk_manifest)), 0)
		   FROM runtime_evidence_bodies`,
	).Scan(&manifestBytes); err != nil {
		t.Fatal(err)
	}
	stored := chunkBytes + manifestBytes

	t.Logf(
		"turns=%d observed=%.2f MB stored=%.2f MB (chunks %.2f + manifests %.2f) ratio=%.1fx",
		turns,
		float64(naive)/(1<<20),
		float64(stored)/(1<<20),
		float64(chunkBytes)/(1<<20),
		float64(manifestBytes)/(1<<20),
		float64(naive)/float64(stored),
	)

	// The final turn alone carries the whole conversation. Anything close to the
	// naive total means the store is still paying per turn for history it
	// already holds.
	finalTurn := int64(len(growingConversationBody(turns)))
	if stored > 4*finalTurn {
		t.Fatalf(
			"stored %d bytes for a conversation whose largest single turn is %d; "+
				"retention is still tracking turn count",
			stored, finalTurn,
		)
	}

	// Every body must still be exactly recoverable.
	for _, turn := range []int{1, turns / 2, turns} {
		loaded, err := repository.GetEnvelope(
			context.Background(), fmt.Sprintf("writer-growth.%d", turn),
		)
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		if string(loaded.Body) != string(growingConversationBody(turn)) {
			t.Fatalf("turn %d: body did not round-trip", turn)
		}
	}
}

func TestBodyHeavyExpiryFitsMaintenanceBudget(t *testing.T) {
	if os.Getenv("VIBERMATE_USAGE_SCALE") != "1" {
		t.Skip("set VIBERMATE_USAGE_SCALE=1 for the isolated body-retention check")
	}
	store := openTestStore(t, filepath.Join(t.TempDir(), "retention.db"))
	defer shutdownTestStore(t, store)
	ctx := context.Background()
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	const count = 10001
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("body-scale-%d", index)
		text := fmt.Sprintf("%s message %d", strings.Repeat("body evidence ", 80), index)
		content := blockRecordFixture(t, id, now, []string{"shared instruction"}, text)
		content.ExpiresAt = now.Add(time.Hour)
		if index == count-1 {
			content.ExpiresAt = now.Add(2 * time.Hour)
		}
		if err := store.ExchangeContentRepository().Put(ctx, content); err != nil {
			t.Fatal(err)
		}
		raw := rawEvidenceRecordForTest(fmt.Sprintf("writer-body-scale.%d", index+1), uint64(index+1),
			rawevidence.LayerClientIngress, []byte(text), []byte(`{"version":1,"headers":[]}`))
		raw.WriterID, raw.ExchangeID = "writer-body-scale", id
		raw.ObservedAt, raw.ExpiresAt = now, content.ExpiresAt
		if err := store.RawEvidenceRepository().AppendBatch(ctx, []rawevidence.StoredEnvelope{raw}, now); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	budget, cancel := context.WithTimeout(ctx, 5*time.Second)
	released, more, err := store.cleanupExpired(budget, now.Add(time.Hour), expiredCleanupBatchSize)
	cancel()
	t.Logf("10k distinct semantic and raw bodies: first maintenance batch %v, released=%+v error=%v", time.Since(started), released, err)
	if err != nil || !more || released.Exchanges != expiredCleanupBatchSize || released.Envelopes != expiredCleanupBatchSize {
		t.Fatalf("body cleanup cannot make bounded progress: %+v more=%v error=%v", released, more, err)
	}
	if err := store.MaintainExpired(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	t.Logf("10k distinct semantic and raw bodies: complete maintenance %v", time.Since(started))
	if _, err := store.ExchangeContentRepository().Get(ctx, "body-scale-10000", now.Add(time.Hour)); err != nil {
		t.Fatalf("live semantic body was damaged: %v", err)
	}
	if _, err := store.RawEvidenceRepository().GetEnvelope(ctx, "writer-body-scale.10001"); err != nil {
		t.Fatalf("live raw body was damaged: %v", err)
	}
}

// The important workload is one expired record among many live records, not a
// mostly expired database. Neither recording nor expiry may walk the live graph.
func TestSparseExpiryDoesNotScanRetainedBodies(t *testing.T) {
	if os.Getenv("VIBERMATE_USAGE_SCALE") != "1" {
		t.Skip("set VIBERMATE_USAGE_SCALE=1 for the isolated sparse-retention check")
	}
	store := openTestStore(t, filepath.Join(t.TempDir(), "sparse-retention.db"))
	defer shutdownTestStore(t, store)
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	repository := store.ExchangeContentRepository()
	const count = 100000
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("live-%d", index)
		record := blockRecordFixture(t, id, now, nil, id)
		record.Parent.CaptureRunID = id
		if err := repository.Put(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 3; index++ {
		record := blockRecordFixture(t, fmt.Sprintf("expired-%d", index), now,
			[]string{"shared instruction"}, "expired content")
		record.ExpiresAt = now.Add(time.Hour)
		if err := repository.Put(ctx, record); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		budget, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		purged, err := repository.PurgeExpired(budget, now.Add(time.Hour))
		cancel()
		t.Logf("100k live records, one expired: %v, removed=%d, error=%v", time.Since(started), purged, err)
		if err != nil || purged != 1 {
			t.Fatalf("sparse expiry blocked on retained content: removed=%d error=%v", purged, err)
		}
	}
	for _, id := range []string{"live-0", "live-50000", "live-99999"} {
		if _, err := repository.Get(ctx, id, now.Add(time.Hour)); err != nil {
			t.Fatalf("retained content %s was damaged: %v", id, err)
		}
	}
}

func TestSparseRawExpiryDoesNotScanRetainedBodies(t *testing.T) {
	if os.Getenv("VIBERMATE_USAGE_SCALE") != "1" {
		t.Skip("set VIBERMATE_USAGE_SCALE=1 for the isolated raw-retention check")
	}
	store := openTestStore(t, filepath.Join(t.TempDir(), "sparse-raw.db"))
	defer shutdownTestStore(t, store)
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	repository := store.RawEvidenceRepository()
	makeRecord := func(writer string, index int) rawevidence.StoredEnvelope {
		id := fmt.Sprintf("%s.%d", writer, index)
		record := rawEvidenceRecordForTest(id, uint64(index), rawevidence.LayerClientIngress,
			[]byte(id), []byte(`{"version":1,"headers":[]}`))
		record.WriterID, record.ExchangeID = writer, id
		record.ObservedAt, record.ExpiresAt = now, now.Add(24*time.Hour)
		return record
	}
	const count = 100000
	for start := 1; start <= count; start += 1000 {
		batch := make([]rawevidence.StoredEnvelope, 0, 1000)
		for index := start; index < start+1000; index++ {
			batch = append(batch, makeRecord("live", index))
		}
		if err := repository.AppendBatch(ctx, batch, now); err != nil {
			t.Fatal(err)
		}
	}
	for index := 1; index <= 3; index++ {
		expired := makeRecord("expired", index)
		expired.ExpiresAt = now.Add(time.Hour)
		if err := repository.AppendBatch(ctx, []rawevidence.StoredEnvelope{expired}, now); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		budget, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		err := store.MaintainExpired(budget, now.Add(time.Hour))
		cancel()
		t.Logf("100k live raw bodies, one expired: %v, error=%v", time.Since(started), err)
		if err != nil {
			t.Fatalf("raw expiry blocked on retained bodies: %v", err)
		}
		if _, err := repository.GetEnvelope(ctx, expired.EnvelopeID); !errors.Is(err, rawevidence.ErrEnvelopeNotFound) {
			t.Fatalf("expired envelope was not reclaimed: %v", err)
		}
	}
	for _, id := range []string{"live.1", "live.50000", "live.100000"} {
		record, err := repository.GetEnvelope(ctx, id)
		if err != nil || string(record.Body) != id {
			t.Fatalf("retained raw body %s was damaged: %v", id, err)
		}
	}
}

func TestSharedContentAppendDoesNotWalkRetainedReferences(t *testing.T) {
	if os.Getenv("VIBERMATE_USAGE_SCALE") != "1" {
		t.Skip("set VIBERMATE_USAGE_SCALE=1 for the isolated shared-content check")
	}
	store := openTestStore(t, filepath.Join(t.TempDir(), "shared-content.db"))
	defer shutdownTestStore(t, store)
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	repository := store.ExchangeContentRepository()
	record := blockRecordFixture(t, "seed", now, []string{"shared instruction"}, "shared question")
	if err := repository.Put(ctx, record); err != nil {
		t.Fatal(err)
	}
	// Duplicate valid frozen records in one fixture transaction. Only identities
	// differ; public reads below verify the fixture before measuring public writes.
	if _, err := store.database.ExecContext(ctx, `WITH RECURSIVE ids(n) AS (
	 VALUES(1) UNION ALL SELECT n+1 FROM ids WHERE n<100000
	) INSERT INTO runtime_exchange_contents
	 SELECT 'retained-'||n,scope_kind,scope_id,mode,recorded_at_unix_ms,expires_at_unix_ms,
	 request_transcript_digest,expected_transcript_digest,base_transcript_digest,request_message_count,
	 expected_message_count,inherited_message_count,response_message_digest,system_message_digest,
	 cast(json_set(manifest_json,'$.exchangeId','retained-'||n) AS BLOB)
	 FROM ids CROSS JOIN runtime_exchange_contents WHERE exchange_id='seed'`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Get(ctx, "retained-100000", now); err != nil {
		t.Fatal("invalid scale fixture", err)
	}
	started := time.Now()
	budget, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	for index := 0; index < 100; index++ {
		record.ExchangeID = fmt.Sprintf("new-%d", index)
		if err := repository.Put(budget, record); err != nil {
			t.Fatalf("shared-content append %d walked retained references: %v", index, err)
		}
	}
	t.Logf("100 appends sharing 100k retained references: %v", time.Since(started))
}

// databaseBytesOnDisk sums the main database, its WAL and its shared-memory
// file. Measuring the payload columns alone answers a narrower question than
// the one a user asks, which is how large the file on their disk gets: envelope
// rows, every index, per-page overhead and the WAL are all real bytes that a
// column-sum measurement cannot see.
func databaseBytesOnDisk(t *testing.T, store *Store, databasePath string) int64 {
	t.Helper()
	if _, err := store.database.ExecContext(
		context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`,
	); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	return databaseFileBytes(t, databasePath)
}

// databaseFileBytes sums the same three files without checkpointing first, so a
// caller can sample what the writes occupy while they are still in flight.
// Checkpointed steady state is the number a user's disk settles at; the peak is
// what it has to accommodate on the way there, and they are not the same claim.
func databaseFileBytes(t *testing.T, databasePath string) int64 {
	t.Helper()
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(databasePath + suffix)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("stat %s%s: %v", databasePath, suffix, err)
		}
		total += info.Size()
	}
	return total
}

// The sixth required item is about what retention costs, and the delivered
// figures measured chunk payloads and manifests. That is the compressed content,
// not the database. This measures the file the user actually stores: schema
// overhead is subtracted as a baseline, and everything the writes add — envelope
// rows, indexes, page overhead, WAL — is counted.
//
// The threshold separates the two regimes rather than fitting the result. The
// old store wrote every turn's whole body, so its growth was the naive total;
// anything near it means per-row overhead has eaten the deduplication.
func TestWholeDatabaseGrowthTracksDistinctContent(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, databasePath)
	defer shutdownTestStore(t, store)
	repository := store.RawEvidenceRepository()
	observed := time.Unix(1_790_000_000, 0).UTC()

	baseline := databaseBytesOnDisk(t, store, databasePath)

	const turns = 120
	var naive, peak int64
	for turn := 1; turn <= turns; turn++ {
		body := growingConversationBody(turn)
		naive += int64(len(body))
		record := rawEvidenceRecordForTest(
			fmt.Sprintf("writer-file-growth.%d", turn),
			uint64(turn),
			rawevidence.LayerClientIngress,
			body,
			[]byte(`{"version":1,"headers":[]}`),
		)
		record.WriterID = "writer-file-growth"
		record.ExchangeID = fmt.Sprintf("exchange-file-growth-%d", turn)
		record.ObservedAt = observed.Add(time.Duration(turn) * time.Second)
		record.ExpiresAt = record.ObservedAt.AddDate(0, 0, 30)
		if err := repository.AppendBatch(
			context.Background(),
			[]rawevidence.StoredEnvelope{record},
			record.ObservedAt,
		); err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		if sampled := databaseFileBytes(t, databasePath); sampled > peak {
			peak = sampled
		}
	}

	growth := databaseBytesOnDisk(t, store, databasePath) - baseline
	t.Logf(
		"turns=%d observed=%.2f MB whole-file growth=%.2f MB ratio=%.1fx "+
			"peak-including-WAL=%.2f MB (baseline schema %.2f MB excluded)",
		turns,
		float64(naive)/(1<<20),
		float64(growth)/(1<<20),
		float64(naive)/float64(growth),
		float64(peak-baseline)/(1<<20),
		float64(baseline)/(1<<20),
	)
	// The settled figure is not the whole cost: before a checkpoint lands, the
	// WAL holds every page version the writes touched, and that tracks
	// transaction count rather than distinct content. Measured across 60, 120
	// and 200 turns the peak is 2.73, 4.07 and 4.23 MB against naive volumes of
	// 2.88, 11.39 and 31.51 MB — it plateaus instead of growing, because SQLite
	// auto-checkpoints at 1000 pages and `connector.go` leaves that default in
	// place.
	//
	// So the property here is a constant bound, not a ratio. The ceiling is
	// twice the auto-checkpoint threshold, which is what makes this assertion
	// fail if that default is ever removed — an unbounded WAL would make the
	// settled figure a fiction on a long capture.
	const walCeiling = 8 << 20
	if peak-baseline >= walCeiling {
		t.Fatalf(
			"in-flight peak grew %d bytes storing %d bytes of observed turns; "+
				"the WAL is no longer bounded by auto-checkpointing, so the "+
				"settled figure understates what the disk must hold",
			peak-baseline, naive,
		)
	}
	if growth >= naive/4 {
		t.Fatalf(
			"the database grew %d bytes storing %d bytes of observed turns; "+
				"whole-file cost is still tracking turn count",
			growth, naive,
		)
	}
}

// BenchmarkNoOpPurgeExpired measures an idle maintenance pass's PurgeExpired
// transaction it runs before every Put.
//
// The shape is worth challenging: a sweep on every write reads like an O(n)
// cost per record. It is not one — the reachability scan runs only when an
// Exchange was actually deleted — but "not one" is a claim, and a claim about
// cost belongs in a benchmark rather than in a document. Run it against the Put
// benchmark below to get the share.
//
//	go test ./internal/runtimepersistence/ -run XXX \
//	  -bench 'BenchmarkNoOpPurgeExpired|BenchmarkExchangeContentPut'
func BenchmarkNoOpPurgeExpired(b *testing.B) {
	store, repository, recordedAt := benchmarkContentStore(b, 2000)
	defer shutdownTestStore(b, store)

	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := repository.PurgeExpired(
			context.Background(), recordedAt,
		); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkExchangeContentPut is the scale the purge is measured against. A
// number in microseconds means nothing until it sits next to the write it
// precedes.
func BenchmarkExchangeContentPut(b *testing.B) {
	store, repository, recordedAt := benchmarkContentStore(b, 2000)
	defer shutdownTestStore(b, store)

	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		record := blockRecordFixture(
			b, fmt.Sprintf("bench-put-%d", index),
			recordedAt.Add(time.Duration(index)*time.Second),
			[]string{"instruction block that is shared across every record"},
			fmt.Sprintf("question %d", index),
		)
		if err := repository.Put(context.Background(), record); err != nil {
			b.Fatal(err)
		}
	}
}

// benchmarkContentStore returns a store already holding `rows` live Exchanges,
// so the purge is measured against a populated table rather than an empty one.
func benchmarkContentStore(
	b *testing.B,
	rows int,
) (*Store, exchangecontent.Repository, time.Time) {
	b.Helper()
	store := openTestStore(b, filepath.Join(b.TempDir(), "runtime.db"))
	repository := store.ExchangeContentRepository()
	recordedAt := time.Date(2026, 8, 17, 5, 0, 0, 0, time.UTC)
	for index := 0; index < rows; index++ {
		record := blockRecordFixture(
			b, fmt.Sprintf("bench-seed-%d", index),
			recordedAt.Add(time.Duration(index)*time.Second),
			[]string{"instruction block that is shared across every record"},
			fmt.Sprintf("question %d", index),
		)
		if err := repository.Put(context.Background(), record); err != nil {
			b.Fatal(err)
		}
	}
	return store, repository, recordedAt
}
