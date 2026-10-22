package egressnetwork

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// A healthy family must retain time to connect through the selected transport
// when the other DNS query cannot complete. No target network leaves the test.
func TestDoHHealthyFamilyCanConnectWhenOtherFamilyStalls(t *testing.T) {
	for _, stalled := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
		t.Run(stalled.String(), func(t *testing.T) {
			fixture := newDoHRecoveryFixture(t, stalled, false)
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			connection, err := fixture.dialer.DialContext(ctx, "tcp", "agent.example:443")
			if err != nil {
				t.Fatalf("healthy DNS family never reached the selected target transport: %v", err)
			}
			connection.Close()
			if got := fixture.targetDials.Load(); got != 1 {
				t.Fatalf("target dials = %d, want one healthy-family connection", got)
			}
		})
	}
}

func TestDoHParentCancellationDoesNotAttemptPartialAnswer(t *testing.T) {
	fixture := newDoHRecoveryFixture(t, dnsmessage.TypeAAAA, false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		connection, err := fixture.dialer.DialContext(ctx, "tcp", "agent.example:443")
		if connection != nil {
			connection.Close()
		}
		finished <- err
	}()
	for _, ready := range []<-chan struct{}{fixture.healthyAnswer, fixture.stalledQuery} {
		select {
		case <-ready:
		case <-time.After(3 * time.Second):
			t.Fatal("DNS fixture did not reach both family operations")
		}
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled lookup returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled DNS lookup did not finish")
	}
	if got := fixture.targetDials.Load(); got != 0 {
		t.Fatalf("canceled lookup attempted %d target dials using a partial answer", got)
	}
}

func TestDoHCompletedPrivateFamilyStillRejectsWholeAnswer(t *testing.T) {
	fixture := newDoHRecoveryFixture(t, 0, true)
	ctx, cancel := context.WithTimeout(WithTargetAccess(t.Context(), TargetAccessPublicOnly), 4*time.Second)
	defer cancel()
	connection, err := fixture.dialer.DialContext(ctx, "tcp", "agent.example:443")
	if connection != nil {
		connection.Close()
	}
	if !errors.Is(err, ErrPrivateTarget) || fixture.targetDials.Load() != 0 {
		t.Fatalf("mixed public/private DNS answer was not refused before dialing: %v", err)
	}
}

type dohRecoveryFixture struct {
	dialer                      ContextDialer
	targetDials                 atomic.Int32
	healthyAnswer, stalledQuery chan struct{}
}

type dohRecoveryDialFunc func(context.Context, string, string) (net.Conn, error)

func (f dohRecoveryDialFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}

func newDoHRecoveryFixture(t *testing.T, stalled dnsmessage.Type, privateAAAA bool) *dohRecoveryFixture {
	t.Helper()
	f := &dohRecoveryFixture{healthyAnswer: make(chan struct{}, 2), stalledQuery: make(chan struct{}, 2)}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		payload, err := io.ReadAll(io.LimitReader(request.Body, 65536))
		var query dnsmessage.Message
		if err != nil || query.Unpack(payload) != nil || len(query.Questions) != 1 {
			http.Error(w, "invalid test DNS query", http.StatusBadRequest)
			return
		}
		question := query.Questions[0]
		if question.Type == stalled {
			f.stalledQuery <- struct{}{}
			<-request.Context().Done()
			return
		}
		var body dnsmessage.ResourceBody
		switch question.Type {
		case dnsmessage.TypeA:
			body = &dnsmessage.AResource{A: [4]byte{1, 1, 1, 1}}
		case dnsmessage.TypeAAAA:
			address := [16]byte{0x26, 0x06, 0x47, 0x00, 0x47, 0x00, 0, 0, 0, 0, 0, 0, 0, 0, 0x11, 0x11}
			if privateAAAA {
				address = [16]byte{15: 1}
			}
			body = &dnsmessage.AAAAResource{AAAA: address}
		default:
			http.Error(w, "unexpected query family", http.StatusBadRequest)
			return
		}
		answer := dnsmessage.Message{
			Header:    dnsmessage.Header{ID: query.ID, Response: true, RecursionAvailable: true},
			Questions: query.Questions,
			Answers: []dnsmessage.Resource{{
				Header: dnsmessage.ResourceHeader{Name: question.Name, Type: question.Type, Class: dnsmessage.ClassINET, TTL: 30},
				Body:   body,
			}},
		}
		encoded, err := answer.Pack()
		if err != nil {
			http.Error(w, "test DNS encoding failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(encoded)
		f.healthyAnswer <- struct{}{}
	}))
	t.Cleanup(server.Close)
	network := dohRecoveryDialFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == server.Listener.Addr().String() {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		}
		f.targetDials.Add(1)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if address != "1.1.1.1:443" && address != "[2606:4700:4700::1111]:443" {
			return nil, errors.New("unexpected synthetic target")
		}
		client, peer := net.Pipe()
		t.Cleanup(func() { client.Close(); peer.Close() })
		return client, nil
	})
	tlsConfig := server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	tlsConfig.MinVersion = tls.VersionTLS12
	builder, err := NewBuilder(BuilderOptions{BaseDialer: network, TLSClientConfig: tlsConfig})
	if err != nil {
		t.Fatal(err)
	}
	f.dialer, err = builder.Dialer(Policy{
		Proxy:    ProxyPolicy{Kind: ProxyDirect},
		Resolver: ResolverPolicy{Kind: ResolverDoH, DoHURL: server.URL + "/dns-query", Transport: ResolverTransportDirect},
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
