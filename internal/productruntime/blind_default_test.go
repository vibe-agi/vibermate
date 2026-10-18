package productruntime

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/blindtunnel"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

type runtimeBlindNetwork struct {
	allowed map[string]bool
	denied  atomic.Int64
}

func (n *runtimeBlindNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" || !n.allowed[address] {
		n.denied.Add(1)
		return nil, errors.New("blind fixture rejected direct/unexpected destination")
	}
	return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
}

func TestRuntimeRejectsNilConcreteBlindProbeFactory(t *testing.T) {
	for _, factoryError := range []bool{false, true} {
		t.Run(fmt.Sprint(factoryError), func(t *testing.T) {
			gate, err := offlinehold.New(offlinehold.DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			options := testOptions(t, hostcontract.Desktop(), gate)
			options.BlindTunnels = &noNetworkBlindTunnels{coordinator: gate}
			options.OriginalTransport = &noNetworkOriginalTransport{}
			calls := 0
			sentinel := errors.New("private probe construction failure")
			r, err := startWithBuilders(t.Context(), options, runtimeBuildOverrides{blindProber: func() (*blindtunnel.ReachabilityProber, error) {
				calls++
				if factoryError {
					return nil, sentinel
				}
				return nil, nil
			}})
			if r != nil {
				shutdownRuntime(t, r)
			}
			if err == nil || calls != 1 || (factoryError && !errors.Is(err, sentinel)) || (!factoryError && !errors.Is(err, ErrInvalidBuildResult)) {
				t.Fatal("nil concrete blind prober was not rejected during startup")
			}
			if s := gate.Snapshot(); s.State != offlinehold.StateUnbound || s.ActiveEgress != 0 || s.ActiveActions != 0 {
				t.Fatalf("failed construction activated gate: %+v", s)
			}
			nextGate, err := offlinehold.New(offlinehold.DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			retry := testOptionsWithPaths(t, options.Paths, hostcontract.Desktop(), nextGate)
			retry.BlindTunnels = &noNetworkBlindTunnels{coordinator: nextGate}
			retry.OriginalTransport = &noNetworkOriginalTransport{}
			recovered, err := Start(t.Context(), retry)
			if err != nil {
				t.Fatalf("failed construction retained private resources: %v", err)
			}
			shutdownRuntime(t, recovered)
		})
	}
}
func (*runtimeBlindNetwork) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	if network != "ip" || host != "blind.fixture" {
		return nil, errors.New("blind fixture blocked DNS")
	}
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

func TestActualRuntimeBlindDefaultEditPreservesLiveTunnel(t *testing.T) {
	runActualRuntimeBlindDefault(t, false, false)
}
func TestActualRuntimeBlindHoldProbesAndSendsFrozenDefault(t *testing.T) {
	runActualRuntimeBlindDefault(t, true, false)
}
func TestActualRuntimeBlindPublicOnlyResumeRejectsPrivateTarget(t *testing.T) {
	runActualRuntimeBlindDefault(t, true, true)
}
func TestActualRuntimeSOCKSHoldOutlivesResponseDeadline(t *testing.T) {
	runActualRuntimeBlindDefault(t, true, false, "resume")
}
func TestActualRuntimeSOCKSPendingCancellation(t *testing.T) {
	for _, action := range []string{"client_close", "revoke", "shutdown", "overflow"} {
		t.Run(action, func(t *testing.T) { runActualRuntimeBlindDefault(t, true, false, action) })
	}
}
func TestActualRuntimeSOCKSApprovalWaiting(t *testing.T) {
	for _, action := range []string{"approval_resume", "approval_cancel"} {
		t.Run(action, func(t *testing.T) { runActualRuntimeBlindDefault(t, true, false, action) })
	}
}
func runActualRuntimeBlindDefault(t *testing.T, hold, publicDenied bool, socksMode ...string) {
	upstream, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			c, err := upstream.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer c.Close()
				c.SetDeadline(time.Now().Add(4 * time.Second))
				io.Copy(c, c)
			}()
		}
	}()
	defer func() { upstream.Close(); workers.Wait() }()
	first, firstCount := runtimeSOCKSFixture(t, upstream.Addr().String())
	second, secondCount := runtimeSOCKSFixture(t, upstream.Addr().String())
	network := &runtimeBlindNetwork{allowed: map[string]bool{first.Addr().String(): true, second.Addr().String(): true}}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: network})
	if err != nil {
		t.Fatal(err)
	}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	dialer, err := blindtunnel.NewDialerWithBuilder(gate, builder)
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions(t, hostcontract.Desktop(), gate)
	options.BlindTunnels = &blindTunnelDialer{coordinator: gate, dialer: dialer}
	options.OriginalTransport = &noNetworkOriginalTransport{}
	buildOverrides := runtimeBuildOverrides{}
	var probeBuilds atomic.Int64
	if hold {
		buildOverrides.blindProber = func() (*blindtunnel.ReachabilityProber, error) {
			probeBuilds.Add(1)
			return blindtunnel.NewReachabilityProberWithBuilder(builder)
		}
	}
	r, err := startWithBuilders(t.Context(), options, buildOverrides)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRuntime(t, r)
	if hold && !publicDenied {
		decision := connectionpolicy.DecisionAllow
		if len(socksMode) > 0 && strings.HasPrefix(socksMode[0], "approval_") {
			decision = connectionpolicy.DecisionAsk
		}
		_, err := r.ConnectionRules().Replace(t.Context(), r.ConnectionRules().Current().Revision, []connectionpolicy.Rule{{ID: "allow.blind-private", Priority: 1, Decision: decision, Match: connectionpolicy.MatchExactHostPort("blind.fixture", 443)}}, connectionpolicy.ModeDenyUnknown)
		if err != nil {
			t.Fatal(err)
		}
	}
	profiles := make([]egressprofile.ProfileRevision, 0, 2)
	for i, proxy := range []net.Listener{first, second} {
		profile, err := r.EgressProfiles().Publish(t.Context(), egressprofile.PublishCommand{ID: egressprofile.ID(fmt.Sprintf("profile.blind-%d", i)), DisplayName: "Private blind", Policy: egressnetwork.Policy{Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: proxy.Addr().String()}}})
		if err != nil {
			t.Fatal(err)
		}
		profiles = append(profiles, profile)
	}
	set := func(index int, revision uint64) {
		t.Helper()
		p := profiles[index]
		if _, err := r.EgressDefaults().SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: revision, Profile: &egressprofile.ProfileRef{ID: p.ID, Revision: p.Revision}}); err != nil {
			t.Fatal(err)
		}
	}
	set(0, 1)
	grant, err := r.ManualCaptures().Create(t.Context(), manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Private blind capture", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	if err != nil {
		t.Fatal(err)
	}
	capture, err := captureidentity.New(captureidentity.KindManualCapture, grant.Capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.CaptureAssignments().Create(t.Context(), captureassignment.CreateCommand{Capture: capture, EnvironmentID: environment.SystemTransparentID, Source: captureassignment.SourceSystemTransparent}); err != nil {
		t.Fatal(err)
	}
	WaitRecordingTestRuntime(t, r)
	server := httptest.NewUnstartedServer(r.ProxyHandler())
	if hold {
		server.Config.BaseContext = func(net.Listener) context.Context {
			return egressnetwork.WithTargetAccess(context.Background(), egressnetwork.TargetAccessPublicOnly)
		}
	}
	server.Start()
	defer server.Close()
	var socks net.Listener
	opened := make(chan net.Conn, 2)
	if len(socksMode) != 0 {
		socks, err = net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		served := make(chan error, 1)
		go func() { served <- r.SOCKSProxy().Serve(socks) }()
		defer func() { socks.Close(); <-served }()
	}
	connect := func() (net.Conn, error) {
		if socks != nil {
			c := runtimeSOCKSAuthenticate(t, socks.Addr().String(), grant.Credential.Value())
			c.SetDeadline(time.Now().Add(20 * time.Second))
			opened <- c
			packet := runtimeUDPDomain("blind.fixture", 443, nil)
			if _, e := c.Write(append(append([]byte{5, 1, 0}, packet[3:]...), []byte("early-one/early-two")...)); e != nil {
				c.Close()
				return nil, e
			}
			var reply [10]byte
			if _, e := io.ReadFull(c, reply[:]); e != nil {
				c.Close()
				return nil, e
			}
			if reply[1] != 0 {
				c.Close()
				return nil, fmt.Errorf("SOCKS CONNECT status %d", reply[1])
			}
			early := make([]byte, len("early-one/early-two"))
			if _, e := io.ReadFull(c, early); e != nil || string(early) != "early-one/early-two" {
				c.Close()
				return nil, fmt.Errorf("early payload %q %v", early, e)
			}
			return c, nil
		}
		c, err := net.DialTimeout("tcp4", server.Listener.Addr().String(), time.Second)
		if err != nil {
			return nil, err
		}
		c.SetDeadline(time.Now().Add(3 * time.Second))
		auth := base64.StdEncoding.EncodeToString([]byte("capture:" + grant.Credential.Value()))
		if _, err = io.WriteString(c, "CONNECT blind.fixture:443 HTTP/1.1\r\nHost: blind.fixture:443\r\nProxy-Authorization: Basic "+auth+"\r\n\r\n"); err != nil {
			c.Close()
			return nil, err
		}
		response, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			c.Close()
			return nil, err
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			c.Close()
			return nil, fmt.Errorf("private CONNECT status%d", response.StatusCode)
		}
		return c, nil
	}
	echo := func(c net.Conn, text string) {
		t.Helper()
		if _, err := io.WriteString(c, text); err != nil {
			t.Fatal(err)
		}
		body := make([]byte, len(text))
		if _, err := io.ReadFull(c, body); err != nil || string(body) != text {
			t.Fatalf("opaque echo mismatch: %v", err)
		}
	}
	var one net.Conn
	if hold {
		if _, err = r.EnterOfflineHold(t.Context(), gate.Snapshot().Revision); err != nil {
			t.Fatal(err)
		}
		type connectionResult struct {
			connection net.Conn
			err        error
		}
		done := make(chan connectionResult, 1)
		go func() { c, err := connect(); done <- connectionResult{c, err} }()
		joined := false
		defer func() {
			if !joined {
				gate.BeginShutdown()
				result := <-done
				if result.connection != nil {
					result.connection.Close()
				}
			}
		}()
		if len(socksMode) > 0 && strings.HasPrefix(socksMode[0], "approval_") {
			var pending toolapproval.View
			runtimeUDPWait(t, func() bool {
				page, e := r.ToolApprovals().ListApprovals(t.Context(), toolapproval.PageRequest{State: toolapproval.StatePending, Limit: 10})
				if e != nil {
					t.Fatal(e)
				}
				if len(page.Items) == 1 {
					pending = page.Items[0]
					return true
				}
				return false
			})
			if pending.Kind != string(toolapproval.KindNetworkAsk) || firstCount.Load() != 0 || secondCount.Load() != 0 || gate.Snapshot().QueuedRequests != 0 {
				t.Fatal("approval did not precede Hold/target send")
			}
			if socksMode[0] == "approval_cancel" {
				(<-opened).Close()
				result := <-done
				joined = true
				if result.connection != nil {
					result.connection.Close()
				}
				if result.err == nil {
					t.Fatal("canceled approval connected")
				}
				runtimeUDPWait(t, func() bool {
					p, e := r.ToolApprovals().GetApproval(t.Context(), pending.ID)
					return e == nil && p.State != toolapproval.StatePending
				})
				if _, e := r.ResumeOfflineHold(t.Context(), gate.Snapshot().Revision); e != nil {
					t.Fatal(e)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				defer cancel()
				if e := r.SOCKSProxy().Shutdown(ctx); e != nil {
					t.Fatal(e)
				}
				if s := gate.Snapshot(); s.ActiveActions != 0 || s.ActiveEgress != 0 || s.QueuedRequests != 0 || firstCount.Load() != 0 || secondCount.Load() != 0 {
					t.Fatal("late send or retained approval work")
				}
				return
			}
			if _, e := r.ToolApprovals().DecideApproval(t.Context(), toolapproval.DecisionCommand{ApprovalID: pending.ID, ExpectedRevision: pending.Revision, IdempotencyKey: "socks-pending-allow-once", Decision: toolapproval.DecisionAllowOnce, Scope: toolapproval.ScopeRequest}); e != nil {
				t.Fatal(e)
			}
			if _, e := r.ConnectionRules().Replace(t.Context(), r.ConnectionRules().Current().Revision, []connectionpolicy.Rule{{ID: "allow.blind-private", Priority: 1, Decision: connectionpolicy.DecisionAllow, Match: connectionpolicy.MatchExactHostPort("blind.fixture", 443)}}, connectionpolicy.ModeDenyUnknown); e != nil {
				t.Fatal(e)
			}
		}
		waitForQueuedTargets(t, gate, 1)
		if socks != nil {
			control := <-opened
			if socksMode[0] == "resume" {
				select {
				case result := <-done:
					joined = true
					if result.connection != nil {
						result.connection.Close()
					}
					t.Fatalf("authorized Hold ended before 10.5s: %v", result.err)
				case <-time.After(10500 * time.Millisecond):
				}
			} else if socksMode[0] != "approval_resume" {
				switch socksMode[0] {
				case "client_close":
					control.Close()
				case "overflow":
					control.Write(make([]byte, (64<<10)+1))
				case "revoke":
					ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
					defer cancel()
					if e := r.SOCKSProxy().Revoke(ctx, capture, "capture_stopped"); e != nil {
						t.Fatal(e)
					}
				case "shutdown":
					shutdownRuntime(t, r)
				}
				select {
				case result := <-done:
					joined = true
					if result.connection != nil {
						result.connection.Close()
					}
					if result.err == nil {
						t.Fatal("canceled pending SOCKS succeeded")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("pending SOCKS/Handler did not cancel")
				}
				runtimeUDPWait(t, func() bool {
					s := gate.Snapshot()
					return s.QueuedRequests == 0 && s.ActiveActions == 0 && s.ActiveEgress == 0
				})
				if socksMode[0] != "shutdown" {
					if _, e := r.ResumeOfflineHold(t.Context(), gate.Snapshot().Revision); e != nil {
						t.Fatal(e)
					}
					ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
					defer cancel()
					if e := r.SOCKSProxy().Shutdown(ctx); e != nil {
						t.Fatal(e)
					}
				}
				if firstCount.Load() != 0 || secondCount.Load() != 0 || network.denied.Load() != 0 {
					t.Fatal("late target send after pending cancellation")
				}
				return
			}
		}
		targets, err := r.resumeProbeTargets()
		wantAccess := egressnetwork.TargetAccessOwnerConfigured
		if publicDenied {
			wantAccess = egressnetwork.TargetAccessPublicOnly
		}
		if err != nil || len(targets) != 1 || targets[0].EgressPolicy != profiles[0].Policy || targets[0].TargetAccess != wantAccess || firstCount.Load() != 0 || secondCount.Load() != 0 {
			t.Fatalf("queued actual frozen target %+v err=%v", targets, err)
		}
		set(1, 2)
		_, err = r.ResumeOfflineHold(t.Context(), gate.Snapshot().Revision)
		if publicDenied {
			if !errors.Is(err, egressnetwork.ErrPrivateTarget) || gate.Snapshot().State != offlinehold.StateHeld || gate.Snapshot().QueuedRequests != 1 || firstCount.Load() != 0 || secondCount.Load() != 0 || network.denied.Load() != 0 {
				t.Fatalf("public-only resume err=%v snapshot=%+v proxies=%d/%d direct=%d", err, gate.Snapshot(), firstCount.Load(), secondCount.Load(), network.denied.Load())
			}
			gate.BeginShutdown()
			result := <-done
			joined = true
			if result.connection != nil {
				result.connection.Close()
			}
			if result.err == nil {
				t.Fatal("denied queued connection succeeded")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		result := <-done
		joined = true
		if result.err != nil {
			t.Fatal(result.err)
		}
		one = result.connection
		if probeBuilds.Load() != 1 {
			t.Fatalf("real contained probe factory calls%d", probeBuilds.Load())
		}
	} else {
		one, err = connect()
		if err != nil {
			t.Fatal(err)
		}
	}
	defer one.Close()
	echo(one, "before-edit")
	if !hold {
		set(1, 2)
	}
	echo(one, "same-open-tunnel")
	if socks != nil {
		// The pending-input budget must not cap established conversation bytes.
		echo(one, strings.Repeat("streaming-after-approval/", 6000))
	}
	if hold {
		one.Close()
		deadline := time.Now().Add(time.Second)
		for gate.Snapshot().State != offlinehold.StateOnline && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if gate.Snapshot().State != offlinehold.StateOnline {
			t.Fatal("released tunnel did not drain before next default target")
		}
	}
	two, err := connect()
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	echo(two, "new-exit")
	one.Close()
	two.Close()
	wantFirst := int64(1)
	if hold {
		wantFirst = 2
	}
	if firstCount.Load() != wantFirst || secondCount.Load() != 1 || network.denied.Load() != 0 {
		t.Fatalf("first%d second%d denied%d", firstCount.Load(), secondCount.Load(), network.denied.Load())
	}
	drain, cancelDrain := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancelDrain()
	if err := gate.Drain(drain); err != nil {
		t.Fatal("actual blind producers did not join", err)
	}
	if _, err := SettledEgressTestPage(t, r, egressaudit.PageRequest{Limit: 10}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		page, err := r.EgressAttempts().List(t.Context(), egressaudit.PageRequest{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 2 && page.Items[0].Attempt.Terminal() && page.Items[1].Attempt.Terminal() {
			seen := map[string]uint64{}
			for _, record := range page.Items {
				a := record.Attempt
				d := a.Decision()
				if d.SelectionSource != egressprofile.SelectionGlobal || d.PolicyRevision != d.DefaultRevision || d.AccountID != "" || a.BytesIn() == 0 || a.BytesOut() == 0 {
					t.Fatalf("blind actual audit %+v", d)
				}
				seen[d.ProxyID] = d.DefaultRevision
			}
			if seen[profiles[0].ID.String()] != 2 || seen[profiles[1].ID.String()] != 3 {
				t.Fatalf("default freeze lost: %+v", seen)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("missing actual Runtime blind terminal audit")
		}
		time.Sleep(time.Millisecond)
	}
}
