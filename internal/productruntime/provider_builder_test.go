package productruntime

import (
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/providertransport"
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

func TestPrivateRuntimeProbeFactoriesFailClosedBeforeInterfaceConversion(t *testing.T) {
	for _, kind := range []string{"original", "provider"} {
		for _, failure := range []string{"nil", "error"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				sentinel := errors.New("synthetic private probe refusal")
				called := false
				overrides := runtimeBuildOverrides{}
				if kind == "original" {
					overrides.originalProber = func() (*originaltransport.TLSProber, error) {
						called = true
						if failure == "error" {
							return nil, sentinel
						}
						return nil, nil
					}
				} else {
					overrides.providerProber = func() (*providertransport.ProviderProber, error) {
						called = true
						if failure == "error" {
							return nil, sentinel
						}
						return nil, nil
					}
				}
				r, err := startWithBuilders(t.Context(), testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}), overrides)
				if r != nil {
					shutdownRuntime(t, r)
				}
				if !called || r != nil || err == nil {
					t.Fatalf("probe factory ignored/failopen called=%v runtime=%v err=%v", called, r != nil, err)
				}
				if failure == "error" && !errors.Is(err, sentinel) {
					t.Fatal("actual probe construction error lost", err)
				}
			})
		}
	}
}
