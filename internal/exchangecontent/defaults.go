package exchangecontent

import (
	"encoding/json"
	"errors"
	"math"
	"unsafe"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func checkedContentEnvelope(terms ...[2]uint64) (uint64, error) {
	var total uint64
	for _, term := range terms {
		if term[1] != 0 && term[0] > (math.MaxInt64-total)/term[1] {
			return 0, errors.New("execution envelope overflows")
		}
		total += term[0] * term[1]
	}
	return total, nil
}

// RequiredResponseReservation is logical record headroom, NOT live allocation.
// F(n)<=7n is the four-step sanitizer bound; sanitized arguments cost at most
// 6*F(3J)+13J<=139J. Text/extensions/metadata have smaller coefficients, so
// their disjoint, per-occurrence semantic payload sum P is bounded by139P.
// A response has4096 blocks plus two views per256 top-level extensions. The
// fixed per-block allowances cover identifiers, agents and canonical syntax.
func RequiredResponseReservation(l protocolcore.ResourceLimits) (RecordCost, protocolcore.ResourceCost, error) {
	if err := l.Validate(); err != nil {
		return RecordCost{}, protocolcore.ResourceCost{}, err
	}
	const blocks = uint64(protocolcore.MaxContentBlocks + 2*protocolcore.MaxProviderExtensions)
	p := l.Response.PayloadBytes
	r, err := checkedContentEnvelope([2]uint64{p, 139}, [2]uint64{blocks, 4096})
	if err != nil {
		return RecordCost{}, protocolcore.ResourceCost{}, err
	}
	c, err := checkedContentEnvelope([2]uint64{p, 139}, [2]uint64{blocks, 32768}, [2]uint64{protocolcore.MaxProtocolEvidenceValues, 32}, [2]uint64{4096, 1})
	if err != nil {
		return RecordCost{}, protocolcore.ResourceCost{}, err
	}
	s, err := checkedContentEnvelope([2]uint64{1, uint64(unsafe.Sizeof(Response{}))}, [2]uint64{blocks, uint64(unsafe.Sizeof(Block{})) + uint64(unsafe.Sizeof(AgentContext{}))}, [2]uint64{protocolcore.MaxProtocolEvidenceValues, uint64(unsafe.Sizeof(protocolcore.ProtocolEvidenceValue{}))})
	if err != nil {
		return RecordCost{}, protocolcore.ResourceCost{}, err
	}
	// Source.reserveBlockScratch / extensionViews: maximum one leaf, never
	// total historical preparation. JSONObject is bounded independently.
	j := min(p, uint64(protocolcore.MaxToolJSONBytes))
	e := min(p, uint64(protocolcore.MaxProviderExtensionBytes))
	t := min(p, uint64(protocolcore.MaxTextBytes))
	ordinary, err := checkedContentEnvelope([2]uint64{t, 29}, [2]uint64{4096, 1})
	if err != nil {
		return RecordCost{}, protocolcore.ResourceCost{}, err
	}
	arguments, err := checkedContentEnvelope([2]uint64{j, 343}, [2]uint64{4096, 1})
	if err != nil {
		return RecordCost{}, protocolcore.ResourceCost{}, err
	}
	extension, err := checkedContentEnvelope([2]uint64{e, 92}, [2]uint64{8192, 1})
	if err != nil {
		return RecordCost{}, protocolcore.ResourceCost{}, err
	}
	q := uint64(unsafe.Sizeof(protocolcore.ContentBlock{})) + 2*uint64(unsafe.Sizeof("")) + uint64(unsafe.Sizeof(json.RawMessage{}))
	argumentCells, err := checkedContentEnvelope([2]uint64{j, 2 * q}, [2]uint64{4096, 1})
	if err != nil {
		return RecordCost{}, protocolcore.ResourceCost{}, err
	}
	scratch := protocolcore.ResourceCost{PayloadBytes: max(ordinary, arguments, extension), StructureBytes: max(argumentCells, 2*uint64(unsafe.Sizeof(Block{}))+8192)}
	return RecordCost{RetainedBytes: r, CanonicalBytes: c, StructureBytes: s, TranscriptNodes: 1, MaxPhysicalSlots: blocks + c/uint64(MaxEncodedBytes-56)}, scratch, nil
}

// DefaultSourceLimits derives fresh finite defaults from the shared semantic policy.
func DefaultSourceLimits() (SourceLimits, error) {
	semantic := protocolcore.DefaultResourceLimits()
	r, scratch, err := RequiredResponseReservation(semantic)
	if err != nil {
		return SourceLimits{}, err
	}
	retained, err := checkedContentEnvelope([2]uint64{r.RetainedBytes, 1}, [2]uint64{64 << 20, 1})
	if err != nil {
		return SourceLimits{}, err
	}
	canonical, err := checkedContentEnvelope([2]uint64{r.CanonicalBytes, 1}, [2]uint64{256 << 20, 1})
	if err != nil {
		return SourceLimits{}, err
	}
	structure, err := checkedContentEnvelope([2]uint64{r.StructureBytes, 1}, [2]uint64{512 << 20, 1})
	if err != nil {
		return SourceLimits{}, err
	}
	limits := SourceLimits{Semantic: semantic, Scratch: scratch, RetainedBytes: retained, CanonicalBytes: canonical, StructureBytes: structure}
	return limits, limits.Validate()
}
