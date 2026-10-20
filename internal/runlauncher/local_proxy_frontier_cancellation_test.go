package runlauncher

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A client that abandons a pending request must release its Runtime connection
// and frontier slot without waiting for a Runtime response or a target change.
func TestLocalProxyFrontierClientCancellationReleasesPendingBackend(t *testing.T) {
	for _, test := range []struct {
		name, target, method string
	}{
		{"GET", "http://model.invalid/pending", http.MethodGet},
		{"CONNECT", "https://model.invalid/pending", http.MethodConnect},
	} {
		t.Run(test.name, func(t *testing.T) {
			entered := make(chan string, 4)
			canceled := make(chan struct{}, 4)
			var requests atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				if request.URL.Path == "/fresh" {
					_, _ = io.WriteString(writer, "fresh request")
					return
				}
				entered <- request.Method
				<-request.Context().Done()
				canceled <- struct{}{}
			}))
			t.Cleanup(backend.Close)
			frontier := newTestLocalProxyFrontier(t, "generation-1", backend.URL, "runtime-capability")
			client := localFrontierHTTPClient(t, frontier)
			result := make(chan error, 1)
			var cancel context.CancelFunc
			if test.method == http.MethodConnect {
				// net/http intentionally lets an in-progress proxy dial outlive
				// Do's context. Close the actual socket to test the disconnect
				// that an HTTP server can observe while CONNECT is pending.
				connection := dialLocalFrontier(t, frontier)
				cancel = func() { _ = connection.Close() }
				_, err := fmt.Fprintf(connection, "CONNECT model.invalid:443 HTTP/1.1\r\nHost: model.invalid:443\r\nProxy-Authorization: %s\r\n\r\n", frontierBasic("capture", frontier.Token()))
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				go func() {
					_, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodConnect})
					result <- err
				}()
			} else {
				var ctx context.Context
				ctx, cancel = context.WithCancel(t.Context())
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, test.target, nil)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				go func() {
					response, err := client.Do(request)
					if response != nil {
						response.Body.Close()
					}
					result <- err
				}()
			}
			defer cancel()
			select {
			case method := <-entered:
				if method != test.method {
					t.Fatalf("Runtime method = %q, want %q", method, test.method)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("pending Runtime request did not start")
			}
			frontier.mu.Lock()
			var released <-chan struct{}
			for _, connection := range frontier.connections {
				released = connection.done
			}
			count := len(frontier.connections)
			frontier.mu.Unlock()
			if count != 1 {
				t.Fatalf("pending request owns %d frontier slots, want 1", count)
			}
			cancel()
			select {
			case err := <-result:
				if err == nil || (test.method == http.MethodGet && !errors.Is(err, context.Canceled)) {
					t.Fatalf("client cancellation = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("canceled client request did not return")
			}
			select {
			case <-canceled:
			case <-time.After(2 * time.Second):
				t.Fatal("Runtime pending request outlived the canceled client")
			}
			select {
			case <-released:
			case <-time.After(2 * time.Second):
				t.Fatal("canceled request retained its frontier connection slot")
			}
			response, err := client.Get("http://model.invalid/fresh")
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil || response.StatusCode != http.StatusOK || string(body) != "fresh request" {
				t.Fatalf("fresh request after cancellation: status=%d body=%q err=%v", response.StatusCode, body, readErr)
			}
			if got := requests.Load(); got != 2 {
				t.Fatalf("canceled request was replayed: Runtime received %d requests, want 2", got)
			}
		})
	}
}

// A successful CONNECT is no longer owned by the outer HTTP request. Cancel its
// context at the exact Hijack boundary and keep exchanging opaque tunnel bytes.
func TestLocalProxyFrontierCONNECTOwnsLifetimeAfterHTTPHandoff(t *testing.T) {
	ended := make(chan struct{})
	backend := frontierTunnelBackend(t, "generation-1", frontierBasic("capture", "runtime-capability"), ended)
	frontier := newTestLocalProxyFrontier(t, "generation-1", backend.URL, "runtime-capability")
	canceled := make(chan struct{})
	ingress := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithCancel(request.Context())
		defer cancel()
		frontier.serveHTTP(cancelFrontierHTTPOnHijack{
			ResponseWriter: writer,
			cancel: func() {
				cancel()
				close(canceled)
			},
		}, request.WithContext(ctx))
	}))
	ingress.Config.ConnContext = frontier.acceptConnection
	ingress.Config.ConnState = frontier.server.ConnState
	ingress.Start()
	t.Cleanup(ingress.Close)
	client, err := net.DialTimeout("tcp4", strings.TrimPrefix(ingress.URL, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(client, "CONNECT model.invalid:443 HTTP/1.1\r\nHost: model.invalid:443\r\nProxy-Authorization: %s\r\nAuthorization: Bearer original-model-token\r\nCookie: session=original-cookie\r\n\r\n", frontierBasic("capture", frontier.Token()))
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT handoff: response=%v err=%v", response, err)
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP request did not reach the tunnel ownership boundary")
	}
	assertFrontierTunnel(t, client, reader, "generation-1")
	client.Close()
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("completed client tunnel retained its Runtime connection")
	}
}

type cancelFrontierHTTPOnHijack struct {
	http.ResponseWriter
	cancel context.CancelFunc
}

func (writer cancelFrontierHTTPOnHijack) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	connection, buffer, err := http.NewResponseController(writer.ResponseWriter).Hijack()
	if err == nil {
		writer.cancel()
	}
	return connection, buffer, err
}

// Cancellation and Runtime withdrawal may close the same socket concurrently.
// Both operations must join without leaking a handler or replaying its request.
func TestLocalProxyFrontierClientCancellationRacesShutdown(t *testing.T) {
	for _, target := range []string{"http://model.invalid/pending", "https://model.invalid/pending"} {
		for _, action := range []string{"withdraw", "close"} {
			t.Run(target+"/"+action, func(t *testing.T) {
				entered, canceled := make(chan struct{}), make(chan struct{})
				var requests atomic.Int32
				backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					requests.Add(1)
					close(entered)
					<-request.Context().Done()
					close(canceled)
				}))
				t.Cleanup(backend.Close)
				frontier := newTestLocalProxyFrontier(t, "generation-1", backend.URL, "runtime-capability")
				client := localFrontierHTTPClient(t, frontier)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
				if err != nil {
					t.Fatal(err)
				}
				returned := make(chan struct{})
				go func() {
					response, _ := client.Do(request)
					if response != nil {
						response.Body.Close()
					}
					close(returned)
				}()
				select {
				case <-entered:
				case <-time.After(2 * time.Second):
					t.Fatal("pending Runtime request did not start")
				}
				start, stopped := make(chan struct{}), make(chan struct{})
				go func() {
					<-start
					if action == "withdraw" {
						frontier.Withdraw()
					} else {
						frontier.Close()
					}
					close(stopped)
				}()
				close(start)
				cancel()
				for _, signal := range []struct {
					name string
					done <-chan struct{}
				}{
					{"Runtime request cancellation", canceled},
					{"client return", returned},
					{"frontier shutdown", stopped},
				} {
					select {
					case <-signal.done:
					case <-time.After(2 * time.Second):
						t.Fatalf("%s did not join", signal.name)
					}
				}
				if got := requests.Load(); got != 1 {
					t.Fatalf("canceled request was replayed %d times", got-1)
				}
			})
		}
	}
}
