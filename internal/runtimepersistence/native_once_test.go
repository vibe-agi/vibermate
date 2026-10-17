package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func nativeStoreFixture(t *testing.T, mode environment.ToolPolicyMode) (*Store, nativetool.ID, uint64) {
	t.Helper()
	return nativeLifecycleFixture(t, mode)
}

func nativePublishOff(t *testing.T, store *Store, id nativetool.ID, publish bool) {
	t.Helper()
	ctx := context.Background()
	op, err := store.NativeLifecycleRepository().Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	manager := newEnvironmentManager(t, store)
	snapshot, err := manager.Resolve(op.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	candidate := snapshot.Aggregate()
	candidate.Revision = snapshot.Revision() + 1
	candidate.ClientToolAccess = nil
	draft, err := manager.SaveDraft(ctx, environment.DraftCommand{ExpectedBaseRevision: snapshot.Revision(), Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if !publish {
		return
	}
	preview, err := manager.Preview(ctx, candidate.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(ctx, preview); err != nil {
		t.Fatal(err)
	}
}

// Catches stale durable approvals/sessions left by authoritative mutations.
func TestNativeAuthoritativeHooksInvalidateReceipts(t *testing.T) {
	for _, action := range []string{"draft", "off", "assignment", "manual-rotate", "manual-revoke", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			store, id, rev := nativeStoreFixture(t, environment.ToolPolicyReview)
			repo := nativeFullRepository(t, store)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			nativeAllow(t, repo, id, now)
			receipt, err := repo.Start(ctx, id, "hook-start", rev, now)
			if err != nil {
				t.Fatal(err)
			}
			op, err := repo.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			manualID, parseErr := manualcapture.ParseID(op.Capture.ID)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			switch action {
			case "draft":
				nativePublishOff(t, store, id, false)
			case "off":
				nativePublishOff(t, store, id, true)
			case "assignment":
				a, exists, err := store.CaptureAssignmentRepository().Load(ctx, op.Capture)
				if err != nil || !exists {
					t.Fatal(err)
				}
				a.Revision++
				a.UpdatedAt = now
				if result, err := store.CaptureAssignmentRepository().Write(ctx, 1, a); err != nil || result.Outcome != captureassignment.CommitOutcomeCommitted {
					t.Fatalf("write: %+v %v", result, err)
				}
			case "manual-rotate":
				_, err = store.ManualCaptureRepository().Rotate(ctx, manualcapture.NewLocalOwnerScope(), manualID, 1, manualcapture.CredentialDigest{9}, now)
			case "manual-revoke":
				_, err = store.ManualCaptureRepository().Revoke(ctx, manualcapture.NewLocalOwnerScope(), manualID, 1, now)
			case "shutdown":
				err = store.Shutdown(ctx)
				store = openTestStore(t, store.databasePath)
				defer shutdownTestStore(t, store)
				repo = nativeFullRepository(t, store)
			}
			if err != nil {
				t.Fatal(err)
			}
			if action == "draft" {
				if err := repo.VerifyStartReceipt(ctx, receipt, now); err != nil {
					t.Fatal(err)
				}
				return
			}
			if repo.VerifyStartReceipt(ctx, receipt, now) == nil {
				t.Fatal("old receipt accepted")
			}
			var state string
			var revision uint64
			var barrier bool
			if err := store.database.QueryRow(`SELECT state,revision,replay_barrier FROM native_tool_scopes`).Scan(&state, &revision, &barrier); err != nil || state != "revoked" || revision <= rev || !barrier {
				t.Fatalf("durable hook: %s %d %v %v", state, revision, barrier, err)
			}
			current, err := repo.Get(ctx, id)
			if err != nil || current.State != "uncertain" || current.BytesOut != nil {
				t.Fatalf("unknown terminal: %+v %v", current, err)
			}
		})
	}
}

func TestNativeExecutionHandshakeWithoutCatalog(t *testing.T) {
	store, _, _ := nativeExecutionHandshakeFixture(t)
	ctx := context.Background()
	var count int
	if err := store.database.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_operations WHERE rpc_kind IN('initialize','initialized','tools_list') AND state='completed'`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("actual handshake: %d %v", count, err)
	}
	if got := countRows(t, store, "native_tool_approvals"); got != 0 {
		t.Fatalf("handshake approvals: %d", got)
	}
	if got := countRows(t, store, "native_tool_deliveries"); got != 3 {
		t.Fatalf("actual handshake audits: %d", got)
	}
}

func TestNativeReceiptRejectsOtherIssuerStoreAndNoncommittedRows(t *testing.T) {
	for _, fault := range []struct {
		name      string
		committer transactionCommitter
		committed bool
	}{{"commitThenError", commitThenError{}, true}, {"rollbackThenError", rollbackThenError{}, false}} {
		t.Run(fault.name, func(t *testing.T) {
			store, id, rev := nativeStoreFixture(t, environment.ToolPolicyReview)
			repo := nativeFullRepository(t, store)
			now := time.Now().UTC().Truncate(time.Millisecond)
			nativeAllow(t, repo, id, now)
			store.nativeTools.committer = fault.committer
			receipt, err := repo.Start(context.Background(), id, "fault-start", rev, now)
			if !errors.Is(err, nativetool.ErrUnavailable) || receipt.OperationID() != "" {
				t.Fatalf("ambiguous start authority: %+v %v", receipt, err)
			}
			store.nativeTools.committer = sqlTransactionCommitter{}
			var count int
			if err := store.database.QueryRow(`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, id).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 0
			if fault.committed {
				want = 1
			}
			if count != want {
				t.Fatalf("once count: %d want %d", count, want)
			}
			if fault.committed {
				if r, err := repo.Start(context.Background(), id, "delayed-resend", rev, now); err == nil || r.OperationID() != "" {
					t.Fatal("delayed receipt issued")
				}
			}
		})
	}
}

func TestNativeReceiptRevokedFinishedRecoveredAndReopened(t *testing.T) {
	for _, action := range []string{"finished", "revoked", "recovered", "foreign", "corrupt-audit", "expired"} {
		t.Run(action, func(t *testing.T) {
			store, id, rev := nativeStoreFixture(t, environment.ToolPolicyReview)
			repo := nativeFullRepository(t, store)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			nativeAllow(t, repo, id, now)
			receipt, err := repo.Start(ctx, id, "receipt-start", rev, now)
			if err != nil {
				t.Fatal(err)
			}
			op, err := repo.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "finished":
				err = repo.Finish(ctx, id, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", BytesIn: 3, BytesOut: 4, ObservedAt: now})
			case "revoked":
				err = repo.Revoke(ctx, op.Capture, rev, "revoked", now)
			case "recovered":
				err = repo.Recover(ctx, now)
				got, _ := repo.Get(ctx, id)
				if got.State != "uncertain" || got.BytesIn != nil || got.BytesOut != nil {
					t.Fatalf("recovery unknown bytes: %+v", got)
				}
				var known bool
				if err := store.database.QueryRow(`SELECT byte_counts_known FROM native_tool_deliveries WHERE attempt_id='receipt-start'`).Scan(&known); err != nil || known {
					t.Fatalf("audit known: %v %v", known, err)
				}
			case "foreign":
				foreign, err := nativetool.NewStartReceiptIssuer()
				if err != nil {
					t.Fatal(err)
				}
				fake, err := foreign.Issue(nativetool.StartFacts{OperationID: id, AttemptID: receipt.AttemptID(), AuthorityRevision: rev, OwnerEpoch: 1, BindingDigest: *op.BindingDigest})
				if err != nil {
					t.Fatal(err)
				}
				if repo.VerifyStartReceipt(ctx, fake, now) == nil {
					t.Fatal("foreign issuer accepted")
				}
				shutdownTestStore(t, store)
				other := openTestStore(t, store.databasePath)
				defer shutdownTestStore(t, other)
				repo = nativeFullRepository(t, other)
			case "corrupt-audit":
				original, e := scanEgressAttempt(store.database.QueryRow(nativeDeliverySelect + ` WHERE attempt_id='receipt-start'`))
				if e != nil {
					t.Fatal(e)
				}
				defer appendNativeDelivery(ctx, store.database, original.Attempt)
				_, err = store.database.Exec(`DELETE FROM native_tool_deliveries WHERE attempt_id='receipt-start'`)
			case "expired":
				now = now.Add(2 * time.Hour)
			}
			if err != nil {
				t.Fatal(err)
			}
			if repo.VerifyStartReceipt(ctx, receipt, now) == nil {
				t.Fatal("invalidated receipt accepted")
			}
			if action == "finished" {
				if err := repo.Finish(ctx, id, nativetool.TerminalInput{Outcome: "uncertain", Reason: "execution_uncertain", ObservedAt: now}); !errors.Is(err, nativetool.ErrConflict) {
					t.Fatalf("late terminal: %v", err)
				}
				var out sql.NullInt64
				store.database.QueryRow(`SELECT bytes_out FROM native_tool_operations WHERE operation_id=?`, id).Scan(&out)
				if !out.Valid || out.Int64 != 4 {
					t.Fatal("terminal overwritten")
				}
			}
		})
	}
}

func nativeFullRepository(t *testing.T, store *Store) nativetool.Repository {
	t.Helper()
	repo, ok := store.NativeLifecycleRepository().(nativetool.Repository)
	if !ok {
		t.Fatal("native adapter lacks durable execution methods")
	}
	return repo
}

func nativeAllow(t *testing.T, repo nativetool.Repository, id nativetool.ID, now time.Time) {
	t.Helper()
	digest := sha256.Sum256([]byte("test-approval/" + string(id)))
	a, err := repo.RequestApproval(context.Background(), id, nativetool.ID(fmt.Sprintf("%x", digest[:16])), now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.DecideApproval(context.Background(), nativetool.DecisionInput{ApprovalID: a.ID, ExpectedRevision: a.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "native-allow-once-" + string(id), ObservedAt: now})
	if err != nil {
		t.Fatal(err)
	}
}

func nativeBindCall(t *testing.T, repo nativetool.Repository, base nativetool.Binding, id nativetool.ID, rpc int, observed []byte, now time.Time) nativetool.Binding {
	t.Helper()
	plan := base.Plan()
	catalog, _ := base.CatalogDigest()
	args, _ := protocolcore.NewJSONObject([]byte(`{"path":"a"}`), 1024)
	key, _ := protocolcore.NewCallKey("native", fmt.Sprint(rpc))
	b, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestToolsCall, Plan: plan, Capture: base.Capture(), AssignmentRevision: base.AssignmentRevision(), OwnerEpoch: base.OwnerEpoch(), SessionID: base.SessionID(), SessionRevision: base.SessionRevision(), CatalogGeneration: base.CatalogGeneration(), CatalogDigest: &catalog, ObservedCallID: observed, ExactRPC: []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, rpc)), Intents: []protocolcore.ToolIntent{{ResponseID: fmt.Sprintf("rpc-%d", rpc), Call: protocolcore.ToolCall{Key: key, Name: "read_file", Arguments: args}}}})
	if err != nil {
		t.Fatal(err)
	}
	nativeReceiveBind(t, repo, id, b, now)
	return b
}

func nativeReceiveBind(t *testing.T, repo nativetool.Repository, id nativetool.ID, b nativetool.Binding, now time.Time) {
	nativeReceiveOnly(t, repo, id, b, now)
	if err := repo.Bind(context.Background(), id, b, now); err != nil {
		t.Fatal(err)
	}
}
func nativeReceiveOnly(t *testing.T, repo nativetool.Repository, id nativetool.ID, b nativetool.Binding, now time.Time) {
	t.Helper()
	plan := b.Plan()
	sid := b.SessionID()
	rpc, has := b.RPCIDDigest()
	digest := b.RequestDigest()
	var rpcPtr, requestPtr *nativetool.Digest
	if has {
		rpcPtr = &rpc
		requestPtr = &digest
	}
	if result, err := repo.Receive(context.Background(), nativetool.ReceiveInput{ID: id, Capture: b.Capture(), EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "actual-native-fixture", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "POST", Kind: b.Kind(), SessionID: &sid, RPCIDDigest: rpcPtr, RequestDigest: requestPtr, Recording: plan.ContentRecording(), ObservedAt: now}); err != nil || result.Duplicate {
		t.Fatalf("receive: %+v %v", result, err)
	}
}

func TestNativeRevokeRacesStartFailClosed(t *testing.T) {
	for _, action := range []string{"revoke", "off"} {
		for iteration := 0; iteration < 4; iteration++ {
			t.Run(fmt.Sprintf("%s-%d", action, iteration), func(t *testing.T) {
				store, id, rev := nativeStoreFixture(t, environment.ToolPolicyReview)
				repo := nativeFullRepository(t, store)
				ctx := context.Background()
				now := time.Now().UTC().Truncate(time.Millisecond)
				nativeAllow(t, repo, id, now)
				op, _ := repo.Get(ctx, id)
				mutate := func() error { return repo.Revoke(ctx, op.Capture, rev, "revoked", now) }
				if action == "off" {
					manager := newEnvironmentManager(t, store)
					snapshot, err := manager.Resolve(op.EnvironmentID)
					if err != nil {
						t.Fatal(err)
					}
					candidate := snapshot.Aggregate()
					candidate.Revision++
					candidate.ClientToolAccess = nil
					draft, err := manager.SaveDraft(ctx, environment.DraftCommand{ExpectedBaseRevision: snapshot.Revision(), Candidate: candidate})
					if err != nil {
						t.Fatal(err)
					}
					preview, err := manager.Preview(ctx, candidate.ID, draft.Revision)
					if err != nil {
						t.Fatal(err)
					}
					mutate = func() error { _, err := manager.Publish(ctx, preview); return err }
				}
				barrier := make(chan struct{})
				done := make(chan error, 1)
				started := make(chan struct {
					receipt nativetool.StartReceipt
					err     error
				}, 1)
				go func() {
					<-barrier
					r, err := repo.Start(ctx, id, "race-start", rev, now)
					started <- struct {
						receipt nativetool.StartReceipt
						err     error
					}{r, err}
				}()
				go func() { <-barrier; done <- mutate() }()
				close(barrier)
				result := <-started
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if result.err != nil && result.receipt.OperationID() != "" {
					t.Fatal("failed start issued receipt")
				}
				if repo.VerifyStartReceipt(ctx, result.receipt, time.Now().UTC()) == nil {
					t.Fatal("post-revocation receipt accepted")
				}
				var once int
				if err := store.database.QueryRow(`SELECT COUNT(*) FROM native_tool_once`).Scan(&once); err != nil || once > 1 {
					t.Fatalf("competing once: %d %v", once, err)
				}
				if got := countRows(t, store, "native_tool_deliveries"); got != once {
					t.Fatalf("split audit: %d once %d", got, once)
				}
			})
		}
	}
}

func TestNativeReceiptRejectsMismatchedAuditCoordinates(t *testing.T) {
	for _, change := range []string{`target_origin='https://foreign.example'`, `rule_id='foreign.route'`, `proxy_id='foreign.proxy'`, `proxy_revision=7`, `policy_id='foreign'`, `policy_revision=2`, `account_id='foreign-account',account_settings_revision=1`, `operation_id='99999999999999999999999999999999'`, `connection_id='foreign-connection'`} {
		t.Run(change, func(t *testing.T) {
			store, id, rev := nativeStoreFixture(t, environment.ToolPolicyReview)
			repo := nativeFullRepository(t, store)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			nativeAllow(t, repo, id, now)
			receipt, err := repo.Start(ctx, id, "coordinate-start", rev, now)
			if err != nil {
				t.Fatal(err)
			}
			original, err := scanEgressAttempt(store.database.QueryRow(nativeDeliverySelect + ` WHERE attempt_id='coordinate-start'`))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.database.Exec(`UPDATE native_tool_deliveries SET ` + change + ` WHERE attempt_id='coordinate-start'`); err != nil {
				if change == `operation_id='99999999999999999999999999999999'` || change == `account_id='foreign-account',account_settings_revision=1` {
					if err := repo.VerifyStartReceipt(ctx, receipt, now); err != nil {
						t.Fatal(err)
					}
					return
				}
				t.Fatal(err)
			}
			if repo.VerifyStartReceipt(ctx, receipt, now) == nil {
				t.Fatal("mismatched committed audit accepted")
			}
			if _, err := store.database.Exec(`DELETE FROM native_tool_deliveries WHERE attempt_id='coordinate-start'`); err != nil {
				t.Fatal(err)
			}
			if _, err := appendNativeDelivery(ctx, store.database, original.Attempt); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeCancelRechecksExactOutstandingTarget(t *testing.T) {
	for _, stage := range []string{"start", "verify"} {
		t.Run(stage, func(t *testing.T) {
			store, id, rev := nativeStoreFixture(t, environment.ToolPolicyReview)
			repo := nativeFullRepository(t, store)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			base, _ := nativeDecisionFixture(t, store, id)
			nativeAllow(t, repo, id, now)
			if _, err := repo.Start(ctx, id, "cancel-target", rev, now); err != nil {
				t.Fatal(err)
			}
			catalog, _ := base.CatalogDigest()
			cancel, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestCancel, Plan: base.Plan(), Capture: base.Capture(), AssignmentRevision: 1, OwnerEpoch: 1, SessionID: base.SessionID(), SessionRevision: 3, CatalogGeneration: 1, CatalogDigest: &catalog, ExactRPC: []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3}}`)})
			if err != nil {
				t.Fatal(err)
			}
			cancelID := nativetool.ID("88888888888888888888888888888888")
			nativeReceiveBind(t, repo, cancelID, cancel, now)
			var receipt nativetool.StartReceipt
			if stage == "verify" {
				receipt, err = repo.Start(ctx, cancelID, "cancel-notification", rev, now)
				if err != nil {
					t.Fatal(err)
				}
				if err := repo.VerifyStartReceipt(ctx, receipt, now); err != nil {
					t.Fatal(err)
				}
			}
			if err := repo.Finish(ctx, id, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
				t.Fatal(err)
			}
			if stage == "start" {
				r, err := repo.Start(ctx, cancelID, "cancel-notification", rev, now)
				if !errors.Is(err, nativetool.ErrDenied) || r.OperationID() != "" {
					t.Fatalf("terminal target granted cancel start: %+v %v", r, err)
				}
			} else if repo.VerifyStartReceipt(ctx, receipt, now) == nil {
				t.Fatal("terminal target granted cancel verification")
			}
		})
	}
}

func TestNativeStartRejectsSubstitutedBindingEvidence(t *testing.T) {
	for _, change := range []string{"cancel", "request", "params", "group", "name", "replay"} {
		t.Run(change, func(t *testing.T) {
			store, id, rev := nativeStoreFixture(t, environment.ToolPolicyReview)
			repo := nativeFullRepository(t, store)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			base, _ := nativeDecisionFixture(t, store, id)
			nativeAllow(t, repo, id, now)
			corruptID := id
			assignment := "request_digest=zeroblob(32)"
			if change == "params" {
				assignment = "params_digest=zeroblob(32)"
			}
			if change == "group" {
				assignment = "group_digest=zeroblob(32)"
			}
			if change == "name" {
				assignment = "tool_name='other_public'"
			}
			if change == "replay" {
				assignment = "replay_key=zeroblob(32)"
			}
			if change == "cancel" {
				if _, err := repo.Start(ctx, id, "substitute-parent", rev, now); err != nil {
					t.Fatal(err)
				}
				other := nativeBindCall(t, repo, base, "66666666666666666666666666666666", 4, nil, now)
				otherRPC, _ := other.RPCIDDigest()
				catalog, _ := base.CatalogDigest()
				cancel, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestCancel, Plan: base.Plan(), Capture: base.Capture(), AssignmentRevision: 1, OwnerEpoch: 1, SessionID: base.SessionID(), SessionRevision: 3, CatalogGeneration: 1, CatalogDigest: &catalog, ExactRPC: []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3}}`)})
				if err != nil {
					t.Fatal(err)
				}
				corruptID = "88888888888888888888888888888888"
				nativeReceiveBind(t, repo, corruptID, cancel, now)
				assignment = fmt.Sprintf("cancel_rpc_id_digest=X'%x'", otherRPC[:])
			}
			// Synthetic on-disk corruption only: preserve the actual owned egress
			// row and restore immutability before any production Start can run.
			tx, err := store.database.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var immutableSQL string
			if err := tx.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='native_tool_binding_immutable'`).Scan(&immutableSQL); err != nil {
				t.Fatal(err)
			}
			field, _, _ := strings.Cut(assignment, "=")
			var original any
			if err := tx.QueryRow(`SELECT `+field+` FROM native_tool_bindings WHERE operation_id=?`, corruptID).Scan(&original); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`DROP TRIGGER native_tool_binding_immutable`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`UPDATE native_tool_bindings SET `+assignment+` WHERE operation_id=?`, corruptID); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(immutableSQL); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				restore, err := store.database.BeginTx(ctx, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer restore.Rollback()
				if _, err := restore.Exec(`DROP TRIGGER native_tool_binding_immutable`); err != nil {
					t.Error(err)
					return
				}
				if _, err := restore.Exec(`UPDATE native_tool_bindings SET `+field+`=? WHERE operation_id=?`, original, corruptID); err != nil {
					t.Error(err)
					return
				}
				if _, err := restore.Exec(immutableSQL); err != nil {
					t.Error(err)
					return
				}
				if err := restore.Commit(); err != nil {
					t.Error(err)
				}
			}()
			if r, err := repo.Start(ctx, corruptID, "corrupt-evidence", rev, now); err == nil || r.OperationID() != "" {
				t.Fatalf("substituted binding granted receipt: %s %v", r.OperationID(), err)
			}
		})
	}
}

func nativeNewSessionCatalog(t *testing.T, repo nativetool.Repository, old nativetool.Binding, owner, authority uint64, now time.Time) nativetool.Binding {
	t.Helper()
	ctx := context.Background()
	session := nativetool.ID("cccccccccccccccccccccccccccccccc")
	plan := old.Plan()
	if err := repo.RegisterSession(ctx, nativetool.SessionInput{ID: session, Capture: old.Capture(), Plan: plan, AssignmentRevision: old.AssignmentRevision(), ExpectedAuthorityRevision: authority, OwnerEpoch: owner, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	input := nativetool.BindingInput{Plan: plan, Capture: old.Capture(), AssignmentRevision: old.AssignmentRevision(), OwnerEpoch: owner, SessionID: session, SessionRevision: 1}
	listRaw := []byte(`{"jsonrpc":"2.0","id":22,"method":"tools/list"}`)
	for index, item := range []struct {
		kind nativetool.RequestKind
		raw  []byte
	}{{nativetool.RequestInitialize, []byte(`{"jsonrpc":"2.0","id":21,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)}, {nativetool.RequestInitialized, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)}, {nativetool.RequestToolsList, listRaw}} {
		if index == 2 {
			var err error
			input.SessionRevision, err = repo.ReadySession(ctx, session, 1, now)
			if err != nil {
				t.Fatal(err)
			}
		}
		input.Kind = item.kind
		input.ExactRPC = item.raw
		b, err := nativetool.NewBinding(input)
		if err != nil {
			t.Fatal(err)
		}
		id := nativetool.ID(fmt.Sprintf("%032x", 2000+index))
		nativeReceiveBind(t, repo, id, b, now)
		receipt, err := repo.Start(ctx, id, fmt.Sprintf("replacement-handshake-%d", index), authority, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.VerifyStartReceipt(ctx, receipt, now); err != nil {
			t.Fatal(err)
		}
		if err := repo.Finish(ctx, id, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	observer, err := nativetool.NewCatalogObserver(nativetool.CatalogScope{Capture: old.Capture(), Plan: plan, AssignmentRevision: old.AssignmentRevision(), OwnerEpoch: owner, SessionID: session, SessionRevision: 3, CatalogGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := observer.Observe([]nativetool.CatalogPageObservation{{ExactRequest: listRaw, ExactResponse: []byte(`{"jsonrpc":"2.0","id":22,"result":{"tools":[{"name":"read_file","inputSchema":{"type":"object"}}]}}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetCatalog(ctx, session, 2, evidence, now); err != nil {
		t.Fatal(err)
	}
	digest := evidence.Digest()
	input.Kind = nativetool.RequestToolsCall
	input.SessionRevision = 3
	input.CatalogGeneration = 1
	input.CatalogDigest = &digest
	input.CatalogEvidence = &evidence
	input.ExactRPC = []byte(`{"jsonrpc":"2.0","id":23,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`)
	args, _ := protocolcore.NewJSONObject([]byte(`{"path":"a"}`), 1024)
	key, _ := protocolcore.NewCallKey("native", "23")
	input.Intents = []protocolcore.ToolIntent{{ResponseID: "rpc-23", Call: protocolcore.ToolCall{Key: key, Name: "read_file", Arguments: args}}}
	input.ObservedCallID = []byte("stable-owner-call")
	b, err := nativetool.NewBinding(input)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNativeNewOwnerGenerationNeverResurrectsOldReceipt(t *testing.T) {
	store, id, rev := nativeStoreFixture(t, environment.ToolPolicyObserve)
	repo := nativeFullRepository(t, store)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	base, _ := nativeDecisionFixture(t, store, id)
	firstID := nativetool.ID("55555555555555555555555555555555")
	first := nativeBindCall(t, repo, base, firstID, 4, []byte("stable-owner-call"), now)
	receipt, err := repo.Start(ctx, firstID, "owner-generation-first", rev, now)
	if err != nil {
		t.Fatal(err)
	}
	newRev, err := repo.EnsureScope(ctx, nativetool.ScopeInput{Capture: base.Capture(), ExpectedRevision: rev, OwnerEpoch: 2, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if repo.VerifyStartReceipt(ctx, receipt, now) == nil {
		t.Fatal("old owner receipt revived")
	}
	op, err := repo.Get(ctx, firstID)
	if err != nil || op.State != "uncertain" || op.BytesOut != nil {
		t.Fatalf("owner boundary: %+v %v", op, err)
	}
	replacement := nativeNewSessionCatalog(t, repo, first, 2, newRev, now)
	replacementID := nativetool.ID("99999999999999999999999999999999")
	nativeReceiveBind(t, repo, replacementID, replacement, now)
	if r, err := repo.Start(ctx, replacementID, "owner-generation-repeat", newRev, now); !errors.Is(err, nativetool.ErrDenied) || r.OperationID() != "" {
		t.Fatal("new generation bypassed fresh Review")
	}
	nativeAllow(t, repo, replacementID, now)
	if r, err := repo.Start(ctx, replacementID, "owner-generation-repeat", newRev, now); !errors.Is(err, nativetool.ErrReplay) || r.OperationID() != "" {
		t.Fatalf("observed identity replayed across owner/session: %s %v", r.OperationID(), err)
	}
	if countRows(t, store, "native_tool_once") != 4 || countRows(t, store, "native_tool_deliveries") != 4 {
		t.Fatal("replacement erased/duplicated once evidence")
	}
}

func TestNativeReceiptRequiresExactConsumedApprovalAndRealStartRows(t *testing.T) {
	for _, corrupt := range []string{"consumption", "started-without-claim"} {
		t.Run(corrupt, func(t *testing.T) {
			store, id, rev := nativeStoreFixture(t, environment.ToolPolicyReview)
			repo := nativeFullRepository(t, store)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			nativeAllow(t, repo, id, now)
			op, err := repo.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if corrupt == "consumption" {
				receipt, err := repo.Start(ctx, id, "consumption-start", rev, now)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.database.Exec(`UPDATE native_tool_approvals SET consumed_at_unix_ms=? WHERE operation_id=?`, now.Add(time.Hour).UnixMilli(), id); err != nil {
					t.Fatal(err)
				}
				defer store.database.Exec(`UPDATE native_tool_approvals SET consumed_at_unix_ms=? WHERE operation_id=?`, now.UnixMilli(), id)
				if repo.VerifyStartReceipt(ctx, receipt, now) == nil {
					t.Fatal("substituted consumption accepted")
				}
			} else {
				// Explicit invalid datastore state: no approval consumption, once or audit.
				if _, err := store.database.Exec(`UPDATE native_tool_operations SET state='started',revision=revision+1 WHERE operation_id=?`, id); err != nil {
					t.Fatal(err)
				}
				if _, err := store.database.Exec(`INSERT INTO native_tool_events(operation_id,operation_revision,kind,occurred_at_unix_ms) VALUES(?,?,'started',?)`, id, op.Revision+1, now.UnixMilli()); err != nil {
					t.Fatal(err)
				}
				defer func() {
					store.database.Exec(`DELETE FROM native_tool_events WHERE operation_id=? AND operation_revision=?`, id, op.Revision+1)
					store.database.Exec(`UPDATE native_tool_operations SET state='validated',revision=? WHERE operation_id=?`, op.Revision, id)
				}()
				lookalike, err := store.nativeTools.issuer.Issue(nativetool.StartFacts{OperationID: id, AttemptID: "missing-claim", AuthorityRevision: rev, OwnerEpoch: 1, BindingDigest: *op.BindingDigest})
				if err != nil {
					t.Fatal(err)
				}
				if repo.VerifyStartReceipt(ctx, lookalike, now) == nil {
					t.Fatal("noncommitted rows granted authority")
				}
				if err := repo.Recover(ctx, now); !errors.Is(err, nativetool.ErrInvalid) {
					t.Fatalf("corrupt start recovery: %v", err)
				}
				if countRows(t, store, "native_tool_once") != 0 || countRows(t, store, "native_tool_deliveries") != 0 {
					t.Fatal("recovery fabricated missing execution evidence")
				}
			}
		})
	}
}

func TestNativeMarkReplayBarrierRequiresFreshQuestion(t *testing.T) {
	store, id, rev := nativeStoreFixture(t, environment.ToolPolicyObserve)
	repo := nativeFullRepository(t, store)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	base, _ := nativeDecisionFixture(t, store, id)
	if err := repo.MarkReplayBarrier(ctx, base.Capture(), rev, now); err != nil {
		t.Fatal(err)
	}
	if r, err := repo.Start(ctx, id, "barrier-initial", rev, now); !errors.Is(err, nativetool.ErrDenied) || r.OperationID() != "" {
		t.Fatal("barrier permitted policy start")
	}
	nativeAllow(t, repo, id, now)
	if err := repo.MarkReplayBarrier(ctx, base.Capture(), rev, now); err != nil {
		t.Fatal(err)
	}
	if r, err := repo.Start(ctx, id, "barrier-old-allow", rev, now); err == nil || r.OperationID() != "" {
		t.Fatal("old Allow once reused after barrier")
	}
	newID := nativetool.ID("99999999999999999999999999999999")
	nativeBindCall(t, repo, base, newID, 4, nil, now)
	nativeAllow(t, repo, newID, now)
	if r, err := repo.Start(ctx, newID, "barrier-fresh-allow", rev, now); err != nil || repo.VerifyStartReceipt(ctx, r, now) != nil {
		t.Fatalf("fresh question start: %v", err)
	}
}

func TestNativeStartProducesVerifiableCommittedReceipt(t *testing.T) {
	store, id, rev := nativeStoreFixture(t, environment.ToolPolicyReview)
	repo := store.NativeToolRepository()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if repo != store.NativeLifecycleRepository() {
		t.Fatal("lifecycle/full getters use different adapters")
	}
	nativeAllow(t, repo, id, now)
	receipt, err := repo.Start(ctx, id, "same-store-proof", rev, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.VerifyStartReceipt(ctx, receipt, now); err != nil {
		t.Fatal(err)
	}
	// The same committed rows remain active; only this other Store's issuer differs.
	other := openTestStore(t, store.databasePath)
	defer shutdownTestStore(t, other)
	if other.NativeToolRepository().VerifyStartReceipt(ctx, receipt, now) == nil {
		t.Fatal("foreign Store accepted active committed receipt")
	}
	if err := repo.VerifyStartReceipt(ctx, receipt, now); err != nil {
		t.Fatalf("foreign verification changed authority: %v", err)
	}
	for _, table := range []string{"runtime_raw_evidence_envelopes", "runtime_activities", "runtime_exchange_contents", "tool_approvals"} {
		if countRows(t, store, table) != 0 {
			t.Fatalf("native Start invented %s", table)
		}
	}
}

func TestNativeRestartOffMetadataNeverReplays(t *testing.T) {
	store, id, rev := nativeStoreFixture(t, environment.ToolPolicyObserve)
	repo := nativeFullRepository(t, store)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	base, _ := nativeDecisionFixture(t, store, id)
	firstID := nativetool.ID("55555555555555555555555555555555")
	nativeBindCall(t, repo, base, firstID, 4, []byte("stable-owner-call"), now)
	receipt, err := repo.Start(ctx, firstID, "off-generation-first", rev, now)
	if err != nil {
		t.Fatal(err)
	}
	nativePublishOff(t, store, id, true)
	if repo.VerifyStartReceipt(ctx, receipt, now) == nil {
		t.Fatal("OFF receipt remained active")
	}
	manager := newEnvironmentManager(t, store)
	snapshot, err := manager.Resolve(base.Plan().EnvironmentID())
	if err != nil {
		t.Fatal(err)
	}
	candidate := snapshot.Aggregate()
	candidate.Revision++
	route := base.Plan().Route()
	route.ID = "native.reenabled"
	route.Revision = 1
	candidate.ClientToolAccess = &environment.ClientToolAccessPolicy{Enabled: true, Route: &route}
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
	if repo.VerifyStartReceipt(ctx, receipt, now) == nil {
		t.Fatal("reenable revived receipt")
	}
	path := store.databasePath
	shutdownTestStore(t, store)
	store = openTestStore(t, path)
	defer shutdownTestStore(t, store)
	repo = nativeFullRepository(t, store)
	if err := repo.Recover(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var current uint64
	if err := store.database.QueryRow(`SELECT revision FROM native_tool_scopes`).Scan(&current); err != nil {
		t.Fatal(err)
	}
	observed := time.Now().UTC().Truncate(time.Millisecond)
	newRev, err := repo.EnsureScope(ctx, nativetool.ScopeInput{Capture: base.Capture(), ExpectedRevision: current, OwnerEpoch: 2, ExpiresAt: observed.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	b := nativeNewSessionCatalog(t, repo, base, 2, newRev, observed)
	operation := nativetool.ID("99999999999999999999999999999999")
	nativeReceiveBind(t, repo, operation, b, observed)
	nativeAllow(t, repo, operation, observed)
	if r, err := repo.Start(ctx, operation, "off-generation-repeat", newRev, observed); !errors.Is(err, nativetool.ErrReplay) || r.OperationID() != "" {
		t.Fatalf("OFF/reopen/recovery replayed: %s %v", r.OperationID(), err)
	}
	if countRows(t, store, "native_tool_contents") != 0 || countRows(t, store, "runtime_evidence_bodies") != 0 {
		t.Fatal("OFF history created content")
	}
}

func TestNativeCatalogRefreshCancelsUnconsumedFrozenReplyApproval(t *testing.T) {
	store, id, rev := nativeStoreFixture(t, environment.ToolPolicyReview)
	repo := nativeFullRepository(t, store)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	base, _ := nativeDecisionFixture(t, store, id)
	nativeAllow(t, repo, id, now)
	parentReceipt, err := repo.Start(ctx, id, "catalog-parent", rev, now)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repo.OpenObservedStream(ctx, parentReceipt, base, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ := base.CatalogDigest()
	server := nativetool.Digest(sha256.Sum256([]byte(`{"jsonrpc":"2.0","id":"server","method":"ping"}`)))
	replyID := nativetool.ID("66666666666666666666666666666666")
	reply, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestServerReply, Plan: base.Plan(), Capture: base.Capture(), AssignmentRevision: 1, OwnerEpoch: 1, SessionID: base.SessionID(), SessionRevision: 3, CatalogGeneration: 1, CatalogDigest: &catalog, ParentOperationID: &id, ServerRequestDigest: &server, ExactRPC: []byte(`{"jsonrpc":"2.0","id":"server","result":{}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := nativeBindObservedPing(t, repo, replyID, reply, base, stream, []byte(`{"jsonrpc":"2.0","id":"server","method":"ping"}`), []byte(`{"jsonrpc":"2.0","id":"server","result":{}}`), now); err != nil {
		t.Fatal(err)
	}
	nativeAllow(t, repo, replyID, now)
	listRaw := []byte(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)
	list, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestToolsList, Plan: base.Plan(), Capture: base.Capture(), AssignmentRevision: 1, OwnerEpoch: 1, SessionID: base.SessionID(), SessionRevision: 3, CatalogGeneration: 1, CatalogDigest: &catalog, ExactRPC: listRaw})
	if err != nil {
		t.Fatal(err)
	}
	listID := nativetool.ID("77777777777777777777777777777777")
	nativeReceiveBind(t, repo, listID, list, now)
	if _, err := repo.Start(ctx, listID, "catalog-refresh-list", rev, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Finish(ctx, listID, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	observer, err := nativetool.NewCatalogObserver(nativetool.CatalogScope{Capture: base.Capture(), Plan: base.Plan(), AssignmentRevision: 1, OwnerEpoch: 1, SessionID: base.SessionID(), SessionRevision: 4, CatalogGeneration: 2})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := observer.Observe([]nativetool.CatalogPageObservation{{ExactRequest: listRaw, ExactResponse: []byte(`{"jsonrpc":"2.0","id":4,"result":{"tools":[]}}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetCatalog(ctx, base.SessionID(), 3, evidence, now); err != nil {
		t.Fatal(err)
	}
	op, err := repo.Get(ctx, replyID)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := repo.GetApproval(ctx, *op.ApprovalID)
	if err != nil || approval.State != "canceled" || approval.ConsumedAt != nil {
		t.Fatalf("old unconsumed reply decision: %+v %v", approval, err)
	}
	if r, err := repo.Start(ctx, replyID, "catalog-old-allow", rev, now); err == nil || r.OperationID() != "" {
		t.Fatal("catalog refresh reused old reply Allow once")
	}
}

func TestNativeReplyRequiresCommittedParent(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			store, id, rev := nativeStoreFixture(t, environment.ToolPolicyReview)
			repo := nativeFullRepository(t, store)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			base, _ := nativeDecisionFixture(t, store, id)
			nativeAllow(t, repo, id, now)
			var stream nativetool.StreamAuthority
			if committed {
				receipt, err := repo.Start(ctx, id, "reply-parent-start", rev, now)
				if err != nil {
					t.Fatal(err)
				}
				stream, err = repo.OpenObservedStream(ctx, receipt, base, time.Minute, now)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				op, err := repo.Get(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.database.Exec(`UPDATE native_tool_operations SET state='started',revision=revision+1 WHERE operation_id=?`, id); err != nil {
					t.Fatal(err)
				}
				if _, err := store.database.Exec(`INSERT INTO native_tool_events(operation_id,operation_revision,kind,occurred_at_unix_ms) VALUES(?,?,'started',?)`, id, op.Revision+1, now.UnixMilli()); err != nil {
					t.Fatal(err)
				}
				defer func() {
					store.database.Exec(`DELETE FROM native_tool_events WHERE operation_id=? AND operation_revision=?`, id, op.Revision+1)
					store.database.Exec(`UPDATE native_tool_operations SET state='validated',revision=? WHERE operation_id=?`, op.Revision, id)
				}()
			}
			catalog, _ := base.CatalogDigest()
			server := nativetool.Digest(sha256.Sum256([]byte(`{"jsonrpc":"2.0","id":"server","method":"ping"}`)))
			replyID := nativetool.ID("66666666666666666666666666666666")
			reply, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestServerReply, Plan: base.Plan(), Capture: base.Capture(), AssignmentRevision: 1, OwnerEpoch: 1, SessionID: base.SessionID(), SessionRevision: 3, CatalogGeneration: 1, CatalogDigest: &catalog, ParentOperationID: &id, ServerRequestDigest: &server, ExactRPC: []byte(`{"jsonrpc":"2.0","id":"server","result":{}}`)})
			if err != nil {
				t.Fatal(err)
			}
			err = nativeBindObservedPing(t, repo, replyID, reply, base, stream, []byte(`{"jsonrpc":"2.0","id":"server","method":"ping"}`), []byte(`{"jsonrpc":"2.0","id":"server","result":{}}`), now)
			if !committed {
				if !errors.Is(err, nativetool.ErrDenied) {
					t.Fatalf("uncommitted parent granted reply binding: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			nativeAllow(t, repo, replyID, now)
			receipt, err := repo.Start(ctx, replyID, "registered-reply-start", rev, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.VerifyStartReceipt(ctx, receipt, now); err != nil {
				t.Fatal(err)
			}
			if err := repo.Finish(ctx, replyID, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeOnceObservedIdentityAndReplayBarrier(t *testing.T) {
	store, id, rev := nativeStoreFixture(t, environment.ToolPolicyObserve)
	repo := nativeFullRepository(t, store)
	base, _ := nativeDecisionFixture(t, store, id)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	first := nativetool.ID("55555555555555555555555555555555")
	nativeBindCall(t, repo, base, first, 4, []byte("observed-logical-call"), now)
	receipt, err := repo.Start(ctx, first, "observed-first", rev, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.VerifyStartReceipt(ctx, receipt, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Finish(ctx, first, nativetool.TerminalInput{Outcome: "uncertain", Reason: "execution_uncertain", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	second := nativetool.ID("66666666666666666666666666666666")
	nativeBindCall(t, repo, base, second, 5, []byte("observed-logical-call"), now)
	if r, err := repo.Start(ctx, second, "observed-repeat", rev, now); !errors.Is(err, nativetool.ErrDenied) || r.OperationID() != "" {
		t.Fatalf("barrier bypass: %+v %v", r, err)
	}
	nativeAllow(t, repo, second, now)
	if r, err := repo.Start(ctx, second, "observed-repeat", rev, now); !errors.Is(err, nativetool.ErrReplay) || r.OperationID() != "" {
		t.Fatalf("reused observed ID: %+v %v", r, err)
	}
	third := nativetool.ID("77777777777777777777777777777777")
	nativeBindCall(t, repo, base, third, 6, []byte("fresh-observed-call"), now)
	if r, err := repo.Start(ctx, third, "fresh-once", rev, now); !errors.Is(err, nativetool.ErrDenied) || r.OperationID() != "" {
		t.Fatal("fresh barrier operation bypassed Review")
	}
	nativeAllow(t, repo, third, now)
	if _, err := repo.Start(ctx, third, "fresh-once", rev, now); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, store, "native_tool_once"); got != 2 {
		t.Fatalf("once claims: %d", got)
	}
}

// Catches send authority issued without the single committed approval/once/audit/event transition.
func TestNativeStartConsumesApprovalAndClaimsOnceAtomically(t *testing.T) {
	store, id, revision := nativeStoreFixture(t, environment.ToolPolicyReview)
	repo := nativeFullRepository(t, store)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if receipt, err := repo.Start(ctx, id, "native-attempt-1", revision, now); !errors.Is(err, nativetool.ErrDenied) || receipt.OperationID() != "" {
		t.Fatalf("unapproved start: %+v %v", receipt, err)
	}
	nativeAllow(t, repo, id, now)
	receipt, err := repo.Start(ctx, id, "native-attempt-1", revision, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.VerifyStartReceipt(ctx, receipt, now); err != nil {
		t.Fatal(err)
	}
	queries := []string{
		`SELECT COUNT(*) FROM native_tool_approvals WHERE operation_id=? AND state='allowed' AND consumed_at_unix_ms IS NOT NULL`,
		`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=? AND attempt_id='native-attempt-1'`,
		`SELECT COUNT(*) FROM native_tool_deliveries WHERE operation_id=? AND outcome=''`,
		`SELECT COUNT(*) FROM native_tool_operations WHERE operation_id=? AND state='started' AND decision_source='user'`,
		`SELECT COUNT(*) FROM native_tool_events WHERE operation_id=? AND kind='started'`,
	}
	for _, query := range queries {
		var count int
		if err := store.database.QueryRowContext(ctx, query, id).Scan(&count); err != nil || count != 1 {
			t.Fatalf("durable mutation %s: %d %v", query, count, err)
		}
	}
	if receipt, err := repo.Start(ctx, id, "native-attempt-2", revision, now); err == nil || receipt.OperationID() != "" {
		t.Fatalf("second start: %+v %v", receipt, err)
	}
}
