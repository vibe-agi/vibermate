package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"math"
	"reflect"
	"strings"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

// These descriptors borrow the Source until the synchronous transaction ends.
// They contain no encoded messages or Blocks; each physical row is regenerated
// only when its message is published inside that transaction.
type storedSourceMessage struct {
	source exchangecontent.MessageSource
	header exchangecontent.MessageHeader
	// Positive is the checked physical slot count (1..storedManifestSlots).
	// Negative marks successful publication in this PutSource transaction only.
	// Reusing its sign adds no descriptor/map allocation or envelope term.
	slots int
}

// PutSource is deliberately concrete until the Manager handoff is activated.
// A nil constructor policy cannot use it to bypass legacy Record admission.
func (repository *exchangeContentRepository) PutSource(ctx context.Context, source *exchangecontent.Source) error {
	if repository.limits == nil || source == nil || ctx == nil {
		return exchangecontent.ErrInvalidEvidence
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	ctx = operation
	cost, err := source.Measure(ctx)
	if err != nil {
		return err
	}
	l := *repository.limits
	if cost.CanonicalBytes > l.CanonicalBytes || cost.RetainedBytes > l.RetainedBytes || cost.StructureBytes > l.StructureBytes || cost.TranscriptNodes == 0 || cost.TranscriptNodes > 100001 || cost.MaxPhysicalSlots > storedManifestSlots || cost.MaxAgentBytes > 4096 {
		return exchangecontent.ErrInvalidEvidence
	}
	// All metadata/node allocations below are proportional to these checked
	// counts. Row production owns at most one 32MiB row, one MaxEncodedSize
	// encoder destination and the driver's row copy. Shared codec workspace is
	// process-owned (unchanged codec configuration), not a logical Source debit.
	if _, err := storedMaterializationBound(l); err != nil {
		return err
	}
	meta := source.Metadata()
	// Match the legacy manifest's empty-slice representation before both
	// encoding and strict completion comparison. Omitted evidence decodes nil;
	// Source metadata deliberately exposes owned empty arrays to its callers.
	if len(meta.Request.Tools) == 0 {
		meta.Request.Tools = nil
	}
	if len(meta.Request.ProtocolEvidence) == 0 {
		meta.Request.ProtocolEvidence = nil
	}
	if meta.Response != nil && len(meta.Response.ProtocolEvidence) == 0 {
		meta.Response.ProtocolEvidence = nil
	}
	manifest := storedExchangeContentManifest{ExchangeID: meta.ExchangeID, Parent: meta.Parent, Frozen: meta.Frozen, Mode: meta.Mode, RecordedAt: meta.RecordedAt, ExpiresAt: meta.ExpiresAt,
		Request: storedRequestManifest{RequestedModel: meta.Request.RequestedModel, EffectiveModel: meta.Request.EffectiveModel, MaxOutputTokens: meta.Request.MaxOutputTokens, Stream: meta.Request.Stream, Tools: meta.Request.Tools, ProtocolEvidence: meta.Request.ProtocolEvidence}}
	if v := meta.Response; v != nil {
		manifest.Response = &storedResponseManifest{ID: v.ID, RequestedModel: v.RequestedModel, EffectiveModel: v.EffectiveModel, ReportedModel: v.ReportedModel, StopReason: v.StopReason, Usage: v.Usage, ProtocolEvidence: v.ProtocolEvidence, EmptyOutput: v.EmptyOutput}
	}
	encoded, err := json.Marshal(manifest)
	if err != nil || len(encoded) > exchangecontent.MaxEncodedBytes {
		return exchangecontent.ErrInvalidEvidence
	}
	transcript := storedTranscript{nodes: make([]storedTranscriptNode, 0, int(cost.TranscriptNodes))}
	messages := make(map[string]storedSourceMessage, int(cost.TranscriptNodes)+1)
	parent := ""
	err = source.Walk(ctx, func(part exchangecontent.Part, _ int, m exchangecontent.MessageSource) error {
		h := m.Header()
		_, err := storedAgentBytes(h.Agent)
		if err != nil {
			return err
		}
		measure := storedCanonicalMeasure{ctx: ctx, hash: sha256.New(), maximum: l.CanonicalBytes}
		if err := exchangecontent.WriteCanonicalMessage(&measure, m); err != nil {
			return err
		}
		digest := hex.EncodeToString(measure.hash.Sum(nil))
		slots := 0
		if err := m.WalkBlocks(ctx, func(block exchangecontent.Block) error {
			v := storedCanonicalMeasure{ctx: ctx, hash: sha256.New(), maximum: l.CanonicalBytes}
			if err := exchangecontent.WriteCanonicalBlock(&v, block); err != nil {
				return err
			}
			n, err := storedBlockSlotCount(v.bytes)
			if err != nil || int(n) > storedManifestSlots-slots {
				return exchangecontent.ErrInvalidEvidence
			}
			slots += int(n)
			return nil
		}); err != nil {
			return err
		}
		if slots == 0 {
			return exchangecontent.ErrInvalidEvidence
		}
		messages[digest] = storedSourceMessage{source: m, header: h, slots: slots}
		switch part {
		case exchangecontent.SystemPart:
			transcript.systemDigest = digest
		case exchangecontent.RequestPart:
			parent = transcript.appendNode(parent, digest)
			transcript.requestRoot = parent
			transcript.requestCount++
		case exchangecontent.ResponsePart:
			transcript.responseDigest = digest
			parent = transcript.appendNode(parent, digest)
		default:
			return exchangecontent.ErrInvalidEvidence
		}
		return nil
	})
	if err != nil {
		return err
	}
	transcript.expectedRoot = parent
	transcript.expectedCount = len(transcript.nodes)
	if transcript.requestCount == 0 || uint64(len(transcript.nodes)) != cost.TranscriptNodes {
		return exchangecontent.ErrInvalidEvidence
	}
	return repository.publishContent(ctx, manifest, encoded, transcript, func(ctx context.Context, tx *sql.Tx, digest string) error {
		m, ok := messages[digest]
		if !ok {
			return exchangecontent.ErrInvalidEvidence
		}
		if m.slots < 0 {
			return nil
		}
		var physical strings.Builder
		physical.Grow(m.slots * storedDigestHexBytes)
		// Check the second Source traversal against the complete first-pass
		// identity without materializing a canonical message.
		check := storedCanonicalMeasure{ctx: ctx, hash: sha256.New(), maximum: l.CanonicalBytes}
		if err := exchangecontent.WriteCanonicalMessage(&check, m.source); err != nil {
			return err
		}
		if hex.EncodeToString(check.hash.Sum(nil)) != digest {
			return exchangecontent.ErrInvalidEvidence
		}
		if err := m.source.WalkBlocks(ctx, func(block exchangecontent.Block) error {
			return writeStoredBlockRows(ctx, block, l.CanonicalBytes, func(d string, row []byte) error {
				if physical.Len()/storedDigestHexBytes >= m.slots {
					return exchangecontent.ErrInvalidEvidence
				}
				physical.WriteString(d)
				return putStoredBlock(ctx, tx, d, row)
			})
		}); err != nil {
			return err
		}
		if physical.Len() != m.slots*storedDigestHexBytes {
			return exchangecontent.ErrInvalidEvidence
		}
		agent, err := storedAgentBytes(m.header.Agent)
		if err != nil {
			return err
		}
		if err := putStoredMessageManifest(ctx, tx, digest, m.header.Role, agent, physical.String()); err != nil {
			return err
		}
		// All row/ref/conflict guards have succeeded in the same transaction.
		// Each transcript occurrence is still inserted by publishContent. A
		// failed publication/retry reconstructs this per-Put map from the Source.
		m.slots = -m.slots
		messages[digest] = m
		return nil
	})
}

func storedAgentBytes(agent *exchangecontent.AgentContext) ([]byte, error) {
	if agent == nil {
		return nil, nil
	}
	legacy, err := json.Marshal(agent)
	if err != nil {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	if len(legacy) <= 4096 {
		return legacy, nil
	}
	var compact bytes.Buffer
	encoder := json.NewEncoder(&compact)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(agent); err != nil {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	data := bytes.TrimSuffix(compact.Bytes(), []byte{'\n'})
	if len(data) > 4096 {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	return data, nil
}

// This is a representation allowance, never an enlarged logical record limit.
// It covers simultaneously live output capacities, tiny builder rounding per
// Block, bounded SQL metadata/driver copies and physical rows. Shared encoder/
// decoder caches remain process-owned; this is not an RSS or codec-peak claim.
func storedMaterializationBound(l exchangecontent.SourceLimits) (uint64, error) {
	var total uint64
	add := func(n, m uint64) bool {
		if m != 0 && n > (math.MaxInt64-total)/m {
			return false
		}
		total += n * m
		return true
	}
	blocks := l.StructureBytes / uint64(reflect.TypeFor[exchangecontent.Block]().Size())
	if !add(l.RetainedBytes, 2) || !add(l.StructureBytes, 4) || !add(blocks, 4096) || !add(uint64(exchangecontent.MaxEncodedBytes), 6) || !add(uint64(bodyEncoder.MaxEncodedSize(exchangecontent.MaxEncodedBytes)), 1) || !add(100001, 1024) || !add(storedDecodeFixedWorkspace+2*maxNestingDepth*8, 1) {
		return 0, exchangecontent.ErrInvalidEvidence
	}
	return total, nil
}
