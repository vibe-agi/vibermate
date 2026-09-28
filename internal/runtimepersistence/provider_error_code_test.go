package runtimepersistence

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/activity"
)

func TestProviderErrorCodePreservesHistoryAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, path)
	appendIdentityActivity(t, store, "retained")
	before, err := store.ActivityRepository().GetExchange(ctx, "retained")
	if err != nil {
		t.Fatal(err)
	}
	shutdownTestStore(t, store)
	store = openTestStore(t, path)
	got, err := store.ActivityRepository().GetExchange(ctx, "retained")
	if err != nil || got.ID != before.ID || got.Sequence != before.Sequence {
		t.Fatalf("history lost: %+v %v", got, err)
	}
	before.ID, before.SubjectID, before.Status = "new-activity", "new-exchange", activity.StatusFailed
	before.ReasonCode = "provider_response_failed"
	before.Diagnosis = &activity.Diagnosis{ProviderStatus: 200, ProviderErrorCode: "invalid_encrypted_content"}
	if _, err := store.ActivityRepository().Append(ctx, before); err != nil {
		t.Fatal(err)
	}
	shutdownTestStore(t, store)
	store = openTestStore(t, path)
	defer shutdownTestStore(t, store)
	got, err = store.ActivityRepository().GetExchange(ctx, "new-exchange")
	if err != nil || got.Diagnosis == nil || *got.Diagnosis != *before.Diagnosis {
		t.Fatalf("diagnosis lost: %+v %v", got, err)
	}
	before.ID, before.SubjectID = "unsafe", "unsafe"
	before.Diagnosis.ProviderErrorCode = "private-provider-text"
	if _, err := store.ActivityRepository().Append(ctx, before); err == nil {
		t.Fatal("arbitrary provider text persisted as code")
	}
}
