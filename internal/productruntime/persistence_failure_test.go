package productruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/hostcontract"
)

func TestPersistenceDiagnosticsPreserveFirstCauseWithoutSecrets(t *testing.T) {
	tracker := newStatusTracker("fixture", hostcontract.KindDesktop, time.Now())
	tracker.commitInitialized(1)
	tracker.failRecording("usage", errors.New("secret-body credential account@example.com"))
	tracker.failStorage("egress_complete", fmt.Errorf("secret-path: %w", context.DeadlineExceeded))
	tracker.failRecordingMaintenance(&recordingMaintenanceError{phase: "compact", cause: fmt.Errorf("secret-path: %w", context.DeadlineExceeded)})
	first := tracker.snapshot()
	var concurrent sync.WaitGroup
	for range 20 {
		concurrent.Go(func() {
			tracker.failStorage("egress_drain", context.Canceled)
			tracker.failRecording("raw_evidence", context.Canceled)
			tracker.failRecordingMaintenance(&recordingMaintenanceError{phase: "delete", cause: context.Canceled})
			tracker.observeStorage(1, nil)
		})
	}
	concurrent.Wait()
	status := tracker.snapshot()
	if *status.StorageFailure != *first.StorageFailure || *status.RecordingFailure != *first.RecordingFailure ||
		status.StorageFailure.Operation != "egress_complete" || status.StorageFailure.Reason != "timeout" ||
		!status.StorageFailure.Valid() || !status.RecordingFailure.Valid() || status.State != RuntimeStateDegraded {
		t.Fatalf("first failure lost: %+v", status)
	}
	payload, err := json.Marshal(status)
	if err != nil || strings.Contains(string(payload), "secret") || strings.Contains(string(payload), "account@") {
		t.Fatalf("failure exposed error text: %s (%v)", payload, err)
	}
	status.StorageFailure.Operation = "mutated"
	status.RecordingFailure.Operation = "mutated"
	if status.RecordingMaintenanceFailure == nil || !status.RecordingMaintenanceFailure.Valid() || status.RecordingMaintenanceFailure.Phase != "compact" || status.RecordingMaintenanceFailure.Reason != "timeout" {
		t.Fatalf("maintenance cause hidden by first generic gap: %+v", status.RecordingMaintenanceFailure)
	}
	status.RecordingMaintenanceFailure.Phase = "mutated"
	if !tracker.snapshot().RecordingMaintenanceFailure.Valid() {
		t.Fatal("status caller mutated maintenance diagnosis")
	}
	if !tracker.snapshot().StorageFailure.Valid() || !tracker.snapshot().RecordingFailure.Valid() {
		t.Fatal("status caller mutated runtime diagnostics")
	}
}
