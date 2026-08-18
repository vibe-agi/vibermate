package exchangecontent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestManagerPurgesExpiredEvidenceAndRejectsWorkAfterShutdown(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC)
	repository := &repositoryDouble{records: make(map[string]Record)}
	manager, err := New(context.Background(), Options{
		Repository: repository,
		Clock:      fixedClock{now: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, response := evidenceFixture(t)
	record, err := NewRecord(
		"exchange-manager", frozenFixture(),
		environment.DefaultContentRecordingPolicy(), now, request, &response,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Record(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	got, err := manager.Get(context.Background(), record.ExchangeID)
	if err != nil || got.Response == nil {
		t.Fatalf("Get() = %+v, %v", got, err)
	}
	got.Request.Messages[0].Blocks[0].Text = "mutated"
	again, err := manager.Get(context.Background(), record.ExchangeID)
	if err != nil || again.Request.Messages[0].Blocks[0].Text == "mutated" {
		t.Fatal("Manager returned an aliased content record")
	}
	projection, err := manager.GetProjection(
		context.Background(), record.ExchangeID, RequestViewIncremental,
	)
	if err != nil || len(projection.Request.Messages) != 1 ||
		projection.TotalMessageCount != 1 {
		t.Fatalf("GetProjection() = %+v, %v", projection, err)
	}
	projection.Request.Messages[0].Blocks[0].Text = "mutated"
	againProjection, err := manager.GetProjection(
		context.Background(), record.ExchangeID, RequestViewIncremental,
	)
	if err != nil || againProjection.Request.Messages[0].Blocks[0].Text == "mutated" {
		t.Fatal("Manager returned an aliased content projection")
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Get(context.Background(), record.ExchangeID); !errors.Is(err, ErrRuntimeStopping) {
		t.Fatalf("Get() after shutdown error = %v", err)
	}
	if _, err := manager.GetProjection(
		context.Background(), record.ExchangeID, RequestViewIncremental,
	); !errors.Is(err, ErrRuntimeStopping) {
		t.Fatalf("GetProjection() after shutdown error = %v", err)
	}
	if err := manager.Record(context.Background(), record); !errors.Is(err, ErrRuntimeStopping) {
		t.Fatalf("Record() after shutdown error = %v", err)
	}
}

type fixedClock struct {
	now time.Time
}

func (clock fixedClock) Now() time.Time { return clock.now }

type repositoryDouble struct {
	mu      sync.Mutex
	records map[string]Record
}

func (repository *repositoryDouble) Put(_ context.Context, record Record) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.records[record.ExchangeID]; exists {
		return ErrInvalidEvidence
	}
	repository.records[record.ExchangeID] = record.Clone()
	return nil
}

func (repository *repositoryDouble) Get(_ context.Context, exchangeID string, now time.Time) (Record, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	record, exists := repository.records[exchangeID]
	if !exists || !record.ExpiresAt.After(now) {
		return Record{}, ErrNotFound
	}
	return record.Clone(), nil
}

func (repository *repositoryDouble) GetProjection(
	_ context.Context,
	exchangeID string,
	now time.Time,
	view RequestView,
) (Projection, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	record, exists := repository.records[exchangeID]
	if !exists || !record.ExpiresAt.After(now) {
		return Projection{}, ErrNotFound
	}
	return Project(record, view)
}

func (repository *repositoryDouble) PurgeExpired(_ context.Context, now time.Time) (uint64, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	var purged uint64
	for exchangeID, record := range repository.records {
		if !record.ExpiresAt.After(now) {
			delete(repository.records, exchangeID)
			purged++
		}
	}
	return purged, nil
}
