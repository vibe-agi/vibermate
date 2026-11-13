package loopbackproxy_test

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureauthority"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/loopbackproxy"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"golang.org/x/net/http2"
)

type heldRetainedExchange struct{ entered, canceled, release, done chan struct{} }

func (e *heldRetainedExchange) Execute(ctx context.Context, _ exchange.ClientRequest, _ exchange.Downstream) (exchange.Result, error) {
	close(e.entered)
	<-ctx.Done()
	close(e.canceled)
	<-e.release
	defer close(e.done)
	return exchange.Result{}, ctx.Err()
}
func TestRetainedTransportShutdownJoinsInnerHandlers(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "h1", true: "h2"}[h2], func(t *testing.T) {
			f := newProxyFixture(t)
			defer f.Close(t)
			held := &heldRetainedExchange{entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(held.release) }) }
			defer release()
			f.exchanges.mu.Lock()
			f.exchanges.delegate = held
			f.exchanges.mu.Unlock()
			raw, response := f.Connect(t, f.grant.ProxyCapability.Value(), "api.anthropic.com:443")
			if response.StatusCode != 200 {
				t.Fatal(response.StatusCode)
			}
			defer raw.Close()
			protos := []string{"http/1.1"}
			if h2 {
				protos = []string{"h2"}
			}
			secure := tls.Client(raw, &tls.Config{ServerName: "api.anthropic.com", InsecureSkipVerify: true, NextProtos: protos})
			if err := secure.Handshake(); err != nil {
				t.Fatal(err)
			}
			request, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", strings.NewReader(`{"model":"client"}`))
			request.Header.Set("Content-Type", "application/json")
			clientDone := make(chan struct{})
			go func() {
				defer close(clientDone)
				if h2 {
					transport := &http2.Transport{}
					client, err := transport.NewClientConn(secure)
					if err == nil {
						response, _ := client.RoundTrip(request)
						if response != nil {
							io.Copy(io.Discard, response.Body)
							response.Body.Close()
						}
						client.Close()
					}
				} else {
					_ = request.Write(secure)
					var b [1]byte
					_, _ = secure.Read(b[:])
				}
			}()
			select {
			case <-held.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("inner exchange did not start")
			}
			f.handler.BeginShutdown()
			select {
			case <-held.canceled:
			case <-time.After(time.Second):
				t.Fatal("inner exchange did not receive cancellation")
			}
			drained := make(chan struct{})
			go func() { _ = f.handler.Drain(t.Context()); close(drained) }()
			select {
			case <-drained:
				t.Error("outer operation drained before inner handler released")
			case <-time.After(30 * time.Millisecond):
			}
			release()
			select {
			case <-held.done:
			case <-time.After(time.Second):
				t.Fatal("inner handler did not release")
			}
			select {
			case <-drained:
			case <-time.After(time.Second):
				t.Fatal("outer operation did not join inner handler")
			}
			select {
			case <-clientDone:
			case <-time.After(time.Second):
				t.Fatal("client did not terminate")
			}
		})
	}
}

type faultedCurrentAuthority struct {
	captureadmission.CurrentAuthority
	fail           atomic.Bool
	entered        chan struct{}
	cancelLookup   bool
	storageFailure bool
}

func (a *faultedCurrentAuthority) Revalidate(ctx context.Context, admission captureadmission.Admission) (captureauthority.Validity, error) {
	if !a.fail.Load() {
		return a.CurrentAuthority.Revalidate(ctx, admission)
	}
	if a.cancelLookup {
		close(a.entered)
		<-ctx.Done()
		return captureauthority.Validity{}, ctx.Err()
	}
	if a.storageFailure {
		return captureauthority.Validity{}, errors.Join(captureauthority.ErrUnavailable, errors.New("synthetic storage failure"))
	}
	return captureauthority.Validity{}, errors.New("synthetic authority read unavailable")
}

// A storage failure while re-reading the capture is not a rejected credential.
// The request is still refused, but as a retryable error, and the same
// conversation continues once storage answers again.
func TestRetainedOperationStorageFailureIsRetryableAndRecovers(t *testing.T) {
	var authority *faultedCurrentAuthority
	f := newProxyFixtureConfigured(t, protocolspec.DialectAnthropicMessages, nil, allowEverythingTestPolicy(t), nil, "", &proxyFixtureComposition{compiler: testEnvironmentCompiler(t, protocolspec.DialectAnthropicMessages), configureProxy: func(options *loopbackproxy.Options) {
		authority = &faultedCurrentAuthority{CurrentAuthority: options.Admissions.(captureadmission.CurrentAuthority), entered: make(chan struct{}), storageFailure: true}
		options.Admissions = authority
	}})
	defer f.Close(t)
	conn := f.ConnectTLS(t, f.grant.ProxyCapability.Value(), "api.anthropic.com:443", "api.anthropic.com")
	defer conn.Close()
	send := func() *http.Response {
		r, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", strings.NewReader(`{"model":"client"}`))
		r.URL.Scheme = ""
		r.URL.Host = ""
		r.Header.Set("Content-Type", "application/json")
		return writeInnerRequest(t, conn, r)
	}
	authority.fail.Store(true)
	response := send()
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || response.Header.Get("Retry-After") != "1" ||
		!strings.Contains(string(body), `"capture_authority_unavailable"`) {
		t.Fatalf("storage failure answered %d retry-after=%q body=%s", response.StatusCode, response.Header.Get("Retry-After"), body)
	}
	if len(f.exchanges.Requests()) != 0 || f.original.Count() != 0 {
		t.Fatal("a request was forwarded while the capture could not be re-read")
	}
	authority.fail.Store(false)
	response = send()
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(f.exchanges.Requests()) != 1 {
		t.Fatalf("the conversation did not continue after storage recovered: %d, %d forwarded", response.StatusCode, len(f.exchanges.Requests()))
	}
}
func TestRetainedOperationAuthorityFailureDeniesBeforeDispatch(t *testing.T) {
	for _, cancelLookup := range []bool{false, true} {
		t.Run(map[bool]string{false: "lookup_error", true: "canceled_lookup"}[cancelLookup], func(t *testing.T) {
			var authority *faultedCurrentAuthority
			f := newProxyFixtureConfigured(t, protocolspec.DialectAnthropicMessages, nil, allowEverythingTestPolicy(t), nil, "", &proxyFixtureComposition{compiler: testEnvironmentCompiler(t, protocolspec.DialectAnthropicMessages), configureProxy: func(options *loopbackproxy.Options) {
				authority = &faultedCurrentAuthority{CurrentAuthority: options.Admissions.(captureadmission.CurrentAuthority), entered: make(chan struct{}), cancelLookup: cancelLookup}
				options.Admissions = authority
			}})
			defer f.Close(t)
			conn := f.ConnectTLS(t, f.grant.ProxyCapability.Value(), "api.anthropic.com:443", "api.anthropic.com")
			defer conn.Close()
			request := func() *http.Request {
				r, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", strings.NewReader(`{"model":"client"}`))
				r.URL.Scheme = ""
				r.URL.Host = ""
				r.Header.Set("Content-Type", "application/json")
				return r
			}
			response := writeInnerRequest(t, conn, request())
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatal(response.StatusCode)
			}
			authority.fail.Store(true)
			if cancelLookup {
				if err := request().Write(conn); err != nil {
					t.Fatal(err)
				}
				select {
				case <-authority.entered:
				case <-time.After(time.Second):
					t.Fatal("lookup not entered")
				}
				f.handler.BeginShutdown()
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				if err := f.handler.Drain(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				response = writeInnerRequest(t, conn, request())
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if response.StatusCode != 403 {
					t.Fatal("failed authority read did not deny", response.StatusCode)
				}
			}
			if len(f.exchanges.Requests()) != 1 || f.original.Count() != 0 {
				t.Fatal("authority failure reached downstream admission")
			}
		})
	}
}
