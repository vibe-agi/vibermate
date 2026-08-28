// Package runtimepersistence owns the file-backed runtime SQLite database.
package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/codelibrary"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/proxyclient"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

const (
	DefaultBusyTimeout            = 5 * time.Second
	DefaultCommitReconcileTimeout = 2 * time.Second
)

var (
	ErrInvalidDatabasePath = errors.New("invalid database path")

	//go:embed schema.sql
	schemaSQL string
)

// Options contains the typed SQLite construction policy.
type Options struct {
	DatabasePath           string
	BusyTimeout            time.Duration
	CommitReconcileTimeout time.Duration
}

// Store owns a SQLite connection pool and its repositories.
type Store struct {
	database           *sql.DB
	repo               *Repository
	activityRepo       *activityRepository
	exchangeContents   *exchangeContentRepository
	captureRepo        *captureRunRepository
	codeLibrary        *codeLibraryRepository
	egressProfiles     *egressProfileRepository
	manualCapture      *manualCaptureRepository
	proxyClients       *proxyClientRepository
	connectionRepo     *connectionEventRepository
	egressAttempts     *egressAttemptRepository
	approvalRepo       *toolApprovalRepository
	connectionRule     *connectionRuleRepository
	environments       *environmentRepository
	captureAssignments *captureAssignmentRepository
	upstreamEndpoints  *upstreamEndpointRepository
	providerAccounts   *providerAccountRepository
	rawEvidence        *rawEvidenceRepository
	runtimeUsers       *runtimeUserRepository
	operations         *operationGate

	closeMu   sync.Mutex
	closing   bool
	closed    bool
	closeDone chan struct{}
	closeErr  error
}

// Open creates the data directory, opens SQLite through an explicit typed
// connector, initializes the current schema, and reads its durable state.
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
	schemaSourceSHA256, err := initializeSchema(ctx, database)
	if err != nil {
		return fail(err)
	}
	if err := protectDatabaseArtifacts(options.DatabasePath); err != nil {
		return fail(err)
	}

	operations := newOperationGate()
	repository := newRepository(database, operations, schemaSourceSHA256)
	captureRepo := newCaptureRunRepository(database, operations)
	codeLibrary := newCodeLibraryRepository(
		database,
		operations,
		options.CommitReconcileTimeout,
		sqlTransactionCommitter{},
	)
	egressProfiles := newEgressProfileRepository(
		database,
		operations,
		options.CommitReconcileTimeout,
		sqlTransactionCommitter{},
	)
	manualCaptureRepo := newManualCaptureRepository(database, operations)
	proxyClientRepo := newProxyClientRepository(
		database,
		operations,
		options.CommitReconcileTimeout,
		sqlTransactionCommitter{},
	)
	activityRepo := newActivityRepository(database, operations)
	exchangeContents := newExchangeContentRepository(database, operations)
	connectionRepo := newConnectionEventRepository(database, operations)
	egressRepo := newEgressAttemptRepository(database, operations)
	approvalRepo := newToolApprovalRepository(database, operations)
	connectionRules := newConnectionRuleRepository(database, operations)
	environments := newEnvironmentRepository(
		database,
		operations,
		options.CommitReconcileTimeout,
		sqlTransactionCommitter{},
	)
	captureAssignments := newCaptureAssignmentRepository(
		database,
		operations,
		options.CommitReconcileTimeout,
		sqlTransactionCommitter{},
	)
	upstreamEndpoints := newUpstreamEndpointRepository(
		database,
		operations,
		options.CommitReconcileTimeout,
		sqlTransactionCommitter{},
	)
	providerAccounts := newProviderAccountRepository(
		database,
		operations,
		options.CommitReconcileTimeout,
		sqlTransactionCommitter{},
	)
	rawEvidence := newRawEvidenceRepository(database, operations)
	runtimeUsers := newRuntimeUserRepository(database, operations)
	_, err = repository.ReadSchemaState(ctx)
	if err != nil {
		operations.closeAdmission()
		return fail(fmt.Errorf("read initial schema state: %w", err))
	}

	return &Store{
		database:           database,
		repo:               repository,
		activityRepo:       activityRepo,
		exchangeContents:   exchangeContents,
		captureRepo:        captureRepo,
		codeLibrary:        codeLibrary,
		egressProfiles:     egressProfiles,
		manualCapture:      manualCaptureRepo,
		proxyClients:       proxyClientRepo,
		connectionRepo:     connectionRepo,
		egressAttempts:     egressRepo,
		approvalRepo:       approvalRepo,
		connectionRule:     connectionRules,
		environments:       environments,
		captureAssignments: captureAssignments,
		upstreamEndpoints:  upstreamEndpoints,
		providerAccounts:   providerAccounts,
		rawEvidence:        rawEvidence,
		runtimeUsers:       runtimeUsers,
		operations:         operations,
		closeDone:          make(chan struct{}),
	}, nil
}

func (s *Store) SchemaStateReader() SchemaStateReader {
	return s.repo
}

func (s *Store) ActivityRepository() activity.Repository {
	return s.activityRepo
}

func (s *Store) ConversationIdentityRepository() activity.ConversationIdentityRepository {
	return s.activityRepo
}

func (s *Store) ConversationProjectionWriter() activity.ConversationProjectionWriter {
	return s.activityRepo
}

func (s *Store) ExchangeContentRepository() exchangecontent.Repository {
	return s.exchangeContents
}

func (s *Store) CaptureRunRepository() capturerun.Repository {
	return s.captureRepo
}

func (s *Store) CodeLibraryRepository() codelibrary.Repository {
	return s.codeLibrary
}

func (s *Store) EgressProfileRepository() egressprofile.Repository {
	return s.egressProfiles
}

func (s *Store) ManualCaptureRepository() manualcapture.Repository {
	return s.manualCapture
}

func (s *Store) ProxyClientRepository() proxyclient.Repository {
	return s.proxyClients
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

func (s *Store) ConnectionRuleRepository() connectionpolicy.Repository {
	return s.connectionRule
}

// EnvironmentRepository is the durable editable Environment authority used by
// the production composition root.
func (s *Store) EnvironmentRepository() environment.Repository {
	return s.environments
}

// CaptureAssignmentRepository stores the current Environment choice for both
// managed runs and manual captures.
func (s *Store) CaptureAssignmentRepository() captureassignment.Repository {
	return s.captureAssignments
}

// UpstreamEndpointRepository persists reusable upstream service identity.
// ProviderAccount rows reference this authority with a non-null foreign key.
func (s *Store) UpstreamEndpointRepository() upstreamendpoint.Repository {
	return s.upstreamEndpoints
}

// ProviderAccountRepository persists only non-secret account configuration.
// Secret bytes remain owned by the host-selected SecretStore.
func (s *Store) ProviderAccountRepository() provideraccount.Repository {
	return s.providerAccounts
}

// RawEvidenceRepository stores safe searchable metadata, the observed payload
// metadata, and a content-addressed reference to the observed body bytes.
// High-frequency batching remains owned by rawevidence.Manager.
func (s *Store) RawEvidenceRepository() rawevidence.Repository {
	return s.rawEvidence
}

func (s *Store) RuntimeUserRepository() runtimeuser.Repository {
	return s.runtimeUsers
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

func initializeSchema(ctx context.Context, database *sql.DB) (string, error) {
	sum := sha256.Sum256([]byte(schemaSQL))
	digest := hex.EncodeToString(sum[:])

	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin SQLite schema initialization: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	var initialized bool
	if err := transaction.QueryRowContext(
		ctx,
		`SELECT EXISTS(
		   SELECT 1 FROM sqlite_schema
		   WHERE type = 'table' AND name = 'runtime_metadata'
		 )`,
	).Scan(&initialized); err != nil {
		return "", fmt.Errorf("inspect SQLite schema: %w", err)
	}
	if initialized {
		return digest, nil
	}

	var objects int
	if err := transaction.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`,
	).Scan(&objects); err != nil {
		return "", fmt.Errorf("inspect empty SQLite schema: %w", err)
	}
	if objects != 0 {
		return "", fmt.Errorf("%w: database contains %d schema objects", ErrSchemaBaselineMismatch, objects)
	}
	if _, err := transaction.ExecContext(ctx, schemaSQL); err != nil {
		return "", fmt.Errorf("initialize SQLite schema: %w", err)
	}
	if _, err := transaction.ExecContext(
		ctx,
		`INSERT INTO runtime_metadata(
		   singleton, schema_identity, schema_revision,
		   schema_source_sha256, initialized_at
		 ) VALUES (1, ?, ?, ?, ?)`,
		currentSchemaIdentity,
		currentSchemaRevision,
		digest,
		time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return "", fmt.Errorf("record SQLite schema state: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return "", fmt.Errorf("commit SQLite schema initialization: %w", err)
	}
	return digest, nil
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
