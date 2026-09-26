package runtimepersistence

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/rawevidence"
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
