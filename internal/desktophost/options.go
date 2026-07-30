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
	defaultLauncherTTL        = 15 * time.Minute
	defaultBootstrapTTL       = 30 * time.Second
	defaultAppSessionTTL      = 12 * time.Hour
	defaultCaptureRunLifetime = 90 * time.Second
	defaultShutdownTimeout    = 20 * time.Second
)

// Options is the complete typed Desktop Host construction input.
type Options struct {
	Paths                Paths
	Runtime              productruntime.Options
	ProxyListenAddress   string
	ControlListenAddress string
	AllowedOrigins       []string
	ClientReleases       []clientadapter.Release
	LauncherTTL          time.Duration
	BootstrapTTL         time.Duration
	AppSessionTTL        time.Duration
	CaptureRunLifetime   time.Duration
	ShutdownTimeout      time.Duration
}

// DefaultOptions creates the fixed macOS arm64 M0 Host policy. Callers still
// supply all ProductRuntime dependencies explicitly.
func DefaultOptions(
	paths Paths,
	runtimeOptions productruntime.Options,
) Options {
	return Options{
		Paths:                paths,
		Runtime:              runtimeOptions,
		ProxyListenAddress:   "127.0.0.1:0",
		ControlListenAddress: "127.0.0.1:0",
		AllowedOrigins:       []string{"tauri://localhost"},
		ClientReleases: []clientadapter.Release{
			clientadapter.ClaudeCode221220DarwinARM64(),
		},
		LauncherTTL:        defaultLauncherTTL,
		BootstrapTTL:       defaultBootstrapTTL,
		AppSessionTTL:      defaultAppSessionTTL,
		CaptureRunLifetime: defaultCaptureRunLifetime,
		ShutdownTimeout:    defaultShutdownTimeout,
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
		len(options.ClientReleases) == 0 ||
		options.Runtime.SecurityRandom == nil ||
		options.LauncherTTL <= 0 ||
		options.BootstrapTTL <= 0 ||
		options.AppSessionTTL <= 0 ||
		options.CaptureRunLifetime <= 0 ||
		options.ShutdownTimeout <= 0 {
		return errors.New("Desktop Host policy is incomplete")
	}
	if options.LauncherTTL > time.Hour {
		return errors.New("launcher capability lifetime exceeds the M0 bound")
	}
	if options.BootstrapTTL > 5*time.Minute {
		return errors.New("Desktop bootstrap lifetime exceeds the M0 bound")
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
