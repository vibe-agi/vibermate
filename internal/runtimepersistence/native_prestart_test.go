package runtimepersistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func TestNativePrestartFinishRecordsZeroEffectWithoutAudit(t *testing.T) {
	for _, kind := range []string{"failed", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			store, id, _ := nativeExecutionHandshakeFixture(t)
			repo := store.NativeToolRepository()
			now := time.Now().UTC()
			reason := "transport_failed"
			if kind == "canceled" {
				reason = "canceled"
			}
			if err := repo.Finish(context.Background(), id, nativetool.TerminalInput{Outcome: kind, Reason: reason, ObservedAt: now}); err != nil {
				t.Fatalf("prestart terminal denied: %v", err)
			}
			op, err := repo.Get(context.Background(), id)
			if err != nil || op.State != kind || op.BytesOut == nil || *op.BytesOut != 0 {
				t.Fatalf("terminal %+v %v", op, err)
			}
			var claims int
			if err = store.database.QueryRow(`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, id).Scan(&claims); err != nil || claims != 0 {
				t.Fatalf("fabricated once %d %v", claims, err)
			}
			if err = repo.Finish(context.Background(), id, nativetool.TerminalInput{Outcome: kind, Reason: reason, ObservedAt: now}); err == nil {
				t.Fatal("terminal overwritten")
			}
		})
	}
}

func TestNativePrestartReceivedAndInvalidTerminals(t *testing.T) {
	store, id, _ := nativeExecutionHandshakeFixture(t)
	repo := store.NativeToolRepository()
	ctx := context.Background()
	now := time.Now().UTC()
	existing, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []nativetool.TerminalInput{{Outcome: "completed", Reason: "completed", ObservedAt: now}, {Outcome: "uncertain", Reason: "execution_uncertain", ObservedAt: now}, {Outcome: "failed", Reason: "rpc_error", BytesOut: 1, ObservedAt: now}, {Outcome: "canceled", Reason: "revoked", BytesIn: 1, ObservedAt: now}} {
		if err = repo.Finish(ctx, id, input); !errors.Is(err, nativetool.ErrInvalid) {
			t.Fatalf("invented effect accepted %v", err)
		}
	}
	receivedID := nativetool.ID("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	if _, err = repo.Receive(ctx, nativetool.ReceiveInput{ID: receivedID, Capture: existing.Capture, EnvironmentID: existing.EnvironmentID, EnvironmentRevision: existing.EnvironmentRevision, EnvironmentDigest: existing.EnvironmentDigest, ConnectionID: "real-unbound-prestart", TargetOrigin: existing.TargetOrigin, Method: "POST", Kind: nativetool.RequestUnclassified, Recording: existing.Recording, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = repo.Finish(ctx, receivedID, nativetool.TerminalInput{Outcome: "failed", Reason: "rpc_error", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	op, err := repo.Get(ctx, receivedID)
	if err != nil || op.State != "denied" || op.BindingDigest != nil || op.BytesOut == nil || *op.BytesOut != 0 {
		t.Fatalf("unbound terminal %+v %v", op, err)
	}
	if err = store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, store.databasePath)
	defer shutdownTestStore(t, reopened)
	op, err = reopened.NativeToolRepository().Get(ctx, receivedID)
	if err != nil || op.State != "denied" || op.BindingDigest != nil {
		t.Fatalf("reopened no-binding terminal %+v %v", op, err)
	}
}
