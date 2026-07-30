package desktophost

import (
	"net"
	"sync"
)

type trackedListener struct {
	net.Listener

	mu          sync.Mutex
	connections map[*trackedConnection]struct{}
}

type trackedConnection struct {
	net.Conn
	owner *trackedListener
	once  sync.Once
}

func newTrackedListener(listener net.Listener) *trackedListener {
	return &trackedListener{
		Listener:    listener,
		connections: make(map[*trackedConnection]struct{}),
	}
}

func (listener *trackedListener) Accept() (net.Conn, error) {
	connection, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tracked := &trackedConnection{Conn: connection, owner: listener}
	listener.mu.Lock()
	listener.connections[tracked] = struct{}{}
	listener.mu.Unlock()
	return tracked, nil
}

func (listener *trackedListener) closeTracked() {
	if listener == nil {
		return
	}
	listener.mu.Lock()
	connections := make([]*trackedConnection, 0, len(listener.connections))
	for connection := range listener.connections {
		connections = append(connections, connection)
	}
	listener.mu.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
}

func (connection *trackedConnection) Close() error {
	var err error
	connection.once.Do(func() {
		err = connection.Conn.Close()
		connection.owner.mu.Lock()
		delete(connection.owner.connections, connection)
		connection.owner.mu.Unlock()
	})
	return err
}
