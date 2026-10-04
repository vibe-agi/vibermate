package productruntime

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/loopbackproxy"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
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
	// Resolve only directory ancestors. A final endpoint link is still rejected,
	// while OS aliases such as /var share one physical data/SQLite/guard authority.
	if info, err := os.Lstat(dataDirectory); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return RuntimePaths{}, fmt.Errorf("%w: data directory endpoint must be a directory", ErrInvalidOptions)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return RuntimePaths{}, fmt.Errorf("%w: inspect data directory", ErrInvalidOptions)
	}
	ancestor := dataDirectory
	var missing []string
	for {
		info, err := os.Stat(ancestor)
		if err == nil {
			if !info.IsDir() {
				return RuntimePaths{}, fmt.Errorf("%w: data path ancestor is not a directory", ErrInvalidOptions)
			}
			physical, err := filepath.EvalSymlinks(ancestor)
			if err != nil {
				return RuntimePaths{}, fmt.Errorf("%w: resolve data directory", ErrInvalidOptions)
			}
			dataDirectory = physical
			for index := len(missing) - 1; index >= 0; index-- {
				dataDirectory = filepath.Join(dataDirectory, missing[index])
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return RuntimePaths{}, fmt.Errorf("%w: inspect data path ancestor", ErrInvalidOptions)
		}
		missing = append(missing, filepath.Base(ancestor))
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return RuntimePaths{}, ErrInvalidOptions
		}
		ancestor = parent
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

// DefaultLifecycleOptions returns the process lifecycle policy.
func DefaultLifecycleOptions() LifecycleOptions {
	return LifecycleOptions{
		RollbackTimeout:    5 * time.Second,
		ShutdownTimeout:    10 * time.Second,
		HealthPollInterval: 30 * time.Second,
	}
}

// Options is the complete typed ProductRuntime construction input.
type Options struct {
	// BlindTunnels is a trusted in-process dial dependency; nil retains the
	// normal offline-gated dialer. It cannot grant connection policy authority.
	BlindTunnels loopbackproxy.BlindTunnelDialer
	// OriginalTransport is a trusted in-process transport dependency. It grants
	// no request authority. Nil selects the normal strict production transport.
	OriginalTransport      originaltransport.Transport
	SchemaUpgradeHostGuard *instanceguard.Guard
	SchemaUpgradeTimeout   time.Duration
	SchemaUpgradeProgress  func(runtimedata.SchemaUpgradePhase) error
	Paths                  RuntimePaths
	Host                   hostcontract.Contract
	OfflineHold            offlinehold.RuntimeCoordinator
	Secrets                secretstore.Store
	Approvals              toolapproval.Config
	ExchangeHold           exchange.HoldPolicy
	Clock                  Clock
	InstanceIDs            InstanceIDSource
	SecurityRandom         io.Reader
	Lifecycle              LifecycleOptions
}

func (o Options) validate() error {
	if o.SchemaUpgradeTimeout < 0 {
		return fmt.Errorf("%w: negative schema upgrade timeout", ErrInvalidOptions)
	}
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
