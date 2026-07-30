package toolapproval_test

import (
	"context"
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func TestAuthorityDurablyBlocksAndReleasesCompleteToolGroup(t *testing.T) {
	t.Parallel()

	store := openStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownStore(t, store)
	authority, err := toolapproval.New(
		context.Background(),
		toolapproval.Options{
			Repository: store.ToolApprovalRepository(),
			Clock:      toolapproval.SystemClock{},
			Random:     rand.Reader,
			Config: toolapproval.Config{
				DecisionTimeout: time.Minute,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownAuthority(t, authority)
	request := decisionRequest(t, `{"path":"/private/value"}`)
	result := make(chan exchange.ToolDecision, 1)
	failure := make(chan error, 1)
	go func() {
		decision, err := authority.Decide(context.Background(), request)
		if err != nil {
			failure <- err
			return
		}
		result <- decision
	}()
	pending := waitForPending(t, authority)
	if pending.State != toolapproval.StatePending ||
		len(pending.ToolCallIDs) != 2 ||
		len(pending.ToolNames) != 2 ||
		pending.TitleKey == "" ||
		pending.SummaryKey == "" {
		t.Fatalf("pending approval = %+v", pending)
	}
	if pending.ToolNames[0] != "read_file" ||
		pending.ToolNames[1] != "list_directory" {
		t.Fatalf("safe tool names = %v", pending.ToolNames)
	}
	resolved, err := authority.DecideApproval(
		context.Background(),
		toolapproval.DecisionCommand{
			ApprovalID:       pending.ID,
			ExpectedRevision: pending.Revision,
			IdempotencyKey:   "approval-decision-0001",
			Decision:         toolapproval.DecisionAllowOnce,
			Scope:            toolapproval.ScopeRequest,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State != toolapproval.StateAllowed ||
		resolved.Revision != pending.Revision+1 {
		t.Fatalf("resolved approval = %+v", resolved)
	}
	select {
	case err := <-failure:
		t.Fatal(err)
	case decision := <-result:
		if decision.Outcome != exchange.ToolDecisionApproved ||
			decision.ReasonCode != "" {
			t.Fatalf("tool decision = %+v", decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tool decision did not unblock")
	}
	replayed, err := authority.DecideApproval(
		context.Background(),
		toolapproval.DecisionCommand{
			ApprovalID:       pending.ID,
			ExpectedRevision: pending.Revision,
			IdempotencyKey:   "approval-decision-0001",
			Decision:         toolapproval.DecisionAllowOnce,
			Scope:            toolapproval.ScopeRequest,
		},
	)
	if err != nil || replayed.Revision != resolved.Revision {
		t.Fatalf("idempotent replay=%+v err=%v", replayed, err)
	}
	if _, err := authority.DecideApproval(
		context.Background(),
		toolapproval.DecisionCommand{
			ApprovalID:       pending.ID,
			ExpectedRevision: pending.Revision,
			IdempotencyKey:   "approval-decision-0002",
			Decision:         toolapproval.DecisionDeny,
			Scope:            toolapproval.ScopeRequest,
			ReasonCode:       "user_denied",
		},
	); !errors.Is(err, toolapproval.ErrRevisionConflict) {
		t.Fatalf("stale decision error = %v", err)
	}

	stored, err := store.ToolApprovalRepository().Get(
		context.Background(),
		pending.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != toolapproval.StateAllowed ||
		stored.Decision != toolapproval.DecisionAllowOnce {
		t.Fatalf("stored approval = %+v", stored)
	}
}

func TestAuthorityRecoveryCancelsOrphanedPendingApproval(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "data", "runtime.db")
	first := openStore(t, path)
	now := time.Now().UTC()
	record := pendingRecord(t, now)
	if err := first.ToolApprovalRepository().Create(
		context.Background(),
		record,
	); err != nil {
		t.Fatal(err)
	}
	shutdownStore(t, first)

	second := openStore(t, path)
	defer shutdownStore(t, second)
	authority, err := toolapproval.New(
		context.Background(),
		toolapproval.Options{
			Repository: second.ToolApprovalRepository(),
			Clock:      fixedClock{now: now.Add(time.Second)},
			Random:     rand.Reader,
			Config: toolapproval.Config{
				DecisionTimeout: time.Minute,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownAuthority(t, authority)
	if authority.Recovery().CanceledPending != 1 {
		t.Fatalf("approval recovery = %+v", authority.Recovery())
	}
	recovered, err := authority.GetApproval(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != toolapproval.StateCanceled ||
		recovered.TerminalReason != "runtime_recovered" ||
		recovered.ResolvedAt == nil {
		t.Fatalf("recovered approval = %+v", recovered)
	}
}

func decisionRequest(
	t *testing.T,
	rawArguments string,
) exchange.ToolDecisionRequest {
	t.Helper()
	accessID, err := access.NewAccessID("access-approval")
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := protocolcore.NewJSONObject(
		[]byte(rawArguments),
		protocolcore.MaxToolJSONBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstKey, err := protocolcore.NewCallKey("openai-chat", "call-1")
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := protocolcore.NewCallKey("openai-chat", "call-2")
	if err != nil {
		t.Fatal(err)
	}
	hash := access.PlanHash{0x42}
	request, err := exchange.NewToolDecisionRequest(
		"exchange-approval",
		accessID,
		3,
		hash,
		[]protocolcore.ToolIntent{
			{
				ResponseID: "response-1",
				Ordinal:    0,
				Call: protocolcore.ToolCall{
					Key:       firstKey,
					Name:      "read_file",
					Arguments: arguments,
				},
			},
			{
				ResponseID: "response-1",
				Ordinal:    1,
				Call: protocolcore.ToolCall{
					Key:       secondKey,
					Name:      "list_directory",
					Arguments: arguments,
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func pendingRecord(t *testing.T, now time.Time) toolapproval.Record {
	t.Helper()
	accessID, err := access.NewAccessID("access-recovery")
	if err != nil {
		t.Fatal(err)
	}
	return toolapproval.Record{
		ID:           "approval-recovery",
		Revision:     1,
		ExchangeID:   "exchange-recovery",
		AccessID:     accessID,
		PlanRevision: 1,
		PlanHash:     access.PlanHash{0x33},
		ToolCallIDs:  []string{"call-recovery"},
		ToolNames:    []string{"read_file"},
		State:        toolapproval.StatePending,
		CreatedAt:    now,
		ExpiresAt:    now.Add(time.Minute),
	}
}

func waitForPending(
	t *testing.T,
	authority *toolapproval.Authority,
) toolapproval.View {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		page, err := authority.ListApprovals(
			context.Background(),
			toolapproval.PageRequest{
				State: toolapproval.StatePending,
				Limit: 10,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 1 {
			return page.Items[0]
		}
		if time.Now().After(deadline) {
			t.Fatal("pending approval was not published")
		}
		time.Sleep(time.Millisecond)
	}
}

func openStore(t *testing.T, path string) *runtimepersistence.Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, err := runtimepersistence.Open(ctx, runtimepersistence.Options{
		DatabasePath:           path,
		BusyTimeout:            runtimepersistence.DefaultBusyTimeout,
		CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func shutdownStore(t *testing.T, store *runtimepersistence.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func shutdownAuthority(t *testing.T, authority *toolapproval.Authority) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := authority.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

type fixedClock struct {
	now time.Time
}

func (clock fixedClock) Now() time.Time {
	return clock.now
}
