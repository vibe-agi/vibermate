package exchange

import (
	"errors"
	"math"
	"unsafe"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/providertransport"
)

func checkedEnvelope(terms ...[2]uint64) (uint64, error) {
	var total uint64
	for _, term := range terms {
		if term[1] != 0 && term[0] > (math.MaxInt64-total)/term[1] {
			return 0, errors.New("execution envelope overflows")
		}
		total += term[0] * term[1]
	}
	return total, nil
}

// RequiredResponseReservation forwards to the record representation owner.
func RequiredResponseReservation(l protocolcore.ResourceLimits) (exchangecontent.RecordCost, protocolcore.ResourceCost, error) {
	return exchangecontent.RequiredResponseReservation(l)
}

// RequiredExecutionEnvelope bounds application-owned representations. It is
// not RSS: allocator/GC retention, shared codec/SQLite caches, operator VM heap
// and bridge-triggered Goja-internal enumeration snapshots are measured
// separately. All exported Go bodies/keys/context graphs remain bounded.
// Request-carried owners remain reserved during the response; Response never
// consumes request slack. Compiled transform-chain retention is added per plan.
func RequiredExecutionEnvelope(l exchangecontent.SourceLimits) (request, response uint64, err error) {
	if err = l.Validate(); err != nil {
		return 0, 0, err
	}
	requestIR, err := checkedEnvelope([2]uint64{l.Semantic.Request.PayloadBytes, 1}, [2]uint64{l.Semantic.Request.StructureBytes, 1})
	if err != nil {
		return 0, 0, err
	}
	responseIR, err := checkedEnvelope([2]uint64{l.Semantic.Response.PayloadBytes, 1}, [2]uint64{l.Semantic.Response.StructureBytes, 1})
	if err != nil {
		return 0, 0, err
	}
	const wire = uint64(providertransport.MaxProviderRequestBytes)
	const responseWire = uint64(maxCompleteResponseBytes)
	// Source write descriptors: transcript node, digest strings, map key and
	// borrowed MessageSource/Header. Two capacities cover map/slice growth.
	type node struct {
		digest  string
		parent  *string
		message string
		depth   int
	}
	type descriptor struct {
		source exchangecontent.MessageSource
		header exchangecontent.MessageHeader
		slots  int
	}
	descriptors, err := checkedEnvelope([2]uint64{100001, 2 * (uint64(unsafe.Sizeof(node{})) + 3*64 + uint64(unsafe.Sizeof(descriptor{})) + uint64(unsafe.Sizeof("")) + 64)})
	if err != nil {
		return 0, 0, err
	}
	// One32MiB physical row, encoder destination (<2 rows), driver row copy.
	// No canonical/history-sized buffer is constructed by PutSource.
	record, err := checkedEnvelope([2]uint64{l.Scratch.PayloadBytes, 1}, [2]uint64{l.Scratch.StructureBytes, 1}, [2]uint64{exchangecontent.MaxEncodedBytes, 4}, [2]uint64{descriptors, 1})
	if err != nil {
		return 0, 0, err
	}
	// Active selector/transform bridge: escaped JSON grow old/new capacities
	// (18 wire), body string + canonical input (2 wire). Header maps/arrays,
	// context JSON/graph and metadata are separately bounded below.
	header := uint64(64<<10) + 128*(uint64(unsafe.Sizeof(""))+uint64(unsafe.Sizeof([]string{}))+128*uint64(unsafe.Sizeof("")))
	context := uint64(7*(64<<10)) + 1024*(uint64(unsafe.Sizeof(any(nil)))+128)
	metadata := uint64(6*(128+4096+64+256+64+128+4096+256+64) + 1024)
	transformMetadata, err := checkedEnvelope([2]uint64{header, 4}, [2]uint64{context, 1}, [2]uint64{metadata, 2})
	if err != nil {
		return 0, 0, err
	}
	// AccountSelector's frozen1024 Account entries (128-byte ID/256-byte
	// display name), their escaped grow/freeze buffers, and allowlist cells.
	selectorMetadata, err := checkedEnvelope([2]uint64{1024, 3*(6*(128+256)+64) + 3*2*uint64(unsafe.Sizeof(""))}, [2]uint64{header, 2}, [2]uint64{metadata + 6*128 + 128, 3})
	if err != nil {
		return 0, 0, err
	}
	script, err := checkedEnvelope([2]uint64{wire, 20}, [2]uint64{max(transformMetadata, selectorMetadata), 1})
	if err != nil {
		return 0, 0, err
	}
	decode, err := checkedEnvelope([2]uint64{l.Semantic.Request.PayloadBytes, 2}, [2]uint64{l.Semantic.Request.StructureBytes, 3})
	if err != nil {
		return 0, 0, err
	}
	// The larger original/managed branch retains at most six request-sized
	// owners: admitted, mapped/evidence, decoder request, stream request,
	// retaining client encoder and tool catalog. They are not cumulative stages.
	request, err = checkedEnvelope([2]uint64{requestIR, 6}, [2]uint64{wire, 8}, [2]uint64{max(record, script, decode), 1})
	if err != nil {
		return 0, 0, err
	}
	// Feed uses32KiB reads plus an unfinished4MiB event. Encoded escaping is
	// ≤6 bytes/input byte;4096 syntax bytes per possible framed event is an
	// upper bound on the closed encoder envelopes. Both safe and returned bytes
	// coexist. Terminal/capture/approval paths have at most seven response-sized
	// immutable/mutable owners, including old/new builder capacities.
	output, err := checkedEnvelope([2]uint64{responseWire, 6}, [2]uint64{streamReadBufferBytes/2 + protocolcore.MaxToolCount + 4, 4096})
	if err != nil {
		return 0, 0, err
	}
	stream, err := checkedEnvelope([2]uint64{responseIR, 7}, [2]uint64{output, 2}, [2]uint64{responseWire, 2}, [2]uint64{4 << 20, 2})
	if err != nil {
		return 0, 0, err
	}
	completion, err := checkedEnvelope([2]uint64{responseIR, 2}, [2]uint64{record, 1})
	if err != nil {
		return 0, 0, err
	}
	return request, max(stream, completion), nil
}
