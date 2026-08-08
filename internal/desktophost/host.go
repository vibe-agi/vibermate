package desktophost

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturegrant"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/desktopbootstrap"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/productruntime"
)

const capabilityBytes = 32

type lockedReader struct {
	mu     sync.Mutex
	source io.Reader
}

func (reader *lockedReader) Read(destination []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.source.Read(destination)
}

type State string

const (
	StateStarting   State = "starting"
	StateReady      State = "ready"
	StateDegraded   State = "degraded"
	StateStopping   State = "stopping"
	StateStopped    State = "stopped"
	StateStopFailed State = "stop_failed"
)

const (
	StopReasonShutdownFailed = "host_shutdown_failed"
	StopReasonListenerFailed = "host_listener_failed"
)

// Status is the safe Host publication state. It never contains a capability.
type Status struct {
	State          State      `json:"state"`
	Ready          bool       `json:"ready"`
	InstanceID     string     `json:"instanceId"`
	ControlBaseURL string     `json:"controlBaseUrl,omitempty"`
	ProxyOrigin    string     `json:"proxyOrigin,omitempty"`
	StartedAt      time.Time  `json:"startedAt"`
	StoppedAt      *time.Time `json:"stoppedAt,omitempty"`
	StopReasonCode string     `json:"stopReasonCode,omitempty"`
}

type AppSession = desktopbootstrap.Session

type readiness struct {
	published atomic.Bool
	runtime   *productruntime.Runtime
}

func (state *readiness) Ready() bool {
	if state == nil || !state.published.Load() || state.runtime == nil {
		return false
	}
	return state.runtime.Status().State == productruntime.RuntimeStateInitialized
}

// Host owns one complete Desktop generation.
type Host struct {
	runtime              *productruntime.Runtime
	guard                *instanceguard.Guard
	discovery            *localdiscovery.File
	bootstrap            *desktopbootstrap.Authority
	descriptor           desktopbootstrap.Descriptor
	router               *desktopcontrol.Router
	readiness            *readiness
	controlServer        *http.Server
	proxyServer          *http.Server
	control              *trackedListener
	proxy                *trackedListener
	appSession           AppSession
	instanceID           string
	shutdownLimit        time.Duration
	clock                productruntime.Clock
	cliControl           *controlprincipal.Authority
	cliControlCredential string
	discoveryTTL         time.Duration
	controlBaseURL       string
	processID            int

	statusMu sync.RWMutex
	status   Status

	closing      atomic.Bool
	committed    atomic.Bool
	shutdownOnce sync.Once
	shutdownDone chan struct{}
	shutdownErr  error

	failureMu       sync.Mutex
	failureRoot     error
	serveWG         sync.WaitGroup
	discoveryWG     sync.WaitGroup
	discoveryCancel context.CancelCauseFunc
}

// Start establishes both listeners and every route before atomically
// publishing local control discovery. The returned Host is externally ready.
func Start(ctx context.Context, options Options) (*Host, error) {
	if ctx == nil {
		return nil, errors.New("Desktop Host startup context is nil")
	}
	if err := options.validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	random := &lockedReader{source: options.Runtime.SecurityRandom}
	options.Runtime.SecurityRandom = random
	verifier, err := clientadapter.NewReleaseVerifier(options.ClientCatalog)
	if err != nil {
		return nil, fmt.Errorf("build fixed ClientAdapter catalog: %w", err)
	}
	guard, err := instanceguard.Acquire(options.Paths.LockPath())
	if err != nil {
		return nil, fmt.Errorf("acquire Desktop generation: %w", err)
	}
	var rollback startupRollback
	rollback.register("generation lock", func(context.Context) error {
		return guard.Release()
	})
	fail := func(stage string, root error) (*Host, error) {
		rollbackContext, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			options.ShutdownTimeout,
		)
		defer cancel()
		return nil, errors.Join(
			fmt.Errorf("start Desktop Host stage %q: %w", stage, root),
			rollback.run(rollbackContext),
		)
	}
	discovery, err := localdiscovery.NewPublisher(
		options.Paths.DiscoveryPath(),
		options.Runtime.Clock,
		guard,
	)
	if err != nil {
		return fail("local control discovery ownership", err)
	}

	cliControlCredential, err := randomCapability(options.Runtime.SecurityRandom)
	if err != nil {
		return fail("CLI control credential", err)
	}
	readToken, err := randomCapability(options.Runtime.SecurityRandom)
	if err != nil {
		return fail("App read capability", err)
	}
	writeToken, err := randomCapability(options.Runtime.SecurityRandom)
	if err != nil {
		return fail("App write capability", err)
	}
	bootstrapNonce, err := randomCapability(options.Runtime.SecurityRandom)
	if err != nil {
		return fail("native-shell bootstrap nonce", err)
	}
	if cliControlCredential == readToken ||
		cliControlCredential == writeToken ||
		cliControlCredential == bootstrapNonce ||
		readToken == writeToken ||
		readToken == bootstrapNonce ||
		writeToken == bootstrapNonce {
		return fail("capability separation", errors.New("random capabilities collided"))
	}

	runtime, err := productruntime.Start(ctx, options.Runtime)
	if err != nil {
		return fail("ProductRuntime", err)
	}
	rollback.register("ProductRuntime", runtime.Shutdown)

	proxyListener, err := listenLoopback(ctx, options.ProxyListenAddress)
	if err != nil {
		return fail("proxy listener", err)
	}
	proxyTracked := newTrackedListener(proxyListener)
	rollback.register("proxy listener", func(context.Context) error {
		return normalizeClose(proxyTracked.Close())
	})
	proxyOrigin := "http://" + proxyTracked.Addr().String()

	controlListener, err := listenLoopback(ctx, options.ControlListenAddress)
	if err != nil {
		return fail("control listener", err)
	}
	controlTracked := newTrackedListener(controlListener)
	rollback.register("control listener", func(context.Context) error {
		return normalizeClose(controlTracked.Close())
	})
	controlBaseURL := "http://" + controlTracked.Addr().String()

	now := options.Runtime.Clock.Now().UTC()
	discoveryExpiresAt := now.Add(options.CLIControlDiscoveryTTL)
	bootstrapExpiresAt := now.Add(options.BootstrapTTL)
	appExpiresAt := now.Add(options.AppSessionTTL)
	appSession := AppSession{
		Schema:     desktopbootstrap.SessionSchema,
		BaseURL:    controlBaseURL,
		ReadToken:  readToken,
		WriteToken: writeToken,
		InstanceID: runtime.Status().InstanceID,
		ExpiresAt:  appExpiresAt,
	}
	bootstrapAuthority, err := desktopbootstrap.New(
		desktopbootstrap.Grant{
			Nonce:     bootstrapNonce,
			ExpiresAt: bootstrapExpiresAt,
			Session:   appSession,
		},
		options.Runtime.Clock,
	)
	if err != nil {
		return fail("native-shell bootstrap authority", err)
	}
	localPrincipal, err := controlprincipal.New(controlprincipal.Attributes{
		ID:                 "local-cli:" + runtime.Status().InstanceID,
		Kind:               controlprincipal.KindLocalCLI,
		CredentialRevision: 1,
		AllowedGrantKinds: []controlprincipal.GrantKind{
			controlprincipal.GrantCaptureRun,
			controlprincipal.GrantManualCapture,
		},
	})
	if err != nil {
		return fail("local CLI principal", err)
	}
	cliControl, err := controlprincipal.NewAuthority(
		controlprincipal.CredentialGrant{
			Credential: cliControlCredential,
			Principal:  localPrincipal,
		},
	)
	if err != nil {
		return fail("CLI control authority", err)
	}
	workspaceResolver, err := capturegrant.NewLocalWorkspaceResolver(
		runtime.WorkspaceIdentity(),
	)
	if err != nil {
		return fail("CaptureRun workspace adapter", err)
	}
	captureAuthorities, err := capturegrant.NewEnvironmentAuthorityResolver(
		runtime.CaptureAssignments(),
		runtime.EnvironmentResolver(),
	)
	if err != nil {
		return fail("Capture Environment authority", err)
	}
	grantIssuer, err := capturegrant.New(capturegrant.Options{
		Runs:           runtime.CaptureRuns(),
		ManualCaptures: runtime.ManualCaptures(),
		Verifier:       verifier,
		Authorities:    captureAuthorities,
		ProxyOrigin:    proxyOrigin,
		Generation:     runtime.Status().InstanceID,
		RootIdentity:   runtime.LocalRootIdentity(),
		Root:           runtime.LocalRootCertificate(),
		RunLifetime:    options.CaptureRunLifetime,
		Workspaces:     workspaceResolver,
		// The same authority that asks about a connection asks about handing
		// a recognized client the Root, so both questions reach a person the
		// same way and appear in the same place.
		ClientRootApprovals: runtime.ClientRootApprovals(),
	})
	if err != nil {
		return fail("capture grant issuer", err)
	}
	manualCaptureHandler, err := capturecontrol.NewManualHandler(grantIssuer)
	if err != nil {
		return fail("ManualCapture control routes", err)
	}
	captureHandler, err := capturecontrol.New(capturecontrol.Options{
		Runs:        runtime.CaptureRuns(),
		Principals:  cliControl,
		Issuer:      grantIssuer,
		Manual:      manualCaptureHandler,
		RunLifetime: options.CaptureRunLifetime,
	})
	if err != nil {
		return fail("capture control routes", err)
	}
	authenticator, err := desktopcontrol.NewAuthenticator(
		desktopcontrol.CapabilityGrant{
			ReadToken:  readToken,
			WriteToken: writeToken,
			ExpiresAt:  appExpiresAt,
			Revision:   1,
			Rotation: &desktopcontrol.SessionRotationPolicy{
				Lifetime:  options.AppSessionTTL,
				ReplayTTL: options.AppSessionReplayTTL,
				Random:    random,
			},
		},
		options.Runtime.Clock,
	)
	if err != nil {
		return fail("App capability authority", err)
	}
	ready := &readiness{runtime: runtime}
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness:       ready,
		Status:          runtime,
		Environments:    runtime.Environments(),
		Assignments:     runtime.CaptureAssignments(),
		Activities:      runtime.Activities(),
		Connections:     runtime.ConnectionEvents(),
		Egress:          runtime.EgressAttempts(),
		Approvals:       runtime.ToolApprovals(),
		Accounts:        runtime.ProviderAccounts(),
		Offline:         runtime,
		ConnectionRules: runtime.ConnectionRules(),
		CaptureRuns:     runtime.CaptureRunReader(),
		ManualCaptures:  runtime.ManualCaptures(),
		Clock:           options.Runtime.Clock,
	})
	if err != nil {
		return fail("App control routes", err)
	}
	desktopPrincipal, err := controlprincipal.New(controlprincipal.Attributes{
		ID:                 "desktop-app:" + runtime.Status().InstanceID,
		Kind:               controlprincipal.KindDesktopApp,
		CredentialRevision: 1,
		AllowedGrantKinds: []controlprincipal.GrantKind{
			controlprincipal.GrantManualCapture,
		},
	})
	if err != nil {
		return fail("Desktop App principal", err)
	}
	router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{
		Authority:        controlTracked.Addr().String(),
		AllowedOrigins:   append([]string(nil), options.AllowedOrigins...),
		Authenticator:    authenticator,
		Application:      application,
		Bootstrap:        bootstrapAuthority,
		CLIControl:       captureHandler,
		ManualCaptures:   manualCaptureHandler,
		DesktopPrincipal: desktopPrincipal,
	})
	if err != nil {
		return fail("control router", err)
	}

	host := &Host{
		runtime:    runtime,
		guard:      guard,
		discovery:  discovery,
		bootstrap:  bootstrapAuthority,
		router:     router,
		readiness:  ready,
		control:    controlTracked,
		proxy:      proxyTracked,
		appSession: appSession,
		descriptor: desktopbootstrap.Descriptor{
			Schema:         desktopbootstrap.DescriptorSchema,
			InstanceID:     runtime.Status().InstanceID,
			ProcessID:      os.Getpid(),
			BaseURL:        controlBaseURL,
			APIVersions:    []string{"v1"},
			EventVersions:  []string{},
			BootstrapNonce: bootstrapNonce,
		},
		instanceID:           runtime.Status().InstanceID,
		shutdownLimit:        options.ShutdownTimeout,
		clock:                options.Runtime.Clock,
		cliControl:           cliControl,
		cliControlCredential: cliControlCredential,
		discoveryTTL:         options.CLIControlDiscoveryTTL,
		controlBaseURL:       controlBaseURL,
		processID:            os.Getpid(),
		status: Status{
			State:          StateStarting,
			InstanceID:     runtime.Status().InstanceID,
			ControlBaseURL: controlBaseURL,
			ProxyOrigin:    proxyOrigin,
			StartedAt:      now,
		},
		shutdownDone: make(chan struct{}),
	}
	host.controlServer = newHTTPServer(router)
	host.proxyServer = newHTTPServer(runtime.ProxyHandler())
	discoveryContext, cancelDiscovery := context.WithCancelCause(context.Background())
	host.discoveryCancel = cancelDiscovery
	rollback.register("CLI control discovery refresh owner", func(context.Context) error {
		cancelDiscovery(errors.New("Desktop Host startup rolled back"))
		return nil
	})
	routerStarted := false
	defer func() {
		if !routerStarted {
			router.BeginShutdown()
		}
	}()
	rollback.register("server goroutines", func(context.Context) error {
		host.serveWG.Wait()
		return nil
	})
	host.startServing("proxy", host.proxyServer, proxyTracked)
	rollback.register("proxy server", func(shutdownContext context.Context) error {
		return stopHTTPServer(shutdownContext, host.proxyServer, proxyTracked)
	})
	host.startServing("control", host.controlServer, controlTracked)
	rollback.register("control server", func(shutdownContext context.Context) error {
		return stopHTTPServer(shutdownContext, host.controlServer, controlTracked)
	})

	session := localdiscovery.Session{
		Schema:            localdiscovery.Schema,
		InstanceID:        host.instanceID,
		ProcessID:         host.processID,
		BaseURL:           controlBaseURL,
		ControlCredential: cliControlCredential,
		ExpiresAt:         discoveryExpiresAt,
	}
	if err := discovery.Publish(session); err != nil {
		host.closing.Store(true)
		router.BeginShutdown()
		return fail("local control discovery publication", err)
	}
	rollback.register("local control discovery", func(context.Context) error {
		return discovery.Remove(host.instanceID)
	})

	host.failureMu.Lock()
	startupServeErr := host.failureRoot
	if startupServeErr == nil {
		host.committed.Store(true)
	}
	host.failureMu.Unlock()
	if startupServeErr != nil {
		host.closing.Store(true)
		router.BeginShutdown()
		return fail("listener activation", startupServeErr)
	}
	ready.published.Store(true)
	host.statusMu.Lock()
	host.status.State = StateReady
	host.status.Ready = true
	host.statusMu.Unlock()
	host.discoveryWG.Add(1)
	go host.refreshCLIControlDiscovery(discoveryContext)
	routerStarted = true
	rollback = startupRollback{}
	return host, nil
}

func (host *Host) Status() Status {
	if host == nil {
		return Status{}
	}
	host.statusMu.RLock()
	status := host.status
	host.statusMu.RUnlock()
	status.Ready = host.readiness.Ready()
	if !status.Ready && status.State == StateReady {
		status.State = StateDegraded
	}
	return status
}

func (host *Host) Ready() bool {
	return host != nil && host.readiness.Ready()
}

func (host *Host) AppSession() AppSession {
	if host == nil {
		return AppSession{}
	}
	return host.appSession
}

func (host *Host) Bootstrap() desktopbootstrap.Descriptor {
	if host == nil {
		return desktopbootstrap.Descriptor{}
	}
	return host.descriptor.Clone()
}

func (host *Host) Runtime() *productruntime.Runtime {
	if host == nil {
		return nil
	}
	return host.runtime
}

func (host *Host) DiscoveryPath() string {
	if host == nil || host.discovery == nil {
		return ""
	}
	return host.discovery.Path()
}

func (host *Host) Done() <-chan struct{} {
	if host == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return host.shutdownDone
}

func (host *Host) Shutdown(ctx context.Context) error {
	if host == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("Desktop Host shutdown context is nil")
	}
	host.beginShutdown(nil)
	select {
	case <-host.shutdownDone:
		return host.shutdownErr
	case <-ctx.Done():
		return fmt.Errorf("wait for Desktop Host shutdown: %w", ctx.Err())
	}
}

func (host *Host) beginShutdown(root error) {
	if root != nil {
		host.failureMu.Lock()
		host.failureRoot = errors.Join(host.failureRoot, root)
		host.failureMu.Unlock()
	}
	host.shutdownOnce.Do(func() {
		host.closing.Store(true)
		host.readiness.published.Store(false)
		if host.discoveryCancel != nil {
			host.discoveryCancel(errors.New("Desktop Host is stopping"))
		}
		host.cliControl.Revoke()
		host.bootstrap.Revoke()
		host.router.BeginShutdown()
		host.statusMu.Lock()
		host.status.State = StateStopping
		host.status.Ready = false
		if root != nil {
			host.status.StopReasonCode = StopReasonListenerFailed
		}
		host.statusMu.Unlock()
		go host.executeShutdown()
	})
}

func (host *Host) executeShutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), host.shutdownLimit)
	defer cancel()
	host.failureMu.Lock()
	root := host.failureRoot
	host.failureMu.Unlock()
	var shutdownErr error
	host.discoveryWG.Wait()
	if err := host.discovery.Remove(host.instanceID); err != nil {
		shutdownErr = errors.Join(shutdownErr, fmt.Errorf("withdraw local control discovery: %w", err))
	}
	if err := normalizeClose(host.control.Close()); err != nil {
		shutdownErr = errors.Join(shutdownErr, fmt.Errorf("close control admission: %w", err))
	}
	if err := normalizeClose(host.proxy.Close()); err != nil {
		shutdownErr = errors.Join(shutdownErr, fmt.Errorf("close proxy admission: %w", err))
	}
	if err := stopControlServer(ctx, host.controlServer, host.control); err != nil {
		shutdownErr = errors.Join(shutdownErr, fmt.Errorf("stop control server: %w", err))
	}
	if err := host.runtime.Shutdown(ctx); err != nil {
		shutdownErr = errors.Join(shutdownErr, fmt.Errorf("stop ProductRuntime: %w", err))
	}
	if err := stopHTTPServer(ctx, host.proxyServer, host.proxy); err != nil {
		shutdownErr = errors.Join(shutdownErr, fmt.Errorf("stop proxy server: %w", err))
	}
	host.serveWG.Wait()
	shutdownErr = errors.Join(root, shutdownErr)
	if shutdownErr == nil {
		shutdownErr = host.guard.Release()
	}
	host.shutdownErr = shutdownErr
	now := host.clock.Now().UTC()
	host.statusMu.Lock()
	if shutdownErr != nil {
		host.status.State = StateStopFailed
		if host.status.StopReasonCode == "" {
			host.status.StopReasonCode = StopReasonShutdownFailed
		}
	} else {
		host.status.State = StateStopped
		host.status.StoppedAt = &now
	}
	host.status.Ready = false
	host.statusMu.Unlock()
	close(host.shutdownDone)
}

func (host *Host) startServing(
	name string,
	server *http.Server,
	listener net.Listener,
) {
	host.serveWG.Add(1)
	go func() {
		defer host.serveWG.Done()
		err := server.Serve(listener)
		if err == nil ||
			errors.Is(err, http.ErrServerClosed) ||
			errors.Is(err, net.ErrClosed) ||
			host.closing.Load() {
			return
		}
		root := fmt.Errorf("%s listener failed: %w", name, err)
		host.failureMu.Lock()
		if !host.committed.Load() {
			host.failureRoot = errors.Join(host.failureRoot, root)
			host.failureMu.Unlock()
			return
		}
		host.failureMu.Unlock()
		host.beginShutdown(root)
	}()
}

func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
}

func listenLoopback(ctx context.Context, address string) (net.Listener, error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", address)
	if err != nil {
		return nil, err
	}
	host, port, splitErr := net.SplitHostPort(listener.Addr().String())
	if splitErr != nil || host != "127.0.0.1" || port == "" {
		_ = listener.Close()
		return nil, errors.New("Desktop Host bound a non-loopback listener")
	}
	return listener, nil
}

func randomCapability(source io.Reader) (string, error) {
	if source == nil {
		return "", errors.New("capability entropy source is missing")
	}
	value := make([]byte, capabilityBytes)
	if _, err := io.ReadFull(source, value); err != nil {
		return "", fmt.Errorf("read capability entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (host *Host) refreshCLIControlDiscovery(ctx context.Context) {
	defer host.discoveryWG.Done()
	interval := host.discoveryTTL / 2
	if interval <= 0 {
		host.beginShutdown(errors.New("CLI control discovery refresh interval is invalid"))
		return
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := host.publishFreshCLIControlDiscovery(); err != nil {
				host.beginShutdown(fmt.Errorf("refresh CLI control discovery: %w", err))
				return
			}
			timer.Reset(interval)
		}
	}
}

func (host *Host) publishFreshCLIControlDiscovery() error {
	return host.discovery.Publish(localdiscovery.Session{
		Schema:            localdiscovery.Schema,
		InstanceID:        host.instanceID,
		ProcessID:         host.processID,
		BaseURL:           host.controlBaseURL,
		ControlCredential: host.cliControlCredential,
		ExpiresAt:         host.clock.Now().UTC().Add(host.discoveryTTL),
	})
}

func stopHTTPServer(
	ctx context.Context,
	server *http.Server,
	listener *trackedListener,
) error {
	if server == nil {
		return nil
	}
	// Shutdown closes the server's listeners, and this path already closed
	// admission first, so an already-closed listener is the intended order
	// rather than a failure. Normalizing cannot mask a real fault because
	// these two errors mean exactly "already closed".
	shutdownErr := normalizeClose(server.Shutdown(ctx))
	var closeErr error
	if shutdownErr != nil {
		closeErr = normalizeClose(server.Close())
	}
	if listener != nil {
		listener.closeTracked()
	}
	return errors.Join(shutdownErr, closeErr)
}

// stopControlServer breaks the native Webview/control-server shutdown cycle.
// The Webview cannot finish exiting while its requests are alive, and a
// graceful http.Server shutdown cannot finish while those requests are alive.
// Control mutations are revisioned, idempotent, and transaction bounded, so
// canceling their request contexts at process shutdown is the correct edge;
// data-plane proxy connections continue to use the graceful path above.
func stopControlServer(
	ctx context.Context,
	server *http.Server,
	listener *trackedListener,
) error {
	if listener != nil {
		listener.closeTracked()
	}
	return stopHTTPServer(ctx, server, listener)
}

func normalizeClose(err error) error {
	if errors.Is(err, net.ErrClosed) ||
		errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

type startupCleanup struct {
	name string
	run  func(context.Context) error
}

type startupRollback struct {
	entries []startupCleanup
}

func (stack *startupRollback) register(
	name string,
	cleanup func(context.Context) error,
) {
	stack.entries = append(stack.entries, startupCleanup{name: name, run: cleanup})
}

func (stack *startupRollback) run(ctx context.Context) error {
	var result error
	for index := len(stack.entries) - 1; index >= 0; index-- {
		entry := stack.entries[index]
		if err := entry.run(ctx); err != nil {
			result = errors.Join(
				result,
				fmt.Errorf("rollback %s: %w", entry.name, err),
			)
		}
	}
	stack.entries = nil
	return result
}
