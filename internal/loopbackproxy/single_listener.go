package loopbackproxy

import (
	"net"
	"sync"
)

type notifyingConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (connection *notifyingConn) Close() error {
	err := connection.Conn.Close()
	connection.once.Do(func() {
		close(connection.closed)
	})
	return err
}

type singleConnListener struct {
	mu       sync.Mutex
	conn     *notifyingConn
	accepted bool
	closed   chan struct{}
	once     sync.Once
}

func newSingleConnListener(connection net.Conn) *singleConnListener {
	closed := make(chan struct{})
	return &singleConnListener{
		conn: &notifyingConn{
			Conn:   connection,
			closed: closed,
		},
		closed: closed,
	}
}

func (listener *singleConnListener) Accept() (net.Conn, error) {
	listener.mu.Lock()
	if !listener.accepted {
		listener.accepted = true
		connection := listener.conn
		listener.mu.Unlock()
		return connection, nil
	}
	closed := listener.closed
	listener.mu.Unlock()
	<-closed
	return nil, net.ErrClosed
}

func (listener *singleConnListener) Close() error {
	listener.once.Do(func() {
		_ = listener.conn.Close()
	})
	return nil
}

func (listener *singleConnListener) Addr() net.Addr {
	if listener.conn == nil {
		return nil
	}
	return listener.conn.LocalAddr()
}

var _ net.Listener = (*singleConnListener)(nil)
