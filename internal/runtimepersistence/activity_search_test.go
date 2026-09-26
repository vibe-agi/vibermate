package runtimepersistence

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

func TestActivitySearchCombinesMetadataAndHonorsContentExpiry(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	t.Cleanup(func() { shutdownTestStore(t, store) })
	recordedAt := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	appendSearchExchange(t, store, "older", recordedAt, activity.StatusSucceeded, "")
	latest := appendSearchExchange(
		t, store, "latest", recordedAt.Add(time.Minute),
		activity.StatusFailed, "provider_transport_failed",
	)
	query := activity.SearchQuery{
		Limit:             10,
		Text:              "codex",
		EnvironmentID:     "search-environment",
		AccountID:         "search-account",
		Model:             "astra",
		Tool:              "inspect",
		Status:            activity.StatusFailed,
		Reason:            "transport",
		OccurredAtOrAfter: recordedAt,
		OccurredBefore:    recordedAt.Add(time.Hour),
	}
	page, err := store.ActivityRepository().SearchExchanges(
		context.Background(),
		activity.SearchRequest{
			Query: query, ContentAvailableAt: recordedAt.Add(2 * time.Hour),
		},
	)
	if err != nil || len(page.Items) != 1 || page.Items[0].Record.ID != latest.ID {
		t.Fatalf("SearchExchanges() = %+v, %v", page, err)
	}
	hit := page.Items[0]
	if !hit.Context.ContentAvailable || hit.Context.RequestedModel != "gpt-6-astra" ||
		hit.Context.ReportedModel != "gpt-6-astra-20260925" ||
		!slices.Equal(hit.Context.ToolNames, []string{"inspect"}) {
		t.Fatalf("search context = %+v", hit.Context)
	}
	for _, kind := range []string{
		"environment", "account", "model", "tool", "status", "error", "source", "time",
	} {
		if !slices.Contains(hit.Matches, kind) {
			t.Fatalf("search matches = %v, missing %q", hit.Matches, kind)
		}
	}

	expired, err := store.ActivityRepository().SearchExchanges(
		context.Background(),
		activity.SearchRequest{
			Query:              activity.SearchQuery{Limit: 10, Model: "astra"},
			ContentAvailableAt: recordedAt.AddDate(1, 0, 0),
		},
	)
	if err != nil || len(expired.Items) != 0 {
		t.Fatalf("expired model search = %+v, %v", expired, err)
	}
	metadata, err := store.ActivityRepository().SearchExchanges(
		context.Background(),
		activity.SearchRequest{
			Query:              activity.SearchQuery{Limit: 10, AccountID: "search-account"},
			ContentAvailableAt: recordedAt.AddDate(1, 0, 0),
		},
	)
	if err != nil || len(metadata.Items) != 2 ||
		metadata.Items[0].Context.ContentAvailable {
		t.Fatalf("expired metadata search = %+v, %v", metadata, err)
	}
	body, err := store.ActivityRepository().SearchExchanges(
		context.Background(),
		activity.SearchRequest{
			Query:              activity.SearchQuery{Limit: 10, Text: "hello"},
			ContentAvailableAt: recordedAt.Add(2 * time.Hour),
		},
	)
	if err != nil || len(body.Items) != 0 {
		t.Fatalf("body-only search = %+v, %v", body, err)
	}
}

func TestActivitySearchPaginationUsesStableSequenceCursor(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	t.Cleanup(func() { shutdownTestStore(t, store) })
	recordedAt := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	appendSearchExchange(t, store, "first", recordedAt, activity.StatusSucceeded, "")
	appendSearchExchange(t, store, "second", recordedAt.Add(time.Minute), activity.StatusSucceeded, "")
	request := activity.SearchRequest{
		Query:              activity.SearchQuery{Limit: 1, Text: "codex"},
		ContentAvailableAt: recordedAt.Add(time.Hour),
	}
	first, err := store.ActivityRepository().SearchExchanges(context.Background(), request)
	if err != nil || len(first.Items) != 1 || first.NextBeforeSequence == 0 ||
		first.Items[0].Record.SubjectID != "exchange-second" {
		t.Fatalf("first search page = %+v, %v", first, err)
	}
	request.Query.BeforeSequence = first.NextBeforeSequence
	second, err := store.ActivityRepository().SearchExchanges(context.Background(), request)
	if err != nil || len(second.Items) != 1 || second.NextBeforeSequence != 0 ||
		second.Items[0].Record.SubjectID != "exchange-first" {
		t.Fatalf("second search page = %+v, %v", second, err)
	}
}

func appendSearchExchange(
	t testing.TB,
	store *Store,
	suffix string,
	observedAt time.Time,
	status activity.Status,
	reason string,
) activity.Record {
	t.Helper()
	exchangeID := "exchange-" + suffix
	content := contentRecordFixture(t, exchangeID, observedAt)
	content.Request.RequestedModel = "gpt-6-astra"
	content.Request.EffectiveModel = "gpt-6-astra"
	content.Request.Tools = []exchangecontent.ToolDefinition{{Name: "inspect"}}
	content.Response.RequestedModel = "gpt-6-astra"
	content.Response.EffectiveModel = "gpt-6-astra"
	content.Response.ReportedModel = "gpt-6-astra-20260925"
	if err := store.ExchangeContentRepository().Put(context.Background(), content); err != nil {
		t.Fatal(err)
	}
	record := activity.Record{
		ID: "activity-" + suffix, OccurredAt: observedAt,
		Kind: activity.KindExchangeCompleted, SubjectID: exchangeID,
		Status: status, ReasonCode: reason,
		SourceKind: activity.SourceCaptureRun, SourceDisplayName: "codex",
		SourceRecognition: activity.SourceRecognitionVerified,
		CaptureRunID:      "run-search", ConnectionID: "connection-" + suffix,
	}
	setFrozenExecutionEvidence(&record, "search")
	stored, err := store.ActivityRepository().Append(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}
