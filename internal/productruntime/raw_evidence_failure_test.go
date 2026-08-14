package productruntime

import (
	"context"
	"errors"
	"testing"
)

func TestRawEvidenceFailureDoesNotStopRuntimeOrFailCoreStorage(t *testing.T) {
	owner, stop := context.WithCancelCause(context.Background())
	repository := &runtimeEgressRepository{owner: owner, stop: stop}

	repository.ReportRawEvidenceFailure(errors.New("fixture Raw writer failure"))

	if cause := context.Cause(owner); cause != nil {
		t.Fatalf("Raw evidence failure stopped runtime: %v", cause)
	}
	if failure := repository.failure(); failure != nil {
		t.Fatalf("Raw evidence failure poisoned core storage: %v", failure)
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
