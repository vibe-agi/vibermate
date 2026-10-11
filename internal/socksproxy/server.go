package socksproxy

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"net"
	"net/http"
	"net/netip"
	"sync"
)

type Target struct {
	Host string
	Port uint16
}
type DatagramFlow interface {
	ReadPacket(context.Context, []byte) (int, error)
	WritePacket(context.Context, []byte) error
	Close() error
}
type OpenedUDP struct {
	Flow DatagramFlow
	Peer netip.AddrPort
}
type UDPOpener interface {
	OpenUDP(context.Context, captureadmission.Admission, Target) (OpenedUDP, error)
}
type Options struct {
	Authorizer   captureadmission.Authorizer
	TCPHandler   http.Handler
	UDP          UDPOpener
	TargetAccess egressnetwork.TargetAccess
}

// Server owns protocol sockets; Runtime supplies destination authority.
type Server struct {
	options   Options
	mu        sync.Mutex
	stopped   bool
	listeners map[net.Listener]struct{}
	sessions  map[*session]struct{}
	workers   sync.WaitGroup
}

func New(o Options) (*Server, error) {
	if o.Authorizer == nil || o.TCPHandler == nil || o.UDP == nil {
		return nil, errors.New("socksproxy: incomplete dependencies")
	}
	if o.TargetAccess != egressnetwork.TargetAccessPublicOnly && o.TargetAccess != egressnetwork.TargetAccessOwnerConfigured {
		return nil, errors.New("socksproxy: explicit target access required")
	}
	return &Server{options: o, listeners: make(map[net.Listener]struct{}), sessions: make(map[*session]struct{})}, nil
}
func (s *Server) Serve(l net.Listener) error {
	if l == nil {
		return errors.New("socksproxy: missing listener")
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		l.Close()
		return net.ErrClosed
	}
	s.listeners[l] = struct{}{}
	s.workers.Add(1)
	s.mu.Unlock()
	defer func() { l.Close(); s.mu.Lock(); delete(s.listeners, l); s.mu.Unlock(); s.workers.Done() }()
	for {
		c, e := l.Accept()
		if e != nil {
			return e
		}
		ctx, cancel := context.WithCancel(egressnetwork.WithTargetAccess(context.Background(), s.options.TargetAccess))
		x := &session{server: s, control: c, ctx: ctx, cancel: cancel, flows: make(map[Target]*udpFlow), done: make(chan struct{})}
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			x.close()
			continue
		}
		s.sessions[x] = struct{}{}
		s.workers.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.workers.Done()
			defer func() {
				x.close()
				x.workers.Wait()
				s.mu.Lock()
				delete(s.sessions, x)
				s.mu.Unlock()
				close(x.done)
			}()
			x.run()
		}()
	}
}
func (s *Server) BeginShutdown() {
	s.mu.Lock()
	s.stopped = true
	listeners := make([]net.Listener, 0, len(s.listeners))
	for l := range s.listeners {
		listeners = append(listeners, l)
	}
	sessions := make([]*session, 0, len(s.sessions))
	for x := range s.sessions {
		sessions = append(sessions, x)
	}
	s.mu.Unlock()
	for _, l := range listeners {
		l.Close()
	}
	for _, x := range sessions {
		x.close()
	}
}
func (s *Server) Drain(ctx context.Context) error {
	s.BeginShutdown()
	done := make(chan struct{})
	go func() { s.workers.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Server) Shutdown(ctx context.Context) error { s.BeginShutdown(); return s.Drain(ctx) }
func (s *Server) Revoke(ctx context.Context, ref captureidentity.Reference, _ string) error {
	if ctx == nil {
		return errors.New("socksproxy: missing revocation context")
	}
	wait, err := s.BeginRevoke(ref)
	if err != nil {
		return err
	}
	return wait(ctx)
}

// BeginRevoke promptly cancels matching controls/transports. The returned
// bounded wait joins ingress workers, letting Runtime fan out cancellation to
// native MCP before waiting. UDP terminal persistence has its own Runtime drain.
func (s *Server) BeginRevoke(ref captureidentity.Reference) (func(context.Context) error, error) {
	if e := ref.Validate(); e != nil {
		return nil, e
	}
	s.mu.Lock()
	var matches []*session
	for x := range s.sessions {
		x.mu.Lock()
		if x.reference == ref {
			matches = append(matches, x)
		}
		x.mu.Unlock()
	}
	s.mu.Unlock()
	for _, x := range matches {
		x.close()
	}
	return func(ctx context.Context) error {
		if ctx == nil {
			return errors.New("socksproxy: missing revocation wait context")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, x := range matches {
			select {
			case <-x.done:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}, nil
}

type session struct {
	server     *Server
	control    net.Conn
	ctx        context.Context
	cancel     context.CancelFunc
	once       sync.Once
	mu         sync.Mutex
	credential captureadmission.ProxyCredential
	admission  captureadmission.Admission
	reference  captureidentity.Reference
	udp        *net.UDPConn
	bridge     net.Conn
	flows      map[Target]*udpFlow
	workers    sync.WaitGroup
	done       chan struct{}
}

func (x *session) close() {
	x.once.Do(func() {
		x.cancel()
		x.control.Close()
		x.mu.Lock()
		udp, bridge := x.udp, x.bridge
		var flows []*udpFlow
		for _, f := range x.flows {
			flows = append(flows, f)
		}
		x.mu.Unlock()
		if udp != nil {
			udp.Close()
		}
		if bridge != nil {
			bridge.Close()
		}
		for _, f := range flows {
			f.close()
		}
	})
}
