package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

func TestRawEvidenceRepositoryCommitsOneBatchAndKeepsSecretsOutOfPlaintext(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, databasePath)
	repository := store.RawEvidenceRepository()
	body := []byte(`{"private":"body-value-that-must-not-appear"}`)
	digest := sha256.Sum256(body)
	records := []rawevidence.StoredEnvelope{
		rawEvidenceRecordForTest(
			"writer-1.1", 1, rawevidence.LayerClientIngress,
			digest, []byte("encrypted-authorization-and-body-one"),
		),
		rawEvidenceRecordForTest(
			"writer-1.2", 2, rawevidence.LayerClientDownstream,
			digest, []byte("encrypted-authorization-and-body-two"),
		),
	}
	if err := repository.AppendBatch(
		context.Background(), records, time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.ListExchange(context.Background(), "exchange-raw-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 || loaded[0].Layer != rawevidence.LayerClientIngress ||
		loaded[1].Layer != rawevidence.LayerClientDownstream {
		t.Fatalf("unexpected stored envelopes: %#v", loaded)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	databaseBytes, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"Bearer private-token", "body-value-that-must-not-appear",
		"Authorization", "private-api-key",
	} {
		if bytes.Contains(databaseBytes, []byte(forbidden)) {
			t.Fatalf("database contains forbidden plaintext %q", forbidden)
		}
	}
}

func TestRawEvidenceRepositoryColumnAndArgumentCountsStayAligned(t *testing.T) {
	digest := sha256.Sum256([]byte("body"))
	record := rawEvidenceRecordForTest(
		"writer-1.1", 1, rawevidence.LayerClientIngress,
		digest, []byte("ciphertext"),
	)
	columns := strings.Split(rawEvidenceColumns, ",")
	for index := range columns {
		columns[index] = strings.TrimSpace(columns[index])
		if columns[index] == "" {
			t.Fatalf("raw evidence column %d is empty", index)
		}
	}
	if got := len(columns); got != rawEvidenceColumnCount {
		t.Fatalf("raw evidence SQL columns=%d declared columns=%d", got, rawEvidenceColumnCount)
	}
	if got := len(rawEvidenceArguments(record)); got != len(columns) {
		t.Fatalf("raw evidence arguments=%d SQL columns=%d", got, len(columns))
	}
	if got := strings.Count(rawEvidencePlaceholders, "?"); got != len(columns) {
		t.Fatalf("raw evidence placeholders=%d SQL columns=%d", got, len(columns))
	}
}

func TestRawEvidenceWriterRecoveryReportsOnlyOpenPredecessorAndPurgesExpiry(
	t *testing.T,
) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.RawEvidenceRepository()
	started := time.Date(2026, 8, 13, 4, 5, 6, 0, time.UTC)
	first := rawevidence.WriterSession{
		WriterID:             "writer-open",
		StartedAt:            started,
		MaximumUnflushedTime: 125 * time.Millisecond,
	}
	if recovery, err := repository.BeginWriterSession(
		context.Background(), first,
	); err != nil || recovery != (rawevidence.Recovery{}) {
		t.Fatalf("first recovery=%+v err=%v", recovery, err)
	}
	bodyDigest := sha256.Sum256([]byte("expired"))
	expired := rawEvidenceRecordForTest(
		"writer-open.1", 1, rawevidence.LayerClientIngress,
		bodyDigest, []byte("ciphertext"),
	)
	expired.WriterID = first.WriterID
	expired.ObservedAt = started.Add(-48 * time.Hour)
	expired.ExpiresAt = started.Add(-time.Hour)
	if err := repository.AppendBatch(
		context.Background(), []rawevidence.StoredEnvelope{expired},
		started.Add(-2*time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	second := rawevidence.WriterSession{
		WriterID:             "writer-next",
		StartedAt:            started.Add(time.Second),
		MaximumUnflushedTime: 100 * time.Millisecond,
	}
	recovery, err := repository.BeginWriterSession(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.RecoveredUncleanWriters != 1 ||
		recovery.PurgedExpiredEnvelopes != 1 ||
		recovery.MaximumPossibleLoss != first.MaximumUnflushedTime {
		t.Fatalf("unexpected recovery: %+v", recovery)
	}
	if err := repository.CloseWriterSession(
		context.Background(), second.WriterID, started.Add(2*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	third := rawevidence.WriterSession{
		WriterID:             "writer-clean-successor",
		StartedAt:            started.Add(3 * time.Second),
		MaximumUnflushedTime: 100 * time.Millisecond,
	}
	recovery, err = repository.BeginWriterSession(context.Background(), third)
	if err != nil || recovery.RecoveredUncleanWriters != 0 ||
		recovery.MaximumPossibleLoss != 0 {
		t.Fatalf("clean successor recovery=%+v err=%v", recovery, err)
	}
}

func TestRawEvidenceRevealAuditStoresNoPayload(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, databasePath)
	repository := store.RawEvidenceRepository()
	digest := sha256.Sum256([]byte("body"))
	record := rawEvidenceRecordForTest(
		"writer-audit.1", 1, rawevidence.LayerProviderResponse,
		digest, []byte("authenticated-ciphertext"),
	)
	record.WriterID = "writer-audit"
	if err := repository.AppendBatch(
		context.Background(), []rawevidence.StoredEnvelope{record}, time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.GetEnvelope(context.Background(), record.EnvelopeID)
	if err != nil || loaded.EnvelopeID != record.EnvelopeID {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if err := repository.AppendRevealAudit(context.Background(), rawevidence.RevealAudit{
		EnvelopeID: record.EnvelopeID,
		ExchangeID: record.ExchangeID,
		ActorID:    "desktop-app:test",
		Outcome:    rawevidence.RevealSucceeded,
		OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	var envelopeID, exchangeID, actorID, outcome string
	if err := store.database.QueryRowContext(
		context.Background(),
		`SELECT envelope_id, exchange_id, actor_id, outcome
		 FROM runtime_raw_evidence_reveal_audits`,
	).Scan(&envelopeID, &exchangeID, &actorID, &outcome); err != nil {
		t.Fatal(err)
	}
	if envelopeID != record.EnvelopeID || exchangeID != record.ExchangeID ||
		actorID != "desktop-app:test" ||
		outcome != string(rawevidence.RevealSucceeded) {
		t.Fatalf("unexpected reveal audit: %q %q %q %q",
			envelopeID, exchangeID, actorID, outcome)
	}
	columns, err := store.database.QueryContext(
		context.Background(),
		`PRAGMA table_info(runtime_raw_evidence_reveal_audits)`,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer columns.Close()
	for columns.Next() {
		var ordinal, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := columns.Scan(&ordinal, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		switch strings.ToLower(name) {
		case "body", "headers", "trailers", "frames", "ciphertext", "cipher_nonce":
			t.Fatalf("reveal audit schema carries payload column %q", name)
		}
	}
	if err := columns.Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	databaseBytes, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(databaseBytes, []byte("raw-private-payload")) {
		t.Fatal("reveal audit persisted unexpected plaintext payload")
	}
}

func rawEvidenceRecordForTest(
	id string,
	watermark uint64,
	layer rawevidence.Layer,
	digest [sha256.Size]byte,
	ciphertext []byte,
) rawevidence.StoredEnvelope {
	observedAt := time.Unix(1_790_000_000, int64(watermark)).UTC()
	return rawevidence.StoredEnvelope{
		EnvelopeID:             id,
		WriterID:               "writer-1",
		Watermark:              watermark,
		Layer:                  layer,
		ScopeKind:              rawevidence.ScopeManagedRun,
		ScopeID:                "capture-raw-1",
		ExchangeID:             "exchange-raw-1",
		ConnectionID:           "connection-raw-1",
		EnvironmentID:          "environment-raw-1",
		EnvironmentRevision:    1,
		ClientEndpointID:       "endpoint-raw-1",
		ClientEndpointRevision: 1,
		ProtocolPlanID:         "protocol-raw-1",
		ProtocolPlanRevision:   1,
		RouteID:                "route-raw-1",
		RouteRevision:          1,
		ObservedAt:             observedAt,
		ExpiresAt:              observedAt.AddDate(0, 0, 30),
		Method:                 "POST",
		Scheme:                 "https",
		Authority:              "api.anthropic.com",
		Path:                   "/v1/messages",
		ContentType:            "application/json",
		Representation:         "http_message",
		Canonicalization:       "go_net_http_v1",
		HeaderCount:            2,
		BodyBytes:              45,
		BodySHA256:             digest,
		DigestScope:            rawevidence.DigestFull,
		PayloadState:           rawevidence.PayloadCaptured,
		ContainsSecret:         true,
		EncryptionKeyRevision:  1,
		CipherNonce:            []byte(strings.Repeat("n", 12)),
		Ciphertext:             ciphertext,
	}
}
