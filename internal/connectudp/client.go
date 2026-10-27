package connectudp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/vibe-agi/vibermate/internal/loopbackclient"
)

const dialTimeout = 30 * time.Second

// Dial opens a connect-udp association to host:port through a run's loopback
// proxy (its frontier), authenticated with the run's proxy credential.
func Dial(ctx context.Context, proxyOrigin, token, host string, port uint16) (*Conn, error) {
	client, err := loopbackclient.New(proxyOrigin, dialTimeout)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	conn, err := client.Dial(ctx)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	credential := base64.StdEncoding.EncodeToString([]byte("capture:" + token))
	request := "GET http://connect-udp.invalid" + Path(host, port) + " HTTP/1.1\r\n" +
		"Host: connect-udp.invalid\r\n" +
		"Connection: Upgrade\r\nUpgrade: " + UpgradeToken + "\r\n" +
		"Capsule-Protocol: ?1\r\n" +
		"Proxy-Authorization: Basic " + credential + "\r\n\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		conn.Close()
		return nil, err
	}
	head, err := readHead(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(head)), &http.Request{Method: http.MethodGet})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if response.StatusCode != http.StatusSwitchingProtocols ||
		!HeaderHasToken(response.Header.Values("Upgrade"), UpgradeToken) {
		conn.Close()
		return nil, fmt.Errorf("proxy refused UDP to %s: %s", net.JoinHostPort(host, fmt.Sprint(port)), response.Status)
	}
	if !stop() {
		conn.Close()
		return nil, ctx.Err()
	}
	return NewConn(conn, bufio.NewReader(conn)), nil
}

// readHead reads the response through its blank line one byte at a time, so
// no capsule byte after the header is consumed.
func readHead(conn net.Conn) ([]byte, error) {
	head := make([]byte, 0, 256)
	var one [1]byte
	for len(head) < 16<<10 {
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
