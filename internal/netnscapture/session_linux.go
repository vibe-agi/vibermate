//go:build linux

package netnscapture

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/fdbased"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"

	"github.com/vibe-agi/vibermate/internal/connectudp"
	"github.com/vibe-agi/vibermate/internal/flowingress"
)

const (
	handoffTimeout = 10 * time.Second
	udpIdleTimeout = 2 * time.Minute
	maxTCPInFlight = 1024
)

// Session is one run's namespace capture: the Agent's netstack, owned by the
// launcher. When the launcher exits, the TUN device closes and the namespace
// has no route out.
type Session struct {
	control *os.File
	helper  *os.File
	tun     int
	ingress *flowingress.Ingress
	dns     *fakeDNS

	mu     sync.Mutex
	stack  *stack.Stack
	ctx    context.Context
	cancel context.CancelFunc
}

// Prepare rewrites command to start through the namespace helper. command
// must name the Agent's resolved path and argv and carry its environment;
// its SysProcAttr may already set process-group and terminal options.
func Prepare(command *exec.Cmd, ingress *flowingress.Ingress) (*Session, error) {
	if command == nil || ingress == nil || command.Path == "" || len(command.Args) == 0 {
		return nil, errors.New("namespace capture needs the Agent command and the run ingress")
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(pair[0]), "vibermate-namespace-launcher")
	child := os.NewFile(uintptr(pair[1]), "vibermate-namespace-helper")
	command.Args = append([]string{self, ChildCommand, command.Path}, command.Args...)
	command.Path = self
	command.ExtraFiles = append([]*os.File{child}, command.ExtraFiles...)
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	uid, gid := os.Getuid(), os.Getgid()
	attributes := command.SysProcAttr
	attributes.Cloneflags |= syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET | syscall.CLONE_NEWNS
	attributes.UidMappings = []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}}
	attributes.GidMappings = []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}}
	attributes.GidMappingsEnableSetgroups = false
	attributes.AmbientCaps = []uintptr{unix.CAP_NET_ADMIN, unix.CAP_SYS_ADMIN}
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{control: parent, helper: child, tun: -1, ingress: ingress, dns: newFakeDNS(),
		ctx: ctx, cancel: cancel}, nil
}

// Start completes the hand-off after command.Start: it receives the TUN
// device, starts the netstack and lets the helper execute the Agent.
func (session *Session) Start() error {
	// The child now holds the helper's end; this process must not.
	_ = session.helper.Close()
	_ = session.control.SetReadDeadline(time.Now().Add(handoffTimeout))
	fd, err := receiveTUN(session.control)
	if err != nil {
		return fmt.Errorf("receive the capture TUN device: %w", err)
	}
	session.tun = fd
	if err := session.startStack(fd); err != nil {
		return err
	}
	_ = session.control.SetWriteDeadline(time.Now().Add(handoffTimeout))
	if _, err := session.control.Write([]byte{'G'}); err != nil {
		return fmt.Errorf("start the Agent in its namespace: %w", err)
	}
	return nil
}

func receiveTUN(control *os.File) (int, error) {
	raw, err := control.SyscallConn()
	if err != nil {
		return -1, err
	}
	data := make([]byte, 1)
	oob := make([]byte, unix.CmsgSpace(4))
	var n, oobn int
	var receiveErr error
	readErr := raw.Read(func(fd uintptr) bool {
		n, oobn, _, _, receiveErr = unix.Recvmsg(int(fd), data, oob, 0)
		return !errors.Is(receiveErr, unix.EAGAIN)
	})
	if readErr != nil {
		return -1, readErr
	}
	if receiveErr != nil {
		return -1, receiveErr
	}
	if n != 1 || data[0] != 'T' {
		return -1, errors.New("the namespace helper failed before handing over its TUN device")
	}
	messages, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(messages) != 1 {
		return -1, errors.New("the namespace helper sent no TUN device")
	}
	fds, err := unix.ParseUnixRights(&messages[0])
	if err != nil || len(fds) != 1 {
		return -1, errors.New("the namespace helper sent an unexpected descriptor")
	}
	return fds[0], nil
}

func (session *Session) startStack(fd int) error {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	endpoint, err := fdbased.New(&fdbased.Options{FDs: []int{fd}, MTU: tunMTU})
	if err != nil {
		s.Destroy()
		return fmt.Errorf("attach the netstack to the TUN device: %w", err)
	}
	const nic = 1
	// Handlers and routes go in first and the interface is enabled last:
	// once attached, the link delivers packets at once.
	tcpForwarder := tcp.NewForwarder(s, 0, maxTCPInFlight, session.acceptTCP)
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpForwarder.HandlePacket)
	udpForwarder := udp.NewForwarder(s, session.acceptUDP)
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpForwarder.HandlePacket)
	if err := s.CreateNICWithOptions(nic, endpoint, stack.NICOptions{Disabled: true}); err != nil {
		s.Destroy()
		return fmt.Errorf("create the netstack interface: %s", err)
	}
	// Accept and answer for every destination: the Agent's whole network.
	s.SetPromiscuousMode(nic, true)
	s.SetSpoofing(nic, true)
	s.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nic},
		{Destination: header.IPv6EmptySubnet, NIC: nic},
	})
	if err := s.EnableNIC(nic); err != nil {
		s.Destroy()
		return fmt.Errorf("enable the netstack interface: %s", err)
	}
	session.mu.Lock()
	session.stack = s
	session.mu.Unlock()
	return nil
}

func addressOf(address tcpip.Address) netip.Addr {
	value, _ := netip.AddrFromSlice(address.AsSlice())
	return value.Unmap()
}

// acceptTCP hands each Agent connection to the run's ingress. A fake address
// carries its name, so the Runtime resolves it through its own DoH.
func (session *Session) acceptTCP(request *tcp.ForwarderRequest) {
	id := request.ID()
	var queue waiter.Queue
	endpoint, err := request.CreateEndpoint(&queue)
	if err != nil {
		request.Complete(true)
		return
	}
	request.Complete(false)
	conn := gonet.NewTCPConn(&queue, endpoint)
	destination := netip.AddrPortFrom(addressOf(id.LocalAddress), id.LocalPort)
	flow := flowingress.Header{Version: 1, Protocol: "tcp", Destination: destination}
	if name, ok := session.dns.Lookup(destination.Addr()); ok {
		flow.Host = name
	}
	go func() { _ = session.ingress.ServeFlow(session.ctx, conn, flow) }()
}

// acceptUDP answers DNS itself and sends every other datagram flow through
// vibermated as connect-udp, so it leaves through the Runtime's policy and
// egress (an upstream SOCKS5 proxy included).
func (session *Session) acceptUDP(request *udp.ForwarderRequest) bool {
	id := request.ID()
	var queue waiter.Queue
	endpoint, err := request.CreateEndpoint(&queue)
	if err != nil {
		return false
	}
	conn := gonet.NewUDPConn(&queue, endpoint)
	destination := netip.AddrPortFrom(addressOf(id.LocalAddress), id.LocalPort)
	if destination.Port() == 53 {
		go session.serveDNS(conn)
		return true
	}
	go session.relayUDP(conn, destination)
	return true
}

func (session *Session) serveDNS(conn *gonet.UDPConn) {
	defer conn.Close()
	buffer := make([]byte, 4096)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(udpIdleTimeout))
		n, err := conn.Read(buffer)
		if err != nil {
			return
		}
		if reply := session.dns.Answer(buffer[:n]); reply != nil {
			_, _ = conn.Write(reply)
		}
	}
}

func (session *Session) relayUDP(conn *gonet.UDPConn, destination netip.AddrPort) {
	defer conn.Close()
	host := destination.Addr().String()
	if name, ok := session.dns.Lookup(destination.Addr()); ok {
		host = name
	} else if fakePrefix.Contains(destination.Addr()) {
		return // A forgotten fake address names nothing; drop rather than guess.
	}
	proxy, err := session.ingress.Proxy()
	if err != nil {
		return
	}
	upstream, err := connectudp.Dial(session.ctx, proxy.Origin, proxy.Token, host, destination.Port())
	if err != nil {
		return
	}
	defer upstream.Close()
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		buffer := make([]byte, connectudp.MaxDatagram)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(udpIdleTimeout))
			n, err := conn.Read(buffer)
			if err != nil {
				return
			}
			if upstream.WriteDatagram(buffer[:n]) != nil {
				return
			}
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			payload, err := upstream.ReadDatagram()
			if err != nil {
				return
			}
			if _, err := conn.Write(payload); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-session.ctx.Done():
	}
}

// Close stops the netstack; the namespace then has no route out.
func (session *Session) Close() {
	session.cancel()
	session.mu.Lock()
	s := session.stack
	session.stack = nil
	session.mu.Unlock()
	// Destroy removes the interface, which stops and joins the packet
	// dispatchers; only then may the TUN descriptor be closed, or a dispatcher
	// would poll a closed (or reused) descriptor.
	if s != nil {
		s.Destroy()
	}
	if session.tun >= 0 {
		_ = unix.Close(session.tun)
		session.tun = -1
	}
	_ = session.helper.Close()
	_ = session.control.Close()
}
