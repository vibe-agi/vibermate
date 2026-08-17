package desktophost_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopbootstrap"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/desktophost"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func TestHostPublishesReadyGenerationAndRunsCapturedChildOverRealSockets(
	t *testing.T,
) {
	t.Parallel()

	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	host := startHost(t, hostOptions(t, paths, filepath.Join(root, "data")))
	sessionFile, err := localdiscovery.NewFile(
		paths.DiscoveryPath(),
		productruntime.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessionFile.Load()
	if err != nil {
		t.Fatal(err)
	}
	status := host.Status()
	app := host.AppSession()
	bootstrap := host.Bootstrap()
	if status.State != desktophost.StateReady ||
		!status.Ready ||
		bootstrap.Schema != desktopbootstrap.DescriptorSchema ||
		bootstrap.InstanceID != status.InstanceID ||
		bootstrap.BaseURL != app.BaseURL ||
		len(bootstrap.APIVersions) != 1 ||
		len(bootstrap.EventVersions) != 0 ||
		session.InstanceID != status.InstanceID ||
		session.BaseURL != app.BaseURL ||
		session.ControlCredential == app.ReadToken ||
		session.ControlCredential == app.WriteToken ||
		app.ReadToken == app.WriteToken {
		t.Fatalf(
			"Host publication status=%+v session=%+v app=%+v",
			status,
			session,
			app,
		)
	}
	exchanged := exchangeBootstrap(t, bootstrap)
	if exchanged != app {
		t.Fatalf("bootstrap App session = %+v, want %+v", exchanged, app)
	}
	replay := bootstrapRequest(t, bootstrap)
	defer replay.Body.Close()
	if replay.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bootstrap replay status = %d", replay.StatusCode)
	}

	response := controlRequest(
		t,
		app.BaseURL,
		http.MethodGet,
		"/api/v1/status",
		app.ReadToken,
		"vibermate://desktop",
	)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("control status = %d", response.StatusCode)
	}
	var controlStatus desktopcontrol.StatusResponse
	if err := json.NewDecoder(response.Body).Decode(&controlStatus); err != nil {
		t.Fatal(err)
	}
	if !controlStatus.Ready ||
		controlStatus.Generation != status.InstanceID ||
		controlStatus.Runtime.State != productruntime.RuntimeStateInitialized {
		t.Fatalf("control status = %+v", controlStatus)
	}

	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery:          sessionFile,
		BaseEnvironment:    []string{"PATH=/usr/bin:/bin"},
		Stdin:              strings.NewReader(""),
		Stdout:             io.Discard,
		Stderr:             io.Discard,
		HeartbeatInterval:  10 * time.Millisecond,
		ControlTimeout:     time.Second,
		TerminationTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	runContext, cancelRun := context.WithTimeout(context.Background(), 10*time.Second)
	exitCode, err := launcher.Run(
		runContext,
		transparentLaunch("/bin/sh", "-c", "exit 0"),
	)
	cancelRun()
	if err != nil || exitCode != 0 {
		t.Fatalf("captured child exitCode=%d error=%v", exitCode, err)
	}

	shutdownHost(t, host)
	if _, err := sessionFile.Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discovery after shutdown error = %v", err)
	}
	if final := host.Status(); final.State != desktophost.StateStopped ||
		final.Ready ||
		final.StoppedAt == nil {
		t.Fatalf("final Host status = %+v", final)
	}
	if runtimeStatus := host.Runtime().Status(); runtimeStatus.State != productruntime.RuntimeStateStopped {
		t.Fatalf("final ProductRuntime status = %+v", runtimeStatus)
	}
	if err := host.Shutdown(context.Background()); err != nil {
		t.Fatalf("idempotent Host shutdown: %v", err)
	}
	guard, err := instanceguard.Acquire(paths.LockPath())
	if err != nil {
		t.Fatalf("generation lock was not released: %v", err)
	}
	if err := guard.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestHostUsesOnlyTheExplicitWebviewOrigin(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	options := hostOptions(t, paths, filepath.Join(root, "data"))
	options.AllowedOrigins = []string{"https://desktop.test"}
	host := startHost(t, options)
	defer shutdownHost(t, host)

	accepted := controlRequest(
		t,
		host.AppSession().BaseURL,
		http.MethodGet,
		"/api/v1/status",
		host.AppSession().ReadToken,
		"https://desktop.test",
	)
	_ = accepted.Body.Close()
	if accepted.StatusCode != http.StatusOK {
		t.Fatalf("development Webview status = %d", accepted.StatusCode)
	}
	rejected := controlRequest(
		t,
		host.AppSession().BaseURL,
		http.MethodGet,
		"/api/v1/status",
		host.AppSession().ReadToken,
		"vibermate://desktop",
	)
	_ = rejected.Body.Close()
	if rejected.StatusCode != http.StatusForbidden {
		t.Fatalf("unlisted Webview status = %d", rejected.StatusCode)
	}
}

func exchangeBootstrap(
	t *testing.T,
	bootstrap desktopbootstrap.Descriptor,
) desktopbootstrap.Session {
	t.Helper()
	response := bootstrapRequest(t, bootstrap)
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap status = %d", response.StatusCode)
	}
	var session desktopbootstrap.Session
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&session); err != nil {
		t.Fatal(err)
	}
	return session
}

func bootstrapRequest(
	t *testing.T,
	bootstrap desktopbootstrap.Descriptor,
) *http.Response {
	t.Helper()
	request, err := http.NewRequest(
		http.MethodPost,
		bootstrap.BaseURL+"/api/v1/auth/sessions",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(
		"Authorization",
		"Bootstrap "+bootstrap.BootstrapNonce,
	)
	transport := &http.Transport{Proxy: nil, DisableCompression: true}
	t.Cleanup(transport.CloseIdleConnections)
	response, err := (&http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
	}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestHostRejectsSecondGenerationWithoutChangingFirstDiscovery(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	first := startHost(t, hostOptions(t, paths, filepath.Join(root, "data")))
	defer shutdownHost(t, first)
	before, err := os.ReadFile(paths.DiscoveryPath())
	if err != nil {
		t.Fatal(err)
	}
	_, err = desktophost.Start(
		context.Background(),
		hostOptions(t, paths, filepath.Join(root, "other-data")),
	)
	if !errors.Is(err, instanceguard.ErrAlreadyOwned) {
		t.Fatalf("second Host Start() error = %v", err)
	}
	after, err := os.ReadFile(paths.DiscoveryPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) || !first.Status().Ready {
		t.Fatal("second generation changed the active Host publication")
	}
}

func TestHostRefreshesDiscoveryWithoutRotatingControlCredential(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	options := hostOptions(t, paths, filepath.Join(root, "data"))
	options.CLIControlDiscoveryTTL = 300 * time.Millisecond
	host := startHost(t, options)
	defer shutdownHost(t, host)
	sessionFile, err := localdiscovery.NewFile(
		paths.DiscoveryPath(),
		productruntime.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := sessionFile.Load()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var refreshed localdiscovery.Session
	for time.Now().Before(deadline) {
		current, loadErr := sessionFile.Load()
		if loadErr == nil &&
			current.ControlCredential == first.ControlCredential &&
			current.ExpiresAt.After(first.ExpiresAt) {
			refreshed = current
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if refreshed.ControlCredential == "" ||
		!host.Status().Ready {
		t.Fatalf(
			"discovery refresh first=%+v refreshed=%+v status=%+v",
			first,
			refreshed,
			host.Status(),
		)
	}
	response := controlCreateProbe(
		t,
		refreshed.BaseURL,
		refreshed.ControlCredential,
	)
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf(
			"refreshed control status=%d, want authorized request validation",
			response.StatusCode,
		)
	}
}

func TestHostDiscoveryPublicationFailureRollsBackBothListenersAndRuntime(
	t *testing.T,
) {
	t.Parallel()

	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	if err := os.MkdirAll(paths.RuntimeDirectory(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(paths.DiscoveryPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	proxyAddress := reserveAddress(t)
	controlAddress := reserveAddress(t)
	options := hostOptions(t, paths, filepath.Join(root, "data"))
	options.ProxyListenAddress = proxyAddress
	options.ControlListenAddress = controlAddress
	_, err := desktophost.Start(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "local control discovery publication") {
		t.Fatalf("Host Start() error = %v", err)
	}
	for _, address := range []string{proxyAddress, controlAddress} {
		listener, err := net.Listen("tcp4", address)
		if err != nil {
			t.Fatalf("listener %s leaked after rollback: %v", address, err)
		}
		_ = listener.Close()
	}
	guard, err := instanceguard.Acquire(paths.LockPath())
	if err != nil {
		t.Fatalf("generation lock leaked after rollback: %v", err)
	}
	if err := guard.Release(); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(paths.DiscoveryPath()); err != nil {
		t.Fatal(err)
	}
	reopened := startHost(t, hostOptions(t, paths, filepath.Join(root, "data")))
	shutdownHost(t, reopened)
}

func controlCreateProbe(
	t *testing.T,
	baseURL string,
	token string,
) *http.Response {
	t.Helper()
	request, err := http.NewRequest(
		http.MethodPost,
		baseURL+"/api/v1/capture-runs",
		strings.NewReader("{}"),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil, DisableCompression: true}
	t.Cleanup(transport.CloseIdleConnections)
	response, err := (&http.Client{
		Transport: transport,
		Timeout:   time.Second,
	}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func newHostPaths(t *testing.T, cache string) desktophost.Paths {
	t.Helper()
	paths, err := desktophost.NewPaths(cache)
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func hostOptions(
	t *testing.T,
	paths desktophost.Paths,
	dataDirectory string,
) desktophost.Options {
	t.Helper()
	runtimePaths, err := productruntime.NewRuntimePaths(dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	options := desktophost.DefaultOptions(paths, productruntime.Options{
		Paths:          runtimePaths,
		Host:           hostcontract.Desktop(),
		OfflineHold:    gate,
		Secrets:        unavailableSecrets{},
		Approvals:      toolapproval.DefaultConfig(),
		ExchangeHold:   exchange.DefaultHoldPolicy(),
		Clock:          productruntime.SystemClock{},
		InstanceIDs:    productruntime.NewCryptographicInstanceIDSource(),
		SecurityRandom: rand.Reader,
		Lifecycle:      productruntime.DefaultLifecycleOptions(),
	})
	options.CLIControlDiscoveryTTL = time.Minute
	options.AppSessionTTL = time.Hour
	options.CaptureRunLifetime = 3 * time.Second
	options.ShutdownTimeout = 15 * time.Second
	return options
}

func startHost(t *testing.T, options desktophost.Options) *desktophost.Host {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	host, err := desktophost.Start(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func shutdownHost(t *testing.T, host *desktophost.Host) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := host.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func controlRequest(
	t *testing.T,
	baseURL string,
	method string,
	path string,
	token string,
	origin string,
) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, baseURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", origin)
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	request.Header.Set("Sec-Fetch-Mode", "cors")
	request.Header.Set("Sec-Fetch-Dest", "empty")
	request.Header.Set("Authorization", "Bearer "+token)
	transport := &http.Transport{Proxy: nil, DisableCompression: true}
	t.Cleanup(transport.CloseIdleConnections)
	response, err := (&http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
	}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func reserveAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

type unavailableSecrets struct{}

func (unavailableSecrets) Read(
	context.Context,
	secretstore.Reference,
) (*secretstore.Value, error) {
	return nil, secretstore.ErrNotFound
}

func (unavailableSecrets) ReadAtRevision(
	context.Context,
	secretstore.Reference,
	secretstore.Revision,
) (*secretstore.Value, error) {
	return nil, secretstore.ErrNotFound
}

func (unavailableSecrets) Inspect(
	context.Context,
	secretstore.Reference,
) (secretstore.Metadata, error) {
	return secretstore.Metadata{State: secretstore.StateMissing}, nil
}

func (unavailableSecrets) Replace(
	context.Context,
	secretstore.ReplaceCommand,
) (secretstore.Metadata, error) {
	return secretstore.Metadata{}, secretstore.ErrReadOnly
}

func (unavailableSecrets) Delete(
	context.Context,
	secretstore.Reference,
) error {
	return secretstore.ErrNotFound
}
