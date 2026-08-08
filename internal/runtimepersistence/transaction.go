package runtimepersistence

import "database/sql"

type transactionCommitter interface {
	Commit(*sql.Tx) error
}

type sqlTransactionCommitter struct{}

func (sqlTransactionCommitter) Commit(transaction *sql.Tx) error {
	return transaction.Commit()
}
