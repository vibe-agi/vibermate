package runtimepersistence

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func TestNativeSessionRegistrationPreservesExistingAuthority(t *testing.T) {
	store, id, revision := nativeExecutionHandshakeFixture(t)
	repo := store.NativeToolRepository()
	ctx := context.Background()
	now := time.Now().UTC()
	binding, _ := nativeDecisionFixture(t, store, id)
	nativeAllow(t, repo, id, now)
	receipt, err := repo.Start(ctx, id, "real-first-session", revision, now)
	if err != nil {
		t.Fatal(err)
	}
	second := nativetool.ID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err = repo.RegisterSession(ctx, nativetool.SessionInput{ID: second, Capture: binding.Capture(), Plan: binding.Plan(), AssignmentRevision: binding.AssignmentRevision(), ExpectedAuthorityRevision: revision, OwnerEpoch: binding.OwnerEpoch(), ExpiresAt: now.Add(30 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err = repo.VerifyStartReceipt(ctx, receipt, now); err != nil {
		t.Fatalf("legitimate second session invalidated first committed authority: %v", err)
	}
	var count int
	if err = store.database.QueryRow(`SELECT COUNT(*) FROM native_tool_sessions WHERE state IN('initializing','ready')`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("independent sessions %d %v", count, err)
	}
	for index := 2; index < 64; index++ {
		session := nativetool.ID(fmt.Sprintf("%032x", 10000+index))
		if err = repo.RegisterSession(ctx, nativetool.SessionInput{ID: session, Capture: binding.Capture(), Plan: binding.Plan(), AssignmentRevision: binding.AssignmentRevision(), ExpectedAuthorityRevision: revision, OwnerEpoch: binding.OwnerEpoch(), ExpiresAt: now.Add(30 * time.Minute)}); err != nil {
			t.Fatalf("session%d: %v", index+1, err)
		}
	}
	if err = repo.RegisterSession(ctx, nativetool.SessionInput{ID: "cccccccccccccccccccccccccccccccc", Capture: binding.Capture(), Plan: binding.Plan(), AssignmentRevision: binding.AssignmentRevision(), ExpectedAuthorityRevision: revision, OwnerEpoch: binding.OwnerEpoch(), ExpiresAt: now.Add(30 * time.Minute)}); err == nil {
		t.Fatal("65th session admitted")
	}
}

func TestNativeGetScopeRecoveryCASPreservesBarrier(t *testing.T) {
	store, id, revision := nativeExecutionHandshakeFixture(t)
	repo := store.NativeToolRepository()
	ctx := context.Background()
	now := time.Now().UTC()
	binding, _ := nativeDecisionFixture(t, store, id)
	nativeAllow(t, repo, id, now)
	receipt, err := repo.Start(ctx, id, "real-recovery-first", revision, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.Recover(ctx, now); err != nil {
		t.Fatal(err)
	}
	scope, err := repo.GetScope(ctx, binding.Capture())
	if err != nil || scope.Revision <= revision || !scope.ReplayBarrier || scope.State != "revoked" {
		t.Fatalf("recovered scope %+v %v", scope, err)
	}
	if _, err = repo.EnsureScope(ctx, nativetool.ScopeInput{Capture: binding.Capture(), ExpectedRevision: revision, OwnerEpoch: 2, ExpiresAt: now.Add(time.Hour)}); !errors.Is(err, nativetool.ErrConflict) {
		t.Fatalf("stale CAS: %v", err)
	}
	next, err := repo.EnsureScope(ctx, nativetool.ScopeInput{Capture: binding.Capture(), ExpectedRevision: scope.Revision, OwnerEpoch: 2, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	scope, err = repo.GetScope(ctx, binding.Capture())
	if err != nil || scope.Revision != next || !scope.ReplayBarrier || scope.OwnerEpoch != 2 || scope.State != "active" {
		t.Fatalf("replacement scope %+v %v", scope, err)
	}
	if repo.VerifyStartReceipt(ctx, receipt, now) == nil {
		t.Fatal("recovery resurrected receipt")
	}
	if err = repo.RegisterSession(ctx, nativetool.SessionInput{ID: "dddddddddddddddddddddddddddddddd", Capture: binding.Capture(), Plan: binding.Plan(), AssignmentRevision: binding.AssignmentRevision(), ExpectedAuthorityRevision: next, OwnerEpoch: 2, ExpiresAt: now.Add(30 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	var once int
	if err = store.database.QueryRow(`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, id).Scan(&once); err != nil || once != 1 {
		t.Fatalf("once lost %d %v", once, err)
	}
}
