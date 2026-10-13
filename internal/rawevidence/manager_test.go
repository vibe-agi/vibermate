package rawevidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestManagerBatchesAndFlushesWatermark(t *testing.T) {
	repository := &memoryRepository{}
	manager, err := Open(context.Background(), Options{
		Repository: repository,
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
	// The credential is absent because it was redacted, and the body is present
	// because the payload is deliberately stored in the clear.
	if bytes.Contains(records[0].PayloadMetadata, []byte("Bearer private-token")) {
		t.Fatal("stored payload retained a credential value")
	}
	payload, err := manager.ReadPayload(records[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(payload.Body) != `{"secret":"private"}` ||
		len(payload.Headers) != 2 ||
		!slices.Equal(
			records[0].RedactedCredentialFields, []string{"Authorization"},
		) {
		t.Fatalf("unexpected payload or metadata: %#v %#v", payload, records[0])
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

func TestManagerFlushWaitDoesNotBlockObservationAdmission(t *testing.T) {
	repository := &blockingAppendRepository{
		memoryRepository: &memoryRepository{},
		appendStarted:    make(chan struct{}),
		appendRelease:    make(chan struct{}),
	}
	manager, err := Open(context.Background(), Options{
		Repository: repository,
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x58}, 4096)),
		Clock:      fixedClock{value: time.Unix(1_790_000_000, 0).UTC()},
		Config: Config{
			MaximumQueueRecords: 4,
			MaximumQueueBytes:   1 << 20,
			MaximumBatchRecords: 1,
			MaximumBatchBytes:   1 << 20,
			FlushInterval:       time.Hour,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			close(repository.appendRelease)
		}
		shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if shutdownErr := manager.Shutdown(shutdownContext); shutdownErr != nil {
			t.Errorf("shutdown raw evidence manager: %v", shutdownErr)
		}
	}()

	first, err := manager.Observe(context.Background(), testObservation())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-repository.appendStarted:
	case <-time.After(time.Second):
		t.Fatal("raw evidence writer did not start its blocked commit")
	}

	flushContext, cancelFlush := context.WithTimeout(context.Background(), time.Second)
	defer cancelFlush()
	flushDone := make(chan error, 1)
	go func() { flushDone <- manager.Flush(flushContext, first) }()
	// Give Flush an opportunity to wait on the busy writer. Observation
	// admission must remain context-aware while that wait is outstanding.
	time.Sleep(20 * time.Millisecond)

	second := testObservation()
	second.ExchangeID = "exchange-raw-2"
	observeContext, cancelObserve := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelObserve()
	observeDone := make(chan error, 1)
	go func() {
		_, observeErr := manager.Observe(observeContext, second)
		observeDone <- observeErr
	}()
	select {
	case observeErr := <-observeDone:
		if observeErr != nil {
			t.Fatalf("admit observation while flush waits: %v", observeErr)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("observation admission was blocked by an unrelated flush wait")
	}

	close(repository.appendRelease)
	released = true
	if err := <-flushDone; err != nil {
		t.Fatalf("flush after repository release: %v", err)
	}
}

func TestTerminalScopeDrainsRequestAndFlushesItsFinalEvidence(t *testing.T) {
	repository := &memoryRepository{}
	manager, err := Open(context.Background(), Options{
		Repository: repository,
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

func TestManagerMetadataOnlyStoresDigestWithoutPayload(t *testing.T) {
	repository := &memoryRepository{}
	manager, err := Open(context.Background(), Options{
		Repository: repository,
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
		len(record.PayloadMetadata) != 0 || len(record.Body) != 0 ||
		record.BodyBytes == 0 ||
		record.DigestScope != DigestFull {
		t.Fatalf("unexpected metadata-only record: %#v", record)
	}
	if _, err := manager.ReadPayload(record); err == nil {
		t.Fatal("metadata-only evidence unexpectedly decrypted")
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagerShutdownFlushesAdmittedRecords(t *testing.T) {
	repository := &memoryRepository{}
	manager, err := Open(context.Background(), Options{
		Repository: repository,
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

func TestManagerDurableUnreadExchangeFencesAreReleased(t *testing.T) {
	manager := openFenceTestManager(t, &memoryRepository{}, DefaultConfig())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for index := 0; index < 5000; index++ {
		observation := testObservation()
		observation.ExchangeID = fmt.Sprintf("unread-exchange-%d", index)
		observation.ScopeID = fmt.Sprintf("unread-scope-%d", index)
		if _, err := manager.Observe(ctx, observation); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Flush(ctx, Watermark{}); err != nil {
		t.Fatal(err)
	}
	// Flush promises durability and may return via its durable-watermark fast
	// path. Shutdown also joins the worker, so cleanup has certainly completed.
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	assertNoDurableFences(t, manager)
}

func TestManagerDurableFenceKeepsNewerWatermark(t *testing.T) {
	for _, test := range []struct {
		name         string
		sameExchange bool
		sameScope    bool
	}{
		{name: "same exchange and scope", sameExchange: true, sameScope: true},
		{name: "same exchange different scope kind", sameExchange: true},
		{name: "same scope different exchange", sameScope: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &blockingAppendRepository{
				memoryRepository: &memoryRepository{},
				appendStarted:    make(chan struct{}), appendRelease: make(chan struct{}),
				nextAppendStarted: make(chan struct{}), nextAppendRelease: make(chan struct{}),
			}
			// Release both gates before the manager's registered shutdown cleanup.
			manager := openFenceTestManager(t, repository, Config{
				MaximumQueueRecords: 2, MaximumQueueBytes: 1 << 20,
				MaximumBatchRecords: 1, MaximumBatchBytes: 1 << 20, FlushInterval: time.Hour,
			})
			t.Cleanup(func() {
				select {
				case <-repository.appendRelease:
				default:
					close(repository.appendRelease)
				}
				select {
				case <-repository.nextAppendRelease:
				default:
					close(repository.nextAppendRelease)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			firstObservation := testObservation()
			if _, err := manager.Observe(ctx, firstObservation); err != nil {
				t.Fatal(err)
			}
			waitFenceAppend(t, ctx, repository.appendStarted)
			secondObservation := testObservation()
			if !test.sameExchange {
				secondObservation.ExchangeID = "newer-exchange"
			}
			if !test.sameScope {
				secondObservation.ScopeKind = ScopeManualCapture
			}
			second, err := manager.Observe(ctx, secondObservation)
			if err != nil {
				t.Fatal(err)
			}
			close(repository.appendRelease)
			// The next AppendBatch starts only after the previous commit's whole
			// success path has returned. Its gate keeps the newer record pending.
			waitFenceAppend(t, ctx, repository.nextAppendStarted)
			manager.admissionMu.Lock()
			exchangeFence := manager.exchangeLatest[firstObservation.ExchangeID]
			scopeFence := manager.scopeLatest[scopeKey{kind: firstObservation.ScopeKind, id: firstObservation.ScopeID}]
			newExchangeFence := manager.exchangeLatest[secondObservation.ExchangeID]
			newScopeFence := manager.scopeLatest[scopeKey{kind: secondObservation.ScopeKind, id: secondObservation.ScopeID}]
			manager.admissionMu.Unlock()
			wantExchange, wantScope := uint64(0), uint64(0)
			if test.sameExchange {
				wantExchange = second.Sequence
			}
			if test.sameScope {
				wantScope = second.Sequence
			}
			if exchangeFence != wantExchange || scopeFence != wantScope ||
				newExchangeFence != second.Sequence || newScopeFence != second.Sequence {
				t.Fatalf("old fences exchange=%d scope=%d, want %d/%d; newer fences=%d/%d want %d",
					exchangeFence, scopeFence, wantExchange, wantScope, newExchangeFence, newScopeFence, second.Sequence)
			}
			readDone := make(chan error, 1)
			go func() {
				metadata, err := manager.ListExchange(ctx, secondObservation.ExchangeID)
				want := 1
				if test.sameExchange {
					want = 2
				}
				if err == nil && len(metadata) != want {
					err = fmt.Errorf("read returned %d envelopes, want %d", len(metadata), want)
				}
				readDone <- err
			}()
			select {
			case err := <-readDone:
				t.Fatalf("read passed an uncommitted newer watermark: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			close(repository.nextAppendRelease)
			if err := <-readDone; err != nil {
				t.Fatal(err)
			}
			if err := manager.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			assertNoDurableFences(t, manager)
		})
	}
}

func TestManagerFailedCommitKeepsReadFencesAndSlots(t *testing.T) {
	repository := &failingFenceRepository{memoryRepository: &memoryRepository{}, fail: true}
	manager := openFenceTestManager(t, repository, Config{
		MaximumQueueRecords: 2, MaximumQueueBytes: 1 << 20,
		MaximumBatchRecords: 2, MaximumBatchBytes: 1 << 20, FlushInterval: time.Hour,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	observation := testObservation()
	mark, err := manager.Observe(ctx, observation)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Flush(ctx, mark); err == nil {
		t.Fatal("failed commit reported success")
	}
	manager.admissionMu.Lock()
	exchangeFence := manager.exchangeLatest[observation.ExchangeID]
	scopeFence := manager.scopeLatest[scopeKey{kind: observation.ScopeKind, id: observation.ScopeID}]
	manager.admissionMu.Unlock()
	stats := manager.Statistics()
	if exchangeFence != mark.Sequence || scopeFence != mark.Sequence || stats.DurableWatermark != 0 ||
		stats.QueueRecords != 1 || stats.QueueBytes == 0 || stats.LastFailure == "" {
		t.Fatalf("failed commit lost pending work: exchange=%d scope=%d stats=%+v", exchangeFence, scopeFence, stats)
	}
	if manager.recordSlots.TryAcquire(2) {
		manager.recordSlots.Release(2)
		t.Fatal("failed commit released its record slot")
	}
	if manager.byteSlots.TryAcquire(manager.config.MaximumQueueBytes) {
		manager.byteSlots.Release(manager.config.MaximumQueueBytes)
		t.Fatal("failed commit released its byte slots")
	}
	if _, err := manager.ListExchange(ctx, observation.ExchangeID); err == nil {
		t.Fatal("read bypassed failed commit fence")
	}
	repository.mu.Lock()
	repository.fail = false
	repository.mu.Unlock()
	if err := manager.Flush(ctx, mark); err != nil {
		t.Fatal(err)
	}
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	assertNoDurableFences(t, manager)
}

func TestManagerDurableFenceCapacity100000UnreadExchanges(t *testing.T) {
	if testing.Short() {
		t.Skip("100000-request capacity measurement")
	}
	// This repository validates writes but discards committed records. Its
	// archive cannot obscure the writer's retained memory.
	repository := &discardFenceRepository{memoryRepository: &memoryRepository{}}
	manager := openFenceTestManager(t, repository, DefaultConfig())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for index := 0; index < 100000; index++ {
		observation := testObservation()
		observation.ExchangeID = fmt.Sprintf("capacity-exchange-%d", index)
		observation.ScopeID = fmt.Sprintf("capacity-scope-%d", index)
		if _, err := manager.Observe(ctx, observation); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Flush(ctx, Watermark{}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	assertNoDurableFences(t, manager)
	stats := manager.Statistics()
	if stats.DurableWatermark != 100000 || stats.QueueRecords != 0 || stats.QueueBytes != 0 || len(manager.records) != 0 {
		t.Fatalf("capacity workload retained queued work: %+v channel=%d", stats, len(manager.records))
	}
	if !manager.recordSlots.TryAcquire(int64(manager.config.MaximumQueueRecords)) {
		t.Fatal("capacity workload retained record slots")
	}
	manager.recordSlots.Release(int64(manager.config.MaximumQueueRecords))
	if !manager.byteSlots.TryAcquire(manager.config.MaximumQueueBytes) {
		t.Fatal("capacity workload retained byte slots")
	}
	manager.byteSlots.Release(manager.config.MaximumQueueBytes)
	runtime.GC()
	runtime.ReadMemStats(&after)
	manager.scopeMu.Lock()
	scopes := len(manager.scopes)
	manager.scopeMu.Unlock()
	t.Logf("requests=100000 exchange_fences=0 scope_fences=0 queue_records=%d queue_bytes=%d channel=%d record_slots_used=0 byte_slots_used=0 scope_states=%d heap_delta_bytes=%d allocated_bytes=%d batch_commits=%d",
		stats.QueueRecords, stats.QueueBytes, len(manager.records), scopes, int64(after.HeapAlloc)-int64(before.HeapAlloc), after.TotalAlloc-before.TotalAlloc, stats.BatchCommits)
	runtime.KeepAlive(manager)
}

func TestTerminalScopeDurableFenceCleanupPreservesLateAdmissionGuard(t *testing.T) {
	manager := openFenceTestManager(t, &discardFenceRepository{memoryRepository: &memoryRepository{}}, DefaultConfig())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for index := 0; index < 5000; index++ {
		id := fmt.Sprintf("sealed-capacity-scope-%d", index)
		lease, err := manager.BeginScope(ctx, ScopeManagedRun, id)
		if err != nil {
			t.Fatal(err)
		}
		observation := testObservation()
		observation.ScopeID, observation.ExchangeID = id, fmt.Sprintf("sealed-exchange-%d", index)
		if _, err := manager.Observe(ctx, observation); err != nil {
			lease.Release()
			t.Fatal(err)
		}
		lease.Release()
		terminal, err := manager.PrepareTerminalScope(ctx, ScopeManagedRun, id)
		if err != nil {
			t.Fatal(err)
		}
		terminal.Commit()
		if lateLease, err := manager.BeginScope(ctx, ScopeManagedRun, id); !errors.Is(err, ErrScopeTerminal) {
			if lateLease != nil {
				lateLease.Release()
			}
			t.Fatalf("sealed scope %d admitted a late request: %v", index, err)
		}
	}
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	assertNoDurableFences(t, manager)
	manager.scopeMu.Lock()
	sealed := 0
	for _, state := range manager.scopes {
		if state.phase == scopeSealed && state.active == 0 {
			sealed++
		}
	}
	scopes := len(manager.scopes)
	manager.scopeMu.Unlock()
	if sealed != 5000 || scopes != 5000 || manager.Statistics().AdmittedRecords != 5000 {
		t.Fatalf("scope lifecycle states=%d sealed=%d stats=%+v", scopes, sealed, manager.Statistics())
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	t.Logf("terminal_scopes=5000 sealed_tombstones=%d active_requests=0 late_requests_denied=5000 exchange_fences=0 scope_fences=0 heap_delta_bytes=%d", sealed, int64(after.HeapAlloc)-int64(before.HeapAlloc))
	runtime.KeepAlive(manager)
}

func openFenceTestManager(t *testing.T, repository Repository, config Config) *Manager {
	t.Helper()
	manager, err := Open(context.Background(), Options{
		Repository: repository, Random: bytes.NewReader(bytes.Repeat([]byte{0x59}, 64)),
		Clock: fixedClock{value: time.Unix(1_790_000_000, 0).UTC()}, Config: config,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Errorf("shutdown fence test manager: %v", err)
		}
	})
	return manager
}

func assertNoDurableFences(t *testing.T, manager *Manager) {
	t.Helper()
	manager.admissionMu.Lock()
	exchanges, scopes := len(manager.exchangeLatest), len(manager.scopeLatest)
	manager.admissionMu.Unlock()
	if exchanges != 0 || scopes != 0 {
		t.Fatalf("durable unread exchanges retained %d exchange and %d scope read fences", exchanges, scopes)
	}
}

func waitFenceAppend(t *testing.T, ctx context.Context, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("writer did not reach blocked AppendBatch")
	}
}

type failingFenceRepository struct {
	*memoryRepository
	fail bool
}

func (repository *failingFenceRepository) AppendBatch(ctx context.Context, records []StoredEnvelope, now time.Time) error {
	repository.mu.Lock()
	fail := repository.fail
	repository.mu.Unlock()
	if fail {
		return errors.New("synthetic append failure")
	}
	return repository.memoryRepository.AppendBatch(ctx, records, now)
}

type discardFenceRepository struct{ *memoryRepository }

func (repository *discardFenceRepository) AppendBatch(_ context.Context, records []StoredEnvelope, _ time.Time) error {
	for _, record := range records {
		if err := record.Validate(); err != nil {
			return err
		}
	}
	return nil
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
	salt           []byte
	saltErr        error
}

// RedactionSalt models the durable load-or-create the real repository performs:
// one salt per database, stable for its lifetime.
func (repository *memoryRepository) RedactionSalt(
	_ context.Context,
) ([]byte, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.saltErr != nil {
		return nil, repository.saltErr
	}
	if repository.salt == nil {
		repository.salt = bytes.Repeat([]byte{0x5C}, RedactionSaltBytes)
	}
	return slices.Clone(repository.salt), nil
}

type blockingAppendRepository struct {
	*memoryRepository
	appendStarted     chan struct{}
	appendRelease     chan struct{}
	startOnce         sync.Once
	nextAppendStarted chan struct{}
	nextAppendRelease chan struct{}
	appendCalls       int
}

func (repository *blockingAppendRepository) AppendBatch(
	ctx context.Context,
	records []StoredEnvelope,
	now time.Time,
) error {
	repository.mu.Lock()
	repository.appendCalls++
	call := repository.appendCalls
	repository.mu.Unlock()
	if call == 2 && repository.nextAppendStarted != nil {
		close(repository.nextAppendStarted)
		select {
		case <-repository.nextAppendRelease:
			return repository.memoryRepository.AppendBatch(ctx, records, now)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	repository.startOnce.Do(func() { close(repository.appendStarted) })
	select {
	case <-repository.appendRelease:
		return repository.memoryRepository.AppendBatch(ctx, records, now)
	case <-ctx.Done():
		return ctx.Err()
	}
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
		record.PayloadMetadata = slices.Clone(record.PayloadMetadata)
		record.Body = slices.Clone(record.Body)
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
