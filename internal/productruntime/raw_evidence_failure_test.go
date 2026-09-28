package productruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/hostcontract"
)

func TestRawEvidenceFailureDoesNotStopRuntimeOrFailCoreStorage(t *testing.T) {
	owner, stop := context.WithCancelCause(context.Background())
	defer stop(nil)
	tracker := newStatusTracker("raw-fixture", hostcontract.KindDesktop, time.Now())
	tracker.commitInitialized(1)
	repository := &runtimeEgressRepository{owner: owner, stop: stop, status: tracker}

	repository.ReportRawEvidenceFailure(errors.New("fixture Raw writer failure"))

	if cause := context.Cause(owner); cause != nil {
		t.Fatalf("Raw evidence failure stopped runtime: %v", cause)
	}
	if failure := repository.failure(); failure != nil {
		t.Fatalf("Raw evidence failure poisoned core storage: %v", failure)
	}
	status := tracker.snapshot()
	if status.State != RuntimeStateInitialized || status.Storage != StorageStateHealthy ||
		status.RecordingFailure == nil || status.RecordingFailure.Operation != "raw_evidence" || !status.RecordingFailure.Valid() {
		t.Fatalf("raw recording failure lost or disabled readiness: %+v", status)
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.rawEvidenceFailureCount != 1 ||
		repository.rawEvidenceFailureErr == nil {
		t.Fatalf(
			"Raw evidence degradation count=%d error=%v",
			repository.rawEvidenceFailureCount,
			repository.rawEvidenceFailureErr,
		)
	}
}
