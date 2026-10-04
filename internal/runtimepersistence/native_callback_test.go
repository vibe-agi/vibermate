package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func TestNativeObservedReplyRejectsMissingOrForeignRegistration(t *testing.T) {
	store, parentID, revision := nativeStoreFixture(t, environment.ToolPolicyReview)
	repo := nativeFullRepository(t, store)
	now := time.Now().UTC().Truncate(time.Millisecond)
	parent, _ := nativeDecisionFixture(t, store, parentID)
	nativeAllow(t, repo, parentID, now)
	if _, err := repo.Start(context.Background(), parentID, "unregistered-parent", revision, now); err != nil {
		t.Fatal(err)
	}
	id := nativetool.ID("77777777777777777777777777777777")
	request := []byte(`{"jsonrpc":"2.0","id":"unregistered","method":"ping"}`)
	raw := []byte(`{"jsonrpc":"2.0","id":"unregistered","result":{}}`)
	server := nativetool.Digest(sha256.Sum256(request))
	catalog, _ := parent.CatalogDigest()
	plan, session := parent.Plan(), parent.SessionID()
	b, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestServerReply, Plan: plan, Capture: parent.Capture(), AssignmentRevision: parent.AssignmentRevision(), OwnerEpoch: parent.OwnerEpoch(), SessionID: session, SessionRevision: parent.SessionRevision(), CatalogGeneration: parent.CatalogGeneration(), CatalogDigest: &catalog, ParentOperationID: &parentID, ServerRequestDigest: &server, ExactRPC: raw})
	if err != nil {
		t.Fatal(err)
	}
	rpc, _ := b.RPCIDDigest()
	digest := b.RequestDigest()
	if _, err := repo.Receive(context.Background(), nativetool.ReceiveInput{ID: id, Capture: parent.Capture(), EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "synthetic-unregistered", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "POST", Kind: nativetool.RequestServerReply, SessionID: &session, RPCIDDigest: &rpc, RequestDigest: &digest, Recording: plan.ContentRecording(), ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Bind(context.Background(), id, b, now); err == nil {
		t.Fatal("unregistered callback obtained durable reply binding from SQL parent alone")
	}
}

type callbackSQLFixture struct {
	store          *Store
	repo           nativetool.Repository
	now            time.Time
	revision       uint64
	parent, child  nativetool.ID
	binding, reply nativetool.Binding
	receipt        nativetool.StartReceipt
	stream         nativetool.StreamAuthority
	callback       nativetool.CallbackAuthority
	facts          nativetool.CallbackFacts
	witness        nativetool.RegisteredParentWitness
}

func newCallbackSQLFixture(t *testing.T) callbackSQLFixture {
	t.Helper()
	store, id, revision := nativeStoreFixture(t, environment.ToolPolicyReview)
	repo := nativeFullRepository(t, store)
	now := time.Now().UTC().Truncate(time.Millisecond)
	base, _ := nativeDecisionFixture(t, store, id)
	catalog, _ := base.CatalogDigest()
	parent := nativetool.ID("75757575757575757575757575757575")
	child := nativetool.ID("76767676767676767676767676767676")
	b, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestGetStream, Plan: base.Plan(), Capture: base.Capture(), AssignmentRevision: base.AssignmentRevision(), OwnerEpoch: base.OwnerEpoch(), SessionID: base.SessionID(), SessionRevision: base.SessionRevision(), CatalogGeneration: base.CatalogGeneration(), CatalogDigest: &catalog})
	if err != nil {
		t.Fatal(err)
	}
	plan, sid := b.Plan(), b.SessionID()
	_, err = repo.Receive(context.Background(), nativetool.ReceiveInput{ID: parent, Capture: b.Capture(), EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "callback-get", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "GET", Kind: b.Kind(), SessionID: &sid, Recording: plan.ContentRecording(), ObservedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Bind(context.Background(), parent, b, now); err != nil {
		t.Fatal(err)
	}
	receipt, err := repo.Start(context.Background(), parent, "callback-get-origin", revision, now)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repo.OpenObservedStream(context.Background(), receipt, b, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	request := []byte(`{"jsonrpc":"2.0","id":"registered","method":"ping"}`)
	raw := []byte(`{"jsonrpc":"2.0","id":"registered","result":{}}`)
	observer, err := nativetool.NewParentObserver(parent, b)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := observer.ObserveRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	callback, err := repo.RegisterObservedCallback(context.Background(), stream, facts, now)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestServerReply, Plan: plan, Capture: b.Capture(), AssignmentRevision: b.AssignmentRevision(), OwnerEpoch: b.OwnerEpoch(), SessionID: sid, SessionRevision: b.SessionRevision(), CatalogGeneration: b.CatalogGeneration(), CatalogDigest: &catalog, ParentOperationID: &parent, ServerRequestDigest: &facts.RequestDigest, ExactRPC: raw})
	if err != nil {
		t.Fatal(err)
	}
	witness, err := observer.ObserveReply(child, request, raw)
	if err != nil {
		t.Fatal(err)
	}
	nativeReceiveOnly(t, repo, child, reply, now)
	return callbackSQLFixture{store, repo, now, revision, parent, child, b, reply, receipt, stream, callback, facts, witness}
}

func TestNativeObservedCallbackRechecksEveryAuthoritySeam(t *testing.T) {
	for _, stage := range []string{"bind", "question", "decision", "start", "verify"} {
		for _, edge := range []string{"missing", "foreign", "expiry", "retired", "off", "audit"} {
			t.Run(stage+"/"+edge, func(t *testing.T) {
				f := newCallbackSQLFixture(t)
				ctx := context.Background()
				now := f.now
				if stage != "bind" {
					if err := f.repo.BindObservedReply(ctx, f.child, f.reply, f.callback, f.witness, now); err != nil {
						t.Fatal(err)
					}
				}
				var approval nativetool.Approval
				if stage == "decision" || stage == "start" || stage == "verify" {
					var err error
					approval, err = f.repo.RequestApproval(ctx, f.child, "78787878787878787878787878787878", now, now.Add(time.Minute))
					if err != nil {
						t.Fatal(err)
					}
				}
				if stage == "start" || stage == "verify" {
					if _, err := f.repo.DecideApproval(ctx, nativetool.DecisionInput{ApprovalID: approval.ID, ExpectedRevision: approval.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "callback-seam-allow", ObservedAt: now}); err != nil {
						t.Fatal(err)
					}
				}
				var receipt nativetool.StartReceipt
				if stage == "verify" {
					var err error
					receipt, err = f.repo.Start(ctx, f.child, "callback-seam-start", f.revision, now)
					if err != nil {
						t.Fatal(err)
					}
				}
				switch edge {
				case "missing":
					f.store.nativeTools.issuerMu.Lock()
					saved := f.store.nativeTools.callbacks[f.callback]
					delete(f.store.nativeTools.callbacks, f.callback)
					f.store.nativeTools.issuerMu.Unlock()
					defer func() {
						f.store.nativeTools.issuerMu.Lock()
						f.store.nativeTools.callbacks[f.callback] = saved
						f.store.nativeTools.issuerMu.Unlock()
					}()
				case "foreign":
					issuer, err := nativetool.NewStartReceiptIssuer()
					if err != nil {
						t.Fatal(err)
					}
					foreign, err := issuer.IssueCallback()
					if err != nil {
						t.Fatal(err)
					}
					if stage == "bind" {
						f.callback = foreign
					} else {
						f.store.nativeTools.issuerMu.Lock()
						f.store.nativeTools.replies[f.child] = foreign
						f.store.nativeTools.issuerMu.Unlock()
					}
				case "expiry":
					now = now.Add(time.Minute)
				case "retired":
					if err := f.repo.RetireObservedCallback(ctx, f.callback, now); err != nil {
						t.Fatal(err)
					}
				case "off":
					if _, err := f.store.database.Exec(`UPDATE native_tool_scopes SET state='revoked',revision=revision+1 WHERE capture_kind=? AND capture_id=?`, f.binding.Capture().Kind, f.binding.Capture().ID); err != nil {
						t.Fatal(err)
					}
				case "audit":
					var original string
					if err := f.store.database.QueryRow(`SELECT connection_id FROM runtime_egress_attempts WHERE attempt_id=?`, f.receipt.AttemptID()).Scan(&original); err != nil {
						t.Fatal(err)
					}
					defer f.store.database.Exec(`UPDATE runtime_egress_attempts SET connection_id=? WHERE attempt_id=?`, original, f.receipt.AttemptID())
					if _, err := f.store.database.Exec(`UPDATE runtime_egress_attempts SET connection_id='foreign' WHERE attempt_id=?`, f.receipt.AttemptID()); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				switch stage {
				case "bind":
					err = f.repo.BindObservedReply(ctx, f.child, f.reply, f.callback, f.witness, now)
				case "question":
					_, err = f.repo.RequestApproval(ctx, f.child, "78787878787878787878787878787878", now, now.Add(time.Minute))
				case "decision":
					_, err = f.repo.DecideApproval(ctx, nativetool.DecisionInput{ApprovalID: approval.ID, ExpectedRevision: approval.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "callback-seam-allow", ObservedAt: now})
				case "start":
					_, err = f.repo.Start(ctx, f.child, "callback-seam-start", f.revision, now)
				case "verify":
					err = f.repo.VerifyStartReceipt(ctx, receipt, now)
				}
				if err == nil {
					t.Fatal("invalid callback authority passed " + stage)
				}
			})
		}
	}
}

func TestNativeObservedCallbackAmbiguousStartCannotIssueLateReceipt(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprint(rollback), func(t *testing.T) {
			f := newCallbackSQLFixture(t)
			ctx := context.Background()
			if err := f.repo.BindObservedReply(ctx, f.child, f.reply, f.callback, f.witness, f.now); err != nil {
				t.Fatal(err)
			}
			nativeAllow(t, f.repo, f.child, f.now)
			committer := setCallbackCommitter(f.store, commitThenError{})
			if rollback {
				setCallbackCommitter(f.store, rollbackThenError{})
			}
			receipt, err := f.repo.Start(ctx, f.child, "ambiguous-callback", f.revision, f.now)
			if err == nil || receipt.OperationID() != "" {
				t.Fatal("ambiguous Start returned receipt")
			}
			setCallbackCommitter(f.store, committer)
			receipt, err = f.repo.Start(ctx, f.child, "delayed-callback", f.revision, f.now)
			if err == nil || receipt.OperationID() != "" {
				t.Fatal("ambiguous callback Start issued delayed receipt")
			}
		})
	}
}

func TestNativeObservedGETCompletesAuditBeforePendingDecision(t *testing.T) {
	f := newCallbackSQLFixture(t)
	ctx := context.Background()
	if err := f.repo.BindObservedReply(ctx, f.child, f.reply, f.callback, f.witness, f.now); err != nil {
		t.Fatal(err)
	}
	approval, err := f.repo.RequestApproval(ctx, f.child, "78787878787878787878787878787878", f.now, f.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repo.FinishAttempt(ctx, f.receipt, nativetool.PhysicalFinishInput{Outcome: "completed", Reason: "completed", Counts: &nativetool.ObservedCounts{BytesIn: 21}, ObservedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	before, err := scanEgressAttempt(f.store.database.QueryRow(egressAttemptSelect+` WHERE attempt_id=?`, f.receipt.AttemptID()))
	if err != nil || !before.Attempt.Terminal() {
		t.Fatal("GET audit still open", err)
	}
	if f.repo.VerifyStartReceipt(ctx, f.receipt, f.now) == nil {
		t.Fatal("completed GET receipt reused")
	}
	if _, err := f.repo.RegisterObservedCallback(ctx, f.stream, f.facts, f.now); err == nil {
		t.Fatal("closed GET re-registered callback")
	}
	if _, err := f.repo.DecideApproval(ctx, nativetool.DecisionInput{ApprovalID: approval.ID, ExpectedRevision: approval.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "late-callback-decision", ObservedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	receipt, err := f.repo.Start(ctx, f.child, "late-callback-start", f.revision, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repo.VerifyStartReceipt(ctx, receipt, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Finish(ctx, f.child, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", ObservedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	after, err := scanEgressAttempt(f.store.database.QueryRow(egressAttemptSelect+` WHERE attempt_id=?`, f.receipt.AttemptID()))
	if err != nil || after.Attempt != before.Attempt {
		t.Fatal("reply rewrote GET audit", err)
	}
	if f.repo.VerifyStartReceipt(ctx, receipt, f.now) == nil {
		t.Fatal("completed reply receipt reused")
	}
}

func TestNativeObservedCallbackForeignStoreAndConsumedIdentity(t *testing.T) {
	f, other := newCallbackSQLFixture(t), newCallbackSQLFixture(t)
	ctx := context.Background()
	if _, err := other.repo.RegisterObservedCallback(ctx, f.stream, f.facts, f.now); err == nil {
		t.Fatal("foreign Store stream registered")
	}
	if err := f.repo.BindObservedReply(ctx, f.child, f.reply, other.callback, f.witness, f.now); err == nil {
		t.Fatal("foreign Store callback bound")
	}
	if err := f.repo.BindObservedReply(ctx, f.child, f.reply, f.callback, f.witness, f.now); err != nil {
		t.Fatal(err)
	}
	nativeAllow(t, f.repo, f.child, f.now)
	if _, err := f.repo.Start(ctx, f.child, "consumed-callback", f.revision, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Finish(ctx, f.child, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", ObservedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	f.store.nativeTools.issuerMu.Lock()
	_, retained := f.store.nativeTools.callbacks[f.callback]
	f.store.nativeTools.issuerMu.Unlock()
	if retained {
		t.Fatal("consumed live registration retained")
	}
	if _, err := f.repo.RegisterObservedCallback(ctx, f.stream, f.facts, f.now); err == nil {
		t.Fatal("consumed identity re-registered")
	}
	conflict := f.facts
	conflict.RequestDigest = nativetool.Digest{9}
	if _, err := f.repo.RegisterObservedCallback(ctx, f.stream, conflict, f.now); err == nil {
		t.Fatal("consumed identity changed request")
	}
}

func TestNativeObservedCallbackAmbiguousApprovalFencesContinuation(t *testing.T) {
	for _, stage := range []string{"question", "decision"} {
		for _, rollback := range []bool{false, true} {
			t.Run(fmt.Sprint(stage, rollback), func(t *testing.T) {
				f := newCallbackSQLFixture(t)
				ctx := context.Background()
				if err := f.repo.BindObservedReply(ctx, f.child, f.reply, f.callback, f.witness, f.now); err != nil {
					t.Fatal(err)
				}
				var a nativetool.Approval
				if stage == "decision" {
					var err error
					a, err = f.repo.RequestApproval(ctx, f.child, "78787878787878787878787878787878", f.now, f.now.Add(time.Minute))
					if err != nil {
						t.Fatal(err)
					}
				}
				original := setCallbackCommitter(f.store, commitThenError{})
				if rollback {
					setCallbackCommitter(f.store, rollbackThenError{})
				}
				var err error
				if stage == "question" {
					_, err = f.repo.RequestApproval(ctx, f.child, "78787878787878787878787878787878", f.now, f.now.Add(time.Minute))
				} else {
					_, err = f.repo.DecideApproval(ctx, nativetool.DecisionInput{ApprovalID: a.ID, ExpectedRevision: a.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "ambiguous-callback-approval", ObservedAt: f.now})
				}
				setCallbackCommitter(f.store, original)
				if err == nil {
					t.Fatal("fault not exercised")
				}
				if stage == "question" {
					if _, err := f.repo.RequestApproval(ctx, f.child, "78787878787878787878787878787878", f.now, f.now.Add(time.Minute)); err == nil {
						t.Fatal("ambiguous question renewed callback authority")
					}
				} else {
					if _, err := f.repo.Start(ctx, f.child, "after-unknown-decision", f.revision, f.now); err == nil {
						t.Fatal("ambiguous callback decision granted send")
					}
				}
			})
		}
	}
}

func TestNativeObservedCallbackCommitCleanupPreservesKnownGET(t *testing.T) {
	for _, first := range []string{"finish", "retire", "callback-retire", "session", "revoke"} {
		for _, rollback := range []bool{false, true} {
			t.Run(fmt.Sprint(first, rollback), func(t *testing.T) {
				f := newCallbackSQLFixture(t)
				ctx := context.Background()
				if err := f.repo.BindObservedReply(ctx, f.child, f.reply, f.callback, f.witness, f.now); err != nil {
					t.Fatal(err)
				}
				nativeAllow(t, f.repo, f.child, f.now)
				childReceipt, err := f.repo.Start(ctx, f.child, "cleanup-callback-send", f.revision, f.now)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.repo.FinishAttempt(ctx, f.receipt, nativetool.PhysicalFinishInput{Outcome: "completed", Reason: "completed", Counts: &nativetool.ObservedCounts{BytesIn: 29}, ObservedAt: f.now}); err != nil {
					t.Fatal(err)
				}
				before, err := scanEgressAttempt(f.store.database.QueryRow(egressAttemptSelect+` WHERE attempt_id=?`, f.receipt.AttemptID()))
				if err != nil {
					t.Fatal(err)
				}
				original := setCallbackCommitter(f.store, commitThenError{})
				if rollback {
					setCallbackCommitter(f.store, rollbackThenError{})
				}
				if first == "finish" {
					err = f.repo.Finish(ctx, f.child, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", ObservedAt: f.now})
				} else if first == "callback-retire" {
					err = f.repo.RetireObservedCallback(ctx, f.callback, f.now)
				} else if first == "session" {
					_, err = f.repo.CloseSession(ctx, f.binding.SessionID(), f.binding.SessionRevision(), f.now)
				} else if first == "revoke" {
					err = f.repo.Revoke(ctx, f.binding.Capture(), f.revision, "revoked", f.now)
				} else {
					err = f.repo.RetireObservedStream(ctx, f.stream, f.now)
				}
				setCallbackCommitter(f.store, original)
				if err == nil {
					t.Fatal("fault not exercised")
				}
				if f.repo.VerifyStartReceipt(ctx, childReceipt, f.now) == nil {
					t.Fatal("cleanup-only callback still sendable")
				}
				if err := f.repo.RetireObservedStream(ctx, f.stream, f.now); err != nil {
					t.Fatal(err)
				}
				child, err := f.repo.Get(ctx, f.child)
				if err != nil || child.CompletedAt == nil {
					t.Fatal("child cleanup lost", err)
				}
				audit, err := scanEgressAttempt(f.store.database.QueryRow(egressAttemptSelect+` WHERE attempt_id=?`, childReceipt.AttemptID()))
				if err != nil || !audit.Attempt.Terminal() {
					t.Fatal("callback audit left open", err)
				}
				after, err := scanEgressAttempt(f.store.database.QueryRow(egressAttemptSelect+` WHERE attempt_id=?`, f.receipt.AttemptID()))
				if err != nil || before.Attempt != after.Attempt {
					t.Fatal("known GET audit rewritten", err)
				}
				parent, err := f.repo.Get(ctx, f.parent)
				if err != nil || parent.State != "completed" {
					t.Fatal("known GET outcome lost", err)
				}
			})
		}
	}
}

func TestNativeObservedCallbackRetirementSettlesStartedChild(t *testing.T) {
	for _, completion := range []string{"retire", "committed-result", "rolled-back-result"} {
		t.Run(completion, func(t *testing.T) {
			store, id, revision := nativeStoreFixture(t, environment.ToolPolicyReview)
			repo := nativeFullRepository(t, store)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			b, _ := nativeDecisionFixture(t, store, id)
			nativeAllow(t, repo, id, now)
			receipt, err := repo.Start(ctx, id, "callback-cleanup-parent", revision, now)
			if err != nil {
				t.Fatal(err)
			}
			stream, err := repo.OpenObservedStream(ctx, receipt, b, time.Minute, now)
			if err != nil {
				t.Fatal(err)
			}
			request := []byte(`{"jsonrpc":"2.0","id":"cleanup","method":"ping"}`)
			raw := []byte(`{"jsonrpc":"2.0","id":"cleanup","result":{}}`)
			server := nativetool.Digest(sha256.Sum256(request))
			catalog, _ := b.CatalogDigest()
			child := nativetool.ID(fmt.Sprintf("%032x", 500))
			reply, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestServerReply, Plan: b.Plan(), Capture: b.Capture(), AssignmentRevision: b.AssignmentRevision(), OwnerEpoch: b.OwnerEpoch(), SessionID: b.SessionID(), SessionRevision: b.SessionRevision(), CatalogGeneration: b.CatalogGeneration(), CatalogDigest: &catalog, ParentOperationID: &id, ServerRequestDigest: &server, ExactRPC: raw})
			if err != nil {
				t.Fatal(err)
			}
			if err := nativeBindObservedPing(t, repo, child, reply, b, stream, request, raw, now); err != nil {
				t.Fatal(err)
			}
			nativeAllow(t, repo, child, now)
			childReceipt, err := repo.Start(ctx, child, "callback-cleanup-child", revision, now)
			if err != nil {
				t.Fatal(err)
			}
			if completion != "retire" {
				original := setCallbackCommitter(store, commitThenError{})
				if completion == "rolled-back-result" {
					setCallbackCommitter(store, rollbackThenError{})
				}
				err := repo.CompleteStreamRPC(ctx, stream, nativetool.LogicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: now})
				setCallbackCommitter(store, original)
				if err == nil {
					t.Fatal("completion fault not exercised")
				}
			}
			if err := repo.RetireObservedStream(ctx, stream, now); err != nil {
				t.Fatal(err)
			}
			op, err := repo.Get(ctx, child)
			if err != nil || op.CompletedAt == nil || op.State != "uncertain" {
				t.Fatalf("started callback lost uncertainty: %+v %v", op, err)
			}
			audit, err := scanEgressAttempt(store.database.QueryRow(egressAttemptSelect+` WHERE attempt_id=?`, childReceipt.AttemptID()))
			if err != nil || !audit.Attempt.Terminal() {
				t.Fatalf("callback physical audit orphaned: %+v %v", audit, err)
			}
		})
	}
}

func nativeBindObservedPing(t *testing.T, repo nativetool.Repository, id nativetool.ID, b, parent nativetool.Binding, stream nativetool.StreamAuthority, request, reply []byte, now time.Time) error {
	t.Helper()
	parentID, _ := b.ParentOperationID()
	observer, err := nativetool.NewParentObserver(parentID, parent)
	if err != nil {
		return err
	}
	facts, err := observer.ObserveRequest(request)
	if err != nil {
		return err
	}
	authority, err := repo.RegisterObservedCallback(context.Background(), stream, facts, now)
	if err != nil {
		return err
	}
	witness, err := observer.ObserveReply(id, request, reply)
	if err != nil {
		return err
	}
	plan, sid := parent.Plan(), parent.SessionID()
	rpc, _ := b.RPCIDDigest()
	digest := b.RequestDigest()
	if _, err := repo.Receive(context.Background(), nativetool.ReceiveInput{ID: id, Capture: parent.Capture(), EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "observed-ping-fixture", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "POST", Kind: nativetool.RequestServerReply, SessionID: &sid, RPCIDDigest: &rpc, RequestDigest: &digest, Recording: plan.ContentRecording(), ObservedAt: now}); err != nil {
		return err
	}
	return repo.BindObservedReply(context.Background(), id, b, authority, witness, now)
}

func setCallbackCommitter(store *Store, next transactionCommitter) transactionCommitter {
	store.nativeTools.issuerMu.Lock()
	defer store.nativeTools.issuerMu.Unlock()
	previous := store.nativeTools.committer
	store.nativeTools.committer = next
	return previous
}

func TestNativeObservedExpiredGETKeepsStartedChildCleanup(t *testing.T) {
	f := newCallbackSQLFixture(t)
	ctx := context.Background()
	if err := f.repo.BindObservedReply(ctx, f.child, f.reply, f.callback, f.witness, f.now); err != nil {
		t.Fatal(err)
	}
	nativeAllow(t, f.repo, f.child, f.now)
	if _, err := f.repo.Start(ctx, f.child, "expiry-child", f.revision, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.FinishAttempt(ctx, f.receipt, nativetool.PhysicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	expired := f.now.Add(2 * time.Minute)
	if _, err := f.repo.OpenObservedStream(ctx, f.receipt, f.binding, time.Minute, expired); err == nil {
		t.Fatal("finished GET reopened")
	}
	if err := f.repo.RetireObservedStream(ctx, f.stream, expired); err != nil {
		t.Fatal(err)
	}
	op, err := f.repo.Get(ctx, f.child)
	if err != nil || op.CompletedAt == nil {
		t.Fatal("expired terminal pruning dropped callback cleanup", op, err)
	}
}

func TestNativeObservedDeniedRetirementRejectsUnclaimedAudit(t *testing.T) {
	f := newCallbackSQLFixture(t)
	ctx := context.Background()
	if err := f.repo.BindObservedReply(ctx, f.child, f.reply, f.callback, f.witness, f.now); err != nil {
		t.Fatal(err)
	}
	a, err := f.repo.RequestApproval(ctx, f.child, "78787878787878787878787878787878", f.now, f.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.DecideApproval(ctx, nativetool.DecisionInput{ApprovalID: a.ID, ExpectedRevision: a.Revision, Decision: nativetool.DecisionDeny, IdempotencyKey: "denied-unclaimed-audit", ObservedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	err = f.store.nativeTools.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		op, err := loadNativeOperation(ctx, tx, f.child)
		if err != nil {
			return err
		}
		b, err := loadNativeBinding(ctx, tx, f.child)
		if err != nil {
			return err
		}
		environment, _, err := loadEnvironmentRevision(ctx, tx, op.EnvironmentID, op.EnvironmentRevision)
		if err != nil {
			return err
		}
		audit, err := nativeAuditAttempt(ctx, tx, op, b, environment, "unclaimed-callback-audit", f.now)
		if err != nil {
			return err
		}
		_, err = appendEgressAttempt(ctx, tx, audit)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer f.store.database.Exec(`DELETE FROM runtime_egress_attempts WHERE attempt_id='unclaimed-callback-audit'`)
	if err := f.repo.RetireObservedCallback(ctx, f.callback, f.now); err == nil {
		t.Fatal("terminal child concealed an unclaimed physical audit")
	}
	f.store.nativeTools.issuerMu.Lock()
	c := f.store.nativeTools.callbacks[f.callback]
	kept := c != nil && c.retired
	f.store.nativeTools.issuerMu.Unlock()
	if !kept {
		t.Fatal("failed callback retirement lost denying cleanup fence")
	}
}
