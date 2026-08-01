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
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/desktopbootstrap"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/launcherdiscovery"
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
	runtime        *productruntime.Runtime
	guard          *instanceguard.Guard
	discovery      *launcherdiscovery.File
	bootstrap      *desktopbootstrap.Authority
	descriptor     desktopbootstrap.Descriptor
	router         *desktopcontrol.Router
	readiness      *readiness
	controlServer  *http.Server
	proxyServer    *http.Server
	control        *trackedListener
	proxy          *trackedListener
	appSession     AppSession
	instanceID     string
	shutdownLimit  time.Duration
	clock          productruntime.Clock
	random         io.Reader
	launcher       *capturecontrol.LauncherAuthority
	launcherTTL    time.Duration
	controlBaseURL string
	processID      int

	statusMu sync.RWMutex
	status   Status

	closing      atomic.Bool
	committed    atomic.Bool
	shutdownOnce sync.Once
	shutdownDone chan struct{}
	shutdownErr  error

	failureMu      sync.Mutex
	failureRoot    error
	serveWG        sync.WaitGroup
	rotationWG     sync.WaitGroup
	rotationCancel context.CancelCauseFunc
}

// Start establishes both listeners and every route before atomically
// publishing launcher discovery. The returned Host is externally ready.
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
	discovery, err := launcherdiscovery.NewPublisher(
		options.Paths.DiscoveryPath(),
		options.Runtime.Clock,
		guard,
	)
	if err != nil {
		return fail("launcher discovery ownership", err)
	}

	launcherToken, err := randomCapability(options.Runtime.SecurityRandom)
	if err != nil {
		return fail("launcher capability", err)
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
	if launcherToken == readToken ||
		launcherToken == writeToken ||
		launcherToken == bootstrapNonce ||
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
	launcherExpiresAt := now.Add(options.LauncherTTL)
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
	launcherAuthority, err := capturecontrol.NewLauncherAuthority(
		capturecontrol.LauncherGrant{
			Token:     launcherToken,
			ExpiresAt: launcherExpiresAt,
		},
		options.Runtime.Clock,
	)
	if err != nil {
		return fail("launcher capability authority", err)
	}
	captureHandler, err := capturecontrol.New(capturecontrol.Options{
		Runs:        runtime.CaptureRuns(),
		Verifier:    verifier,
		Authorities: runtime,
		ProxyOrigin: proxyOrigin,
		Root:        runtime.LocalRootCertificate(),
		Launcher:    launcherAuthority,
		RunLifetime: options.CaptureRunLifetime,
		Clock:       options.Runtime.Clock,
	})
	if err != nil {
		return fail("CaptureRun routes", err)
	}
	authenticator, err := desktopcontrol.NewAuthenticator(
		desktopcontrol.CapabilityGrant{
			ReadToken:  readToken,
			WriteToken: writeToken,
			ExpiresAt:  appExpiresAt,
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
		Accesses:        runtime.AccessWriter(),
		Resolver:        runtime.SnapshotResolver(),
		Credentials:     runtime.Credentials(),
		Activities:      runtime.Activities(),
		Connections:     runtime.ConnectionEvents(),
		Egress:          runtime.EgressAttempts(),
		Approvals:       runtime.ToolApprovals(),
		Offline:         runtime,
		ConnectionRules: runtime.ConnectionRules(),
		CaptureRuns:     runtime.CaptureRunReader(),
	})
	if err != nil {
		return fail("App control routes", err)
	}
	router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{
		Authority:      controlTracked.Addr().String(),
		AllowedOrigins: append([]string(nil), options.AllowedOrigins...),
		Authenticator:  authenticator,
		Application:    application,
		Bootstrap:      bootstrapAuthority,
		CaptureRuns:    captureHandler,
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
		instanceID:     runtime.Status().InstanceID,
		shutdownLimit:  options.ShutdownTimeout,
		clock:          options.Runtime.Clock,
		random:         random,
		launcher:       launcherAuthority,
		launcherTTL:    options.LauncherTTL,
		controlBaseURL: controlBaseURL,
		processID:      os.Getpid(),
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
	rotationContext, cancelRotation := context.WithCancelCause(context.Background())
	host.rotationCancel = cancelRotation
	rollback.register("launcher refresh owner", func(context.Context) error {
		cancelRotation(errors.New("Desktop Host startup rolled back"))
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

	session := launcherdiscovery.Session{
		Schema:        launcherdiscovery.SchemaV1,
		InstanceID:    host.instanceID,
		ProcessID:     host.processID,
		BaseURL:       controlBaseURL,
		LauncherToken: launcherToken,
		ExpiresAt:     launcherExpiresAt,
	}
	if err := discovery.Publish(session); err != nil {
		host.closing.Store(true)
		router.BeginShutdown()
		return fail("launcher discovery publication", err)
	}
	rollback.register("launcher discovery", func(context.Context) error {
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
	host.rotationWG.Add(1)
	go host.refreshLauncher(rotationContext)
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
		if host.rotationCancel != nil {
			host.rotationCancel(errors.New("Desktop Host is stopping"))
		}
		host.launcher.Revoke()
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
	host.rotationWG.Wait()
	if err := host.discovery.Remove(host.instanceID); err != nil {
		shutdownErr = errors.Join(shutdownErr, fmt.Errorf("withdraw launcher discovery: %w", err))
	}
	if err := normalizeClose(host.control.Close()); err != nil {
		shutdownErr = errors.Join(shutdownErr, fmt.Errorf("close control admission: %w", err))
	}
	if err := normalizeClose(host.proxy.Close()); err != nil {
		shutdownErr = errors.Join(shutdownErr, fmt.Errorf("close proxy admission: %w", err))
	}
	if err := stopHTTPServer(ctx, host.controlServer, host.control); err != nil {
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

func (host *Host) refreshLauncher(ctx context.Context) {
	defer host.rotationWG.Done()
	interval := host.launcherTTL / 2
	if interval <= 0 {
		host.beginShutdown(errors.New("launcher refresh interval is invalid"))
		return
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := host.rotateLauncher(); err != nil {
				host.beginShutdown(fmt.Errorf("rotate launcher capability: %w", err))
				return
			}
			timer.Reset(interval)
		}
	}
}

func (host *Host) rotateLauncher() error {
	token, err := randomCapability(host.random)
	if err != nil {
		return err
	}
	expiresAt := host.clock.Now().UTC().Add(host.launcherTTL)
	rotation, err := host.launcher.Prepare(capturecontrol.LauncherGrant{
		Token:     token,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = rotation.Abort()
		}
	}()
	if err := host.discovery.Publish(launcherdiscovery.Session{
		Schema:        launcherdiscovery.SchemaV1,
		InstanceID:    host.instanceID,
		ProcessID:     host.processID,
		BaseURL:       host.controlBaseURL,
		LauncherToken: token,
		ExpiresAt:     expiresAt,
	}); err != nil {
		return err
	}
	if err := rotation.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
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
