package exchange

import (
	"context"
	"errors"
	"math"
	"sync"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

// ResourcePolicy is an internal candidate policy, copied at construction.
// ResponseBytes is independent of RequestBytes; neither may borrow the other.
// Nil candidate wiring retains legacy behavior until the activation gate.
type ResourcePolicy struct {
	Content                                             exchangecontent.SourceLimits
	RequestBytes, ResponseBytes, SlotBytes, ActiveBytes uint64
}

func (p ResourcePolicy) Validate() error {
	if err := p.Content.Validate(); err != nil {
		return err
	}
	if p.RequestBytes == 0 || p.ResponseBytes == 0 || p.SlotBytes > math.MaxInt64 || p.ActiveBytes > math.MaxInt64 || p.RequestBytes > p.SlotBytes || p.ResponseBytes > p.SlotBytes-p.RequestBytes || p.ActiveBytes < p.SlotBytes {
		return errors.New("invalid finite execution resource policy")
	}
	reserve, scratch, err := RequiredResponseReservation(p.Content.Semantic)
	if err != nil {
		return err
	}
	if reserve.MaxPhysicalSlots > 16384 {
		return errors.New("response reservation cannot prove manifest slot feasibility")
	}
	if p.Content.RetainedBytes <= reserve.RetainedBytes || p.Content.CanonicalBytes <= reserve.CanonicalBytes || p.Content.StructureBytes <= reserve.StructureBytes || p.Content.Scratch.PayloadBytes < scratch.PayloadBytes || p.Content.Scratch.StructureBytes < scratch.StructureBytes {
		return errors.New("content policy lacks independent response reservation")
	}
	request, response, err := RequiredExecutionEnvelope(p.Content)
	if err != nil {
		return err
	}
	if p.RequestBytes < request || p.ResponseBytes < response {
		return errors.New("execution slot is smaller than its owning phases")
	}
	return nil
}

// BodyAdmission has one shared wake channel and no per-waiter state.
type BodyAdmission struct {
	mu      sync.Mutex
	policy  ResourcePolicy
	used    uint64
	closing bool
	changed chan struct{}
}

func NewBodyAdmission(policy ResourcePolicy) (*BodyAdmission, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &BodyAdmission{policy: policy, changed: make(chan struct{})}, nil
}
func (g *BodyAdmission) Policy() ResourcePolicy { return g.policy }

// CheckPlan performs no body work. The fixed slot must already accommodate
// this actual chain; callers never wait for an incremental slot upgrade.
func (g *BodyAdmission) CheckPlan(plan environment.RequestPlan) error {
	if g == nil {
		return errors.New("body admission gate missing")
	}
	retained, err := plan.TransformPipeline().RetainedRequestBytes()
	if err != nil {
		return err
	}
	base, _, err := RequiredExecutionEnvelope(g.policy.Content)
	if err != nil {
		return err
	}
	if retained > g.policy.RequestBytes-base {
		return errors.New("transform retention exceeds the fixed request phase")
	}
	return nil
}
func (g *BodyAdmission) Acquire(ctx context.Context) (*BodyLease, error) {
	if g == nil || ctx == nil {
		return nil, errors.New("body admission context or gate missing")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		g.mu.Lock()
		if g.changed == nil {
			g.mu.Unlock()
			return nil, errors.New("body admission gate is uninitialized")
		}
		if g.closing {
			g.mu.Unlock()
			return nil, ErrRuntimeStopping
		}
		if g.policy.SlotBytes <= g.policy.ActiveBytes-g.used {
			g.used += g.policy.SlotBytes
			g.mu.Unlock()
			return &BodyLease{state: &bodyLeaseState{gate: g}}, nil
		}
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
func (g *BodyAdmission) notify()        { close(g.changed); g.changed = make(chan struct{}) }
func (g *BodyAdmission) BeginShutdown() { g.mu.Lock(); g.closing = true; g.notify(); g.mu.Unlock() }
func (g *BodyAdmission) Drain(ctx context.Context) error {
	if ctx == nil {
		return errors.New("body admission drain context missing")
	}
	g.mu.Lock()
	for g.used != 0 {
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
		g.mu.Lock()
	}
	g.mu.Unlock()
	return nil
}

// BodyLease keeps the caller's slot through its entire cleanup scope. A release
// request cannot refund an execution that is still observing final evidence.
type BodyLease struct {
	state *bodyLeaseState
}

// Copies of the exported wrapper share this closed state. Copying a lease
// value can neither mint another execution nor refund its slot twice.
type bodyLeaseState struct {
	gate                                                *BodyAdmission
	constructed, claimed, executing, released, refunded bool
	requestUsed                                         uint64
}

func (l *BodyLease) checkPlan(plan environment.RequestPlan) error {
	if l == nil || l.state == nil {
		return errors.New("body lease missing")
	}
	return l.state.gate.CheckPlan(plan)
}
func (l *BodyLease) reserveConstructor(n uint64) error {
	if l == nil || l.state == nil || l.state.gate == nil {
		return errors.New("body lease missing")
	}
	s := l.state
	g := s.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	if s.constructed || s.claimed || s.released || n > g.policy.RequestBytes {
		return errors.New("body lease cannot construct request")
	}
	s.constructed = true
	s.requestUsed = n
	return nil
}
func (l *BodyLease) claim(g *BodyAdmission) error {
	if l == nil || l.state == nil || g == nil || l.state.gate != g {
		return errors.New("foreign or missing body lease")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	s := l.state
	if !s.constructed || s.claimed || s.released {
		return errors.New("body lease already used or released")
	}
	s.claimed = true
	s.executing = true
	return nil
}
func (l *BodyLease) finish() {
	s := l.state
	g := s.gate
	g.mu.Lock()
	s.executing = false
	s.refund()
	g.mu.Unlock()
}
func (l *BodyLease) Release() {
	if l == nil || l.state == nil || l.state.gate == nil {
		return
	}
	s := l.state
	g := s.gate
	g.mu.Lock()
	s.released = true
	s.refund()
	g.mu.Unlock()
}
func (l *bodyLeaseState) refund() {
	if l.released && !l.executing && !l.refunded {
		l.refunded = true
		l.gate.used -= l.gate.policy.SlotBytes
		l.gate.notify()
	}
}
