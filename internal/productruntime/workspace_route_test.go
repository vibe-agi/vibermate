package productruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/workspaceroute"
)

func TestWorkspaceRouteCASSelectsAndFreezesTheNextExchangeProfile(t *testing.T) {
	t.Parallel()

	accessID, err := access.NewAccessID("access-workspace-route")
	if err != nil {
		t.Fatal(err)
	}
	provider := &failThenAnswerProvider{calls: 1}
	runtime := failoverRuntime(t, provider)
	aggregate := twoCandidateAggregate(t, accessID)
	if write, writeErr := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{ExpectedRevision: 0, Aggregate: aggregate},
	); writeErr != nil || write.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write Access result=%+v err=%v", write, writeErr)
	}

	workspaceDirectory := t.TempDir()
	scope, err := runtime.WorkspaceIdentity().ResolveLocal(
		context.Background(),
		workspaceDirectory,
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.SnapshotResolver().ResolveAccess(accessID)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := runtime.WorkspaceRoutes().Resolve(
		context.Background(),
		snapshot,
		scope,
	)
	if err != nil {
		t.Fatal(err)
	}
	initial.Release()
	primaryID := aggregate.RouteSets[0].CandidateProfileIDs[0]
	secondaryID := aggregate.RouteSets[0].CandidateProfileIDs[1]
	if initial.ProfileID != primaryID || initial.BindingRevision != 1 {
		t.Fatalf("initial resolution = %+v", initial)
	}

	updated, err := runtime.WorkspaceRoutes().UpdateBinding(
		context.Background(),
		initial.BindingID,
		initial.BindingRevision,
		secondaryID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Record.Revision != 2 || updated.Record.ProfileID != secondaryID {
		t.Fatalf("updated binding = %+v", updated)
	}

	operation := runtimeAnthropicOperationEvidence(t)
	admission, err := captureadmission.NewManagedRun(
		captureadmission.ManagedRunEvidence{
			CaptureRunID: "capture-run-workspace",
			SourceLabel:  "test-agent",
			Workspace:    scope,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := exchange.NewClientRequest(
		"exchange-workspace-secondary",
		snapshot.IngressBinding(),
		operation,
		[]byte(`{
			"model":"claude-client-alias",
			"max_tokens":32,
			"messages":[{"role":"user","content":"hello"}]
		}`),
		exchange.ReplayGenerationCostOnly,
		access.ApplicationProtocolHTTP1,
		exchange.WithIngressCorrelation(admission, "connection-workspace"),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.ExchangeExecutor().Execute(
		context.Background(),
		request,
		&runtimeDownstream{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkspaceRouteBindingID != initial.BindingID.String() ||
		result.WorkspaceRouteRevision != 2 ||
		result.WorkspaceProfileID != secondaryID.String() ||
		!strings.Contains(result.RouteHost, "api.secondary.example") {
		t.Fatalf("workspace-routed result = %+v", result)
	}

	oldPin, err := runtime.WorkspaceRoutes().Resolve(
		context.Background(),
		snapshot,
		scope,
	)
	if err != nil {
		t.Fatal(err)
	}
	newView, err := runtime.WorkspaceRoutes().UpdateBinding(
		context.Background(),
		initial.BindingID,
		2,
		primaryID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if newView.Record.Revision != 3 || newView.PinnedRequestCount != 1 {
		t.Fatalf("route switch did not expose the old pin: %+v", newView)
	}
	oldPin.Release()
	afterRelease, err := runtime.WorkspaceRoutes().GetBinding(
		context.Background(),
		initial.BindingID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if afterRelease.PinnedRequestCount != 0 {
		t.Fatalf("released pin count = %d", afterRelease.PinnedRequestCount)
	}
	if _, err := runtime.WorkspaceRoutes().UpdateBinding(
		context.Background(),
		initial.BindingID,
		2,
		secondaryID,
	); !errors.Is(err, workspaceroute.ErrRevisionConflict) {
		t.Fatalf("stale route CAS error = %v", err)
	}
}
