package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

const captureRunColumns = `
	run_id,
	proxy_capability_hash,
	control_capability_hash,
	cwd,
	executable_label,
	client_catalog_revision,
	adapter_id,
	adapter_revision,
	adapter_version,
	adapter_install_shape,
	adapter_release_sha256,
	adapter_launch_recipe,
	adapter_features,
	process_id,
	state,
	observation,
	first_observed_at_unix_ms,
	created_at_unix_ms,
	expires_at_unix_ms,
	updated_at_unix_ms`

type captureRunRepository struct {
	database   *sql.DB
	operations *operationGate
}

var _ capturerun.Repository = (*captureRunRepository)(nil)

func newCaptureRunRepository(
	database *sql.DB,
	operations *operationGate,
) *captureRunRepository {
	return &captureRunRepository{database: database, operations: operations}
}

func (repository *captureRunRepository) Create(
	ctx context.Context,
	record capturerun.DurableRecord,
) error {
	if err := record.Validate(); err != nil {
		return err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	adapterID, adapterRevision, adapterVersion, installShape,
		releaseDigest, launchRecipe, features := captureRunAdapterColumns(
		record.Adapter,
	)
	_, err = repository.database.ExecContext(
		operation,
		`INSERT INTO capture_runs (
		     run_id,
		     proxy_capability_hash,
		     control_capability_hash,
		     cwd,
		     executable_label,
		     client_catalog_revision,
		     adapter_id,
		     adapter_revision,
		     adapter_version,
		     adapter_install_shape,
		     adapter_release_sha256,
		     adapter_launch_recipe,
		     adapter_features,
		     process_id,
		     state,
		     created_at_unix_ms,
		     expires_at_unix_ms,
		     updated_at_unix_ms
		 )
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID,
		record.ProxyCapabilityHash[:],
		record.ControlCapabilityHash[:],
		record.CWD,
		record.ExecutableLabel,
		int64(record.CatalogRevision),
		adapterID,
		adapterRevision,
		adapterVersion,
		installShape,
		releaseDigest,
		launchRecipe,
		features,
		record.ProcessID,
		string(record.State),
		toUnixMillis(record.CreatedAt),
		toUnixMillis(record.ExpiresAt),
		toUnixMillis(record.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert CaptureRun: %w", err)
	}
	return nil
}

func (repository *captureRunRepository) AuthorizeProxy(
	ctx context.Context,
	digest capturerun.CapabilityDigest,
	now time.Time,
) (capturerun.DurableRecord, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return capturerun.DurableRecord{}, err
	}
	defer finish()
	record, err := scanCaptureRun(repository.database.QueryRowContext(
		operation,
		`SELECT `+captureRunColumns+`
		 FROM capture_runs
		 WHERE proxy_capability_hash = ?
		   AND state IN ('created', 'attached')
		   AND expires_at_unix_ms > ?`,
		digest[:],
		toUnixMillis(now),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return capturerun.DurableRecord{}, capturerun.ErrCapabilityRejected
	}
	if err != nil {
		return capturerun.DurableRecord{}, fmt.Errorf("authorize CaptureRun proxy: %w", err)
	}
	// This is the only place that can honestly mark observation: an
	// authenticated proxy connection is the one signal that traffic actually
	// arrived through this run. The write is conditional so a later connection
	// cannot move the first-observed time.
	// The stored value is what a later read will see, so the value returned to
	// this caller is the stored one. Handing back an untruncated time would
	// make the same fact differ between the first call and every later one.
	storedAt := fromUnixMillis(toUnixMillis(now))
	observed, changed := record.WithObservedTraffic(storedAt)
	if !changed {
		return record, nil
	}
	if _, err := repository.database.ExecContext(
		operation,
		`UPDATE capture_runs
		    SET observation = 'observed',
		        first_observed_at_unix_ms = ?,
		        updated_at_unix_ms = ?
		  WHERE run_id = ?
		    AND observation = 'waiting_for_traffic'
		    AND state IN ('created', 'attached')`,
		toUnixMillis(storedAt),
		toUnixMillis(now),
		record.ID,
	); err != nil {
		return capturerun.DurableRecord{}, fmt.Errorf(
			"record CaptureRun observation: %w",
			err,
		)
	}
	return observed, nil
}

func (repository *captureRunRepository) Attach(
	ctx context.Context,
	runID string,
	digest capturerun.CapabilityDigest,
	processID int,
	now time.Time,
) (capturerun.DurableRecord, error) {
	return repository.updateAndRead(
		ctx,
		runID,
		digest,
		`UPDATE capture_runs
		 SET process_id = ?, state = 'attached', updated_at_unix_ms = ?
		 WHERE run_id = ?
		   AND control_capability_hash = ?
		   AND state IN ('created', 'attached')
		   AND expires_at_unix_ms > ?
		   AND (process_id = 0 OR process_id = ?)`,
		processID,
		toUnixMillis(now),
		runID,
		digest[:],
		toUnixMillis(now),
		processID,
	)
}

func (repository *captureRunRepository) Heartbeat(
	ctx context.Context,
	runID string,
	digest capturerun.CapabilityDigest,
	now time.Time,
	expiresAt time.Time,
) (capturerun.DurableRecord, error) {
	return repository.updateAndRead(
		ctx,
		runID,
		digest,
		`UPDATE capture_runs
		 SET expires_at_unix_ms = ?, updated_at_unix_ms = ?
		 WHERE run_id = ?
		   AND control_capability_hash = ?
		   AND state = 'attached'
		   AND expires_at_unix_ms > ?`,
		toUnixMillis(expiresAt),
		toUnixMillis(now),
		runID,
		digest[:],
		toUnixMillis(now),
	)
}

func (repository *captureRunRepository) Finish(
	ctx context.Context,
	runID string,
	digest capturerun.CapabilityDigest,
	now time.Time,
) error {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	result, err := repository.database.ExecContext(
		operation,
		`UPDATE capture_runs
		 SET state = 'finished', updated_at_unix_ms = ?
		 WHERE run_id = ?
		   AND control_capability_hash = ?
		   AND (
		       state = 'finished' OR
		       (state IN ('created', 'attached') AND expires_at_unix_ms > ?)
		   )`,
		toUnixMillis(now),
		runID,
		digest[:],
		toUnixMillis(now),
	)
	if err != nil {
		return fmt.Errorf("finish CaptureRun: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read CaptureRun finish result: %w", err)
	}
	if affected != 1 {
		return capturerun.ErrCapabilityRejected
	}
	return nil
}

func (repository *captureRunRepository) Recover(
	ctx context.Context,
	now time.Time,
) (capturerun.Recovery, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return capturerun.Recovery{}, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return capturerun.Recovery{}, fmt.Errorf("begin CaptureRun recovery: %w", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	result, err := transaction.ExecContext(
		operation,
		`UPDATE capture_runs
		 SET state = 'expired', updated_at_unix_ms = ?
		 WHERE state IN ('created', 'attached')
		   AND expires_at_unix_ms <= ?`,
		toUnixMillis(now),
		toUnixMillis(now),
	)
	if err != nil {
		return capturerun.Recovery{}, fmt.Errorf("expire recovered CaptureRuns: %w", err)
	}
	expired, err := result.RowsAffected()
	if err != nil {
		return capturerun.Recovery{}, fmt.Errorf("count expired CaptureRuns: %w", err)
	}
	var active int
	if err := transaction.QueryRowContext(
		operation,
		`SELECT COUNT(*)
		 FROM capture_runs
		 WHERE state IN ('created', 'attached')
		   AND expires_at_unix_ms > ?`,
		toUnixMillis(now),
	).Scan(&active); err != nil {
		return capturerun.Recovery{}, fmt.Errorf("count active CaptureRuns: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return capturerun.Recovery{}, fmt.Errorf("commit CaptureRun recovery: %w", err)
	}
	return capturerun.Recovery{
		ExpiredCount: int(expired),
		ActiveCount:  active,
	}, nil
}

func (repository *captureRunRepository) RevokeActive(
	ctx context.Context,
	now time.Time,
) (int, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer finish()
	result, err := repository.database.ExecContext(
		operation,
		`UPDATE capture_runs
		 SET state = 'revoked', updated_at_unix_ms = ?
		 WHERE state IN ('created', 'attached')`,
		toUnixMillis(now),
	)
	if err != nil {
		return 0, fmt.Errorf("revoke active CaptureRuns: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count revoked CaptureRuns: %w", err)
	}
	return int(affected), nil
}

func (repository *captureRunRepository) updateAndRead(
	ctx context.Context,
	runID string,
	_ capturerun.CapabilityDigest,
	statement string,
	arguments ...any,
) (capturerun.DurableRecord, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return capturerun.DurableRecord{}, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return capturerun.DurableRecord{}, fmt.Errorf("begin CaptureRun update: %w", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	result, err := transaction.ExecContext(operation, statement, arguments...)
	if err != nil {
		return capturerun.DurableRecord{}, fmt.Errorf("update CaptureRun: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return capturerun.DurableRecord{}, fmt.Errorf("read CaptureRun update result: %w", err)
	}
	if affected != 1 {
		return capturerun.DurableRecord{}, capturerun.ErrCapabilityRejected
	}
	record, err := scanCaptureRun(transaction.QueryRowContext(
		operation,
		`SELECT `+captureRunColumns+` FROM capture_runs WHERE run_id = ?`,
		runID,
	))
	if err != nil {
		return capturerun.DurableRecord{}, fmt.Errorf("read updated CaptureRun: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return capturerun.DurableRecord{}, fmt.Errorf("commit CaptureRun update: %w", err)
	}
	return record, nil
}

type captureRunScanner interface {
	Scan(...any) error
}

func scanCaptureRun(scanner captureRunScanner) (capturerun.DurableRecord, error) {
	var (
		record                             capturerun.DurableRecord
		proxyHash, controlHash             []byte
		releaseHash                        []byte
		catalogRevision, adapterRevision   int64
		adapterFeatures                    int64
		adapterID, adapterVersion          string
		adapterInstallShape, adapterRecipe string
		state                              string
		observation                        string
		firstObservedAt                    sql.NullInt64
		createdAt, expiresAt, updatedAtMS  int64
	)
	if err := scanner.Scan(
		&record.ID,
		&proxyHash,
		&controlHash,
		&record.CWD,
		&record.ExecutableLabel,
		&catalogRevision,
		&adapterID,
		&adapterRevision,
		&adapterVersion,
		&adapterInstallShape,
		&releaseHash,
		&adapterRecipe,
		&adapterFeatures,
		&record.ProcessID,
		&state,
		&observation,
		&firstObservedAt,
		&createdAt,
		&expiresAt,
		&updatedAtMS,
	); err != nil {
		return capturerun.DurableRecord{}, err
	}
	if len(proxyHash) != len(record.ProxyCapabilityHash) ||
		len(controlHash) != len(record.ControlCapabilityHash) {
		return capturerun.DurableRecord{}, errors.New("CaptureRun capability hash length is invalid")
	}
	copy(record.ProxyCapabilityHash[:], proxyHash)
	copy(record.ControlCapabilityHash[:], controlHash)
	record.Observation = capturerun.Observation(observation)
	if firstObservedAt.Valid {
		record.FirstObservedAt = fromUnixMillis(firstObservedAt.Int64)
	}
	if catalogRevision <= 0 ||
		adapterRevision < 0 ||
		adapterFeatures < 0 {
		return capturerun.DurableRecord{}, errors.New(
			"CaptureRun client evidence number is invalid",
		)
	}
	record.CatalogRevision = clientadapter.CatalogRevision(catalogRevision)
	if adapterID != "" {
		if len(releaseHash) != 32 ||
			adapterRevision == 0 {
			return capturerun.DurableRecord{}, errors.New(
				"CaptureRun adapter evidence is incomplete",
			)
		}
		record.Adapter = &clientadapter.Evidence{
			ID:              adapterID,
			Revision:        clientadapter.AdapterRevision(adapterRevision),
			Version:         adapterVersion,
			CatalogRevision: record.CatalogRevision,
			InstallShape:    clientadapter.InstallShape(adapterInstallShape),
			ReleaseSHA256:   hex.EncodeToString(releaseHash),
			LaunchRecipe:    clientadapter.LaunchRecipe(adapterRecipe),
			Features:        clientadapter.Feature(adapterFeatures),
		}
	} else if adapterRevision != 0 ||
		adapterVersion != "" ||
		adapterInstallShape != "" ||
		len(releaseHash) != 0 ||
		adapterRecipe != "" ||
		adapterFeatures != 0 {
		return capturerun.DurableRecord{}, errors.New(
			"CaptureRun generic client evidence is inconsistent",
		)
	}
	record.State = capturerun.State(state)
	record.CreatedAt = fromUnixMillis(createdAt)
	record.ExpiresAt = fromUnixMillis(expiresAt)
	record.UpdatedAt = fromUnixMillis(updatedAtMS)
	if err := record.Validate(); err != nil {
		return capturerun.DurableRecord{}, err
	}
	return record, nil
}

func captureRunAdapterColumns(
	evidence *clientadapter.Evidence,
) (
	string,
	int64,
	string,
	string,
	[]byte,
	string,
	int64,
) {
	if evidence == nil {
		return "", 0, "", "", []byte{}, "", 0
	}
	releaseDigest, _ := hex.DecodeString(evidence.ReleaseSHA256)
	return evidence.ID,
		int64(evidence.Revision),
		evidence.Version,
		string(evidence.InstallShape),
		releaseDigest,
		string(evidence.LaunchRecipe),
		int64(evidence.Features)
}
