//go:build darwin || linux

package runlauncher_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"golang.org/x/sys/unix"
)

type acpProcessOutcome struct {
	result runlauncher.ACPProcessResult
	err    error
}

type acpProcessFixtureResult struct {
	Arguments []string
	Directory string
	Setting   string
	Input     []byte
}

func TestACPProcessPreservesInvocationAndDrainsFinalOutput(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	input, writeInput := acpProcessPipe(t)
	readOutput, output := acpProcessPipe(t)
	readError, errorOutput := acpProcessPipe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	arguments := []string{
		"editor-agent-entry", "-test.run=^TestACPProcessFixture$", "--",
		"--cli", "literal space", "$(not-a-shell)", "--env=child-value",
	}
	finished := make(chan acpProcessOutcome, 1)
	go func() {
		result, runErr := runlauncher.RunACPProcess(ctx, runlauncher.ACPProcessConfig{
			Executable: executable, Arguments: arguments, Directory: directory,
			Environment: []string{"VIBERMATE_TEST_ACP_PROCESS=echo", "ACP_FIXTURE_SETTING=editor value", "GORACE=atexit_sleep_ms=0"},
			Stdin:       input, Stdout: output, Stderr: errorOutput,
			ShutdownTimeout: time.Second,
		})
		_ = output.Close()
		_ = errorOutput.Close()
		finished <- acpProcessOutcome{result, runErr}
	}()
	payload := []byte("{\"jsonrpc\":\"2.0\",\"method\":\"_future/request\"}\r\n")
	if _, err := writeInput.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = writeInput.Close()
	var got acpProcessFixtureResult
	if err := json.NewDecoder(readOutput).Decode(&got); err != nil {
		t.Fatalf("read final child output: %v", err)
	}
	if !reflect.DeepEqual(got.Arguments, arguments) || got.Directory != directory ||
		got.Setting != "editor value" || !bytes.Equal(got.Input, payload) {
		t.Fatalf("child invocation or bytes changed: %+v", got)
	}
	diagnostic, err := io.ReadAll(readError)
	if err != nil || string(diagnostic) != "child diagnostic only\n" {
		t.Fatalf("separate stderr = %q, %v", diagnostic, err)
	}
	select {
	case got := <-finished:
		if got.err != nil || got.result.ExitCode != 0 || got.result.ClientToAgentBytes != int64(len(payload)) {
			t.Fatalf("process outcome = %+v, %v", got.result, got.err)
		}
	case <-ctx.Done():
		t.Fatal("ACP process did not finish after input EOF")
	}
}

func acpProcessPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	return reader, writer
}

func TestACPProcessBoundsShutdownWhenAgentIgnoresClientEOF(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, writeInput := acpProcessPipe(t)
	readOutput, output := acpProcessPipe(t)
	readError, errorOutput := acpProcessPipe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_ = readOutput.SetReadDeadline(deadline)
	finished := make(chan acpProcessOutcome, 1)
	go func() {
		result, runErr := runlauncher.RunACPProcess(ctx, runlauncher.ACPProcessConfig{
			Executable: executable, Arguments: []string{executable, "-test.run=^TestACPProcessFixture$"},
			Environment: []string{"VIBERMATE_TEST_ACP_PROCESS=ignore-input", "GORACE=atexit_sleep_ms=0"},
			Stdin:       input, Stdout: output, Stderr: errorOutput,
			ShutdownTimeout: 100 * time.Millisecond,
		})
		_ = output.Close()
		_ = errorOutput.Close()
		finished <- acpProcessOutcome{result, runErr}
	}()
	defer readError.Close()
	ready := make([]byte, 6)
	if _, err := io.ReadFull(readOutput, ready); err != nil || string(ready) != "ready\n" {
		cancel()
		<-finished
		t.Fatalf("child readiness = %q, %v", ready, err)
	}
	_ = writeInput.Close()
	select {
	case got := <-finished:
		if got.err == nil || got.result.ExitCode != 137 || ctx.Err() != nil {
			t.Fatalf("uncooperative EOF outcome = %+v, %v; outer context = %v", got.result, got.err, ctx.Err())
		}
	case <-time.After(time.Second):
		cancel()
		<-finished
		t.Fatal("client EOF left the uncooperative agent running past the shutdown budget")
	}
}

func TestACPProcessCleansDescendantsAfterWrapperExits(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, writeInput := acpProcessPipe(t)
	readOutput, output := acpProcessPipe(t)
	_, errorOutput := acpProcessPipe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_ = readOutput.SetReadDeadline(deadline)
	finished := make(chan acpProcessOutcome, 1)
	go func() {
		result, runErr := runlauncher.RunACPProcess(ctx, runlauncher.ACPProcessConfig{
			Executable: executable, Arguments: []string{executable, "-test.run=^TestACPProcessFixture$"},
			Environment: []string{"VIBERMATE_TEST_ACP_PROCESS=wrapper", "GORACE=atexit_sleep_ms=0"},
			Stdin:       input, Stdout: output, Stderr: errorOutput,
			ShutdownTimeout: 100 * time.Millisecond,
		})
		_ = output.Close()
		_ = errorOutput.Close()
		finished <- acpProcessOutcome{result, runErr}
	}()
	var descendant int
	if _, err := fmt.Fscanf(readOutput, "%d\n", &descendant); err != nil || descendant <= 0 {
		cancel()
		<-finished
		t.Fatalf("read fixture descendant: %v", err)
	}
	defer func() {
		if descendant > 0 {
			_ = syscall.Kill(descendant, syscall.SIGKILL)
		}
	}()
	_ = writeInput.Close()
	got := <-finished
	if got.err != nil || got.result.ExitCode != 0 {
		t.Fatalf("wrapper outcome = %+v, %v", got.result, got.err)
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(descendant, 0); err == syscall.ESRCH {
			descendant = 0
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("successful wrapper exit left its descendant running")
}

func TestLauncherCleansDescendantsAfterAgentWrapperExits(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	command := []string{executable, "-test.run=^TestACPProcessFixture$"}
	control := &controlFixture{
		t: t, executable: executable, workspace: directory,
		credential: capability(0x51), proxy: capability(0x52), run: capability(0x53),
		expectedCommand: command, recipe: clientadapter.LaunchGeneric,
		recognition: clientadapter.RecognitionUnknown,
	}
	server := httptest.NewServer(control)
	defer server.Close()
	var output bytes.Buffer
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery: fixedDiscovery{session: localdiscovery.Session{
			Schema: localdiscovery.Schema, ProcessID: os.Getpid(), BaseURL: server.URL,
			InstanceID:        base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x54}, 20)),
			ControlCredential: control.credential, ExpiresAt: time.Now().UTC().Add(time.Minute),
		}},
		BaseEnvironment: []string{"VIBERMATE_TEST_ACP_PROCESS=wrapper", "GORACE=atexit_sleep_ms=0"},
		Stdin:           bytes.NewReader(nil), Stdout: &output, Stderr: io.Discard,
		TerminationTimeout: 100 * time.Millisecond,
		Getwd:              func() (string, error) { return directory, nil },
		LookPath:           func(string) (string, error) { return executable, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, runErr := launcher.Run(ctx, runlauncher.LaunchRequest{EnvironmentID: environment.SystemTransparentID, Command: command})
	var descendant int
	if _, err := fmt.Fscanf(&output, "%d\n", &descendant); err != nil || descendant <= 0 {
		t.Fatalf("managed wrapper output: %v; launch error: %v", err, runErr)
	}
	defer func() {
		if descendant > 0 {
			_ = syscall.Kill(descendant, syscall.SIGKILL)
		}
	}()
	if runErr != nil || code != 0 {
		t.Fatalf("managed wrapper outcome = %d, %v", code, runErr)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(descendant, 0); err == syscall.ESRCH {
			descendant = 0
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("managed Capture finished while its wrapper descendant was still running")
}

func TestACPProcessReleasesBlockingEditorInputAndPreservesDelayedExit(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, writeInput := acpProcessPipe(t)
	readOutput, output := acpProcessPipe(t)
	readError, errorOutput := acpProcessPipe(t)
	// Match exec-inherited editor stdio, not os.Pipe's default pollable handles.
	for _, file := range []*os.File{input, output, errorOutput} {
		if err := unix.SetNonblock(int(file.Fd()), false); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_ = readOutput.SetReadDeadline(deadline)
	finished := make(chan acpProcessOutcome, 1)
	go func() {
		result, runErr := runlauncher.RunACPProcess(ctx, runlauncher.ACPProcessConfig{
			Executable: executable, Arguments: []string{executable, "-test.run=^TestACPProcessFixture$"},
			Environment: []string{"VIBERMATE_TEST_ACP_PROCESS=delayed-exit", "GORACE=atexit_sleep_ms=0"},
			Stdin:       input, Stdout: output, Stderr: errorOutput,
			ShutdownTimeout: time.Second,
		})
		finished <- acpProcessOutcome{result, runErr}
	}()
	errorBytes := make(chan int64, 1)
	go func() { n, _ := io.Copy(io.Discard, readError); errorBytes <- n }()
	ready := make([]byte, 6)
	if _, err := io.ReadFull(readOutput, ready); err != nil || string(ready) != "ready\n" {
		t.Fatalf("child readiness = %q, %v", ready, err)
	}
	got := <-finished
	if got.err != nil || got.result.ExitCode != 27 || ctx.Err() != nil {
		t.Fatalf("delayed exit with editor input still open = %+v, %v", got.result, got.err)
	}
	for _, file := range []*os.File{input, output, errorOutput} {
		connection, err := file.SyscallConn()
		if err != nil {
			t.Fatalf("caller-owned stdio was closed: %v", err)
		}
		var flags int
		var flagErr error
		if err := connection.Control(func(fd uintptr) { flags, flagErr = unix.FcntlInt(fd, unix.F_GETFL, 0) }); err != nil || flagErr != nil || flags&unix.O_NONBLOCK != 0 {
			t.Fatalf("caller stdio flags were not restored: %x, %v, %v", flags, err, flagErr)
		}
	}
	_ = errorOutput.Close()
	if got := <-errorBytes; got != 2<<20 {
		t.Fatalf("unterminated stderr was not fully forwarded: %d bytes", got)
	}
	if _, err := writeInput.Write([]byte("still open")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(input, make([]byte, 10)); err != nil {
		t.Fatalf("original editor input is unusable after process exit: %v", err)
	}
}

func TestACPProcessForwardsSignalsAndBoundsEscalation(t *testing.T) {
	for _, mode := range []string{"signal-supervisor", "signal-attach-supervisor"} {
		t.Run(mode, func(t *testing.T) { testACPProcessSignal(t, mode) })
	}
}

func testACPProcessSignal(t *testing.T, mode string) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, _ := acpProcessPipe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, executable, "-test.run=^TestACPProcessFixture$")
	child.Env = []string{"VIBERMATE_TEST_ACP_PROCESS=" + mode, "GORACE=atexit_sleep_ms=0"}
	child.Stdin = input
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	child.Stderr = &stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	var agentPID int
	if _, err := fmt.Fscanf(stdout, "%d\n", &agentPID); err != nil || agentPID <= 0 {
		cancel()
		_ = child.Wait()
		t.Fatalf("signal fixture did not start: %v", err)
	}
	defer func() {
		if agentPID > 0 {
			_ = syscall.Kill(agentPID, syscall.SIGKILL)
		}
	}()
	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		cancel()
		_ = child.Wait()
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- child.Wait() }()
	select {
	case <-finished:
		if child.ProcessState.ExitCode() != 137 {
			t.Fatalf("signal exit = %d; stderr = %q", child.ProcessState.ExitCode(), stderr.String())
		}
		agentPID = 0 // The supervisor joined its child; never signal a reused PID.
	case <-time.After(time.Second):
		cancel()
		<-finished
		t.Fatal("ACP wrapper forwarded TERM but never escalated for an uncooperative agent")
	}
}

func TestACPProcessFixture(t *testing.T) {
	mode := os.Getenv("VIBERMATE_TEST_ACP_PROCESS")
	if mode == "signal-supervisor" || mode == "signal-attach-supervisor" {
		executable, _ := os.Executable()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		result, err := runlauncher.RunACPProcess(ctx, runlauncher.ACPProcessConfig{
			Executable: executable, Arguments: []string{executable, "-test.run=^TestACPProcessFixture$"},
			Environment: []string{"VIBERMATE_TEST_ACP_PROCESS=signal-agent", "GORACE=atexit_sleep_ms=0"},
			Stdin:       os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
			ShutdownTimeout: 100 * time.Millisecond,
			OnStart: func(_ context.Context, pid int) error {
				if mode == "signal-attach-supervisor" {
					_, _ = fmt.Fprintf(os.Stdout, "%d\n", pid)
					time.Sleep(200 * time.Millisecond) // Simulated bounded control attachment.
				}
				return nil
			},
		})
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(94)
		}
		os.Exit(result.ExitCode)
	}
	if mode == "delayed-exit" {
		_, _ = io.WriteString(os.Stdout, "ready\n")
		_ = os.Stdout.Close()
		_, _ = os.Stderr.Write(bytes.Repeat([]byte{'x'}, 2<<20))
		time.Sleep(80 * time.Millisecond)
		os.Exit(27)
	}
	if mode == "wrapper" {
		executable, err := os.Executable()
		if err != nil {
			os.Exit(84)
		}
		reader, writer, err := os.Pipe()
		if err != nil {
			os.Exit(85)
		}
		child := exec.Command(executable, "-test.run=^TestACPProcessFixture$")
		child.Env = []string{"VIBERMATE_TEST_ACP_PROCESS=descendant", "GORACE=atexit_sleep_ms=0"}
		child.ExtraFiles = []*os.File{writer}
		if err := child.Start(); err != nil {
			os.Exit(86)
		}
		_ = writer.Close()
		ready := make([]byte, 1)
		if _, err := io.ReadFull(reader, ready); err != nil {
			_ = child.Process.Kill()
			os.Exit(87)
		}
		_, _ = fmt.Fprintf(os.Stdout, "%d\n", child.Process.Pid)
		os.Exit(0)
	}
	if mode == "ignore-input" || mode == "descendant" || mode == "signal-agent" {
		signal.Ignore(syscall.SIGTERM)
		if mode == "signal-agent" {
			_, _ = fmt.Fprintf(os.Stdout, "%d\n", os.Getpid())
		} else if mode == "descendant" {
			ready := os.NewFile(3, "fixture-ready")
			_, _ = ready.Write([]byte{1})
			_ = ready.Close()
		} else {
			_, _ = io.WriteString(os.Stdout, "ready\n")
		}
		for {
			time.Sleep(time.Second)
		}
	}
	if mode != "echo" {
		return
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(81)
	}
	directory, err := os.Getwd()
	if err != nil {
		os.Exit(82)
	}
	if err := json.NewEncoder(os.Stdout).Encode(acpProcessFixtureResult{
		Arguments: os.Args, Directory: directory,
		Setting: os.Getenv("ACP_FIXTURE_SETTING"), Input: input,
	}); err != nil {
		os.Exit(83)
	}
	_, _ = io.WriteString(os.Stderr, "child diagnostic only\n")
	os.Exit(0)
}

func TestACPProcessReapsChildWhenAttachmentFails(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, _ := acpProcessPipe(t)
	_, output := acpProcessPipe(t)
	_, stderr := acpProcessPipe(t)
	cause := errors.New("private control failure")
	pid := 0
	result, err := runlauncher.RunACPProcess(context.Background(), runlauncher.ACPProcessConfig{
		Executable: executable, Arguments: []string{executable, "-test.run=^TestACPProcessFixture$"}, Environment: []string{"VIBERMATE_TEST_ACP_PROCESS=echo", "GORACE=atexit_sleep_ms=0"},
		Stdin: input, Stdout: output, Stderr: stderr, ShutdownTimeout: 100 * time.Millisecond,
		OnStart: func(_ context.Context, childPID int) error { pid = childPID; return cause },
	})
	if !errors.Is(err, cause) || strings.Contains(err.Error(), "private") || result.ExitCode == 0 || result.AgentToClientBytes != 0 || pid <= 0 {
		t.Fatalf("attachment failure: %+v %v", result, err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatal("attachment failure left a live child")
	}
}
