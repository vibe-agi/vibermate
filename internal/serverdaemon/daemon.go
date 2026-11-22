// Package serverdaemon is the thin headless process adapter around ServerHost.
package serverdaemon

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/serverhost"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

type Options struct {
	Host            serverhost.Options
	ReadyWriter     io.Writer
	ShutdownTimeout time.Duration
}

func ProductionOptions(
	ctx context.Context,
	dataDirectory string,
	listenAddress string,
	managementUIRoot string,
	transport serverhost.TransportOptions,
	secretsFactory secretstore.Factory,
	readyWriter io.Writer,
) (Options, error) {
	if ctx == nil || secretsFactory == nil || readyWriter == nil {
		return Options{}, errors.New("Server production dependencies are incomplete")
	}
	paths, err := productruntime.NewRuntimePaths(dataDirectory)
	if err != nil {
		return Options{}, err
	}
	secrets, err := secretsFactory.Open(ctx)
	if err != nil {
		return Options{}, err
	}
	coordinator, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		return Options{}, err
	}
	runtimeOptions := productruntime.Options{
		Paths: paths, Host: hostcontract.Server(), OfflineHold: coordinator, Secrets: secrets,
		Approvals: toolapproval.DefaultConfig(), ExchangeHold: exchange.DefaultHoldPolicy(),
		Clock: productruntime.SystemClock{}, InstanceIDs: productruntime.NewCryptographicInstanceIDSource(),
		SecurityRandom: rand.Reader, Lifecycle: productruntime.DefaultLifecycleOptions(),
		RecognizedClientProtections: recognizedClientProtections(
			os.Getenv(clientadapter.RecognizedProtectionsVariable),
			os.Stderr,
		),
	}
	hostOptions := serverhost.DefaultOptions(runtimeOptions)
	hostOptions.ListenAddress = listenAddress
	hostOptions.ManagementUIRoot = managementUIRoot
	hostOptions.Transport = transport
	return Options{Host: hostOptions, ReadyWriter: readyWriter, ShutdownTimeout: 25 * time.Second}, nil
}

// recognizedClientProtections reads the operator switch once, at start-up
// (ADR 0026). A value that is neither on nor off is reported on one line and
// otherwise ignored, so the App still shows the protections as on and an
// operator who expected them off sees at once that the switch did not take.
func recognizedClientProtections(
	value string,
	report io.Writer,
) clientadapter.ProtectionSwitch {
	state, recognized := clientadapter.ParseProtectionSwitch(value)
	if !recognized && report != nil {
		_, _ = fmt.Fprintf(
			report,
			"vibermated: ignoring %s=%q (use on or off); recognized-client protections stay on\n",
			clientadapter.RecognizedProtectionsVariable,
			value,
		)
	}
	return state
}

func Run(ctx context.Context, options Options) error {
	if ctx == nil || options.ReadyWriter == nil || options.ShutdownTimeout <= 0 {
		return errors.New("Server daemon options are incomplete")
	}
	host, err := serverhost.Start(ctx, options.Host)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(options.ReadyWriter).Encode(host.Status()); err != nil {
		shutdownContext, cancel := context.WithTimeout(context.Background(), options.ShutdownTimeout)
		defer cancel()
		return errors.Join(err, host.Shutdown(shutdownContext))
	}
	select {
	case <-ctx.Done():
	case <-host.Done():
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), options.ShutdownTimeout)
	defer cancel()
	return errors.Join(host.Failure(), host.Shutdown(shutdownContext))
}
