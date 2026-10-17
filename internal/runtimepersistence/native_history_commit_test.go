package runtimepersistence

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/recording"
	"path/filepath"
	"testing"
	"time"
)

type nativeTestScopes struct{ epoch *recording.Epoch }

func (s nativeTestScopes) ScopeStamp(c captureidentity.Reference) recording.Stamp {
	stamp, _ := recording.NewStamp(s.epoch, c, recording.Fence{ArchiveEpoch: s.epoch.Value(), CaptureGeneration: 1})
	return stamp
}

type nativeHistorySpy struct {
	prepared, published, discarded int
	dropped                        int
	last                           nativetool.HistoryObservation
	reason                         nativetool.HistoryDiscardReason
}
type nativeHistoryTicketSpy struct {
	owner *nativeHistorySpy
	done  bool
}

func (s *nativeHistorySpy) PrepareNativeHistory(_ recording.Stamp, o nativetool.HistoryObservation) (nativetool.PreparedHistory, recording.OfferResult) {
	s.prepared++
	s.last = o
	return &nativeHistoryTicketSpy{owner: s}, recording.Accepted
}
func (t *nativeHistoryTicketSpy) Publish() recording.OfferResult {
	if !t.done {
		t.done = true
		t.owner.published++
	}
	return recording.Accepted
}
func (t *nativeHistoryTicketSpy) Discard(reason nativetool.HistoryDiscardReason) {
	if !t.done {
		t.done = true
		t.owner.discarded++
		t.owner.reason = reason
	}
}

type nativeHistoryCommitCheck struct {
	t     *testing.T
	spy   *nativeHistorySpy
	fault transactionCommitter
}

func (c nativeHistoryCommitCheck) Commit(tx *sql.Tx) error {
	if c.spy.published != 0 {
		c.t.Error("native history published before outer control commit")
	}
	return c.fault.Commit(tx)
}

func TestNativeHistoryPublishesOnlyAfterKnownOuterCommit(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		fault     transactionCommitter
		published int
	}{{"success", sqlTransactionCommitter{}, 1}, {"rollback", rollbackThenError{}, 0}, {"unknown", commitThenError{}, 0}} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			epoch, _ := recording.NewEpoch(1)
			spy := &nativeHistorySpy{}
			control, err := OpenControl(ctx, ControlOptions{DatabasePath: filepath.Join(t.TempDir(), "control.db"), LayoutID: "history-commit", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second, NativeHistory: spy, RecordingScopes: nativeTestScopes{epoch}})
			if err != nil {
				t.Fatal(err)
			}
			_, id, rev := nativeFixtureOnStore(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, true, nil, control.store, egressprofile.Direct())
			now := time.Now().UTC().Truncate(time.Millisecond)
			nativeAllow(t, control.store.NativeToolRepository(), id, now)
			*spy = nativeHistorySpy{}
			control.store.nativeTools.committer = nativeHistoryCommitCheck{t, spy, scenario.fault}
			receipt, err := control.NativeToolRepository().Start(ctx, id, "post-commit-history", rev, now)
			control.store.nativeTools.committer = sqlTransactionCommitter{}
			if scenario.published == 1 && err != nil {
				t.Fatal(err)
			}
			if scenario.published == 0 && (err == nil || receipt.OperationID() != "") {
				t.Fatal("failed/unknown commit issued receipt")
			}
			if spy.prepared != 1 || spy.published != scenario.published || spy.discarded != 1-scenario.published {
				t.Fatalf("ticket lifecycle: prepared=%d published=%d discarded=%d", spy.prepared, spy.published, spy.discarded)
			}
			if spy.last.Operation.State != "started" || spy.last.Operation.DecisionSource != "user" || spy.last.Operation.Arguments != nil || spy.last.Operation.Result != nil {
				t.Fatalf("not final safe state: %+v", spy.last.Operation)
			}
		})
	}
}

func TestNativeHistoryBulkRevokeCapsTicketsAndDropsOldScope(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprint(stale), func(t *testing.T) {
			ctx := context.Background()
			epoch, _ := recording.NewEpoch(1)
			spy := &nativeHistorySpy{}
			control, err := OpenControl(ctx, ControlOptions{DatabasePath: filepath.Join(t.TempDir(), "control.db"), LayoutID: "history-bulk", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second, NativeHistory: spy, RecordingScopes: nativeTestScopes{epoch}})
			if err != nil {
				t.Fatal(err)
			}
			_, id, rev := nativeFixtureOnStore(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, true, nil, control.store, egressprofile.Direct())
			base, _ := nativeDecisionFixture(t, control.store, id)
			now := time.Now().UTC().Truncate(time.Millisecond)
			for i := 0; i < 40; i++ {
				nativeBindCall(t, control.store.NativeToolRepository(), base, nativetool.ID(fmt.Sprintf("%032x", 100+i)), 100+i, nil, now)
			}
			*spy = nativeHistorySpy{}
			if stale {
				epoch.Advance(2)
				if _, err := control.store.database.Exec(`UPDATE runtime_storage_state SET archive_epoch=2`); err != nil {
					t.Fatal(err)
				}
			}
			if err := control.NativeToolRepository().Revoke(ctx, base.Capture(), rev, "revoked", now); err != nil {
				t.Fatal(err)
			}
			want := 32
			if stale {
				want = 0
			}
			if spy.prepared != want || spy.published != want || spy.dropped != 41-want {
				t.Fatalf("bounded original-scope publication: %+v want %d", spy, want)
			}
			var canceled int
			if err := control.store.database.QueryRow(`SELECT COUNT(*) FROM native_tool_operations WHERE state='canceled'`).Scan(&canceled); err != nil || canceled != 41 {
				t.Fatalf("dropped history prevented required revocation: %d %v", canceled, err)
			}
		})
	}
}

func (s *nativeHistorySpy) NativeHistoryDropped(nativetool.HistoryDropReason) { s.dropped++ }

type nativeGenerationScopes struct {
	epoch      *recording.Epoch
	generation uint64
}

func (s *nativeGenerationScopes) ScopeStamp(c captureidentity.Reference) recording.Stamp {
	stamp, _ := recording.NewStamp(s.epoch, c, recording.Fence{ArchiveEpoch: s.epoch.Value(), CaptureGeneration: s.generation})
	return stamp
}
func TestNativeHistoryOldCaptureGenerationDropsWithoutChangingExecution(t *testing.T) {
	ctx := context.Background()
	epoch, _ := recording.NewEpoch(1)
	scopes := &nativeGenerationScopes{epoch, 1}
	spy := &nativeHistorySpy{}
	control, err := OpenControl(ctx, ControlOptions{DatabasePath: filepath.Join(t.TempDir(), "control.db"), LayoutID: "generation", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second, NativeHistory: spy, RecordingScopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	_, id, rev := nativeFixtureOnStore(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, true, nil, control.store, egressprofile.Direct())
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeAllow(t, control.store.NativeToolRepository(), id, now)
	receipt, err := control.NativeToolRepository().Start(ctx, id, "original-generation", rev, now)
	if err != nil {
		t.Fatal(err)
	}
	*spy = nativeHistorySpy{}
	scopes.generation = 2
	if _, err := control.store.database.Exec(`UPDATE recording_capture_fences SET generation=2`); err != nil {
		t.Fatal(err)
	}
	if err := control.NativeToolRepository().VerifyStartReceipt(ctx, receipt, now); err != nil {
		t.Fatal("recording generation became send authority", err)
	}
	if err := control.NativeToolRepository().Finish(ctx, id, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if spy.prepared != 0 || spy.dropped != 1 {
		t.Fatalf("old generation republished: %+v", spy)
	}
	op, err := control.NativeToolRepository().Get(ctx, id)
	if err != nil || op.RecordingScope.Fence.CaptureGeneration != 1 {
		t.Fatal("original recording generation changed", err)
	}
}
func TestNativeHistoryCallbackRollbackDiscardsPreparedTerminal(t *testing.T) {
	ctx := context.Background()
	epoch, _ := recording.NewEpoch(1)
	spy := &nativeHistorySpy{}
	control, err := OpenControl(ctx, ControlOptions{DatabasePath: filepath.Join(t.TempDir(), "control.db"), LayoutID: "rollback", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second, NativeHistory: spy, RecordingScopes: nativeTestScopes{epoch}})
	if err != nil {
		t.Fatal(err)
	}
	_, id, rev := nativeFixtureOnStore(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, true, nil, control.store, egressprofile.Direct())
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeAllow(t, control.store.NativeToolRepository(), id, now)
	receipt, err := control.NativeToolRepository().Start(ctx, id, "rollback-terminal", rev, now)
	if err != nil {
		t.Fatal(err)
	}
	*spy = nativeHistorySpy{}
	if _, err := control.store.database.Exec(`CREATE TEMP TRIGGER reject_terminal_bytes BEFORE UPDATE OF bytes_in ON native_tool_operations BEGIN SELECT RAISE(ABORT,'synthetic terminal rollback'); END`); err != nil {
		t.Fatal(err)
	}
	err = control.NativeToolRepository().Finish(ctx, id, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", BytesIn: 7, BytesOut: 11, ObservedAt: now})
	if _, dropErr := control.store.database.Exec(`DROP TRIGGER reject_terminal_bytes`); dropErr != nil {
		t.Fatal(dropErr)
	}
	if err == nil || spy.prepared != 1 || spy.published != 0 || spy.discarded != 1 || spy.reason != nativetool.HistoryRolledBack {
		t.Fatalf("rollback ticket leak %+v %v", spy, err)
	}
	if err := control.NativeToolRepository().VerifyStartReceipt(ctx, receipt, now); err != nil {
		t.Fatal("rollback changed required authority", err)
	}
}
