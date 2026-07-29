package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/vibe-agi/vibermate/internal/access"
)

var errInjectedCommitResult = errors.New("injected commit result error")

func TestAccessMigrationUpgradesFoundationSchema(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatalf("create foundation data directory: %v", err)
	}
	database := sql.OpenDB(newSQLiteConnector(databasePath, DefaultBusyTimeout))
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	migrations, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatalf("open embedded migrations: %v", err)
	}
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		database,
		migrations,
		goose.WithSlog(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	if err != nil {
		t.Fatalf("construct foundation migration provider: %v", err)
	}
	if _, err := provider.UpTo(context.Background(), 1); err != nil {
		t.Fatalf("create schema revision 1 fixture: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close schema revision 1 fixture: %v", err)
	}

	store := openTestStore(t, databasePath)
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown upgraded store: %v", err)
		}
	}()
	state, err := store.SchemaStateReader().ReadSchemaState(context.Background())
	if err != nil {
		t.Fatalf("read upgraded schema state: %v", err)
	}
	if state.Revision != 2 {
		t.Fatalf("upgraded schema revision = %d, want 2", state.Revision)
	}
	mutation := accessMutation(t, "access-upgraded", 0, "Upgraded")
	result, err := store.AccessRepository().CompareAndSwap(
		context.Background(),
		mutation,
	)
	if err != nil || result.Outcome != access.CommitOutcomeCommitted {
		t.Fatalf("write Access after schema upgrade result=%+v err=%v", result, err)
	}
}

func TestAccessRepositoryReconcilesCommitThatReturnedAnError(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown store: %v", err)
		}
	}()
	store.accessRepo.committer = commitThenError{}

	mutation := accessMutation(t, "access-committed", 0, "Committed")
	result, err := store.AccessRepository().CompareAndSwap(
		context.Background(),
		mutation,
	)
	if err != nil {
		t.Fatalf("reconciled commit returned an error: %v", err)
	}
	if result.Outcome != access.CommitOutcomeCommitted ||
		result.Record != mutation.Candidate {
		t.Fatalf("reconciled commit result = %+v", result)
	}
	records, err := store.AccessRepository().LoadAll(context.Background())
	if err != nil {
		t.Fatalf("load reconciled commit: %v", err)
	}
	if len(records) != 1 || records[0] != mutation.Candidate {
		t.Fatalf("durable reconciled records = %+v", records)
	}
}

func TestAccessManagerPublishesReconciledCommittedOutcome(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown store: %v", err)
		}
	}()
	projection := access.NewSnapshotProjection()
	manager, err := access.NewManager(
		context.Background(),
		store.AccessRepository(),
		projection,
	)
	if err != nil {
		t.Fatalf("construct Access manager: %v", err)
	}
	defer func() {
		if err := manager.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown Access manager: %v", err)
		}
	}()
	store.accessRepo.committer = commitThenError{}

	mutation := accessMutation(t, "access-manager-committed", 0, "Committed")
	result, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		AccessID:         mutation.Candidate.AccessID,
		ExpectedRevision: mutation.ExpectedRevision,
		Binding:          mutation.Candidate.Binding,
	})
	if err != nil || result.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("reconciled manager write result=%+v err=%v", result, err)
	}
	snapshot, err := manager.ResolveAccess(mutation.Candidate.AccessID)
	if err != nil {
		t.Fatalf("resolve reconciled manager write: %v", err)
	}
	if snapshot.Revision() != 1 || snapshot.Binding().Name != "Committed" {
		t.Fatalf("reconciled manager snapshot: revision=%d binding=%+v",
			snapshot.Revision(), snapshot.Binding())
	}
}

func TestAccessRepositoryReconcilesDefinitelyUncommittedError(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown store: %v", err)
		}
	}()
	initial := accessMutation(t, "access-not-committed", 0, "Revision one")
	if result, err := store.AccessRepository().CompareAndSwap(
		context.Background(),
		initial,
	); err != nil || result.Outcome != access.CommitOutcomeCommitted {
		t.Fatalf("create initial Access result=%+v err=%v", result, err)
	}

	store.accessRepo.committer = rollbackThenError{}
	update := accessMutation(t, "access-not-committed", 1, "Revision two")
	result, err := store.AccessRepository().CompareAndSwap(
		context.Background(),
		update,
	)
	if !errors.Is(err, errInjectedCommitResult) {
		t.Fatalf("uncommitted result error = %v", err)
	}
	if result.Outcome != access.CommitOutcomeNotCommitted {
		t.Fatalf("uncommitted result = %+v", result)
	}
	records, loadErr := store.AccessRepository().LoadAll(context.Background())
	if loadErr != nil {
		t.Fatalf("load after uncommitted result: %v", loadErr)
	}
	if len(records) != 1 || records[0] != initial.Candidate {
		t.Fatalf("uncommitted write changed durable state: %+v", records)
	}
}

func TestAccessManagerDoesNotPublishReconciledUncommittedOutcome(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown store: %v", err)
		}
	}()
	manager, err := access.NewManager(
		context.Background(),
		store.AccessRepository(),
		access.NewSnapshotProjection(),
	)
	if err != nil {
		t.Fatalf("construct Access manager: %v", err)
	}
	defer func() {
		if err := manager.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown Access manager: %v", err)
		}
	}()
	accessID, err := access.NewAccessID("access-manager-not-committed")
	if err != nil {
		t.Fatalf("construct Access ID: %v", err)
	}
	store.accessRepo.committer = rollbackThenError{}
	result, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		AccessID:         accessID,
		ExpectedRevision: 0,
		Binding:          access.Binding{Name: "Not committed"},
	})
	if result.Outcome != access.WriteOutcomeNotCommitted ||
		!errors.Is(err, access.ErrWriteNotCommitted) {
		t.Fatalf("uncommitted manager result=%+v err=%v", result, err)
	}
	if _, err := manager.ResolveAccess(accessID); !errors.Is(
		err,
		access.ErrAccessNotConfigured,
	) {
		t.Fatalf("uncommitted manager write published a snapshot: %v", err)
	}
}

func TestAccessRepositoryReturnsIndeterminateWhenReconciliationIsUnavailable(
	t *testing.T,
) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openTestStore(t, databasePath)
	store.accessRepo.committer = commitThenCloseAdmission{
		operations: store.operations,
	}
	mutation := accessMutation(t, "access-indeterminate", 0, "Committed")
	result, err := store.AccessRepository().CompareAndSwap(
		context.Background(),
		mutation,
	)
	if err == nil || result.Outcome != access.CommitOutcomeIndeterminate {
		t.Fatalf("indeterminate commit result=%+v err=%v", result, err)
	}
	if loadResult, loadErr := store.AccessRepository().LoadAll(
		context.Background(),
	); !errors.Is(loadErr, ErrStoreClosing) || loadResult != nil {
		t.Fatalf("closed operation gate load=%+v err=%v", loadResult, loadErr)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown indeterminate store: %v", err)
	}

	reopened := openTestStore(t, databasePath)
	defer func() {
		if err := reopened.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown reopened store: %v", err)
		}
	}()
	records, err := reopened.AccessRepository().LoadAll(context.Background())
	if err != nil {
		t.Fatalf("load durable indeterminate commit after restart: %v", err)
	}
	if len(records) != 1 || records[0] != mutation.Candidate {
		t.Fatalf("recovered indeterminate commit = %+v", records)
	}
}

func TestAccessManagerReturnsTypedIndeterminateOutcomeAndRestartRecovers(
	t *testing.T,
) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openTestStore(t, databasePath)
	manager, err := access.NewManager(
		context.Background(),
		store.AccessRepository(),
		access.NewSnapshotProjection(),
	)
	if err != nil {
		t.Fatalf("construct Access manager: %v", err)
	}
	accessID, err := access.NewAccessID("access-manager-indeterminate")
	if err != nil {
		t.Fatalf("construct Access ID: %v", err)
	}
	created, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		AccessID:         accessID,
		ExpectedRevision: 0,
		Binding:          access.Binding{Name: "Revision one"},
	})
	if err != nil || created.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("create initial Access result=%+v err=%v", created, err)
	}
	oldHandle, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve initial Access: %v", err)
	}

	store.accessRepo.committer = commitThenCloseAdmission{
		operations: store.operations,
	}
	result, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		AccessID:         accessID,
		ExpectedRevision: 1,
		Binding:          access.Binding{Name: "Durably ambiguous revision two"},
	})
	if result.Outcome != access.WriteOutcomeIndeterminate ||
		!errors.Is(err, access.ErrCommitOutcomeUnknown) {
		t.Fatalf("indeterminate manager result=%+v err=%v", result, err)
	}
	var failure *access.Failure
	if !errors.As(err, &failure) ||
		failure.Code != access.ReasonCommitOutcomeUnknown {
		t.Fatalf("indeterminate manager failure = %v", err)
	}
	if _, err := manager.ResolveAccess(accessID); !errors.Is(
		err,
		access.ErrProjectionUnavailable,
	) {
		t.Fatalf("indeterminate manager served a stale snapshot: %v", err)
	} else {
		var projectionFailure *access.Failure
		if !errors.As(err, &projectionFailure) ||
			projectionFailure.Code != access.ReasonProjectionUnavailable {
			t.Fatalf("poisoned resolver failure = %v", err)
		}
	}
	if oldHandle.Revision() != 1 ||
		oldHandle.Binding().Name != "Revision one" {
		t.Fatalf("indeterminate write changed old handle: revision=%d binding=%+v",
			oldHandle.Revision(), oldHandle.Binding())
	}
	health := manager.ProjectionHealth()
	if health.State != access.ProjectionStateUnavailable ||
		health.UnavailableAccessCount != 1 {
		t.Fatalf("indeterminate projection health = %+v", health)
	}
	retry, retryErr := manager.WriteAccess(context.Background(), access.WriteCommand{
		AccessID:         accessID,
		ExpectedRevision: 2,
		Binding:          access.Binding{Name: "Rejected while unavailable"},
	})
	if retry.Outcome != access.WriteOutcomeNotCommitted ||
		!errors.Is(retryErr, access.ErrProjectionUnavailable) {
		t.Fatalf("unavailable projection write result=%+v err=%v", retry, retryErr)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown Access manager: %v", err)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown indeterminate store: %v", err)
	}

	reopened := openTestStore(t, databasePath)
	defer func() {
		if err := reopened.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown reopened store: %v", err)
		}
	}()
	recovered, err := access.NewManager(
		context.Background(),
		reopened.AccessRepository(),
		access.NewSnapshotProjection(),
	)
	if err != nil {
		t.Fatalf("recover Access manager: %v", err)
	}
	defer func() {
		if err := recovered.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown recovered Access manager: %v", err)
		}
	}()
	snapshot, err := recovered.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve recovered indeterminate commit: %v", err)
	}
	if snapshot.Revision() != 2 ||
		snapshot.Binding().Name != "Durably ambiguous revision two" {
		t.Fatalf("recovered indeterminate snapshot: revision=%d binding=%+v",
			snapshot.Revision(), snapshot.Binding())
	}
	if health := recovered.ProjectionHealth(); health.State != access.ProjectionStateHealthy ||
		health.UnavailableAccessCount != 0 {
		t.Fatalf("recovered indeterminate projection health = %+v", health)
	}
}

func TestAccessRecoveryRejectsInvalidDurableContent(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown store: %v", err)
		}
	}()
	if _, err := store.database.ExecContext(
		context.Background(),
		`PRAGMA ignore_check_constraints = ON`,
	); err != nil {
		t.Fatalf("enable corruption fixture: %v", err)
	}
	if _, err := store.database.ExecContext(
		context.Background(),
		`INSERT INTO access_bindings (access_id, revision, name, description)
		 VALUES ('access-corrupt', 1, '', '')`,
	); err != nil {
		t.Fatalf("insert corruption fixture: %v", err)
	}

	if _, err := store.AccessRepository().LoadAll(context.Background()); !errors.Is(
		err,
		access.ErrInvalidRepositoryState,
	) {
		t.Fatalf("invalid durable Access was accepted: %v", err)
	}
}

type commitThenError struct{}

func (commitThenError) Commit(transaction *sql.Tx) error {
	if err := transaction.Commit(); err != nil {
		return err
	}
	return errInjectedCommitResult
}

type rollbackThenError struct{}

func (rollbackThenError) Commit(transaction *sql.Tx) error {
	if err := transaction.Rollback(); err != nil {
		return err
	}
	return errInjectedCommitResult
}

type commitThenCloseAdmission struct {
	operations *operationGate
}

func (c commitThenCloseAdmission) Commit(transaction *sql.Tx) error {
	if err := transaction.Commit(); err != nil {
		return err
	}
	c.operations.closeAdmission()
	return errInjectedCommitResult
}

func accessMutation(
	t *testing.T,
	accessIDText string,
	expected access.Revision,
	name string,
) access.Mutation {
	t.Helper()
	accessID, err := access.NewAccessID(accessIDText)
	if err != nil {
		t.Fatalf("construct Access ID: %v", err)
	}
	return access.Mutation{
		ExpectedRevision: expected,
		Candidate: access.Record{
			AccessID: accessID,
			Revision: expected + 1,
			Binding:  access.Binding{Name: name},
		},
	}
}

func TestAccessRepositoryCancellationBeforeCommitDoesNotPublishDurableState(
	t *testing.T,
) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown store: %v", err)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := store.AccessRepository().CompareAndSwap(
		ctx,
		accessMutation(t, "access-cancelled", 0, "Cancelled"),
	)
	if !errors.Is(err, context.Canceled) ||
		result.Outcome != access.CommitOutcomeNotCommitted {
		t.Fatalf("cancelled write result=%+v err=%v", result, err)
	}
	verifyContext, cancelVerify := context.WithTimeout(context.Background(), time.Second)
	defer cancelVerify()
	records, loadErr := store.AccessRepository().LoadAll(verifyContext)
	if loadErr != nil {
		t.Fatalf("load after cancelled write: %v", loadErr)
	}
	if len(records) != 0 {
		t.Fatalf("cancelled write persisted records: %+v", records)
	}
}
