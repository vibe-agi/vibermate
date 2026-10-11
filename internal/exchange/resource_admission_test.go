package exchange

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func admissionTestPolicy(slots uint64) ResourcePolicy {
	content := exchangecontent.SourceLimits{Semantic: protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 1 << 20, StructureBytes: 1 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 1 << 20, StructureBytes: 1 << 20}}, CanonicalBytes: 1 << 20, RetainedBytes: 1 << 20, StructureBytes: 1 << 20}
	reserve, scratch, err := RequiredResponseReservation(content.Semantic)
	if err != nil {
		panic(err)
	}
	content.RetainedBytes += reserve.RetainedBytes
	content.CanonicalBytes += reserve.CanonicalBytes
	content.StructureBytes += reserve.StructureBytes
	content.Scratch = scratch
	request, response, err := RequiredExecutionEnvelope(content)
	if err != nil {
		panic(err)
	}
	return ResourcePolicy{Content: content, RequestBytes: request, ResponseBytes: response, SlotBytes: request + response, ActiveBytes: slots * (request + response)}
}

type admissionWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *admissionWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestBodyAdmissionMixedPlansReserveAtomicallyAndRefundActualCredit(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{destination: environment.DestinationKindOriginal, backend: protocolspec.DialectAnthropicMessages, transform: messagetransform.Policy{RequestJavaScript: `request.headers["x-stage"]="one";`}})
	other := mustEnvironmentRequestPlan(t, testPlanOptions{destination: environment.DestinationKindOriginal, backend: protocolspec.DialectAnthropicMessages, transform: messagetransform.Policy{RequestJavaScript: `request.headers["x-stage"]="other";`}})
	retained, err := plan.TransformPipeline().RetainedRequestBytes()
	if err != nil {
		t.Fatal(err)
	}
	p := admissionTestPolicy(2)
	// Exactly one base and one transformed owner, including both independent responses.
	p.ActiveBytes += retained
	gate, err := NewBodyAdmission(p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	base, err := gate.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Release()
	if err := base.checkPlan(plan); err == nil {
		t.Fatal("no-plan lease silently reused for a transform")
	}
	large, err := gate.AcquirePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	defer large.Release()
	if err := large.checkPlan(other); err == nil {
		t.Fatal("different frozen plan accepted")
	}
	if err := large.reserveConstructor(1); err != nil {
		t.Fatal(err)
	}
	if err := large.claim(gate); err != nil {
		t.Fatal(err)
	}
	copy := *large
	copy.Release()
	assertUsed := func(want uint64) {
		t.Helper()
		gate.mu.Lock()
		got := gate.used
		gate.mu.Unlock()
		if got != want {
			t.Fatalf("charged=%d want=%d", got, want)
		}
	}
	assertUsed(p.ActiveBytes)
	waiter, stop := context.WithCancel(ctx)
	observed := &admissionWaitContext{Context: waiter, waiting: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		lease, err := gate.AcquirePlan(observed, plan)
		if lease != nil {
			lease.Release()
		}
		done <- err
	}()
	select {
	case <-observed.waiting:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	assertUsed(p.ActiveBytes)
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	assertUsed(p.ActiveBytes)
	base.Release()
	assertUsed(p.SlotBytes + retained)
	// Releasing only the base cannot admit the larger waiter: no partial debit.
	observed = &admissionWaitContext{Context: ctx, waiting: make(chan struct{})}
	acquired := make(chan *BodyLease, 1)
	go func() {
		lease, err := gate.AcquirePlan(observed, plan)
		if err != nil {
			done <- err
			return
		}
		acquired <- lease
	}()
	select {
	case <-observed.waiting:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	assertUsed(p.SlotBytes + retained)
	large.finish()
	var next *BodyLease
	select {
	case next = <-acquired:
	case err := <-done:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	assertUsed(p.SlotBytes + retained)
	copy.Release()
	large.Release()
	assertUsed(p.SlotBytes + retained)
	if err := copy.claim(gate); err == nil {
		t.Fatal("reused copied owner accepted")
	}
	next.Release()
	assertUsed(0)
	if err := gate.Drain(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestBodyAdmissionPlanExcessAndCheckedEnvelopeNeverDebit(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{destination: environment.DestinationKindOriginal, backend: protocolspec.DialectAnthropicMessages, transform: messagetransform.Policy{RequestJavaScript: `;`}})
	gate, err := NewBodyAdmission(admissionTestPolicy(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.CheckPlan(plan); err == nil {
		t.Fatal("over-capacity plan accepted")
	}
	if lease, err := gate.AcquirePlan(context.Background(), plan); err == nil || lease != nil {
		t.Fatal("over-capacity plan debited")
	}
	if gate.used != 0 {
		t.Fatal("failed complete admission changed credit")
	}
	// A valid policy can have explicit padding at MaxInt64. Adding this
	// plan's retention must reject checked sum overflow, without any debit.
	p := admissionTestPolicy(1)
	p.SlotBytes, p.ActiveBytes = math.MaxInt64, math.MaxInt64
	overflow, err := NewBodyAdmission(p)
	if err != nil {
		t.Fatal(err)
	}
	if lease, err := overflow.AcquirePlan(context.Background(), plan); err == nil || lease != nil || overflow.used != 0 {
		t.Fatal("overflowing plan reservation changed credit")
	}
	for _, terms := range [][][2]uint64{{{math.MaxInt64, 1}, {1, 1}}, {{math.MaxInt64, 2}}} {
		if _, err := checkedEnvelope(terms...); err == nil {
			t.Fatal("sum/multiply overflow accepted")
		}
	}
}

func TestBodyLeaseConstructorRequiresItsActualFrozenPlan(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{destination: environment.DestinationKindOriginal, backend: protocolspec.DialectAnthropicMessages, transform: messagetransform.Policy{RequestJavaScript: `;`}})
	other := mustEnvironmentRequestPlan(t, testPlanOptions{destination: environment.DestinationKindOriginal, backend: protocolspec.DialectAnthropicMessages, transform: messagetransform.Policy{RequestJavaScript: `request.headers["x-other"]="yes";`}})
	gate, err := NewBodyAdmission(admissionTestPolicy(2))
	if err != nil {
		t.Fatal(err)
	}
	base, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Release()
	operation, err := NewClientOperationEvidence(plan.Operation().ID(), plan.Operation().Revision(), "POST", "/v1/messages", "")
	if err != nil {
		t.Fatal(err)
	}
	construct := func(p environment.RequestPlan, lease *BodyLease) error {
		_, err := NewClientRequest("plan-bound", p, operation, completeClientRequest(), ReplayGenerationCostOnly, wireprofile.ApplicationProtocolHTTP1, WithBodyLease(lease))
		return err
	}
	if err := construct(plan, base); err == nil {
		t.Fatal("constructor upgraded no-plan lease")
	}
	base.Release()
	lease, err := gate.AcquirePlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if err := construct(other, lease); err == nil {
		t.Fatal("constructor changed frozen plan")
	}
	if err := construct(plan, lease); err != nil {
		t.Fatal(err)
	}
	copy := *lease
	if err := construct(plan, &copy); err == nil {
		t.Fatal("copied lease constructed twice")
	}
	foreign, _ := NewBodyAdmission(admissionTestPolicy(2))
	if err := lease.claim(foreign); err == nil {
		t.Fatal("foreign gate claimed variable reservation")
	}
	lease.Release()
	if err := lease.claim(gate); err == nil {
		t.Fatal("released lease claimed")
	}
	if gate.used != 0 {
		t.Fatal("failed constructors minted/lost credit")
	}
}

func TestResourceEnvelopeBoundariesAndOverflow(t *testing.T) {
	policy := admissionTestPolicy(1)
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ResourcePolicy){func(p *ResourcePolicy) { p.RequestBytes-- }, func(p *ResourcePolicy) { p.ResponseBytes-- }, func(p *ResourcePolicy) { p.Content.Scratch.PayloadBytes-- }, func(p *ResourcePolicy) { p.Content.Scratch.StructureBytes-- }, func(p *ResourcePolicy) { p.ActiveBytes = p.SlotBytes - 1 }, func(p *ResourcePolicy) { p.Content.Semantic.Response.PayloadBytes = math.MaxInt64 }} {
		copy := policy
		mutate(&copy)
		if err := copy.Validate(); err == nil {
			t.Fatal("under-reserved/overflowed policy accepted")
		}
	}
	gate, _ := NewBodyAdmission(policy)
	policy.Content.Semantic.Request.PayloadBytes = 1
	if gate.Policy().Content.Semantic.Request.PayloadBytes == 1 {
		t.Fatal("gate retained a mutable policy alias")
	}
}

func TestIndependentResponseReservationCoversEscapedSupportedText(t *testing.T) {
	limits := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 32 << 20, StructureBytes: 32 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 32 << 20, StructureBytes: 32 << 20}}
	reserve, scratch, err := RequiredResponseReservation(limits)
	if err != nil {
		t.Fatal(err)
	}
	l := exchangecontent.SourceLimits{Semantic: limits, Scratch: scratch, CanonicalBytes: reserve.CanonicalBytes + (1 << 20), RetainedBytes: reserve.RetainedBytes + (1 << 20), StructureBytes: reserve.StructureBytes + (1 << 20)}
	question, _ := protocolcore.NewTextBlock("question")
	answer, _ := protocolcore.NewTextBlock(strings.Repeat("&", 6<<20))
	request := protocolcore.Request{RequestedModel: "m", EffectiveModel: "m", Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{question}}}}
	response := protocolcore.Response{ID: "r", RequestedModel: "m", EffectiveModel: "m", ReportedModel: "m", StopReason: protocolcore.StopReasonEndTurn, Blocks: []protocolcore.ContentBlock{answer}}
	source, err := exchangecontent.NewSourceWithin(l, "response-reservation", exchangecontent.FrozenRef{EnvironmentID: "env", EnvironmentRevision: 1, EnvironmentDigest: strings.Repeat("a", 64), ClientEndpointID: "ep", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, RouteID: "route", RouteRevision: 1}, environment.DefaultContentRecordingPolicy(), time.Now(), request, &response)
	if err != nil {
		t.Fatal(err)
	}
	cost, err := source.Measure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cost.CanonicalBytes <= 32<<20 || cost.CanonicalBytes > reserve.CanonicalBytes || cost.RetainedBytes > reserve.RetainedBytes || cost.StructureBytes > reserve.StructureBytes || cost.MaxPhysicalSlots > 16384 {
		t.Fatalf("fixed response reservation failed: %+v reserve=%+v", cost, reserve)
	}
}
func TestBodyAdmissionCallerReleaseCannotRefundExecutingLease(t *testing.T) {
	gate, err := NewBodyAdmission(admissionTestPolicy(1))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = lease.reserveConstructor(12); err != nil {
		t.Fatal(err)
	}
	if err = lease.claim(gate); err != nil {
		t.Fatal(err)
	}
	lease.Release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = gate.Drain(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("live execution refunded: %v", err)
	}
	if err = lease.claim(gate); err == nil {
		t.Fatal("reused released lease accepted")
	}
	lease.finish()
	if err = gate.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease.Release()
	next, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	next.Release()
}
func TestBodyAdmissionFourSlotsWaitCancelAndShutdown(t *testing.T) {
	gate, err := NewBodyAdmission(admissionTestPolicy(4))
	if err != nil {
		t.Fatal(err)
	}
	leases := make([]*BodyLease, 4)
	for i := range leases {
		leases[i], err = gate.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan error, 4)
	for range 4 {
		go func() {
			lease, err := gate.Acquire(context.Background())
			if lease != nil {
				lease.Release()
			}
			results <- err
		}()
	}
	gate.BeginShutdown()
	for range 4 {
		if err := <-results; !errors.Is(err, ErrRuntimeStopping) {
			t.Fatalf("waiter shutdown: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = gate.Drain(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("shutdown refunded live callers")
	}
	for _, lease := range leases {
		lease.Release()
	}
	if err = gate.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestBodyAdmissionRejectsForeignAndDuplicateConstruction(t *testing.T) {
	first, _ := NewBodyAdmission(admissionTestPolicy(1))
	second, _ := NewBodyAdmission(admissionTestPolicy(1))
	lease, _ := first.Acquire(context.Background())
	defer lease.Release()
	if err := lease.reserveConstructor(1); err != nil {
		t.Fatal(err)
	}
	if err := lease.reserveConstructor(1); err == nil {
		t.Fatal("duplicate constructor allowed")
	}
	if err := lease.claim(second); err == nil {
		t.Fatal("foreign gate allowed")
	}
	if err := lease.claim(first); err != nil {
		t.Fatal(err)
	}
	lease.finish()
	if err := lease.claim(first); err == nil {
		t.Fatal("second execution allowed")
	}
}

func TestBodyLeaseValueCopiesShareReleaseAndExecutionState(t *testing.T) {
	gate, err := NewBodyAdmission(admissionTestPolicy(1))
	if err != nil {
		t.Fatal(err)
	}
	lease, _ := gate.Acquire(context.Background())
	copy := *lease
	lease.Release()
	copy.Release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gate.Drain(ctx); err != nil {
		t.Fatalf("value copy duplicated slot refund: %v", err)
	}
	if err := copy.reserveConstructor(1); err == nil {
		t.Fatal("copied released lease constructed a request")
	}
}
