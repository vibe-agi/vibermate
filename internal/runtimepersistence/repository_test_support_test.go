package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

var errInjectedCommitResult = errors.New("injected commit result error")

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

func (committer commitThenCloseAdmission) Commit(transaction *sql.Tx) error {
	if err := transaction.Commit(); err != nil {
		return err
	}
	committer.operations.closeAdmission()
	return errInjectedCommitResult
}

func shutdownTestStore(t testing.TB, store *Store) {
	t.Helper()
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown store: %v", err)
	}
}
