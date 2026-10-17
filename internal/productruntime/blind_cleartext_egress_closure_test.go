package productruntime

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
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
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

// These tests catch builtin selection losing its persisted revision, re-selection
// after Hold, fallback after proxy loss, or audit/lease loss at failure boundaries.
// Only socket destinations are mapped; admission, resolution, probes and sends
// are the real Runtime components.
type b4DialKey struct {
	address string
	ordinal int
}
type b4DialBarrier struct {
	entered, release chan struct{}
	once             sync.Once
}

func (b *b4DialBarrier) open() { b.once.Do(func() { close(b.release) }) }

type b4Network struct {
	destinations      map[string]string
	mu                sync.Mutex
	entries, physical map[string]int
	barriers          map[b4DialKey]*b4DialBarrier
	rejected          []string
	unlisted, lookups atomic.Int64
}

func (n *b4Network) DialContext(ctx context.Context, kind, address string) (net.Conn, error) {
	destination, ok := n.destinations[address]
	if kind != "tcp" || !ok {
		n.unlisted.Add(1)
		n.mu.Lock()
		n.rejected = append(n.rejected, kind+" "+address)
		n.mu.Unlock()
		return nil, errors.New("B4 blocked unlisted socket")
	}
	n.mu.Lock()
	n.entries[address]++
	b := n.barriers[b4DialKey{address, n.entries[address]}]
	n.mu.Unlock()
	if b != nil {
		close(b.entered)
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	n.mu.Lock()
	n.physical[address]++
	n.mu.Unlock()
	return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, kind, destination)
}
func (n *b4Network) LookupNetIP(_ context.Context, kind, host string) ([]netip.Addr, error) {
	n.lookups.Add(1)
	if kind != "ip" || host != "blind.fixture" {
		n.unlisted.Add(1)
		return nil, errors.New("B4 blocked unlisted DNS")
	}
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}
func (n *b4Network) block(address string, ordinal int) *b4DialBarrier {
	b := &b4DialBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	n.mu.Lock()
	n.barriers[b4DialKey{address, ordinal}] = b
	n.mu.Unlock()
	return b
}
func (n *b4Network) counts(address string) (int, int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.entries[address], n.physical[address]
}

// Each listener owns and joins its accept loop, children, and both copy workers.
type b4Listener struct {
	l                 net.Listener
	mu                sync.Mutex
	sockets           map[net.Conn]bool // true only when stop explicitly closes this socket
	wg                sync.WaitGroup
	done              chan struct{}
	once              sync.Once
	started           bool
	accepted, onward  atomic.Int64
	handshakes, limit atomic.Int64
}

func b4Listen(t *testing.T) *b4Listener {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &b4Listener{l: l, sockets: map[net.Conn]bool{}, done: make(chan struct{})}
	t.Cleanup(func() { p.stop(); b4Join(t, p.done, "listener") })
	return p
}
func (p *b4Listener) track(c net.Conn) { p.mu.Lock(); p.sockets[c] = false; p.mu.Unlock() }
func (p *b4Listener) closedByStop(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sockets[c]
}
func (p *b4Listener) serve(handle func(net.Conn)) {
	p.mu.Lock()
	p.started = true
	p.mu.Unlock()
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			c, err := p.l.Accept()
			if err != nil {
				return
			}
			p.accepted.Add(1)
			p.track(c)
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(3 * time.Second))
				handle(c)
			}()
		}
	}()
	go func() { p.wg.Wait(); close(p.done) }()
}
func (p *b4Listener) stop() {
	p.once.Do(func() {
		p.l.Close()
		p.mu.Lock()
		defer p.mu.Unlock()
		for c := range p.sockets {
			p.sockets[c] = true
			c.Close()
		}
		if !p.started {
			close(p.done)
		}
	})
}
func b4Join(t *testing.T, done <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Errorf("B4 cleanup unfinished %s", name)
	}
}

type b4Fixture struct {
	t                            *testing.T
	ctx                          context.Context
	cancel                       context.CancelFunc
	r                            *Runtime
	gate                         *offlinehold.Gate
	n                            *b4Network
	origin, a, b                 *b4Listener
	server                       *httptest.Server
	auth, target, resolvedTarget string
	port                         uint16
	profiles                     [2]egressprofile.ProfileRevision
	requests, probes, bytes      atomic.Int64
	mu                           sync.Mutex
	invalid                      []string
	clients                      []net.Conn
	jobs                         []<-chan struct{}
	expectedAttempts             int
}

func (f *b4Fixture) bad(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalid = append(f.invalid, fmt.Sprintf(format, args...))
}
func newB4Fixture(t *testing.T, port uint16, recorders ...observationRecorderBuilder) *b4Fixture {
	var recorder observationRecorderBuilder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	t.Helper()
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	// Allocate Store paths before cleanup, so TempDir cannot remove live SQL files.
	options := testOptions(t, hostcontract.Desktop(), gate)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	f := &b4Fixture{t: t, ctx: ctx, cancel: cancel, gate: gate, port: port, target: fmt.Sprintf("blind.fixture:%d", port), resolvedTarget: fmt.Sprintf("127.0.0.1:%d", port)}
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			cancel()
			if f.n != nil {
				f.n.mu.Lock()
				for _, b := range f.n.barriers {
					b.open()
				}
				f.n.mu.Unlock()
			}
			gate.BeginShutdown()
			f.mu.Lock()
			for _, c := range f.clients {
				c.Close()
			}
			jobs := append([]<-chan struct{}(nil), f.jobs...)
			f.mu.Unlock()
			for _, p := range []*b4Listener{f.a, f.b, f.origin} {
				if p != nil {
					p.stop()
				}
			}
			for _, job := range jobs {
				b4Join(t, job, "request/Resume")
			}
			if f.r != nil {
				cleanupContext, stop := context.WithTimeout(context.Background(), 10*time.Second)
				if err := gate.Drain(cleanupContext); err != nil {
					t.Errorf("B4 cleanup gate drain: %v", err)
				}
				if err := f.r.recorder.Flush(cleanupContext); err != nil {
					t.Error(err)
				}
				page, err := f.r.EgressAttempts().List(cleanupContext, egressaudit.PageRequest{Limit: 10})
				stop()
				if err != nil {
					t.Errorf("B4 cleanup audit: %v", err)
				} else {
					if f.expectedAttempts > 0 && len(page.Items) != f.expectedAttempts {
						t.Errorf("B4 cleanup attempts=%d want=%d", len(page.Items), f.expectedAttempts)
					}
					for _, v := range page.Items {
						a := v.Attempt
						if !a.Terminal() {
							t.Errorf("B4 cleanup nonterminal outbound=%s", a.ID())
						}
						t.Logf("B4 cleanup outbound=%s parent=%s terminal=%t outcome=%s", a.ID(), a.ConnectionID(), a.Terminal(), a.Outcome())
					}
				}
			}
			if f.server != nil {
				f.server.Close()
			}
			if f.r != nil {
				shutdownRuntime(t, f.r)
			}
			for _, p := range []*b4Listener{f.a, f.b, f.origin} {
				if p != nil {
					b4Join(t, p.done, "socket workers")
				}
			}
			s := gate.Snapshot()
			if s.QueuedRequests != 0 || s.ActiveActions != 0 || s.ActiveEgress != 0 {
				t.Errorf("B4 cleanup gate=%+v", s)
			}
			t.Logf("B4 cleanup joined requests/Resume/listeners/copy workers; gate=%+v", s)
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.invalid) != 0 {
				t.Errorf("B4 fixture failures=%v", f.invalid)
			}
		})
	}
	t.Cleanup(cleanup)
	f.origin = b4Listen(t)
	f.origin.serve(func(c net.Conn) {
		reader := bufio.NewReader(c)
		_, err := reader.Peek(1)
		if err == io.EOF {
			f.probes.Add(1)
			return
		}
		if err != nil {
			f.bad("origin first byte: %v", err)
			return
		}
		if port == 443 {
			n, err := io.Copy(c, reader)
			f.bytes.Add(n)
			// stop closes this fixture's socket before joining its copy worker.
			// Only that exact local close is an expected teardown interruption.
			if err != nil && !(errors.Is(err, net.ErrClosed) && f.origin.closedByStop(c)) {
				f.bad("opaque echo: %v", err)
			}
			return
		}
		req, err := http.ReadRequest(reader)
		if err != nil {
			f.bad("origin request: %v", err)
			return
		}
		defer req.Body.Close()
		body, err := io.ReadAll(req.Body)
		f.requests.Add(1)
		f.bytes.Add(int64(len(body)))
		if err != nil || req.Method != "POST" || req.RequestURI != "/b4" || req.URL.IsAbs() || string(body) != "b4-opaque-body" || req.Header.Get("Authorization") != "Bearer client-b4" || req.Header.Get("X-B4") != "retained" || req.Host != "blind.fixture:80" {
			f.bad("origin method=%s uri=%s host=%s body=%q headers=%v err=%v", req.Method, req.RequestURI, req.Host, body, req.Header, err)
		}
		for _, name := range []string{"Proxy-Authorization", "Proxy-Connection", "Connection", "X-B4-Hop"} {
			if req.Header.Get(name) != "" {
				f.bad("origin leaked %s", name)
			}
		}
		_, err = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\nConnection: close\r\n\r\nb4-ok")
		if err != nil {
			f.bad("origin reply: %v", err)
		}
	})
	f.a = b4Listen(t)
	f.b = b4Listen(t)
	for _, p := range []*b4Listener{f.a, f.b} {
		p.serve(func(c net.Conn) {
			var hello [3]byte
			if _, err := io.ReadFull(c, hello[:]); err != nil {
				f.bad("SOCKS hello: %v", err)
				return
			}
			if hello != [3]byte{5, 1, 0} {
				f.bad("SOCKS hello=%v", hello)
				return
			}
			if _, err := c.Write([]byte{5, 0}); err != nil {
				f.bad("SOCKS greeting reply: %v", err)
				return
			}
			var target [10]byte
			if _, err := io.ReadFull(c, target[:]); err != nil {
				f.bad("SOCKS target: %v", err)
				return
			}
			if target[0] != 5 || target[1] != 1 || target[2] != 0 || target[3] != 1 || net.IP(target[4:8]).String() != "127.0.0.1" || binary.BigEndian.Uint16(target[8:]) != port {
				f.bad("SOCKS target=%v", target)
				return
			}
			// Strict per-case handshake budget is checked before replay/copy.
			if p.handshakes.Add(1) > p.limit.Load() {
				f.bad("extra SOCKS handshake beyond prescribed budget")
				return
			}
			peer, err := net.DialTimeout("tcp4", f.origin.l.Addr().String(), time.Second)
			if err != nil {
				_, _ = c.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0})
				f.bad("SOCKS onward: %v", err)
				return
			}
			p.onward.Add(1)
			p.track(peer)
			defer peer.Close()
			_ = peer.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err = c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
				f.bad("SOCKS success reply: %v", err)
				return
			}
			done := make(chan struct{})
			go func() { io.Copy(peer, c); peer.Close(); c.Close(); close(done) }()
			io.Copy(c, peer)
			c.Close()
			peer.Close()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				f.bad("copy worker unfinished")
			}
		})
	}
	// Owner-configured Direct/system passes the original canonical name to the
	// base dialer. Both exact representations map only to this owned origin;
	// count both, so either representation of an accidental fallback is visible.
	f.n = &b4Network{destinations: map[string]string{f.target: f.origin.l.Addr().String(), f.resolvedTarget: f.origin.l.Addr().String(), f.a.l.Addr().String(): f.a.l.Addr().String(), f.b.l.Addr().String(): f.b.l.Addr().String()}, entries: map[string]int{}, physical: map[string]int{}, barriers: map[b4DialKey]*b4DialBarrier{}}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: f.n, SystemResolver: f.n})
	if err != nil {
		t.Fatal(err)
	}
	dialer, err := blindtunnel.NewDialerWithBuilder(gate, builder)
	if err != nil {
		t.Fatal(err)
	}
	options.BlindTunnels = &blindTunnelDialer{coordinator: gate, dialer: dialer}
	options.OriginalTransport = &noNetworkOriginalTransport{}
	f.r, err = startWithBuilders(ctx, options, runtimeBuildOverrides{recorder: recorder, blindProber: func() (*blindtunnel.ReachabilityProber, error) {
		return blindtunnel.NewReachabilityProberWithBuilder(builder)
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.r.ConnectionRules().Replace(ctx, f.r.ConnectionRules().Current().Revision, []connectionpolicy.Rule{{ID: "allow.b4", Priority: 1, Decision: connectionpolicy.DecisionAllow, Match: connectionpolicy.MatchExactHostPort("blind.fixture", port)}}, connectionpolicy.ModeDenyUnknown)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range []*b4Listener{f.a, f.b} {
		f.profiles[i], err = f.r.EgressProfiles().Publish(ctx, egressprofile.PublishCommand{ID: egressprofile.ID([]string{"profile.b4-a", "profile.b4-b"}[i]), DisplayName: "B4 private", Policy: egressnetwork.Policy{Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: p.l.Addr().String()}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	grant, err := f.r.ManualCaptures().Create(ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "B4 capture", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	if err != nil {
		t.Fatal(err)
	}
	capture, err := captureidentity.New(captureidentity.KindManualCapture, grant.Capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.r.CaptureAssignments().Create(ctx, captureassignment.CreateCommand{Capture: capture, EnvironmentID: environment.SystemTransparentID, Source: captureassignment.SourceSystemTransparent})
	if err != nil {
		t.Fatal(err)
	}
	f.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte("capture:"+grant.Credential.Value()))
	f.server = httptest.NewUnstartedServer(f.r.ProxyHandler())
	f.server.Config.BaseContext = func(net.Listener) context.Context {
		return egressnetwork.WithTargetAccess(ctx, egressnetwork.TargetAccessPublicOnly)
	}
	f.server.Start()
	// Run the aggregate cancellation first; earlier registrations cover partial setup.
	t.Cleanup(cleanup)
	return f
}
func (f *b4Fixture) set(index int, revision uint64) {
	f.t.Helper()
	p := f.profiles[index]
	s, err := f.r.EgressDefaults().SetDefault(f.ctx, egressprofile.SetDefaultCommand{ExpectedRevision: revision, Profile: &egressprofile.ProfileRef{ID: p.ID, Revision: p.Revision}})
	if err != nil || s.Revision != revision+1 {
		f.t.Fatalf("set=%+v err=%v", s, err)
	}
}
func b4Want(proxy string, revision uint64) egressaudit.DecisionRef {
	w := egressaudit.BuiltInDirectDecision(egressaudit.AuthorityNetwork)
	w.ProxyID, w.ProxyRevision = proxy, 1
	if proxy == "profile.direct" {
		w.SelectionSource = egressprofile.SelectionBuiltin
		w.DefaultRevision = revision
		return w
	}
	w.PolicyID, w.PolicyRevision, w.RuleID = "runtime.egress-default", revision, "global.default"
	w.SelectionSource, w.DefaultRevision = egressprofile.SelectionGlobal, revision
	return w
}
func (f *b4Fixture) observe(name string, predicate func() bool) {
	f.t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if predicate() {
			return
		}
		select {
		case <-ticker.C:
		case <-f.ctx.Done():
			f.t.Fatalf("B4 observer %s timeout: gate=%+v", name, f.gate.Snapshot())
		}
	}
}
func (f *b4Fixture) entered(b *b4DialBarrier, jobs ...*b4Job) {
	f.t.Helper()
	var completed <-chan struct{}
	if len(jobs) > 0 {
		completed = jobs[0].done
	}
	select {
	case <-b.entered:
	case <-completed:
		connections, connectionErr := f.r.ConnectionEvents().List(f.ctx, connectionevent.PageRequest{Limit: 10})
		attempts, attemptErr := f.r.EgressAttempts().List(f.ctx, egressaudit.PageRequest{Limit: 10})
		f.n.mu.Lock()
		entries := fmt.Sprint(f.n.entries)
		physical := fmt.Sprint(f.n.physical)
		rejected := fmt.Sprint(f.n.rejected)
		f.n.mu.Unlock()
		f.t.Fatalf("B4 worker ended before barrier: result=%+v connections=%+v connectionErr=%v attempts=%+v attemptErr=%v gate=%+v dial keys=%s physical=%s rejected=%s DNS=%d unlisted=%d", jobs[0].result, connections, connectionErr, attempts, attemptErr, f.gate.Snapshot(), entries, physical, rejected, f.n.lookups.Load(), f.n.unlisted.Load())
	case <-f.ctx.Done():
		f.t.Fatal("B4 dial barrier timeout")
	}
}
func (f *b4Fixture) start(count int) egressaudit.Attempt {
	f.t.Helper()
	var own egressaudit.Attempt
	f.observe("durable start", func() bool {
		page, err := f.r.EgressAttempts().List(f.ctx, egressaudit.PageRequest{Limit: 10})
		if err != nil {
			f.t.Fatal(err)
		}
		if len(page.Items) != count {
			return false
		}
		for _, v := range page.Items {
			if !v.Attempt.Terminal() {
				if own.ID() != "" {
					f.t.Fatal("multiple nonterminal starts")
				}
				own = v.Attempt
			}
		}
		return own.ID() != ""
	})
	return own
}
func (f *b4Fixture) terminal(start egressaudit.Attempt, want egressaudit.DecisionRef, outcome egressaudit.Outcome, count int) {
	f.t.Helper()
	f.expectedAttempts = count
	var end egressaudit.Attempt
	f.observe("own durable terminal", func() bool {
		page, err := f.r.EgressAttempts().List(f.ctx, egressaudit.PageRequest{Limit: 10})
		if err != nil {
			f.t.Fatal(err)
		}
		if len(page.Items) != count {
			f.t.Fatalf("attempt count=%d want=%d", len(page.Items), count)
		}
		for _, v := range page.Items {
			if v.Attempt.ID() == start.ID() {
				end = v.Attempt
				return end.Terminal()
			}
		}
		return false
	})
	origin := "http://blind.fixture:80"
	if f.port == 443 {
		origin = "https://blind.fixture:443"
	}
	if start.Terminal() || start.ID() != end.ID() || start.StartedAt() != end.StartedAt() || start.Decision() != want || end.Decision() != want || start.Parent() != end.Parent() || start.ConnectionID() != end.ConnectionID() || start.Purpose() != end.Purpose() || start.PayloadClass() != end.PayloadClass() || start.Caller() != end.Caller() || start.CallerID() != end.CallerID() || start.TargetOrigin() != end.TargetOrigin() || end.TargetOrigin() != origin || end.Purpose() != egressaudit.PurposeBlindTunnel || end.PayloadClass() != egressaudit.PayloadOpaqueTunnel || end.Caller() != egressaudit.CallerCore || end.Parent().Kind != egressaudit.ParentBlindConnection || end.Parent().ID != end.ConnectionID() || end.Parent().ExchangeID != "" || end.Outcome() != outcome || want.AccountID != "" || want.AccountSettingsRevision != 0 {
		f.t.Fatalf("own attempt mutated start=%+v terminal=%+v want=%+v outcome=%s", start, end, want, outcome)
	}
	if outcome == egressaudit.OutcomeCompleted && (end.BytesOut() == 0 || end.BytesIn() == 0) {
		f.t.Fatalf("successful send lacks framing bytes: %+v", end)
	}
	if outcome != egressaudit.OutcomeCompleted && (end.BytesOut() != 0 || end.BytesIn() != 0) {
		f.t.Fatalf("failed pre-send has bytes: %+v", end)
	}
	f.observe("connection terminal", func() bool {
		timeline, err := f.r.ConnectionEvents().Timeline(f.ctx, end.ConnectionID())
		if err != nil {
			f.t.Fatal(err)
		}
		attempted, allowed, terminal := 0, 0, 0
		for _, event := range timeline.Events {
			if event.ConnectionID != end.ConnectionID() {
				f.t.Fatal("wrong journal ID")
			}
			if event.Phase == connectionevent.PhaseAttempted {
				if event.RequestedHost != "blind.fixture" || event.Port != f.port {
					f.t.Fatalf("wrong attempted target: %+v", event)
				}
				attempted++
				continue
			}
			if event.RequestedHost != "blind.fixture" || event.Port != f.port || event.RouteHost != "blind.fixture" || event.Decryption != connectionevent.DecryptionBlind {
				f.t.Fatalf("wrong journal target: %+v", event)
			}
			if event.Phase == connectionevent.PhaseDecided && event.Decision == connectionevent.DecisionAllow {
				allowed++
			}
			if event.Phase == connectionevent.PhaseClosed || event.Phase == connectionevent.PhaseFailed {
				terminal++
				if string(event.Outcome) != string(outcome) {
					f.t.Fatalf("journal outcome=%s want=%s", event.Outcome, outcome)
				}
			}
		}
		if terminal == 0 {
			return false
		}
		if attempted != 1 || allowed != 1 || terminal != 1 {
			f.t.Fatalf("own journal phases attempted/allowed/terminal=%d/%d/%d want=1/1/1", attempted, allowed, terminal)
		}
		return true
	})
	f.t.Logf("B4 outbound=%s connection/parent=%s expected=%+v start=%+v terminal=%+v outcome=%s bytes=%d/%d", end.ID(), end.ConnectionID(), want, start.Decision(), end.Decision(), end.Outcome(), end.BytesOut(), end.BytesIn())
}
func (f *b4Fixture) drained(online bool) {
	f.t.Helper()
	f.observe("gate drained", func() bool {
		s := f.gate.Snapshot()
		return s.QueuedRequests == 0 && s.ActiveActions == 0 && s.ActiveEgress == 0 && (!online || s.State == offlinehold.StateOnline)
	})
}

type b4Result struct {
	status int
	body   string
	err    error
}
type b4Job struct {
	done   chan struct{}
	result b4Result
	cancel context.CancelFunc
}

func (f *b4Fixture) job(work func(context.Context) b4Result) *b4Job {
	ctx, cancel := context.WithCancel(f.ctx)
	j := &b4Job{done: make(chan struct{}), cancel: cancel}
	f.mu.Lock()
	f.jobs = append(f.jobs, j.done)
	f.mu.Unlock()
	go func() { defer close(j.done); defer cancel(); j.result = work(ctx) }()
	return j
}
func (f *b4Fixture) finish(j *b4Job, status int) {
	f.t.Helper()
	select {
	case <-j.done:
	case <-f.ctx.Done():
		f.t.Fatal("B4 request unfinished")
	}
	if j.result.err != nil || j.result.status != status || (status == 200 && j.result.body != "b4-ok") {
		f.t.Fatalf("request=%+v want status=%d", j.result, status)
	}
}
func (f *b4Fixture) request(recorder bool) *b4Job {
	return f.job(func(ctx context.Context) b4Result {
		if recorder {
			r := httptest.NewRequest("POST", "http://blind.fixture:80/b4", strings.NewReader("b4-opaque-body"))
			r = r.WithContext(egressnetwork.WithTargetAccess(ctx, egressnetwork.TargetAccessPublicOnly))
			r.Header.Set("Proxy-Authorization", f.auth)
			r.Header.Set("Authorization", "Bearer client-b4")
			r.Header.Set("X-B4", "retained")
			r.Header.Set("Connection", "X-B4-Hop")
			r.Header.Set("X-B4-Hop", "stripped")
			r.Header.Set("Proxy-Connection", "close")
			w := httptest.NewRecorder()
			f.r.ProxyHandler().ServeHTTP(w, r)
			return b4Result{status: w.Code, body: w.Body.String()}
		}
		return f.wire(false)
	})
}
func (f *b4Fixture) wire(connect bool) b4Result {
	// Ingress has a separate exact listener allowlist; never use the egress map.
	address := f.server.Listener.Addr().String()
	c, err := net.DialTimeout("tcp4", address, time.Second)
	if err != nil {
		return b4Result{err: err}
	}
	f.mu.Lock()
	f.clients = append(f.clients, c)
	f.mu.Unlock()
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	text := "POST http://blind.fixture:80/b4 HTTP/1.1\r\nHost: blind.fixture:80\r\nProxy-Authorization: " + f.auth + "\r\nAuthorization: Bearer client-b4\r\nX-B4: retained\r\nProxy-Connection: close\r\nConnection: X-B4-Hop\r\nX-B4-Hop: stripped\r\nContent-Length: 14\r\n\r\nb4-opaque-body"
	if connect {
		text = "CONNECT blind.fixture:443 HTTP/1.1\r\nHost: blind.fixture:443\r\nProxy-Authorization: " + f.auth + "\r\n\r\n"
	}
	if _, err = io.WriteString(c, text); err != nil {
		return b4Result{err: err}
	}
	reader := bufio.NewReader(c)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		return b4Result{err: err}
	}
	if connect && response.StatusCode == 200 {
		opaque := "b4-opaque-body"
		_, err = io.WriteString(c, opaque)
		if err != nil {
			return b4Result{err: err}
		}
		body := make([]byte, len(opaque))
		_, err = io.ReadFull(reader, body)
		if err == nil && string(body) != opaque {
			err = errors.New("opaque echo mismatch")
		}
		return b4Result{status: 200, body: "b4-ok", err: err}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return b4Result{status: response.StatusCode, body: string(body), err: err}
}

type b4Budget struct {
	a, b, direct, acceptedA, acceptedB, probe, httpCount int
	lookup                                               int64
}

func (f *b4Fixture) budget(want b4Budget) {
	f.t.Helper()
	a, b, direct, acceptedA, acceptedB, probe, httpCount, lookup := want.a, want.b, want.direct, want.acceptedA, want.acceptedB, want.probe, want.httpCount, want.lookup
	ae, ap := f.n.counts(f.a.l.Addr().String())
	be, bp := f.n.counts(f.b.l.Addr().String())
	de, dp := f.n.counts(f.target)
	re, rp := f.n.counts(f.resolvedTarget)
	de += re
	dp += rp
	if ae != a || ap != a || be != b || bp != b || de != direct || dp != direct || f.a.accepted.Load() != int64(acceptedA) || f.b.accepted.Load() != int64(acceptedB) || f.a.onward.Load() != int64(acceptedA) || f.b.onward.Load() != int64(acceptedB) || f.origin.accepted.Load() != int64(acceptedA+acceptedB+direct) || f.probes.Load() != int64(probe) || f.requests.Load() != int64(httpCount) || f.n.unlisted.Load() != 0 || f.n.lookups.Load() != lookup {
		f.t.Fatalf("B4 budget A=%d/%d accept/onward=%d/%d B=%d/%d accept/onward=%d/%d Direct=%d/%d origin=%d probes=%d HTTP=%d DNS=%d unlisted=%d", ae, ap, f.a.accepted.Load(), f.a.onward.Load(), be, bp, f.b.accepted.Load(), f.b.onward.Load(), de, dp, f.origin.accepted.Load(), f.probes.Load(), f.requests.Load(), f.n.lookups.Load(), f.n.unlisted.Load())
	}
	if httpCount > 0 && f.bytes.Load() <= 0 {
		f.t.Fatal("origin did not observe application bytes")
	}
	f.t.Logf("B4 socket budget A=%d B=%d Direct=%d accepts=%d/%d opaque probes=%d HTTP=%d DNS=%d origin bytes=%d gate=%+v", a, b, direct, acceptedA, acceptedB, probe, httpCount, lookup, f.bytes.Load(), f.gate.Snapshot())
}
func (f *b4Fixture) hold() {
	f.t.Helper()
	if _, err := f.r.EnterOfflineHold(f.ctx, f.gate.Snapshot().Revision); err != nil {
		f.t.Fatal(err)
	}
}
func (f *b4Fixture) queued() {
	f.t.Helper()
	f.observe("queued one", func() bool { return f.gate.Snapshot().QueuedRequests == 1 })
	targets, err := f.r.resumeProbeTargets()
	want := offlinehold.ProbeTarget{Kind: offlinehold.EgressBlindTunnel, Transport: offlinehold.ProbeTransportTCP, TargetRef: "blind.fixture:80", NetworkOrigin: "blind.fixture:80", HTTPAuthority: "blind.fixture:80", EgressPolicy: f.profiles[0].Policy, TargetAccess: egressnetwork.TargetAccessOwnerConfigured}
	if err != nil || len(targets) != 1 || targets[0] != want {
		f.t.Fatalf("queued target=%+v want=%+v err=%v", targets, want, err)
	}
}

// Keep a real echo copy active when fixture cleanup closes its own socket.
// This forces the teardown ordering seen after retained-authority cancellation.
func TestBlindEchoFixtureShutdownJoinsActiveCopy(t *testing.T) {
	f := newB4Fixture(t, 443)
	conn, err := net.DialTimeout("tcp4", f.origin.l.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	const payload = "fixture-echo"
	if _, err := io.WriteString(conn, payload); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, reply); err != nil || string(reply) != payload {
		t.Fatalf("fixture echo=%q err=%v", reply, err)
	}
	f.origin.stop()
	b4Join(t, f.origin.done, "active origin echo")
	if got := f.bytes.Load(); got != int64(len(payload)) {
		t.Errorf("fixture echoed bytes=%d want=%d", got, len(payload))
	}
}

func TestActualRuntimeBlindBuiltinUnset(t *testing.T)     { runB4Unset(t, 443) }
func TestActualRuntimeCleartextBuiltinUnset(t *testing.T) { runB4Unset(t, 80) }
func runB4Unset(t *testing.T, port uint16) {
	f := newB4Fixture(t, port)
	want := b4Want("profile.direct", 3)
	wantSockets := b4Budget{direct: 1, lookup: 0}
	if port == 80 {
		wantSockets.httpCount = 1
	}
	f.set(0, 1)
	s, err := f.r.EgressDefaults().SetDefault(f.ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 2, Profile: nil})
	if err != nil || s.Revision != 3 || s.Profile != nil {
		t.Fatalf("unset=%+v err=%v", s, err)
	}
	s, err = f.r.EgressDefaults().GetDefault(f.ctx)
	if err != nil || s.Revision != 3 || s.Profile != nil {
		t.Fatalf("read unset=%+v err=%v", s, err)
	}
	barrier := f.n.block(f.target, 1)
	j := f.job(func(context.Context) b4Result { return f.wire(port == 443) })
	f.entered(barrier, j)
	start := f.start(1)
	barrier.open()
	f.finish(j, 200)
	f.terminal(start, want, egressaudit.OutcomeCompleted, 1)
	f.drained(true)
	if port == 443 {
		f.observe("opaque echo bytes", func() bool { return f.bytes.Load() == 14 })
	}
	f.budget(wantSockets)
}
func TestActualRuntimeCleartextProxyLoss(t *testing.T) {
	f := newB4Fixture(t, 80)
	want := b4Want("profile.b4-a", 2)
	wantSockets := b4Budget{a: 1, lookup: 1}
	f.set(0, 1)
	barrier := f.n.block(f.a.l.Addr().String(), 1)
	j := f.request(false)
	f.entered(barrier)
	start := f.start(1)
	f.a.stop()
	b4Join(t, f.a.done, "closed A listener")
	barrier.open()
	f.finish(j, 502)
	f.terminal(start, want, egressaudit.OutcomeFailed, 1)
	f.drained(true)
	f.budget(wantSockets)
}
func TestActualRuntimeCleartextHoldFrozenDefault(t *testing.T) {
	f := newB4Fixture(t, 80)
	wantA, wantB := b4Want("profile.b4-a", 2), b4Want("profile.b4-b", 3)
	wantFirst := b4Budget{a: 2, acceptedA: 2, probe: 1, httpCount: 1, lookup: 2}
	wantFinal := b4Budget{a: 2, b: 1, acceptedA: 2, acceptedB: 1, probe: 1, httpCount: 2, lookup: 3}
	f.a.limit.Store(2)
	f.b.limit.Store(1)
	f.set(0, 1)
	f.hold()
	j := f.request(false)
	f.queued()
	startA := f.start(1)
	f.set(1, 2)
	if _, err := f.r.ResumeOfflineHold(f.ctx, f.gate.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	f.finish(j, 200)
	f.terminal(startA, wantA, egressaudit.OutcomeCompleted, 1)
	f.drained(true)
	f.observe("opaque probe EOF", func() bool { return f.probes.Load() == 1 })
	f.budget(wantFirst)
	barrier := f.n.block(f.b.l.Addr().String(), 1)
	next := f.request(false)
	f.entered(barrier)
	startB := f.start(2)
	if startB.ID() == startA.ID() {
		t.Fatal("next request reused outbound ID")
	}
	barrier.open()
	f.finish(next, 200)
	f.terminal(startB, wantB, egressaudit.OutcomeCompleted, 2)
	f.drained(true)
	f.budget(wantFinal)
}
func TestActualRuntimeCleartextHoldCancellation(t *testing.T) {
	f := newB4Fixture(t, 80)
	want := b4Want("profile.b4-a", 2)
	wantSockets := b4Budget{}
	f.set(0, 1)
	f.hold()
	j := f.request(true)
	f.queued()
	start := f.start(1)
	f.set(1, 2)
	j.cancel()
	f.finish(j, 502)
	f.terminal(start, want, egressaudit.OutcomeCanceled, 1)
	f.drained(false)
	f.budget(wantSockets)
}
func TestActualRuntimeCleartextHoldFailureBoundaries(t *testing.T) {
	t.Run("probe", func(t *testing.T) {
		f := newB4Fixture(t, 80)
		want := b4Want("profile.b4-a", 2)
		wantSockets := b4Budget{a: 1, lookup: 1}
		f.set(0, 1)
		f.hold()
		j := f.request(true)
		f.queued()
		start := f.start(1)
		f.set(1, 2)
		f.a.stop()
		b4Join(t, f.a.done, "closed A listener")
		_, err := f.r.ResumeOfflineHold(f.ctx, f.gate.Snapshot().Revision)
		s := f.gate.Snapshot()
		if err == nil || s.State != offlinehold.StateHeld || s.QueuedRequests != 1 || s.LastProbeReason != offlinehold.ProbeReasonTransportUnavailable {
			t.Fatalf("probe failure err=%v gate=%+v", err, s)
		}
		f.budget(wantSockets)
		j.cancel()
		f.finish(j, 502)
		f.terminal(start, want, egressaudit.OutcomeCanceled, 1)
		f.drained(false)
		f.budget(wantSockets)
	})
	t.Run("released-dial", func(t *testing.T) {
		f := newB4Fixture(t, 80)
		want := b4Want("profile.b4-a", 2)
		wantSockets := b4Budget{a: 2, acceptedA: 1, probe: 1, lookup: 2}
		f.a.limit.Store(1)
		f.set(0, 1)
		f.hold()
		barrier := f.n.block(f.a.l.Addr().String(), 2)
		j := f.request(false)
		f.queued()
		start := f.start(1)
		f.set(1, 2)
		resume := f.job(func(ctx context.Context) b4Result {
			_, err := f.r.ResumeOfflineHold(ctx, f.gate.Snapshot().Revision)
			return b4Result{err: err}
		})
		f.entered(barrier)
		f.observe("successful opaque probe", func() bool { return f.probes.Load() == 1 })
		if f.requests.Load() != 0 {
			t.Fatal("probe sent application request")
		}
		f.a.stop()
		b4Join(t, f.a.done, "closed A after probe")
		barrier.open()
		f.finish(resume, 0)
		f.finish(j, 502)
		f.terminal(start, want, egressaudit.OutcomeFailed, 1)
		f.drained(true)
		f.budget(wantSockets)
	})
}
