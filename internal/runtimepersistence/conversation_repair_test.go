package runtimepersistence

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/recording"
	"testing"
	"time"
)

func TestCurrentConversationRepairRequiresExactRetainedRoot(t *testing.T) {
	f := newEgressInspectionFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(context.Background(), f.scope, inspectionActivity("repair-root", "repair", activity.KindExchangeCompleted, now)); err != nil {
		t.Fatal(err)
	}
	reader := newActivityInspectionTestReader(t, f)
	record, err := reader.GetExchange(context.Background(), "repair")
	if err != nil {
		t.Fatal(err)
	}
	writer, ok := any(f.current).(activity.ConversationRepairWriter)
	if !ok {
		t.Fatal("current optional role has no conditional repair writer")
	}
	identity := agentconversation.ClientIdentity{Client: "codex", SessionID: "exact-local-session", SessionResumable: true, ProviderResponseID: "response", Source: agentconversation.ClientIdentitySourceLocalState, Confidence: "exact", ObservedAt: now}
	repair := activity.ConversationRepair{Root: *record.InspectionRoot, Identity: identity}
	for _, bad := range []string{"archive", "source", "generation", "row"} {
		candidate := repair
		switch bad {
		case "archive":
			candidate.Root.Current = false
		case "source":
			candidate.Root.SourceID = "foreign"
		case "generation":
			candidate.Root.Scope.Fence.CaptureGeneration++
		case "row":
			candidate.Root.ActivityID = "replacement"
		}
		if err = writer.RepairConversation(context.Background(), candidate); !errors.Is(err, recording.ErrObservationDiscarded) {
			t.Fatalf("%s repair accepted: %v", bad, err)
		}
	}
	if err = writer.RepairConversation(context.Background(), repair); err != nil {
		t.Fatalf("repair root=%+v err=%v", repair.Root, err)
	}
	got, err := f.current.ConversationIdentityRepository().GetConversationIdentity(context.Background(), "repair")
	if err != nil || got.SessionID != identity.SessionID {
		t.Fatalf("identity=%+v %v", got, err)
	}
	if _, err = f.current.store.database.Exec(`DELETE FROM runtime_activities WHERE activity_id='repair-root'`); err != nil {
		t.Fatal(err)
	}
	if err = writer.RepairConversation(context.Background(), repair); !errors.Is(err, recording.ErrObservationDiscarded) {
		t.Fatal("removed root repaired", err)
	}
}
