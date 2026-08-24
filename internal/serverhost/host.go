// Package serverhost composes one network-facing Runtime Server generation.
// It shares ProductRuntime with Desktop but owns different transport and
// authentication boundaries: TLS Server identity, Runtime User login,
// remote companion evidence, and a client-local proxy relay.
package serverhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturegrant"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runtimecontrol"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/serveradmin"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
)

type synchronizedReader struct {
	mu     sync.Mutex
	source io.Reader
}

type runtimeReadiness struct{ runtime *productruntime.Runtime }

func (readiness runtimeReadiness) Ready() bool {
	return readiness.runtime != nil &&
		readiness.runtime.Status().State == productruntime.RuntimeStateInitialized
}

func (reader *synchronizedReader) Read(destination []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.source.Read(destination)
}

type Status struct {
	Ready              bool   `json:"ready"`
	InstanceID         string `json:"instanceId"`
	ListenAddress      string `json:"listenAddress"`
	Scheme             string `json:"scheme"`
	TLSFingerprint     string `json:"tlsFingerprint"`
	AdminAccessKeyPath string `json:"adminAccessKeyPath"`
	ManagementUI       bool   `json:"managementUi"`
}

type Host struct {
	runtime        *productruntime.Runtime
	admin          *serveradmin.Authority
	guard          *instanceguard.Guard
	listener       net.Listener
	server         *http.Server
	address        string
	scheme         string
	fingerprint    string
	shutdown       time.Duration
	managementUI   bool
	ownsRuntime    bool
	managementHTTP http.Handler

	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
	failureMu sync.Mutex
	serveErr  error
}

func Start(ctx context.Context, options Options) (*Host, error) {
	if ctx == nil {
		return nil, errors.New("Runtime Server startup context is nil")
	}
	if err := options.validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	random := &synchronizedReader{source: options.Runtime.SecurityRandom}
	options.Runtime.SecurityRandom = random
	guard, err := instanceguard.Acquire(
		filepath.Join(options.Runtime.Paths.DataDirectory(), "server.lock"),
	)
	if err != nil {
		return nil, fmt.Errorf("acquire Runtime Server generation: %w", err)
	}
	runtime, err := productruntime.Start(ctx, options.Runtime)
	if err != nil {
		_ = guard.Release()
		return nil, err
	}
	attached := DefaultAttachOptions(
		runtime,
		options.Runtime.Paths.DataDirectory(),
		options.Runtime.Clock,
		random,
	)
	attached.ListenAddress = options.ListenAddress
	attached.Transport = options.Transport
	attached.ManagementUIRoot = options.ManagementUIRoot
	attached.ClientCatalog = options.ClientCatalog
	attached.AdminSessionLifetime = options.AdminSessionLifetime
	attached.CaptureRunLifetime = options.CaptureRunLifetime
	attached.ShutdownTimeout = options.ShutdownTimeout
	host, err := startAttached(ctx, attached, guard, true)
	if err == nil {
		return host, nil
	}
	shutdownContext, cancel := context.WithTimeout(
		context.Background(),
		options.ShutdownTimeout,
	)
	defer cancel()
	return nil, errors.Join(err, runtime.Shutdown(shutdownContext), guard.Release())
}

// Attach exposes the network Server boundary around an existing ProductRuntime.
// The returned Host owns its listener, TLS/admin identities, settings and
// generation lock, but never shuts down the supplied ProductRuntime.
func Attach(ctx context.Context, options AttachOptions) (*Host, error) {
	if ctx == nil {
		return nil, errors.New("attached Runtime Server startup context is nil")
	}
	if err := options.validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options.SecurityRandom = &synchronizedReader{source: options.SecurityRandom}
	guard, err := instanceguard.Acquire(
		filepath.Join(options.DataDirectory, "server.lock"),
	)
	if err != nil {
		return nil, fmt.Errorf("acquire Runtime Server generation: %w", err)
	}
	host, err := startAttached(ctx, options, guard, false)
	if err != nil {
		return nil, errors.Join(err, guard.Release())
	}
	return host, nil
}

func startAttached(
	ctx context.Context,
	options AttachOptions,
	guard *instanceguard.Guard,
	ownsRuntime bool,
) (*Host, error) {
	listener, err := net.Listen("tcp", options.ListenAddress)
	if err != nil {
		return nil, fmt.Errorf("listen for Runtime Server: %w", err)
	}
	started := false
	defer func() {
		if !started {
			_ = listener.Close()
		}
	}()
	admin, err := serveradmin.Open(serveradmin.Options{
		DataDirectory:   filepath.Join(options.DataDirectory, "server-admin"),
		Clock:           options.Clock,
		Random:          options.SecurityRandom,
		SessionLifetime: options.AdminSessionLifetime,
	})
	if err != nil {
		return nil, fmt.Errorf("open Runtime Server admin authority: %w", err)
	}
	managementUI, err := newManagementUI(options.ManagementUIRoot)
	if err != nil {
		return nil, err
	}
	runtime := options.Runtime
	application, err := runtimecontrol.New(runtimecontrol.Options{
		Runtime: runtime, Readiness: runtimeReadiness{runtime: runtime},
		Clock: options.Clock, ResolveLocalIdentities: options.ResolveLocalIdentities,
	})
	if err != nil {
		return nil, fmt.Errorf("build Runtime Server management application: %w", err)
	}
	verifier, err := clientadapter.NewReleaseVerifier(options.ClientCatalog)
	if err != nil {
		return nil, err
	}
	authorities, err := capturegrant.NewEnvironmentAuthorityResolver(
		runtime.CaptureAssignments(), runtime.EnvironmentResolver(),
	)
	if err != nil {
		return nil, err
	}
	issuer, err := capturegrant.New(capturegrant.Options{
		Runs: runtime.CaptureRuns(), ManualCaptures: runtime.ManualCaptures(),
		Verifier: verifier, Authorities: authorities,
		ProxyDelivery: capturegrant.ProxyDeliveryClientRelay,
		Generation:    runtime.Status().InstanceID,
		RootIdentity:  runtime.LocalRootIdentity(), Root: runtime.LocalRootCertificate(),
		RunLifetime:      options.CaptureRunLifetime,
		Workspaces:       capturegrant.NewCompanionWorkspaceResolver(),
		CompanionCatalog: options.ClientCatalog,
	})
	if err != nil {
		return nil, err
	}
	manual, err := capturecontrol.NewManualHandler(issuer)
	if err != nil {
		return nil, err
	}
	capture, err := capturecontrol.New(capturecontrol.Options{
		Runs:       runtime.CaptureRuns(),
		Principals: runtimeUserAuthenticator{users: runtime.RuntimeUsers()}, Issuer: issuer,
		Manual: manual, RunLifetime: options.CaptureRunLifetime,
	})
	if err != nil {
		return nil, err
	}
	userSessions, err := servercontrol.NewRuntimeUserSessions(
		servercontrol.RuntimeUserSessionsOptions{
			InstanceID: runtime.Status().InstanceID,
			Users:      runtime.RuntimeUsers(),
		},
	)
	if err != nil {
		return nil, err
	}
	usage, err := runtimeusage.New(runtimeusage.Options{
		Users: runtime.RuntimeUsers(), Runs: runtime.CaptureRunReader(),
		Activities: runtime.Activities(), Contents: runtime.ExchangeContents(),
		Identities: runtime.ConversationIdentities(), Clock: options.Clock,
	})
	if err != nil {
		return nil, err
	}
	runtimeUsers, err := servercontrol.NewRuntimeUsers(servercontrol.RuntimeUsersOptions{
		Users: runtime.RuntimeUsers(), Usage: usage,
	})
	if err != nil {
		return nil, err
	}
	adminSessions, err := servercontrol.NewAdminSessions(servercontrol.AdminSessionsOptions{
		InstanceID: runtime.Status().InstanceID, Authority: admin,
	})
	if err != nil {
		return nil, err
	}
	transport, err := prepareTransport(
		ctx,
		listener,
		options.Transport,
		options.DataDirectory,
		options.SecurityRandom,
		options.Clock.Now().UTC(),
	)
	if err != nil {
		return nil, err
	}
	serverAccess, err := servercontrol.NewServerAccess(
		servercontrol.ServerAccessOptions{Transport: transport.scheme},
	)
	if err != nil {
		return nil, err
	}
	localManagement := serverManagementRouter{
		access: serverAccess, runtimeUsers: runtimeUsers,
	}
	host := &Host{
		runtime: runtime, admin: admin,
		guard:    guard,
		listener: transport.listener, address: listener.Addr().String(),
		scheme: transport.scheme, fingerprint: transport.fingerprint,
		shutdown:       options.ShutdownTimeout,
		managementUI:   managementUI != nil,
		ownsRuntime:    ownsRuntime,
		managementHTTP: localManagement,
		done:           make(chan struct{}),
	}
	host.server = &http.Server{
		Handler: router{
			scheme:       transport.scheme,
			userSessions: userSessions, runtimeUsers: runtimeUsers, access: serverAccess,
			capture: capture, proxy: runtime.ProxyHandler(),
			adminSessions: adminSessions, admin: admin,
			application: application, managementUI: managementUI,
		},
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	go host.serve()
	started = true
	return host, nil
}

func (host *Host) serve() {
	err := host.server.Serve(host.listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		host.failureMu.Lock()
		host.serveErr = fmt.Errorf("serve Runtime Server: %w", err)
		host.failureMu.Unlock()
	}
	close(host.done)
}

func (host *Host) Status() Status {
	if host == nil || host.runtime == nil {
		return Status{}
	}
	return Status{
		Ready:              host.runtime.Status().State == productruntime.RuntimeStateInitialized,
		InstanceID:         host.runtime.Status().InstanceID,
		ListenAddress:      host.address,
		Scheme:             host.scheme,
		TLSFingerprint:     host.fingerprint,
		AdminAccessKeyPath: host.admin.AccessKeyPath(),
		ManagementUI:       host.managementUI,
	}
}

func (host *Host) Runtime() *productruntime.Runtime { return host.runtime }
func (host *Host) Done() <-chan struct{}            { return host.done }
func (host *Host) ManagementHandler() http.Handler {
	if host == nil {
		return nil
	}
	return host.managementHTTP
}

func (host *Host) Failure() error {
	if host == nil {
		return nil
	}
	host.failureMu.Lock()
	defer host.failureMu.Unlock()
	return host.serveErr
}

func (host *Host) Shutdown(ctx context.Context) error {
	if host == nil {
		return nil
	}
	host.closeOnce.Do(func() {
		shutdownContext := ctx
		var cancel context.CancelFunc
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			shutdownContext, cancel = context.WithTimeout(ctx, host.shutdown)
			defer cancel()
		}
		serverErr := host.server.Shutdown(shutdownContext)
		select {
		case <-host.done:
		case <-shutdownContext.Done():
			serverErr = errors.Join(serverErr, fmt.Errorf(
				"wait for Runtime Server listener: %w", shutdownContext.Err(),
			))
		}
		var runtimeErr error
		if host.ownsRuntime {
			runtimeErr = host.runtime.Shutdown(shutdownContext)
		}
		host.closeErr = errors.Join(
			serverErr, host.Failure(), runtimeErr, host.guard.Release(),
		)
	})
	return host.closeErr
}
