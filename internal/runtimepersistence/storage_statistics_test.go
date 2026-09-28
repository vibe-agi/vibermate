package runtimepersistence

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

func TestStorageStatisticsPreviewAndCleanupRespectRetention(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.RawEvidenceRepository()
	observed := time.Unix(1_790_000_000, 0).UTC()

	expired := rawEvidenceRecordForTest(
		"writer-storage.1", 1, rawevidence.LayerClientIngress,
		[]byte("expired body"), []byte(`{"version":1,"headers":[]}`),
	)
	expired.WriterID = "writer-storage"
	expired.ExchangeID = "exchange-storage-expired"
	expired.ObservedAt = observed
	expired.ExpiresAt = observed.Add(time.Hour)
	current := rawEvidenceRecordForTest(
		"writer-storage.2", 2, rawevidence.LayerClientIngress,
		[]byte("current body"), []byte(`{"version":1,"headers":[]}`),
	)
	current.WriterID = "writer-storage"
	current.ExchangeID = "exchange-storage-current"
	current.ObservedAt = observed.Add(time.Minute)
	current.ExpiresAt = observed.Add(24 * time.Hour)
	if err := repository.AppendBatch(
		context.Background(),
		[]rawevidence.StoredEnvelope{expired, current},
		observed,
	); err != nil {
		t.Fatal(err)
	}
	if err := repository.AppendRevealAudit(context.Background(), rawevidence.RevealAudit{
		EnvelopeID: expired.EnvelopeID, ExchangeID: expired.ExchangeID,
		ActorID: "storage-test-owner", Outcome: rawevidence.RevealSucceeded,
		OccurredAt: observed.Add(30 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	now := observed.Add(2 * time.Hour)
	preview, err := store.StorageStatistics(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	archive, archiveErr := store.EvidenceArchivePreview(context.Background())
	if preview.Expired.Envelopes != 1 || archiveErr != nil ||
		archive.Envelopes != 2 || preview.EvidenceBytes == 0 ||
		preview.ReusableBytes < 0 {
		t.Fatalf("preview = %+v", preview)
	}
	released, err := store.CleanupExpired(context.Background(), now)
	if err != nil || released.Envelopes != 1 || released.Exchanges != 0 {
		t.Fatalf("cleanup = %+v, %v", released, err)
	}
	after, err := store.StorageStatistics(context.Background(), now)
	archive, archiveErr = store.EvidenceArchivePreview(context.Background())
	if err != nil || archiveErr != nil || after.Expired.Envelopes != 0 ||
		archive.Envelopes != 1 {
		t.Fatalf("after = %+v, %v", after, err)
	}
	if _, err := repository.GetEnvelope(context.Background(), expired.EnvelopeID); !errors.Is(err, rawevidence.ErrEnvelopeNotFound) {
		t.Fatalf("expired envelope remained: %v", err)
	}
	if _, err := repository.GetEnvelope(context.Background(), current.EnvelopeID); err != nil {
		t.Fatalf("unexpired envelope was removed: %v", err)
	}
	var audits int
	if err := store.database.QueryRowContext(
		context.Background(), `SELECT COUNT(*) FROM runtime_raw_evidence_reveal_audits`,
	).Scan(&audits); err != nil || audits != 0 {
		t.Fatalf("expired reveal audits = %d, %v", audits, err)
	}
}

func TestExpiredMaintenanceBoundsEachEvidencePlane(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	ctx := context.Background()
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	for index, id := range []string{"expired-a", "expired-b", "live"} {
		expires := now
		if id == "live" {
			expires = now.Add(time.Hour)
		}
		content := blockRecordFixture(t, id, now.Add(-time.Hour), []string{"shared system"}, "shared message")
		content.ExpiresAt = expires
		if err := store.ExchangeContentRepository().Put(ctx, content); err != nil {
			t.Fatal(err)
		}
		raw := rawEvidenceRecordForTest(fmt.Sprintf("writer-batch.%d", index+1), uint64(index+1), rawevidence.LayerClientIngress,
			[]byte("shared raw body"), []byte(`{"version":1,"headers":[]}`))
		raw.WriterID, raw.ExchangeID = "writer-batch", id
		raw.ObservedAt, raw.ExpiresAt = now.Add(-time.Hour), expires
		if err := store.RawEvidenceRepository().AppendBatch(ctx, []rawevidence.StoredEnvelope{raw}, now.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		insertUsageFixture(t, store, runtimeusage.Observation{
			ExchangeID: id, OccurredAt: now.Add(-time.Hour), Status: activity.StatusSucceeded,
		}, expires)
	}
	if _, err := store.database.Exec(`CREATE TRIGGER reject_expiry BEFORE DELETE ON runtime_usage_observations BEGIN SELECT RAISE(ABORT, 'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.cleanupExpired(ctx, now, 1); err == nil {
		t.Fatal("failed batch reported success")
	}
	for _, table := range []string{"runtime_exchange_contents", "runtime_raw_evidence_envelopes", "runtime_usage_observations"} {
		if got := countRows(t, store, table); got != 3 {
			t.Fatalf("failed batch partially removed %s: %d", table, got)
		}
	}
	if _, err := store.database.Exec(`DROP TRIGGER reject_expiry`); err != nil {
		t.Fatal(err)
	}
	released, more, err := store.cleanupExpired(ctx, now, 1)
	if err != nil || !more || released.Exchanges != 1 || released.Envelopes != 1 {
		t.Fatalf("first batch=%+v more=%v error=%v", released, more, err)
	}
	for _, table := range []string{"runtime_exchange_contents", "runtime_raw_evidence_envelopes", "runtime_usage_observations"} {
		if got := countRows(t, store, table); got != 2 {
			t.Fatalf("unbounded %s batch: %d rows remain", table, got)
		}
	}
	if err := store.MaintainExpired(ctx, now); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"runtime_exchange_contents", "runtime_raw_evidence_envelopes", "runtime_usage_observations"} {
		if got := countRows(t, store, table); got != 1 {
			t.Fatalf("expired %s remaining: %d", table, got)
		}
	}
	if _, err := store.ExchangeContentRepository().Get(ctx, "live", now); err != nil {
		t.Fatalf("shared live content was damaged: %v", err)
	}
	if _, err := store.RawEvidenceRepository().GetEnvelope(ctx, "writer-batch.3"); err != nil {
		t.Fatalf("shared live raw body was damaged: %v", err)
	}
}

func TestContentDeletionUsesIndexedForeignKeyProbes(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	for _, table := range []string{"runtime_exchange_content_transcripts", "runtime_exchange_content_messages", "runtime_evidence_bodies"} {
		t.Run(table, func(t *testing.T) {
			rows, err := store.database.Query("EXPLAIN QUERY PLAN DELETE FROM "+table+" WHERE digest=?", strings.Repeat("a", 64))
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var plan string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				plan += detail + "\n"
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(plan, "SCAN ") {
				t.Fatalf("deleting one content node scans retained references:\n%s", plan)
			}
		})
	}
}

func TestStorageStatisticsLargeDatabasePerformance(t *testing.T) {
	if os.Getenv("VIBERMATE_PERFORMANCE") != "1" {
		t.Skip("set VIBERMATE_PERFORMANCE=1 to collect storage capacity samples")
	}
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	transaction, err := store.database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	statement, err := transaction.Prepare(
		`INSERT INTO runtime_exchange_content_blocks(digest, plain_bytes, codec, payload)
		 VALUES (?, 1024, 'identity', ?)`,
	)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 1024)
	for index := 0; index < 30_000; index++ {
		if _, err := statement.Exec(fmt.Sprintf("%064x", index+1), payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}

	samples := make([]time.Duration, 20)
	for index := range samples {
		started := time.Now()
		if _, err := store.StorageStatistics(context.Background(), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		samples[index] = time.Since(started)
	}
	sort.Slice(samples, func(left, right int) bool { return samples[left] < samples[right] })
	p50, p95 := samples[9], samples[18]
	t.Logf("30k 1 KiB evidence blocks: storage snapshot p50=%s p95=%s", p50, p95)
	if p95 > 250*time.Millisecond {
		t.Fatalf("storage snapshot p95 %s exceeds measured 250ms regression bound", p95)
	}
}

// BenchmarkStorageStatistics keeps the routine Settings read honest against a
// populated evidence store. It uses SQLite page metadata and expiry indexes;
// it must never rebuild or decompress retained bodies.
//
//	go test ./internal/runtimepersistence -run XXX -bench BenchmarkStorageStatistics
func BenchmarkStorageStatistics(b *testing.B) {
	store, _, recordedAt := benchmarkContentStore(b, 2000)
	defer shutdownTestStore(b, store)
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := store.StorageStatistics(context.Background(), recordedAt); err != nil {
			b.Fatal(err)
		}
	}
}
