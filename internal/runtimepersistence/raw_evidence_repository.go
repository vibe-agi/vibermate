package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

const rawEvidenceColumns = `
  envelope_id, writer_id, watermark, layer,
  scope_kind, scope_id, exchange_id, connection_id, attempt_id,
	  environment_id, environment_revision, environment_digest,
	  client_endpoint_id, client_endpoint_revision,
	  upstream_endpoint_id, upstream_endpoint_revision,
	  protocol_plan_id, protocol_plan_revision,
  route_id, route_revision,
  account_id, account_revision, credential_epoch,
  observed_at_unix_ms, expires_at_unix_ms,
  method, status_code, scheme, authority, path, raw_query,
  content_type, content_encoding, representation, canonicalization,
  header_count, trailer_count, body_bytes, body_sha256,
  digest_scope, payload_state, payload_reason, redacted_credential_fields,
  payload_metadata, stored_body_digest`

type rawEvidenceRepository struct {
	database   *sql.DB
	operations *operationGate
}

var _ rawevidence.Repository = (*rawEvidenceRepository)(nil)

func newRawEvidenceRepository(
	database *sql.DB,
	operations *operationGate,
) *rawEvidenceRepository {
	return &rawEvidenceRepository{database: database, operations: operations}
}

func (repository *rawEvidenceRepository) AppendBatch(
	ctx context.Context,
	records []rawevidence.StoredEnvelope,
	now time.Time,
) error {
	if len(records) == 0 || now.IsZero() {
		return errors.New("raw evidence batch is empty")
	}
	for _, record := range records {
		if err := record.Validate(); err != nil {
			return err
		}
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return fmt.Errorf("begin raw evidence batch: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	statement, err := transaction.PrepareContext(
		operation,
		`INSERT INTO runtime_raw_evidence_envelopes (`+rawEvidenceColumns+`)
		 VALUES (`+rawEvidencePlaceholders+`)`,
	)
	if err != nil {
		return fmt.Errorf("prepare raw evidence append: %w", err)
	}
	defer statement.Close()
	expired, err := transaction.ExecContext(
		operation,
		`DELETE FROM runtime_raw_evidence_envelopes
		 WHERE expires_at_unix_ms <= ?`,
		toUnixMillis(now.UTC()),
	)
	if err != nil {
		return fmt.Errorf("purge expired raw evidence: %w", err)
	}
	releasedEnvelopes, err := expired.RowsAffected()
	if err != nil {
		return fmt.Errorf("read purged raw evidence count: %w", err)
	}
	for _, record := range records {
		storedBodyDigest, bodyErr := storeEvidenceBody(
			operation, transaction, record.Body,
		)
		if bodyErr != nil {
			return bodyErr
		}
		if _, err := statement.ExecContext(
			operation, rawEvidenceArguments(record, storedBodyDigest)...,
		); err != nil {
			return fmt.Errorf("append raw evidence envelope: %w", err)
		}
	}
	// Reachability is only recomputed when an envelope actually went away.
	// Running it on every batch would scan every body and every envelope for
	// each commit, which is the shape of cost this store exists to avoid.
	if releasedEnvelopes > 0 {
		if err := purgeUnreferencedEvidenceBytes(operation, transaction); err != nil {
			return err
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit raw evidence batch: %w", err)
	}
	committed = true
	return nil
}

func (repository *rawEvidenceRepository) GetEnvelope(
	ctx context.Context,
	envelopeID string,
) (rawevidence.StoredEnvelope, error) {
	if envelopeID == "" || len(envelopeID) > rawevidence.MaxIdentityBytes {
		return rawevidence.StoredEnvelope{}, errors.New("raw evidence envelope identity is invalid")
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return rawevidence.StoredEnvelope{}, err
	}
	defer finish()
	record, storedBodyDigest, err := scanRawEvidence(
		repository.database.QueryRowContext(
			operation,
			`SELECT `+rawEvidenceColumns+`
			 FROM runtime_raw_evidence_envelopes WHERE envelope_id = ?`,
			envelopeID,
		),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return rawevidence.StoredEnvelope{}, rawevidence.ErrEnvelopeNotFound
	}
	if err != nil {
		return rawevidence.StoredEnvelope{}, err
	}
	return attachEvidenceBody(
		operation, repository.database, record, storedBodyDigest,
	)
}

func (repository *rawEvidenceRepository) AppendRevealAudit(
	ctx context.Context,
	audit rawevidence.RevealAudit,
) error {
	if err := audit.Validate(); err != nil {
		return err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	_, err = repository.database.ExecContext(
		operation,
		`INSERT INTO runtime_raw_evidence_reveal_audits(
		   envelope_id, exchange_id, actor_id, outcome,
		   occurred_at_unix_ms
		 ) VALUES (?, ?, ?, ?, ?)`,
		audit.EnvelopeID, audit.ExchangeID, audit.ActorID,
		string(audit.Outcome), toUnixMillis(audit.OccurredAt.UTC()),
	)
	if err != nil {
		return fmt.Errorf("append raw evidence reveal audit: %w", err)
	}
	return nil
}

func (repository *rawEvidenceRepository) BeginWriterSession(
	ctx context.Context,
	session rawevidence.WriterSession,
) (rawevidence.Recovery, error) {
	if err := session.Validate(); err != nil {
		return rawevidence.Recovery{}, err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return rawevidence.Recovery{}, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return rawevidence.Recovery{}, fmt.Errorf("begin raw evidence recovery: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	now := session.StartedAt.UTC()
	var unclean, maximumLossMillis int64
	if err := transaction.QueryRowContext(
		operation,
		`SELECT COUNT(*), COALESCE(MAX(maximum_unflushed_ms), 0)
		 FROM runtime_raw_evidence_writer_sessions WHERE state = 'open'`,
	).Scan(&unclean, &maximumLossMillis); err != nil {
		return rawevidence.Recovery{}, fmt.Errorf("inspect raw evidence recovery: %w", err)
	}
	if _, err := transaction.ExecContext(
		operation,
		`UPDATE runtime_raw_evidence_writer_sessions
		 SET state = 'recovered_unclean', ended_at_unix_ms = ?
		 WHERE state = 'open'`,
		toUnixMillis(now),
	); err != nil {
		return rawevidence.Recovery{}, fmt.Errorf("mark unclean raw evidence writers: %w", err)
	}
	result, err := transaction.ExecContext(
		operation,
		`DELETE FROM runtime_raw_evidence_envelopes
		 WHERE expires_at_unix_ms <= ?`,
		toUnixMillis(now),
	)
	if err != nil {
		return rawevidence.Recovery{}, fmt.Errorf("recover expired raw evidence: %w", err)
	}
	purged, err := result.RowsAffected()
	if err != nil {
		return rawevidence.Recovery{}, fmt.Errorf("read raw evidence purge result: %w", err)
	}
	// Recovery is the path that expires the most rows at once, and the sweep
	// AppendBatch runs is conditional on its own deletion — so without this the
	// bytes these envelopes were the sole reference to would never be reclaimed.
	if purged > 0 {
		if err := purgeUnreferencedEvidenceBytes(operation, transaction); err != nil {
			return rawevidence.Recovery{}, err
		}
	}
	if _, err := transaction.ExecContext(
		operation,
		`INSERT INTO runtime_raw_evidence_writer_sessions(
		   writer_id, started_at_unix_ms, maximum_unflushed_ms, state
		 ) VALUES (?, ?, ?, 'open')`,
		session.WriterID, toUnixMillis(now),
		session.MaximumUnflushedTime.Milliseconds(),
	); err != nil {
		return rawevidence.Recovery{}, fmt.Errorf("open raw evidence writer session: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return rawevidence.Recovery{}, fmt.Errorf("commit raw evidence recovery: %w", err)
	}
	committed = true
	return rawevidence.Recovery{
		RecoveredUncleanWriters: uint64(unclean),
		PurgedExpiredEnvelopes:  uint64(purged),
		MaximumPossibleLoss:     time.Duration(maximumLossMillis) * time.Millisecond,
	}, nil
}

func (repository *rawEvidenceRepository) CloseWriterSession(
	ctx context.Context,
	writerID string,
	now time.Time,
) error {
	if writerID == "" || len(writerID) > rawevidence.MaxIdentityBytes || now.IsZero() {
		return errors.New("raw evidence writer close is invalid")
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	result, err := repository.database.ExecContext(
		operation,
		`UPDATE runtime_raw_evidence_writer_sessions
		 SET state = 'closed', ended_at_unix_ms = ?
		 WHERE writer_id = ? AND state = 'open'`,
		toUnixMillis(now.UTC()), writerID,
	)
	if err != nil {
		return fmt.Errorf("close raw evidence writer session: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return errors.Join(errors.New("raw evidence writer session was not open"), err)
	}
	return nil
}

const rawEvidenceColumnCount = 45

var rawEvidencePlaceholders = strings.TrimSuffix(
	strings.Repeat("?,", rawEvidenceColumnCount),
	",",
)

func rawEvidenceArguments(
	record rawevidence.StoredEnvelope,
	storedBodyDigest []byte,
) []any {
	return []any{
		record.EnvelopeID, record.WriterID, record.Watermark, string(record.Layer),
		string(record.ScopeKind), record.ScopeID, record.ExchangeID,
		record.ConnectionID, record.AttemptID,
		record.EnvironmentID, record.EnvironmentRevision, record.EnvironmentDigest,
		record.ClientEndpointID, record.ClientEndpointRevision,
		record.UpstreamEndpointID, record.UpstreamEndpointRevision,
		record.ProtocolPlanID, record.ProtocolPlanRevision,
		record.RouteID, record.RouteRevision,
		record.AccountID, record.AccountRevision, record.CredentialEpoch,
		toUnixMillis(record.ObservedAt), toUnixMillis(record.ExpiresAt),
		record.Method, record.StatusCode, record.Scheme, record.Authority,
		record.Path, record.RawQuery, record.ContentType, record.ContentEncoding,
		record.Representation, record.Canonicalization,
		record.HeaderCount, record.TrailerCount, record.BodyBytes,
		record.BodySHA256[:], string(record.DigestScope),
		string(record.PayloadState), record.PayloadReason,
		encodeRedactedCredentialFields(record.RedactedCredentialFields),
		nonNullSQLiteBlob(record.PayloadMetadata),
		storedBodyDigest,
	}
}

// SQLite distinguishes a nil []byte (NULL) from an empty []byte (a zero-length
// BLOB). The raw-evidence schema deliberately uses the latter for metadata-only
// and unavailable payloads so every row has one stable binary shape while the
// payload-state CHECK continues to enforce that a captured payload has metadata
// and an uncaptured one has none.
func nonNullSQLiteBlob(value []byte) []byte {
	if value == nil {
		return []byte{}
	}
	return value
}

func (repository *rawEvidenceRepository) ListExchange(
	ctx context.Context,
	exchangeID string,
) ([]rawevidence.StoredEnvelope, error) {
	if exchangeID == "" || len(exchangeID) > rawevidence.MaxIdentityBytes {
		return nil, errors.New("raw evidence Exchange identity is invalid")
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	rows, err := repository.database.QueryContext(
		operation,
		`SELECT `+rawEvidenceColumns+`
		 FROM runtime_raw_evidence_envelopes
		 WHERE exchange_id = ?
		 ORDER BY observed_at_unix_ms ASC, watermark ASC`,
		exchangeID,
	)
	if err != nil {
		return nil, fmt.Errorf("list raw evidence envelopes: %w", err)
	}
	type scanned struct {
		record           rawevidence.StoredEnvelope
		storedBodyDigest []byte
	}
	pending := make([]scanned, 0)
	for rows.Next() {
		record, storedBodyDigest, scanErr := scanRawEvidence(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		pending = append(pending, scanned{record, storedBodyDigest})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate raw evidence envelopes: %w", err)
	}
	// The rows must be closed before any body is read: the store keeps one open
	// connection, and resolving a body while these rows are open would wait for a
	// connection this call is still holding.
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close raw evidence envelopes: %w", err)
	}
	records := make([]rawevidence.StoredEnvelope, 0, len(pending))
	for _, item := range pending {
		record, attachErr := attachEvidenceBody(
			operation, repository.database, item.record, item.storedBodyDigest,
		)
		if attachErr != nil {
			return nil, attachErr
		}
		records = append(records, record)
	}
	return records, nil
}

type rawEvidenceRow interface {
	Scan(...any) error
}

// scanRawEvidence reads one row and returns the digest of its stored body
// without resolving it.
//
// Resolution is a separate step on purpose. The store runs with one open
// connection, so issuing a query while an outer *sql.Rows is still open waits
// forever for a connection that the same call holds. A caller iterating rows
// must therefore finish and close them before attaching bodies.
func scanRawEvidence(
	row rawEvidenceRow,
) (rawevidence.StoredEnvelope, []byte, error) {
	var (
		record                    rawevidence.StoredEnvelope
		layer, scopeKind          string
		observedAt, expiresAt     int64
		digestScope, payloadState string
		redactedFields            string
		observedDigest            []byte
		payloadMetadata           []byte
		storedBodyDigest          []byte
	)
	if err := row.Scan(
		&record.EnvelopeID, &record.WriterID, &record.Watermark, &layer,
		&scopeKind, &record.ScopeID, &record.ExchangeID,
		&record.ConnectionID, &record.AttemptID,
		&record.EnvironmentID, &record.EnvironmentRevision,
		&record.EnvironmentDigest, &record.ClientEndpointID,
		&record.ClientEndpointRevision, &record.UpstreamEndpointID,
		&record.UpstreamEndpointRevision, &record.ProtocolPlanID,
		&record.ProtocolPlanRevision, &record.RouteID, &record.RouteRevision,
		&record.AccountID, &record.AccountRevision, &record.CredentialEpoch,
		&observedAt, &expiresAt, &record.Method, &record.StatusCode,
		&record.Scheme, &record.Authority, &record.Path, &record.RawQuery,
		&record.ContentType, &record.ContentEncoding, &record.Representation,
		&record.Canonicalization, &record.HeaderCount, &record.TrailerCount,
		&record.BodyBytes, &observedDigest, &digestScope, &payloadState,
		&record.PayloadReason, &redactedFields, &payloadMetadata,
		&storedBodyDigest,
	); err != nil {
		return rawevidence.StoredEnvelope{}, nil, fmt.Errorf(
			"scan raw evidence envelope: %w", err,
		)
	}
	if len(observedDigest) != len(record.BodySHA256) {
		return rawevidence.StoredEnvelope{}, nil, errors.New(
			"stored raw evidence shape is invalid",
		)
	}
	decodedFields, err := decodeRedactedCredentialFields(redactedFields)
	if err != nil {
		return rawevidence.StoredEnvelope{}, nil, err
	}
	copy(record.BodySHA256[:], observedDigest)
	record.Layer = rawevidence.Layer(layer)
	record.ScopeKind = rawevidence.ScopeKind(scopeKind)
	record.ObservedAt = time.UnixMilli(observedAt).UTC()
	record.ExpiresAt = time.UnixMilli(expiresAt).UTC()
	record.DigestScope = rawevidence.DigestScope(digestScope)
	record.PayloadState = rawevidence.PayloadState(payloadState)
	record.RedactedCredentialFields = decodedFields
	record.PayloadMetadata = slices.Clone(payloadMetadata)
	return record, slices.Clone(storedBodyDigest), nil
}

// attachEvidenceBody rejoins a scanned record with its stored bytes and validates
// the result, so a caller never sees a record whose body was not reassembled and
// verified against its digest.
func attachEvidenceBody(
	ctx context.Context,
	queryer evidenceQueryer,
	record rawevidence.StoredEnvelope,
	storedBodyDigest []byte,
) (rawevidence.StoredEnvelope, error) {
	body, err := loadEvidenceBody(ctx, queryer, storedBodyDigest)
	if err != nil {
		return rawevidence.StoredEnvelope{}, err
	}
	record.Body = body
	if err := record.Validate(); err != nil {
		return rawevidence.StoredEnvelope{}, err
	}
	return record, nil
}

func boolToSQLite(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
