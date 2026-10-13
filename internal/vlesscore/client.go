// Package vlesscore owns an isolated VLESS/REALITY core and its private SOCKS transport.
package vlesscore

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

var ErrClosed = errors.New("vlesscore: client closed or core exited")

type Client struct {
	mu                 sync.Mutex
	closed             bool
	connections        map[io.Closer]struct{}
	process            *exec.Cmd
	done               chan struct{}
	dir                string
	endpoint           string
	username, password string
	udpPath            string
	udpID              [16]byte
	closeOnce          sync.Once
}

func (*Client) String() string   { return "vlesscore.Client{redacted}" }
func (*Client) GoString() string { return "vlesscore.Client{redacted}" }

// Start launches one immutable core. The owner context owns its lifetime.
func Start(owner context.Context, config Config) (*Client, error) {
	if err := owner.Err(); err != nil {
		return nil, err
	}
	config, err := config.normalized()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("vlesscore: private listener allocation failed")
	}
	endpoint := listener.Addr().String()
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	username, err := randomCredential()
	if err != nil {
		return nil, err
	}
	password, err := randomCredential()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "vibermate-vless-")
	if err != nil {
		return nil, errors.New("vlesscore: private configuration directory failed")
	}
	path := filepath.Join(dir, "core.json")
	udpPath := filepath.Join(dir, "u")
	udpID, err := randomUUID()
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	data, err := config.coreJSON(port, username, password, udpPath, udpID)
	if err == nil {
		err = os.WriteFile(path, data, 0600)
	}
	if err != nil {
		os.RemoveAll(dir)
		return nil, errors.New("vlesscore: private configuration creation failed")
	}
	command := exec.Command(config.Executable, "run", "-c", path)
	command.Dir = dir
	// Core output may contain configuration secrets; do not attach it to diagnostics.
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		os.RemoveAll(dir)
		return nil, errors.New("vlesscore: core process start failed")
	}
	c := &Client{connections: make(map[io.Closer]struct{}), process: command, done: make(chan struct{}), dir: dir, endpoint: endpoint, username: username, password: password, udpPath: udpPath, udpID: udpID}
	go func() {
		command.Wait()
		for _, conn := range c.markClosed() {
			conn.Close()
		}
		os.RemoveAll(dir)
		close(c.done)
	}()
	startup, cancel := context.WithTimeout(owner, 5*time.Second)
	defer cancel()
	for {
		if c.isClosed() {
			c.Close()
			return nil, errors.New("vlesscore: core exited during startup")
		}
		probeCtx, probeCancel := context.WithTimeout(startup, 200*time.Millisecond)
		conn, err := c.authenticate(probeCtx)
		probeCancel()
		if err == nil {
			conn.Close()
			if startup.Err() != nil || c.isClosed() {
				c.Close()
				return nil, errors.New("vlesscore: startup canceled or core exited")
			}
			go func() {
				select {
				case <-owner.Done():
					c.Close()
				case <-c.done:
				}
			}()
			return c, nil
		}
		select {
		case <-startup.Done():
			c.Close()
			return nil, errors.New("vlesscore: core readiness canceled or timed out")
		case <-c.done:
			c.Close()
			return nil, errors.New("vlesscore: core exited during startup")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (c *Client) isClosed() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.closed }
func (c *Client) track(conn io.Closer) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	c.connections[conn] = struct{}{}
	return nil
}
func (c *Client) untrack(conn io.Closer) { c.mu.Lock(); delete(c.connections, conn); c.mu.Unlock() }
func (c *Client) markClosed() []io.Closer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	connections := make([]io.Closer, 0, len(c.connections))
	for conn := range c.connections {
		connections = append(connections, conn)
	}
	return connections
}

// Close drains connections, joins the owned process, and removes its private files.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		for _, conn := range c.markClosed() {
			conn.Close()
		}
		c.process.Process.Signal(os.Interrupt)
		select {
		case <-c.done:
		case <-time.After(2 * time.Second):
			c.process.Process.Kill()
			<-c.done
		}
		os.RemoveAll(c.dir)
	})
	return nil
}

type ownedConn struct {
	net.Conn
	client *Client
	once   sync.Once
}

func (c *ownedConn) Close() error {
	var err error
	c.once.Do(func() { err = c.Conn.Close(); c.client.untrack(c) })
	return err
}

// DialContext accepts only TCP and resolved literal destination authorities.
func (c *Client) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, errors.New("vlesscore: unsupported stream network")
	}
	target, err := netip.ParseAddrPort(address)
	if err != nil || !validTarget(target) {
		return nil, errors.New("vlesscore: TCP target must be a literal address with a nonzero port")
	}
	if network == "tcp4" && !target.Addr().Is4() || network == "tcp6" && !target.Addr().Is6() {
		return nil, errors.New("vlesscore: target address family does not match network")
	}
	conn, _, err := c.command(ctx, 1, target)
	if err != nil {
		return nil, err
	}
	owned := &ownedConn{Conn: conn, client: c}
	if err := c.track(owned); err != nil {
		conn.Close()
		return nil, err
	}
	return owned, nil
}
func validTarget(target netip.AddrPort) bool {
	return target.IsValid() && target.Port() != 0 && !target.Addr().IsUnspecified() && target.Addr().Zone() == ""
}
