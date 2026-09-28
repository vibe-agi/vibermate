package runtimepersistence

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
)

func TestActivitySummaryUsesAllLifecycleWinnersAndExactSession(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "summary.db"))
	defer shutdownTestStore(t, store)
	repository := store.ActivityRepository()
	ctx := context.Background()
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	appendRequest := func(index int, run, manual, client string, status activity.Status) {
		t.Helper()
		id := fmt.Sprintf("summary-%d", index)
		record := activity.Record{
			ID: id + "-start", SubjectID: id, OccurredAt: now.Add(time.Duration(index) * time.Second),
			Kind: activity.KindExchangeStarted, Status: activity.StatusPending,
			CaptureRunID: run, ManualCaptureID: manual, ConnectionID: "connection-" + id,
			SourceKind: activity.SourceCaptureRun, SourceDisplayName: client, SourceRecognition: activity.SourceRecognitionVerified,
		}
		if manual != "" {
			record.SourceKind = activity.SourceManualProxy
		}
		setFrozenExecutionEvidence(&record, "summary")
		record.AccountID, record.AccountRevision, record.CredentialEpoch = "", 0, 0
		if _, err := repository.Append(ctx, record); err != nil {
			t.Fatal(err)
		}
		identity := agentconversation.ClientIdentity{
			Client: client, SessionID: "shared-session", SessionResumable: true,
			Source: agentconversation.ClientIdentitySourceProtocolEvidence, Confidence: "exact", ObservedAt: now,
		}
		if err := store.ConversationIdentityRepository().PutConversationIdentity(ctx, id, identity); err != nil {
			t.Fatal(err)
		}
		if status != activity.StatusPending {
			record.Kind, record.Status, record.ID = activity.KindExchangeCompleted, status, id+"-end"
			setFrozenExecutionEvidence(&record, "summary")
			record.OccurredAt = record.OccurredAt.Add(time.Millisecond)
			if status == activity.StatusFailed {
				record.ReasonCode = "provider_transport_failed"
			}
			if _, err := repository.Append(ctx, record); err != nil {
				t.Fatal(err)
			}
		}
	}
	for index := range 240 {
		status := activity.StatusSucceeded
		switch {
		case index >= 220:
			status = activity.StatusPending
		case index >= 210:
			status = activity.StatusCanceled
		case index >= 200:
			status = activity.StatusFailed
		}
		appendRequest(index, "run-a", "", "codex", status)
	}
	appendRequest(240, "run-b", "", "codex", activity.StatusSucceeded)
	appendRequest(241, "run-c", "", "claude", activity.StatusSucceeded)
	appendRequest(242, "", "manual-a", "codex", activity.StatusSucceeded)
	// Hold the writer connection: summary reads must use the read-only pool.
	writer, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	read, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	summary, err := repository.SummarizeExchanges(read, activity.SummaryScope{CaptureRunID: "run-a"})
	if err != nil || summary.Requests != 240 || summary.Succeeded != 200 || summary.Failed != 10 ||
		summary.Canceled != 10 || summary.Pending != 20 || len(summary.Failures) != 1 || summary.Failures[0].Count != 10 ||
		summary.FirstObservedAt == nil || summary.LastObservedAt == nil {
		t.Fatalf("complete run summary: %+v, %v", summary, err)
	}
	page, err := repository.ListExchanges(read, activity.PageRequest{CaptureRunID: "run-a", Limit: 100})
	if err != nil || len(page.Items) != 100 || page.NextBeforeSequence == 0 {
		t.Fatalf("independent paged records: %+v, %v", page, err)
	}
	for _, test := range []struct {
		scope activity.SummaryScope
		want  int
	}{
		{activity.SummaryScope{Client: "codex", SessionID: "shared-session"}, 242},
		{activity.SummaryScope{Client: "claude", SessionID: "shared-session"}, 1},
		{activity.SummaryScope{ManualCaptureID: "manual-a"}, 1},
		{activity.SummaryScope{CaptureRunID: "missing"}, 0},
	} {
		got, err := repository.SummarizeExchanges(read, test.scope)
		if err != nil || got.Requests != test.want || got.Scope != test.scope || got.Failures == nil {
			t.Fatalf("scope %+v: %+v, %v", test.scope, got, err)
		}
	}
	for _, scope := range []activity.SummaryScope{
		{}, {Client: "codex"}, {SessionID: "shared-session"},
		{CaptureRunID: "run-a", ManualCaptureID: "manual-a"},
		{CaptureRunID: "run-a", Client: "codex", SessionID: "shared-session"},
		{Client: "codex", SessionID: "bad\nidentity"},
	} {
		if _, err := repository.SummarizeExchanges(read, scope); err == nil {
			t.Fatalf("invalid summary scope accepted: %+v", scope)
		}
	}
}

func TestActivitySummaryScalesWithoutARequestLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("300k lifecycle-row scale check; run without -short to measure")
	}
	// Three lifecycle rows per request, spread across many launches. A tiny
	// native session must remain bounded even when the archive passes 100k.
	store := activityReadFixture(t, 300003)
	ctx := context.Background()
	if _, err := store.database.Exec(`DELETE FROM runtime_activities WHERE subject_id='exchange-0'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.Exec(`UPDATE runtime_activities SET capture_run_id='large-run'`); err != nil {
		t.Fatal(err)
	}
	if err := store.ConversationIdentityRepository().PutConversationIdentity(ctx, "exchange-10", agentconversation.ClientIdentity{
		Client: "codex", SessionID: "tiny-session", SessionResumable: true,
		Source: agentconversation.ClientIdentitySourceProtocolEvidence, Confidence: "exact", ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		scope activity.SummaryScope
		want  int
	}{
		{activity.SummaryScope{CaptureRunID: "large-run"}, 100001},
		{activity.SummaryScope{Client: "codex", SessionID: "tiny-session"}, 1},
	} {
		start := time.Now()
		// Timing is reported, not asserted: the pure-Go SQLite engine is much
		// slower under -race. Functional read/write isolation is checked above.
		got, err := store.ActivityRepository().SummarizeExchanges(ctx, test.scope)
		if err != nil || got.Requests != test.want {
			t.Fatalf("scope %+v: requests=%d error=%v", test.scope, got.Requests, err)
		}
		t.Logf("scope %+v: %d requests in %v", test.scope, got.Requests, time.Since(start))
	}
}
