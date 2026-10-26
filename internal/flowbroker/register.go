//go:build linux || darwin

package flowbroker

import (
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"
)

// Registration is a run's live claim on its descendants' flows. Closing it
// (or the process exiting) ends the claim.
type Registration struct {
	conn      *net.UnixConn
	extension bool
	timeout   time.Duration
	done      chan struct{}
	once      sync.Once
}

// Register claims the calling process's descendants for run. The broker
// identifies the caller from socket credentials; handle receives each owned
// flow and owns it. A non-empty token also claims any process whose
// environment carries TokenEnvironment=token.
func Register(socketPath, run, token string, handle func(net.Conn)) (*Registration, error) {
	return register(socketPath, run, token, handle, keepaliveTimeout)
}

func register(socketPath, run, token string, handle func(net.Conn), timeout time.Duration) (*Registration, error) {
	if handle == nil {
		return nil, errors.New("flow handler is required")
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, err
	}
	request, _ := json.Marshal(registrationRequest{Version: 1, Run: run, Token: token})
	_ = conn.SetDeadline(time.Now().Add(requestTimeout))
	if _, err := conn.Write(append(request, '\n')); err != nil {
		conn.Close()
		return nil, err
	}
	// Read the reply byte by byte: a buffered reader could swallow the first
	// passed descriptor's data byte.
	line, err := readLine(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	var reply registrationReply
	if json.Unmarshal(line, &reply) != nil || !reply.OK {
		conn.Close()
		if reply.Error != "" {
			return nil, errors.New("flow broker refused registration: " + reply.Error)
		}
		return nil, errors.New("flow broker refused registration")
	}
	_ = conn.SetDeadline(time.Time{})
	registration := &Registration{conn: conn, extension: reply.Extension, timeout: timeout, done: make(chan struct{})}
	go registration.receive(handle)
	return registration, nil
}

func readLine(conn *net.UnixConn) ([]byte, error) {
	line := make([]byte, 0, 64)
	one := make([]byte, 1)
	for len(line) < maxRequestBytes {
		if _, err := conn.Read(one); err != nil {
			return nil, err
		}
		if one[0] == '\n' {
			return line, nil
		}
		line = append(line, one[0])
	}
	return nil, errors.New("flow broker reply is too long")
}

// keepaliveTimeout is how long a registration waits for any message from
// the broker before treating its claim as lost: a broker that stopped
// answering may also have stopped answering the extension.
const keepaliveTimeout = 3 * livenessInterval

func (registration *Registration) receive(handle func(net.Conn)) {
	defer close(registration.done)
	defer registration.conn.Close()
	for {
		_ = registration.conn.SetReadDeadline(time.Now().Add(registration.timeout))
		flow, err := receiveConn(registration.conn)
		if err != nil {
			return
		}
		if flow != nil {
			go handle(flow)
		}
	}
}

// Extension reports whether the broker had heard from an active capture
// extension when the claim was made. A claim without one captures nothing.
func (registration *Registration) Extension() bool { return registration.extension }

// Done is closed when the claim ends: the broker closed it, vibermated
// exited, or Close was called.
func (registration *Registration) Done() <-chan struct{} { return registration.done }

// Close ends the claim and waits for the receiver to stop.
func (registration *Registration) Close() error {
	var err error
	registration.once.Do(func() { err = registration.conn.Close() })
	<-registration.done
	return err
}
