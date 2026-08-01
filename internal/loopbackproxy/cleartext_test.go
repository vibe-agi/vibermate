package loopbackproxy_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
)

func cleartextOrigin(t *testing.T) (string, func()) {
	t.Helper()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{
		Handler: http.HandlerFunc(func(
			writer http.ResponseWriter,
			request *http.Request,
		) {
			// A forwarded request must arrive in origin form with no proxy
			// credential attached.
			if request.URL.IsAbs() ||
				request.Header.Get("Proxy-Authorization") != "" {
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			writer.Header().Set("Content-Type", "text/plain")
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte("cleartext-origin"))
		}),
	}
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String(), func() {
		_ = server.Close()
	}
}

// The launcher exports the proxy to the whole child process tree, so an Agent
// reaching an http:// host arrives here. Answering 405 makes that host simply
// unreachable.
func TestCleartextRequestIsForwardedToItsOrigin(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixture(t)
	defer fixture.Close(t)
	authority, stop := cleartextOrigin(t)
	defer stop()

	connection, err := net.DialTimeout(
		"tcp4",
		fixture.listener.Addr().String(),
		2*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	credentials := base64.StdEncoding.EncodeToString(
		[]byte("capture:" + fixture.grant.ProxyCapability.Value()),
	)
	if _, err := io.WriteString(
		connection,
		"GET http://"+authority+"/status HTTP/1.1\r\n"+
			"Host: "+authority+"\r\n"+
			"Proxy-Authorization: Basic "+credentials+"\r\n"+
			"Connection: close\r\n\r\n",
	); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(
		bufio.NewReader(connection),
		&http.Request{Method: http.MethodGet},
	)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK ||
		string(body) != "cleartext-origin" {
		t.Fatalf(
			"cleartext forward status=%d body=%q",
			response.StatusCode,
			body,
		)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		page, listErr := fixture.egress.List(
			context.Background(),
			egressaudit.PageRequest{Limit: 20},
		)
		if listErr != nil {
			t.Fatal(listErr)
		}
		for _, record := range page.Items {
			if record.Attempt.Purpose() != egressaudit.PurposeBlindTunnel {
				continue
			}
			if !record.Attempt.Terminal() {
				continue
			}
			connections, connErr := fixture.connections.List(
				context.Background(),
				connectionevent.PageRequest{Limit: 20},
			)
			if connErr != nil {
				t.Fatal(connErr)
			}
			for _, event := range connections.Items {
				if event.Decision == connectionevent.DecisionAllow &&
					event.Decryption != connectionevent.DecryptionBlind {
					t.Fatalf(
						"a cleartext forward claimed a decryption mode: %+v",
						event,
					)
				}
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("a cleartext forward left no per-egress record")
}

// Cleartext forwarding never enters the model pipeline and never carries a
// provider credential.
func TestCleartextRequestNeverReachesTheModelPipeline(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixture(t)
	defer fixture.Close(t)
	authority, stop := cleartextOrigin(t)
	defer stop()

	connection, err := net.DialTimeout(
		"tcp4",
		fixture.listener.Addr().String(),
		2*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	credentials := base64.StdEncoding.EncodeToString(
		[]byte("capture:" + fixture.grant.ProxyCapability.Value()),
	)
	if _, err := io.WriteString(
		connection,
		"POST http://"+authority+"/v1/messages HTTP/1.1\r\n"+
			"Host: "+authority+"\r\n"+
			"Proxy-Authorization: Basic "+credentials+"\r\n"+
			"Content-Length: 2\r\n"+
			"Connection: close\r\n\r\n{}",
	); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(
		bufio.NewReader(connection),
		&http.Request{Method: http.MethodPost},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()

	if len(fixture.exchanges.Requests()) != 0 {
		t.Fatal("a cleartext request created an Exchange")
	}
	if fixture.original.Count() != 0 {
		t.Fatal("a cleartext request reached the original-origin transport")
	}
}
