package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	_ "modernc.org/sqlite"
)

const (
	exchangeAuditDatabaseName = "runtime.db"
	exchangeAuditResultLimit  = 1024
)

// exchangeAuditRecord is deliberately narrower than the durable Activity row.
// The acceptance command needs an ordering anchor and the exact terminal reason,
// but no request content, diagnosis, transport evidence, or public raw endpoint.
type exchangeAuditRecord struct {
	Sequence   int64
	ExchangeID string
	AccessID   string
	Status     activity.Status
	ReasonCode string
}

func (record exchangeAuditRecord) validate() error {
	candidate := activity.Record{
		Sequence:   record.Sequence,
		ID:         "acceptance-audit",
		OccurredAt: time.Unix(1, 0).UTC(),
		Kind:       activity.KindExchangeCompleted,
		AccessID:   record.AccessID,
		SubjectID:  record.ExchangeID,
		Status:     record.Status,
		ReasonCode: record.ReasonCode,
	}
	if err := candidate.Validate(); err != nil {
		return fmt.Errorf("validate committed Exchange audit record: %w", err)
	}
	return nil
}

type exchangeAuditReader struct {
	database      *sql.DB
	connection    *sql.Conn
	directoryPath string
	databasePath  string
	directoryInfo os.FileInfo
	databaseInfo  os.FileInfo
}

func openExchangeAuditReader(
	ctx context.Context,
	dataDirectory string,
) (*exchangeAuditReader, error) {
	if ctx == nil {
		return nil, errors.New("Exchange audit context is nil")
	}
	if dataDirectory == "" ||
		!filepath.IsAbs(dataDirectory) ||
		filepath.Clean(dataDirectory) != dataDirectory {
		return nil, errors.New("Exchange audit data directory is invalid")
	}
	directoryInfo, err := privateAuditPath(dataDirectory, true, 0o700)
	if err != nil {
		return nil, fmt.Errorf("validate Exchange audit data directory: %w", err)
	}
	databasePath := filepath.Join(dataDirectory, exchangeAuditDatabaseName)
	databaseInfo, err := privateAuditPath(databasePath, false, 0o600)
	if err != nil {
		return nil, fmt.Errorf("validate Exchange audit database: %w", err)
	}
	if directoryInfo == nil || databaseInfo == nil {
		return nil, errors.New("Exchange audit paths are unavailable")
	}

	database, err := sql.Open("sqlite", exchangeAuditDSN(databasePath))
	if err != nil {
		return nil, fmt.Errorf("construct read-only Exchange audit: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	var connection *sql.Conn
	fail := func(root error) (*exchangeAuditReader, error) {
		if connection != nil {
			root = errors.Join(root, connection.Close())
		}
		return nil, errors.Join(root, database.Close())
	}
	connection, err = database.Conn(ctx)
	if err != nil {
		return fail(fmt.Errorf("reserve read-only Exchange audit connection: %w", err))
	}
	if err := connection.PingContext(ctx); err != nil {
		return fail(fmt.Errorf("open read-only Exchange audit: %w", err))
	}
	var queryOnly int
	if err := connection.QueryRowContext(ctx, `PRAGMA query_only`).Scan(&queryOnly); err != nil {
		return fail(fmt.Errorf("read Exchange audit query-only state: %w", err))
	}
	if queryOnly != 1 {
		return fail(errors.New("Exchange audit connection is not query-only"))
	}
	var sequence int
	var name string
	var openedPath string
	if err := connection.QueryRowContext(
		ctx,
		`PRAGMA database_list`,
	).Scan(&sequence, &name, &openedPath); err != nil {
		return fail(fmt.Errorf("read Exchange audit database identity: %w", err))
	}
	if sequence != 0 || name != "main" ||
		openedPath == "" || !filepath.IsAbs(openedPath) {
		return fail(errors.New("Exchange audit opened an unexpected database"))
	}
	openedDatabaseInfo, err := os.Stat(openedPath)
	if err != nil || !os.SameFile(databaseInfo, openedDatabaseInfo) {
		return fail(errors.New("Exchange audit opened an unexpected database"))
	}
	openedDirectoryInfo, err := privateAuditPath(dataDirectory, true, 0o700)
	if err != nil || !os.SameFile(directoryInfo, openedDirectoryInfo) {
		return fail(errors.New("Exchange audit data directory changed while opening"))
	}
	openedInfo, err := privateAuditPath(databasePath, false, 0o600)
	if err != nil {
		return fail(fmt.Errorf("revalidate Exchange audit database: %w", err))
	}
	if !os.SameFile(databaseInfo, openedInfo) {
		return fail(errors.New("Exchange audit database changed while opening"))
	}
	reader := &exchangeAuditReader{
		database:      database,
		connection:    connection,
		directoryPath: dataDirectory,
		databasePath:  databasePath,
		directoryInfo: directoryInfo,
		databaseInfo:  databaseInfo,
	}
	if _, err := reader.latestSequence(ctx); err != nil {
		return fail(err)
	}
	return reader, nil
}

func privateAuditPath(
	path string,
	directory bool,
	permissions os.FileMode,
) (os.FileInfo, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("path must be absolute and clean")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 ||
		(directory && !info.IsDir()) ||
		(!directory && !info.Mode().IsRegular()) ||
		info.Mode().Perm() != permissions {
		return nil, errors.New("path is not the expected private file type")
	}
	if err := requirePrivateAuditOwnership(info, directory); err != nil {
		return nil, errors.New("path ownership is invalid")
	}
	return info, nil
}

func exchangeAuditDSN(databasePath string) string {
	databaseURL := url.URL{Scheme: "file", Path: databasePath}
	query := databaseURL.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "query_only(1)")
	query.Set("_dqs", "false")
	query.Set("_error_rc", "true")
	databaseURL.RawQuery = query.Encode()
	return databaseURL.String()
}

func (reader *exchangeAuditReader) Close() error {
	if reader == nil {
		return nil
	}
	connection := reader.connection
	database := reader.database
	reader.connection = nil
	reader.database = nil
	if connection == nil && database == nil {
		return nil
	}
	var err error
	if connection != nil {
		if closeErr := connection.Close(); !errors.Is(closeErr, sql.ErrConnDone) {
			err = errors.Join(err, closeErr)
		}
	}
	if database != nil {
		err = errors.Join(err, database.Close())
	}
	return err
}

func (reader *exchangeAuditReader) validateIdentity() error {
	if reader == nil ||
		reader.connection == nil ||
		reader.database == nil ||
		reader.directoryInfo == nil ||
		reader.databaseInfo == nil {
		return errors.New("Exchange audit reader is unavailable")
	}
	directoryInfo, err := privateAuditPath(reader.directoryPath, true, 0o700)
	if err != nil || !os.SameFile(reader.directoryInfo, directoryInfo) {
		return errors.New("Exchange audit data directory changed after opening")
	}
	databaseInfo, err := privateAuditPath(reader.databasePath, false, 0o600)
	if err != nil || !os.SameFile(reader.databaseInfo, databaseInfo) {
		return errors.New("Exchange audit database changed after opening")
	}
	return nil
}

func (reader *exchangeAuditReader) latestSequence(
	ctx context.Context,
) (int64, error) {
	if ctx == nil {
		return 0, errors.New("Exchange audit reader is unavailable")
	}
	if err := reader.validateIdentity(); err != nil {
		return 0, err
	}
	var sequence int64
	if err := reader.connection.QueryRowContext(
		ctx,
		`SELECT COALESCE(MAX(sequence), 0)
		 FROM runtime_activities
		 WHERE kind = ?`,
		activity.KindExchangeCompleted,
	).Scan(&sequence); err != nil {
		return 0, fmt.Errorf("read latest Exchange audit sequence: %w", err)
	}
	if sequence < 0 {
		return 0, errors.New("latest Exchange audit sequence is invalid")
	}
	return sequence, nil
}

func (reader *exchangeAuditReader) latestFailure(
	ctx context.Context,
	accessID string,
) (exchangeAuditRecord, bool, error) {
	return reader.queryOne(
		ctx,
		`SELECT sequence, subject_id, access_id, status, reason_code
		 FROM runtime_activities
		 WHERE kind = ? AND access_id = ? AND status = ?
		 ORDER BY sequence DESC
		 LIMIT 1`,
		activity.KindExchangeCompleted,
		accessID,
		activity.StatusFailed,
	)
}

func (reader *exchangeAuditReader) terminalsAfter(
	ctx context.Context,
	accessID string,
	after int64,
) ([]exchangeAuditRecord, error) {
	if ctx == nil || accessID == "" || after < 0 {
		return nil, errors.New("terminal Exchange audit request is invalid")
	}
	if err := reader.validateIdentity(); err != nil {
		return nil, err
	}
	rows, err := reader.connection.QueryContext(
		ctx,
		`SELECT sequence, subject_id, access_id, status, reason_code
		 FROM runtime_activities
		 WHERE kind = ? AND access_id = ? AND sequence > ?
		 ORDER BY sequence ASC
		 LIMIT ?`,
		activity.KindExchangeCompleted,
		accessID,
		after,
		exchangeAuditResultLimit+1,
	)
	if err != nil {
		return nil, fmt.Errorf("list terminal Exchange audit records: %w", err)
	}
	defer rows.Close()
	records := make([]exchangeAuditRecord, 0)
	for rows.Next() {
		record, err := scanExchangeAuditRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
		if len(records) > exchangeAuditResultLimit {
			return nil, errors.New("terminal Exchange audit exceeded its bound")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate terminal Exchange audit records: %w", err)
	}
	return records, nil
}

func (reader *exchangeAuditReader) bySequence(
	ctx context.Context,
	sequence int64,
) (exchangeAuditRecord, bool, error) {
	if sequence <= 0 {
		return exchangeAuditRecord{}, false, errors.New("Exchange audit sequence is invalid")
	}
	return reader.queryOne(
		ctx,
		`SELECT sequence, subject_id, access_id, status, reason_code
		 FROM runtime_activities
		 WHERE kind = ? AND sequence = ?
		 LIMIT 1`,
		activity.KindExchangeCompleted,
		sequence,
	)
}

type exchangeAuditScanner interface {
	Scan(...any) error
}

func (reader *exchangeAuditReader) queryOne(
	ctx context.Context,
	query string,
	arguments ...any,
) (exchangeAuditRecord, bool, error) {
	if ctx == nil || strings.TrimSpace(query) == "" {
		return exchangeAuditRecord{}, false, errors.New("Exchange audit query is invalid")
	}
	if err := reader.validateIdentity(); err != nil {
		return exchangeAuditRecord{}, false, err
	}
	record, err := scanExchangeAuditRecord(
		reader.connection.QueryRowContext(ctx, query, arguments...),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return exchangeAuditRecord{}, false, nil
	}
	if err != nil {
		return exchangeAuditRecord{}, false, err
	}
	return record, true, nil
}

func scanExchangeAuditRecord(
	scanner exchangeAuditScanner,
) (exchangeAuditRecord, error) {
	var record exchangeAuditRecord
	if err := scanner.Scan(
		&record.Sequence,
		&record.ExchangeID,
		&record.AccessID,
		&record.Status,
		&record.ReasonCode,
	); err != nil {
		return exchangeAuditRecord{}, err
	}
	if err := record.validate(); err != nil {
		return exchangeAuditRecord{}, err
	}
	return record, nil
}

func requireExchangeAuditRecord(
	ctx context.Context,
	reader *exchangeAuditReader,
	expected exchangeAuditRecord,
) error {
	actual, exists, err := reader.bySequence(ctx, expected.Sequence)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("committed Exchange audit record is missing")
	}
	if actual != expected {
		return fmt.Errorf(
			"committed Exchange audit record changed: sequence=%s",
			strconv.FormatInt(expected.Sequence, 10),
		)
	}
	return nil
}
