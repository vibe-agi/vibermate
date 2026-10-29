package flowingress

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/capturescope"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/loopbackclient"
)

const (
	headerTimeout   = 5 * time.Second
	connectTimeout  = 30 * time.Second
	maxResponseHead = 16 << 10
)

// Proxy is the run's existing child-facing proxy frontier: a literal loopback
// HTTP proxy origin and the run's proxy credential.
type Proxy struct {
	Origin string
	Token  string
}

// Ingress forwards flows for one Capture Run. Its proxy is the only
// authority a flow can reach; flows never choose a run.
type Ingress struct {
	proxy func() (Proxy, error)
	// scope keeps the Environment's client-only destinations off ViberMate
	// (ADR 0025). The zero Scope keeps only the built-in set off.
	scope capturescope.Scope
}

// New returns an ingress that reads the run's current proxy for every flow,
// so a recovered frontier is used without restarting capture.
func New(proxy func() (Proxy, error)) (*Ingress, error) {
	if proxy == nil {
		return nil, errors.New("flow ingress proxy is required")
	}
	return &Ingress{proxy: proxy}, nil
}

// UseScope sets the run's compiled capture scope. Call it before serving.
func (ingress *Ingress) UseScope(scope capturescope.Scope) { ingress.scope = scope }

// Scope is the run's compiled capture scope.
func (ingress *Ingress) Scope() capturescope.Scope { return ingress.scope }

// Serve owns conn: it reads the flow header, names the destination, opens a
// CONNECT through the run's proxy and copies bytes until both sides finish.
// A UDP flow instead carries datagram frames, each relayed as connect-udp.
func (ingress *Ingress) Serve(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(headerTimeout))
	reader := bufio.NewReaderSize(conn, sniffBytes)
	header, err := ReadHeader(reader)
	if err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Time{})
	if header.Protocol == "udp" {
		return ingress.serveDatagrams(ctx, conn, reader)
	}
	return ingress.serve(ctx, conn, reader, header)
}

// ServeFlow is Serve for a caller that already knows the flow's original
// destination, such as a netstack, so no header precedes the client's bytes.
// It carries TCP only; such a caller relays its own UDP.
func (ingress *Ingress) ServeFlow(ctx context.Context, conn net.Conn, header Header) error {
	defer conn.Close()
	if err := header.validate(); err != nil || header.Protocol != "tcp" {
		return errors.Join(errInvalidHeader, err)
	}
	return ingress.serve(ctx, conn, bufio.NewReaderSize(conn, sniffBytes), header)
}

func (ingress *Ingress) serve(ctx context.Context, conn net.Conn, reader *bufio.Reader, header Header) error {
	name := destinationName(header, conn, reader)
	var upstream net.Conn
	var err error
	if _, bypass := ingress.scope.Match(capturescope.Destination{
		Name: name, Addr: header.Destination.Addr(), Port: header.Destination.Port(),
	}); bypass {
		var target string
		if target, err = directTarget(name, header.Destination); err == nil {
			upstream, err = dialDirectTCP(ctx, target)
		}
	} else {
		upstream, err = ingress.connect(ctx, targetOf(name, header.Destination))
	}
	if err != nil {
		return err
	}
	defer upstream.Close()
	return splice(conn, reader, upstream)
}

// Proxy returns the run's current proxy, for other transports (such as
// connect-udp) that must reach the Runtime the same way.
func (ingress *Ingress) Proxy() (Proxy, error) { return ingress.proxy() }

// Destination names the CONNECT target: a platform-reported host name, else
// the sniffed TLS SNI or HTTP Host, else the IP address.
func Destination(header Header, conn net.Conn, reader *bufio.Reader) string {
	return targetOf(destinationName(header, conn, reader), header.Destination)
}

// destinationName is the platform-reported host name, else the sniffed TLS
// SNI or HTTP Host, else empty.
func destinationName(header Header, conn net.Conn, reader *bufio.Reader) string {
	if header.Host != "" {
		return header.Host
	}
	return sniffHost(conn, reader)
}

func targetOf(name string, destination netip.AddrPort) string {
	port := strconv.Itoa(int(destination.Port()))
	if name != "" {
		return net.JoinHostPort(name, port)
	}
	return net.JoinHostPort(destination.Addr().Unmap().String(), port)
}

func (ingress *Ingress) connect(ctx context.Context, target string) (net.Conn, error) {
	proxy, err := ingress.proxy()
	if err != nil {
		return nil, err
	}
	client, err := loopbackclient.New(proxy.Origin, connectTimeout)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	upstream, err := client.Dial(ctx)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = upstream.Close() })
	defer stop()
	credential := base64.StdEncoding.EncodeToString([]byte("capture:" + proxy.Token))
	request := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target +
		"\r\nProxy-Authorization: Basic " + credential + "\r\n\r\n"
	if _, err := io.WriteString(upstream, request); err != nil {
		upstream.Close()
		return nil, err
	}
	head, err := readResponseHead(upstream)
	if err != nil {
		upstream.Close()
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(head)), &http.Request{Method: http.MethodConnect})
	if err != nil {
		upstream.Close()
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		upstream.Close()
		return nil, fmt.Errorf("proxy refused %s: %s", target, response.Status)
	}
	if !stop() {
		upstream.Close()
		return nil, ctx.Err()
	}
	return upstream, nil
}

// readResponseHead reads the CONNECT response through its blank line one
// byte at a time, so no tunneled byte after the header is consumed.
func readResponseHead(conn net.Conn) ([]byte, error) {
	head := make([]byte, 0, 256)
	var one [1]byte
	for len(head) < maxResponseHead {
		if _, err := io.ReadFull(conn, one[:]); err != nil {
			return nil, err
		}
		head = append(head, one[0])
		if bytes.HasSuffix(head, []byte("\r\n\r\n")) {
			return head, nil
		}
	}
	return nil, errors.New("proxy response header is too large")
}

func splice(client net.Conn, clientReader *bufio.Reader, upstream net.Conn) error {
	var wait sync.WaitGroup
	var upErr, downErr error
	wait.Add(2)
	go func() {
		defer wait.Done()
		_, upErr = io.Copy(upstream, clientReader)
		closeWrite(upstream)
	}()
	go func() {
		defer wait.Done()
		_, downErr = io.Copy(client, upstream)
		closeWrite(client)
	}()
	wait.Wait()
	return errors.Join(ignoreClosed(upErr), ignoreClosed(downErr))
}

func closeWrite(conn net.Conn) {
	if half, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = half.CloseWrite()
		return
	}
	_ = conn.Close()
}

func ignoreClosed(err error) error {
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
