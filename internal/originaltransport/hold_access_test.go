package originaltransport_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/originidentity"
)

type heldOriginalNetwork struct {
	address      string
	dials, bytes atomic.Int64
}

func (n *heldOriginalNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" || address != n.address {
		return nil, errors.New("closed hold fixture rejected dial")
	}
	n.dials.Add(1)
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return &heldOriginalConnection{Conn: conn, network: n}, nil
}
func (*heldOriginalNetwork) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return nil, errors.New("closed hold fixture forbids DNS")
}

type heldOriginalConnection struct {
	net.Conn
	network *heldOriginalNetwork
}

func (c *heldOriginalConnection) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.network.bytes.Add(int64(n))
	return n, err
}

type heldOriginalFixture struct {
	gate      *offlinehold.Gate
	client    *originaltransport.Client
	prober    *originaltransport.TLSProber
	network   *heldOriginalNetwork
	origin    originidentity.ProviderOrigin
	httpCalls atomic.Int64
	cancels   []context.CancelFunc
	results   []chan error
}

func newHeldOriginalFixture(t *testing.T) *heldOriginalFixture {
	t.Helper()
	f := &heldOriginalFixture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.httpCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer synthetic-held-alias" || r.URL.RequestURI() != "/models?client_version=synthetic" {
			t.Error("held original request changed")
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)
	f.network = &heldOriginalNetwork{address: server.Listener.Addr().String()}
	var err error
	f.origin, err = originidentity.ParseProviderOrigin(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: f.network, SystemResolver: f.network})
	if err != nil {
		t.Fatal(err)
	}
	transport, err := originaltransport.NewProductionTransportWithBuilder(builder)
	if err != nil {
		t.Fatal(err)
	}
	f.prober, err = originaltransport.NewTLSProberWithBuilder(builder, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.gate, err = offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err = f.gate.Start(t.Context(), offlinehold.RuntimeBinding{InstanceID: "held-original-access"}); err != nil {
		t.Fatal(err)
	}
	f.client, err = originaltransport.New(originaltransport.Options{Coordinator: f.gate, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, cancel := range f.cancels {
			cancel()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := f.client.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		f.gate.BeginShutdown()
		if err := f.gate.Drain(ctx); err != nil {
			t.Error(err)
		}
		for _, result := range f.results {
			select {
			case <-result:
			case <-ctx.Done():
				t.Error("held caller did not join")
			}
		}
	})
	if _, err = f.gate.Enter(t.Context(), f.gate.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *heldOriginalFixture) queue(t *testing.T, id string, access egressnetwork.TargetAccess) chan error {
	t.Helper()
	options := payloadClassRequestOptions(t)
	options.RequestID = id
	options.Origin = f.origin
	options.Path = "/models"
	options.RawQuery = "client_version=synthetic"
	options.Headers = http.Header{"Authorization": {"Bearer synthetic-held-alias"}}
	frozen, err := originaltransport.NewRequest(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(egressnetwork.WithTargetAccess(t.Context(), access))
	f.cancels = append(f.cancels, cancel)
	done := make(chan error, 1)
	f.results = append(f.results, done)
	go func() {
		response, err := f.client.Do(ctx, frozen)
		if response != nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusAccepted {
				err = errors.New("original peer response failed")
			}
		}
		done <- err
	}()
	return done
}
func (f *heldOriginalFixture) pending(t *testing.T, count int) []offlinehold.ProbeTarget {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if f.gate.Snapshot().QueuedRequests == count {
			return f.gate.PendingProbeTargets()
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("original requests did not queue")
		}
	}
}
func TestOriginalHTTPHoldResumePreservesTargetAccess(t *testing.T) {
	for _, access := range []egressnetwork.TargetAccess{egressnetwork.TargetAccessPublicOnly, egressnetwork.TargetAccessOwnerConfigured} {
		name := "public_only"
		if access == egressnetwork.TargetAccessOwnerConfigured {
			name = "owner_configured"
		}
		t.Run(name, func(t *testing.T) {
			f := newHeldOriginalFixture(t)
			f.queue(t, "held-original", access)
			pending := f.pending(t, 1)
			if len(pending) != 1 {
				t.Fatalf("missing queued target: %+v", pending)
			}
			resumeContext := context.Background()
			if access == egressnetwork.TargetAccessOwnerConfigured {
				resumeContext = egressnetwork.WithTargetAccess(resumeContext, egressnetwork.TargetAccessPublicOnly)
			}
			snapshot, err := f.gate.Resume(resumeContext, f.gate.Snapshot().Revision, offlinehold.ResumeRequest{Targets: pending}, f.prober)
			if access == egressnetwork.TargetAccessPublicOnly {
				if err == nil || snapshot.State != offlinehold.StateHeld || f.network.dials.Load() != 0 || f.network.bytes.Load() != 0 || f.httpCalls.Load() != 0 {
					t.Errorf("public-only resume gained authority: err=%v state=%s physical_dials=%d bytes=%d HTTP=%d", err, snapshot.State, f.network.dials.Load(), f.network.bytes.Load(), f.httpCalls.Load())
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				select {
				case result := <-f.results[0]:
					f.results = f.results[1:]
					if result != nil {
						t.Fatal(result)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("owner request did not finish")
				}
				if f.network.dials.Load() != 2 || f.httpCalls.Load() != 1 || f.network.bytes.Load() == 0 {
					t.Errorf("owner resume missing probe/send: dials=%d HTTP=%d bytes=%d", f.network.dials.Load(), f.httpCalls.Load(), f.network.bytes.Load())
				}
			}
			t.Logf("frozen_access=%d resume_state=%s physical_dials=%d bytes=%d HTTP=%d", pending[0].TargetAccess, snapshot.State, f.network.dials.Load(), f.network.bytes.Load(), f.httpCalls.Load())
			if len(pending) != 1 || pending[0].TargetAccess != access {
				t.Errorf("queued access not frozen: want=%d targets=%+v", access, pending)
			}
		})
	}
}
func TestOriginalHTTPHeldTargetsKeepDistinctAccess(t *testing.T) {
	f := newHeldOriginalFixture(t)
	f.queue(t, "public-original", egressnetwork.TargetAccessPublicOnly)
	f.queue(t, "owner-original", egressnetwork.TargetAccessOwnerConfigured)
	pending := f.pending(t, 2)
	if len(pending) != 2 {
		t.Fatalf("different request allowances coalesced: %+v", pending)
	}
	seen := map[egressnetwork.TargetAccess]bool{}
	for _, target := range pending {
		seen[target.TargetAccess] = true
	}
	if !seen[egressnetwork.TargetAccessPublicOnly] || !seen[egressnetwork.TargetAccessOwnerConfigured] {
		t.Fatalf("pending access lost: %+v", pending)
	}
	t.Logf("pending_targets=%d public=%v owner=%v physical_dials=%d", len(pending), seen[egressnetwork.TargetAccessPublicOnly], seen[egressnetwork.TargetAccessOwnerConfigured], f.network.dials.Load())
	if f.network.dials.Load() != 0 || f.network.bytes.Load() != 0 {
		t.Fatal("held requests escaped before resume")
	}
}
