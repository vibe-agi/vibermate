package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func TestNativeLifecycleAdapterCompleteWithoutExecution(t *testing.T) {
	store, id, _ := nativeLifecycleFixture(t, environment.ToolPolicyReview)
	repo := store.NativeLifecycleRepository()
	ctx := context.Background()
	if _, ok := repo.(nativetool.Repository); !ok {
		t.Fatal("same lifecycle adapter lacks full repository")
	}
	op, err := repo.Get(ctx, id)
	if err != nil || op.State != "validated" || op.ToolName != "read_file" || op.ApprovalID != nil || op.CredentialOwner != "original_client" || op.Recording.Mode != environment.ContentRecordingOff {
		t.Fatalf("operation %+v %v", op, err)
	}
	now := time.Now().UTC()
	approval, err := repo.RequestApproval(ctx, id, "55555555555555555555555555555555", now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	answer := nativetool.DecisionInput{ApprovalID: approval.ID, ExpectedRevision: approval.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "native-answer-key-0001", ObservedAt: now}
	allowed, err := repo.DecideApproval(ctx, answer)
	if err != nil || allowed.State != "allowed" || allowed.ConsumedAt != nil {
		t.Fatalf("answer %+v %v", allowed, err)
	}
	repeated, err := repo.DecideApproval(ctx, answer)
	if err != nil || repeated.Revision != allowed.Revision {
		t.Fatalf("idempotent %+v %v", repeated, err)
	}
	answer.Decision = nativetool.DecisionDeny
	if _, err := repo.DecideApproval(ctx, answer); !errors.Is(err, nativetool.ErrConflict) {
		t.Fatalf("changed answer: %v", err)
	}
	read, err := repo.GetApproval(ctx, approval.ID)
	if err != nil || read.State != "allowed" {
		t.Fatalf("approval %+v %v", read, err)
	}
	page, err := repo.Page(ctx, nativetool.PageRequest{Capture: op.Capture, Limit: 200})
	if err != nil || len(page.Items) == 0 || page.NextSequence == 0 {
		t.Fatalf("page %+v %v", page, err)
	}
	for _, table := range []string{"native_tool_once", "native_tool_contents", "native_tool_deliveries", "tool_approvals", "runtime_activities", "runtime_raw_evidence_envelopes"} {
		var count int
		err := store.database.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count)
		if err != nil || count != 0 {
			t.Fatalf("unexpected %s: %d %v", table, count, err)
		}
	}
	path := store.databasePath
	if err := store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	defer shutdownTestStore(t, reopened)
	persisted, err := reopened.NativeLifecycleRepository().Get(ctx, id)
	if err != nil || persisted.ApprovalID == nil || *persisted.ApprovalID != approval.ID {
		t.Fatalf("reopened %+v %v", persisted, err)
	}
}

func TestNativeCatalogEvidenceScopeCASAndProjection(t *testing.T) {
	store, id, _ := nativeLifecycleFixture(t, environment.ToolPolicyReview)
	repo := store.NativeLifecycleRepository()
	binding, _ := nativeDecisionFixture(t, store, id)
	ctx := context.Background()
	now := time.Now().UTC()
	plan := binding.Plan()
	session := binding.SessionID()
	catalog, _ := binding.CatalogDigest()
	a, err := repo.RequestApproval(ctx, id, "55555555555555555555555555555555", now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	list, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestToolsList, Plan: plan, Capture: binding.Capture(), AssignmentRevision: 1, OwnerEpoch: 1, SessionID: session, SessionRevision: 3, CatalogGeneration: 1, CatalogDigest: &catalog, ExactRPC: []byte(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)})
	if err != nil {
		t.Fatal(err)
	}
	listID := nativetool.ID("66666666666666666666666666666666")
	rpc, _ := list.RPCIDDigest()
	requestDigest := list.RequestDigest()
	if _, err := repo.Receive(ctx, nativetool.ReceiveInput{ID: listID, Capture: binding.Capture(), EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "synthetic-list-refresh", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "POST", Kind: nativetool.RequestToolsList, SessionID: &session, RPCIDDigest: &rpc, RequestDigest: &requestDigest, Recording: plan.ContentRecording(), ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Bind(ctx, listID, list, now); err != nil {
		t.Fatal(err)
	}
	seedNativeHistoricalCompletion(t, store, listID, now)
	scope := nativetool.CatalogScope{Capture: binding.Capture(), Plan: plan, AssignmentRevision: 1, OwnerEpoch: 1, SessionID: session, SessionRevision: 4, CatalogGeneration: 2}
	observe := func(scope nativetool.CatalogScope) nativetool.CatalogEvidence {
		t.Helper()
		observer, err := nativetool.NewCatalogObserver(scope)
		if err != nil {
			t.Fatal(err)
		}
		e, err := observer.Observe([]nativetool.CatalogPageObservation{{ExactRequest: []byte(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`), ExactResponse: []byte(`{"jsonrpc":"2.0","id":4,"result":{"tools":[{"name":"read_file","description":"new public schema","inputSchema":{"type":"object"}}]}}`)}})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	for _, mutate := range []func(*nativetool.CatalogScope){func(s *nativetool.CatalogScope) { s.Capture.ID = "foreign" }, func(s *nativetool.CatalogScope) { s.OwnerEpoch++ }, func(s *nativetool.CatalogScope) { s.AssignmentRevision++ }, func(s *nativetool.CatalogScope) { s.SessionID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" }, func(s *nativetool.CatalogScope) { s.SessionRevision++ }, func(s *nativetool.CatalogScope) { s.CatalogGeneration++ }} {
		wrong := scope
		mutate(&wrong)
		if _, err := repo.SetCatalog(ctx, session, 3, observe(wrong), now); err == nil {
			t.Fatal("foreign catalog scope accepted")
		}
	}
	swappedObserver, err := nativetool.NewCatalogObserver(scope)
	if err != nil {
		t.Fatal(err)
	}
	swapped, err := swappedObserver.Observe([]nativetool.CatalogPageObservation{{ExactRequest: []byte(`{"jsonrpc":"2.0","id":99,"method":"tools/list"}`), ExactResponse: []byte(`{"jsonrpc":"2.0","id":99,"result":{"tools":[]}}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetCatalog(ctx, session, 3, swapped, now); !errors.Is(err, nativetool.ErrDenied) {
		t.Fatalf("swapped observed request accepted %v", err)
	}
	if revision, err := repo.SetCatalog(ctx, session, 3, observe(scope), now); err != nil || revision != 4 {
		t.Fatalf("catalog CAS %d %v", revision, err)
	}
	canceled, err := repo.GetApproval(ctx, a.ID)
	if err != nil || canceled.State != "canceled" {
		t.Fatalf("catalog did not cancel old question %+v %v", canceled, err)
	}
	if _, err := repo.SetCatalog(ctx, session, 3, observe(scope), now); !errors.Is(err, nativetool.ErrConflict) {
		t.Fatalf("stale catalog %v", err)
	}
	if _, err := repo.RequestApproval(ctx, id, "77777777777777777777777777777777", now, now.Add(time.Minute)); err == nil {
		t.Fatal("stale catalog question accepted")
	}
	op, err := repo.Get(ctx, id)
	if err != nil || op.ToolName != "read_file" || op.Arguments != nil || op.Result != nil {
		t.Fatalf("frozen OFF projection %+v %v", op, err)
	}
}

func TestNativePageRealDescriptorsNeverLoadBody(t *testing.T) {
	store, id, _ := nativeLifecycleFixtureRecording(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1})
	repo := store.NativeLifecycleRepository()
	ctx := context.Background()
	op, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// HISTORICAL SYNTHETIC CONTENT: Task 3 owns production content insertion.
	tx, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := storeEvidenceBody(ctx, tx, []byte(`{"synthetic":"body"}`))
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	deadline := op.CreatedAt.Add(time.Hour)
	if _, err := tx.ExecContext(ctx, `INSERT INTO native_tool_contents(operation_id,part,body_digest,expires_at_unix_ms) VALUES(?,'tool_arguments',?,?)`, id, digest[:], deadline.UnixMilli()); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// A corrupt body manifest must not affect body-free descriptor projection.
	if _, err := store.database.ExecContext(ctx, `UPDATE runtime_evidence_bodies SET plain_bytes=999 WHERE digest=?`, digest[:]); err != nil {
		t.Fatal(err)
	}
	projected, err := repo.Get(ctx, id)
	if err != nil || projected.Arguments == nil || projected.Arguments.Digest != nativetool.Digest(digest) || !projected.Arguments.ExpiresAt.Equal(deadline) || projected.Result != nil || projected.Recording.Mode != environment.ContentRecordingFull {
		t.Fatalf("descriptor %+v %v", projected, err)
	}
	page, err := repo.Page(ctx, nativetool.PageRequest{Capture: op.Capture, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range page.Items {
		if item.ID == id {
			found = true
			if item.Arguments == nil || item.Arguments.Digest != projected.Arguments.Digest {
				t.Fatal("Page/Get descriptor disagreement")
			}
		}
	}
	if !found {
		t.Fatal("missing page operation")
	}
}

func TestNativeProjectionRejectsForgedApprovalDigest(t *testing.T) {
	store, id, _ := nativeLifecycleFixture(t, environment.ToolPolicyReview)
	repo := store.NativeLifecycleRepository()
	ctx := context.Background()
	now := time.Now().UTC()
	a, err := repo.RequestApproval(ctx, id, "55555555555555555555555555555555", now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	// CORRUPT SYNTHETIC DATA: prove reads reject an internally inconsistent row.
	if _, err := store.database.ExecContext(ctx, `UPDATE native_tool_approvals SET binding_digest=zeroblob(32) WHERE approval_id=?`, a.ID); err != nil {
		t.Fatal(err)
	}
	defer store.database.ExecContext(ctx, `UPDATE native_tool_approvals SET binding_digest=? WHERE approval_id=?`, a.BindingDigest[:], a.ID)
	if _, err := repo.GetApproval(ctx, a.ID); !errors.Is(err, nativetool.ErrInvalid) {
		t.Fatalf("forged binding accepted: %v", err)
	}
}

func TestNativeReviewIdempotentAnswerRejectsCorruptBinding(t *testing.T) {
	store, id, _ := nativeLifecycleFixture(t, environment.ToolPolicyReview)
	repo := store.NativeLifecycleRepository()
	ctx := context.Background()
	now := time.Now().UTC()
	a, err := repo.RequestApproval(ctx, id, "55555555555555555555555555555555", now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	answer := nativetool.DecisionInput{ApprovalID: a.ID, ExpectedRevision: a.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "idempotent-validation-key", ObservedAt: now}
	if _, err := repo.DecideApproval(ctx, answer); err != nil {
		t.Fatal(err)
	}
	// CORRUPT SYNTHETIC DATA: historical idempotency still validates row identity.
	if _, err := store.database.ExecContext(ctx, `UPDATE native_tool_approvals SET binding_digest=zeroblob(32) WHERE approval_id=?`, a.ID); err != nil {
		t.Fatal(err)
	}
	defer store.database.ExecContext(ctx, `UPDATE native_tool_approvals SET binding_digest=? WHERE approval_id=?`, a.BindingDigest[:], a.ID)
	if _, err := repo.DecideApproval(ctx, answer); !errors.Is(err, nativetool.ErrInvalid) {
		t.Fatalf("corrupt idempotent answer accepted %v", err)
	}
}

func TestNativePageLateUpdatesAndCursorBudget(t *testing.T) {
	store, id, _ := nativeLifecycleFixture(t, environment.ToolPolicyReview)
	repo := store.NativeLifecycleRepository()
	ctx := context.Background()
	op, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	page, err := repo.Page(ctx, nativetool.PageRequest{Capture: op.Capture, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	cursor := page.NextSequence
	now := time.Now().UTC()
	a, err := repo.RequestApproval(ctx, id, "55555555555555555555555555555555", now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DecideApproval(ctx, nativetool.DecisionInput{ApprovalID: a.ID, ExpectedRevision: a.Revision, Decision: nativetool.DecisionDeny, IdempotencyKey: "page-native-answer-key", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	late, err := repo.Page(ctx, nativetool.PageRequest{Capture: op.Capture, AfterSequence: cursor, Limit: 200})
	if err != nil || len(late.Items) != 2 || late.Items[0].ID != id || late.Items[0].ApprovalID == nil || *late.Items[0].ApprovalID != a.ID || late.Items[0].State != "denied" {
		t.Fatalf("late updates %+v %v", late, err)
	}
	for i := 1; i <= 210; i++ {
		_, err := repo.Receive(ctx, nativetool.ReceiveInput{ID: nativetool.ID(fmt.Sprintf("%032x", i+100)), Capture: op.Capture, EnvironmentID: op.EnvironmentID, EnvironmentRevision: op.EnvironmentRevision, EnvironmentDigest: op.EnvironmentDigest, ConnectionID: "synthetic-page", TargetOrigin: op.TargetOrigin, Method: "POST", Kind: nativetool.RequestUnclassified, Recording: op.Recording, ObservedAt: now})
		if err != nil {
			t.Fatal(err)
		}
	}
	bounded, err := repo.Page(ctx, nativetool.PageRequest{Capture: op.Capture, AfterSequence: late.NextSequence, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(bounded)
	if len(encoded) > 128<<10 || len(bounded.Items) == 0 {
		t.Fatalf("budget items=%d bytes=%d", len(bounded.Items), len(encoded))
	}
	next, err := repo.Page(ctx, nativetool.PageRequest{Capture: op.Capture, AfterSequence: bounded.NextSequence, Limit: 200})
	if err != nil || len(next.Items) == 0 {
		t.Fatalf("next %+v %v", next, err)
	}
	var expected string
	if err := store.database.QueryRowContext(ctx, `SELECT operation_id FROM native_tool_events WHERE sequence>? ORDER BY sequence LIMIT 1`, bounded.NextSequence).Scan(&expected); err != nil {
		t.Fatal(err)
	}
	if next.Items[0].ID.String() != expected {
		t.Fatal("oversized boundary item was skipped")
	}
	foreign := captureidentity.Reference{Kind: op.Capture.Kind, ID: "other-capture"}
	empty, err := repo.Page(ctx, nativetool.PageRequest{Capture: foreign, Limit: 200})
	if err != nil || len(empty.Items) != 0 || empty.NextSequence != 0 {
		t.Fatalf("cross capture %+v %v", empty, err)
	}
}

func TestNativeLifecycleOwnerRenewalAndChange(t *testing.T) {
	store, id, revision := nativeLifecycleFixture(t, environment.ToolPolicyReview)
	repo := store.NativeLifecycleRepository()
	ctx := context.Background()
	op, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	a, err := repo.RequestApproval(ctx, id, "55555555555555555555555555555555", now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	next, err := repo.EnsureScope(ctx, nativetool.ScopeInput{Capture: op.Capture, ExpectedRevision: revision, OwnerEpoch: 1, ExpiresAt: now.Add(2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	still, err := repo.GetApproval(ctx, a.ID)
	if err != nil || still.State != "pending" {
		t.Fatalf("same owner renewal canceled question %+v %v", still, err)
	}
	if _, err := repo.EnsureScope(ctx, nativetool.ScopeInput{Capture: op.Capture, ExpectedRevision: next, OwnerEpoch: 2, ExpiresAt: now.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	closed, err := repo.GetApproval(ctx, a.ID)
	if err != nil || closed.State != "canceled" {
		t.Fatalf("owner replacement %+v %v", closed, err)
	}
	var barrier int
	var state string
	if err := store.database.QueryRowContext(ctx, `SELECT replay_barrier FROM native_tool_scopes WHERE capture_kind=? AND capture_id=?`, op.Capture.Kind, op.Capture.ID).Scan(&barrier); err != nil {
		t.Fatal(err)
	}
	if err := store.database.QueryRowContext(ctx, `SELECT state FROM native_tool_sessions WHERE session_id='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if barrier != 1 || state != "revoked" {
		t.Fatalf("old generation active barrier=%d session=%s", barrier, state)
	}
}

func TestNativeLifecycleCancelActiveRPCAndDuplicate(t *testing.T) {
	store, id, _ := nativeLifecycleFixture(t, environment.ToolPolicyReview)
	repo := store.NativeLifecycleRepository()
	binding, _ := nativeDecisionFixture(t, store, id)
	ctx := context.Background()
	plan := binding.Plan()
	session := binding.SessionID()
	catalog, _ := binding.CatalogDigest()
	now := time.Now().UTC()
	for index, target := range []string{"3", "\"3\"", "999"} {
		cancel, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestCancel, Plan: plan, Capture: binding.Capture(), AssignmentRevision: 1, OwnerEpoch: 1, SessionID: session, SessionRevision: 3, CatalogGeneration: 1, CatalogDigest: &catalog, ExactRPC: []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":` + target + `}}`)})
		if err != nil {
			t.Fatal(err)
		}
		cancelID := nativetool.ID(fmt.Sprintf("%032x", index+900))
		rpc, _ := cancel.RPCIDDigest()
		request := cancel.RequestDigest()
		input := nativetool.ReceiveInput{ID: cancelID, Capture: binding.Capture(), EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "synthetic-cancel", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "POST", Kind: nativetool.RequestCancel, SessionID: &session, RPCIDDigest: &rpc, RequestDigest: &request, Recording: plan.ContentRecording(), ObservedAt: now}
		received, err := repo.Receive(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		err = repo.Bind(ctx, received.OperationID, cancel, now)
		if index == 0 {
			if err != nil {
				t.Fatalf("active target denied: %v", err)
			}
			input.ID = "99999999999999999999999999999999"
			duplicate, err := repo.Receive(ctx, input)
			if err != nil || !duplicate.Duplicate || duplicate.OperationID != cancelID {
				t.Fatalf("duplicate cancel %+v %v", duplicate, err)
			}
		} else if !errors.Is(err, nativetool.ErrDenied) {
			t.Fatalf("unknown/typed target admitted: %v", err)
		}
	}
}

func TestNativeLifecycleFrozenPolicyAndLiveOff(t *testing.T) {
	store, id, scopeRevision := nativeLifecycleFixture(t, environment.ToolPolicyReview)
	repo := store.NativeLifecycleRepository()
	ctx := context.Background()
	binding, intents := nativeDecisionFixture(t, store, id)
	manager := newEnvironmentManager(t, store)
	publish := func(off bool) {
		t.Helper()
		snapshot, err := manager.Resolve(binding.Plan().EnvironmentID())
		if err != nil {
			t.Fatal(err)
		}
		candidate := snapshot.Aggregate()
		candidate.Revision++
		candidate.PolicySet = &environment.PolicySet{ToolMode: environment.ToolPolicyObserve}
		if off {
			candidate.ClientToolAccess = nil
		}
		draft, err := manager.SaveDraft(ctx, environment.DraftCommand{ExpectedBaseRevision: snapshot.Revision(), Candidate: candidate})
		if err != nil {
			t.Fatal(err)
		}
		preview, err := manager.Preview(ctx, candidate.ID, draft.Revision)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Publish(ctx, preview); err != nil {
			t.Fatal(err)
		}
	}
	publish(false)
	now := time.Now().UTC()
	if _, err := repo.RequestApproval(ctx, id, "55555555555555555555555555555555", now, now.Add(time.Minute)); err != nil {
		t.Fatalf("new publication overwrote assigned frozen Review: %v", err)
	}
	publish(true)
	if approval, err := repo.GetApproval(ctx, "55555555555555555555555555555555"); err != nil || approval.State != "canceled" {
		t.Fatalf("OFF pending question retained authority %+v %v", approval, err)
	}
	if _, err := repo.EnsureScope(ctx, nativetool.ScopeInput{Capture: binding.Capture(), ExpectedRevision: scopeRevision, OwnerEpoch: 2, ExpiresAt: now.Add(time.Hour)}); !errors.Is(err, nativetool.ErrDenied) {
		t.Fatalf("OFF owner generation activated %v", err)
	}
	plan := binding.Plan()
	session := binding.SessionID()
	catalog, _ := binding.CatalogDigest()
	next, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestToolsCall, Plan: plan, Capture: binding.Capture(), AssignmentRevision: 1, OwnerEpoch: 1, SessionID: session, SessionRevision: 3, CatalogGeneration: 1, CatalogDigest: &catalog, ExactRPC: []byte(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`), Intents: intents})
	if err != nil {
		t.Fatal(err)
	}
	rpc, _ := next.RPCIDDigest()
	digest := next.RequestDigest()
	newID := nativetool.ID("99999999999999999999999999999999")
	if _, err := repo.Receive(ctx, nativetool.ReceiveInput{ID: newID, Capture: binding.Capture(), EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "synthetic-off", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "POST", Kind: nativetool.RequestToolsCall, SessionID: &session, RPCIDDigest: &rpc, RequestDigest: &digest, Recording: plan.ContentRecording(), ObservedAt: now}); !errors.Is(err, nativetool.ErrDenied) {
		t.Fatalf("OFF retired session admitted: %v", err)
	}
}

func TestNativeProjectionRejectsUnsafeNameAndMissingEvent(t *testing.T) {
	for _, corrupt := range []string{"name", "event"} {
		t.Run(corrupt, func(t *testing.T) {
			store, id, _ := nativeLifecycleFixture(t, environment.ToolPolicyObserve)
			repo := store.NativeLifecycleRepository()
			ctx := context.Background()
			op, err := repo.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			// CORRUPT SYNTHETIC DATA: no production write API creates these rows.
			if corrupt == "event" {
				defer store.database.ExecContext(ctx, `INSERT INTO native_tool_events(operation_id,operation_revision,kind,occurred_at_unix_ms) VALUES(?,?,?,?)`, id, op.Revision, "validated", time.Now().UnixMilli())
				if _, err := store.database.ExecContext(ctx, `DELETE FROM native_tool_events WHERE operation_id=? AND operation_revision=?`, id, op.Revision); err != nil {
					t.Fatal(err)
				}
			} else {
				newID := nativetool.ID("99999999999999999999999999999999")
				defer store.database.ExecContext(ctx, `DELETE FROM native_tool_operations WHERE operation_id=?`, newID)
				if _, err := repo.Receive(ctx, nativetool.ReceiveInput{ID: newID, Capture: op.Capture, EnvironmentID: op.EnvironmentID, EnvironmentRevision: op.EnvironmentRevision, EnvironmentDigest: op.EnvironmentDigest, ConnectionID: "synthetic-corrupt", TargetOrigin: op.TargetOrigin, Method: "POST", Kind: nativetool.RequestToolsCall, Recording: op.Recording, ObservedAt: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
				if _, err := store.database.ExecContext(ctx, `INSERT INTO native_tool_bindings SELECT ?,session_id,session_revision,catalog_generation,catalog_digest,route_id,route_revision,owner_epoch,assignment_revision,request_digest,params_digest,group_digest,binding_digest,replay_key,parent_operation_id,server_request_digest,'unsafe name',NULL FROM native_tool_bindings WHERE operation_id=?`, newID, id); err != nil {
					t.Fatal(err)
				}
				if _, err := store.database.ExecContext(ctx, `UPDATE native_tool_operations SET state='validated',revision=2 WHERE operation_id=?`, newID); err != nil {
					t.Fatal(err)
				}
				if _, err := store.database.ExecContext(ctx, `INSERT INTO native_tool_events(operation_id,operation_revision,kind,occurred_at_unix_ms) VALUES(?,2,'validated',?)`, newID, time.Now().UnixMilli()); err != nil {
					t.Fatal(err)
				}
				id = newID
			}
			if _, err := repo.Get(ctx, id); !errors.Is(err, nativetool.ErrInvalid) {
				t.Fatalf("corrupt %s accepted: %v", corrupt, err)
			}
		})
	}
}

func TestNativeLifecycleReplyFreezesParentCatalog(t *testing.T) {
	store, parentID, authorityRevision := nativeLifecycleFixture(t, environment.ToolPolicyReview)
	repo := store.NativeToolRepository()
	parent, _ := nativeDecisionFixture(t, store, parentID)
	ctx := context.Background()
	now := time.Now().UTC()
	plan := parent.Plan()
	session := parent.SessionID()
	catalog, _ := parent.CatalogDigest()
	nativeAllow(t, repo, parentID, now)
	receipt, err := repo.Start(ctx, parentID, "parent-catalog-start", authorityRevision, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.VerifyStartReceipt(ctx, receipt, now); err != nil {
		t.Fatal(err)
	}
	stream, err := repo.OpenObservedStream(ctx, receipt, parent, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	list, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestToolsList, Plan: plan, Capture: parent.Capture(), AssignmentRevision: 1, OwnerEpoch: 1, SessionID: session, SessionRevision: 3, CatalogGeneration: 1, CatalogDigest: &catalog, ExactRPC: []byte(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)})
	if err != nil {
		t.Fatal(err)
	}
	rpc, _ := list.RPCIDDigest()
	digest := list.RequestDigest()
	listID := nativetool.ID("66666666666666666666666666666666")
	if _, err := repo.Receive(ctx, nativetool.ReceiveInput{ID: listID, Capture: parent.Capture(), EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "synthetic-list", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "POST", Kind: nativetool.RequestToolsList, SessionID: &session, RPCIDDigest: &rpc, RequestDigest: &digest, Recording: plan.ContentRecording(), ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Bind(ctx, listID, list, now); err != nil {
		t.Fatal(err)
	}
	listReceipt, err := repo.Start(ctx, listID, "parent-catalog-list", authorityRevision, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.VerifyStartReceipt(ctx, listReceipt, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Finish(ctx, listID, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	observer, err := nativetool.NewCatalogObserver(nativetool.CatalogScope{Capture: parent.Capture(), Plan: plan, AssignmentRevision: 1, OwnerEpoch: 1, SessionID: session, SessionRevision: 4, CatalogGeneration: 2})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := observer.Observe([]nativetool.CatalogPageObservation{{ExactRequest: []byte(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`), ExactResponse: []byte(`{"jsonrpc":"2.0","id":4,"result":{"tools":[]}}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetCatalog(ctx, session, 3, evidence, now); err != nil {
		t.Fatal(err)
	}
	for index, kind := range []string{"valid", "parent", "owner", "session"} {
		t.Run(kind, func(t *testing.T) {
			replyID := nativetool.ID(fmt.Sprintf("%032x", index+1000))
			request := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":"server-%d","method":"ping"}`, index))
			raw := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":"server-%d","result":{}}`, index))
			server := nativetool.Digest(sha256.Sum256(request))
			input := nativetool.BindingInput{Kind: nativetool.RequestServerReply, Plan: plan, Capture: parent.Capture(), AssignmentRevision: 1, OwnerEpoch: 1, SessionID: session, SessionRevision: 3, CatalogGeneration: 1, CatalogDigest: &catalog, ParentOperationID: &parentID, ServerRequestDigest: &server, ExactRPC: raw}
			switch kind {
			case "parent":
				bad := nativetool.ID("11111111111111111111111111111111")
				input.ParentOperationID = &bad
			case "owner":
				input.OwnerEpoch = 2
			case "session":
				input.SessionID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			}
			reply, err := nativetool.NewBinding(input)
			if err != nil {
				t.Fatal(err)
			}
			err = nativeBindObservedPing(t, repo, replyID, reply, parent, stream, request, raw, now)
			if kind == "valid" {
				if err != nil {
					t.Fatalf("current catalog rewrote parent authority: %v", err)
				}
				if _, err := repo.RequestApproval(ctx, replyID, "77777777777777777777777777777777", now, now.Add(time.Minute)); err != nil {
					t.Fatalf("frozen reply question: %v", err)
				}
			} else if err == nil {
				t.Fatal("wrong parent/owner/session admitted")
			}
		})
	}
	for _, table := range []string{"native_tool_deliveries", "native_tool_once"} {
		var count int
		if err := store.database.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil || count != 2 {
			t.Fatalf("real parent/list execution %s %d %v", table, count, err)
		}
	}
}

func TestNativeLifecycleReceiveRejectsWrongFrozenTargetAndDuplicateID(t *testing.T) {
	store, id, _ := nativeLifecycleFixture(t, environment.ToolPolicyObserve)
	repo := store.NativeLifecycleRepository()
	ctx := context.Background()
	op, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	input := nativetool.ReceiveInput{ID: "99999999999999999999999999999999", Capture: op.Capture, EnvironmentID: op.EnvironmentID, EnvironmentRevision: op.EnvironmentRevision, EnvironmentDigest: op.EnvironmentDigest, ConnectionID: "synthetic-wrong-target", TargetOrigin: "https://foreign.example", Method: "POST", Kind: nativetool.RequestUnclassified, Recording: op.Recording, ObservedAt: time.Now().UTC()}
	if _, err := repo.Receive(ctx, input); !errors.Is(err, nativetool.ErrInvalid) {
		t.Fatalf("wrong frozen target accepted: %v", err)
	}
	input.ID = id
	input.TargetOrigin = op.TargetOrigin
	if _, err := repo.Receive(ctx, input); !errors.Is(err, nativetool.ErrConflict) {
		t.Fatalf("duplicate operation identity: %v", err)
	}
}

func TestNativeLifecycleReadyRequiresInitializedNotTwoInitializes(t *testing.T) {
	store, id, _ := nativeLifecycleFixture(t, environment.ToolPolicyObserve)
	repo := store.NativeLifecycleRepository()
	parent, _ := nativeDecisionFixture(t, store, id)
	ctx := context.Background()
	now := time.Now().UTC()
	plan := parent.Plan()
	session := parent.SessionID()
	// HISTORICAL SYNTHETIC INVALID HANDSHAKE HISTORY: no network or execution.
	if _, err := store.database.ExecContext(ctx, `DELETE FROM native_tool_operations WHERE operation_id='22222222222222222222222222222222'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.ExecContext(ctx, `UPDATE native_tool_sessions SET state='initializing',revision=1,catalog_generation=0,catalog_digest=NULL WHERE session_id=?`, session); err != nil {
		t.Fatal(err)
	}
	catalog, _ := parent.CatalogDigest()
	defer store.database.ExecContext(ctx, `UPDATE native_tool_sessions SET state='ready',revision=3,catalog_generation=1,catalog_digest=? WHERE session_id=?`, catalog[:], session)
	second, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestInitialize, Plan: plan, Capture: parent.Capture(), AssignmentRevision: 1, OwnerEpoch: 1, SessionID: session, SessionRevision: 1, ExactRPC: []byte(`{"jsonrpc":"2.0","id":5,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	rpc, _ := second.RPCIDDigest()
	digest := second.RequestDigest()
	secondID := nativetool.ID("77777777777777777777777777777777")
	if _, err := repo.Receive(ctx, nativetool.ReceiveInput{ID: secondID, Capture: parent.Capture(), EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "synthetic-second-initialize", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "POST", Kind: nativetool.RequestInitialize, SessionID: &session, RPCIDDigest: &rpc, RequestDigest: &digest, Recording: plan.ContentRecording(), ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Bind(ctx, secondID, second, now); err != nil {
		t.Fatal(err)
	}
	seedNativeHistoricalCompletion(t, store, secondID, now)
	if _, err := repo.ReadySession(ctx, session, 1, now); !errors.Is(err, nativetool.ErrDenied) {
		t.Fatalf("two initialize rows replaced required initialized notification: %v", err)
	}
}

func TestNativeLifecycleExpiredCaptureAndChangedAssignment(t *testing.T) {
	for _, kind := range []string{"capture_expired", "assignment_changed"} {
		t.Run(kind, func(t *testing.T) {
			store, id, _ := nativeLifecycleFixture(t, environment.ToolPolicyReview)
			repo := store.NativeLifecycleRepository()
			ctx := context.Background()
			op, err := repo.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			if kind == "capture_expired" {
				// HISTORICAL SYNTHETIC EXPIRY: constrained actual Capture row; observed time
				// advances past its deadline, with session/scope still within their hour.
				expires := now.Add(time.Minute)
				if _, err := store.database.ExecContext(ctx, `UPDATE manual_captures SET lifetime='temporary',expires_at_unix_ms=? WHERE capture_id=?`, expires.UnixMilli(), op.Capture.ID); err != nil {
					t.Fatal(err)
				}
				now = expires.Add(time.Millisecond)
			} else {
				assignment, exists, err := store.CaptureAssignmentRepository().Load(ctx, op.Capture)
				if err != nil || !exists {
					t.Fatalf("assignment %v %v", exists, err)
				}
				previous := assignment.Revision
				assignment.Revision++
				assignment.UpdatedAt = now.Truncate(time.Millisecond)
				result, err := store.CaptureAssignmentRepository().Write(ctx, previous, assignment)
				if err != nil || result.Outcome != captureassignment.CommitOutcomeCommitted {
					t.Fatalf("assignment transition %+v %v", result, err)
				}
			}
			if _, err := repo.RequestApproval(ctx, id, "55555555555555555555555555555555", now, now.Add(time.Minute)); !errors.Is(err, nativetool.ErrDenied) {
				t.Fatalf("stale Capture/assignment admitted: %v", err)
			}
		})
	}
}

func TestNativeReviewCancellationExpiryAndLateAnswer(t *testing.T) {
	for _, closure := range []nativetool.ApprovalClosure{nativetool.ApprovalClosureCancelled, nativetool.ApprovalClosureExpired} {
		t.Run(string(closure), func(t *testing.T) {
			store, id, _ := nativeLifecycleFixture(t, environment.ToolPolicyReview)
			repo := store.NativeLifecycleRepository()
			ctx := context.Background()
			now := time.Now().UTC()
			expires := now.Add(time.Minute)
			a, err := repo.RequestApproval(ctx, id, "55555555555555555555555555555555", now, expires)
			if err != nil {
				t.Fatal(err)
			}
			observed := now
			if closure == nativetool.ApprovalClosureExpired {
				if _, err := repo.CloseApproval(ctx, a.ID, a.Revision, closure, now); !errors.Is(err, nativetool.ErrDenied) {
					t.Fatalf("premature expiry %v", err)
				}
				observed = expires
			}
			closed, err := repo.CloseApproval(ctx, a.ID, a.Revision, closure, observed)
			if err != nil || closed.State != string(closure) {
				t.Fatalf("close %+v %v", closed, err)
			}
			if _, err := repo.DecideApproval(ctx, nativetool.DecisionInput{ApprovalID: a.ID, ExpectedRevision: a.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "late-native-answer-key", ObservedAt: observed}); !errors.Is(err, nativetool.ErrConflict) {
				t.Fatalf("late answer %v", err)
			}
			path := store.databasePath
			if err := store.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			reopened := openTestStore(t, path)
			defer shutdownTestStore(t, reopened)
			persisted, err := reopened.NativeLifecycleRepository().GetApproval(ctx, a.ID)
			if err != nil || persisted.State != closed.State {
				t.Fatalf("reopened %+v %v", persisted, err)
			}
		})
	}
}
