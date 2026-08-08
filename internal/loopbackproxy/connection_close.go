package loopbackproxy

import (
	"context"
	"errors"
	"net"
	"sync"
)

var (
	ErrProxyStopping          = errors.New("loopback proxy is stopping")
	errConnectionAlreadyBound = errors.New("connection close handle is already bound")
)

// deferredConnectionCloseHandle lets Capture assignment admission happen
// before http.Hijacker yields the downstream socket. A reconnect request that
// wins that race closes the later socket immediately instead of allowing one
// request under an obsolete Environment connection contract.
type deferredConnectionCloseHandle struct {
	mu             sync.Mutex
	connection     net.Conn
	bound          bool
	closeRequested bool
}

func (handle *deferredConnectionCloseHandle) Bind(connection net.Conn) error {
	if handle == nil || connection == nil {
		return ErrProxyStopping
	}
	handle.mu.Lock()
	if handle.bound {
		handle.mu.Unlock()
		return errConnectionAlreadyBound
	}
	handle.bound = true
	if handle.closeRequested {
		handle.mu.Unlock()
		_ = connection.Close()
		return ErrProxyStopping
	}
	handle.connection = connection
	handle.mu.Unlock()
	return nil
}

func (handle *deferredConnectionCloseHandle) Close(context.Context) error {
	if handle == nil {
		return nil
	}
	handle.mu.Lock()
	handle.closeRequested = true
	connection := handle.connection
	handle.connection = nil
	handle.mu.Unlock()
	if connection == nil {
		return nil
	}
	err := connection.Close()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
