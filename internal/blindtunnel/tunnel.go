// Package blindtunnel forwards a connection whose authority is not an enabled
// AgentEndpoint. It copies bytes and counts them; it never terminates TLS,
// interprets a request, or retains a tunnelled byte, so nothing it observes can
// reach a record.
package blindtunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// bufferSize bounds one copy step. It is a throughput choice, not a limit on
// the connection: a tunnel has no content budget because it never buffers a
// whole message.
const bufferSize = 32 * 1024

// Result reports what crossed the boundary. Counts and outcome are the only
// facts a blind tunnel can honestly produce.
type Result struct {
	BytesOut int64
	BytesIn  int64
}

type halfCloser interface {
	CloseWrite() error
}

// Copy forwards between the accepted client connection and the upstream
// connection until either side ends or the owner cancels. It returns when both
// directions have finished, so no half is left running after the other closes.
//
// The caller owns both connections; Copy does not close them, because the
// caller also owns the audit terminal that must be written after the counts are
// final.
func Copy(
	ctx context.Context,
	client net.Conn,
	upstream net.Conn,
) (Result, error) {
	if client == nil || upstream == nil {
		return Result{}, errors.New("blind tunnel requires both connections")
	}
	var (
		waiting  sync.WaitGroup
		outBytes int64
		inBytes  int64
		outErr   error
		inErr    error
	)
	// Cancellation and either direction ending both unblock the other, because
	// a Read on a live peer would otherwise never return.
	release := func() {
		_ = client.SetDeadline(pastDeadline())
		_ = upstream.SetDeadline(pastDeadline())
	}
	stop := context.AfterFunc(ctx, release)
	defer stop()

	// A CONNECT tunnel tears down symmetrically. A TCP full close and a
	// half close both surface as EOF on the read side, so a proxy cannot tell
	// "the client finished its request" from "the client is gone" without
	// writing. Keeping the other direction alive on that ambiguity leaks a
	// goroutine and a socket whenever the client simply disappeared, which is
	// the common case. The write side is still half-closed first so a peer
	// that does distinguish them sees the orderly signal.
	waiting.Add(2)
	go func() {
		defer waiting.Done()
		outBytes, outErr = copyHalf(upstream, client)
		halfClose(upstream)
		release()
	}()
	go func() {
		defer waiting.Done()
		inBytes, inErr = copyHalf(client, upstream)
		halfClose(client)
		release()
	}()
	waiting.Wait()

	result := Result{BytesOut: outBytes, BytesIn: inBytes}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, firstTransportError(outErr, inErr)
}

func copyHalf(destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, bufferSize)
	return io.CopyBuffer(destination, source, buffer)
}

func halfClose(connection net.Conn) {
	if closer, ok := connection.(halfCloser); ok {
		_ = closer.CloseWrite()
	}
}

// firstTransportError discards the ordinary end-of-connection signals. A peer
// closing is how a tunnel ends, not a failure to report.
func firstTransportError(errs ...error) error {
	for _, err := range errs {
		if err == nil ||
			errors.Is(err, io.EOF) ||
			errors.Is(err, net.ErrClosed) ||
			errors.Is(err, io.ErrClosedPipe) {
			continue
		}
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			continue
		}
		return err
	}
	return nil
}

func pastDeadline() time.Time {
	return time.Now().Add(-time.Second)
}
