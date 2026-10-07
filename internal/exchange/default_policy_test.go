package exchange

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"unsafe"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

func TestDefaultPolicyCoupledBoundsAndFourSlots(t *testing.T) {
	p, err := DefaultResourcePolicy()
	if err != nil {
		t.Fatal(err)
	}
	q := uint64(unsafe.Sizeof(protocolcore.ContentBlock{})) + 2*uint64(unsafe.Sizeof("")) + uint64(unsafe.Sizeof(json.RawMessage{}))
	semantic := p.Content.Semantic
	if semantic.Request != (protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 512 << 20}) || semantic.Response != (protocolcore.ResourceCost{PayloadBytes: 256 << 20, StructureBytes: (16<<20)*q + (128 << 20)}) {
		t.Fatal("coupled semantic tuple changed")
	}
	r, s, err := exchangecontent.RequiredResponseReservation(semantic)
	if err != nil {
		t.Fatal(err)
	}
	if r.RetainedBytes != 37331402752 || r.CanonicalBytes != 37463789568 || r.MaxPhysicalSlots != 5724 || r.MaxPhysicalSlots >= 16384 {
		t.Fatalf("independent response reserve: %+v", r)
	}
	if p.Content.RetainedBytes-r.RetainedBytes != 64<<20 || p.Content.CanonicalBytes-r.CanonicalBytes != 256<<20 || p.Content.StructureBytes-r.StructureBytes != 512<<20 || p.Content.Scratch != s {
		t.Fatal("source residual or scratch changed")
	}
	request, response, err := RequiredExecutionEnvelope(p.Content)
	if err != nil {
		t.Fatal(err)
	}
	if p.RequestBytes-request != 64<<20 || p.ResponseBytes != response || p.SlotBytes != p.RequestBytes+p.ResponseBytes || p.ActiveBytes != 4*p.SlotBytes {
		t.Fatal("execution policy lost independent phases or four slots")
	}
	// Lexical JSON and Chat event temporaries are source-local; Chat retained
	// notice/raw cells have an event-count bound independent of total Feed wire.
	const w = uint64(16 << 20)
	sse := ssewire.DefaultOptions()
	a := uint64(sse.MaxEventBytes)
	e := uint64(sse.MaxEvents)
	k := uint64(protocolcore.MaxToolCount)
	x := uint64(protocolcore.MaxProviderExtensions)
	b := uint64(protocolcore.MaxContentBlocks)
	v := uint64(protocolcore.MaxProtocolEvidenceValues)
	extra := 4 + x + 2
	chatP := w + a + 2*512 + k*(512+256) + (128 + 64 + 1024) + 2*e*(25+13) + 2*extra*(64+1024)
	// Complete Chat retained structure is asserted in anthropicchat's owning
	// package, where the actual private streamToolAccumulator can be measured.
	if 7*w > semantic.Response.PayloadBytes || w*q > semantic.Response.StructureBytes || 7*a > semantic.Response.PayloadBytes || a*q > semantic.Response.StructureBytes || chatP > semantic.Response.PayloadBytes {
		t.Fatal("supported lexical/Chat retained envelope exceeds defaults")
	}
	metadataP := b*(3*512+2*(64+512)+2*256+2*64) + x*(128+64+1024) + v*(128+512) + 4*512 + 1024 + 5*128 + 128
	typedS := uint64(unsafe.Sizeof(protocolcore.Response{})) + b*(uint64(unsafe.Sizeof(protocolcore.ContentBlock{}))+uint64(unsafe.Sizeof(protocolcore.AgentMessageContext{}))) + x*uint64(unsafe.Sizeof(protocolcore.ProviderExtension{})) + w*uint64(unsafe.Sizeof(json.RawMessage{})) + v*uint64(unsafe.Sizeof(protocolcore.ProtocolEvidenceValue{}))
	if 3*w+w+metadataP > semantic.Response.PayloadBytes || typedS > semantic.Response.StructureBytes {
		t.Fatal("supported typed response envelope exceeds defaults")
	}
	if _, err := checkedEnvelope([2]uint64{math.MaxInt64, 2}); err == nil {
		t.Fatal("execution overflow accepted")
	}
	over := semantic
	over.Response.PayloadBytes = math.MaxInt64
	if _, _, err := exchangecontent.RequiredResponseReservation(over); err == nil {
		t.Fatal("response reservation overflow accepted")
	}
	gate, err := NewBodyAdmission(p)
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		lease, err := gate.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Release()
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := gate.Acquire(canceled); err != context.Canceled {
		t.Fatalf("fifth default slot admitted: %v", err)
	}
	fresh, _ := DefaultResourcePolicy()
	p.Content.Semantic.Request.PayloadBytes = 1
	if fresh.Content.Semantic.Request.PayloadBytes != 128<<20 {
		t.Fatal("mutable default shared state")
	}
}
