package acpbridge_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/acpbridge"
)

type outcome struct {
	result acpbridge.Result
	err    error
}

type wire struct {
	client, agent acpbridge.Endpoint
	clientRead    *io.PipeReader
	clientWrite   *io.PipeWriter
	agentRead     *io.PipeReader
	agentWrite    *io.PipeWriter
}

func newWire(t *testing.T) wire {
	t.Helper()
	clientInput, clientWrite := io.Pipe()
	clientRead, clientOutput := io.Pipe()
	agentInput, agentWrite := io.Pipe()
	agentRead, agentOutput := io.Pipe()
	w := wire{
		client:     acpbridge.Endpoint{Input: clientInput, Output: clientOutput},
		agent:      acpbridge.Endpoint{Input: agentInput, Output: agentOutput},
		clientRead: clientRead, clientWrite: clientWrite,
		agentRead: agentRead, agentWrite: agentWrite,
	}
	t.Cleanup(func() {
		for _, c := range []io.Closer{
			clientInput, clientWrite, clientRead, clientOutput,
			agentInput, agentWrite, agentRead, agentOutput,
		} {
			_ = c.Close()
		}
	})
	return w
}

func startRelay(t *testing.T, ctx context.Context, w wire) <-chan outcome {
	t.Helper()
	finished := make(chan outcome, 1)
	go func() {
		result, err := acpbridge.Relay(ctx, w.client, w.agent)
		finished <- outcome{result, err}
	}()
	return finished
}

func await(t *testing.T, finished <-chan outcome) outcome {
	t.Helper()
	select {
	case result := <-finished:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("ACP relay failed to join its pumps")
		return outcome{}
	}
}

func transfer(t *testing.T, writer io.Writer, reader io.Reader, payload []byte) {
	t.Helper()
	written := make(chan error, 1)
	go func() {
		n, err := writer.Write(payload)
		if err == nil && n != len(payload) {
			err = io.ErrShortWrite
		}
		written <- err
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatalf("receive forwarded bytes: %v", err)
	}
	if err := <-written; err != nil {
		t.Fatalf("send bytes: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("relay changed protocol bytes")
	}
}

func TestRelayPreservesBidirectionalProtocolIncludingUnknownExtensions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := newWire(t)
	finished := startRelay(t, ctx, w)
	clientMessages := []string{
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":1,\"_meta\":{\"future\":true}}}\r\n",
		"{\"jsonrpc\":\"2.0\",\"id\":\"new-a\",\"method\":\"session/new\",\"params\":{\"cwd\":\"/workspace/a\",\"mcpServers\":[]}}\n",
		"{\"jsonrpc\":\"2.0\",\"id\":\"new-b\",\"method\":\"session/new\",\"params\":{\"cwd\":\"/workspace/b\",\"additionalDirectories\":[\"/shared\"],\"mcpServers\":[]}}\n",
		"{\"jsonrpc\":\"2.0\",\"id\":\"load\",\"method\":\"session/load\",\"params\":{\"sessionId\":\"s-c\",\"cwd\":\"/workspace/c\",\"mcpServers\":[]}}\n",
		"{\"jsonrpc\":\"2.0\",\"id\":\"resume\",\"method\":\"session/resume\",\"params\":{\"sessionId\":\"s-d\",\"cwd\":\"/workspace/d\",\"mcpServers\":[]}}\n",
		"{\"jsonrpc\":\"2.0\",\"method\":\"session/cancel\",\"params\":{\"sessionId\":\"s-a\"}}\n",
		"{\"jsonrpc\":\"2.0\",\"method\":\"$/cancel_request\",\"params\":{\"requestId\":\"resume\"}}\n",
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"outcome\":{\"outcome\":\"cancelled\"}}}\n",
		"{\"jsonrpc\":\"2.0\",\"id\":\"ext\",\"method\":\"_future/example\",\"params\":{\"literal\":\"\\u0061\"}}\n",
	}
	agentMessages := []string{
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":1,\"agentCapabilities\":{\"future\":true}}}\n",
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"session/request_permission\",\"params\":{\"sessionId\":\"s-a\",\"options\":[]}}\n",
		"{\"jsonrpc\":\"2.0\",\"id\":\"ext\",\"method\":\"fs/read_text_file\",\"params\":{\"sessionId\":\"s-b\",\"path\":\"/workspace/b/a.go\"}}\n",
		"{\"jsonrpc\":\"2.0\",\"id\":\"question\",\"method\":\"cursor/ask_question\",\"params\":{\"future\":true}}\n",
		"{\"jsonrpc\":\"2.0\",\"id\":\"plan\",\"method\":\"cursor/create_plan\",\"params\":{\"future\":true}}\n",
		"{\"jsonrpc\":\"2.0\",\"method\":\"cursor/update_todos\",\"params\":{\"future\":true}}\n",
		"{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"s-a\",\"update\":{\"sessionUpdate\":\"future_update\"}}}\n",
	}
	var toAgent, toClient int64
	for _, message := range clientMessages {
		transfer(t, w.clientWrite, w.agentRead, []byte(message))
		toAgent += int64(len(message))
	}
	for _, message := range agentMessages {
		transfer(t, w.agentWrite, w.clientRead, []byte(message))
		toClient += int64(len(message))
	}
	_ = w.agentWrite.Close()
	got := await(t, finished)
	if got.err != nil || got.result.ClientToAgentBytes != toAgent || got.result.AgentToClientBytes != toClient {
		t.Fatalf("relay result = %+v, %v", got.result, got.err)
	}
}

func TestRelayDoesNotFrameOrRejectLargeMessages(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := newWire(t)
	finished := startRelay(t, ctx, w)
	// Exceeds the Coder SDK's 10 MiB line limit. The relay also preserves
	// fragments, malformed input, and non-UTF-8 bytes; validation is peer-owned.
	payload := append([]byte("{\"future\":\""), bytes.Repeat([]byte("x"), 11<<20)...)
	payload = append(payload, []byte("\"}\n")...)
	transfer(t, w.clientWrite, w.agentRead, payload)
	for _, fragment := range [][]byte{
		{0xff, 0x00}, []byte("{\"incomplete\":\""), {0xe4}, {0xb8, 0xad, 0xe6}, {0x96, 0x87}, []byte("\"}"), []byte("\n"),
	} {
		transfer(t, w.agentWrite, w.clientRead, fragment)
	}
	_ = w.agentWrite.Close()
	got := await(t, finished)
	if got.err != nil || got.result.ClientToAgentBytes != int64(len(payload)) {
		t.Fatalf("large transfer failed: %+v, %v", got.result, got.err)
	}
}

func TestClientEOFClosesAgentInputAndDrainsFinalResponse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := newWire(t)
	finished := startRelay(t, ctx, w)
	transfer(t, w.clientWrite, w.agentRead, []byte("request\n"))
	_ = w.clientWrite.Close()
	if trailing, err := io.ReadAll(w.agentRead); err != nil || len(trailing) != 0 {
		t.Fatalf("agent input did not reach EOF: %d bytes, %v", len(trailing), err)
	}
	transfer(t, w.agentWrite, w.clientRead, []byte("final response\n"))
	_ = w.agentWrite.Close()
	got := await(t, finished)
	if got.err != nil || got.result.ClientToAgentBytes != 8 || got.result.AgentToClientBytes != 15 {
		t.Fatalf("final response did not drain: %+v, %v", got.result, got.err)
	}
	if _, err := w.clientRead.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("client output remains open: %v", err)
	}
}

func TestAgentEOFUnblocksIdleClientInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := newWire(t)
	finished := startRelay(t, ctx, w)
	_ = w.agentWrite.Close()
	if got := await(t, finished); got.err != nil || got.result != (acpbridge.Result{}) {
		t.Fatalf("agent EOF result = %+v, %v", got.result, got.err)
	}
}

func TestAgentEOFMustNotSilentlyDiscardAcceptedInput(t *testing.T) {
	for _, test := range []struct {
		name      string
		delivered int
	}{{"none", 0}, {"partial", 3}} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			w := newWire(t)
			finished := startRelay(t, ctx, w)
			// The source accepts all seven bytes, but the agent stops reading
			// before delivery completes. Successful shutdown would lose data.
			if _, err := w.clientWrite.Write([]byte("pending")); err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadFull(w.agentRead, make([]byte, test.delivered)); err != nil {
				t.Fatal(err)
			}
			_ = w.agentWrite.Close()
			got := await(t, finished)
			if !errors.Is(got.err, acpbridge.ErrIncompleteTransfer) || got.result.ClientToAgentBytes != int64(test.delivered) {
				t.Fatalf("pending input lost without a failure: %+v, %v", got.result, got.err)
			}
		})
	}
}

type observedRead struct {
	io.ReadCloser
	started chan struct{}
	once    sync.Once
	active  atomic.Int32
}

func (r *observedRead) Read(p []byte) (int, error) {
	r.active.Add(1)
	defer r.active.Add(-1)
	r.once.Do(func() { close(r.started) })
	return r.ReadCloser.Read(p)
}

func TestCancellationJoinsBothIdlePumps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := newWire(t)
	client := &observedRead{ReadCloser: w.client.Input, started: make(chan struct{})}
	agent := &observedRead{ReadCloser: w.agent.Input, started: make(chan struct{})}
	w.client.Input, w.agent.Input = client, agent
	finished := startRelay(t, ctx, w)
	<-client.started
	<-agent.started
	cancel()
	if got := await(t, finished); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("cancellation = %v", got.err)
	}
	if client.active.Load() != 0 || agent.active.Load() != 0 {
		t.Fatal("relay returned before both readers stopped")
	}
}

func TestCancellationUnblocksBackpressureInEitherDirection(t *testing.T) {
	for _, fromClient := range []bool{true, false} {
		name := "agent-to-client"
		if fromClient {
			name = "client-to-agent"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := newWire(t)
			finished := startRelay(t, ctx, w)
			writer := w.agentWrite
			if fromClient {
				writer = w.clientWrite
			}
			// Input acceptance proves the pump has reached the deliberately
			// undrained destination. Do not add a queue to bypass backpressure.
			if _, err := writer.Write([]byte("pending")); err != nil {
				t.Fatal(err)
			}
			cancel()
			got := await(t, finished)
			if !errors.Is(got.err, context.Canceled) || got.result != (acpbridge.Result{}) {
				t.Fatalf("blocked copy result = %+v, %v", got.result, got.err)
			}
		})
	}
}

func TestBackpressureDoesNotBlockTheOppositeDirection(t *testing.T) {
	for _, fromClient := range []bool{true, false} {
		name := "agent-to-client"
		if fromClient {
			name = "client-to-agent"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			w := newWire(t)
			finished := startRelay(t, ctx, w)
			blockedWrite, blockedRead := w.agentWrite, w.clientRead
			flowingWrite, flowingRead := w.clientWrite, w.agentRead
			if fromClient {
				blockedWrite, blockedRead = w.clientWrite, w.agentRead
				flowingWrite, flowingRead = w.agentWrite, w.clientRead
			}
			if _, err := blockedWrite.Write([]byte("pending")); err != nil {
				t.Fatal(err)
			}
			transfer(t, flowingWrite, flowingRead, []byte("flowing"))
			gotBytes := make([]byte, 7)
			if _, err := io.ReadFull(blockedRead, gotBytes); err != nil || string(gotBytes) != "pending" {
				t.Fatalf("blocked bytes did not drain: %q, %v", gotBytes, err)
			}
			_ = w.agentWrite.Close()
			got := await(t, finished)
			if got.err != nil || got.result.ClientToAgentBytes != 7 || got.result.AgentToClientBytes != 7 {
				t.Fatalf("duplex progress = %+v, %v", got.result, got.err)
			}
		})
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }
func (errorReader) Close() error               { return nil }

type shortWriter struct{ io.WriteCloser }

func (shortWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

type closeErrorWriter struct {
	io.WriteCloser
	err error
}

func (w closeErrorWriter) Close() error {
	_ = w.WriteCloser.Close()
	return w.err
}

func TestGracefulCloseFailuresAreReportedWithoutPrivateDiagnostics(t *testing.T) {
	for _, fromClient := range []bool{true, false} {
		name := "agent-to-client"
		if fromClient {
			name = "client-to-agent"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			w := newWire(t)
			cause := errors.New("private output-close diagnostic")
			if fromClient {
				w.agent.Output = closeErrorWriter{w.agent.Output, cause}
			} else {
				w.client.Output = closeErrorWriter{w.client.Output, cause}
			}
			finished := startRelay(t, ctx, w)
			if fromClient {
				_ = w.clientWrite.Close()
			} else {
				_ = w.agentWrite.Close()
			}
			got := await(t, finished)
			if !errors.Is(got.err, cause) || strings.Contains(got.err.Error(), cause.Error()) {
				t.Fatalf("output-close error is missing or unsafe: %v", got.err)
			}
		})
	}
}

func TestFailureIsPayloadFreeButRetainsErrorIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := newWire(t)
	cause := errors.New("private-token-and-prompt-do-not-log")
	w.client.Input = errorReader{cause}
	got := await(t, startRelay(t, ctx, w))
	if !errors.Is(got.err, cause) || strings.Contains(got.err.Error(), cause.Error()) {
		t.Fatalf("unsafe or unidentifiable transfer error: %v", got.err)
	}
	var failure *acpbridge.TransferError
	if !errors.As(got.err, &failure) || failure.Error() != "ACP client-to-agent transfer failed" {
		t.Fatalf("unexpected failure shape: %v", got.err)
	}
}

func TestShortWriteFailsAndCountsOnlyAcceptedBytes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := newWire(t)
	w.agent.Output = shortWriter{w.agent.Output}
	finished := startRelay(t, ctx, w)
	_, _ = w.clientWrite.Write([]byte("12345678"))
	got := await(t, finished)
	if !errors.Is(got.err, io.ErrShortWrite) || got.result.ClientToAgentBytes != 4 {
		t.Fatalf("short write = %+v, %v", got.result, got.err)
	}
}

type countReader struct {
	io.ReadCloser
	closed int
}

func (r *countReader) Close() error { r.closed++; return r.ReadCloser.Close() }

type countWriter struct {
	io.WriteCloser
	closed int
}

func (w *countWriter) Close() error { w.closed++; return w.WriteCloser.Close() }

func TestPreCancelledRelayClosesEveryOwnedHalfOnce(t *testing.T) {
	w := newWire(t)
	ci, ai := &countReader{ReadCloser: w.client.Input}, &countReader{ReadCloser: w.agent.Input}
	co, ao := &countWriter{WriteCloser: w.client.Output}, &countWriter{WriteCloser: w.agent.Output}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := acpbridge.Relay(ctx, acpbridge.Endpoint{Input: ci, Output: co}, acpbridge.Endpoint{Input: ai, Output: ao})
	if !errors.Is(err, context.Canceled) || ci.closed != 1 || ai.closed != 1 || co.closed != 1 || ao.closed != 1 {
		t.Fatalf("ownership after early cancellation = %v (%d %d %d %d)", err, ci.closed, ai.closed, co.closed, ao.closed)
	}
}

func TestInvalidArgumentsDoNotAcquireOwnership(t *testing.T) {
	w := newWire(t)
	input := &countReader{ReadCloser: w.client.Input}
	w.client.Input = input
	if _, err := acpbridge.Relay(nil, w.client, w.agent); !errors.Is(err, acpbridge.ErrInvalidStreams) || input.closed != 0 {
		t.Fatalf("invalid context acquired ownership: %v", err)
	}
	if _, err := acpbridge.Relay(context.Background(), w.client, acpbridge.Endpoint{}); !errors.Is(err, acpbridge.ErrInvalidStreams) || input.closed != 0 {
		t.Fatalf("missing stream acquired ownership: %v", err)
	}
}

func TestRelayWithRealChildKeepsStderrSeparateAndReapsAfterDrain(t *testing.T) {
	if os.Getenv("VIBERMATE_ACP_RELAY_FIXTURE") == "1" {
		t.Skip("parent-only test")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, executable, "-test.run=^TestACPRelayFixtureProcess$")
	child.Env = []string{"VIBERMATE_ACP_RELAY_FIXTURE=1"}
	if testing.CoverMode() != "" {
		// Keep the Go coverage runtime's diagnostics off the fixture stderr
		// without inheriting any personal environment or coverage directory.
		child.Env = append(child.Env, "GOCOVERDIR="+t.TempDir())
	}
	var diagnostic bytes.Buffer
	child.Stderr = &diagnostic
	childInput, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	childOutput, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	var waitOnce sync.Once
	var waitError error
	waitChild := func() error {
		waitOnce.Do(func() { waitError = child.Wait() })
		return waitError
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = waitChild()
	})
	w := newWire(t)
	w.agent = acpbridge.Endpoint{Input: childOutput, Output: childInput}
	finished := startRelay(t, ctx, w)
	transfer(t, w.clientWrite, w.clientRead, []byte("{\"opaque\":true}\n"))
	_ = w.clientWrite.Close()
	tail, err := io.ReadAll(w.clientRead)
	if err != nil || string(tail) != "{\"final\":true}\n" {
		t.Fatalf("child final response = %q, %v", tail, err)
	}
	if got := await(t, finished); got.err != nil {
		t.Fatal(got.err)
	}
	if err := waitChild(); err != nil {
		t.Fatalf("child did not exit cleanly: %v", err)
	}
	if diagnostic.String() != "fixture diagnostic\n" {
		t.Fatalf("stderr = %q", diagnostic.String())
	}
}

func TestACPRelayFixtureProcess(t *testing.T) {
	if os.Getenv("VIBERMATE_ACP_RELAY_FIXTURE") != "1" {
		return
	}
	_, _ = io.WriteString(os.Stderr, "fixture diagnostic\n")
	if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
		os.Exit(2)
	}
	_, _ = io.WriteString(os.Stdout, "{\"final\":true}\n")
	os.Exit(0)
}
