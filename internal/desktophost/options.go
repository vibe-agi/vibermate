package desktophost

import (
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/productruntime"
)

const (
	defaultCLIControlDiscoveryTTL = 15 * time.Minute
	defaultBootstrapTTL           = 30 * time.Second
	defaultAppSessionTTL          = 12 * time.Hour
	defaultAppSessionReplayTTL    = 2 * time.Minute
	defaultCaptureRunLifetime     = 90 * time.Second
	defaultShutdownTimeout        = 20 * time.Second
)

// Options is the complete typed Desktop Host construction input.
type Options struct {
	Paths                  Paths
	Runtime                productruntime.Options
	ProxyListenAddress     string
	ControlListenAddress   string
	AllowedOrigins         []string
	ClientCatalog          clientadapter.Catalog
	CLIControlDiscoveryTTL time.Duration
	BootstrapTTL           time.Duration
	AppSessionTTL          time.Duration
	AppSessionReplayTTL    time.Duration
	CaptureRunLifetime     time.Duration
	ShutdownTimeout        time.Duration
}

// DefaultOptions creates the current macOS arm64 Host policy. Callers still
// supply all ProductRuntime dependencies explicitly.
func DefaultOptions(
	paths Paths,
	runtimeOptions productruntime.Options,
) Options {
	return Options{
		Paths:                  paths,
		Runtime:                runtimeOptions,
		ProxyListenAddress:     "127.0.0.1:0",
		ControlListenAddress:   "127.0.0.1:0",
		AllowedOrigins:         []string{"tauri://localhost"},
		ClientCatalog:          clientadapter.BuiltInCatalog(),
		CLIControlDiscoveryTTL: defaultCLIControlDiscoveryTTL,
		BootstrapTTL:           defaultBootstrapTTL,
		AppSessionTTL:          defaultAppSessionTTL,
		AppSessionReplayTTL:    defaultAppSessionReplayTTL,
		CaptureRunLifetime:     defaultCaptureRunLifetime,
		ShutdownTimeout:        defaultShutdownTimeout,
	}
}

func (options Options) validate() error {
	if options.Paths.AppCacheDirectory() == "" ||
		options.Paths.RuntimeDirectory() == "" ||
		options.Paths.LockPath() == "" ||
		options.Paths.DiscoveryPath() == "" {
		return errors.New("Desktop Host paths are incomplete")
	}
	if options.Runtime.Host.Kind() != hostcontract.KindDesktop {
		return errors.New("Desktop Host requires the Desktop ProductRuntime contract")
	}
	if err := validateListenAddress(options.ProxyListenAddress); err != nil {
		return err
	}
	if err := validateListenAddress(options.ControlListenAddress); err != nil {
		return err
	}
	if len(options.AllowedOrigins) == 0 ||
		!options.ClientCatalog.Valid() ||
		options.Runtime.SecurityRandom == nil ||
		options.CLIControlDiscoveryTTL <= 0 ||
		options.BootstrapTTL <= 0 ||
		options.AppSessionTTL <= 0 ||
		options.AppSessionReplayTTL <= 0 ||
		options.AppSessionReplayTTL > options.AppSessionTTL ||
		options.AppSessionReplayTTL > 5*time.Minute ||
		options.CaptureRunLifetime <= 0 ||
		options.ShutdownTimeout <= 0 {
		return errors.New("Desktop Host policy is incomplete")
	}
	if options.CLIControlDiscoveryTTL > time.Hour {
		return errors.New("CLI control discovery freshness exceeds the supported bound")
	}
	if options.BootstrapTTL > 5*time.Minute {
		return errors.New("Desktop bootstrap lifetime exceeds the supported bound")
	}
	return nil
}

func validateListenAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || port == "" {
		return errors.New("Desktop Host listener must use literal IPv4 loopback")
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number > 65535 {
		return errors.New("Desktop Host listener port is invalid")
	}
	return nil
}
