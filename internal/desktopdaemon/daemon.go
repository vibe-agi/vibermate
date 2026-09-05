// Package desktopdaemon is the thin production adapter between the packaged
// sidecar process and DesktopHost. Product components remain assembled only by
// ProductRuntime.
package desktopdaemon

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopbootstrap"
	"github.com/vibe-agi/vibermate/internal/desktophost"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

type Options struct {
	Host            desktophost.Options
	BootstrapWriter io.Writer
	ShutdownTimeout time.Duration
}

// ProductionOptions constructs the current macOS arm64 dependency graph from
// native-shell-resolved absolute paths.
func ProductionOptions(
	ctx context.Context,
	appCacheDirectory string,
	dataDirectory string,
	webviewOrigin string,
	bootstrapWriter io.Writer,
	secretsFactory secretstore.Factory,
) (Options, error) {
	if ctx == nil {
		return Options{}, errors.New("Desktop production options context is nil")
	}
	if secretsFactory == nil {
		return Options{}, errors.New("Desktop SecretStore factory is nil")
	}
	if bootstrapWriter == nil {
		return Options{}, errors.New("Desktop bootstrap descriptor is unavailable")
	}
	if webviewOrigin != "vibermate://desktop" {
		return Options{}, errors.New("Desktop Webview origin is unsupported")
	}
	hostPaths, err := desktophost.NewPaths(appCacheDirectory)
	if err != nil {
		return Options{}, err
	}
	runtimePaths, err := productruntime.NewRuntimePaths(dataDirectory)
	if err != nil {
		return Options{}, err
	}
	secrets, err := secretsFactory.Open(ctx)
	if err != nil {
		return Options{}, err
	}
	if secrets == nil {
		return Options{}, errors.New("Desktop SecretStore factory returned nil")
	}
	coordinator, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		return Options{}, err
	}
	runtimeOptions := productruntime.Options{
		Paths:          runtimePaths,
		Host:           hostcontract.Desktop(),
		OfflineHold:    coordinator,
		Secrets:        secrets,
		Approvals:      toolapproval.DefaultConfig(),
		ExchangeHold:   exchange.DefaultHoldPolicy(),
		Clock:          productruntime.SystemClock{},
		InstanceIDs:    productruntime.NewCryptographicInstanceIDSource(),
		SecurityRandom: rand.Reader,
		Lifecycle:      productruntime.DefaultLifecycleOptions(),
	}
	hostOptions := desktophost.DefaultOptions(hostPaths, runtimeOptions)
	hostOptions.AllowedOrigins = []string{webviewOrigin}
	// The native App is also a Runtime Server. Remote clients authenticate as
	// Runtime Users and share the same evidence database as the local App.
	hostOptions.RemoteServerEnabled = true
	return Options{
		Host:            hostOptions,
		BootstrapWriter: bootstrapWriter,
		ShutdownTimeout: 25 * time.Second,
	}, nil
}

// Run first announces capability-free runtime initialization, then starts one
// Host, writes one capability-bearing descriptor to the inherited pipe, and
// owns shutdown until the process context or Host fails.
func Run(ctx context.Context, options Options) error {
	if ctx == nil ||
		options.BootstrapWriter == nil ||
		options.ShutdownTimeout <= 0 {
		return errors.New("Desktop daemon options are incomplete")
	}
	encoder := json.NewEncoder(options.BootstrapWriter)
	progress := desktopbootstrap.RuntimeStartingProgress()
	if err := progress.Validate(); err != nil {
		return err
	}
	if err := encoder.Encode(progress); err != nil {
		return fmt.Errorf("write native-shell bootstrap progress: %w", err)
	}
	host, err := desktophost.Start(ctx, options.Host)
	if err != nil {
		failure := classifyStartupFailure(err)
		if writeErr := encoder.Encode(failure); writeErr != nil {
			return errors.Join(
				err,
				fmt.Errorf("write native-shell bootstrap failure: %w", writeErr),
			)
		}
		return err
	}
	if err := encoder.Encode(host.Bootstrap()); err != nil {
		shutdownContext, cancel := context.WithTimeout(
			context.Background(),
			options.ShutdownTimeout,
		)
		shutdownErr := host.Shutdown(shutdownContext)
		cancel()
		return errors.Join(
			errors.New("write native-shell bootstrap descriptor"),
			err,
			shutdownErr,
		)
	}
	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(
			context.Background(),
			options.ShutdownTimeout,
		)
		shutdownErr := host.Shutdown(shutdownContext)
		cancel()
		return shutdownErr
	case <-host.Done():
		waitContext, cancel := context.WithTimeout(
			context.Background(),
			options.ShutdownTimeout,
		)
		defer cancel()
		return host.Shutdown(waitContext)
	}
}

func classifyStartupFailure(err error) desktopbootstrap.Failure {
	reason := desktopbootstrap.FailureRuntimeUnavailable
	switch {
	case errors.Is(err, instanceguard.ErrAlreadyOwned):
		reason = desktopbootstrap.FailureRuntimeAlreadyActive
	case errors.Is(err, localca.ErrRootResetFailed):
		reason = desktopbootstrap.FailureRootResetFailed
	case errors.Is(err, runtimepersistence.ErrInvalidDatabasePath),
		errors.Is(err, runtimepersistence.ErrSchemaBaselineMismatch):
		reason = desktopbootstrap.FailureStorageUnavailable
	case errors.Is(err, secretstore.ErrLocked),
		errors.Is(err, secretstore.ErrDenied),
		errors.Is(err, secretstore.ErrUnavailable):
		reason = desktopbootstrap.FailureSecretStoreUnavailable
	}
	return desktopbootstrap.StartupFailure(reason)
}
