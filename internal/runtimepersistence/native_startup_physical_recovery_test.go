package runtimepersistence

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"testing"
	"time"
)

func TestNativeStartupRecoversLogicalTerminalOpenDelivery(t *testing.T) {
	store, id, revision := nativeExecutionHandshakeFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	repo := nativeFullRepository(t, store)
	nativeAllow(t, repo, id, now)
	receipt, err := repo.Start(ctx, id, "interrupted-physical", revision, now)
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := nativeDecisionFixture(t, store, id)
	stream, err := repo.OpenObservedStream(ctx, receipt, binding, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.CompleteStreamRPC(ctx, stream, nativetool.LogicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	before, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.Recover(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	after, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if before.State != after.State || before.Reason != after.Reason {
		t.Fatal("physical recovery rewrote known logical outcome")
	}
	physical, err := scanEgressAttempt(store.database.QueryRow(nativeDeliverySelect + ` WHERE attempt_id='interrupted-physical'`))
	if err != nil {
		t.Fatal(err)
	}
	if !physical.Attempt.Terminal() || physical.Attempt.ByteCountsKnown() {
		t.Fatalf("physical outcome left open or invented counts: %+v", physical)
	}
	if err = repo.VerifyStartReceipt(ctx, receipt, now.Add(time.Second)); err == nil {
		t.Fatal("old send receipt survived recovery")
	}
	var once int
	if err = store.database.QueryRow(`SELECT count(*) FROM native_tool_once WHERE operation_id=? AND attempt_id='interrupted-physical'`, id).Scan(&once); err != nil || once != 1 {
		t.Fatal("once provenance lost", err)
	}
}

func TestNativeStartupPhysicalRecoveryCommitFault(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "unknown-commit"}[committed], func(t *testing.T) {
			store, id, revision := nativeExecutionHandshakeFixture(t)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			repo := nativeFullRepository(t, store)
			nativeAllow(t, repo, id, now)
			receipt, err := repo.Start(ctx, id, "recovery-commit", revision, now)
			if err != nil {
				t.Fatal(err)
			}
			binding, _ := nativeDecisionFixture(t, store, id)
			stream, err := repo.OpenObservedStream(ctx, receipt, binding, time.Minute, now)
			if err != nil {
				t.Fatal(err)
			}
			if err = repo.CompleteStreamRPC(ctx, stream, nativetool.LogicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
				t.Fatal(err)
			}
			store.nativeTools.committer = rollbackThenError{}
			if committed {
				store.nativeTools.committer = commitThenError{}
			}
			err = repo.Recover(ctx, now.Add(time.Second))
			store.nativeTools.committer = sqlTransactionCommitter{}
			if err == nil {
				t.Fatal("required recovery commit fault was ignored")
			}
			if err = repo.VerifyStartReceipt(ctx, receipt, now.Add(time.Second)); err == nil {
				t.Fatal("receipt remained executable after uncertain recovery")
			}
			physical, err := scanEgressAttempt(store.database.QueryRow(nativeDeliverySelect + ` WHERE attempt_id='recovery-commit'`))
			if err != nil || physical.Attempt.Terminal() != committed {
				t.Fatalf("wrong physical commit fact: %+v %v", physical, err)
			}
			if err = repo.Recover(ctx, now.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			operation, err := repo.Get(ctx, id)
			if err != nil || operation.State != "completed" {
				t.Fatalf("logical result lost after retry: %+v %v", operation, err)
			}
		})
	}
}
