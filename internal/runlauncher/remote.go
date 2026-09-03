package runlauncher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/servertransport"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

type RemoteClock interface {
	Now() time.Time
}

type RemoteConfig struct {
	Target         serverconnection.Target
	StateDirectory string
	DisplayName    string
	Clock          RemoteClock
	Random         io.Reader
}

func (config RemoteConfig) validate() error {
	if !config.Target.Valid() || config.StateDirectory == "" ||
		!filepath.IsAbs(config.StateDirectory) ||
		filepath.Clean(config.StateDirectory) != config.StateDirectory ||
		config.DisplayName == "" || config.Clock == nil || config.Random == nil {
		return errors.New("remote Runtime Server configuration is incomplete")
	}
	return nil
}

type remoteConnection struct {
	control     *controlClient
	workspace   *workspaceidentity.Manager
	target      serverconnection.Target
	firstUse    bool
	fingerprint string
	relay       *localServerRelay
	transport   *servertransport.Transport
}

func connectRemote(
	ctx context.Context,
	config RemoteConfig,
	timeout time.Duration,
	cwd string,
	command []string,
	executable string,
) (*remoteConnection, *capturecontrol.CompanionAttestationInput, error) {
	if err := config.validate(); err != nil {
		return nil, nil, err
	}
	workspace, err := workspaceidentity.Open(
		ctx,
		filepath.Join(config.StateDirectory, "identity"),
		config.Random,
		config.Clock.Now().UTC(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("open remote companion identity: %w", err)
	}
	fail := func(root error) (*remoteConnection, *capturecontrol.CompanionAttestationInput, error) {
		_ = workspace.Shutdown(context.Background())
		return nil, nil, root
	}
	scope, err := workspace.ResolveLocal(ctx, cwd)
	if err != nil {
		return fail(fmt.Errorf("resolve remote companion workspace: %w", err))
	}
	catalog := clientadapter.BuiltInCatalog()
	verifier, err := clientadapter.NewReleaseVerifier(catalog)
	if err != nil {
		return fail(err)
	}
	detection, err := verifier.Verify(ctx, clientadapter.Request{
		Command: append([]string(nil), command...), CWD: cwd, ExecutablePath: executable,
	})
	if err != nil {
		return fail(fmt.Errorf("verify remote companion executable: %w", err))
	}
	transport, err := servertransport.Open(servertransport.Options{
		Target: config.Target, TrustDirectory: filepath.Join(config.StateDirectory, "trust"),
		Clock: config.Clock, Timeout: timeout,
	})
	if err != nil {
		return fail(err)
	}
	cleanupTransport := true
	defer func() {
		if cleanupTransport {
			transport.Close()
		}
	}()
	loginStore, err := serverconnection.OpenLoginStore(
		filepath.Join(config.StateDirectory, "login"),
	)
	if err != nil {
		return fail(err)
	}
	login, err := loginStore.Load(config.Target, config.Clock.Now().UTC())
	if err != nil {
		if errors.Is(err, serverconnection.ErrLoginRequired) {
			return fail(fmt.Errorf(
				"%w: run vibermate login --server %s",
				ErrRemoteLoginRequired,
				config.Target.Origin(),
			))
		}
		return fail(err)
	}
	control, err := newRemoteControlClient(
		config.Target.Origin(), login.SessionToken().Value(),
		transport, transport.Close,
	)
	if err != nil {
		return fail(err)
	}
	observedFirstUse, observedFingerprint := transport.Trust()
	cleanupTransport = false
	return &remoteConnection{
			control: control, workspace: workspace,
			target: config.Target, firstUse: observedFirstUse, fingerprint: observedFingerprint,
			transport: transport,
		}, &capturecontrol.CompanionAttestationInput{
			Detection: detection,
			Workspace: capturecontrol.CompanionWorkspaceInput{
				MachineID: scope.MachineID().String(), WorkspaceID: scope.WorkspaceID().String(),
				WorkspaceLabel:       scope.WorkspaceLabel(),
				RegistrationRevision: scope.RegistrationRevision(),
				DerivationRevision:   scope.DerivationRevision(),
			},
		}, nil
}

func (connection *remoteConnection) materialize(
	grant capturecontrol.LaunchGrant,
) (capturecontrol.LaunchGrant, func(), error) {
	if connection == nil || grant.ProxyDelivery != capturecontrol.ProxyDeliveryClientRelay ||
		grant.ProxyAddress != "" || grant.RootPEMPath != "" {
		return capturecontrol.LaunchGrant{}, nil, errors.New("Runtime Server returned an invalid remote launch grant")
	}
	if err := grant.Validate(); err != nil {
		return capturecontrol.LaunchGrant{}, nil, err
	}
	relay, err := startLocalServerRelay(connection.transport)
	if err != nil {
		return capturecontrol.LaunchGrant{}, nil, err
	}
	connection.relay = relay
	grant.ProxyAddress = relay.Origin()
	cleanupRoot := func() {}
	if grant.RootPEM != "" {
		rootDirectory, err := os.MkdirTemp("", "vibermate-remote-root-*")
		if err != nil {
			relay.Close()
			return capturecontrol.LaunchGrant{}, nil, err
		}
		rootPath := filepath.Join(rootDirectory, "root.pem")
		if err := os.WriteFile(rootPath, []byte(grant.RootPEM), 0o600); err != nil {
			_ = os.RemoveAll(rootDirectory)
			relay.Close()
			return capturecontrol.LaunchGrant{}, nil, err
		}
		grant.RootPEMPath = rootPath
		grant.RootPEM = ""
		cleanupRoot = func() { _ = os.RemoveAll(rootDirectory) }
	}
	if err := validateGrant(grant); err != nil {
		cleanupRoot()
		relay.Close()
		return capturecontrol.LaunchGrant{}, nil, err
	}
	return grant, cleanupRoot, nil
}

func (connection *remoteConnection) close() {
	if connection == nil {
		return
	}
	if connection.relay != nil {
		connection.relay.Close()
	}
	if connection.workspace != nil {
		_ = connection.workspace.Shutdown(context.Background())
	}
}

type localServerRelay struct {
	listener net.Listener
	dialer   serverStreamDialer
	done     chan struct{}
	mu       sync.Mutex
	active   map[net.Conn]struct{}
	closed   bool
	wg       sync.WaitGroup
	once     sync.Once
}

type serverStreamDialer interface {
	Dial(context.Context) (net.Conn, error)
}

func startLocalServerRelay(dialer serverStreamDialer) (*localServerRelay, error) {
	if dialer == nil {
		return nil, errors.New("remote proxy relay configuration is invalid")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for local remote-proxy relay: %w", err)
	}
	relay := &localServerRelay{
		listener: listener, dialer: dialer,
		done: make(chan struct{}), active: make(map[net.Conn]struct{}),
	}
	go relay.accept()
	return relay, nil
}

func (relay *localServerRelay) Origin() string {
	if relay == nil || relay.listener == nil {
		return ""
	}
	return "http://" + relay.listener.Addr().String()
}

func (relay *localServerRelay) accept() {
	defer close(relay.done)
	for {
		client, err := relay.listener.Accept()
		if err != nil {
			return
		}
		if !relay.admitClient(client) {
			_ = client.Close()
			return
		}
		go relay.forward(client)
	}
}

func (relay *localServerRelay) forward(client net.Conn) {
	defer relay.wg.Done()
	defer relay.remove(client)
	defer client.Close()
	server, err := relay.dialer.Dial(context.Background())
	if err != nil {
		return
	}
	if !relay.add(server) {
		_ = server.Close()
		return
	}
	defer relay.remove(server)
	defer server.Close()
	copyDone := make(chan struct{}, 1)
	go func() {
		_, _ = io.Copy(server, client)
		if closer, ok := server.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		copyDone <- struct{}{}
	}()
	_, _ = io.Copy(client, server)
	// The Runtime Server is authoritative for stream lifetime. Once it ends its
	// direction, close the local side so a client that kept its write half open
	// cannot strand the reverse copier and the relay forever.
	_ = client.Close()
	<-copyDone
}

func (relay *localServerRelay) admitClient(connection net.Conn) bool {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if relay.closed {
		return false
	}
	relay.active[connection] = struct{}{}
	relay.wg.Add(1)
	return true
}

func (relay *localServerRelay) add(connection net.Conn) bool {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if relay.closed {
		return false
	}
	relay.active[connection] = struct{}{}
	return true
}

func (relay *localServerRelay) remove(connection net.Conn) {
	relay.mu.Lock()
	delete(relay.active, connection)
	relay.mu.Unlock()
}

func (relay *localServerRelay) Close() {
	if relay == nil {
		return
	}
	relay.once.Do(func() {
		relay.mu.Lock()
		relay.closed = true
		relay.mu.Unlock()
		_ = relay.listener.Close()
		<-relay.done
		relay.mu.Lock()
		active := make([]net.Conn, 0, len(relay.active))
		for connection := range relay.active {
			active = append(active, connection)
		}
		relay.mu.Unlock()
		for _, connection := range active {
			_ = connection.Close()
		}
		relay.wg.Wait()
	})
}
