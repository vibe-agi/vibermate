package runtimepersistence

import (
	"context"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
)

func TestRecordingPrivacyAcknowledgementRequiresFullSynchronousCommit(t *testing.T) {
	for _, operation := range []string{"delete-capture", "cleanup-intent", "usage-consent"} {
		t.Run(operation, func(t *testing.T) {
			control := inspectionControlFixture(t)
			seedOwnedApprovalCaptures(t, control)
			table := "recording_cleanup_state"
			if operation == "delete-capture" {
				table = "recording_capture_fences"
			} else if operation == "usage-consent" {
				table = "runtime_usage_policy"
			}
			// Inspect the actual SQLite transaction policy at the system boundary.
			// NORMAL can acknowledge a WAL commit before its privacy fence is synced.
			nativeExec(t, control.store.database, `CREATE TEMP TRIGGER privacy_requires_full BEFORE UPDATE ON main.`+table+`
 WHEN (SELECT synchronous FROM pragma_synchronous)<>2
 BEGIN SELECT RAISE(ABORT,'privacy commit requires FULL synchronization'); END`)
			switch operation {
			case "delete-capture":
				released, err := control.DeleteCapture(t.Context(), "manual_capture", "doomed")
				if err != nil || released.Captures != 1 {
					t.Fatalf("durable Capture deletion refused: %+v %v", released, err)
				}
			case "cleanup-intent":
				if err := control.RequestRecordingCleanup(t.Context(), time.Now().UTC()); err != nil {
					t.Fatalf("durable cleanup intent refused: %v", err)
				}
			case "usage-consent":
				repository := control.UsagePolicyRepository()
				policy, err := repository.UsagePolicy(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				policy.Enabled = false
				policy.RetentionDays = 7
				updated, err := repository.SetUsagePolicy(t.Context(), policy, time.Now().UTC())
				if err != nil || updated.Enabled || updated.RetentionDays != 7 || updated.Revision != policy.Revision+1 {
					t.Fatalf("durable consent change refused: %+v %v", updated, err)
				}
			}
			state, err := control.RecordingCleanupState(t.Context())
			if err != nil || state.RequestedRevision != 1 || state.CompletedRevision != 0 {
				t.Fatalf("acknowledged privacy change lost cleanup obligation: %+v %v", state, err)
			}
			var synchronous int
			if err := control.store.database.QueryRowContext(t.Context(), `PRAGMA synchronous`).Scan(&synchronous); err != nil || synchronous != 1 {
				t.Fatalf("ordinary write policy was not restored: %d %v", synchronous, err)
			}
		})
	}
}

func TestRecordingFailedCaptureDeleteReturnsNoCommittedRemoval(t *testing.T) {
	control := inspectionControlFixture(t)
	seedOwnedApprovalCaptures(t, control)
	if err := control.ToolApprovalRepository().Create(t.Context(), ownedApproval(t, "rollback-owned", "doomed")); err != nil {
		t.Fatal(err)
	}
	// Fail after required approval deletion, so the public receipt must not claim
	// a removed approval when SQLite rolls back the whole privacy transaction.
	nativeExec(t, control.store.database, `CREATE TEMP TRIGGER reject_capture_removal BEFORE DELETE ON main.manual_captures
 BEGIN SELECT RAISE(ABORT,'synthetic required delete failure'); END`)
	released, err := control.DeleteCapture(t.Context(), "manual_capture", "doomed")
	if err == nil || released != (resourcedeletion.Released{}) {
		t.Fatalf("failed transaction claimed committed removals: %+v %v", released, err)
	}
	state, err := control.RecordingCleanupState(context.Background())
	if err != nil || state.RequestedRevision != 0 {
		t.Fatalf("failed deletion partially published cleanup: %+v %v", state, err)
	}
	if _, err := control.ToolApprovalRepository().Get(t.Context(), "rollback-owned"); err != nil {
		t.Fatalf("rolled-back approval removal became visible: %v", err)
	}
}
