package runtimepersistence

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
)

func TestCleartextPresentationAdjustmentSurvivesSQLAndRejectsCorruption(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer store.Shutdown(ctx)
	record := activity.Record{ID: "cleartext-activity", OccurredAt: time.Now().UTC(), Kind: activity.KindExchangeCompleted, SubjectID: "cleartext-exchange", Status: activity.StatusSucceeded, SourceKind: activity.SourceSystemProxy, SourceDisplayName: "Private fixture", SourceRecognition: activity.SourceRecognitionUnknown, ConnectionID: "cleartext-connection"}
	setFrozenExecutionEvidence(&record, "cleartext")
	record.Transport = &activity.TransportEvidence{Presentation: &activity.WirePresentationEvidence{RequestedRef: "follow-client", EffectiveRef: "follow-client", Revision: 1, Mode: "follow_client", ClientProtocol: "h2", UpstreamProtocol: "http/1.1", ProtocolAdjustment: "cleartext_http1"}}
	if _, err := store.ActivityRepository().Append(ctx, record); err != nil {
		t.Fatal(err)
	}
	got, err := store.ActivityRepository().GetExchange(ctx, record.SubjectID)
	if err != nil || got.Transport == nil || got.Transport.Presentation.ProtocolAdjustment != "cleartext_http1" || got.Transport.Validate() != nil {
		t.Fatalf("real SQL roundtrip lost adjustment: %v", err)
	}
	var wire string
	if err = store.database.QueryRowContext(ctx, `SELECT transport_evidence_json FROM runtime_activities WHERE activity_id=?`, record.ID).Scan(&wire); err != nil {
		t.Fatal(err)
	}
	var value activity.TransportEvidence
	if err = json.Unmarshal([]byte(wire), &value); err != nil {
		t.Fatal(err)
	}
	value.Presentation.ProtocolAdjustment = "unproved"
	corrupt, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.database.ExecContext(ctx, `UPDATE runtime_activities SET transport_evidence_json=? WHERE activity_id=?`, corrupt, record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ActivityRepository().GetExchange(ctx, record.SubjectID); err == nil {
		t.Fatal("SQL reader accepted unknown adjustment")
	}
	record.ID = "invalid-adjustment"
	record.SubjectID = "invalid-adjustment-exchange"
	setFrozenExecutionEvidence(&record, "cleartext")
	record.Transport = &value
	if _, err = store.ActivityRepository().Append(ctx, record); err == nil {
		t.Fatal("SQL writer accepted unknown adjustment")
	}
}
