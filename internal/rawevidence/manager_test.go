package rawevidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/secretstore"
)

func TestManagerEncryptsBatchesAndFlushesWatermark(t *testing.T) {
	repository := &memoryRepository{}
	secrets := newMemorySecrets()
	manager, err := Open(context.Background(), Options{
		Repository: repository,
		Secrets:    secrets,
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x41}, 4096)),
		Clock:      fixedClock{value: time.Unix(1_790_000_000, 0).UTC()},
		Config: Config{
			MaximumQueueRecords: 8,
			MaximumQueueBytes:   1 << 20,
			MaximumBatchRecords: 8,
			MaximumBatchBytes:   1 << 20,
			FlushInterval:       time.Hour,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	observation := testObservation()
	first, err := manager.Observe(context.Background(), observation)
	if err != nil {
		t.Fatal(err)
	}
	observation.Layer = LayerClientDownstream
	second, err := manager.Observe(context.Background(), observation)
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || second.Sequence != 2 ||
		first.WriterID != second.WriterID {
		t.Fatalf("unexpected watermarks: %#v %#v", first, second)
	}
	if err := manager.Flush(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	records := repository.snapshot()
	if len(records) != 2 || repository.commits != 1 {
		t.Fatalf("records=%d commits=%d", len(records), repository.commits)
	}
	if bytes.Contains(records[0].Ciphertext, []byte("Bearer private-token")) ||
		bytes.Contains(records[0].Ciphertext, []byte(`{"secret":"private"}`)) {
		t.Fatal("ciphertext contains plaintext evidence")
	}
	payload, err := manager.Decrypt(context.Background(), records[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(payload.Body) != `{"secret":"private"}` ||
		len(payload.Headers) != 2 || !records[0].ContainsSecret ||
		records[0].EncryptionKeyRevision != 1 {
		t.Fatalf("unexpected decrypted payload or metadata: %#v %#v", payload, records[0])
	}
	stats := manager.Statistics()
	if stats.AdmittedRecords != 2 || stats.DurableWatermark != 2 ||
		stats.BatchCommits != 1 || stats.QueueRecords != 0 || stats.QueueBytes != 0 {
		t.Fatalf("unexpected writer statistics: %#v", stats)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagerConcurrentAgentsStayBoundedAndCommitBatches(t *testing.T) {
	const (
		agentCount        = 8
		exchangesPerAgent = 16
		boundaries        = 4
		recordCount       = agentCount * exchangesPerAgent * boundaries
	)
	config := Config{
		MaximumQueueRecords: 64,
		MaximumQueueBytes:   2 << 20,
		MaximumBatchRecords: 32,
		MaximumBatchBytes:   1 << 20,
		FlushInterval:       time.Hour,
	}
	repository := &memoryRepository{}
	manager, err := Open(context.Background(), Options{
		Repository: repository,
		Secrets:    newMemorySecrets(),
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x56}, 64<<10)),
		Clock:      fixedClock{value: time.Unix(1_790_000_000, 0).UTC()},
		Config:     config,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errorsFound := make(chan error, recordCount)
	var writers sync.WaitGroup
	for agent := 0; agent < agentCount; agent++ {
		writers.Add(1)
		go func(agent int) {
			defer writers.Done()
			for exchange := 0; exchange < exchangesPerAgent; exchange++ {
				for boundary := 0; boundary < boundaries; boundary++ {
					observation := testObservation()
					observation.ScopeID = fmt.Sprintf("capture-agent-%d", agent)
					observation.ExchangeID = fmt.Sprintf(
						"exchange-agent-%d-%d", agent, exchange,
					)
					observation.ConnectionID = fmt.Sprintf(
						"connection-agent-%d-%d", agent, exchange,
					)
					observation.Layer = []Layer{
						LayerClientIngress,
						LayerProviderEgress,
						LayerProviderResponse,
						LayerClientDownstream,
					}[boundary]
					observation.Body = []byte(fmt.Sprintf(
						`{"agent":%d,"exchange":%d,"boundary":%d}`,
						agent, exchange, boundary,
					))
					if _, observeErr := manager.Observe(ctx, observation); observeErr != nil {
						errorsFound <- observeErr
						return
					}
					statistics := manager.Statistics()
					if statistics.QueueRecords > config.MaximumQueueRecords ||
						statistics.QueueBytes > config.MaximumQueueBytes {
						errorsFound <- fmt.Errorf(
							"raw writer exceeded configured bounds: %+v", statistics,
						)
						return
					}
				}
			}
		}(agent)
	}
	writers.Wait()
	close(errorsFound)
	for writeErr := range errorsFound {
		if writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := manager.Flush(ctx, Watermark{}); err != nil {
		t.Fatal(err)
	}
	statistics := manager.Statistics()
	if records := repository.snapshot(); len(records) != recordCount {
		t.Fatalf("durable raw envelopes = %d, want %d", len(records), recordCount)
	}
	if statistics.BatchCommits >= recordCount/4 {
		t.Fatalf(
			"batching did not substantially reduce commits: records=%d commits=%d",
			recordCount, statistics.BatchCommits,
		)
	}
	repository.mu.Lock()
	for _, size := range repository.batchSizes {
		if size > config.MaximumBatchRecords {
			repository.mu.Unlock()
			t.Fatalf("raw evidence transaction contained %d records", size)
		}
	}
	repository.mu.Unlock()
	if statistics.QueueRecords != 0 || statistics.QueueBytes != 0 ||
		statistics.DurableWatermark != recordCount {
		t.Fatalf("unexpected final writer statistics: %+v", statistics)
	}
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalScopeDrainsRequestAndFlushesItsFinalEvidence(t *testing.T) {
	repository := &memoryRepository{}
	manager, err := Open(context.Background(), Options{
		Repository: repository,
		Secrets:    newMemorySecrets(),
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x51}, 4096)),
		Clock:      fixedClock{value: time.Unix(1_790_000_000, 0).UTC()},
		Config: Config{
			MaximumQueueRecords: 8,
			MaximumQueueBytes:   1 << 20,
			MaximumBatchRecords: 8,
			MaximumBatchBytes:   1 << 20,
			FlushInterval:       time.Hour,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := manager.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	}()

	lease, err := manager.BeginScope(
		context.Background(), ScopeManagedRun, "capture-terminal",
	)
	if err != nil {
		t.Fatal(err)
	}
	observation := testObservation()
	observation.ScopeID = "capture-terminal"
	observation.ExchangeID = "exchange-terminal"
	if _, err := manager.Observe(context.Background(), observation); err != nil {
		t.Fatal(err)
	}

	prepared := make(chan TerminalScope, 1)
	prepareErrors := make(chan error, 1)
	go func() {
		terminal, prepareErr := manager.PrepareTerminalScope(
			context.Background(), ScopeManagedRun, "capture-terminal",
		)
		if prepareErr != nil {
			prepareErrors <- prepareErr
			return
		}
		prepared <- terminal
	}()

	deadline := time.Now().Add(time.Second)
	for {
		manager.scopeMu.Lock()
		phase := manager.scopes[scopeKey{
			kind: ScopeManagedRun,
			id:   "capture-terminal",
		}].phase
		manager.scopeMu.Unlock()
		if phase == scopePreparingTerminal {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal scope did not close request admission")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := manager.BeginScope(
		context.Background(), ScopeManagedRun, "capture-terminal",
	); !errors.Is(err, ErrScopeTerminal) {
		t.Fatalf("late request admission error = %v", err)
	}
	select {
	case terminal := <-prepared:
		terminal.Abort()
		t.Fatal("terminal scope completed while a request lease was active")
	case err := <-prepareErrors:
		t.Fatalf("terminal scope failed while draining: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	observation.Layer = LayerClientDownstream
	if _, err := manager.Observe(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
	lease.Release()
	var terminal TerminalScope
	select {
	case terminal = <-prepared:
	case err := <-prepareErrors:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("terminal scope did not finish after request release")
	}
	if records := repository.snapshot(); len(records) != 2 {
		t.Fatalf("durable terminal records = %d", len(records))
	}
	terminal.Commit()
	if _, err := manager.BeginScope(
		context.Background(), ScopeManagedRun, "capture-terminal",
	); !errors.Is(err, ErrScopeTerminal) {
		t.Fatalf("sealed request admission error = %v", err)
	}
}

func TestTerminalScopeAbortReopensRequestAdmission(t *testing.T) {
	manager, err := Open(context.Background(), Options{
		Repository: &memoryRepository{},
		Secrets:    newMemorySecrets(),
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x52}, 4096)),
		Clock:      fixedClock{value: time.Unix(1_790_000_000, 0).UTC()},
		Config:     DefaultConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := manager.PrepareTerminalScope(
		context.Background(), ScopeManualCapture, "manual-terminal",
	)
	if err != nil {
		t.Fatal(err)
	}
	terminal.Abort()
	lease, err := manager.BeginScope(
		context.Background(), ScopeManualCapture, "manual-terminal",
	)
	if err != nil {
		t.Fatalf("request admission remained closed after abort: %v", err)
	}
	lease.Release()
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagerRevealAuditsBeforeReturningPayload(t *testing.T) {
	repository := &memoryRepository{}
	manager, err := Open(context.Background(), Options{
		Repository: repository,
		Secrets:    newMemorySecrets(),
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x53}, 4096)),
		Clock:      fixedClock{value: time.Unix(1_790_000_000, 0).UTC()},
		Config:     DefaultConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	observation := testObservation()
	watermark, err := manager.Observe(context.Background(), observation)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Flush(context.Background(), watermark); err != nil {
		t.Fatal(err)
	}
	metadata, err := manager.ListExchange(
		context.Background(), observation.ExchangeID,
	)
	if err != nil || len(metadata) != 1 {
		t.Fatalf("metadata=%+v err=%v", metadata, err)
	}
	revealed, err := manager.Reveal(context.Background(), RevealRequest{
		EnvelopeID: metadata[0].EnvelopeID,
		ActorID:    "desktop-app:test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(revealed.Payload.Body) != `{"secret":"private"}` ||
		revealed.Metadata.EnvelopeID != metadata[0].EnvelopeID {
		t.Fatalf("unexpected reveal: %+v", revealed)
	}
	repository.mu.Lock()
	if len(repository.audits) != 1 ||
		repository.audits[0].Outcome != RevealSucceeded {
		t.Fatalf("reveal audits = %+v", repository.audits)
	}
	repository.mu.Unlock()
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagerListExchangeFlushesItsAdmittedWatermark(t *testing.T) {
	repository := &memoryRepository{}
	manager, err := Open(context.Background(), Options{
		Repository: repository,
		Secrets:    newMemorySecrets(),
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x57}, 4096)),
		Clock:      fixedClock{value: time.Unix(1_790_000_000, 0).UTC()},
		Config: Config{
			MaximumQueueRecords: 8,
			MaximumQueueBytes:   1 << 20,
			MaximumBatchRecords: 8,
			MaximumBatchBytes:   1 << 20,
			FlushInterval:       time.Hour,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := manager.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	}()

	observation := testObservation()
	if _, err := manager.Observe(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
	if records := repository.snapshot(); len(records) != 0 {
		t.Fatalf("record committed before the read barrier: %d", len(records))
	}

	metadata, err := manager.ListExchange(
		context.Background(), observation.ExchangeID,
	)
	if err != nil || len(metadata) != 1 {
		t.Fatalf("metadata=%+v err=%v", metadata, err)
	}
	statistics := manager.Statistics()
	if statistics.DurableWatermark != 1 || statistics.QueueRecords != 0 {
		t.Fatalf("read barrier did not make evidence durable: %+v", statistics)
	}
	manager.admissionMu.Lock()
	_, retained := manager.exchangeLatest[observation.ExchangeID]
	manager.admissionMu.Unlock()
	if retained {
		t.Fatal("durable Exchange read watermark was retained")
	}
}

func TestManagerRecoveryReportsUncleanPredecessor(t *testing.T) {
	repository := &memoryRepository{}
	first, err := Open(context.Background(), Options{
		Repository: repository,
		Secrets:    newMemorySecrets(),
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x54}, 4096)),
		Clock:      fixedClock{value: time.Unix(1_790_000_000, 0).UTC()},
		Config:     DefaultConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately do not shut the first manager down: this models a process
	// crash whose queue could lose at most its configured flush window.
	second, err := Open(context.Background(), Options{
		Repository: repository,
		Secrets:    newMemorySecrets(),
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x55}, 4096)),
		Clock:      fixedClock{value: time.Unix(1_790_000_001, 0).UTC()},
		Config:     DefaultConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	recovery := second.Recovery()
	if recovery.RecoveredUncleanWriters != 1 ||
		recovery.MaximumPossibleLoss != DefaultConfig().FlushInterval {
		t.Fatalf("unexpected manager recovery: %+v", recovery)
	}
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := second.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagerMetadataOnlyStoresDigestWithoutCiphertext(t *testing.T) {
	repository := &memoryRepository{}
	manager, err := Open(context.Background(), Options{
		Repository: repository,
		Secrets:    newMemorySecrets(),
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x42}, 4096)),
		Clock:      fixedClock{value: time.Unix(1_790_000_000, 0).UTC()},
		Config:     DefaultConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	observation := testObservation()
	observation.Recording = RecordingMetadataOnly
	watermark, err := manager.Observe(context.Background(), observation)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Flush(context.Background(), watermark); err != nil {
		t.Fatal(err)
	}
	record := repository.snapshot()[0]
	if record.PayloadState != PayloadMetadataOnly ||
		len(record.Ciphertext) != 0 || record.BodyBytes == 0 ||
		record.DigestScope != DigestFull {
		t.Fatalf("unexpected metadata-only record: %#v", record)
	}
	if _, err := manager.Decrypt(context.Background(), record); err == nil {
		t.Fatal("metadata-only evidence unexpectedly decrypted")
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagerMetadataOnlyDoesNotRequireWritableSecretStore(t *testing.T) {
	repository := &memoryRepository{}
	manager, err := Open(context.Background(), Options{
		Repository: repository,
		Secrets:    readOnlyMissingSecrets{},
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x44}, 4096)),
		Clock:      fixedClock{value: time.Unix(1_790_000_000, 0).UTC()},
		Config:     DefaultConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	observation := testObservation()
	observation.Recording = RecordingMetadataOnly
	watermark, err := manager.Observe(context.Background(), observation)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Flush(context.Background(), watermark); err != nil {
		t.Fatal(err)
	}
	if got := repository.snapshot(); len(got) != 1 ||
		got[0].PayloadState != PayloadMetadataOnly {
		t.Fatalf("metadata-only records = %#v", got)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagerShutdownFlushesAdmittedRecords(t *testing.T) {
	repository := &memoryRepository{}
	manager, err := Open(context.Background(), Options{
		Repository: repository,
		Secrets:    newMemorySecrets(),
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x43}, 4096)),
		Clock:      fixedClock{value: time.Unix(1_790_000_000, 0).UTC()},
		Config: Config{
			MaximumQueueRecords: 8,
			MaximumQueueBytes:   1 << 20,
			MaximumBatchRecords: 8,
			MaximumBatchBytes:   1 << 20,
			FlushInterval:       time.Hour,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		if _, err := manager.Observe(context.Background(), testObservation()); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repository.snapshot()) != 3 {
		t.Fatal("shutdown did not flush admitted raw evidence")
	}
	if _, err := manager.Observe(context.Background(), testObservation()); err == nil {
		t.Fatal("closed writer admitted new evidence")
	}
}

func TestManagerFlushScopeUsesTheScopesLatestAdmittedWatermark(t *testing.T) {
	repository := &memoryRepository{}
	manager, err := Open(context.Background(), Options{
		Repository: repository,
		Secrets:    newMemorySecrets(),
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x45}, 4096)),
		Clock:      fixedClock{value: time.Unix(1_790_000_000, 0).UTC()},
		Config: Config{
			MaximumQueueRecords: 8,
			MaximumQueueBytes:   1 << 20,
			MaximumBatchRecords: 8,
			MaximumBatchBytes:   1 << 20,
			FlushInterval:       time.Hour,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := testObservation()
	first.ScopeID = "capture-a"
	first.ExchangeID = "exchange-a"
	if _, err := manager.Observe(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := testObservation()
	second.ScopeKind = ScopeManualCapture
	second.ScopeID = "capture-b"
	second.ExchangeID = "exchange-b"
	if _, err := manager.Observe(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := manager.FlushScope(
		context.Background(),
		ScopeManagedRun,
		"capture-a",
	); err != nil {
		t.Fatal(err)
	}
	if manager.Statistics().DurableWatermark < 1 {
		t.Fatalf("scope watermark was not durable: %#v", manager.Statistics())
	}
	manager.admissionMu.Lock()
	_, retained := manager.scopeLatest[scopeKey{
		kind: ScopeManagedRun,
		id:   "capture-a",
	}]
	manager.admissionMu.Unlock()
	if retained {
		t.Fatal("durable terminal scope watermark was retained")
	}
	if err := manager.FlushScope(
		context.Background(),
		ScopeManagedRun,
		"capture-with-no-evidence",
	); err != nil {
		t.Fatalf("empty scope flush = %v", err)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func testObservation() Observation {
	return Observation{
		Context: Context{
			ScopeKind:              ScopeManagedRun,
			ScopeID:                "capture-1",
			ExchangeID:             "exchange-1",
			ConnectionID:           "connection-1",
			EnvironmentID:          "environment-1",
			EnvironmentRevision:    1,
			ClientEndpointID:       "endpoint-1",
			ClientEndpointRevision: 1,
			ProtocolPlanID:         "protocol-1",
			ProtocolPlanRevision:   1,
			RouteID:                "route-1",
			RouteRevision:          1,
			Recording:              RecordingFull,
			RetentionDays:          30,
		},
		Layer:      LayerClientIngress,
		ObservedAt: time.Unix(1_790_000_000, 0).UTC(),
		Method:     http.MethodPost,
		Scheme:     "https",
		Authority:  "api.anthropic.com",
		Path:       "/v1/messages",
		Headers: http.Header{
			"Authorization": []string{"Bearer private-token"},
			"Content-Type":  []string{"application/json"},
		},
		Body:           []byte(`{"secret":"private"}`),
		Complete:       true,
		Representation: "http_message",
		ContentType:    "application/json",
	}
}

type fixedClock struct{ value time.Time }

func (clock fixedClock) Now() time.Time { return clock.value }

type memoryRepository struct {
	mu             sync.Mutex
	records        []StoredEnvelope
	commits        int
	batchSizes     []int
	sessions       map[string]WriterSession
	closedSessions map[string]time.Time
	audits         []RevealAudit
}

func (repository *memoryRepository) AppendBatch(
	_ context.Context,
	records []StoredEnvelope,
	_ time.Time,
) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	for _, record := range records {
		if err := record.Validate(); err != nil {
			return err
		}
		record.CipherNonce = slices.Clone(record.CipherNonce)
		record.Ciphertext = slices.Clone(record.Ciphertext)
		repository.records = append(repository.records, record)
	}
	repository.commits++
	repository.batchSizes = append(repository.batchSizes, len(records))
	return nil
}

func (repository *memoryRepository) GetEnvelope(
	_ context.Context,
	envelopeID string,
) (StoredEnvelope, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	for _, record := range repository.records {
		if record.EnvelopeID == envelopeID {
			return record, nil
		}
	}
	return StoredEnvelope{}, ErrEnvelopeNotFound
}

func (repository *memoryRepository) AppendRevealAudit(
	_ context.Context,
	audit RevealAudit,
) error {
	if err := audit.Validate(); err != nil {
		return err
	}
	repository.mu.Lock()
	repository.audits = append(repository.audits, audit)
	repository.mu.Unlock()
	return nil
}

func (repository *memoryRepository) BeginWriterSession(
	_ context.Context,
	session WriterSession,
) (Recovery, error) {
	if err := session.Validate(); err != nil {
		return Recovery{}, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.sessions == nil {
		repository.sessions = make(map[string]WriterSession)
	}
	recovery := Recovery{}
	for id, previous := range repository.sessions {
		if _, closed := repository.closedSessions[id]; !closed {
			recovery.RecoveredUncleanWriters++
			if previous.MaximumUnflushedTime > recovery.MaximumPossibleLoss {
				recovery.MaximumPossibleLoss = previous.MaximumUnflushedTime
			}
		}
	}
	repository.sessions[session.WriterID] = session
	return recovery, nil
}

func (repository *memoryRepository) CloseWriterSession(
	_ context.Context,
	writerID string,
	now time.Time,
) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, ok := repository.sessions[writerID]; !ok {
		return errors.New("raw evidence writer session was not found")
	}
	if repository.closedSessions == nil {
		repository.closedSessions = make(map[string]time.Time)
	}
	repository.closedSessions[writerID] = now
	return nil
}

func (repository *memoryRepository) ListExchange(
	_ context.Context,
	exchangeID string,
) ([]StoredEnvelope, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	var records []StoredEnvelope
	for _, record := range repository.records {
		if record.ExchangeID == exchangeID {
			records = append(records, record)
		}
	}
	return records, nil
}

func (repository *memoryRepository) snapshot() []StoredEnvelope {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return slices.Clone(repository.records)
}

type memorySecretItem struct {
	value    []byte
	revision secretstore.Revision
}

type memorySecrets struct {
	mu    sync.Mutex
	items map[string]memorySecretItem
}

func newMemorySecrets() *memorySecrets {
	return &memorySecrets{items: make(map[string]memorySecretItem)}
}

func (store *memorySecrets) Read(
	_ context.Context,
	reference secretstore.Reference,
) (*secretstore.Value, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	item, ok := store.items[reference.String()]
	if !ok {
		return nil, secretstore.ErrNotFound
	}
	return secretstore.NewValue(item.value)
}

func (store *memorySecrets) ReadAtRevision(
	_ context.Context,
	reference secretstore.Reference,
	expected secretstore.Revision,
) (*secretstore.Value, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	item, ok := store.items[reference.String()]
	if !ok {
		return nil, secretstore.ErrNotFound
	}
	if item.revision != expected {
		return nil, secretstore.ErrRevisionConflict
	}
	return secretstore.NewValue(item.value)
}

func (store *memorySecrets) Inspect(
	_ context.Context,
	reference secretstore.Reference,
) (secretstore.Metadata, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	item, ok := store.items[reference.String()]
	if !ok {
		return secretstore.Metadata{State: secretstore.StateMissing}, nil
	}
	return secretstore.Metadata{
		State: secretstore.StateConfigured, Revision: item.revision,
	}, nil
}

func (store *memorySecrets) Replace(
	_ context.Context,
	command secretstore.ReplaceCommand,
) (secretstore.Metadata, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	item, exists := store.items[command.Reference.String()]
	if (!exists && command.ExpectedRevision != 0) ||
		(exists && item.revision != command.ExpectedRevision) {
		return secretstore.Metadata{}, secretstore.ErrRevisionConflict
	}
	value, err := command.Value.CopyBytes()
	if err != nil {
		return secretstore.Metadata{}, err
	}
	item = memorySecretItem{value: value, revision: item.revision + 1}
	store.items[command.Reference.String()] = item
	return secretstore.Metadata{
		State: secretstore.StateConfigured, Revision: item.revision,
	}, nil
}

func (store *memorySecrets) Delete(
	_ context.Context,
	reference secretstore.Reference,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.items[reference.String()]; !ok {
		return secretstore.ErrNotFound
	}
	delete(store.items, reference.String())
	return nil
}

var _ secretstore.Store = (*memorySecrets)(nil)

type readOnlyMissingSecrets struct{}

func (readOnlyMissingSecrets) Read(
	context.Context,
	secretstore.Reference,
) (*secretstore.Value, error) {
	return nil, secretstore.ErrNotFound
}

func (readOnlyMissingSecrets) ReadAtRevision(
	context.Context,
	secretstore.Reference,
	secretstore.Revision,
) (*secretstore.Value, error) {
	return nil, secretstore.ErrNotFound
}

func (readOnlyMissingSecrets) Inspect(
	context.Context,
	secretstore.Reference,
) (secretstore.Metadata, error) {
	return secretstore.Metadata{State: secretstore.StateMissing}, nil
}

func (readOnlyMissingSecrets) Replace(
	context.Context,
	secretstore.ReplaceCommand,
) (secretstore.Metadata, error) {
	return secretstore.Metadata{}, secretstore.ErrReadOnly
}

func (readOnlyMissingSecrets) Delete(
	context.Context,
	secretstore.Reference,
) error {
	return secretstore.ErrNotFound
}
