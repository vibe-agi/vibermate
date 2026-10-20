package runtimepersistence

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

// An ordinary read-only SQLite attachment can create an empty WAL/SHM beside
// a checkpointed WAL-mode archive. It must not invalidate every subsequent
// history reader or turn actual retained history into an empty result.
func TestInspectionReadersAcceptEmptyWALCreatedByReadOnlyAttachment(t *testing.T) {
	f, usage, archive, before := checkpointedWALInspectionFixture(t)
	reader := newActivityInspectionTestReader(t, f)
	for pass := range 2 {
		summary, err := reader.SummarizeExchanges(t.Context(), activity.SummaryScope{CaptureRunID: "audit-a"})
		if err != nil || summary.Requests != 1 || summary.Succeeded != 1 {
			t.Errorf("read-only summary pass %d lost archived history: requests=%d succeeded=%d err=%v", pass, summary.Requests, summary.Succeeded, err)
		}
		_, calls, err := usage.scan(t)
		if err != nil || calls != 1 {
			t.Errorf("read-only usage pass %d lost archived usage: calls=%d err=%v", pass, calls, err)
		}
	}
	after, err := os.ReadFile(archive)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("read-only attachment changed archived database bytes: %v", err)
	}
	info, err := os.Stat(archive + "-wal")
	if err != nil || info.Size() != 0 {
		t.Fatalf("fixture did not create the zero-byte WAL transition: info=%v err=%v", info, err)
	}
}

func TestInspectionEmptyWALDoesNotAdmitDatabaseReplacementOrCommittedWAL(t *testing.T) {
	for _, change := range []string{"database replacement", "committed WAL"} {
		t.Run(change, func(t *testing.T) {
			f, _, archive, _ := checkpointedWALInspectionFixture(t)
			reader := newActivityInspectionTestReader(t, f)
			if _, err := reader.SummarizeExchanges(t.Context(), activity.SummaryScope{CaptureRunID: "audit-a"}); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "database replacement":
				data, err := os.ReadFile(archive)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(archive+".replacement", data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(archive+".replacement", archive); err != nil {
					t.Fatal(err)
				}
			case "committed WAL":
				db := addInspectionWALFrame(t, archive)
				defer db.Close()
			}
			if _, err := reader.SummarizeExchanges(t.Context(), activity.SummaryScope{CaptureRunID: "audit-a"}); !errors.Is(err, ErrInspectionChanged) {
				t.Fatalf("unexpected archive %s was accepted: %v", change, err)
			}
		})
	}
}

func TestInspectionRetainedNonemptyWALMutationStillInvalidatesTheSource(t *testing.T) {
	f, _, archive, _ := checkpointedWALInspectionFixture(t)
	db := addInspectionWALFrame(t, archive)
	defer db.Close()
	reader := newActivityInspectionTestReader(t, f)
	if _, err := reader.SummarizeExchanges(t.Context(), activity.SummaryScope{CaptureRunID: "audit-a"}); err != nil {
		t.Fatal(err)
	}
	wal := archive + "-wal"
	info, err := os.Stat(wal)
	if err != nil || info.Size() == 0 {
		t.Fatalf("retained WAL fixture: %v %v", info, err)
	}
	if err := os.Chtimes(wal, info.ModTime(), info.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.SummarizeExchanges(t.Context(), activity.SummaryScope{CaptureRunID: "audit-a"}); !errors.Is(err, ErrInspectionChanged) {
		t.Fatalf("nonempty WAL mutation was ignored: %v", err)
	}
}

func TestInspectionEmptyWALDoesNotBypassMaintenanceGeneration(t *testing.T) {
	f, _, _, _ := checkpointedWALInspectionFixture(t)
	reader := newActivityInspectionTestReader(t, f)
	if _, err := reader.SummarizeExchanges(t.Context(), activity.SummaryScope{CaptureRunID: "audit-a"}); err != nil {
		t.Fatal(err)
	}
	token, err := reader.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	maintenance, err := f.reader.catalog.BeginMaintenance(t.Context(), token.Sources.Generation+1)
	if err != nil {
		t.Fatal(err)
	}
	if err := maintenance.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Check(t.Context(), token); !errors.Is(err, ErrInspectionChanged) {
		t.Fatalf("empty-WAL handling accepted a retired generation: %v", err)
	}
	summary, err := reader.SummarizeExchanges(t.Context(), activity.SummaryScope{CaptureRunID: "audit-a"})
	if err != nil || summary.Requests != 1 {
		t.Fatalf("fresh maintenance generation lost history: %+v %v", summary, err)
	}
}

func checkpointedWALInspectionFixture(t *testing.T) (*egressInspectionFixture, *usageInspectionFixture, string, []byte) {
	t.Helper()
	f := newEgressInspectionFixture(t, 3)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	seedFrozenV1Activity(t, &Store{database: f.legacy}, inspectionActivity("wal-archived-event", "wal-archived-exchange", activity.KindExchangeCompleted, now.Add(-time.Hour)))
	usage := &usageInspectionFixture{current: f.current, control: f.control, legacy: f.legacy, now: now, user: runtimeuser.UserID("user.AAAAAAAAAAAAAAAAAAAAAAAAAAA")}
	value := usage.value("wal-archived-exchange", "fixture-profile")
	value.CaptureRunID = "audit-a"
	usage.put(t, false, value, now.Add(24*time.Hour), 0)
	usage.reader = NewUsageInspectionReader(f.reader.catalog, f.control)
	if _, err := f.legacy.ExecContext(t.Context(), `PRAGMA journal_mode=WAL;PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if err := f.legacy.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(f.reader.catalog.root, f.reader.catalog.sources[0].RelativePath)
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(archive + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("checkpointed archive retained %s before inspection: %v", suffix, err)
		}
	}
	before, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	return f, usage, archive, before
}

func addInspectionWALFrame(t *testing.T, archive string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", archive)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), `PRAGMA wal_autocheckpoint=0;INSERT INTO code_library_collections VALUES('unexpected-wal-write','Synthetic changed archive')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	info, err := os.Stat(archive + "-wal")
	if err != nil || info.Size() == 0 {
		db.Close()
		t.Fatalf("committed WAL missing: %v %v", info, err)
	}
	return db
}
