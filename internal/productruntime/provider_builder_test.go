package productruntime

import (
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/hostcontract"
)

func TestPrivateRuntimeProviderBuilderUsesActualStartupDependencyAndFailsClosed(t *testing.T) {
	sentinel := errors.New("synthetic private provider construction refusal")
	called := false
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	r, err := startWithBuilders(t.Context(), options, runtimeBuildOverrides{provider: func(request providerBuildRequest) (providerRuntime, error) {
		called = true
		if request.coordinator != options.OfflineHold || request.secrets == nil || request.instanceIDs == nil || request.audit == nil || request.rawEvidence == nil || request.egressDefaults == nil {
			t.Error("actual provider dependencies missing")
		}
		return nil, sentinel
	}})
	if r != nil {
		shutdownRuntime(t, r)
	}
	if !called || r != nil || !errors.Is(err, sentinel) {
		t.Fatalf("private startup builder ignored/fail-open: called=%v runtime=%v error=%v", called, r != nil, err)
	}
}
