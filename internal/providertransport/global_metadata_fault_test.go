package providertransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

// This tests the actual queued policy plus real TLS/SOCKS, not the production
// auxiliary prober's implementation. It has no HTTP/credential capability.
type containedMetadataProber struct {
	dialers *egressnetwork.Builder
	roots   *x509.CertPool
	policy  egressnetwork.Policy
	calls   int
}

func (p *containedMetadataProber) Probe(ctx context.Context, request offlinehold.ProbeRequest) error {
	p.calls++
	if len(request.Targets) != 1 {
		return errors.New("unexpected probe target count")
	}
	target := request.Targets[0]
	if target.Validate() != nil || target.Kind != offlinehold.EgressAuxiliary || target.NetworkOrigin != "https://models.dev" || target.TLSServerName != "models.dev" || target.EgressPolicy != p.policy {
		return errors.New("queued probe changed frozen target/exit")
	}
	dialer, err := p.dialers.Dialer(target.EgressPolicy)
	if err != nil {
		return err
	}
	connection, err := dialer.DialContext(ctx, "tcp", "models.dev:443")
	if err != nil {
		return err
	}
	defer connection.Close()
	secure := tls.Client(connection, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: p.roots, ServerName: "models.dev"})
	if err = secure.HandshakeContext(ctx); err != nil {
		return err
	}
	return secure.Close()
}

func runGlobalMetadataFault(t *testing.T, mode string, client *Client, gate *offlinehold.Gate, resolver egressprofile.Resolver, audit *ownedWorkflowAudit, dialers *egressnetwork.Builder, roots *x509.CertPool, network *accountLoopbackNetwork, proxyCalls *atomic.Int64, observed <-chan *http.Request) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	defaults := resolver.(egressprofile.Defaults)
	setting, err := defaults.GetDefault(ctx)
	if err != nil || setting.Profile == nil {
		t.Fatal("missing actual default", err)
	}
	if mode == "doh_failure" {
		response, err := client.FetchModelsDev(ctx)
		if response != nil {
			response.Body.Close()
		}
		if err == nil || proxyCalls.Load() != 2 || network.calls.Load() != 2 || network.direct.Load() != 0 || network.dnsCalls.Load() != 0 {
			t.Fatalf("DoH failed open or changed exact A/AAAA query count: error=%t proxy=%d dial=%d direct=%d systemDNS=%d", err != nil, proxyCalls.Load(), network.calls.Load(), network.direct.Load(), network.dnsCalls.Load())
		}
		if len(observed) != 2 {
			t.Fatal("DoH failure emitted a target HTTP request")
		}
		audits := audit.snapshot()
		if len(audits) != 1 || audits[0].Outcome() == egressaudit.OutcomeCompleted || audits[0].Decision().SelectionSource != egressprofile.SelectionGlobal || audits[0].Decision().DefaultRevision != 2 {
			t.Fatal("DoH failure lost exact global terminal audit")
		}
		return
	}
	if _, err := gate.Enter(ctx, gate.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	result := make(chan runtimeFetchResult, 1)
	go func() {
		response, err := client.FetchModelsDev(ctx)
		result <- runtimeFetchResult{response: response, err: err}
	}()
	joined := false
	defer func() {
		cancel()
		if !joined {
			completed := <-result
			if completed.response != nil {
				completed.response.Body.Close()
			}
		}
	}()
	waitForGateQueue(t, gate, 1)
	if network.calls.Load() != 0 || audit.appendCount() != 0 {
		t.Fatal("held operation touched physical transport/audit")
	}
	targets := gate.PendingProbeTargets()
	if len(targets) != 1 || targets[0].EgressPolicy != setting.Profile.Policy {
		t.Fatal("held target does not contain admitted global exit")
	}
	if _, err := defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	if mode == "hold_cancel" {
		cancel()
	} else {
		prober := &containedMetadataProber{dialers: dialers, roots: roots, policy: setting.Profile.Policy}
		if _, err := gate.Resume(ctx, gate.Snapshot().Revision, offlinehold.ResumeRequest{Targets: targets}, prober); err != nil {
			t.Fatal(err)
		}
		if prober.calls != 1 {
			t.Fatal("hold resumed without exact probe")
		}
	}
	completed := <-result
	joined = true
	if mode == "hold_cancel" {
		if completed.response != nil {
			completed.response.Body.Close()
			t.Fatal("canceled held operation returned response")
		}
		if !errors.Is(completed.err, context.Canceled) || proxyCalls.Load() != 0 || network.calls.Load() != 0 || audit.appendCount() != 0 {
			t.Fatal("canceled held request sent or lost cancellation", completed.err)
		}
	} else {
		if completed.err != nil || completed.response == nil {
			t.Fatal("resumed metadata failed", completed.err)
		}
		body, err := io.ReadAll(completed.response.Body)
		completed.response.Body.Close()
		if err != nil || string(body) != "{}" || proxyCalls.Load() != 2 || network.calls.Load() != 2 || network.direct.Load() != 0 || len(observed) != 1 {
			t.Fatal("resumed metadata changed exact probe+HTTP count/body")
		}
		started, terminal := audit.attempts()
		if !terminal.Terminal() || terminal.Outcome() != egressaudit.OutcomeCompleted || terminal.Decision() != started.Decision() || terminal.Decision().SelectionSource != egressprofile.SelectionGlobal || terminal.Decision().DefaultRevision != 2 {
			t.Fatal("held operation reread changed default")
		}
	}
	snapshot := gate.Snapshot()
	if snapshot.ActiveActions != 0 || snapshot.ActiveEgress != 0 || snapshot.QueuedRequests != 0 {
		t.Fatal("held operation failed to drain")
	}
	next, err := client.FreezeLoginEgress(t.Context())
	if err != nil || next.Source() != egressprofile.SelectionBuiltin || next.DefaultRevision() != 3 {
		t.Fatal("next account-free operation did not see updated default", err)
	}
}
