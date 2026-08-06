package access

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRequestDeletionFenceClosesAdmissionAndWaitsForDrain(t *testing.T) {
	gate := newRequestUsageGate()
	binding := requestUsageBinding(t, "access-request-drain")
	first, err := gate.admit(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	second, err := gate.admit(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		fence *requestDeletionFence
		err   error
	}
	completed := make(chan result, 1)
	go func() {
		fence, drainErr := gate.beginDeletion(t.Context(), binding.AccessID())
		completed <- result{fence: fence, err: drainErr}
	}()

	deadline := time.Now().Add(time.Second)
	for {
		probe, admissionErr := gate.admit(t.Context(), binding)
		if errors.Is(admissionErr, ErrAccessRequestAdmissionClosed) {
			break
		}
		if admissionErr == nil {
			probe.Release()
			continue
		}
		if time.Now().After(deadline) {
			t.Fatal("deletion fence did not close admission")
		}
	}

	first.Release()
	select {
	case outcome := <-completed:
		t.Fatalf("deletion drained with a live request: %+v", outcome)
	default:
	}
	second.Release()
	outcome := <-completed
	if outcome.err != nil || outcome.fence == nil {
		t.Fatalf("beginDeletion() = %+v", outcome)
	}
	outcome.fence.Commit()
	if _, err := gate.admit(t.Context(), binding); !errors.Is(
		err,
		ErrAccessRequestAdmissionClosed,
	) {
		t.Fatalf("retired Access admission error = %v", err)
	}

	// Release remains safe when a caller has several cleanup paths.
	first.Release()
	second.Release()
}

func TestCanceledDeletionDrainReopensAdmission(t *testing.T) {
	gate := newRequestUsageGate()
	binding := requestUsageBinding(t, "access-request-cancel")
	lease, err := gate.admit(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	completed := make(chan error, 1)
	go func() {
		_, drainErr := gate.beginDeletion(ctx, binding.AccessID())
		completed <- drainErr
	}()

	deadline := time.Now().Add(time.Second)
	for {
		probe, admissionErr := gate.admit(t.Context(), binding)
		if errors.Is(admissionErr, ErrAccessRequestAdmissionClosed) {
			break
		}
		if admissionErr == nil {
			probe.Release()
			continue
		}
		if time.Now().After(deadline) {
			t.Fatal("deletion fence did not close admission")
		}
	}
	cancel()
	if err := <-completed; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled deletion drain error = %v", err)
	}
	reopened, err := gate.admit(t.Context(), binding)
	if err != nil {
		t.Fatalf("admission was not reopened: %v", err)
	}
	reopened.Release()
	lease.Release()
}

func requestUsageBinding(t *testing.T, rawAccessID string) IngressBinding {
	t.Helper()
	accessID, err := NewAccessID(rawAccessID)
	if err != nil {
		t.Fatal(err)
	}
	endpointID, err := NewAgentEndpointID("endpoint-" + rawAccessID)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := NewClientOrigin("https://api.example.test:443")
	if err != nil {
		t.Fatal(err)
	}
	hash := PlanHash{1}
	return IngressBinding{
		accessID:         accessID,
		accessName:       "Request usage test",
		endpointID:       endpointID,
		endpointRevision: 1,
		clientOrigin:     origin,
		dialect:          DialectAnthropicMessages,
		revision:         1,
		planHash:         hash,
	}
}
