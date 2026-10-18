package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// migrationWriter owns only transaction resources. The two typed migration
// runners retain their independent schema, value and default proofs.
type migrationWriter struct {
	database *sql.DB
	conn     *sql.Conn
	runner   schemaMigrationRunner
	begun    bool
	closed   bool
	closeErr error
}

func openMigrationWriter(ctx context.Context, source string, runner schemaMigrationRunner) (*migrationWriter, error) {
	// No journal_mode pragma: callers certify the source before opening a writer.
	database := sql.OpenDB(newSQLiteUpgradeConnector(source, DefaultBusyTimeout))
	database.SetMaxOpenConns(1)
	writer := &migrationWriter{database: database, runner: runner}
	var err error
	writer.conn, err = database.Conn(ctx)
	return writer, err
}

func (writer *migrationWriter) close() error {
	if !writer.closed {
		var connectionErr error
		if writer.conn != nil {
			closeConnection := writer.runner.closeConn
			if closeConnection == nil {
				closeConnection = (*sql.Conn).Close
			}
			connectionErr = closeConnection(writer.conn)
		}
		writer.closeErr = errors.Join(connectionErr, writer.runner.closeDatabasePool(writer.database))
		writer.closed = true
	}
	return writer.closeErr
}

func (writer *migrationWriter) commit(ctx context.Context) error {
	commit := writer.runner.commit
	if commit == nil {
		commit = commitSchemaMigration
	}
	if err := commit(ctx, writer.conn); err != nil {
		return err
	}
	writer.begun = false
	return nil
}

func (writer *migrationWriter) reconcile(ctx context.Context, timeout time.Duration, from int64, prove func(context.Context) (MigrationResult, error)) (MigrationResult, error) {
	return reconcileMigrationVerified(ctx, timeout, func(cleanup context.Context) (MigrationResult, error) {
		if writer.begun {
			_, _ = writer.conn.ExecContext(cleanup, "ROLLBACK")
		}
		// Both closes must positively succeed before considering source visibility.
		// A readable target revision cannot resolve a failed resource close.
		if err := writer.close(); err != nil {
			return MigrationResult{FromRevision: from}, ErrMigrationOutcomeUnknown
		}
		return prove(cleanup)
	})
}

func reconcileMigrationVerified(ctx context.Context, timeout time.Duration, prove func(context.Context) (MigrationResult, error)) (MigrationResult, error) {
	proof, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	result, err := prove(proof)
	if err != nil {
		return result, errors.Join(err, ctx.Err())
	}
	return result, nil
}
