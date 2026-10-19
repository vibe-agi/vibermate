package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

// The claim is about credentials, not about content. Bodies are deliberately
// stored in the clear now, so asserting that a body value is absent would assert
// an artifact of compression rather than a property of the store.
func TestRawEvidenceRepositoryCommitsOneBatchAndKeepsCredentialsOutOfEveryColumn(
	t *testing.T,
) {
	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, databasePath)
	repository := store.RawEvidenceRepository()
	body := []byte(`{"private":"body-value-that-must-not-appear"}`)
	records := []rawevidence.StoredEnvelope{
		rawEvidenceRecordForTest(
			"writer-1.1", 1, rawevidence.LayerClientIngress,
			body, []byte(`{"version":1,"headers":[]}`),
		),
		rawEvidenceRecordForTest(
			"writer-1.2", 2, rawevidence.LayerClientDownstream,
			body, []byte(`{"version":1,"headers":[]}`),
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
	// Credential header values never reach any column. Field names deliberately do:
	// design 02 §9.6 retains header names and order while excluding their
	// values, and design 06 §8.1 permits storing redaction state. The name is
	// the evidence that the client sent a credential and that it was removed.
	for _, forbidden := range []string{
		"Bearer private-token", "private-api-key",
	} {
		if bytes.Contains(databaseBytes, []byte(forbidden)) {
			t.Fatalf("database contains forbidden plaintext %q", forbidden)
		}
	}
	if !bytes.Contains(databaseBytes, []byte("Authorization")) {
		t.Fatal("database lost the evidence that a credential field was redacted")
	}
}

func TestRawEvidenceManagerDurableFencesPreserveSQLiteReadsAndTerminalDeletion(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := store.database.ExecContext(ctx, `
		INSERT INTO capture_runs(
		  run_id, proxy_capability_hash, control_capability_hash, cwd,
		  canonical_executable_path, executable_label,
		  client_catalog_revision, state, created_at_unix_ms,
		  expires_at_unix_ms, updated_at_unix_ms
		) VALUES('capture-writer-fences', randomblob(32), randomblob(32),
		  '/tmp', '/bin/echo', 'Synthetic client', 1, 'finished', 1, 2, 2)
	`); err != nil {
		t.Fatal(err)
	}
	manager, err := rawevidence.Open(ctx, rawevidence.Options{
		Repository: store.RawEvidenceRepository(),
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x73}, 64)),
		Clock:      rawevidence.SystemClock{},
		Config: rawevidence.Config{
			MaximumQueueRecords: 8, MaximumQueueBytes: 1 << 20,
			MaximumBatchRecords: 8, MaximumBatchBytes: 1 << 20, FlushInterval: time.Hour,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := manager.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	observation := rawevidence.Observation{
		Context: rawevidence.Context{
			ScopeKind: rawevidence.ScopeManagedRun, ScopeID: "capture-writer-fences",
			ExchangeID: "exchange-writer-fences", ConnectionID: "connection-writer-fences",
			EnvironmentID: "environment-writer-fences", EnvironmentRevision: 1,
			ClientEndpointID: "endpoint-writer-fences", ClientEndpointRevision: 1,
			ProtocolPlanID: "protocol-writer-fences", ProtocolPlanRevision: 1,
			RouteID: "route-writer-fences", RouteRevision: 1,
			Recording: rawevidence.RecordingFull, RetentionDays: 30,
		},
		Layer: rawevidence.LayerClientIngress, Method: "POST", Scheme: "https",
		Authority: "api.example.test", Path: "/v1/messages", Complete: true,
		Representation: "http_message", Body: []byte("synthetic request"),
	}
	lease, err := manager.BeginScope(ctx, observation.ScopeKind, observation.ScopeID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if _, err := manager.Observe(ctx, observation); err != nil {
		t.Fatal(err)
	}
	if err := manager.Flush(ctx, rawevidence.Watermark{}); err != nil {
		t.Fatal(err)
	}
	metadata, err := manager.ListExchange(ctx, observation.ExchangeID)
	if err != nil || len(metadata) != 1 {
		t.Fatalf("first SQLite read=%+v err=%v", metadata, err)
	}
	commits := manager.Statistics().BatchCommits
	if _, err := manager.ListExchange(ctx, observation.ExchangeID); err != nil {
		t.Fatal(err)
	}
	if manager.Statistics().BatchCommits != commits {
		t.Fatal("durable SQLite read triggered another batch commit")
	}
	observation.Layer = rawevidence.LayerClientDownstream
	observation.Body = []byte("synthetic response")
	if _, err := manager.Observe(ctx, observation); err != nil {
		t.Fatal(err)
	}
	lease.Release()
	terminal, err := manager.PrepareTerminalScope(ctx, observation.ScopeKind, observation.ScopeID)
	if err != nil {
		t.Fatal(err)
	}
	terminal.Commit()
	metadata, err = manager.ListExchange(ctx, observation.ExchangeID)
	if err != nil || len(metadata) != 2 {
		t.Fatalf("terminal SQLite read=%+v err=%v", metadata, err)
	}
	records, err := store.RawEvidenceRepository().ListExchange(ctx, observation.ExchangeID)
	if err != nil || len(records) != 2 {
		t.Fatalf("durable SQLite envelopes=%d err=%v", len(records), err)
	}
	for index, wantBody := range []string{"synthetic request", "synthetic response"} {
		payload, err := manager.ReadPayload(records[index])
		if err != nil || string(payload.Body) != wantBody {
			t.Fatalf("SQLite payload %d=%q err=%v", index, payload.Body, err)
		}
	}
	beforeLate := manager.Statistics().AdmittedRecords
	if lateLease, err := manager.BeginScope(ctx, observation.ScopeKind, observation.ScopeID); !errors.Is(err, rawevidence.ErrScopeTerminal) {
		if lateLease != nil {
			lateLease.Release()
		}
		t.Fatalf("late SQLite request admission=%v", err)
	}
	if manager.Statistics().AdmittedRecords != beforeLate {
		t.Fatal("denied late request created a watermark")
	}
	deletion, err := store.DeleteCapture(ctx, string(observation.ScopeKind), observation.ScopeID)
	if err != nil || deletion.Envelopes != 2 || deletion.Captures != 1 {
		t.Fatalf("terminal deletion=%+v err=%v", deletion, err)
	}
	metadata, err = manager.ListExchange(ctx, observation.ExchangeID)
	if err != nil || len(metadata) != 0 {
		t.Fatalf("deleted SQLite read=%+v err=%v", metadata, err)
	}
}

func TestRawEvidenceRepositoryColumnAndArgumentCountsStayAligned(t *testing.T) {
	body := []byte("body")
	record := rawEvidenceRecordForTest(
		"writer-1.1", 1, rawevidence.LayerClientIngress,
		body, []byte(`{"version":1,"headers":[]}`),
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
	if got := len(rawEvidenceArguments(record, nil)); got != len(columns) {
		t.Fatalf("raw evidence arguments=%d SQL columns=%d", got, len(columns))
	}
	if got := strings.Count(rawEvidencePlaceholders, "?"); got != len(columns) {
		t.Fatalf("raw evidence placeholders=%d SQL columns=%d", got, len(columns))
	}
}

func TestRawEvidenceRepositoryStoresUnavailablePayloadAsAnEmptyBlob(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.RawEvidenceRepository()
	record := rawEvidenceRecordForTest(
		"writer-unavailable.1",
		1,
		rawevidence.LayerClientDownstream,
		nil,
		nil,
	)
	record.WriterID = "writer-unavailable"
	record.BodyBytes = 0
	record.BodySHA256 = [sha256.Size]byte{}
	record.DigestScope = rawevidence.DigestUnavailable
	record.PayloadState = rawevidence.PayloadUnavailable
	record.PayloadReason = "response_stream_unavailable"
	record.PayloadMetadata = nil

	if err := repository.AppendBatch(
		context.Background(),
		[]rawevidence.StoredEnvelope{record},
		time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.GetEnvelope(context.Background(), record.EnvelopeID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PayloadState != rawevidence.PayloadUnavailable ||
		len(loaded.PayloadMetadata) != 0 {
		t.Fatalf("unexpected unavailable envelope: %+v", loaded)
	}
	var payloadType string
	var payloadBytes int
	if err := store.database.QueryRowContext(
		context.Background(),
		`SELECT typeof(payload_metadata), length(payload_metadata)
		 FROM runtime_raw_evidence_envelopes
		 WHERE envelope_id = ?`,
		record.EnvelopeID,
	).Scan(&payloadType, &payloadBytes); err != nil {
		t.Fatal(err)
	}
	if payloadType != "blob" || payloadBytes != 0 {
		t.Fatalf(
			"unavailable payload storage = %s/%d", payloadType, payloadBytes,
		)
	}
}

func TestRawEvidenceRepositoryCommitsCapturedAndUnavailablePayloadsTogether(
	t *testing.T,
) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.RawEvidenceRepository()
	capturedBody := []byte("captured-response")
	captured := rawEvidenceRecordForTest(
		"writer-mixed.1",
		1,
		rawevidence.LayerClientIngress,
		capturedBody,
		[]byte("authenticated-captured-payload"),
	)
	captured.WriterID = "writer-mixed"
	unavailable := rawEvidenceRecordForTest(
		"writer-mixed.2",
		2,
		rawevidence.LayerProviderResponse,
		nil,
		nil,
	)
	unavailable.WriterID = captured.WriterID
	unavailable.BodyBytes = 0
	unavailable.BodySHA256 = [sha256.Size]byte{}
	unavailable.DigestScope = rawevidence.DigestUnavailable
	unavailable.PayloadState = rawevidence.PayloadUnavailable
	unavailable.PayloadReason = "response_stream_unavailable"
	unavailable.RedactedCredentialFields = nil
	unavailable.PayloadMetadata = nil

	if err := repository.AppendBatch(
		context.Background(),
		[]rawevidence.StoredEnvelope{captured, unavailable},
		time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.ListExchange(context.Background(), captured.ExchangeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatalf("stored envelopes = %d, want 2", len(loaded))
	}
	if loaded[0].PayloadState != rawevidence.PayloadCaptured ||
		!bytes.Equal(loaded[0].PayloadMetadata, captured.PayloadMetadata) ||
		loaded[1].PayloadState != rawevidence.PayloadUnavailable ||
		len(loaded[1].PayloadMetadata) != 0 {
		t.Fatalf("unexpected mixed batch: %#v", loaded)
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
	expired := rawEvidenceRecordForTest(
		"writer-open.1", 1, rawevidence.LayerClientIngress,
		[]byte("expired"), []byte(`{"version":1,"headers":[]}`),
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
	body := []byte("body")
	record := rawEvidenceRecordForTest(
		"writer-audit.1", 1, rawevidence.LayerProviderResponse,
		body, []byte(`{"version":1,"headers":[]}`),
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
		case "body", "headers", "trailers", "frames", "payload_metadata":
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
	body []byte,
	payloadMetadata []byte,
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
		// No body by default. A test that wants one sets Body and BodyBytes
		// together, because a captured envelope must store every byte it counts.
		// Body, BodyBytes and BodySHA256 are derived together so this fixture
		// cannot express an envelope whose counted, stored and hashed bytes
		// disagree — the shape that let a read return an empty body as success.
		Body:                     body,
		BodyBytes:                int64(len(body)),
		BodySHA256:               sha256.Sum256(body),
		DigestScope:              rawevidence.DigestFull,
		PayloadState:             rawevidence.PayloadCaptured,
		RedactedCredentialFields: []string{"Authorization"},
		PayloadMetadata:          payloadMetadata,
	}
}

// An abandoned sequence is a recording gap, never a permanent writer failure.
func TestRawManagerPersistsExactLaterBytesAfterAbandonedBatch(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repo := &failFirstRawBatch{Repository: store.RawEvidenceRepository()}
	manager, err := rawevidence.Open(context.Background(), rawevidence.Options{Repository: repo, Random: bytes.NewReader(bytes.Repeat([]byte{0x31}, 64)), Clock: rawevidence.SystemClock{}, Config: rawevidence.Config{MaximumQueueRecords: 4, MaximumQueueBytes: 1 << 20, MaximumBatchRecords: 4, MaximumBatchBytes: 1 << 20, FlushInterval: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown(context.Background())
	observation := rawevidence.Observation{Context: rawevidence.Context{ScopeKind: rawevidence.ScopeRuntime, ExchangeID: "exchange-gap", Recording: rawevidence.RecordingFull, RetentionDays: 1}, Layer: rawevidence.LayerProviderResponse, Complete: true, Representation: "http_message", Body: []byte("lost")}
	mark, _ := manager.Observe(context.Background(), observation)
	if manager.Flush(context.Background(), mark) == nil {
		t.Fatal("synthetic failed batch succeeded")
	}
	observation.Body = []byte("later exact bytes\x00\xff")
	mark, _ = manager.Observe(context.Background(), observation)
	if mark.Sequence != 2 {
		t.Fatalf("later offer: %+v", mark)
	}
	if err := manager.Flush(context.Background(), mark); err != nil {
		t.Fatal(err)
	}
	records, err := store.RawEvidenceRepository().ListExchange(context.Background(), "exchange-gap")
	if err != nil || len(records) != 1 || !bytes.Equal(records[0].Body, observation.Body) || records[0].Watermark != 2 {
		t.Fatalf("recovery lost bytes: %+v %v", records, err)
	}
	stats := manager.Statistics()
	if stats.DurableWatermark != 0 || stats.HighestPersistedSequence != 2 || stats.PersistedRecords != 1 || stats.SettledSequence != 2 {
		t.Fatalf("gap claimed durable: %+v", stats)
	}
}

type failFirstRawBatch struct {
	rawevidence.Repository
	attempted bool
}

func (r *failFirstRawBatch) AppendBatch(ctx context.Context, records []rawevidence.StoredEnvelope, now time.Time) error {
	if !r.attempted {
		r.attempted = true
		return errors.New("synthetic failed batch")
	}
	return r.Repository.AppendBatch(ctx, records, now)
}
