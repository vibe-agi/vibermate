package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
)

const connectionRuleSelect = `SELECT
	    rule_id,
	    priority,
	    decision,
	    match_kind,
	    match_host,
	    match_port
	 FROM connection_rules`

type connectionRuleRepository struct {
	database   *sql.DB
	operations *operationGate
}

var _ connectionpolicy.Repository = (*connectionRuleRepository)(nil)

func newConnectionRuleRepository(
	database *sql.DB,
	operations *operationGate,
) *connectionRuleRepository {
	return &connectionRuleRepository{
		database:   database,
		operations: operations,
	}
}

func (repository *connectionRuleRepository) Load(
	ctx context.Context,
) (connectionpolicy.Snapshot, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return connectionpolicy.Snapshot{}, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(
		operation,
		&sql.TxOptions{ReadOnly: true},
	)
	if err != nil {
		return connectionpolicy.Snapshot{}, fmt.Errorf(
			"begin connection rule read: %w",
			err,
		)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	return repository.read(operation, transaction)
}

// Seed writes the shipped set exactly once. A second start finds rules already
// there and leaves them alone, so a person's edits are never replaced by the
// shipped placeholder.
func (repository *connectionRuleRepository) Seed(
	ctx context.Context,
	snapshot connectionpolicy.Snapshot,
	now time.Time,
) (connectionpolicy.Snapshot, error) {
	if _, err := snapshot.Compile(); err != nil {
		return connectionpolicy.Snapshot{}, err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return connectionpolicy.Snapshot{}, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return connectionpolicy.Snapshot{}, fmt.Errorf(
			"begin connection rule seed: %w",
			err,
		)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	stored, err := repository.read(operation, transaction)
	switch {
	case err == nil:
		return stored, nil
	case !errors.Is(err, connectionpolicy.ErrNoRuleSet):
		return connectionpolicy.Snapshot{}, err
	}
	if err := repository.write(operation, transaction, snapshot, now); err != nil {
		return connectionpolicy.Snapshot{}, err
	}
	if err := transaction.Commit(); err != nil {
		return connectionpolicy.Snapshot{}, fmt.Errorf(
			"commit connection rule seed: %w",
			err,
		)
	}
	return snapshot, nil
}

// Replace swaps the whole set in one commit. Rules are never changed one at a
// time: a set that would not construct is refused before anything is written.
func (repository *connectionRuleRepository) Replace(
	ctx context.Context,
	expectedRevision uint64,
	snapshot connectionpolicy.Snapshot,
	now time.Time,
) (connectionpolicy.Snapshot, error) {
	if _, err := snapshot.Compile(); err != nil {
		return connectionpolicy.Snapshot{}, err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return connectionpolicy.Snapshot{}, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return connectionpolicy.Snapshot{}, fmt.Errorf(
			"begin connection rule replace: %w",
			err,
		)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	stored, err := repository.read(operation, transaction)
	if err != nil {
		return connectionpolicy.Snapshot{}, err
	}
	if stored.Revision != expectedRevision {
		return stored, connectionpolicy.ErrRevisionConflict
	}
	if snapshot.Revision <= stored.Revision {
		return stored, fmt.Errorf(
			"%w: a replacement must advance the revision",
			connectionpolicy.ErrRevisionConflict,
		)
	}
	if _, err := transaction.ExecContext(
		operation,
		`DELETE FROM connection_rules`,
	); err != nil {
		return connectionpolicy.Snapshot{}, fmt.Errorf(
			"clear connection rules: %w",
			err,
		)
	}
	if err := repository.write(operation, transaction, snapshot, now); err != nil {
		return connectionpolicy.Snapshot{}, err
	}
	if err := transaction.Commit(); err != nil {
		return connectionpolicy.Snapshot{}, fmt.Errorf(
			"commit connection rule replace: %w",
			err,
		)
	}
	return snapshot, nil
}

func (repository *connectionRuleRepository) write(
	ctx context.Context,
	transaction *sql.Tx,
	snapshot connectionpolicy.Snapshot,
	now time.Time,
) error {
	if _, err := transaction.ExecContext(
		ctx,
		`INSERT INTO connection_rule_sets (id, revision, mode, updated_at_unix_ms)
		 VALUES (1, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE
		 SET revision = excluded.revision,
		     mode = excluded.mode,
		     updated_at_unix_ms = excluded.updated_at_unix_ms`,
		int64(snapshot.Revision),
		string(snapshot.Mode),
		toUnixMillis(now.UTC()),
	); err != nil {
		return fmt.Errorf("write connection rule set: %w", err)
	}
	for _, rule := range snapshot.Rules {
		if _, err := transaction.ExecContext(
			ctx,
			`INSERT INTO connection_rules (
			     rule_id, priority, decision,
			     match_kind, match_host, match_port
			 ) VALUES (?, ?, ?, ?, ?, ?)`,
			rule.ID,
			int64(rule.Priority),
			string(rule.Decision),
			string(rule.Match.Kind),
			rule.Match.Host,
			int64(rule.Match.Port),
		); err != nil {
			return fmt.Errorf("write connection rule %q: %w", rule.ID, err)
		}
	}
	return nil
}

func (repository *connectionRuleRepository) read(
	ctx context.Context,
	transaction *sql.Tx,
) (connectionpolicy.Snapshot, error) {
	var (
		revision int64
		mode     string
	)
	err := transaction.QueryRowContext(
		ctx,
		`SELECT revision, mode FROM connection_rule_sets WHERE id = 1`,
	).Scan(&revision, &mode)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return connectionpolicy.Snapshot{}, connectionpolicy.ErrNoRuleSet
	case err != nil:
		return connectionpolicy.Snapshot{}, fmt.Errorf(
			"read connection rule set: %w",
			err,
		)
	}
	rows, err := transaction.QueryContext(ctx, connectionRuleSelect)
	if err != nil {
		return connectionpolicy.Snapshot{}, fmt.Errorf(
			"read connection rules: %w",
			err,
		)
	}
	defer func() {
		_ = rows.Close()
	}()
	snapshot := connectionpolicy.Snapshot{
		Revision: uint64(revision),
		Mode:     connectionpolicy.Mode(mode),
	}
	for rows.Next() {
		var (
			rule      connectionpolicy.Rule
			priority  int64
			decision  string
			matchKind string
			host      string
			port      int64
		)
		if err := rows.Scan(
			&rule.ID,
			&priority,
			&decision,
			&matchKind,
			&host,
			&port,
		); err != nil {
			return connectionpolicy.Snapshot{}, err
		}
		rule.Priority = uint32(priority)
		rule.Decision = connectionpolicy.Decision(decision)
		rule.Match = connectionpolicy.Match{
			Kind: connectionpolicy.MatchKind(matchKind),
			Host: host,
			Port: uint16(port),
		}
		snapshot.Rules = append(snapshot.Rules, rule)
	}
	if err := rows.Err(); err != nil {
		return connectionpolicy.Snapshot{}, err
	}
	if _, err := snapshot.Compile(); err != nil {
		return connectionpolicy.Snapshot{}, err
	}
	return snapshot, nil
}
