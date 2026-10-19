package productruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
)

func TestProductRuntimeRecoversInterruptedEgressAtStartup(t *testing.T) {
	t.Parallel()

	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	first := startTestRuntime(t, options)
	waitRecording(t, first)
	attempt := runtimeProviderAttempt(t, "egress-before-restart")
	repository := first.recordingStorage.current.Load().store.EgressAttemptRepository()
	if err := repository.(egressaudit.ScopedAttemptStore).PutScopedAttempt(context.Background(), first.recorder.ScopeStamp(captureidentity.Reference{}).Scope(), attempt); err != nil {
		t.Fatal(err)
	}
	initial, err := repository.List(context.Background(), egressaudit.PageRequest{Limit: 10})
	if err != nil || len(initial.Items) != 1 {
		t.Fatal("seed optional history", err)
	}
	record := initial.Items[0]
	shutdownRuntime(t, first)
	options.OfflineHold = &coordinatorDouble{}
	runtime := startTestRuntime(t, options)
	defer shutdownRuntime(t, runtime)
	// Eventual optional recovery is explicit; required startup did not join it.
	waitRecording(t, runtime)
	page, err := runtime.recordingStorage.current.Load().store.EgressAttemptRepository().List(context.Background(), egressaudit.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("recovered attempt count = %d, want 1", len(page.Items))
	}
	recovered := page.Items[0]
	if recovered.Sequence != record.Sequence ||
		recovered.Attempt.ID() != attempt.ID() ||
		!recovered.Attempt.Terminal() ||
		recovered.Attempt.Outcome() != egressaudit.OutcomeFailed ||
		recovered.Attempt.ErrorClass() != egressaudit.RecoveryErrorClass ||
		recovered.Attempt.CompletedAt().Before(attempt.StartedAt()) ||
		recovered.Attempt.Parent() != attempt.Parent() ||
		recovered.Attempt.Decision() != attempt.Decision() {
		t.Fatalf("startup recovery changed or omitted evidence: %+v", recovered)
	}
}

func TestProductRuntimeOrdinaryRecoveryFailureIsOptional(t *testing.T) {
	runtime, err := startWithBuilders(context.Background(), testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}), runtimeBuildOverrides{recoverEgress: func(context.Context, egressaudit.Repository, time.Time) error {
		return errors.New("synthetic ordinary recovery failure")
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRuntime(t, runtime)
	deadline := time.Now().Add(time.Second)
	for runtime.Status().Recording.State == RecordingStarting && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if status := runtime.Status(); status.State != RuntimeStateInitialized || status.Recording.State != RecordingDegraded {
		t.Fatalf("optional recovery changed control readiness: %+v", status)
	}
}

func runtimeProviderAttempt(t *testing.T, id string) egressaudit.Attempt {
	t.Helper()
	attempt, err := egressaudit.New(egressaudit.NewInput{
		ID:           id,
		ConnectionID: "connection-before-restart",
		Purpose:      egressaudit.PurposeProviderAttempt,
		PayloadClass: egressaudit.PayloadClientSemantic,
		Parent: egressaudit.ParentRef{
			Kind:       egressaudit.ParentUpstreamAttempt,
			ID:         "upstream-before-restart",
			ExchangeID: "exchange-before-restart",
		},
		Caller:       egressaudit.CallerCore,
		TargetOrigin: "https://provider.example:443",
		Decision: egressaudit.DecisionRef{
			PolicyID:       "policy-before-restart",
			PolicyRevision: 7,
			Authority:      egressaudit.AuthorityEnvironment,
			RuleID:         "rule-before-restart",
			ProxyID:        "direct",
		},
		StartedAt: time.Now().UTC().Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}
