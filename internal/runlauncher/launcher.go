// Package runlauncher owns the local CLI's one-child CaptureRun supervision
// lifecycle. It never accepts control or proxy origins from command arguments.
package runlauncher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
)

const (
	defaultHeartbeatInterval = 30 * time.Second
	defaultControlTimeout    = 5 * time.Second
	// defaultCreateTimeout is the one control call that can contain work
	// measured in people rather than in milliseconds.
	//
	// Creating a run makes the runtime verify the launched artifact's platform
	// signature (bounded by codesignature.VerifyDeadline, 30s — it hashes a
	// binary that can be hundreds of megabytes) and, for a client recognized by
	// its publisher rather than by a catalogued build, ask a person whether it
	// may carry the Root (bounded by toolapproval.DefaultClientRootGrace, 30s).
	//
	// The short budget was applied to it too, so the sum of the inner bounds
	// was twelve times the outer one and the recognized tier could not
	// complete: the launcher abandoned the request, the runtime went on to
	// create a run nobody would collect, and the client did not start at all —
	// where the design says a launch that cannot reach a person starts without
	// a Root instead. This is a ceiling and not a delay: a generic client never
	// reaches the ask and answers as fast as it always did.
	//
	// runlauncher_budget_test.go holds this to the sum of what it must cover,
	// so the three numbers cannot drift apart again in silence.
	defaultCreateTimeout      = 90 * time.Second
	defaultTerminationTimeout = 3 * time.Second
)

var ErrRuntimeUnavailable = errors.New("local ViberMate runtime is unavailable")

type Discovery interface {
	Load() (localdiscovery.Session, error)
}

type Config struct {
	Discovery         Discovery
	BaseEnvironment   []string
	Stdin             io.Reader
	Stdout            io.Writer
	Stderr            io.Writer
	HeartbeatInterval time.Duration
	// ControlTimeout bounds every control call except create. None of them can
	// contain a person, so none of them may be given a budget that would let a
	// launch hang on one.
	ControlTimeout     time.Duration
	CreateTimeout      time.Duration
	TerminationTimeout time.Duration
	Getwd              func() (string, error)
	LookPath           func(string) (string, error)
}

type Launcher struct {
	config Config
}

func New(config Config) (*Launcher, error) {
	if config.Discovery == nil {
		return nil, errors.New("local control discovery is required")
	}
	if config.HeartbeatInterval == 0 {
		config.HeartbeatInterval = defaultHeartbeatInterval
	}
	if config.ControlTimeout == 0 {
		config.ControlTimeout = defaultControlTimeout
	}
	if config.CreateTimeout == 0 {
		config.CreateTimeout = defaultCreateTimeout
	}
	if config.TerminationTimeout == 0 {
		config.TerminationTimeout = defaultTerminationTimeout
	}
	if config.HeartbeatInterval <= 0 ||
		config.ControlTimeout <= 0 ||
		config.CreateTimeout <= 0 ||
		config.TerminationTimeout <= 0 {
		return nil, errors.New("launcher lifecycle timeouts must be positive")
	}
	if config.Getwd == nil {
		config.Getwd = os.Getwd
	}
	if config.LookPath == nil {
		config.LookPath = exec.LookPath
	}
	if config.BaseEnvironment == nil {
		config.BaseEnvironment = os.Environ()
	} else {
		config.BaseEnvironment = append([]string(nil), config.BaseEnvironment...)
	}
	if config.Stdin == nil {
		config.Stdin = os.Stdin
	}
	if config.Stdout == nil {
		config.Stdout = os.Stdout
	}
	if config.Stderr == nil {
		config.Stderr = os.Stderr
	}
	return &Launcher{config: config}, nil
}

// Run starts exactly one child, remains its supervisor, and returns its exit
// status. A child non-zero exit is an outcome, not a launcher error.
func (launcher *Launcher) Run(
	ctx context.Context,
	command []string,
) (int, error) {
	if launcher == nil || ctx == nil || len(command) == 0 || command[0] == "" {
		return 1, errors.New("launcher requires a context and command")
	}
	cwd, executable, err := launcher.resolveCommand(command)
	if err != nil {
		return 1, err
	}
	session, err := launcher.config.Discovery.Load()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) ||
			errors.Is(err, localdiscovery.ErrExpired) {
			return 1, fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
		}
		return 1, fmt.Errorf("load local control discovery: %w", err)
	}
	control, err := newControlClient(
		session,
		controlTransportTimeout(launcher.config),
	)
	if err != nil {
		return 1, fmt.Errorf("construct local control client: %w", err)
	}
	defer control.close()
	var grant capturecontrol.LaunchGrant
	err = launcher.callWithin(
		ctx,
		launcher.config.CreateTimeout,
		func(call context.Context) error {
			var createErr error
			grant, createErr = control.create(
				call,
				capturecontrol.CreateRequest{
					CWD:            cwd,
					Command:        append([]string(nil), command...),
					ExecutablePath: executable,
					LocalUserLabel: localUserLabel(launcher.config.BaseEnvironment),
				},
			)
			return createErr
		},
	)
	if err != nil {
		return 1, err
	}
	if err := validateGrant(grant); err != nil {
		launcher.finishBestEffort(control, grant)
		return 1, err
	}
	// A catalogued client at a version this build has no evidence for is
	// launched without a trust root, on purpose: the catalog is versioned
	// evidence and an update must not silently widen what may be decrypted.
	// It will fail its handshake, so it is told why here rather than being
	// left with a transport error nobody can explain.
	launcher.warnUnverified(grant)
	environment, err := buildEnvironment(
		launcher.config.BaseEnvironment,
		grant,
	)
	if err != nil {
		launcher.finishBestEffort(control, grant)
		return 1, err
	}

	childContext, cancelChild := context.WithCancelCause(context.WithoutCancel(ctx))
	defer cancelChild(errors.New("launcher child supervision ended"))
	child := exec.CommandContext(
		childContext,
		grant.ExecutablePath,
		append([]string(nil), command[1:]...)...,
	)
	child.Dir = cwd
	child.Env = environment
	child.Stdin = launcher.config.Stdin
	child.Stdout = launcher.config.Stdout
	child.Stderr = launcher.config.Stderr
	restoreTerminal := configureChild(
		child,
		launcher.config.TerminationTimeout,
		launcher.config.Stdin,
	)
	if err := child.Start(); err != nil {
		restoreTerminal()
		launcher.finishBestEffort(control, grant)
		return 1, fmt.Errorf("start captured process: %w", err)
	}
	defer restoreTerminal()
	stopSignals := relaySignals(child.Process)
	defer stopSignals()
	if err := launcher.callWithTimeout(
		ctx,
		func(call context.Context) error {
			return control.attach(call, grant, child.Process.Pid)
		},
	); err != nil {
		cancelChild(errors.New("CaptureRun attachment failed"))
		_ = child.Wait()
		restoreTerminal()
		launcher.finishBestEffort(control, grant)
		return 1, fmt.Errorf("attach captured process: %w", err)
	}

	waitResult := make(chan error, 1)
	go func() {
		waitResult <- child.Wait()
	}()
	heartbeatContext, stopHeartbeat := context.WithCancelCause(
		context.Background(),
	)
	heartbeatFailure := launcher.heartbeat(
		heartbeatContext,
		control,
		grant,
	)
	defer stopHeartbeat(errors.New("captured process ended"))

	var waitErr error
	select {
	case waitErr = <-waitResult:
	case heartbeatErr := <-heartbeatFailure:
		stopHeartbeat(heartbeatErr)
		cancelChild(heartbeatErr)
		waitErr = <-waitResult
		restoreTerminal()
		launcher.finishBestEffort(control, grant)
		return 1, fmt.Errorf("CaptureRun heartbeat failed: %w", heartbeatErr)
	case <-ctx.Done():
		stopHeartbeat(ctx.Err())
		cancelChild(ctx.Err())
		waitErr = <-waitResult
		restoreTerminal()
		launcher.finishBestEffort(control, grant)
		return 1, ctx.Err()
	}
	restoreTerminal()
	stopHeartbeat(errors.New("captured process exited"))
	finishErr := launcher.callWithTimeout(
		context.Background(),
		func(call context.Context) error {
			return control.finish(call, grant)
		},
	)
	exitCode, childErr := childExit(waitErr)
	if finishErr != nil {
		return exitCode, fmt.Errorf("finish CaptureRun: %w", finishErr)
	}
	return exitCode, childErr
}

// controlTransportTimeout is only a backstop around the pinned loopback HTTP
// transport. Each operation still owns its narrower context deadline. The
// transport must cover the widest operation, however, or http.Client can
// cancel a valid create while code-signature verification or a Root decision
// is still inside its own bound.
func controlTransportTimeout(config Config) time.Duration {
	return max(config.ControlTimeout, config.CreateTimeout)
}

func localUserLabel(environment []string) string {
	keys := []string{"USER", "USERNAME"}
	if runtime.GOOS == "windows" {
		keys = []string{"USERNAME", "USER"}
	}
	for _, key := range keys {
		prefix := key + "="
		for index := len(environment) - 1; index >= 0; index-- {
			if !strings.HasPrefix(environment[index], prefix) {
				continue
			}
			value := strings.TrimSpace(
				strings.TrimPrefix(environment[index], prefix),
			)
			if capturerun.ValidLocalUserLabel(value) {
				return value
			}
			return ""
		}
	}
	return ""
}

func (launcher *Launcher) resolveCommand(
	command []string,
) (string, string, error) {
	cwd, err := launcher.config.Getwd()
	if err != nil {
		return "", "", fmt.Errorf("resolve current working directory: %w", err)
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return "", "", err
	}
	cwd = filepath.Clean(cwd)
	executable, err := launcher.config.LookPath(command[0])
	if err != nil {
		return "", "", fmt.Errorf("resolve captured executable: %w", err)
	}
	if !filepath.IsAbs(executable) {
		executable, err = filepath.Abs(executable)
		if err != nil {
			return "", "", err
		}
	}
	executable = filepath.Clean(executable)
	return cwd, executable, nil
}

func (launcher *Launcher) heartbeat(
	ctx context.Context,
	control *controlClient,
	grant capturecontrol.LaunchGrant,
) <-chan error {
	failure := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(launcher.config.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				err := launcher.callWithTimeout(
					ctx,
					func(call context.Context) error {
						return control.heartbeat(call, grant)
					},
				)
				if err != nil {
					select {
					case failure <- err:
					case <-ctx.Done():
					}
					return
				}
			}
		}
	}()
	return failure
}

func (launcher *Launcher) callWithTimeout(
	parent context.Context,
	call func(context.Context) error,
) error {
	return launcher.callWithin(parent, launcher.config.ControlTimeout, call)
}

func (launcher *Launcher) callWithin(
	parent context.Context,
	budget time.Duration,
	call func(context.Context) error,
) error {
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	return call(ctx)
}

// warnUnverified writes one line, to the stream the person is already
// watching. It names the program and nothing else: what it does not say is
// what the client sent, where it connected, or any credential.
func (launcher *Launcher) warnUnverified(grant capturecontrol.LaunchGrant) {
	if grant.Recognition != clientadapter.RecognitionUnverified ||
		launcher.config.Stderr == nil {
		return
	}
	_, _ = fmt.Fprintf(
		launcher.config.Stderr,
		"vibermate: %s is a client this build knows, at a version it has no "+
			"release evidence for. It was started without a trust root, so "+
			"requests it makes through vibermate will fail to connect.\n",
		grant.Run.ExecutableLabel,
	)
}

func (launcher *Launcher) finishBestEffort(
	control *controlClient,
	grant capturecontrol.LaunchGrant,
) {
	if grant.Run.ID == "" || grant.RunCapability == "" {
		return
	}
	_ = launcher.callWithTimeout(
		context.Background(),
		func(ctx context.Context) error {
			return control.finish(ctx, grant)
		},
	)
}

func childExit(waitErr error) (int, error) {
	if waitErr == nil {
		return 0, nil
	}
	var exitError *exec.ExitError
	if errors.As(waitErr, &exitError) {
		if code := exitError.ExitCode(); code >= 0 {
			return code, nil
		}
		return signaledExitCode(exitError), nil
	}
	return 1, fmt.Errorf("wait for captured process: %w", waitErr)
}

var _ Discovery = (*localdiscovery.File)(nil)
