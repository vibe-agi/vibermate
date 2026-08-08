package runtimepersistence

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestExchangeContentRepositoryReopensAndExpiresEvidence(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openTestStore(t, databasePath)
	repository := store.ExchangeContentRepository()
	recordedAt := time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC)
	record := contentRecordFixture(t, "exchange-content", recordedAt)
	if err := repository.Put(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := repository.Put(context.Background(), record); err == nil {
		t.Fatal("duplicate content evidence was accepted")
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, databasePath)
	defer func() {
		if err := reopened.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	got, err := reopened.ExchangeContentRepository().Get(
		context.Background(), record.ExchangeID, recordedAt.Add(time.Hour),
	)
	if err != nil || got.ExchangeID != record.ExchangeID || got.Response == nil ||
		got.Response.Usage.Output.Tokens != 2 {
		t.Fatalf("Get() = %+v, %v", got, err)
	}
	if _, err := reopened.ExchangeContentRepository().Get(
		context.Background(), record.ExchangeID, record.ExpiresAt,
	); !errors.Is(err, exchangecontent.ErrNotFound) {
		t.Fatalf("expired Get() error = %v", err)
	}
	purged, err := reopened.ExchangeContentRepository().PurgeExpired(
		context.Background(), record.ExpiresAt,
	)
	if err != nil || purged != 1 {
		t.Fatalf("PurgeExpired() = %d, %v", purged, err)
	}
}

func contentRecordFixture(t *testing.T, exchangeID string, recordedAt time.Time) exchangecontent.Record {
	t.Helper()
	block, err := protocolcore.NewTextBlock("hello")
	if err != nil {
		t.Fatal(err)
	}
	request := protocolcore.Request{
		RequestedModel: "model", EffectiveModel: "model", MaxOutputTokens: 16,
		Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{block}}},
	}
	response := protocolcore.Response{
		ID: "response", RequestedModel: "model", EffectiveModel: "model", ReportedModel: "model",
		Blocks: []protocolcore.ContentBlock{block}, StopReason: protocolcore.StopReasonEndTurn,
		Usage: protocolcore.Usage{Output: protocolcore.UsageValue{Known: true, Tokens: 2, Source: "provider"}},
	}
	record, err := exchangecontent.NewRecord(
		exchangeID,
		exchangecontent.FrozenRef{
			EnvironmentID: "work", EnvironmentRevision: 1,
			EnvironmentDigest: strings.Repeat("a", 64),
			ClientEndpointID:  "endpoint", ClientEndpointRevision: 1,
			ProtocolPlanID: "plan", ProtocolPlanRevision: 1,
			RouteID: "route", RouteRevision: 1,
		},
		environment.DefaultContentRecordingPolicy(), recordedAt, request, &response,
	)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
