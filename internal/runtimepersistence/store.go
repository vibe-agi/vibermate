// Package runtimepersistence owns the file-backed runtime SQLite database.
package runtimepersistence

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

const (
	DefaultBusyTimeout            = 5 * time.Second
	DefaultCommitReconcileTimeout = 2 * time.Second
)

var (
	ErrInvalidDatabasePath = errors.New("invalid database path")

	//go:embed migrations/*.sql
	migrationFiles embed.FS
)

// Options contains the typed SQLite construction policy.
type Options struct {
	DatabasePath           string
	BusyTimeout            time.Duration
	CommitReconcileTimeout time.Duration
}

// RuntimeStore is the storage ownership boundary consumed by ProductRuntime.
type RuntimeStore interface {
	SchemaStateReader() SchemaStateReader
	AccessRepository() access.Repository
	ActivityRepository() activity.Repository
	CaptureRunRepository() capturerun.Repository
	ConnectionEventRepository() connectionevent.Repository
	ToolApprovalRepository() toolapproval.Repository
	Shutdown(context.Context) error
}

// Store owns a SQLite connection pool and its repositories.
type Store struct {
	database       *sql.DB
	repo           *Repository
	accessRepo     *accessRepository
	activityRepo   *activityRepository
	captureRepo    *captureRunRepository
	connectionRepo *connectionEventRepository
	egressAttempts *egressAttemptRepository
	approvalRepo   *toolApprovalRepository
	operations     *operationGate

	closeMu   sync.Mutex
	closing   bool
	closed    bool
	closeDone chan struct{}
	closeErr  error
}

var _ RuntimeStore = (*Store)(nil)

// Open creates the data directory, opens SQLite through an explicit typed
// connector, applies embedded migrations, and reads the initial schema state.
func Open(ctx context.Context, options Options) (*Store, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is nil", ErrInvalidDatabasePath)
	}
	if options.BusyTimeout <= 0 {
		return nil, fmt.Errorf("%w: busy timeout must be positive", ErrInvalidDatabasePath)
	}
	if options.CommitReconcileTimeout <= 0 {
		return nil, fmt.Errorf(
			"%w: commit reconcile timeout must be positive",
			ErrInvalidDatabasePath,
		)
	}
	if err := prepareDatabasePath(options.DatabasePath); err != nil {
		return nil, err
	}

	database := sql.OpenDB(newSQLiteConnector(options.DatabasePath, options.BusyTimeout))
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)

	fail := func(root error) (*Store, error) {
		closeErr := database.Close()
		if closeErr != nil {
			return nil, errors.Join(root, fmt.Errorf("close SQLite after open failure: %w", closeErr))
		}
		return nil, root
	}

	if err := database.PingContext(ctx); err != nil {
		return fail(fmt.Errorf("open SQLite database: %w", err))
	}
	if err := applyMigrations(ctx, database); err != nil {
		return fail(err)
	}
	if err := protectDatabaseArtifacts(options.DatabasePath); err != nil {
		return fail(err)
	}

	operations := newOperationGate()
	repository := newRepository(database, operations)
	accessRepo := newAccessRepository(
		database,
		operations,
		options.CommitReconcileTimeout,
		sqlTransactionCommitter{},
	)
	captureRepo := newCaptureRunRepository(database, operations)
	activityRepo := newActivityRepository(database, operations)
	connectionRepo := newConnectionEventRepository(database, operations)
	egressRepo := newEgressAttemptRepository(database, operations)
	approvalRepo := newToolApprovalRepository(database, operations)
	if _, err := repository.ReadSchemaState(ctx); err != nil {
		operations.closeAdmission()
		return fail(fmt.Errorf("read initial schema state: %w", err))
	}

	return &Store{
		database:       database,
		repo:           repository,
		accessRepo:     accessRepo,
		activityRepo:   activityRepo,
		captureRepo:    captureRepo,
		connectionRepo: connectionRepo,
		egressAttempts: egressRepo,
		approvalRepo:   approvalRepo,
		operations:     operations,
		closeDone:      make(chan struct{}),
	}, nil
}

func (s *Store) SchemaStateReader() SchemaStateReader {
	return s.repo
}

func (s *Store) AccessRepository() access.Repository {
	return s.accessRepo
}

func (s *Store) ActivityRepository() activity.Repository {
	return s.activityRepo
}

func (s *Store) CaptureRunRepository() capturerun.Repository {
	return s.captureRepo
}

func (s *Store) EgressAttemptRepository() egressaudit.Repository {
	return s.egressAttempts
}

func (s *Store) ConnectionEventRepository() connectionevent.Repository {
	return s.connectionRepo
}

func (s *Store) ToolApprovalRepository() toolapproval.Repository {
	return s.approvalRepo
}

// Settings reads the active SQLite connection policy through the same
// operation admission and cancellation boundary as repository reads.
func (s *Store) Settings(ctx context.Context) (DatabaseSettings, error) {
	return s.repo.Settings(ctx)
}

// Shutdown closes operation admission, cancels runtime-owned database work,
// drains it within the caller deadline, and only then closes database/sql.
func (s *Store) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("SQLite shutdown context is nil")
	}
	s.operations.closeAdmission()
	if err := s.operations.drain(ctx); err != nil {
		return err
	}

	s.closeMu.Lock()
	switch {
	case s.closed:
		err := s.closeErr
		s.closeMu.Unlock()
		return err
	case s.closing:
		done := s.closeDone
		s.closeMu.Unlock()
		select {
		case <-done:
			s.closeMu.Lock()
			err := s.closeErr
			s.closeMu.Unlock()
			return err
		case <-ctx.Done():
			return fmt.Errorf("wait for SQLite close: %w", ctx.Err())
		}
	default:
		s.closing = true
	}
	s.closeMu.Unlock()

	closeErr := s.database.Close()
	s.closeMu.Lock()
	s.closeErr = closeErr
	s.closed = true
	s.closing = false
	close(s.closeDone)
	s.closeMu.Unlock()
	return closeErr
}

func applyMigrations(ctx context.Context, database *sql.DB) error {
	migrations, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		database,
		migrations,
		goose.WithSlog(logger),
	)
	if err != nil {
		return fmt.Errorf("construct migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply SQLite migrations: %w", err)
	}
	return nil
}

func prepareDatabasePath(databasePath string) error {
	if databasePath == "" || !filepath.IsAbs(databasePath) {
		return fmt.Errorf("%w: path must be absolute", ErrInvalidDatabasePath)
	}
	if filepath.Clean(databasePath) != databasePath {
		return fmt.Errorf("%w: path must be clean", ErrInvalidDatabasePath)
	}

	dataDirectory := filepath.Dir(databasePath)
	if err := os.MkdirAll(dataDirectory, 0o700); err != nil {
		return fmt.Errorf("create runtime data directory: %w", err)
	}
	directoryInfo, err := os.Lstat(dataDirectory)
	if err != nil {
		return fmt.Errorf("inspect runtime data directory: %w", err)
	}
	if directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir() {
		return fmt.Errorf("%w: data directory must not be a symbolic link", ErrInvalidDatabasePath)
	}
	if err := os.Chmod(dataDirectory, 0o700); err != nil {
		return fmt.Errorf("protect runtime data directory: %w", err)
	}

	databaseInfo, err := os.Lstat(databasePath)
	switch {
	case err == nil:
		if databaseInfo.Mode()&os.ModeSymlink != 0 || !databaseInfo.Mode().IsRegular() {
			return fmt.Errorf("%w: database must be a regular file", ErrInvalidDatabasePath)
		}
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("inspect runtime database: %w", err)
	}
	return nil
}

func protectDatabaseArtifacts(databasePath string) error {
	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("protect SQLite artifact %q: %w", filepath.Base(path), err)
		}
	}
	return nil
}
