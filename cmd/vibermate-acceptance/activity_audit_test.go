package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func TestExchangeAuditReaderIsReadOnlyAndFiltersCommittedExchanges(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	dataDirectory := filepath.Join(t.TempDir(), "data")
	store, err := runtimepersistence.Open(ctx, runtimepersistence.Options{
		DatabasePath:           filepath.Join(dataDirectory, exchangeAuditDatabaseName),
		BusyTimeout:            5 * time.Second,
		CommitReconcileTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := store.Shutdown(shutdownContext); err != nil {
			t.Error(err)
		}
	})
	repository := store.ActivityRepository()
	appendAuditActivity(t, repository, activity.Record{
		ID:         "activity-access",
		OccurredAt: time.Unix(1, 0).UTC(),
		Kind:       activity.KindAccessApplied,
		AccessID:   "Acc-001",
		SubjectID:  "Acc-001",
		Status:     activity.StatusSucceeded,
	})
	first := appendAuditActivity(t, repository, activity.Record{
		ID:         "activity-failed",
		OccurredAt: time.Unix(2, 0).UTC(),
		Kind:       activity.KindExchangeCompleted,
		AccessID:   "Acc-001",
		SubjectID:  "exchange-failed",
		Status:     activity.StatusFailed,
		ReasonCode: "provider_credential_unavailable",
	})
	second := appendAuditActivity(t, repository, activity.Record{
		ID:         "activity-succeeded",
		OccurredAt: time.Unix(3, 0).UTC(),
		Kind:       activity.KindExchangeCompleted,
		AccessID:   "Acc-001",
		SubjectID:  "exchange-succeeded",
		Status:     activity.StatusSucceeded,
	})
	otherAccess := appendAuditActivity(t, repository, activity.Record{
		ID:         "activity-other-access",
		OccurredAt: time.Unix(4, 0).UTC(),
		Kind:       activity.KindExchangeCompleted,
		AccessID:   "Acc-002",
		SubjectID:  "exchange-other-access",
		Status:     activity.StatusFailed,
		ReasonCode: "provider_credential_unavailable",
	})

	reader, err := openExchangeAuditReader(ctx, dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	})
	latest, err := reader.latestSequence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if latest != otherAccess.Sequence {
		t.Fatalf("latest Exchange sequence=%d want=%d", latest, otherAccess.Sequence)
	}
	terminals, err := reader.terminalsAfter(ctx, "Acc-001", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(terminals) != 2 || terminals[0] != (exchangeAuditRecord{
		Sequence:   first.Sequence,
		ExchangeID: first.SubjectID,
		AccessID:   first.AccessID,
		Status:     first.Status,
		ReasonCode: first.ReasonCode,
	}) {
		t.Fatalf("terminal Exchange records=%+v", terminals)
	}
	if terminals[1].Sequence != second.Sequence ||
		terminals[1].ExchangeID != second.SubjectID ||
		terminals[1].Status != activity.StatusSucceeded {
		t.Fatalf("terminal Exchange records=%+v", terminals)
	}
	if _, err := reader.connection.ExecContext(
		ctx,
		`CREATE TABLE acceptance_must_not_write (id INTEGER)`,
	); err == nil {
		t.Fatal("query-only Exchange audit accepted a write")
	}
}

func TestExchangeAuditReaderSeesOnlyCommittedWALRecordsAndSurvivesReopen(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	dataDirectory := filepath.Join(t.TempDir(), "data")
	databasePath := filepath.Join(dataDirectory, exchangeAuditDatabaseName)
	store, err := runtimepersistence.Open(ctx, runtimepersistence.Options{
		DatabasePath:           databasePath,
		BusyTimeout:            5 * time.Second,
		CommitReconcileTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := store.Shutdown(shutdownContext); err != nil {
			t.Error(err)
		}
	})
	repository := store.ActivityRepository()
	baseline := appendAuditActivity(t, repository, activity.Record{
		ID:         "activity-baseline",
		OccurredAt: time.Unix(10, 0).UTC(),
		Kind:       activity.KindExchangeCompleted,
		AccessID:   "Acc-001",
		SubjectID:  "exchange-baseline",
		Status:     activity.StatusSucceeded,
	})
	reader, err := openExchangeAuditReader(ctx, dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	})

	writer, err := sql.Open("sqlite", auditWriterDSN(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	})
	transaction, err := writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := transaction.ExecContext(
		ctx,
		`INSERT INTO runtime_activities (
		     activity_id, occurred_at_unix_ms, kind, access_id, subject_id,
		     status, reason_code, provider_status, provider_field,
		     client_field, client_path, transport_evidence_json
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, 0, '', '', '', NULL)`,
		"activity-committed",
		time.Unix(11, 0).UnixMilli(),
		activity.KindExchangeCompleted,
		"Acc-001",
		"exchange-committed",
		activity.StatusFailed,
		"provider_credential_unavailable",
	)
	if err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if latest, err := reader.latestSequence(ctx); err != nil || latest != baseline.Sequence {
		_ = transaction.Rollback()
		t.Fatalf("uncommitted latest=%d err=%v", latest, err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	expected := exchangeAuditRecord{
		Sequence:   sequence,
		ExchangeID: "exchange-committed",
		AccessID:   "Acc-001",
		Status:     activity.StatusFailed,
		ReasonCode: "provider_credential_unavailable",
	}
	if err := requireExchangeAuditRecord(ctx, reader, expected); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	shutdownContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := store.Shutdown(shutdownContext); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()

	reopened, err := openExchangeAuditReader(ctx, dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := requireExchangeAuditRecord(ctx, reopened, expected); err != nil {
		t.Fatal(err)
	}
}

func TestExchangeAuditReaderFailsClosedWhenDatabasePathChanges(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dataDirectory := filepath.Join(t.TempDir(), "data")
	databasePath := filepath.Join(dataDirectory, exchangeAuditDatabaseName)
	store, err := runtimepersistence.Open(ctx, runtimepersistence.Options{
		DatabasePath:           databasePath,
		BusyTimeout:            5 * time.Second,
		CommitReconcileTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := store.Shutdown(shutdownContext); err != nil {
			t.Error(err)
		}
	})
	appendAuditActivity(t, store.ActivityRepository(), activity.Record{
		ID:         "activity-original",
		OccurredAt: time.Unix(20, 0).UTC(),
		Kind:       activity.KindExchangeCompleted,
		AccessID:   "Acc-001",
		SubjectID:  "exchange-original",
		Status:     activity.StatusSucceeded,
	})
	shutdownContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := store.Shutdown(shutdownContext); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()

	reader, err := openExchangeAuditReader(ctx, dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	})
	originalBytes, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(databasePath, databasePath+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(databasePath, originalBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.latestSequence(ctx); err == nil {
		t.Fatal("Exchange audit accepted a replacement database path")
	}
}

func TestExchangeAuditReaderDoesNotReconnectARejectedPhysicalConnection(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	dataDirectory := filepath.Join(t.TempDir(), "data")
	store, err := runtimepersistence.Open(ctx, runtimepersistence.Options{
		DatabasePath:           filepath.Join(dataDirectory, exchangeAuditDatabaseName),
		BusyTimeout:            5 * time.Second,
		CommitReconcileTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := store.Shutdown(shutdownContext); err != nil {
			t.Error(err)
		}
	})
	appendAuditActivity(t, store.ActivityRepository(), activity.Record{
		ID:         "activity-connection",
		OccurredAt: time.Unix(21, 0).UTC(),
		Kind:       activity.KindExchangeCompleted,
		AccessID:   "Acc-001",
		SubjectID:  "exchange-connection",
		Status:     activity.StatusSucceeded,
	})
	reader, err := openExchangeAuditReader(ctx, dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := reader.connection.Raw(func(any) error {
		return driver.ErrBadConn
	}); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("reject physical connection error=%v", err)
	}
	if _, err := reader.latestSequence(ctx); err == nil {
		t.Fatal("Exchange audit reconnected after its physical connection was rejected")
	}
}

func TestExchangeAuditReaderRejectsMissingOrNonPrivateDatabase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	missingDirectory := filepath.Join(t.TempDir(), "missing")
	if err := os.Mkdir(missingDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := openExchangeAuditReader(ctx, missingDirectory); err == nil {
		t.Fatal("missing Exchange audit database was accepted")
	}
	if _, err := os.Stat(filepath.Join(missingDirectory, exchangeAuditDatabaseName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing database was created: %v", err)
	}

	dataDirectory := filepath.Join(t.TempDir(), "data")
	store, err := runtimepersistence.Open(ctx, runtimepersistence.Options{
		DatabasePath:           filepath.Join(dataDirectory, exchangeAuditDatabaseName),
		BusyTimeout:            5 * time.Second,
		CommitReconcileTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	shutdownContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := store.Shutdown(shutdownContext); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	if err := os.Chmod(filepath.Join(dataDirectory, exchangeAuditDatabaseName), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := openExchangeAuditReader(ctx, dataDirectory); err == nil {
		t.Fatal("non-private Exchange audit database was accepted")
	}
	if err := os.Chmod(filepath.Join(dataDirectory, exchangeAuditDatabaseName), 0o600); err != nil {
		t.Fatal(err)
	}

	linkDirectory := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(linkDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(dataDirectory, exchangeAuditDatabaseName),
		filepath.Join(linkDirectory, exchangeAuditDatabaseName),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := openExchangeAuditReader(ctx, linkDirectory); err == nil {
		t.Fatal("symbolic-link Exchange audit database was accepted")
	}
}

func appendAuditActivity(
	t *testing.T,
	repository activity.Repository,
	record activity.Record,
) activity.Record {
	t.Helper()
	if record.Kind == activity.KindExchangeCompleted {
		record.AccessName = "Acceptance Access"
		record.AccessRevision = 1
		record.SourceKind = activity.SourceSystemProxy
		record.SourceDisplayName = "ViberMate runtime"
		record.SourceRecognition = activity.SourceRecognitionUnknown
		record.IngressProfileID = "system-proxy"
		record.ConnectionID = "connection-" + record.SubjectID
	}
	stored, err := repository.Append(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func auditWriterDSN(databasePath string) string {
	return "file:" + databasePath +
		"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)&_dqs=false&_error_rc=true"
}
