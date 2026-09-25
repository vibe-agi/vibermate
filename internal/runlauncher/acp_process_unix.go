//go:build darwin || linux

package runlauncher

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/acpbridge"
	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"golang.org/x/sys/unix"
)

// ACPProcessConfig describes an already-resolved invocation. Arguments includes
// argv[0]; no shell, PATH lookup, environment overlay, login, or Runtime authority
// is inferred here. The caller exclusively lends the three stdio files for the
// duration of RunACPProcess; they remain open afterwards. Protocol stdin/stdout
// uses owned pollable handles. Interactive terminal input instead selects direct
// inherited stdio: authentication has a real TTY and is never observed.
type ACPProcessConfig struct {
	Executable      string
	Arguments       []string
	Directory       string
	Environment     []string
	Stdin           *os.File
	Stdout          *os.File
	Stderr          *os.File
	ShutdownTimeout time.Duration
	// OnStart attaches the PID before any protocol bytes are forwarded. It
	// must be bounded by its caller; failure kills and reaps the owned group.
	OnStart  func(context.Context, int) error
	Observer *acpobservation.Observer
}

// ACPProcessResult is a process/transport outcome, not a retained Capture or a
// statement that the child implements ACP or honors HTTP traffic policy.
type ACPProcessResult struct {
	ExitCode    int
	Signal      int
	Interactive bool
	acpbridge.Result
}

var ErrACPShutdownTimeout = errors.New("ACP agent did not finish within the shutdown budget")

type acpProcessError struct {
	operation string
	cause     error
}

func (err *acpProcessError) Error() string { return "ACP process " + err.operation + " failed" }
func (err *acpProcessError) Unwrap() error { return err.cause }

// RunACPProcess relays opaque duplex bytes and separate, unretained stderr to a
// real child. It does not interpret permissions, credentials, or session IDs.
func RunACPProcess(ctx context.Context, config ACPProcessConfig) (ACPProcessResult, error) {
	result := ACPProcessResult{ExitCode: 1}
	if ctx == nil || config.Executable == "" || len(config.Arguments) == 0 ||
		config.Stdin == nil || config.Stdout == nil || config.Stderr == nil || config.ShutdownTimeout < 0 {
		return result, errors.New("ACP process requires an invocation and three stdio files")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if config.ShutdownTimeout == 0 {
		config.ShutdownTimeout = defaultTerminationTimeout
	}
	if interactiveACPInput(config.Stdin) {
		return runACPInteractive(ctx, config)
	}
	streams, restore, err := borrowACPStreams(config.Stdin, config.Stdout, config.Stderr)
	if err != nil {
		return result, &acpProcessError{"stdio setup", err}
	}
	defer restore()
	var pipes []*os.File
	defer func() {
		for _, pipe := range pipes {
			_ = pipe.Close()
		}
	}()
	makePipe := func() (*os.File, *os.File, error) {
		read, write, err := os.Pipe()
		if err == nil {
			pipes = append(pipes, read, write)
		}
		return read, write, err
	}
	childInput, agentOutput, err := makePipe()
	if err != nil {
		return result, &acpProcessError{"pipe setup", err}
	}
	agentInput, childOutput, err := makePipe()
	if err != nil {
		return result, &acpProcessError{"pipe setup", err}
	}
	errorInput, childError, err := makePipe()
	if err != nil {
		return result, &acpProcessError{"pipe setup", err}
	}
	childContext, cancelChild := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelChild()
	relayContext, cancelRelay := context.WithCancel(ctx)
	defer cancelRelay()
	child := exec.CommandContext(childContext, config.Executable)
	child.Args = append([]string(nil), config.Arguments...)
	child.Dir = config.Directory
	child.Env = append([]string{}, config.Environment...)
	child.Stdin, child.Stdout, child.Stderr = childInput, childOutput, childError
	restoreTerminal := configureChild(child, config.ShutdownTimeout, childInput)
	defer restoreTerminal()
	// Own signals before there is a live child, including the bounded control
	// attachment round trip. Otherwise TERM during attachment can orphan it.
	signals, stopSignals := subscribeChildSignals()
	defer stopSignals()
	if err := child.Start(); err != nil {
		return result, &acpProcessError{"start", err}
	}
	_ = childInput.Close()
	_ = childOutput.Close()
	_ = childError.Close()
	if config.OnStart != nil {
		if err := config.OnStart(ctx, child.Process.Pid); err != nil {
			cancelChild()
			_ = waitChild(child, config.ShutdownTimeout)
			return result, &acpProcessError{"attachment", err}
		}
	}
	type relayed struct {
		result acpbridge.Result
		err    error
	}
	relayDone := make(chan relayed, 1)
	inputClosed := make(chan struct{})
	var clientInput io.ReadCloser = streams[0]
	var agentRead io.ReadCloser = agentInput
	if config.Observer != nil {
		clientInput = &acpObservedInput{ReadCloser: clientInput, observer: config.Observer, client: true}
		agentRead = &acpObservedInput{ReadCloser: agentRead, observer: config.Observer}
	}
	go func() {
		copied, err := acpbridge.Relay(relayContext,
			acpbridge.Endpoint{Input: clientInput, Output: streams[1]},
			acpbridge.Endpoint{Input: agentRead, Output: &acpInputClose{WriteCloser: agentOutput, done: inputClosed}})
		relayDone <- relayed{copied, err}
	}()
	errorDone := make(chan error, 1)
	go func() {
		_, err := io.CopyBuffer(struct{ io.Writer }{streams[2]}, struct{ io.Reader }{errorInput}, make([]byte, 32<<10))
		_ = errorInput.Close()
		_ = streams[2].Close()
		errorDone <- err
	}()
	closeIO := func() {
		cancelRelay()
		_ = errorInput.Close()
		_ = streams[2].Close()
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- child.Wait() }()
	timer := time.NewTimer(config.ShutdownTimeout)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	var expiry <-chan time.Time
	phase := 0 // running, final-output drain, TERM grace, KILL
	var terminal error
	beginDrain := func() {
		if phase == 0 {
			phase = 1
			timer.Reset(config.ShutdownTimeout)
			expiry = timer.C
		}
	}
	terminate := func(reason error) {
		if terminal == nil {
			terminal = reason
		}
		if phase < 2 {
			phase = 2
			cancelChild()
			timer.Reset(config.ShutdownTimeout)
			expiry = timer.C
		}
	}
	var waitErr, stderrErr error
	var forwarded relayed
	cancelled := ctx.Done()
	for waitDone != nil || relayDone != nil || errorDone != nil {
		select {
		case forwardedSignal := <-signals:
			if forwarded, ok := forwardedSignal.(unix.Signal); ok {
				if result.Signal == 0 {
					result.Signal = int(forwarded)
				}
				if err := unix.Kill(-child.Process.Pid, forwarded); err != nil && !errors.Is(err, unix.ESRCH) {
					terminate(&acpProcessError{"signal forwarding", err})
				}
				if phase < 2 {
					phase = 2
					timer.Reset(config.ShutdownTimeout)
					expiry = timer.C
				}
			}
		case <-cancelled:
			cancelled = nil
			terminate(ctx.Err())
			closeIO()
		case <-inputClosed:
			inputClosed = nil
			beginDrain()
		case waitErr = <-waitDone:
			waitDone = nil
			beginDrain()
		case forwarded = <-relayDone:
			relayDone = nil
			if forwarded.err != nil && phase < 2 {
				terminate(forwarded.err)
			}
		case stderrErr = <-errorDone:
			errorDone = nil
			if stderrErr != nil && phase < 2 {
				terminate(&acpProcessError{"stderr transfer", stderrErr})
			}
		case <-expiry:
			if phase == 1 {
				terminate(ErrACPShutdownTimeout)
			} else {
				_ = unix.Kill(-child.Process.Pid, unix.SIGKILL)
				phase = 3
				expiry = nil
				closeIO()
			}
		}
	}
	result.ExitCode, err = childExit(waitErr)
	result.Result = forwarded.result
	cleanupErr := finishChildGroup(child.Process, config.ShutdownTimeout)
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if terminal != nil {
		return result, terminal
	}
	if cleanupErr != nil {
		return result, &acpProcessError{"descendant cleanup", cleanupErr}
	}
	if err != nil {
		return result, &acpProcessError{"wait", err}
	}
	if forwarded.err != nil && result.Signal == 0 {
		return result, forwarded.err
	}
	if stderrErr != nil && result.Signal == 0 {
		return result, &acpProcessError{"stderr transfer", stderrErr}
	}
	return result, nil
}

type acpInputClose struct {
	io.WriteCloser
	done chan struct{}
	once sync.Once
	err  error
}

type acpObservedInput struct {
	io.ReadCloser
	observer *acpobservation.Observer
	client   bool
}

func (stream *acpObservedInput) Read(data []byte) (int, error) {
	n, err := stream.ReadCloser.Read(data)
	if n > 0 {
		stream.observer.Feed(stream.client, data[:n])
	}
	return n, err
}

func (stream *acpInputClose) Close() error {
	stream.once.Do(func() {
		stream.err = stream.WriteCloser.Close()
		close(stream.done)
	})
	return stream.err
}

// Duplicate each lent file into a cancellable Go-poller handle. dup shares file
// status flags, so restore O_NONBLOCK only after every owned handle/pump closes.
// This also handles stdout/stderr redirected to the same open-file description.
// No caller-owned descriptor is closed, and no goroutine reads borrowed stdin.
func borrowACPStreams(files ...*os.File) ([]*os.File, func(), error) {
	var owned []*os.File
	var changed []*os.File
	restore := func() {
		for _, file := range owned {
			_ = file.Close()
		}
		for _, file := range changed {
			connection, err := file.SyscallConn()
			if err == nil {
				_ = connection.Control(func(fd uintptr) { _ = unix.SetNonblock(int(fd), false) })
			}
		}
	}
	for _, file := range files {
		connection, err := file.SyscallConn()
		if err != nil {
			restore()
			return nil, nil, err
		}
		duplicate := -1
		var setupErr error
		controlErr := connection.Control(func(fd uintptr) {
			flags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
			if err != nil {
				setupErr = err
				return
			}
			duplicate, setupErr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0)
			if setupErr == nil && flags&unix.O_NONBLOCK == 0 {
				setupErr = unix.SetNonblock(duplicate, true)
				if setupErr == nil {
					changed = append(changed, file)
				}
			}
		})
		if err := errors.Join(controlErr, setupErr); err != nil {
			if duplicate >= 0 {
				_ = unix.Close(duplicate)
			}
			restore()
			return nil, nil, err
		}
		owned = append(owned, os.NewFile(uintptr(duplicate), "ACP owned stdio"))
	}
	return owned, restore, nil
}
