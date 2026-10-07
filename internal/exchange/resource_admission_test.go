package exchange

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
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
