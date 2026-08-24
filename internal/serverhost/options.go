package serverhost

import (
	"errors"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/productruntime"
)

const (
	defaultAdminSessionLifetime = 8 * time.Hour
	defaultCaptureRunLifetime   = 90 * time.Second
	defaultShutdownTimeout      = 20 * time.Second
)

type Options struct {
	Runtime              productruntime.Options
	ListenAddress        string
	Transport            TransportOptions
	ManagementUIRoot     string
	ClientCatalog        clientadapter.Catalog
	AdminSessionLifetime time.Duration
	CaptureRunLifetime   time.Duration
	ShutdownTimeout      time.Duration
}

// AttachOptions exposes the Runtime Server transport around an already-owned
// ProductRuntime. DesktopHost uses this form so the Mac App and remote clients
// see one database, one Environment authority, and one evidence timeline.
type AttachOptions struct {
	Runtime                *productruntime.Runtime
	DataDirectory          string
	ListenAddress          string
	Transport              TransportOptions
	ManagementUIRoot       string
	ClientCatalog          clientadapter.Catalog
	AdminSessionLifetime   time.Duration
	CaptureRunLifetime     time.Duration
	ShutdownTimeout        time.Duration
	Clock                  productruntime.Clock
	SecurityRandom         io.Reader
	ResolveLocalIdentities bool
}

func DefaultOptions(runtimeOptions productruntime.Options) Options {
	return Options{
		Runtime:              runtimeOptions,
		ListenAddress:        "0.0.0.0:9666",
		Transport:            TransportOptions{Mode: TransportSelfSignedTLS},
		ClientCatalog:        clientadapter.BuiltInCatalog(),
		AdminSessionLifetime: defaultAdminSessionLifetime,
		CaptureRunLifetime:   defaultCaptureRunLifetime,
		ShutdownTimeout:      defaultShutdownTimeout,
	}
}

func DefaultAttachOptions(
	runtime *productruntime.Runtime,
	dataDirectory string,
	clock productruntime.Clock,
	random io.Reader,
) AttachOptions {
	return AttachOptions{
		Runtime: runtime, DataDirectory: dataDirectory,
		ListenAddress:        "0.0.0.0:9666",
		Transport:            TransportOptions{Mode: TransportSelfSignedTLS},
		ClientCatalog:        clientadapter.BuiltInCatalog(),
		AdminSessionLifetime: defaultAdminSessionLifetime,
		CaptureRunLifetime:   defaultCaptureRunLifetime,
		ShutdownTimeout:      defaultShutdownTimeout,
		Clock:                clock,
		SecurityRandom:       random,
	}
}

func (options Options) validate() error {
	if options.Runtime.Host.Kind() != hostcontract.KindServer ||
		!options.Runtime.Host.SupportsCaptureRuns() ||
		options.Runtime.SecurityRandom == nil {
		return errors.New("Runtime Server Host policy is incomplete")
	}
	return validateNetworkPolicy(
		options.ListenAddress, options.Transport, options.ManagementUIRoot,
		options.ClientCatalog, options.AdminSessionLifetime,
		options.CaptureRunLifetime, options.ShutdownTimeout,
	)
}

func (options AttachOptions) validate() error {
	if options.Runtime == nil || options.Clock == nil || options.SecurityRandom == nil ||
		options.DataDirectory == "" || !filepath.IsAbs(options.DataDirectory) ||
		filepath.Clean(options.DataDirectory) != options.DataDirectory {
		return errors.New("attached Runtime Server dependencies are incomplete")
	}
	return validateNetworkPolicy(
		options.ListenAddress, options.Transport, options.ManagementUIRoot,
		options.ClientCatalog, options.AdminSessionLifetime,
		options.CaptureRunLifetime, options.ShutdownTimeout,
	)
}

func validateNetworkPolicy(
	listenAddress string,
	transport TransportOptions,
	managementUIRoot string,
	clientCatalog clientadapter.Catalog,
	adminSessionLifetime time.Duration,
	captureRunLifetime time.Duration,
	shutdownTimeout time.Duration,
) error {
	if transport.validate() != nil || !clientCatalog.Valid() ||
		adminSessionLifetime <= 0 || adminSessionLifetime > 24*time.Hour ||
		captureRunLifetime <= 0 || shutdownTimeout <= 0 {
		return errors.New("Runtime Server Host policy is incomplete")
	}
	if managementUIRoot != "" &&
		(!filepath.IsAbs(managementUIRoot) ||
			filepath.Clean(managementUIRoot) != managementUIRoot) {
		return errors.New("Runtime Server Web root is invalid")
	}
	host, portText, err := net.SplitHostPort(listenAddress)
	if err != nil || host == "" || portText == "" {
		return errors.New("Runtime Server listen address is invalid")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port > 65535 {
		return errors.New("Runtime Server listen port is invalid")
	}
	return nil
}
