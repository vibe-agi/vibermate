package toolapproval_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

// resolvedViews are the settled questions the Approvals screen lists under
// "Decided recently": every kind in every resolved state, as the runtime
// would send them. The app parses this file, so a shape it cannot read fails
// a test instead of silently emptying the list.
func resolvedViews(t *testing.T) []toolapproval.View {
	t.Helper()

	pending := sampleViews(t)
	records := []toolapproval.Record{}
	resolvedAt := time.Date(2026, 8, 2, 9, 0, 30, 0, time.UTC)
	for _, view := range pending {
		base := recordFromSampleView(t, view)
		settle := func(state toolapproval.State, decision toolapproval.Decision, scope, reason, idempotency string) {
			record := base
			record.ID = view.ID + "-" + string(state) + "-" + scope
			record.Revision = 2
			record.State = state
			record.Decision = decision
			record.DecisionScope = scope
			record.DecisionReason = reason
			record.IdempotencyKey = idempotency
			record.ResolvedAt = resolvedAt
			if err := record.Validate(); err != nil {
				t.Fatalf("%s: %v", record.ID, err)
			}
			records = append(records, record)
		}
		settle(toolapproval.StateAllowed, toolapproval.DecisionAllowOnce, toolapproval.ScopeRequest, "", "key-allow")
		settle(toolapproval.StateDenied, toolapproval.DecisionDeny, toolapproval.ScopeRequest, "user_denied", "key-deny")
		if base.Kind == toolapproval.KindNetworkAsk {
			settle(toolapproval.StateAllowed, toolapproval.DecisionAllowOnce, toolapproval.ScopeHostPort, "", "key-allow-always")
			settle(toolapproval.StateDenied, toolapproval.DecisionDeny, toolapproval.ScopeHostPort, "user_denied", "key-deny-always")
		}
		settle(toolapproval.StateExpired, "", "", "approval_expired", "")
		settle(toolapproval.StateCanceled, "", "", "runtime_stopping", "")
	}
	views := make([]toolapproval.View, 0, len(records))
	for _, record := range records {
		views = append(views, toolapproval.ViewOf(record))
	}
	return views
}

// recordFromSampleView rebuilds the pending Record behind a sample View.
func recordFromSampleView(t *testing.T, view toolapproval.View) toolapproval.Record {
	t.Helper()
	record := toolapproval.Record{
		ID:            view.ID,
		Revision:      view.Revision,
		Kind:          toolapproval.Kind(view.Kind),
		AggregateKey:  view.AggregateKey,
		SubjectRefs:   view.SubjectRefs,
		SubjectLabels: view.SubjectLabels,
		RequestCount:  view.RequestCount,
		WaiterCount:   view.WaiterCount,
		State:         toolapproval.StatePending,
		CreatedAt:     view.CreatedAt,
		ExpiresAt:     view.ExpiresAt,
	}
	if view.Target != nil {
		record.Target = *view.Target
	}
	if record.Kind == toolapproval.KindToolIntent {
		record.ExchangeID = view.ExchangeID
		record.EnvironmentID = sampleEnvironmentID(t)
		record.EnvironmentRevision = view.EnvironmentRevision
		record.EnvironmentDigest = sampleEnvironmentDigest()
		record.RouteID = sampleRouteID(t)
		record.RouteRevision = view.RouteRevision
	}
	return record
}

func TestResolvedApprovalSamplesDescribeWhatTheRuntimeSends(t *testing.T) {
	encoded, err := json.MarshalIndent(resolvedViews(t), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join("..", "..", "api", "samples", "approvals-resolved.json")
	if os.Getenv("VIBERMATE_UPDATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read resolved approval samples: %v", err)
	}
	if string(current) != string(encoded) {
		t.Fatal("the resolved approval samples are stale; rerun with VIBERMATE_UPDATE=1")
	}
}
