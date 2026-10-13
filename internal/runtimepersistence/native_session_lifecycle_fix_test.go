package runtimepersistence

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"testing"
	"time"
)

func TestNativeSessionTouchAndExactClose(t *testing.T) {
	store, id, _ := nativeExecutionHandshakeFixture(t)
	repo := store.NativeToolRepository()
	binding, _ := nativeDecisionFixture(t, store, id)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := repo.TouchSession(ctx, binding.SessionID(), binding.SessionRevision(), now.Add(30*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CloseSession(ctx, binding.SessionID(), binding.SessionRevision(), now); err != nil {
		t.Fatal(err)
	}
	if err := repo.TouchSession(ctx, binding.SessionID(), binding.SessionRevision()+1, now.Add(30*time.Minute), now); err == nil {
		t.Fatal("closed session revived")
	}
}

func TestNativeSessionTouchFencesAndCommitFaults(t *testing.T) {
	for _, condition := range []string{"stale", "expired", "recovered", "new-owner", "off", "wrong-id", "commit-error", "rollback-error"} {
		t.Run(condition, func(t *testing.T) {
			store, id, revision := nativeExecutionHandshakeFixture(t)
			repo := store.NativeToolRepository()
			binding, _ := nativeDecisionFixture(t, store, id)
			now := time.Now().UTC().Truncate(time.Millisecond)
			expected := binding.SessionRevision()
			sessionID := binding.SessionID()
			switch condition {
			case "stale":
				expected++
			case "expired":
				now = now.Add(2 * time.Hour)
			case "recovered":
				if err := repo.Recover(context.Background(), now); err != nil {
					t.Fatal(err)
				}
			case "new-owner":
				if _, err := repo.EnsureScope(context.Background(), nativetool.ScopeInput{Capture: binding.Capture(), ExpectedRevision: revision, OwnerEpoch: 2, ExpiresAt: now.Add(time.Hour)}); err != nil {
					t.Fatal(err)
				}
			case "off":
				nativePublishOff(t, store, id, true)
			case "wrong-id":
				sessionID = "ffffffffffffffffffffffffffffffff"
			case "commit-error":
				store.nativeTools.committer = commitThenError{}
			case "rollback-error":
				store.nativeTools.committer = rollbackThenError{}
			}
			err := repo.TouchSession(context.Background(), sessionID, expected, now.Add(30*time.Minute), now)
			store.nativeTools.committer = sqlTransactionCommitter{}
			if err == nil {
				t.Fatalf("%s renewed stale authority", condition)
			}
			if (condition == "commit-error" || condition == "rollback-error") && !errors.Is(err, nativetool.ErrUnavailable) {
				t.Fatalf("unknown commit misreported %v", err)
			}
		})
	}
}

func TestNativeSessionTouchExtendsLiveScopeWithoutRevisionOrReceiptChange(t *testing.T) {
	store, id, revision := nativeExecutionHandshakeFixture(t)
	repo := store.NativeToolRepository()
	binding, _ := nativeDecisionFixture(t, store, id)
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeAllow(t, repo, id, now)
	receipt, err := repo.Start(context.Background(), id, "touch-receipt", revision, now)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := repo.GetScope(context.Background(), binding.Capture())
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.TouchSession(context.Background(), binding.SessionID(), binding.SessionRevision(), now.Add(30*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if err = repo.VerifyStartReceipt(context.Background(), receipt, now); err != nil {
		t.Fatalf("immediate touch altered receipt %v", err)
	}
	for i := 0; i < 6; i++ {
		now = now.Add(20 * time.Minute)
		if err = repo.TouchSession(context.Background(), binding.SessionID(), binding.SessionRevision(), now.Add(30*time.Minute), now); err != nil {
			t.Fatal(err)
		}
	}
	if repo.VerifyStartReceipt(context.Background(), receipt, now) == nil {
		t.Fatal("session renewal revived expired approval receipt")
	}
	after, err := repo.GetScope(context.Background(), binding.Capture())
	if err != nil || after.Revision != scope.Revision || after.OwnerEpoch != scope.OwnerEpoch || after.ReplayBarrier != scope.ReplayBarrier || !after.ExpiresAt.After(now) {
		t.Fatalf("renewal changed fences %+v %v", after, err)
	}
}
