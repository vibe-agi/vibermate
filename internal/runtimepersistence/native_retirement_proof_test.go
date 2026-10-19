package runtimepersistence

import (
	"context"
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func TestNativeRetirementReturnsOnlyCommittedPhysicalReceipts(t *testing.T) {
	for _, kind := range []string{"stream", "callback"} {
		for _, childState := range []string{"unbound", "bound", "started"} {
			t.Run(kind+"/"+childState, func(t *testing.T) {
				f := newCallbackSQLFixture(t)
				ctx := context.Background()
				var childReceipt nativetool.StartReceipt
				if childState != "unbound" {
					if err := f.repo.BindObservedReply(ctx, f.child, f.reply, f.callback, f.witness, f.now); err != nil {
						t.Fatal(err)
					}
				}
				if childState == "started" {
					nativeAllow(t, f.repo, f.child, f.now)
					var err error
					childReceipt, err = f.repo.Start(ctx, f.child, "retirement-proof-child", f.revision, f.now)
					if err != nil {
						t.Fatal(err)
					}
				}
				retire := func() ([]nativetool.StartReceipt, error) {
					if kind == "stream" {
						return f.repo.RetireObservedStream(ctx, f.stream, f.now)
					}
					return f.repo.RetireObservedCallback(ctx, f.callback, f.now)
				}
				proof, err := retire()
				if err != nil {
					t.Fatal(err)
				}
				want := map[nativetool.ID]nativetool.StartReceipt{}
				if kind == "stream" {
					want[f.parent] = f.receipt
				}
				if childState == "started" {
					want[f.child] = childReceipt
				}
				for id := range want {
					audit, err := scanEgressAttempt(f.store.database.QueryRow(nativeDeliverySelect+` WHERE operation_id=?`, id))
					if err != nil || !audit.Attempt.Terminal() {
						t.Fatal("actual retirement transaction did not close expected physical audit", err)
					}
				}
				if kind == "callback" {
					parent, err := scanEgressAttempt(f.store.database.QueryRow(nativeDeliverySelect+` WHERE operation_id=?`, f.parent))
					if err != nil || parent.Attempt.Terminal() {
						t.Fatal("child retirement closed original parent physical attempt")
					}
				}
				if len(proof) != len(want) {
					t.Fatalf("actual committed retirement returned %d exact receipt proofs, want%d", len(proof), len(want))
				}
				for _, receipt := range proof {
					if expected, ok := want[receipt.OperationID()]; !ok || receipt != expected {
						t.Fatal("retirement returned foreign/new/duplicate receipt")
					}
					delete(want, receipt.OperationID())
				}
				if again, err := retire(); err != nil || len(again) != 0 {
					t.Fatal("absent/empty registration reissued physical proof", err)
				}
			})
		}
	}
}

func TestNativeRetirementErrorsAndForeignAuthoritiesReturnNoProof(t *testing.T) {
	for _, kind := range []string{"stream", "callback"} {
		for _, edge := range []string{"foreign", "rollback", "committed_error"} {
			t.Run(kind+"/"+edge, func(t *testing.T) {
				f := newCallbackSQLFixture(t)
				ctx := context.Background()
				if err := f.repo.BindObservedReply(ctx, f.child, f.reply, f.callback, f.witness, f.now); err != nil {
					t.Fatal(err)
				}
				nativeAllow(t, f.repo, f.child, f.now)
				if _, err := f.repo.Start(ctx, f.child, "retirement-negative-child", f.revision, f.now); err != nil {
					t.Fatal(err)
				}
				stream, callback := f.stream, f.callback
				if edge == "foreign" {
					issuer, _ := nativetool.NewStartReceiptIssuer()
					var err error
					stream, err = issuer.IssueStream(f.stream.ExpiresAt())
					if err != nil {
						t.Fatal(err)
					}
					callback, err = issuer.IssueCallback()
					if err != nil {
						t.Fatal(err)
					}
				} else {
					f.store.nativeTools.issuerMu.Lock()
					if edge == "rollback" {
						f.store.nativeTools.committer = rollbackThenError{}
					} else {
						f.store.nativeTools.committer = commitThenError{}
					}
					f.store.nativeTools.issuerMu.Unlock()
				}
				var proof []nativetool.StartReceipt
				var err error
				if kind == "stream" {
					proof, err = f.repo.RetireObservedStream(ctx, stream, f.now)
				} else {
					proof, err = f.repo.RetireObservedCallback(ctx, callback, f.now)
				}
				f.store.nativeTools.issuerMu.Lock()
				f.store.nativeTools.committer = sqlTransactionCommitter{}
				f.store.nativeTools.issuerMu.Unlock()
				if len(proof) != 0 || edge == "foreign" && err != nil || edge != "foreign" && !errors.Is(err, nativetool.ErrUnavailable) {
					t.Fatalf("failed/foreign retirement exposed proof: count%d err%v", len(proof), err)
				}
			})
		}
	}
}
