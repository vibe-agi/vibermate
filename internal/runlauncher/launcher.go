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
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/launcherdiscovery"
)

const (
	defaultHeartbeatInterval  = 30 * time.Second
	defaultControlTimeout     = 5 * time.Second
	defaultTerminationTimeout = 3 * time.Second
)

var ErrRuntimeUnavailable = errors.New("local VibeMate runtime is unavailable")

type Discovery interface {
	Load() (launcherdiscovery.Session, error)
}

type Config struct {
	Discovery          Discovery
	BaseEnvironment    []string
	Stdin              io.Reader
	Stdout             io.Writer
	Stderr             io.Writer
	HeartbeatInterval  time.Duration
	ControlTimeout     time.Duration
	TerminationTimeout time.Duration
	Getwd              func() (string, error)
	LookPath           func(string) (string, error)
}

type Launcher struct {
	config Config
}

func New(config Config) (*Launcher, error) {
	if config.Discovery == nil {
		return nil, errors.New("launcher discovery is required")
	}
	if config.HeartbeatInterval == 0 {
		config.HeartbeatInterval = defaultHeartbeatInterval
	}
	if config.ControlTimeout == 0 {
		config.ControlTimeout = defaultControlTimeout
	}
	if config.TerminationTimeout == 0 {
		config.TerminationTimeout = defaultTerminationTimeout
	}
	if config.HeartbeatInterval <= 0 ||
		config.ControlTimeout <= 0 ||
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
			errors.Is(err, launcherdiscovery.ErrExpired) {
			return 1, fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
		}
		return 1, fmt.Errorf("load launcher discovery: %w", err)
	}
	control, err := newControlClient(session)
	if err != nil {
		return 1, fmt.Errorf("construct launcher control client: %w", err)
	}
	defer control.close()
	var grant capturecontrol.LaunchGrant
	err = launcher.callWithTimeout(
		ctx,
		func(call context.Context) error {
			var createErr error
			grant, createErr = control.create(
				call,
				capturecontrol.CreateRequest{
					CWD:            cwd,
					Command:        append([]string(nil), command...),
					ExecutablePath: executable,
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
	configureChild(child, launcher.config.TerminationTimeout)
	if err := child.Start(); err != nil {
		launcher.finishBestEffort(control, grant)
		return 1, fmt.Errorf("start captured process: %w", err)
	}
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
		launcher.finishBestEffort(control, grant)
		return 1, fmt.Errorf("CaptureRun heartbeat failed: %w", heartbeatErr)
	case <-ctx.Done():
		stopHeartbeat(ctx.Err())
		cancelChild(ctx.Err())
		waitErr = <-waitResult
		launcher.finishBestEffort(control, grant)
		return 1, ctx.Err()
	}
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
	ctx, cancel := context.WithTimeout(parent, launcher.config.ControlTimeout)
	defer cancel()
	return call(ctx)
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

var _ Discovery = (*launcherdiscovery.File)(nil)
