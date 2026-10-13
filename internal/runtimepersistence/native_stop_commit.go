package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Called only after the mutation matched the authenticated owner/capability
// and revision. Read-back proves a committed stop or a known rollback. Failure
// to prove either is a closed unknown outcome, never a generic error flag.
func commitNativeStop(ctx context.Context, db *sql.DB, transaction *sql.Tx, committer transactionCommitter, unknown error, stopped string, expectedRevision uint64, query string, args ...any) error {
	if committer == nil {
		committer = sqlTransactionCommitter{}
	}
	err := committer.Commit(transaction)
	if err == nil {
		return nil
	}
	_ = transaction.Rollback()
	proof, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	var state string
	var revision uint64
	readErr := db.QueryRowContext(proof, query, args...).Scan(&state, &revision)
	if readErr != nil || revision != expectedRevision {
		return errors.Join(unknown, err)
	}
	if state == stopped {
		return nil
	}
	// A readable original active state is independently a known non-commit.
	switch state {
	case "created", "attached", "expired", "active":
		return err
	}
	return errors.Join(unknown, err)
}
