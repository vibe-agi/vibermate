package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
)

// The observation writer's TEMP privacy certificate belongs to one physical
// connection. database/sql discards a canceled Tx on drivers without both
// SessionResetter and Validator, even after a successful rollback. Own that
// rollback synchronously instead. Acquisition and data statements still use
// the operation context; only the already-owned transaction lifetime is detached.
// The operation gate remains active until the caller's deferred Rollback returns.
type observationTransaction struct {
	*sql.Tx
	conn      *sql.Conn
	operation context.Context
}

func beginObservationTransaction(ctx context.Context, db *sql.DB) (*observationTransaction, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		conn.Close()
		return nil, err
	}
	tx, err := conn.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &observationTransaction{Tx: tx, conn: conn, operation: ctx}, nil
}
func (tx *observationTransaction) Rollback() error {
	err := tx.Tx.Rollback()
	if tx.conn != nil {
		err = errors.Join(err, tx.conn.Close())
	}
	return err
}
func (tx *observationTransaction) Commit() error {
	if err := tx.operation.Err(); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	err := tx.Tx.Commit()
	if tx.conn != nil {
		err = errors.Join(err, tx.conn.Close())
	}
	return err
}

// Maintenance already owns this connection and its synchronization policy.
// The transaction returns before that owner restores policy or closes it.
func beginObservationOnConn(ctx context.Context, conn *sql.Conn, opts *sql.TxOptions) (*observationTransaction, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tx, err := conn.BeginTx(context.WithoutCancel(ctx), opts)
	if err != nil {
		return nil, err
	}
	return &observationTransaction{Tx: tx, operation: ctx}, nil
}
