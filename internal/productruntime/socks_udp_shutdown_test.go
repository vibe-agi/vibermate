package productruntime

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/recording"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

type udpTerminalWrite struct {
	ctx   context.Context
	event connectionevent.Event
}
type blockedUDPTerminals struct {
	connectionevent.Reader
	legacy  connectionevent.Repository
	scoped  connectionevent.ScopedStore
	entered chan udpTerminalWrite
	release chan struct{}
	once    sync.Once
}

func (b *blockedUDPTerminals) unblock() { b.once.Do(func() { close(b.release) }) }
func (b *blockedUDPTerminals) Append(ctx context.Context, event connectionevent.Event) (connectionevent.Record, error) {
	if event.Phase == connectionevent.PhaseClosed || event.Phase == connectionevent.PhaseFailed {
		b.entered <- udpTerminalWrite{ctx: ctx, event: event}
		select {
		case <-ctx.Done():
			return connectionevent.Record{}, ctx.Err()
		case <-b.release:
		}
	}
	return b.legacy.Append(ctx, event)
}

type observedDirectUDP struct {
	mu      sync.Mutex
	sockets []*net.UDPConn
}

func (n *observedDirectUDP) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	c, e := (&net.Dialer{}).DialContext(ctx, network, address)
	if e == nil {
		if u, ok := c.(*net.UDPConn); ok {
			n.mu.Lock()
			n.sockets = append(n.sockets, u)
			n.mu.Unlock()
		}
	}
	return c, e
}
func (n *observedDirectUDP) requireClosed(t *testing.T) {
	t.Helper()
	n.mu.Lock()
	s := append([]*net.UDPConn(nil), n.sockets...)
	n.mu.Unlock()
	if len(s) != 2 {
		t.Fatal("missing real packet transports", len(s))
	}
	for _, c := range s {
		if _, e := c.Write([]byte{1}); !errors.Is(e, net.ErrClosed) {
			t.Error("later Runtime datagram transport remained open", e)
		}
	}
}

type udpShutdownFixture struct {
	runtime    *Runtime
	repository *blockedUDPTerminals
	network    *observedDirectUDP
	controls   []net.Conn
	closed     []chan error
	capture    captureidentity.Reference
	budget     time.Duration
	revocation *nativeRevocation
}

func newUDPShutdownFixture(t *testing.T, recorders ...observationRecorderBuilder) *udpShutdownFixture {
	t.Helper()
	gate, e := offlinehold.New(offlinehold.DefaultConfig())
	if e != nil {
		t.Fatal(e)
	}
	network := &observedDirectUDP{}
	options := testOptions(t, hostcontract.Desktop(), gate)
	options.Lifecycle.ShutdownTimeout = 200 * time.Millisecond
	options.NetworkOptions = egressnetwork.BuilderOptions{BaseDialer: network}
	block := &blockedUDPTerminals{entered: make(chan udpTerminalWrite, 4), release: make(chan struct{})}
	var revocation *nativeRevocation
	var recorder observationRecorderBuilder = func(ctx context.Context, b *recording.Budget, c connectionevent.ScopedStore, e egressaudit.ScopedAttemptStore) *observationRecorder {
		block.scoped = c
		return newObservationRecorder(ctx, b, block, e)
	}
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	r, e := startWithBuilders(t.Context(), options, runtimeBuildOverrides{recorder: recorder, environment: func(ctx context.Context, request environmentBuildRequest) (environmentBuildResult, error) {
		revocation = request.nativeRevocation.(*nativeRevocation)
		return buildEnvironment(ctx, request)
	}, connectionEvent: func(ctx context.Context, request connectionEventBuildRequest) (connectionEventRuntime, error) {
		block.Reader = request.repository
		request.repository = block
		return buildConnectionEvent(ctx, request)
	}})
	if e != nil {
		t.Fatal(e)
	}
	if len(recorders) == 0 {
		waitRecording(t, r)
	}
	// Task4 installs the lazy optional facade after constructing the queue.
	// This held-writer fixture delegates to that actual scoped sink.
	if block.scoped == nil {
		block.scoped = r.recordingStorage
	}
	t.Cleanup(func() {
		block.unblock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		r.Shutdown(ctx)
	})
	peer := runtimeUDPEcho(t)
	_, e = r.ConnectionRules().Replace(t.Context(), r.ConnectionRules().Current().Revision, []connectionpolicy.Rule{{ID: "owned.shutdown-udp", Priority: 1, Decision: connectionpolicy.DecisionAllow, Match: connectionpolicy.MatchExactHostPort("127.0.0.1", peer.Port())}}, connectionpolicy.ModeDenyUnknown)
	if e != nil {
		t.Fatal(e)
	}
	grant, e := r.ManualCaptures().Create(t.Context(), manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "owned lifecycle capture", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	if e != nil {
		t.Fatal(e)
	}
	if len(recorders) == 0 {
		waitRecordingMirror(t, r)
	}
	capture, _ := captureidentity.New(captureidentity.KindManualCapture, grant.Capture.ID)
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	served := make(chan error, 1)
	go func() { served <- r.SOCKSProxy().Serve(listener) }()
	t.Cleanup(func() {
		listener.Close()
		select {
		case <-served:
		case <-time.After(time.Second):
			t.Error("listener owner did not join")
		}
	})
	f := &udpShutdownFixture{runtime: r, repository: block, network: network, capture: capture, budget: options.Lifecycle.ShutdownTimeout, revocation: revocation}
	for range 2 {
		c, relay, u := runtimeUDPAssociate(t, listener.Addr().String(), grant.Credential.Value())
		packet := append(append([]byte{0, 0, 0}, runtimeUDPWire(peer)...), []byte{0, 255, 128, 9}...)
		runtimeUDPSend(t, u, relay, packet)
		runtimeUDPReceive(t, u, relay, peer, []byte{0, 255, 128, 9})
		f.controls = append(f.controls, c)
		closed := make(chan error, 1)
		f.closed = append(f.closed, closed)
		go func() { var p [1]byte; _, e := c.Read(p[:]); closed <- e }()
	}
	return f
}
func (f *udpShutdownFixture) firstTerminal(t *testing.T) udpTerminalWrite {
	t.Helper()
	select {
	case v := <-f.repository.entered:
		return v
	case <-time.After(time.Second):
		t.Fatal("terminal storage seam was not reached")
	}
	return udpTerminalWrite{}
}
func (f *udpShutdownFixture) requireControlsClosed(t *testing.T) {
	t.Helper()
	watchdog := time.NewTimer(f.budget)
	defer watchdog.Stop()
	for _, closed := range f.closed {
		select {
		case e := <-closed:
			if !errors.Is(e, io.EOF) && !errors.Is(e, net.ErrClosed) {
				t.Error("control close", e)
			}
		case <-watchdog.C:
			t.Error("later authenticated Runtime UDP control stayed open behind terminal persistence")
			return
		}
	}
}

// A per-flow uncancelable terminal budget or synchronous durable Close
// keeps the second real authenticated session alive behind the first append.
func TestRuntimeUDPShutdownCancelsAllSessionsBeforeSharedTerminalBudget(t *testing.T) {
	f := newUDPShutdownFixture(t)
	f.controls[0].Close()
	f.firstTerminal(t)
	done := make(chan error, 1)
	go func() { done <- f.runtime.Shutdown(context.Background()) }()
	f.requireControlsClosed(t)
	f.network.requireClosed(t)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * f.budget):
		t.Fatal("shutdown did not cancel optional writer")
	}
	if f.runtime.offlineHold.Snapshot().ActiveActions != 0 {
		t.Fatal("shutdown retained packet leases")
	}
}

func TestExpiredUDPRevokeClosesAllSessionsBeforeWaitingOnPersistence(t *testing.T) {
	f := newUDPShutdownFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan error, 1)
	go func() { done <- f.runtime.SOCKSProxy().Revoke(ctx, f.capture, "capture_stopped") }()
	f.firstTerminal(t)
	f.requireControlsClosed(t)
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Error("expired Revoke did not return its wait error", e)
		}
	case <-time.After(f.budget):
		t.Error("expired Revoke blocked before its bounded wait")
		f.repository.unblock()
		<-done
	}
	f.network.requireClosed(t)
	f.repository.unblock()
	if t.Failed() {
		return
	}
	f.requireSuccessfulTerminals(t)
}

func TestShortNativeRevocationCancelsUDPPromptlyWithOwnedTerminalDrain(t *testing.T) {
	f := newUDPShutdownFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.revocation.Revoke(ctx, f.capture, string(nativemcp.RevocationCaptureStopped)) }()
	f.firstTerminal(t)
	f.requireControlsClosed(t)
	select {
	case e := <-done:
		if e != nil && !errors.Is(e, context.DeadlineExceeded) {
			t.Error("native/SOCKS fanout", e)
		}
	case <-time.After(f.budget):
		t.Error("short native fanout waited for UDP persistence")
	}
	f.network.requireClosed(t)
	f.repository.unblock()
	if t.Failed() {
		return
	}
	f.requireSuccessfulTerminals(t)
}

func TestNormalUDPControlCloseJoinsTransportBeforeOptionalHistory(t *testing.T) {
	f := newUDPShutdownFixture(t)
	for _, c := range f.controls {
		c.Close()
	}
	f.firstTerminal(t)
	if err := f.runtime.udp.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.network.requireClosed(t)
	if f.runtime.recordingBudget.Snapshot().ReservedRecords == 0 {
		t.Fatal("blocked worker lost recording ownership")
	}
	f.repository.unblock()
	f.requireSuccessfulTerminals(t)
}

func (f *udpShutdownFixture) requireSuccessfulTerminals(t *testing.T) {
	t.Helper()
	runtimeUDPWait(t, func() bool { return f.runtime.offlineHold.Snapshot().ActiveActions == 0 })
	if err := f.runtime.recorder.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	page, e := f.runtime.egress.List(t.Context(), egressaudit.PageRequest{Purpose: egressaudit.PurposeDatagram, Limit: 20})
	if e != nil || len(page.Items) != 2 {
		t.Fatal("once-only packet audit count", e, len(page.Items))
	}
	for _, item := range page.Items {
		a := item.Attempt
		if !a.Terminal() || a.BytesOut() != 4 || a.BytesIn() != 4 || a.Outcome() != egressaudit.OutcomeCanceled {
			t.Fatal("revoke lost exact packet terminal", egressaudit.ViewOf(item))
		}
	}
	events, e := f.runtime.connections.List(t.Context(), connectionevent.PageRequest{Limit: 20, LatestPerConnection: true})
	if e != nil || len(events.Items) != 2 {
		t.Fatal("connection terminal count", e)
	}
	for _, v := range events.Items {
		if v.Outcome != connectionevent.OutcomeCanceled || v.BytesUp != 4 || v.BytesDown != 4 {
			t.Fatal("connection terminal bytes/once-only close lost")
		}
	}
}

func (b *blockedUDPTerminals) PutConnection(ctx context.Context, scope recording.Scope, e connectionevent.Event) error {
	if e.Phase == connectionevent.PhaseClosed || e.Phase == connectionevent.PhaseFailed {
		b.entered <- udpTerminalWrite{ctx: ctx, event: e}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-b.release:
		}
	}
	return b.scoped.PutConnection(ctx, scope, e)
}
