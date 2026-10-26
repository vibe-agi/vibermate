package loopbackproxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/vibe-agi/vibermate/internal/connectudp"
	"github.com/vibe-agi/vibermate/internal/socksproxy"
)

// UDPOpener opens one UDP association for an admitted capture. The Runtime's
// implementation applies connection policy, approvals, the egress profile
// (an upstream SOCKS5 proxy included) and audit, as for SOCKS5 UDP.
type UDPOpener = socksproxy.UDPOpener

const (
	ReasonUDPUnavailable ReasonCode = "udp_unavailable"
	ReasonUDPRefused     ReasonCode = "udp_refused"
)

// isConnectUDP recognizes an RFC 9298 connect-udp request over HTTP/1.1.
func isConnectUDP(request *http.Request) bool {
	return request.Method == http.MethodGet &&
		connectudp.HeaderHasToken(request.Header.Values("Upgrade"), connectudp.UpgradeToken) &&
		connectudp.HeaderHasToken(request.Header.Values("Connection"), "upgrade")
}

// serveConnectUDP carries one captured UDP flow through the Runtime's UDP
// path, so it leaves through the same policy and egress as SOCKS5 UDP.
func (handler *Handler) serveConnectUDP(writer http.ResponseWriter, request *http.Request) {
	if handler.udp == nil {
		writeReason(writer, http.StatusNotImplemented, ReasonUDPUnavailable, "")
		return
	}
	host, port, err := connectudp.ParsePath(request.URL.EscapedPath())
	if err != nil {
		writeReason(writer, http.StatusBadRequest, ReasonConnectAuthorityInvalid, "")
		return
	}
	rawCapability, err := consumeProxyAuthorization(request.Header)
	if err != nil {
		writeReason(writer, http.StatusProxyAuthRequired, ReasonProxyAuthenticationRequired, "")
		return
	}
	ctx := request.Context()
	admission, err := authorizeCaptureAdmission(ctx, handler.admissions, rawCapability)
	if err != nil {
		writeReason(writer, http.StatusProxyAuthRequired, ReasonCaptureAdmissionRejected, "")
		return
	}
	opened, err := handler.udp.OpenUDP(ctx, admission, socksproxy.Target{Host: host, Port: port})
	if err != nil {
		writeReason(writer, http.StatusForbidden, ReasonUDPRefused, "")
		return
	}
	defer opened.Flow.Close()
	conn, buffered, err := http.NewResponseController(writer).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	if _, err := io.WriteString(buffered, "HTTP/1.1 101 Switching Protocols\r\n"+
		"Connection: Upgrade\r\nUpgrade: "+connectudp.UpgradeToken+"\r\n"+
		"Capsule-Protocol: ?1\r\n\r\n"); err != nil {
		return
	}
	if err := buffered.Flush(); err != nil {
		return
	}
	relayDatagrams(ctx, conn, buffered.Reader, opened.Flow)
}

// relayDatagrams copies capsules to datagrams and back until either side
// ends or ctx is canceled (proxy shutdown included).
func relayDatagrams(ctx context.Context, stream io.ReadWriteCloser, reader *bufio.Reader, flow socksproxy.DatagramFlow) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = stream.Close(); _ = flow.Close() })
	defer stop()
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		defer cancel()
		for {
			payload, err := connectudp.ReadDatagram(reader)
			if err != nil {
				return
			}
			if err := flow.WritePacket(ctx, payload); err != nil && ctx.Err() != nil {
				return
			}
		}
	}()
	go func() {
		defer wait.Done()
		defer cancel()
		buffer := make([]byte, connectudp.MaxDatagram)
		for {
			n, err := flow.ReadPacket(ctx, buffer)
			if err != nil {
				return
			}
			if err := connectudp.WriteDatagram(stream, buffer[:n]); err != nil && !errors.Is(err, io.ErrShortWrite) {
				return
			}
		}
	}()
	wait.Wait()
}
