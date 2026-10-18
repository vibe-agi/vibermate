package productruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/socksproxy"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

// runtimeUDP keeps opaque packets out of model/provider planning. Ingress
// reauthenticates the real capture credential on every send and reply.
type runtimeUDP struct {
	owner       context.Context
	activity    *captureActivityResolver
	policy      connectionpolicy.Source
	approvals   approvalRuntime
	defaults    egressprofile.Resolver
	gate        offlinehold.Coordinator
	network     *egressnetwork.Builder
	connections connectionEventRuntime
	audit       *runtimeEgressRepository
	clock       Clock
	mu          sync.Mutex
	closing     bool
	active      int
	changed     chan struct{}
}

// Admission owns each opening/established flow until its terminal worker
// finishes. Close only cancels transport; Runtime joins this owned drain.
func (u *runtimeUDP) begin(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closing {
		return nil, net.ErrClosed
	}
	u.active++
	var once sync.Once
	return func() {
		once.Do(func() {
			u.mu.Lock()
			u.active--
			close(u.changed)
			u.changed = make(chan struct{})
			u.mu.Unlock()
		})
	}, nil
}
func (u *runtimeUDP) BeginShutdown() { u.mu.Lock(); u.closing = true; u.mu.Unlock() }
func (u *runtimeUDP) Drain(ctx context.Context) error {
	u.BeginShutdown()
	for {
		u.mu.Lock()
		active, changed := u.active, u.changed
		u.mu.Unlock()
		if active == 0 {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
func (u *runtimeUDP) Shutdown(ctx context.Context) error { return u.Drain(ctx) }

func (r *Runtime) SOCKSProxy() *socksproxy.Server { return r.socks }

func (u *runtimeUDP) OpenUDP(ctx context.Context, admission captureadmission.Admission, target socksproxy.Target) (opened socksproxy.OpenedUDP, err error) {
	if ctx == nil || admission.Validate() != nil || target.Port == 0 {
		return opened, captureadmission.ErrInvalidAdmission
	}
	done, err := u.begin(ctx)
	if err != nil {
		return opened, err
	}
	var connection *connectionevent.Connection
	var record egressaudit.Attempt
	var action *offlinehold.ActionLease
	var lease offlinehold.Lease
	retained, finished, appended := false, false, false
	defer func() {
		if retained {
			return
		}
		if connection == nil {
			done()
			return
		}
		go func() {
			defer done()
			if action != nil {
				defer action.Release()
			}
			if lease != nil {
				defer lease.Release()
			}
			completion, cancel := u.audit.completionContext()
			defer cancel()
			if appended {
				u.finishAudit(completion, record, err, 0, 0)
			}
			if !finished {
				u.finishConnection(completion, connection, err, 0, 0)
			}
		}()
	}()
	capture, err := admission.CaptureReference()
	if err != nil {
		return opened, err
	}
	active, err := u.activity.Active(ctx, capture)
	if err != nil || !active {
		return opened, captureadmission.ErrCredentialRejected
	}
	host, err := egressnetwork.CanonicalHost(target.Host)
	if err != nil {
		return opened, err
	}
	source := connectionevent.Source{IngressID: admission.AdmissionRef(), Label: admission.SourceLabel(), Confidence: connectionevent.SourceConfidenceConfigured}
	if admission.AttributionConfidence() == captureadmission.AttributionVerified {
		source.Confidence = connectionevent.SourceConfidenceVerified
	}
	connection, err = u.connections.Start(ctx, connectionevent.Attempt{Source: source, RequestedHost: host, Port: target.Port})
	if err != nil {
		return opened, err
	}
	_ = connection.BindObservationScope(connection.OriginalObservationScope(admission.RecordingScope()))
	outcome := u.policy.Current().Evaluate(connectionpolicy.Request{Host: host, Port: target.Port})
	if outcome.Decision == connectionpolicy.DecisionAsk {
		if err = connection.Decide(ctx, connectionevent.DecisionEvidence{Source: source, Decision: connectionevent.DecisionAsk, RuleID: outcome.RuleID, Decryption: connectionevent.DecryptionNone}); err != nil {
			return opened, err
		}
		answer, askErr := u.approvals.AskNetwork(ctx, toolapproval.NetworkAskRequest{IngressID: admission.AdmissionRef(), Host: host, Port: target.Port})
		outcome.Decision = connectionpolicy.DecisionDeny
		if askErr == nil && answer.Allowed {
			outcome.Decision = connectionpolicy.DecisionAllow
		}
	}
	if outcome.Decision != connectionpolicy.DecisionAllow {
		err = connection.Decide(ctx, connectionevent.DecisionEvidence{Source: source, Decision: connectionevent.DecisionDeny, RuleID: outcome.RuleID, Decryption: connectionevent.DecryptionNone, ErrorClass: "connection_denied"})
		if err == nil {
			finished = true
			err = errors.New("datagram connection denied")
		}
		return opened, err
	}
	if outcome.Explicit {
		ctx = egressnetwork.WithTargetAccess(ctx, egressnetwork.TargetAccessOwnerConfigured)
	}
	resolved, err := u.defaults.Resolve(ctx, egressprofile.Inherit(), egressprofile.Inherit())
	if err != nil {
		return opened, err
	}
	if err = resolved.Validate(); err != nil {
		return opened, err
	}
	if resolved.Source() != egressprofile.SelectionGlobal && resolved.Source() != egressprofile.SelectionBuiltin {
		return opened, egressprofile.ErrDefaultUnavailable
	}
	if err = connection.Decide(ctx, connectionevent.DecisionEvidence{Source: source, Decision: connectionevent.DecisionAllow, RuleID: outcome.RuleID, RouteHost: host, Decryption: connectionevent.DecryptionBlind, EgressScope: connectionevent.EgressScopeNetwork, EgressSource: connectionevent.EgressSourceNetworkDefault, EgressPolicyRevision: outcome.Revision, EgressProxyID: resolved.Profile().ID.String()}); err != nil {
		return opened, err
	}
	var idBytes [20]byte
	if _, err = rand.Read(idBytes[:]); err != nil {
		return opened, err
	}
	id := hex.EncodeToString(idBytes[:])
	action, err = u.gate.BeginAction(ctx, offlinehold.ActionRequest{ActionID: id})
	if err != nil {
		return opened, err
	}
	decision := egressaudit.BuiltInDirectDecision(egressaudit.AuthorityNetwork)
	decision.SelectionSource, decision.DefaultRevision = resolved.Source(), resolved.DefaultRevision()
	if resolved.Source() == egressprofile.SelectionGlobal {
		decision.PolicyID, decision.PolicyRevision, decision.RuleID = "runtime.egress-default", resolved.DefaultRevision(), "global.default"
	}
	decision.ProxyID, decision.ProxyRevision = resolved.Profile().ID.String(), uint64(resolved.Profile().Revision)
	authority := net.JoinHostPort(host, strconv.Itoa(int(target.Port)))
	record, err = egressaudit.New(egressaudit.NewInput{ObservationStamp: connection.ObservationStamp(), ID: id, ConnectionID: connection.ID(), Purpose: egressaudit.PurposeDatagram, PayloadClass: egressaudit.PayloadOpaqueDatagram, Parent: egressaudit.ParentRef{Kind: egressaudit.ParentDatagramConnection, ID: connection.ID()}, Caller: egressaudit.CallerCore, TargetOrigin: "udp://" + authority, Decision: decision, StartedAt: u.clock.Now()})
	if err != nil {
		u.audit.ReportTerminalFailure(err)
		err = nil
	} else {
		u.audit.ObserveEgress(record.ObservationStamp(), record)
	}
	appended = record.ID() != ""
	probe := offlinehold.ProbeTarget{Kind: offlinehold.EgressDatagram, Transport: offlinehold.ProbeTransportUDPSetup, TargetRef: authority, NetworkOrigin: authority, EgressPolicy: resolved.Profile().Policy, TargetAccess: egressnetwork.EffectiveTargetAccess(ctx)}
	lease, err = u.gate.Acquire(ctx, offlinehold.AcquireRequest{RequestID: id, Action: action, Target: probe})
	if err != nil {
		return opened, err
	}
	active, err = u.activity.Active(ctx, capture)
	if err != nil || !active {
		err = captureadmission.ErrCredentialRejected
		return opened, err
	}
	// Setup is bounded; an established packet flow keeps the association context.
	setup, cancel := context.WithTimeout(ctx, 15*time.Second)
	transport, peer, err := u.network.OpenUDP(setup, resolved.Profile().Policy, host, target.Port)
	cancel()
	if err != nil {
		return opened, err
	}
	if err = connection.Connected(ctx, connectionevent.ConnectedEvidence{}); err != nil {
		transport.Close()
		return opened, err
	}
	flow := &runtimeDatagram{transport: transport, action: action, lease: lease, connection: connection, record: record, runtime: u, ctx: ctx, drained: done}
	retained = true
	finished = true
	stop := context.AfterFunc(ctx, func() { flow.Close() })
	flow.mu.Lock()
	if flow.closed {
		stop()
	} else {
		flow.stop = stop
	}
	flow.mu.Unlock()
	return socksproxy.OpenedUDP{Flow: flow, Peer: peer}, nil
}

type runtimeDatagram struct {
	transport           egressnetwork.DatagramConn
	action              *offlinehold.ActionLease
	lease               offlinehold.Lease
	connection          *connectionevent.Connection
	record              egressaudit.Attempt
	runtime             *runtimeUDP
	ctx                 context.Context
	stop                func() bool
	once                sync.Once
	readMu, writeMu, mu sync.Mutex
	out, in             int64
	failure             error
	closed              bool
	drained             func()
}

func (f *runtimeDatagram) ReadPacket(ctx context.Context, b []byte) (int, error) {
	f.readMu.Lock()
	n, err := f.transport.ReadPacket(ctx, b)
	f.mu.Lock()
	if err == nil {
		f.in += int64(n)
	} else if f.failure == nil {
		f.failure = err
	}
	f.mu.Unlock()
	f.readMu.Unlock()
	if err != nil {
		f.Close()
	}
	return n, err
}
func (f *runtimeDatagram) WritePacket(ctx context.Context, p []byte) error {
	f.writeMu.Lock()
	err := f.transport.WritePacket(ctx, p)
	f.mu.Lock()
	if err == nil {
		f.out += int64(len(p))
	} else if f.failure == nil {
		f.failure = err
	}
	f.mu.Unlock()
	f.writeMu.Unlock()
	if err != nil {
		f.Close()
	}
	return err
}
func (f *runtimeDatagram) Close() error {
	f.once.Do(func() {
		f.mu.Lock()
		f.closed = true
		stop := f.stop
		f.mu.Unlock()
		if stop != nil {
			stop()
		}
		f.transport.Close()
		go f.finalize()
	})
	return nil
}
func (f *runtimeDatagram) finalize() {
	defer f.drained()
	defer f.action.Release()
	defer f.lease.Release()
	f.readMu.Lock()
	f.writeMu.Lock()
	f.mu.Lock()
	cause, out, in := f.failure, f.out, f.in
	f.mu.Unlock()
	f.writeMu.Unlock()
	f.readMu.Unlock()
	if f.ctx.Err() != nil {
		cause = f.ctx.Err()
	} else if cause == nil {
		cause = net.ErrClosed
	}
	completion, cancel := f.runtime.audit.completionContext()
	defer cancel()
	f.runtime.finishAudit(completion, f.record, cause, out, in)
	f.runtime.finishConnection(completion, f.connection, cause, out, in)
}
func udpTerminal(err error) (egressaudit.Outcome, connectionevent.Outcome, string) {
	if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
		return egressaudit.OutcomeCanceled, connectionevent.OutcomeCanceled, "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return egressaudit.OutcomeFailed, connectionevent.OutcomeFailed, "deadline"
	}
	if errors.Is(err, egressnetwork.ErrPrivateTarget) {
		return egressaudit.OutcomeFailed, connectionevent.OutcomeFailed, "private_destination_denied"
	}
	return egressaudit.OutcomeFailed, connectionevent.OutcomeFailed, "datagram_failed"
}
func (u *runtimeUDP) finishAudit(ctx context.Context, record egressaudit.Attempt, err error, out, in int64) {
	outcome, _, class := udpTerminal(err)
	now := u.clock.Now()
	if now.Before(record.StartedAt()) {
		now = record.StartedAt()
	}
	terminal, e := record.Finish(egressaudit.TerminalInput{Outcome: outcome, ErrorClass: class, BytesOut: out, BytesIn: in, CompletedAt: now})
	if e != nil {
		u.audit.ReportTerminalFailure(e)
		return
	}
	u.audit.ObserveEgress(terminal.ObservationStamp(), terminal)
}
func (u *runtimeUDP) finishConnection(ctx context.Context, c *connectionevent.Connection, err error, out, in int64) {
	_, outcome, class := udpTerminal(err)
	if e := c.Finish(ctx, connectionevent.TerminalEvidence{Outcome: outcome, ErrorClass: class, BytesUp: uint64(out), BytesDown: uint64(in)}); e != nil {
		u.audit.ReportTerminalFailure(e)
	}
}

// udpSetupProber sends no invented payload to an arbitrary UDP service. It
// proves resolver/admission and local/upstream channel construction only.
type udpSetupProber struct{ network *egressnetwork.Builder }

func (p udpSetupProber) Probe(ctx context.Context, request offlinehold.ProbeRequest) error {
	if ctx == nil || len(request.Targets) == 0 {
		return offlinehold.ErrInvalidRequest
	}
	for _, t := range request.Targets {
		if err := t.Validate(); err != nil {
			return err
		}
		if t.Kind != offlinehold.EgressDatagram || t.Transport != offlinehold.ProbeTransportUDPSetup || t.TargetRef != t.NetworkOrigin {
			return offlinehold.ErrInvalidRequest
		}
		host, port, err := net.SplitHostPort(t.NetworkOrigin)
		if err != nil {
			return err
		}
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return offlinehold.ErrInvalidRequest
		}
		setup, cancel := context.WithTimeout(egressnetwork.WithTargetAccess(ctx, t.TargetAccess), 15*time.Second)
		c, _, err := p.network.OpenUDP(setup, t.EgressPolicy, host, uint16(n))
		cancel()
		if err != nil {
			return offlinehold.NewProbeFailure(offlinehold.ProbeReasonTransportUnavailable, err)
		}
		c.Close()
	}
	return nil
}
