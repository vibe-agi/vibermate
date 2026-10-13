package socksproxy

import (
	"context"
	"io"
)

// This is a handshake budget, not an established-stream size limit.
const maxPendingConnectInputBytes = 64 << 10

type connectInputChunk struct {
	data []byte
	err  error
}

// One worker owns the client reader for its entire lifetime. The unbuffered
// handoff backpressures established traffic; shutdown closes the socket and
// joins done even if the worker is blocked in Read or delivering a chunk.
func readConnectInput(ctx context.Context, reader io.Reader, chunks chan<- connectInputChunk, done chan<- struct{}) {
	defer close(done)
	defer close(chunks)
	for {
		buffer := make([]byte, 4096)
		n, err := reader.Read(buffer)
		select {
		case chunks <- connectInputChunk{buffer[:n], err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

type connectInputStream struct {
	ctx     context.Context
	chunks  <-chan connectInputChunk
	pending []byte
	err     error
}

func (r *connectInputStream) Read(p []byte) (int, error) {
	for len(r.pending) == 0 && r.err == nil {
		select {
		case chunk, ok := <-r.chunks:
			if !ok {
				r.err = io.EOF
				break
			}
			r.pending, r.err = chunk.data, chunk.err
		case <-r.ctx.Done():
			r.err = r.ctx.Err()
		}
	}
	if len(r.pending) > 0 {
		n := copy(p, r.pending)
		r.pending = r.pending[n:]
		return n, nil
	}
	return 0, r.err
}
