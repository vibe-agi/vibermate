package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// A recoverable socket failure must close the real physical audit without
// declaring the logical tools/call failed or allowing its approval to be reused.
func TestNativeStreamSeparatesAttemptAndRPCCompletion(t *testing.T) {
	store, id, revision := nativeStoreFixture(t, environment.ToolPolicyReview)
	repo := nativeFullRepository(t, store)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeAllow(t, repo, id, now)
	receipt, err := repo.Start(ctx, id, "stream-origin", revision, now)
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := nativeDecisionFixture(t, store, id)
	stream, err := repo.OpenObservedStream(ctx, receipt, binding, 30*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishAttempt(ctx, receipt, nativetool.PhysicalFinishInput{Outcome: "failed", Reason: "transport_failed", Counts: &nativetool.ObservedCounts{BytesIn: 17, BytesOut: 31}, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	op, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != "started" {
		t.Fatalf("physical disconnect completed logical RPC: state=%s; want started", op.State)
	}
	before, err := scanEgressAttempt(store.database.QueryRow(egressAttemptSelect + ` WHERE attempt_id='stream-origin'`))
	if err != nil {
		t.Fatal(err)
	}
	if !before.Attempt.Terminal() || before.Attempt.Outcome() != "failed" || before.Attempt.ErrorClass() != "transport_failed" {
		t.Fatalf("physical audit: %+v", before)
	}
	if err := repo.CompleteStreamRPC(ctx, stream, nativetool.LogicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
		t.Fatalf("late logical completion: %v", err)
	}
	after, err := scanEgressAttempt(store.database.QueryRow(egressAttemptSelect + ` WHERE attempt_id='stream-origin'`))
	if err != nil {
		t.Fatal(err)
	}
	op, err = repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if before.Attempt != after.Attempt || op.State != "completed" || op.BytesIn == nil || *op.BytesIn != 17 || op.BytesOut == nil || *op.BytesOut != 31 {
		t.Fatalf("logical completion rewrote physical evidence: %+v", op)
	}
}

func TestNativeStreamExpiryNeverRenews(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		configured, ceiling, delay time.Duration
	}{
		{"short-executor", 100 * time.Millisecond, 30 * time.Minute, 20 * time.Millisecond},
		{"strict-store", 30 * time.Minute, 100 * time.Millisecond, 20 * time.Millisecond},
		{"delayed-open", 100 * time.Millisecond, 30 * time.Minute, 100 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, id, revision := nativeStoreFixture(t, environment.ToolPolicyReview)
			repo := nativeFullRepository(t, store)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			store.nativeTools.streamLifetime = tc.ceiling
			binding, _ := nativeDecisionFixture(t, store, id)
			nativeAllow(t, repo, id, now)
			receipt, err := repo.Start(ctx, id, "expiry-origin", revision, now)
			if err != nil {
				t.Fatal(err)
			}
			a, err := repo.OpenObservedStream(ctx, receipt, binding, tc.configured, now.Add(tc.delay))
			if tc.delay == 100*time.Millisecond {
				if err == nil {
					t.Fatal("late Open renewed claim deadline")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repo.OpenObservedStream(ctx, receipt, binding, 30*time.Minute, now.Add(40*time.Millisecond)); err == nil {
				t.Fatal("duplicate Open renewed authority")
			}
			if !a.ExpiresAt().Equal(now.Add(100 * time.Millisecond)) {
				t.Fatalf("authority metadata lost exact clamp: %v", a.ExpiresAt())
			}
			if err := repo.FinishAttempt(ctx, receipt, nativetool.PhysicalFinishInput{Outcome: "failed", Reason: "transport_failed", ObservedAt: now.Add(50 * time.Millisecond)}); err != nil {
				t.Fatal(err)
			}
			if err := repo.CompleteStreamRPC(ctx, a, nativetool.LogicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: now.Add(100 * time.Millisecond)}); err == nil {
				t.Fatal("authority survived absolute deadline")
			}
		})
	}
}

func TestNativeStreamTerminalBeforeSocketFailure(t *testing.T) {
	store, id, revision := nativeStoreFixture(t, environment.ToolPolicyReview)
	repo := nativeFullRepository(t, store)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	binding, _ := nativeDecisionFixture(t, store, id)
	nativeAllow(t, repo, id, now)
	receipt, err := repo.Start(ctx, id, "ordered-origin", revision, now)
	if err != nil {
		t.Fatal(err)
	}
	a, err := repo.OpenObservedStream(ctx, receipt, binding, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteStreamRPC(ctx, a, nativetool.LogicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishAttempt(ctx, receipt, nativetool.PhysicalFinishInput{Outcome: "failed", Reason: "transport_failed", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	op, err := repo.Get(ctx, id)
	if err != nil || op.State != "completed" || op.BytesIn != nil || op.BytesOut != nil {
		t.Fatalf("known outcome lost: %+v %v", op, err)
	}
	if err := repo.ValidateLocalReplay(ctx, a, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Finish(ctx, id, nativetool.TerminalInput{Outcome: "uncertain", Reason: "execution_uncertain", ObservedAt: now}); !errors.Is(err, nativetool.ErrConflict) {
		t.Fatalf("late uncertainty: %v", err)
	}
}

func TestNativeStreamSuspendedCleanupPreservesPhysicalAudit(t *testing.T) {
	for _, action := range []string{"recover", "revoke", "off", "session", "retire", "reopen"} {
		for _, known := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/known=%v", action, known), func(t *testing.T) {
				store, id, revision := nativeStoreFixture(t, environment.ToolPolicyReview)
				repo := nativeFullRepository(t, store)
				ctx := context.Background()
				now := time.Now().UTC().Truncate(time.Millisecond)
				b, _ := nativeDecisionFixture(t, store, id)
				nativeAllow(t, repo, id, now)
				receipt, err := repo.Start(ctx, id, "cleanup-origin", revision, now)
				if err != nil {
					t.Fatal(err)
				}
				a, err := repo.OpenObservedStream(ctx, receipt, b, time.Minute, now)
				if err != nil {
					t.Fatal(err)
				}
				var counts *nativetool.ObservedCounts
				if known {
					counts = &nativetool.ObservedCounts{BytesIn: 5, BytesOut: 7}
				}
				if err := repo.FinishAttempt(ctx, receipt, nativetool.PhysicalFinishInput{Outcome: "failed", Reason: "transport_failed", Counts: counts, ObservedAt: now}); err != nil {
					t.Fatal(err)
				}
				before, err := scanEgressAttempt(store.database.QueryRow(egressAttemptSelect + ` WHERE attempt_id='cleanup-origin'`))
				if err != nil {
					t.Fatal(err)
				}
				switch action {
				case "recover":
					err = repo.Recover(ctx, now)
				case "revoke":
					err = repo.Revoke(ctx, b.Capture(), revision, "revoked", now)
				case "off":
					nativePublishOff(t, store, id, true)
				case "session":
					_, err = repo.CloseSession(ctx, b.SessionID(), b.SessionRevision(), now)
				case "retire":
					_, err = repo.RetireObservedStream(ctx, a, now)
				case "reopen":
					shutdownTestStore(t, store)
					store = openTestStore(t, store.databasePath)
					defer shutdownTestStore(t, store)
					repo = nativeFullRepository(t, store)
				}
				if err != nil {
					t.Fatal(err)
				}
				after, err := scanEgressAttempt(store.database.QueryRow(egressAttemptSelect + ` WHERE attempt_id='cleanup-origin'`))
				if err != nil {
					t.Fatal(err)
				}
				op, err := repo.Get(ctx, id)
				if err != nil || op.State != "uncertain" || before.Attempt != after.Attempt {
					t.Fatalf("cleanup rewrote evidence: %+v %v", op, err)
				}
				if known && (op.BytesIn == nil || *op.BytesIn != 5 || op.BytesOut == nil || *op.BytesOut != 7) || !known && (op.BytesIn != nil || op.BytesOut != nil) {
					t.Fatalf("count projection %+v", op)
				}
				if repo.CompleteStreamRPC(ctx, a, nativetool.LogicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: now}) == nil {
					t.Fatal("retired authority accepted")
				}
				scope, err := repo.GetScope(ctx, b.Capture())
				if err != nil || !scope.ReplayBarrier {
					t.Fatalf("missing uncertainty barrier: %+v %v", scope, err)
				}
			})
		}
	}
}

func TestNativeStreamRejectsForeignAndAmbiguousAuthority(t *testing.T) {
	for _, fault := range []string{"foreign", "ambiguous-open", "ambiguous-finish", "corrupt-audit"} {
		t.Run(fault, func(t *testing.T) {
			store, id, revision := nativeStoreFixture(t, environment.ToolPolicyReview)
			repo := nativeFullRepository(t, store)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			b, _ := nativeDecisionFixture(t, store, id)
			nativeAllow(t, repo, id, now)
			receipt, err := repo.Start(ctx, id, "fault-origin", revision, now)
			if err != nil {
				t.Fatal(err)
			}
			if fault == "foreign" {
				issuer, _ := nativetool.NewStartReceiptIssuer()
				forged, _ := issuer.Issue(nativetool.StartFacts{OperationID: id, AttemptID: receipt.AttemptID(), AuthorityRevision: revision, OwnerEpoch: b.OwnerEpoch(), BindingDigest: b.Digest()})
				if _, err := repo.OpenObservedStream(ctx, forged, b, time.Minute, now); err == nil {
					t.Fatal("foreign receipt accepted")
				}
				return
			}
			if fault == "ambiguous-open" {
				store.nativeTools.committer = commitThenError{}
				_, err := repo.OpenObservedStream(ctx, receipt, b, time.Minute, now)
				store.nativeTools.committer = sqlTransactionCommitter{}
				if !errors.Is(err, nativetool.ErrUnavailable) {
					t.Fatalf("fault: %v", err)
				}
				if _, err := repo.OpenObservedStream(ctx, receipt, b, time.Minute, now); err == nil {
					t.Fatal("ambiguous Open reissued authority")
				}
				return
			}
			a, err := repo.OpenObservedStream(ctx, receipt, b, time.Minute, now)
			if err != nil {
				t.Fatal(err)
			}
			if fault == "ambiguous-finish" {
				store.nativeTools.committer = commitThenError{}
				err = repo.FinishAttempt(ctx, receipt, nativetool.PhysicalFinishInput{Outcome: "failed", Reason: "transport_failed", ObservedAt: now})
				store.nativeTools.committer = sqlTransactionCommitter{}
				if !errors.Is(err, nativetool.ErrUnavailable) {
					t.Fatalf("fault: %v", err)
				}
			} else {
				var original string
				if err := store.database.QueryRow(`SELECT connection_id FROM runtime_egress_attempts WHERE attempt_id='fault-origin'`).Scan(&original); err != nil {
					t.Fatal(err)
				}
				_, err = store.database.Exec(`UPDATE runtime_egress_attempts SET connection_id='foreign' WHERE attempt_id='fault-origin'`)
				if err != nil {
					t.Fatal(err)
				}
				defer store.database.Exec(`UPDATE runtime_egress_attempts SET connection_id=? WHERE attempt_id='fault-origin'`, original)
			}
			if repo.CompleteStreamRPC(ctx, a, nativetool.LogicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: now}) == nil {
				t.Fatal("invalid observation authority accepted")
			}
		})
	}
}

func TestNativeStreamCapacityReusesCompletedRegistrations(t *testing.T) {
	store, first, revision := nativeStoreFixture(t, environment.ToolPolicyObserve)
	repo := nativeFullRepository(t, store)
	ctx := context.Background()
	base, _ := nativeDecisionFixture(t, store, first)
	for i := 0; i < 1025; i++ {
		// Capacity reuse must not impose one shared expiry on the whole loop.
		now := time.Now().UTC().Truncate(time.Millisecond)
		id := nativetool.ID(fmt.Sprintf("%032x", i+1000))
		b := nativeBindCall(t, repo, base, id, i+1000, nil, now)
		receipt, err := repo.Start(ctx, id, fmt.Sprintf("capacity-attempt-%d", i), revision, now)
		if err != nil {
			t.Fatalf("iteration %d operation %s age %s Start: %v", i, id, time.Since(now), err)
		}
		a, err := repo.OpenObservedStream(ctx, receipt, b, time.Minute, now)
		if err != nil {
			t.Fatalf("iteration %d operation %s age %s OpenObservedStream: %v", i, id, time.Since(now), err)
		}
		if err := repo.CompleteStreamRPC(ctx, a, nativetool.LogicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
			t.Fatalf("iteration %d operation %s age %s CompleteStreamRPC: %v", i, id, time.Since(now), err)
		}
		if err := repo.FinishAttempt(ctx, receipt, nativetool.PhysicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
			t.Fatalf("iteration %d operation %s age %s FinishAttempt: %v", i, id, time.Since(now), err)
		}
		if _, err := repo.RetireObservedStream(ctx, a, now); err != nil {
			t.Fatalf("iteration %d operation %s age %s RetireObservedStream: %v", i, id, time.Since(now), err)
		}
	}
}

func TestNativeStreamActiveLimitDoesNotCountTerminalReplay(t *testing.T) {
	store, first, revision := nativeStoreFixture(t, environment.ToolPolicyObserve)
	repo := nativeFullRepository(t, store)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	base, _ := nativeDecisionFixture(t, store, first)
	var oldest nativetool.StreamAuthority
	for i := 0; i < 17; i++ {
		id := nativetool.ID(fmt.Sprintf("%032x", i+2000))
		b := nativeBindCall(t, repo, base, id, i+2000, nil, now)
		receipt, err := repo.Start(ctx, id, fmt.Sprintf("active-attempt-%d", i), revision, now)
		if err != nil {
			t.Fatal(err)
		}
		if i == 16 {
			if _, err := repo.OpenObservedStream(ctx, receipt, b, time.Minute, now); err == nil {
				t.Fatal("active limit exceeded")
			}
			if err := repo.CompleteStreamRPC(ctx, oldest, nativetool.LogicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
				t.Fatal(err)
			}
		}
		a, err := repo.OpenObservedStream(ctx, receipt, b, time.Minute, now)
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if i == 0 {
			oldest = a
		}
	}
}

func TestNativeStreamStoreCeilingRetiresDetachedRPC(t *testing.T) {
	store, id, revision := nativeStoreFixture(t, environment.ToolPolicyObserve)
	repo := nativeFullRepository(t, store)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store.nativeTools.streamLifetime = 30 * time.Millisecond
	b, _ := nativeDecisionFixture(t, store, id)
	receipt, err := repo.Start(ctx, id, "ceiling-expiry", revision, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.OpenObservedStream(ctx, receipt, b, 30*time.Minute, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishAttempt(ctx, receipt, nativetool.PhysicalFinishInput{Outcome: "failed", Reason: "transport_failed", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(300 * time.Millisecond)
	for {
		op, err := repo.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if op.State == "uncertain" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Store expiry did not retire detached RPC: %s", op.State)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestNativeRecoveryRechecksStreamRetirementAtStartAndVerify(t *testing.T) {
	for _, stage := range []string{"start", "verify", "race"} {
		t.Run(stage, func(t *testing.T) {
			store, id, revision := nativeStoreFixture(t, environment.ToolPolicyObserve)
			repo := nativeFullRepository(t, store)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			b, _ := nativeDecisionFixture(t, store, id)
			receipt, err := repo.Start(ctx, id, "recheck-origin", revision, now)
			if err != nil {
				t.Fatal(err)
			}
			a, err := repo.OpenObservedStream(ctx, receipt, b, time.Minute, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.FinishAttempt(ctx, receipt, nativetool.PhysicalFinishInput{Outcome: "failed", Reason: "transport_failed", ObservedAt: now}); err != nil {
				t.Fatal(err)
			}
			catalog, _ := b.CatalogDigest()
			get, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestGetStream, Plan: b.Plan(), Capture: b.Capture(), AssignmentRevision: b.AssignmentRevision(), OwnerEpoch: b.OwnerEpoch(), SessionID: b.SessionID(), SessionRevision: b.SessionRevision(), CatalogGeneration: b.CatalogGeneration(), CatalogDigest: &catalog})
			if err != nil {
				t.Fatal(err)
			}
			child := nativetool.ID("76767676767676767676767676767676")
			plan, sid := get.Plan(), get.SessionID()
			if _, err := repo.Receive(ctx, nativetool.ReceiveInput{ID: child, Capture: get.Capture(), EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "recheck-get", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "GET", Kind: get.Kind(), SessionID: &sid, Recording: plan.ContentRecording(), ObservedAt: now}); err != nil {
				t.Fatal(err)
			}
			if err := repo.BindRecoveryGET(ctx, child, get, a, now); err != nil {
				t.Fatal(err)
			}
			var getReceipt nativetool.StartReceipt
			if stage == "verify" {
				getReceipt, err = repo.Start(ctx, child, "recheck-child", revision, now)
				if err != nil {
					t.Fatal(err)
				}
			}
			if stage == "race" {
				ready := make(chan struct{})
				retired := make(chan error, 1)
				started := make(chan nativetool.StartReceipt, 1)
				go func() { <-ready; _, err := repo.RetireObservedStream(ctx, a, now); retired <- err }()
				go func() {
					<-ready
					receipt, _ := repo.Start(ctx, child, "recheck-child", revision, now)
					started <- receipt
				}()
				close(ready)
				if err := <-retired; err != nil {
					t.Fatal(err)
				}
				getReceipt = <-started
			} else if _, err := repo.RetireObservedStream(ctx, a, now); err != nil {
				t.Fatal(err)
			}
			if stage == "start" {
				if _, err := repo.Start(ctx, child, "recheck-child", revision, now); err == nil {
					t.Fatal("retired stream granted GET Start")
				}
			} else if repo.VerifyStartReceipt(ctx, getReceipt, now) == nil {
				t.Fatal("retired stream granted GET Verify")
			}
			scope, err := repo.GetScope(ctx, b.Capture())
			if err != nil || scope.State != "active" {
				t.Fatalf("test masked association check with scope revocation: %+v %v", scope, err)
			}
		})
	}
}

func TestNativeStreamRetirementClosesExistingReplyQuestion(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			store, id, revision := nativeStoreFixture(t, environment.ToolPolicyReview)
			repo := nativeFullRepository(t, store)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			b, _ := nativeDecisionFixture(t, store, id)
			nativeAllow(t, repo, id, now)
			receipt, err := repo.Start(ctx, id, "question-origin", revision, now)
			if err != nil {
				t.Fatal(err)
			}
			a, err := repo.OpenObservedStream(ctx, receipt, b, time.Minute, now)
			if err != nil {
				t.Fatal(err)
			}
			catalog, _ := b.CatalogDigest()
			request := []byte(`{"jsonrpc":"2.0","id":"server","method":"ping"}`)
			digest := nativetool.Digest(sha256.Sum256(request))
			child := nativetool.ID("67676767676767676767676767676767")
			reply, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestServerReply, Plan: b.Plan(), Capture: b.Capture(), AssignmentRevision: b.AssignmentRevision(), OwnerEpoch: b.OwnerEpoch(), SessionID: b.SessionID(), SessionRevision: b.SessionRevision(), CatalogGeneration: b.CatalogGeneration(), CatalogDigest: &catalog, ParentOperationID: &id, ServerRequestDigest: &digest, ExactRPC: []byte(`{"jsonrpc":"2.0","id":"server","result":{}}`)})
			if err != nil {
				t.Fatal(err)
			}
			if err := nativeBindObservedPing(t, repo, child, reply, b, a, request, []byte(`{"jsonrpc":"2.0","id":"server","result":{}}`), now); err != nil {
				t.Fatal(err)
			}
			approval, err := repo.RequestApproval(ctx, child, nativetool.ID("68686868686868686868686868686868"), now, now.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if allowed {
				_, err = repo.DecideApproval(ctx, nativetool.DecisionInput{ApprovalID: approval.ID, ExpectedRevision: approval.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "reply-allow-before-detach", ObservedAt: now})
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := repo.FinishAttempt(ctx, receipt, nativetool.PhysicalFinishInput{Outcome: "failed", Reason: "transport_failed", ObservedAt: now}); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.RetireObservedStream(ctx, a, now); err != nil {
				t.Fatal(err)
			}
			closed, err := repo.GetApproval(ctx, approval.ID)
			if err != nil || closed.State != "canceled" || closed.ConsumedAt != nil {
				t.Fatalf("retired stream left actionable question: %+v %v", closed, err)
			}
		})
	}
}

// Recovery must never obtain a second send of the original operation: its
// physical receipt becomes unusable while its logical RPC remains unresolved.
func TestNativeStreamRecoveryCannotResendOriginal(t *testing.T) {
	store, id, revision := nativeStoreFixture(t, environment.ToolPolicyReview)
	repo := nativeFullRepository(t, store)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeAllow(t, repo, id, now)
	receipt, err := repo.Start(ctx, id, "stream-origin", revision, now)
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := nativeDecisionFixture(t, store, id)
	stream, err := repo.OpenObservedStream(ctx, receipt, binding, 30*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishAttempt(ctx, receipt, nativetool.PhysicalFinishInput{Outcome: "failed", Reason: "transport_failed", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if repo.VerifyStartReceipt(ctx, receipt, now) == nil {
		t.Fatal("closed physical receipt still authorizes send")
	}
	if _, err := repo.Start(ctx, id, "forbidden-resend", revision, now); err == nil {
		t.Fatal("original operation started twice")
	}
	var once, consumed int
	if err := store.database.QueryRow(`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, id).Scan(&once); err != nil {
		t.Fatal(err)
	}
	if err := store.database.QueryRow(`SELECT COUNT(*) FROM native_tool_approvals WHERE operation_id=? AND consumed_at_unix_ms IS NOT NULL`, id).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if once != 1 || consumed != 1 {
		t.Fatalf("once=%d consumed=%d", once, consumed)
	}
	op, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != "started" {
		t.Fatalf("recovery lost original logical RPC: state=%s; want started", op.State)
	}
	child := nativetool.ID("98989898989898989898989898989898")
	catalog, _ := binding.CatalogDigest()
	get, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestGetStream, Plan: binding.Plan(), Capture: binding.Capture(), AssignmentRevision: binding.AssignmentRevision(), OwnerEpoch: binding.OwnerEpoch(), SessionID: binding.SessionID(), SessionRevision: binding.SessionRevision(), CatalogGeneration: binding.CatalogGeneration(), CatalogDigest: &catalog})
	if err != nil {
		t.Fatal(err)
	}
	plan, sid := get.Plan(), get.SessionID()
	_, err = repo.Receive(ctx, nativetool.ReceiveInput{ID: child, Capture: get.Capture(), EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "recovery-fixture", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "GET", Kind: get.Kind(), SessionID: &sid, Recording: plan.ContentRecording(), ObservedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.BindRecoveryGET(ctx, child, get, stream, now); err != nil {
		t.Fatal(err)
	}
	getReceipt, err := repo.Start(ctx, child, "stream-recovery-get", revision, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.VerifyStartReceipt(ctx, getReceipt, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishAttempt(ctx, getReceipt, nativetool.PhysicalFinishInput{Outcome: "completed", Reason: "completed", Counts: &nativetool.ObservedCounts{BytesIn: 103}, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteStreamRPC(ctx, stream, nativetool.LogicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	op, err = repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != "completed" || op.BytesIn != nil || op.BytesOut != nil {
		t.Fatalf("recovery replaced original NULL counts: %+v", op)
	}
	getOp, err := repo.Get(ctx, child)
	if err != nil || getOp.State != "completed" || getOp.BytesIn == nil || *getOp.BytesIn != 103 {
		t.Fatalf("GET evidence %+v %v", getOp, err)
	}
	if repo.VerifyStartReceipt(ctx, getReceipt, now) == nil {
		t.Fatal("completed GET receipt authorizes resend")
	}
}
