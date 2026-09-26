// Package acpbridge provides the byte-opaque transport for an editor-launched
// ACP agent. It is not an ACP agent, JSON-RPC router, transcript recorder, or
// process supervisor. In particular, it cannot answer permission requests.
package acpbridge

import (
	"context"
	"errors"
	"io"
	"sync"
)

const copyBufferBytes = 32 << 10

var (
	ErrInvalidStreams     = errors.New("ACP relay requires a context and four owned streams")
	ErrIncompleteTransfer = errors.New("ACP relay closed with undelivered input")
)

// Endpoint is one peer's read and write half, as seen by the relay. Input and
// Output must be independently closeable; closing either must unblock pending
// I/O on that half. Use dedicated pipes, not a borrowed terminal or log stream.
// Relay owns both halves after validating all arguments and closes them before
// returning. Callers retain process supervision and stderr ownership.
type Endpoint struct {
	Input  io.ReadCloser
	Output io.WriteCloser
}

// Result counts bytes accepted by the destination streams, not ACP requests,
// successful prompts, tokens, or retained content. It contains no payload data.
type Result struct {
	ClientToAgentBytes int64
	AgentToClientBytes int64
}

// TransferError keeps diagnostics payload-free even if a stream's error embeds
// private content. Its cause remains available to errors.Is/errors.As; callers
// must not log unwrapped causes to stderr or retained diagnostics either. No
// diagnostics, including this safe error, belong on the protocol stdout.
type TransferError struct {
	direction string
	operation string
	cause     error
}

func (err *TransferError) Error() string {
	return "ACP " + err.direction + " " + err.operation + " failed"
}

func (err *TransferError) Unwrap() error { return err.cause }

type ownedCloser struct {
	stream io.Closer
	once   sync.Once
	err    error
}

func (closer *ownedCloser) close() error {
	closer.once.Do(func() { closer.err = closer.stream.Close() })
	return closer.err
}

type transferResult struct {
	fromClient bool
	readBytes  int64
	bytes      int64
	err        error
}

type countingReader struct {
	source io.Reader
	bytes  int64
}

func (reader *countingReader) Read(p []byte) (int, error) {
	n, err := reader.source.Read(p)
	reader.bytes += int64(n)
	return n, err
}

// Relay forwards both directions concurrently, without framing, parsing,
// changing IDs/capabilities, buffering a complete line, or retaining content.
// Unknown methods and arbitrarily large messages pass through unchanged.
//
// Client EOF closes only the agent's input, allowing a final response to drain.
// Agent EOF, an I/O failure, or context cancellation closes all owned streams
// and joins both pumps. A caller must bound the context while waiting for an
// agent that does not exit after input EOF. Closing streams does not kill or
// reap a child: that remains the existing process supervisor's responsibility.
// Agent EOF may interrupt idle client input, but discarding already-read bytes
// is an ErrIncompleteTransfer, never a successful result. Graceful output-close
// failures are reported; cleanup failures do not replace an earlier failure.
func Relay(ctx context.Context, client, agent Endpoint) (Result, error) {
	if ctx == nil || client.Input == nil || client.Output == nil ||
		agent.Input == nil || agent.Output == nil {
		return Result{}, ErrInvalidStreams
	}
	clientInput := ownedCloser{stream: client.Input}
	clientOutput := ownedCloser{stream: client.Output}
	agentInput := ownedCloser{stream: agent.Input}
	agentOutput := ownedCloser{stream: agent.Output}
	closeAll := func() {
		_ = clientInput.close()
		_ = clientOutput.close()
		_ = agentInput.close()
		_ = agentOutput.close()
	}
	defer closeAll()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	completed := make(chan transferResult, 2)
	pump := func(fromClient bool, source io.Reader, target io.Writer) {
		// Hide WriterTo/ReaderFrom so an optional fast path cannot replace the
		// relay's fixed-size copying with a peer-supplied whole-message buffer.
		reader := countingReader{source: source}
		n, err := io.CopyBuffer(
			struct{ io.Writer }{target}, &reader,
			make([]byte, copyBufferBytes),
		)
		completed <- transferResult{fromClient: fromClient, readBytes: reader.bytes, bytes: n, err: err}
	}
	go pump(true, client.Input, agent.Output)
	go pump(false, agent.Input, client.Output)

	var result Result
	var terminal error
	stopping := false
	cancelled := ctx.Done()
	for remaining := 2; remaining > 0; {
		select {
		case <-cancelled:
			if !stopping {
				terminal = ctx.Err()
				stopping = true
				closeAll()
			}
			cancelled = nil
		case copied := <-completed:
			remaining--
			direction := "agent-to-client"
			if copied.fromClient {
				direction = "client-to-agent"
				result.ClientToAgentBytes = copied.bytes
			} else {
				result.AgentToClientBytes = copied.bytes
			}
			if stopping {
				// Agent EOF interrupts the idle input reader, but must not turn
				// interrupted delivery into success. Cancellation or a preceding
				// I/O error still remains the primary failure.
				if terminal == nil && copied.readBytes > copied.bytes {
					terminal = &TransferError{direction, "transfer", ErrIncompleteTransfer}
				}
				continue
			}
			if err := ctx.Err(); err != nil {
				terminal = err
				stopping = true
				closeAll()
				continue
			}
			if copied.err != nil {
				terminal = &TransferError{direction, "transfer", copied.err}
				stopping = true
				closeAll()
				continue
			}
			if copied.fromClient {
				if err := agentOutput.close(); err != nil {
					terminal = &TransferError{"client-to-agent", "input close", err}
					stopping = true
					closeAll()
				}
			} else {
				if err := clientOutput.close(); err != nil {
					terminal = &TransferError{"agent-to-client", "output close", err}
				}
				stopping = true
				closeAll()
			}
		}
	}
	return result, terminal
}
