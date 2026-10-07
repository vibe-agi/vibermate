package loopbackproxy_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/loopbackproxy"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

func TestBodyAdmissionHTTPFourActiveFourUnreadWaiters(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("http2=%v", h2), func(t *testing.T) {
			limits := exchangecontent.SourceLimits{Semantic: protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 32 << 20, StructureBytes: 32 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 32 << 20, StructureBytes: 32 << 20}}, Scratch: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 32 << 20}, CanonicalBytes: 256 << 20, RetainedBytes: 64 << 20, StructureBytes: 32 << 20}
			gate, err := exchange.NewBodyAdmission(bodyHTTPPolicy(limits, 4))
			if err != nil {
				t.Fatal(err)
			}
			entered, executing := make(chan struct{}, 8), make(chan struct{}, 8)
			release := make(chan struct{})
			released := false
			defer func() {
				if !released {
					close(release)
				}
			}()
			raw := &admissionRawCounter{}
			fixture := newProxyFixtureForDialectWithPolicyAndRawEvidence(t, protocolspec.DialectAnthropicMessages, nil, allowEverythingTestPolicy(t), raw, "", func(o *loopbackproxy.Options) {
				o.BodyAdmission = gate
				o.Assignments = admissionAssignments{o.Assignments, entered}
				o.Exchanges = admissionExecutor{o.Exchanges, executing, release}
			})
			defer fixture.Close(t)
			if _, err := fixture.runs.Attach(context.Background(), fixture.grant.Run.ID, fixture.grant.ControlCapability, 321); err != nil {
				t.Fatal(err)
			}
			roots := x509.NewCertPool()
			roots.AppendCertsFromPEM(fixture.authority.Certificate().CertificatePEM())
			proxyURL, _ := url.Parse("http://" + fixture.listener.Addr().String())
			proxyURL.User = url.UserPassword("capture", fixture.grant.ProxyCapability.Value())
			// Expect:100-continue makes the server's first body Read observable.
			// No timer is used to release the barrier; the long client fallback is
			// never waited out and is independent of all product deadlines.
			transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, ForceAttemptHTTP2: h2, ExpectContinueTimeout: time.Hour}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			bodies := make([]*admissionBody, 8)
			results := make(chan error, 8)
			send := func(i int) {
				bodies[i] = &admissionBody{reader: strings.NewReader(`{"model":"client"}`)}
				go func() {
					req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bodies[i])
					req.ContentLength = int64(len(`{"model":"client"}`))
					req.Header.Set("Expect", "100-continue")
					req.Header.Set("Content-Type", "application/json")
					resp, err := client.Do(req)
					if err == nil {
						_, err = io.Copy(io.Discard, resp.Body)
						resp.Body.Close()
						if resp.StatusCode != 200 {
							err = fmt.Errorf("status=%d", resp.StatusCode)
						}
						if h2 && resp.ProtoMajor != 2 {
							err = fmt.Errorf("HTTP/2 not negotiated")
						}
					}
					results <- err
				}()
			}
			wait := func(ch <-chan struct{}) {
				t.Helper()
				select {
				case <-ch:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			for i := 0; i < 4; i++ {
				send(i)
			}
			for range 4 {
				wait(entered)
				wait(executing)
			}
			for i := 4; i < 8; i++ {
				send(i)
			}
			for range 4 {
				wait(entered)
			}
			for i := 4; i < 8; i++ {
				if bodies[i].reads.Load() != 0 {
					t.Fatalf("waiting body %d read", i)
				}
			}
			for i := 0; i < 4; i++ {
				if bodies[i].reads.Load() == 0 {
					t.Fatalf("active body %d read probe did not observe demand", i)
				}
			}
			if raw.scopes.Load() != 4 {
				t.Fatalf("queued request acquired Raw scope: %d", raw.scopes.Load())
			}
			if len(fixture.exchanges.Requests()) != 0 {
				t.Fatal("held executor released before barrier")
			}
			probe, _ := http.NewRequestWithContext(ctx, "GET", "https://api.anthropic.com/api/claude_code/settings", nil)
			resp, err := client.Do(probe)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("auxiliary blocked/status=%d", resp.StatusCode)
			}
			if _, err := fixture.runs.Heartbeat(ctx, fixture.grant.Run.ID, fixture.grant.ControlCapability, 0); err != nil {
				t.Fatalf("real heartbeat blocked: %v", err)
			}
			close(release)
			released = true
			for range 8 {
				select {
				case err := <-results:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if len(fixture.exchanges.Requests()) != 8 {
				t.Fatal("uncanceled requests did not all complete")
			}
			gate.BeginShutdown()
			if err := gate.Drain(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type admissionBody struct {
	reader *strings.Reader
	reads  atomic.Int32
}

func (b *admissionBody) Read(p []byte) (int, error) { b.reads.Add(1); return b.reader.Read(p) }

type admissionAssignments struct {
	loopbackproxy.CaptureAssignmentAuthority
	entered chan<- struct{}
}

func (a admissionAssignments) BeginRequest(ctx context.Context, c captureidentity.Reference, id string, f environment.RequestFacts) (*captureassignment.RequestLease, error) {
	r, err := a.CaptureAssignmentAuthority.BeginRequest(ctx, c, id, f)
	if err == nil && r.Plan().Operation().Kind() == protocolspec.ClientOperationSemantic {
		a.entered <- struct{}{}
	}
	return r, err
}

type admissionExecutor struct {
	exchange.Executor
	entered chan<- struct{}
	release <-chan struct{}
}

func (e admissionExecutor) Execute(ctx context.Context, r exchange.ClientRequest, d exchange.Downstream) (exchange.Result, error) {
	e.entered <- struct{}{}
	select {
	case <-e.release:
	case <-ctx.Done():
		return exchange.Result{}, ctx.Err()
	}
	return e.Executor.Execute(ctx, r, d)
}

type admissionRawCounter struct {
	rawObserver
	scopes atomic.Int32
}

func (r *admissionRawCounter) BeginScope(context.Context, rawevidence.ScopeKind, string) (rawevidence.ScopeLease, error) {
	r.scopes.Add(1)
	return rawScopeLease{}, nil
}

func TestBodyAdmissionHTTPRetainsSlotThroughDownstreamFinalize(t *testing.T) {
	limits := exchangecontent.SourceLimits{Semantic: protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 32 << 20, StructureBytes: 32 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 32 << 20, StructureBytes: 32 << 20}}, Scratch: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 32 << 20}, CanonicalBytes: 256 << 20, RetainedBytes: 64 << 20, StructureBytes: 32 << 20}
	gate, err := exchange.NewBodyAdmission(bodyHTTPPolicy(limits, 1))
	if err != nil {
		t.Fatal(err)
	}
	raw := &heldFinalRaw{entered: make(chan struct{}), release: make(chan struct{})}
	fixture := newProxyFixtureForDialectWithPolicyAndRawEvidence(t, protocolspec.DialectAnthropicMessages, nil, allowEverythingTestPolicy(t), raw, "", func(o *loopbackproxy.Options) { o.BodyAdmission = gate })
	defer fixture.Close(t)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(fixture.authority.Certificate().CertificatePEM())
	proxyURL, _ := url.Parse("http://" + fixture.listener.Addr().String())
	proxyURL.User = url.UserPassword("capture", fixture.grant.ProxyCapability.Value())
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", strings.NewReader(`{"model":"client"}`))
		resp, err := (&http.Client{Transport: transport}).Do(req)
		if resp != nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		done <- err
	}()
	<-raw.entered
	cancel()
	<-done
	gate.BeginShutdown()
	if err := gate.Drain(ctx); !errors.Is(err, context.Canceled) {
		close(raw.release)
		t.Fatalf("held downstream finalizer refunded: %v", err)
	}
	close(raw.release)
	if err := gate.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(raw.snapshot()) != 2 {
		t.Fatal("Raw finalizer did not complete its real Handler observation")
	}
}

type heldFinalRaw struct {
	rawObserver
	entered, release chan struct{}
}

func (r *heldFinalRaw) Observe(ctx context.Context, o rawevidence.Observation) (rawevidence.Watermark, error) {
	if o.Layer == rawevidence.LayerClientDownstream {
		close(r.entered)
		<-r.release
	}
	return r.rawObserver.Observe(ctx, o)
}

func bodyHTTPPolicy(content exchangecontent.SourceLimits, slots uint64) exchange.ResourcePolicy {
	reserve, scratch, err := exchange.RequiredResponseReservation(content.Semantic)
	if err != nil {
		panic(err)
	}
	content.RetainedBytes += reserve.RetainedBytes
	content.CanonicalBytes += reserve.CanonicalBytes
	content.StructureBytes += reserve.StructureBytes
	content.Scratch = scratch
	request, response, err := exchange.RequiredExecutionEnvelope(content)
	if err != nil {
		panic(err)
	}
	return exchange.ResourcePolicy{Content: content, RequestBytes: request, ResponseBytes: response, SlotBytes: request + response, ActiveBytes: slots * (request + response)}
}

func TestBodyAdmissionHTTPCancellationAndReadConstructorFailures(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("http2=%v", h2), func(t *testing.T) {
			limits := exchangecontent.SourceLimits{Semantic: protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 32 << 20, StructureBytes: 32 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 32 << 20, StructureBytes: 32 << 20}}, CanonicalBytes: 256 << 20, RetainedBytes: 64 << 20, StructureBytes: 32 << 20}
			gate, err := exchange.NewBodyAdmission(bodyHTTPPolicy(limits, 1))
			if err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{}, 8)
			fixture := newProxyFixtureForDialectWithPolicyAndRawEvidence(t, protocolspec.DialectAnthropicMessages, nil, allowEverythingTestPolicy(t), nil, "", func(o *loopbackproxy.Options) {
				o.BodyAdmission = gate
				o.Assignments = admissionAssignments{o.Assignments, entered}
			})
			defer fixture.Close(t)
			roots := x509.NewCertPool()
			roots.AppendCertsFromPEM(fixture.authority.Certificate().CertificatePEM())
			proxyURL, _ := url.Parse("http://" + fixture.listener.Addr().String())
			proxyURL.User = url.UserPassword("capture", fixture.grant.ProxyCapability.Value())
			transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, ForceAttemptHTTP2: h2, ExpectContinueTimeout: time.Hour}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport}
			ctx, cancelAll := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancelAll()
			held, err := gate.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			canceled, cancel := context.WithCancel(ctx)
			body := &admissionBody{reader: strings.NewReader(`{"model":"client"}`)}
			done := make(chan error, 1)
			go func() {
				r, _ := http.NewRequestWithContext(canceled, "POST", "https://api.anthropic.com/v1/messages", body)
				r.ContentLength = int64(len(`{"model":"client"}`))
				r.Header.Set("Expect", "100-continue")
				response, err := client.Do(r)
				if response != nil {
					response.Body.Close()
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			cancel()
			if err := <-done; err == nil {
				t.Fatal("canceled waiter succeeded")
			}
			if body.reads.Load() != 0 {
				t.Fatal("canceled waiting body was read")
			}
			held.Release()
			// Truncated wire and an empty semantic body fail in read/constructor,
			// then the same single slot must execute a valid request successfully.
			r, _ := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", admissionReadFailure{})
			r.ContentLength = 100
			response, err := client.Do(r)
			if response != nil {
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
			}
			if err == nil && response.StatusCode == 200 {
				t.Fatal("broken request body succeeded")
			}
			r, _ = http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", nil)
			response, err = client.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != 400 {
				t.Fatalf("empty constructor status=%d", response.StatusCode)
			}
			r, _ = http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", strings.NewReader(`{"model":"client"}`))
			response, err = client.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != 200 || len(fixture.exchanges.Requests()) != 1 {
				t.Fatal("failed read/constructor leaked or executed a slot")
			}
			gate.BeginShutdown()
			if err := gate.Drain(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type admissionReadFailure struct{}

func (admissionReadFailure) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
