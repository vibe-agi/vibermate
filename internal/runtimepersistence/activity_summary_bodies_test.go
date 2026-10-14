package runtimepersistence

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// Capture statistics must learn whether any full conversation body remains
// without listing conversations(200) and attaching identities.
func TestActivitySummaryReportsLaunchContentBodyPresence(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "summary-bodies.db"))
	defer shutdownTestStore(t, store)
	ctx := context.Background()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	seedCaptureGraph(t, store, "managed_run", "run-bodies", 1)

	appendTerminal := func(exchangeID, run string) {
		t.Helper()
		record := activity.Record{
			ID: exchangeID + "-end", SubjectID: exchangeID, OccurredAt: now,
			Kind: activity.KindExchangeCompleted, Status: activity.StatusSucceeded,
			CaptureRunID: run, ConnectionID: "connection-" + exchangeID,
			SourceKind: activity.SourceCaptureRun, SourceDisplayName: "Codex",
			SourceRecognition: activity.SourceRecognitionVerified,
		}
		setFrozenExecutionEvidence(&record, "summary-bodies")
		record.AccountID, record.AccountRevision, record.CredentialEpoch = "", 0, 0
		if _, err := store.ActivityRepository().Append(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	appendTerminal("exchange-no-body", "run-bodies")

	absent, err := store.ActivityRepository().SummarizeExchanges(ctx, activity.SummaryScope{
		CaptureRunID: "run-bodies",
	})
	if err != nil {
		t.Fatal(err)
	}
	if absent.ContentBodiesAvailable == nil || *absent.ContentBodiesAvailable {
		t.Fatalf("expected absent bodies flag, got %+v", absent.ContentBodiesAvailable)
	}

	content := transcriptContentRecordFixture(
		t,
		"exchange-with-body",
		now.Add(time.Minute),
		exchangecontent.ParentRef{CaptureRunID: "run-bodies"},
		[]transcriptMessage{{role: protocolcore.RoleUser, text: "hello"}},
		"",
	)
	if err := store.ExchangeContentRepository().Put(ctx, content); err != nil {
		t.Fatal(err)
	}
	appendTerminal("exchange-with-body", "run-bodies")

	present, err := store.ActivityRepository().SummarizeExchanges(ctx, activity.SummaryScope{
		CaptureRunID: "run-bodies",
	})
	if err != nil {
		t.Fatal(err)
	}
	if present.ContentBodiesAvailable == nil || !*present.ContentBodiesAvailable {
		t.Fatalf("expected retained bodies flag, got %+v", present.ContentBodiesAvailable)
	}

	session, err := store.ActivityRepository().SummarizeExchanges(ctx, activity.SummaryScope{
		Client: "codex", SessionID: "missing-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.ContentBodiesAvailable != nil {
		t.Fatalf("session summary must not claim launch body presence: %+v", session.ContentBodiesAvailable)
	}
}
