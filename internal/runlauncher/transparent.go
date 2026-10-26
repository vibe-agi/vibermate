package runlauncher

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/flowbroker"
	"github.com/vibe-agi/vibermate/internal/flowingress"
	"github.com/vibe-agi/vibermate/internal/runtimepath"
)

var (
	// ErrTransparentUnsupported reports a platform without transparent capture.
	ErrTransparentUnsupported = errors.New("transparent capture is available on macOS only")
	// ErrTransparentUnavailable reports that the local ViberMate app could not
	// accept the run's claim, so nothing would intercept the Agent.
	ErrTransparentUnavailable = errors.New("transparent capture needs the ViberMate app running")
	// ErrTransparentExtensionInactive reports that the network extension has
	// not announced itself, so the Agent's connections would leave uncaptured.
	ErrTransparentExtensionInactive = errors.New("the ViberMate network extension is not active")
	// ErrTransparentLost reports that capture could not be restored in time
	// after the app or extension went away during a run.
	ErrTransparentLost = errors.New("transparent capture was lost and could not be restored")
)

const (
	transparentRecoveryWindow = 30 * time.Second
	transparentRetryInterval  = time.Second
)

// proxyEnvironmentKeys are the variables ordinary launch injects to route the
// Agent. Transparent launch omits them; CA trust variables stay.
var proxyEnvironmentKeys = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy",
	"NO_PROXY", "no_proxy", "NODE_USE_ENV_PROXY",
}

func withoutProxyEnvironment(environment []string) []string {
	kept := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		drop := false
		for _, proxyKey := range proxyEnvironmentKeys {
			if key == proxyKey {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, entry)
		}
	}
	return kept
}

// transparentCapture is this launcher's claim on its descendants' flows. The
// broker identifies the launcher from socket credentials; each owned flow it
// passes back is forwarded through the run's own proxy frontier.
type transparentCapture struct {
	socket string
	run    string
	// token is set as flowbroker.TokenEnvironment in the Agent's environment
	// only; descendants inherit it, so the broker can attribute an orphaned
	// process and the launcher can pause every process of its run.
	token   string
	ingress *flowingress.Ingress
	ctx     context.Context
	cancel  context.CancelFunc
	// recoveryWindow bounds how long a paused Agent waits for capture to
	// return before the run fails.
	recoveryWindow time.Duration

	mu           sync.Mutex
	registration *flowbroker.Registration
}

func startTransparentCapture(socket, run string, proxy flowingress.Proxy) (*transparentCapture, error) {
	ingress, err := flowingress.New(func() (flowingress.Proxy, error) { return proxy, nil })
	if err != nil {
		return nil, err
	}
	entropy := make([]byte, 32)
	if _, err := rand.Read(entropy); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	capture := &transparentCapture{
		socket: socket, run: run, token: base64.RawURLEncoding.EncodeToString(entropy),
		ingress: ingress, ctx: ctx, cancel: cancel,
		recoveryWindow: transparentRecoveryWindow,
	}
	if err := capture.register(); err != nil {
		cancel()
		return nil, err
	}
	return capture, nil
}

func (capture *transparentCapture) register() error {
	registration, err := flowbroker.Register(capture.socket, capture.run, capture.token, capture.serve)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTransparentUnavailable, err)
	}
	if !registration.Extension() {
		registration.Close()
		return ErrTransparentExtensionInactive
	}
	capture.mu.Lock()
	capture.registration = registration
	capture.mu.Unlock()
	return nil
}

// marker is the environment entry that identifies this run's processes.
func (capture *transparentCapture) marker() string {
	return flowbroker.TokenEnvironment + "=" + capture.token
}

// agentEnvironment drops proxy variables and sets this run's token.
func (capture *transparentCapture) agentEnvironment(environment []string) []string {
	return append(withoutProxyEnvironment(environment), capture.marker())
}

func (capture *transparentCapture) serve(flow net.Conn) {
	_ = capture.ingress.Serve(capture.ctx, flow)
}

func (capture *transparentCapture) done() <-chan struct{} {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.registration.Done()
}

// supervise keeps capture in force for processGroup. When the claim drops
// (the app quit or restarted), the Agent is paused at once so no new
// connection can leave uncaptured; it resumes once the claim is restored with
// an active extension, and the run fails if that does not happen in time.
// Flows already handed to this launcher continue through its frontier.
func (capture *transparentCapture) supervise(ctx context.Context, processGroup int, notices io.Writer) <-chan error {
	failed := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-capture.done():
			}
			if ctx.Err() != nil {
				return
			}
			paused := pauseRun(processGroup, capture.marker())
			fmt.Fprintln(notices, "vibermate: transparent capture was interrupted; the Agent is paused until ViberMate is available again.")
			if err := capture.restore(ctx); err != nil {
				failed <- err
				return
			}
			paused.resume()
			fmt.Fprintln(notices, "vibermate: transparent capture restored; the Agent resumed.")
		}
	}()
	return failed
}

func (capture *transparentCapture) restore(ctx context.Context) error {
	deadline := time.NewTimer(capture.recoveryWindow)
	defer deadline.Stop()
	retry := time.NewTicker(transparentRetryInterval)
	defer retry.Stop()
	for {
		if err := capture.register(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return ErrTransparentLost
		case <-retry.C:
		}
	}
}

// Close ends the claim and the flows still in progress.
func (capture *transparentCapture) Close() {
	capture.mu.Lock()
	registration := capture.registration
	capture.mu.Unlock()
	if registration != nil {
		_ = registration.Close()
	}
	capture.cancel()
}

// startTransparent claims this launcher's descendants with the local app's
// broker before the Agent starts. Flows go through the same proxy the
// environment would have named, so the Runtime sees the same credential.
func (launcher *Launcher) startTransparent(grant, environmentGrant capturecontrol.LaunchGrant) (*transparentCapture, error) {
	if !transparentPlatformSupported() {
		return nil, ErrTransparentUnsupported
	}
	layout, err := runtimepath.Default()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransparentUnavailable, err)
	}
	return startTransparentCapture(layout.FlowRegistrationSocket, grant.Run.ID, flowingress.Proxy{
		Origin: environmentGrant.ProxyAddress,
		Token:  environmentGrant.ProxyToken,
	})
}
