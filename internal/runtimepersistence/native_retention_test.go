package runtimepersistence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

// Catches Full content reads that ignore Capture ownership, byte budget, expiry or verified body integrity.
func TestNativeContentAuthorizedBudgetAndIntegrity(t *testing.T) {
	store, id, rev := nativeLifecycleFixtureRecording(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1})
	repo := nativeFullRepository(t, store)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeAllow(t, repo, id, now)
	if _, err := repo.Start(ctx, id, "content-start", rev, now); err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte(`{"path":"a"}`), 20000)
	if err := repo.PutContent(ctx, id, "tool_arguments", body, now); err != nil {
		t.Fatal(err)
	}
	op, err := repo.Get(ctx, id)
	if err != nil || op.Arguments == nil {
		t.Fatalf("descriptor: %+v %v", op, err)
	}
	got, err := repo.ReadContent(ctx, op.Capture, id, "tool_arguments", 5, 128<<10)
	if err != nil || !bytes.Equal(got, body[5:5+(128<<10)]) {
		t.Fatalf("read: %d %v", len(got), err)
	}
	for _, request := range []struct {
		capture       captureidentity.Reference
		offset, limit int
	}{{op.Capture, -1, 1}, {op.Capture, 0, 0}, {op.Capture, 0, (128 << 10) + 1}, {op.Capture, len(body) + 1, 1}, {captureidentity.Reference{Kind: op.Capture.Kind, ID: "wrong"}, 0, 1}} {
		if _, err := repo.ReadContent(ctx, request.capture, id, "tool_arguments", request.offset, request.limit); err == nil {
			t.Fatalf("invalid read: %+v", request)
		}
	}
	if err := repo.PutContent(ctx, id, "elicitation", []byte("secret"), now); err == nil {
		t.Fatal("sensitive content part accepted")
	}
	if _, err := store.database.Exec(`UPDATE runtime_evidence_bodies SET plain_bytes=999 WHERE digest=?`, op.Arguments.Digest[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReadContent(ctx, op.Capture, id, "tool_arguments", 0, 1); err == nil {
		t.Fatal("corrupt body returned")
	}
	// Descriptor projections are independent of body validation.
	if _, err := repo.Get(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func TestNativeContentSharedBodyGC(t *testing.T) {
	for _, first := range []string{"raw", "native"} {
		t.Run(first, func(t *testing.T) {
			store, id, rev := nativeLifecycleFixtureRecording(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1})
			repo := nativeFullRepository(t, store)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			nativeAllow(t, repo, id, now)
			if _, err := repo.Start(ctx, id, "shared-content", rev, now); err != nil {
				t.Fatal(err)
			}
			body := []byte(`{"shared":"safe tool result"}`)
			if err := repo.PutContent(ctx, id, "tool_result", body, now); err != nil {
				t.Fatal(err)
			}
			op, err := repo.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			raw := rawEvidenceRecordForTest("native-shared-raw", 1, rawevidence.LayerClientIngress, body, []byte(`{"version":1,"headers":[]}`))
			raw.ObservedAt = now
			raw.ExpiresAt = now.Add(time.Hour)
			if err := store.RawEvidenceRepository().AppendBatch(ctx, []rawevidence.StoredEnvelope{raw}, now); err != nil {
				t.Fatal(err)
			}
			if countRows(t, store, "runtime_evidence_bodies") != 1 {
				t.Fatal("shared bytes duplicated")
			}
			if first == "raw" {
				if err := store.MaintainExpired(ctx, now.Add(2*time.Hour)); err != nil {
					t.Fatal(err)
				}
				got, err := repo.ReadContent(ctx, op.Capture, id, "tool_result", 0, 128<<10)
				if err != nil || !bytes.Equal(got, body) {
					t.Fatalf("native root lost: %s %v", got, err)
				}
				if _, err := store.DeleteCapture(ctx, string(op.Capture.Kind), op.Capture.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := store.DeleteCapture(ctx, string(op.Capture.Kind), op.Capture.ID); err != nil {
					t.Fatal(err)
				}
				loaded, err := store.RawEvidenceRepository().GetEnvelope(ctx, raw.EnvelopeID)
				if err != nil || !bytes.Equal(loaded.Body, body) {
					t.Fatalf("raw root lost: %+v %v", loaded, err)
				}
				if err := store.MaintainExpired(ctx, now.Add(2*time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			if countRows(t, store, "runtime_evidence_bodies") != 0 || countRows(t, store, "runtime_evidence_chunks") != 0 {
				t.Fatal("last root did not release shared body")
			}
		})
	}
}

func TestNativeDeleteCaptureKeepsOtherCaptureAuditOnSharedConnection(t *testing.T) {
	store, id, rev := nativeLifecycleFixtureRecording(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1})
	repo := nativeFullRepository(t, store)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	base, _ := nativeDecisionFixture(t, store, id)
	firstID := nativetool.ID("55555555555555555555555555555555")
	nativeBindCall(t, repo, base, firstID, 4, nil, now)
	nativeAllow(t, repo, firstID, now)
	if _, err := repo.Start(ctx, firstID, "shared-capture-first", rev, now); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"shared":"two native Captures"}`)
	if err := repo.PutContent(ctx, firstID, "tool_result", body, now); err != nil {
		t.Fatal(err)
	}
	manager := newManualCaptureManager(t, store, &manualCaptureClock{now: now}, &manualCaptureRandom{next: 87})
	grant, err := manager.Create(ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Independent native Capture", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	if err != nil {
		t.Fatal(err)
	}
	capture := captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: grant.Capture.ID}
	assignment, exists, err := store.CaptureAssignmentRepository().Load(ctx, base.Capture())
	if err != nil || !exists {
		t.Fatal(err)
	}
	assignment.Capture = capture
	assignment.UpdatedAt = now
	if result, err := store.CaptureAssignmentRepository().Write(ctx, 0, assignment); err != nil || result.Outcome != captureassignment.CommitOutcomeCommitted {
		t.Fatalf("second assignment: %+v %v", result, err)
	}
	secondRev, err := repo.EnsureScope(ctx, nativetool.ScopeInput{Capture: capture, OwnerEpoch: 1, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestInitialize, Plan: base.Plan(), Capture: capture, AssignmentRevision: 1, OwnerEpoch: 1, SessionID: "dddddddddddddddddddddddddddddddd", SessionRevision: 1, ExactRPC: []byte(`{"jsonrpc":"2.0","id":20,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	second := nativeNewSessionCatalog(t, repo, seed, 1, secondRev, now)
	secondID := nativetool.ID("99999999999999999999999999999999")
	nativeReceiveBind(t, repo, secondID, second, now)
	nativeAllow(t, repo, secondID, now)
	receipt, err := repo.Start(ctx, secondID, "shared-capture-second", secondRev, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.PutContent(ctx, secondID, "tool_result", body, now); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.DeleteCapture(ctx, string(base.Capture().Kind), base.Capture().ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Attempts != 1 {
		t.Fatalf("exact Capture deletion released other audits: %+v", deleted)
	}
	if err := repo.VerifyStartReceipt(ctx, receipt, now); err != nil {
		t.Fatalf("other Capture lost audit authority: %v", err)
	}
	got, err := repo.ReadContent(ctx, capture, secondID, "tool_result", 0, 128<<10)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("other Capture lost shared content: %s %v", got, err)
	}
}

func TestNativeContentExpiredAndRecordingOff(t *testing.T) {
	store, id, rev := nativeLifecycleFixtureRecording(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1})
	repo := nativeFullRepository(t, store)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeAllow(t, repo, id, now)
	if _, err := repo.Start(ctx, id, "expiry-content", rev, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.PutContent(ctx, id, "tool_result", []byte(`{"safe":true}`), now); err != nil {
		t.Fatal(err)
	}
	op, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// Constrained historical time passage; no execution authority is created.
	created := op.CreatedAt.Add(-2 * 24 * time.Hour)
	if _, err := store.database.Exec(`UPDATE native_tool_operations SET created_at_unix_ms=? WHERE operation_id=?`, created.UnixMilli(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.Exec(`UPDATE native_tool_contents SET expires_at_unix_ms=? WHERE operation_id=?`, created.Add(24*time.Hour).UnixMilli(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReadContent(ctx, op.Capture, id, "tool_result", 0, 1); !errors.Is(err, nativetool.ErrDenied) {
		t.Fatalf("expired read: %v", err)
	}
	if err := store.MaintainExpired(ctx, now); err != nil {
		t.Fatal(err)
	}
	if countRows(t, store, "native_tool_contents") != 0 || countRows(t, store, "runtime_evidence_bodies") != 0 {
		t.Fatal("expired content retained")
	}
	for _, mode := range []environment.ContentRecordingMode{environment.ContentRecordingOff, environment.ContentRecordingMetadataOnly} {
		t.Run(string(mode), func(t *testing.T) {
			recording := environment.ContentRecordingPolicy{Mode: mode}
			if mode == environment.ContentRecordingMetadataOnly {
				recording.RetentionDays = 1
			}
			s, op, rev := nativeLifecycleFixtureRecording(t, environment.ToolPolicyObserve, recording)
			r := nativeFullRepository(t, s)
			observed := time.Now().UTC().Truncate(time.Millisecond)
			if _, err := r.Start(ctx, op, "off-content", rev, observed); err != nil {
				t.Fatal(err)
			}
			if err := r.PutContent(ctx, op, "tool_arguments", []byte(`{"safe":true}`), observed); !errors.Is(err, nativetool.ErrDenied) {
				t.Fatalf("OFF/meta accepted content: %v", err)
			}
			if countRows(t, s, "runtime_evidence_bodies") != 0 {
				t.Fatal("OFF/meta body persisted")
			}
		})
	}
}

func TestNativeCapacityRejectsWithoutEvictingSafety(t *testing.T) {
	for _, kind := range []string{"once", "pending"} {
		t.Run(kind, func(t *testing.T) {
			store, id, rev := nativeStoreFixture(t, environment.ToolPolicyObserve)
			repo := nativeFullRepository(t, store)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			op, err := repo.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "once" {
				// Historical minimal once tombstones are seed evidence only, never Start proof.
				if _, err := store.database.Exec(`WITH RECURSIVE ids(n) AS(VALUES(1) UNION ALL SELECT n+1 FROM ids WHERE n<100000) INSERT INTO native_tool_once(capture_kind,capture_id,replay_key,operation_id,intent_digest,attempt_id,claimed_at_unix_ms,authority_revision) SELECT ?,?,CAST(printf('%032d',n) AS BLOB),printf('%032x',n),zeroblob(32),'historical-once-'||n,?,1 FROM ids`, op.Capture.Kind, op.Capture.ID, now.UnixMilli()); err != nil {
					t.Fatal(err)
				}
				if receipt, err := repo.Start(ctx, id, "overflow-once", rev, now); !errors.Is(err, nativetool.ErrDenied) || receipt.OperationID() != "" {
					t.Fatalf("once overflow: %v", err)
				}
				if countRows(t, store, "native_tool_once") != 100000 || countRows(t, store, "runtime_egress_attempts") != 0 {
					t.Fatal("once safety evicted/partially committed")
				}
			} else {
				// Actual Receive commits fill the remaining slots (the fixture has two pending rows).
				for n := 0; n < 1022; n++ {
					_, err := repo.Receive(ctx, nativetool.ReceiveInput{ID: nativetool.ID(fmt.Sprintf("%032x", 10000+n)), Capture: op.Capture, EnvironmentID: op.EnvironmentID, EnvironmentRevision: op.EnvironmentRevision, EnvironmentDigest: op.EnvironmentDigest, ConnectionID: "capacity-fixture", TargetOrigin: op.TargetOrigin, Method: "POST", Kind: nativetool.RequestUnclassified, Recording: op.Recording, ObservedAt: now})
					if err != nil {
						t.Fatal(err)
					}
				}
				if _, err := repo.Receive(ctx, nativetool.ReceiveInput{ID: "99999999999999999999999999999999", Capture: op.Capture, EnvironmentID: op.EnvironmentID, EnvironmentRevision: op.EnvironmentRevision, EnvironmentDigest: op.EnvironmentDigest, ConnectionID: "capacity-fixture", TargetOrigin: op.TargetOrigin, Method: "POST", Kind: nativetool.RequestUnclassified, Recording: op.Recording, ObservedAt: now}); !errors.Is(err, nativetool.ErrDenied) {
					t.Fatalf("pending overflow: %v", err)
				}
				var pending int
				if err := store.database.QueryRow(`SELECT COUNT(*) FROM native_tool_operations WHERE state IN('received','validated','awaiting_decision','started')`).Scan(&pending); err != nil || pending != 1024 {
					t.Fatalf("pending evicted: %d %v", pending, err)
				}
			}
		})
	}
}

func TestNativeDeletionReleasesFullContentButPreservesReplayBarrier(t *testing.T) {
	for _, clear := range []bool{false, true} {
		t.Run(map[bool]string{false: "capture", true: "archive"}[clear], func(t *testing.T) {
			store, id, rev := nativeLifecycleFixtureRecording(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1})
			repo := nativeFullRepository(t, store)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			nativeAllow(t, repo, id, now)
			receipt, err := repo.Start(ctx, id, "deletion-start", rev, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.PutContent(ctx, id, "tool_result", []byte(`{"data":"safe"}`), now); err != nil {
				t.Fatal(err)
			}
			op, err := repo.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if clear {
				_, err = store.ClearEvidence(ctx)
			} else {
				deleted, e := store.DeleteCapture(ctx, string(op.Capture.Kind), op.Capture.ID)
				err = e
				if deleted.Approvals != 1 || deleted.Attempts != 1 {
					t.Fatalf("native counts: %+v", deleted)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if repo.VerifyStartReceipt(ctx, receipt, now) == nil {
				t.Fatal("deleted receipt accepted")
			}
			for _, table := range []string{"native_tool_contents", "native_tool_operations", "runtime_evidence_bodies", "runtime_evidence_chunks"} {
				if count := countRows(t, store, table); count != 0 {
					t.Fatalf("retained %s: %d", table, count)
				}
			}
			var state string
			var barrier bool
			if err := store.database.QueryRow(`SELECT state,replay_barrier FROM native_tool_scopes`).Scan(&state, &barrier); err != nil || state != "closed" || !barrier {
				t.Fatalf("closed tombstone: %s %v %v", state, barrier, err)
			}
		})
	}
}

// Catches dropping inbound/once protection when safe 30-day metadata expires.
func TestNativeRetentionKeepsInboundIdentity(t *testing.T) {
	store, id, rev := nativeStoreFixture(t, environment.ToolPolicyReview)
	repo := nativeFullRepository(t, store)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeAllow(t, repo, id, now)
	if _, err := repo.Start(ctx, id, "retention-start", rev, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Finish(ctx, id, nativetool.TerminalInput{Outcome: "uncertain", Reason: "execution_uncertain", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.MaintainExpired(ctx, now.Add(31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, store, "native_tool_operations"); got != 1 {
		t.Fatalf("remaining nonterminal initialized metadata: %d", got)
	}
	if got := countRows(t, store, "native_tool_once"); got != 1 {
		t.Fatalf("once tombstone lost: %d", got)
	}
	if got := countRows(t, store, "runtime_egress_attempts"); got != 0 {
		t.Fatalf("expired native audit metadata retained: %d", got)
	}
	if got := countRows(t, store, "native_tool_inbound"); got != 4 {
		t.Fatalf("inbound identity lost: %d", got)
	}
	var barrier bool
	if err := store.database.QueryRow(`SELECT replay_barrier FROM native_tool_scopes`).Scan(&barrier); err != nil || !barrier {
		t.Fatalf("barrier: %v %v", barrier, err)
	}
}

func TestNativeRetiredHistoryAndLiveRenewalPreserveIdentity(t *testing.T) {
	store, id, rev := nativeStoreFixture(t, environment.ToolPolicyReview)
	repo := nativeFullRepository(t, store)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	base, _ := nativeDecisionFixture(t, store, id)
	nativeAllow(t, repo, id, now)
	receipt, err := repo.Start(ctx, id, "history-start", rev, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Finish(ctx, id, nativetool.TerminalInput{Outcome: "uncertain", Reason: "execution_uncertain", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.MaintainExpired(ctx, now.Add(31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	newRev, err := repo.EnsureScope(ctx, nativetool.ScopeInput{Capture: base.Capture(), ExpectedRevision: rev, OwnerEpoch: 1, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	rpc, _ := base.RPCIDDigest()
	digest := base.RequestDigest()
	session := base.SessionID()
	plan := base.Plan()
	result, err := repo.Receive(ctx, nativetool.ReceiveInput{ID: "99999999999999999999999999999999", Capture: base.Capture(), EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "renewed-native", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "POST", Kind: base.Kind(), SessionID: &session, RPCIDDigest: &rpc, RequestDigest: &digest, Recording: plan.ContentRecording(), ObservedAt: now})
	if err != nil || !result.Duplicate || result.OperationID != id {
		t.Fatalf("expired inbound identity: %+v %v", result, err)
	}
	if r, err := repo.Start(ctx, result.OperationID, "expired-retry", newRev, now); err == nil || r.OperationID() != "" {
		t.Fatal("expired metadata restored execution")
	}
	if repo.VerifyStartReceipt(ctx, receipt, now) == nil {
		t.Fatal("old receipt survived metadata expiry")
	}
	if countRows(t, store, "native_tool_inbound") != 4 || countRows(t, store, "native_tool_once") != 1 {
		t.Fatal("renewal reset protections")
	}
	path := store.databasePath
	shutdownTestStore(t, store)
	reopened := openTestStore(t, path)
	defer shutdownTestStore(t, reopened)
	if countRows(t, reopened, "native_tool_inbound") != 4 || countRows(t, reopened, "native_tool_once") != 1 {
		t.Fatal("reopen reset protections")
	}
	// A separate retained terminal remains readable after actual session retirement.
	s, operation, authority := nativeExecutionHandshakeFixture(t)
	r := nativeFullRepository(t, s)
	observed := time.Now().UTC().Truncate(time.Millisecond)
	nativeAllow(t, r, operation, observed)
	if _, err := r.Start(ctx, operation, "retired-projection", authority, observed); err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(ctx, operation, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", ObservedAt: observed}); err != nil {
		t.Fatal(err)
	}
	op, err := r.Get(ctx, operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Revoke(ctx, op.Capture, authority, "revoked", observed); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Get(ctx, operation); err != nil || got.State != "completed" {
		t.Fatalf("retired history disappeared: %+v %v", got, err)
	}
	if _, err := r.Page(ctx, nativetool.PageRequest{Capture: op.Capture, Limit: 200}); err != nil {
		t.Fatal(err)
	}
}
