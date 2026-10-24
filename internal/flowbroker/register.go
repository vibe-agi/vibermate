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
	conn *net.UnixConn
	done chan struct{}
	once sync.Once
}

// Register claims the calling process's descendants for run. The broker
// identifies the caller from socket credentials; handle receives each owned
// flow and owns it.
func Register(socketPath, run string, handle func(net.Conn)) (*Registration, error) {
	if handle == nil {
		return nil, errors.New("flow handler is required")
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, err
	}
	request, _ := json.Marshal(registrationRequest{Version: 1, Run: run})
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
	registration := &Registration{conn: conn, done: make(chan struct{})}
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

func (registration *Registration) receive(handle func(net.Conn)) {
	defer close(registration.done)
	for {
		flow, err := receiveConn(registration.conn)
		if err != nil {
			return
		}
		go handle(flow)
	}
}

// Close ends the claim and waits for the receiver to stop.
func (registration *Registration) Close() error {
	var err error
	registration.once.Do(func() { err = registration.conn.Close() })
	<-registration.done
	return err
}
