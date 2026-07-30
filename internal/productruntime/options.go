package productruntime

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

const runtimeDatabaseName = "runtime.db"

var ErrInvalidOptions = errors.New("invalid ProductRuntime options")

// RuntimePaths contains host-resolved persistent paths. ProductRuntime never
// derives these paths from HOME or the process working directory.
type RuntimePaths struct {
	dataDirectory string
	databasePath  string
}

// NewRuntimePaths validates an explicit host data directory without creating
// it.
func NewRuntimePaths(dataDirectory string) (RuntimePaths, error) {
	if dataDirectory == "" || !filepath.IsAbs(dataDirectory) {
		return RuntimePaths{}, fmt.Errorf("%w: data directory must be absolute", ErrInvalidOptions)
	}
	if filepath.Clean(dataDirectory) != dataDirectory {
		return RuntimePaths{}, fmt.Errorf("%w: data directory must be clean", ErrInvalidOptions)
	}
	if dataDirectory == filepath.VolumeName(dataDirectory)+string(filepath.Separator) {
		return RuntimePaths{}, fmt.Errorf("%w: data directory must not be a filesystem root", ErrInvalidOptions)
	}
	return RuntimePaths{
		dataDirectory: dataDirectory,
		databasePath:  filepath.Join(dataDirectory, runtimeDatabaseName),
	}, nil
}

func (p RuntimePaths) DataDirectory() string {
	return p.dataDirectory
}

func (p RuntimePaths) DatabasePath() string {
	return p.databasePath
}

func (p RuntimePaths) LocalCADirectory() string {
	return filepath.Join(p.dataDirectory, "local-ca")
}

// LifecycleOptions bounds startup rollback, shutdown, and health observation.
type LifecycleOptions struct {
	RollbackTimeout    time.Duration
	ShutdownTimeout    time.Duration
	HealthPollInterval time.Duration
}

// DefaultLifecycleOptions returns the M0 process lifecycle policy.
func DefaultLifecycleOptions() LifecycleOptions {
	return LifecycleOptions{
		RollbackTimeout:    5 * time.Second,
		ShutdownTimeout:    10 * time.Second,
		HealthPollInterval: 30 * time.Second,
	}
}

// Options is the complete typed ProductRuntime construction input.
type Options struct {
	Paths          RuntimePaths
	Host           hostcontract.Contract
	OfflineHold    offlinehold.RuntimeCoordinator
	Secrets        secretstore.Store
	Approvals      toolapproval.Config
	ExchangeHold   exchange.HoldPolicy
	Clock          Clock
	InstanceIDs    InstanceIDSource
	SecurityRandom io.Reader
	Lifecycle      LifecycleOptions
}

func (o Options) validate() error {
	if o.Paths.dataDirectory == "" || o.Paths.databasePath == "" {
		return fmt.Errorf("%w: runtime paths are missing", ErrInvalidOptions)
	}
	if err := o.Host.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidOptions, err)
	}
	if o.OfflineHold == nil {
		return fmt.Errorf("%w: offline-hold coordinator is missing", ErrInvalidOptions)
	}
	if o.Secrets == nil {
		return fmt.Errorf("%w: secret store is missing", ErrInvalidOptions)
	}
	if err := o.Approvals.Validate(); err != nil {
		return fmt.Errorf("%w: approval policy: %w", ErrInvalidOptions, err)
	}
	if err := o.ExchangeHold.Validate(); err != nil {
		return fmt.Errorf("%w: Exchange Hold policy: %w", ErrInvalidOptions, err)
	}
	if o.Clock == nil {
		return fmt.Errorf("%w: clock is missing", ErrInvalidOptions)
	}
	if o.InstanceIDs == nil {
		return fmt.Errorf("%w: instance ID source is missing", ErrInvalidOptions)
	}
	if o.SecurityRandom == nil {
		return fmt.Errorf("%w: security random source is missing", ErrInvalidOptions)
	}
	if o.Lifecycle.RollbackTimeout <= 0 {
		return fmt.Errorf("%w: rollback timeout must be positive", ErrInvalidOptions)
	}
	if o.Lifecycle.ShutdownTimeout <= 0 {
		return fmt.Errorf("%w: shutdown timeout must be positive", ErrInvalidOptions)
	}
	if o.Lifecycle.HealthPollInterval <= 0 {
		return fmt.Errorf("%w: health poll interval must be positive", ErrInvalidOptions)
	}
	return nil
}
