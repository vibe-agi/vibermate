package exchangecontent

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/recording"
	"reflect"
	"strings"
	"time"
	"unsafe"
)

type OwnedRecord struct {
	source *Source
	cost   RecordCost
}
type OwnInput struct {
	ExchangeID string
	Frozen     FrozenRef
	Policy     environment.ContentRecordingPolicy
	RecordedAt time.Time
	Parent     ParentRef
	Request    protocolcore.Request
	Response   *protocolcore.Response
}

var ErrOwnershipCapacity = errors.New("optional content recording capacity unavailable")

// OwnWithin charges preparation before Source construction. Source's sanitizer
// charges its largest live scratch phase before allocating. Every retained
// output allocation is then charged before copying; producer input is borrowed
// only until this call returns. The caller owns reservation on every return.
func OwnWithin(ctx context.Context, in OwnInput, limits SourceLimits, reservation *recording.Reservation) (*OwnedRecord, error) {
	if ctx == nil || reservation == nil {
		return nil, ErrInvalidEvidence
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	requestCost, err := protocolcore.MeasureRequest(in.Request)
	if err != nil {
		return nil, err
	}
	structure := requestCost.StructureBytes
	if in.Response != nil {
		c, e := protocolcore.MeasureResponse(*in.Response)
		if e != nil {
			return nil, e
		}
		structure += c.StructureBytes
	}
	// Constructor metadata slices, validation workspace, Source and record headers.
	if structure > 64<<20 || !reservation.TryGrow(int64(3*structure+32768)) {
		return nil, ErrOwnershipCapacity
	}
	source, err := newSourceWithin(limits, in.ExchangeID, in.Frozen, in.Policy, in.RecordedAt, in.Request, in.Response, reservation, WithParentRef(in.Parent))
	if err != nil {
		return nil, err
	}
	meta := source.metadata
	record := Record{ExchangeID: meta.ExchangeID, Parent: meta.Parent, Frozen: meta.Frozen, Mode: meta.Mode, RecordedAt: meta.RecordedAt, ExpiresAt: meta.ExpiresAt,
		Request: Request{RequestedModel: meta.Request.RequestedModel, EffectiveModel: meta.Request.EffectiveModel, MaxOutputTokens: meta.Request.MaxOutputTokens, Stream: meta.Request.Stream, Tools: meta.Request.Tools, ProtocolEvidence: meta.Request.ProtocolEvidence}}
	if v := meta.Response; v != nil {
		record.Response = &Response{ID: v.ID, RequestedModel: v.RequestedModel, EffectiveModel: v.EffectiveModel, ReportedModel: v.ReportedModel, StopReason: v.StopReason, Usage: v.Usage, ProtocolEvidence: v.ProtocolEvidence}
	}
	if err := ownValue(reflect.ValueOf(&record).Elem(), reservation); err != nil {
		return nil, err
	}
	if !reservation.TryGrow(int64(len(in.Request.Messages)) * int64(unsafe.Sizeof(Message{}))) {
		return nil, ErrOwnershipCapacity
	}
	record.Request.Messages = make([]Message, len(in.Request.Messages))
	err = source.Walk(ctx, func(part Part, index int, m MessageSource) error {
		h := m.Header()
		if err := ownValue(reflect.ValueOf(&h).Elem(), reservation); err != nil {
			return err
		}
		if !reservation.TryGrow(int64(h.BlockCount) * int64(unsafe.Sizeof(Block{}))) {
			return ErrOwnershipCapacity
		}
		blocks := make([]Block, 0, h.BlockCount)
		if err := m.WalkBlocks(ctx, func(b Block) error {
			if len(blocks) == cap(blocks) {
				return ErrInvalidEvidence
			}
			if err := ownValue(reflect.ValueOf(&b).Elem(), reservation); err != nil {
				return err
			}
			blocks = append(blocks, b)
			return nil
		}); err != nil {
			return err
		}
		switch part {
		case SystemPart:
			record.Request.System = blocks
		case RequestPart:
			record.Request.Messages[index] = Message{Role: h.Role, Agent: h.Agent, Blocks: blocks}
		case ResponsePart:
			record.Response.Blocks = blocks
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	owned := &Source{limits: limits, record: &record, metadata: metadataFromRecord(record)}
	cost, err := owned.Measure(ctx)
	if err != nil {
		return nil, err
	}
	// Sequential worker workspace: canonical row, grow destination, driver copy,
	// metadata JSON and descriptor/map capacity. Charge independently of payload
	// now, so a worker never waits for credit retained by its own queue.
	workspace := 8*min(cost.MaxLogicalBlockBytes+56, uint64(MaxEncodedBytes)) + 16*ownedValueBytes(reflect.ValueOf(owned.metadata)) + 2048*(cost.TranscriptNodes+1) + 128*cost.MaxPhysicalSlots + 131072
	if workspace > 64<<20 || !reservation.TryGrow(int64(workspace)) {
		return nil, ErrOwnershipCapacity
	}
	return &OwnedRecord{source: owned, cost: cost}, nil
}

// Reflection is confined to the closed, exported content projection structs.
// It counts before cloning and never walks protocol requests or body owners.
func ownValue(v reflect.Value, reservation *recording.Reservation) error {
	n := ownedValueBytes(v)
	if n > 64<<20 || !reservation.TryGrow(int64(n)) {
		return ErrOwnershipCapacity
	}
	cloneOwnedValue(v)
	return nil
}
func ownedValueBytes(v reflect.Value) uint64 {
	if v.Type() == reflect.TypeOf(time.Time{}) {
		return 0
	}
	var n uint64
	switch v.Kind() {
	case reflect.String:
		return uint64(v.Len())
	case reflect.Pointer:
		if !v.IsNil() {
			return uint64(v.Type().Elem().Size()) + ownedValueBytes(v.Elem())
		}
	case reflect.Slice:
		n = uint64(v.Len()) * uint64(v.Type().Elem().Size())
		for i := 0; i < v.Len(); i++ {
			n += ownedValueBytes(v.Index(i))
			if n > 64<<20 {
				return n
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			n += ownedValueBytes(v.Field(i))
			if n > 64<<20 {
				return n
			}
		}
	}
	return n
}
func cloneOwnedValue(v reflect.Value) {
	if v.Type() == reflect.TypeOf(time.Time{}) {
		v.Set(reflect.ValueOf(v.Interface().(time.Time).UTC()))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(strings.Clone(v.String()))
	case reflect.Pointer:
		if !v.IsNil() {
			p := reflect.New(v.Type().Elem())
			p.Elem().Set(v.Elem())
			cloneOwnedValue(p.Elem())
			v.Set(p)
		}
	case reflect.Slice:
		if !v.IsNil() {
			p := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
			reflect.Copy(p, v)
			for i := 0; i < p.Len(); i++ {
				cloneOwnedValue(p.Index(i))
			}
			v.Set(p)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			cloneOwnedValue(v.Field(i))
		}
	}
}

// Source is a synchronous worker borrow of owned sanitized storage only.
func (o *OwnedRecord) Source() *Source {
	if o == nil {
		return nil
	}
	return o.source
}

// RetainedBytes is the measured owned projection, excluding temporary worker
// workspace. The reservation remains owned by the caller/queue until completion.
func (o *OwnedRecord) RetainedBytes() uint64 {
	if o == nil {
		return 0
	}
	n := o.cost.RetainedBytes + o.cost.StructureBytes + uint64(unsafe.Sizeof(*o)) + uint64(unsafe.Sizeof(*o.source))
	if o.source.metadata.Response != nil {
		n += uint64(unsafe.Sizeof(*o.source.metadata.Response))
	}
	return n
}
