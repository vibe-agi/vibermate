package productruntime

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func TestProductRuntimeRecoversInterruptedEgressAtStartup(t *testing.T) {
	t.Parallel()

	paths, err := NewRuntimePaths(filepath.Join(t.TempDir(), "runtime-data"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := runtimepersistence.Open(
		context.Background(),
		runtimepersistence.Options{
			DatabasePath:           paths.DatabasePath(),
			BusyTimeout:            runtimepersistence.DefaultBusyTimeout,
			CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	attempt := runtimeProviderAttempt(t, "egress-before-restart")
	record, err := store.EgressAttemptRepository().Append(
		context.Background(),
		attempt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	runtime := startTestRuntime(t, testOptionsWithPaths(
		t,
		paths,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	))
	defer shutdownRuntime(t, runtime)
	page, err := runtime.EgressAttempts().List(
		context.Background(),
		egressaudit.PageRequest{Limit: 10},
	)
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

func TestProductRuntimeRefusesToStartWhenEgressRecoveryFails(t *testing.T) {
	t.Parallel()

	recoveryErr := errors.New("injected EgressAttempt recovery failure")
	overrides := runtimeBuildOverrides{
		recoverEgress: func(context.Context, egressaudit.Repository, time.Time) error {
			return recoveryErr
		},
	}
	_, err := startWithBuilders(
		context.Background(),
		testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}),
		overrides,
	)
	if !errors.Is(err, recoveryErr) ||
		!strings.Contains(err.Error(), `stage "EgressAttempt recovery"`) {
		t.Fatalf("startup recovery error = %v", err)
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
