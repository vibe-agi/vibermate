package runtimepersistence

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
)

func TestEgressTerminalSnapshotWithoutStart(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	repository := store.EgressAttemptRepository()
	writer, ok := repository.(interface {
		PutAttempt(context.Context, egressaudit.Attempt) error
	})
	if !ok {
		t.Fatal("repository cannot retain self-contained terminal snapshots")
	}
	started := providerAttempt(t, "terminal-only")
	terminal, err := started.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeCompleted, BytesOut: 123, BytesIn: 456, CompletedAt: started.StartedAt().Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []egressaudit.Attempt{terminal, terminal, started} {
		if err := writer.PutAttempt(context.Background(), attempt); err != nil {
			t.Fatal(err)
		}
	}
	assertSnapshot := func() {
		t.Helper()
		page, err := repository.List(context.Background(), egressaudit.PageRequest{ExchangeID: started.Parent().ExchangeID})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || !reflect.DeepEqual(page.Items[0].Attempt, terminal) {
			t.Fatalf("terminal/frozen tuple changed: %+v", page)
		}
	}
	assertSnapshot()
	conflictingTerminal, err := started.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeCompleted, BytesOut: 124, BytesIn: 456, CompletedAt: started.StartedAt().Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.PutAttempt(context.Background(), conflictingTerminal); err == nil {
		t.Fatal("conflicting terminal accepted")
	}
	assertSnapshot()
	input := egressaudit.NewInput{ID: started.ID(), ConnectionID: "other-connection", Purpose: started.Purpose(), PayloadClass: started.PayloadClass(), Parent: started.Parent(), Caller: started.Caller(), CallerID: started.CallerID(), TargetOrigin: started.TargetOrigin(), Decision: started.Decision(), ReusedTransport: started.ReusedTransport(), StartedAt: started.StartedAt()}
	conflict, err := egressaudit.New(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.PutAttempt(context.Background(), conflict); err == nil {
		t.Fatal("conflicting immutable tuple accepted")
	}
	assertSnapshot()
}

func TestEgressSnapshotStartThenTerminalNormalizesTimes(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	writer := store.EgressAttemptRepository().(egressaudit.AttemptStore)
	started := providerAttempt(t, "start-first")
	if err := writer.PutAttempt(context.Background(), started); err != nil {
		t.Fatal(err)
	}
	terminal, err := started.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeFailed, ErrorClass: "upstream_failed", BytesOut: 19, BytesIn: 23, CompletedAt: started.StartedAt().Add(time.Second + 123456*time.Nanosecond)})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := writer.PutAttempt(context.Background(), terminal); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.EgressAttemptRepository().List(context.Background(), egressaudit.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("rows = %d", len(page.Items))
	}
	got := page.Items[0].Attempt
	if normalizedEgressStart(got) != normalizedEgressStart(terminal) || got.CompletedAt() != terminal.CompletedAt().Truncate(time.Millisecond) || got.Outcome() != terminal.Outcome() || got.ErrorClass() != terminal.ErrorClass() || got.BytesOut() != terminal.BytesOut() || got.BytesIn() != terminal.BytesIn() || got.ByteCountsKnown() != terminal.ByteCountsKnown() {
		t.Fatalf("snapshot differs: %+v", got)
	}
	if err := writer.PutAttempt(context.Background(), egressaudit.Attempt{}); err == nil {
		t.Fatal("zero snapshot accepted")
	}
}

func TestEgressSnapshotDoesNotReplaceNativeControlTransaction(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	writer := store.EgressAttemptRepository().(egressaudit.AttemptStore)
	native := nativeAuditFixture(t, "native-snapshot")
	if err := writer.PutAttempt(context.Background(), native); err == nil {
		t.Fatal("native start accepted through optional snapshot writer")
	}
	page, err := store.EgressAttemptRepository().List(context.Background(), egressaudit.PageRequest{})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("native snapshot changed rows: %+v %v", page, err)
	}
}
