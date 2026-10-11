package runtimepersistence

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

type manifestField struct {
	name, kind, child   string
	maximum             int
	optional, noLogical bool
}

var manifestShapes = map[string][]manifestField{
	"record":     {{name: "exchangeId", kind: "string", maximum: exchangecontent.MaxExchangeIDBytes}, {name: "parent", kind: "object", child: "parent"}, {name: "frozen", kind: "object", child: "frozen"}, {name: "mode", kind: "string", maximum: 16}, {name: "recordedAt", kind: "string", maximum: 64, noLogical: true}, {name: "expiresAt", kind: "string", maximum: 64, noLogical: true}, {name: "request", kind: "object", child: "request"}, {name: "response", kind: "object", child: "response", optional: true}},
	"parent":     {{name: "captureRunId", kind: "string", maximum: 128, optional: true}, {name: "manualCaptureId", kind: "string", maximum: 128, optional: true}},
	"frozen":     {{name: "environmentId", kind: "string"}, {name: "environmentRevision", kind: "uint"}, {name: "environmentDigest", kind: "string"}, {name: "clientEndpointId", kind: "string"}, {name: "clientEndpointRevision", kind: "uint"}, {name: "protocolPlanId", kind: "string"}, {name: "protocolPlanRevision", kind: "uint"}, {name: "routeId", kind: "string"}, {name: "routeRevision", kind: "uint"}},
	"request":    {{name: "requestedModel", kind: "string"}, {name: "effectiveModel", kind: "string"}, {name: "maxOutputTokens", kind: "int"}, {name: "stream", kind: "bool"}, {name: "tools", kind: "array", child: "tool", maximum: protocolcore.MaxToolCount}, {name: "protocolEvidence", kind: "array", child: "evidence", maximum: protocolcore.MaxProtocolEvidenceValues, optional: true}},
	"response":   {{name: "id", kind: "string", maximum: 512}, {name: "requestedModel", kind: "string"}, {name: "effectiveModel", kind: "string"}, {name: "reportedModel", kind: "string"}, {name: "stopReason", kind: "string"}, {name: "usage", kind: "object", child: "usage"}, {name: "protocolEvidence", kind: "array", child: "evidence", maximum: protocolcore.MaxProtocolEvidenceValues, optional: true}, {name: "emptyOutput", kind: "bool", optional: true}},
	"tool":       {{name: "name", kind: "string", maximum: protocolcore.MaxToolNameBytes}, {name: "namespace", kind: "string", maximum: protocolcore.MaxToolNamespaceBytes, optional: true}},
	"evidence":   {{name: "name", kind: "string", maximum: protocolcore.MaxProtocolEvidenceNameBytes}, {name: "value", kind: "string", maximum: protocolcore.MaxProtocolEvidenceValueBytes}},
	"usage":      {{name: "inputUncached", kind: "object", child: "usageValue"}, {name: "cacheWrite", kind: "object", child: "usageValue"}, {name: "cacheRead", kind: "object", child: "usageValue"}, {name: "output", kind: "object", child: "usageValue"}, {name: "reasoning", kind: "object", child: "usageValue"}},
	"usageValue": {{name: "known", kind: "bool"}, {name: "tokens", kind: "int", optional: true}, {name: "source", kind: "string", optional: true}},
}

// The preflight has fixed nesting/schema state and never creates decoded
// strings, collection cells or a generic JSON tree. Logical metadata is charged
// in the same Record/Response/tool/evidence currencies as Source.Measure.
type manifestAdmission struct {
	ctx                                        context.Context
	data                                       []byte
	pos, checked                               int
	bytes, cells, maxBytes, maxCells, capacity uint64
}

func (p *manifestAdmission) charge(b, s uint64) error {
	if b > p.maxBytes-p.bytes || s > p.maxCells-p.cells {
		return exchangecontent.ErrInvalidEvidence
	}
	p.bytes += b
	p.cells += s
	return nil
}
func (p *manifestAdmission) tick() error {
	if p.pos-p.checked >= 4096 {
		p.checked = p.pos
		return p.ctx.Err()
	}
	return nil
}
func (p *manifestAdmission) take(b byte) error {
	if p.pos >= len(p.data) || p.data[p.pos] != b {
		return exchangecontent.ErrInvalidEvidence
	}
	p.pos++
	return p.tick()
}
func (p *manifestAdmission) literal(s string) bool {
	if !bytes.HasPrefix(p.data[p.pos:], []byte(s)) {
		return false
	}
	p.pos += len(s)
	return true
}
func (p *manifestAdmission) stringValue(logical bool, maximum int) error {
	if err := p.take('"'); err != nil {
		return err
	}
	var n uint64
	for p.pos < len(p.data) {
		if err := p.tick(); err != nil {
			return err
		}
		c := p.data[p.pos]
		p.pos++
		if c == '"' {
			if maximum > 0 && n > uint64(maximum) {
				return exchangecontent.ErrInvalidEvidence
			}
			if logical {
				return p.charge(n, 0)
			}
			return nil
		}
		var width uint64 = 1
		if c == '\\' {
			if p.pos >= len(p.data) {
				return exchangecontent.ErrInvalidEvidence
			}
			c = p.data[p.pos]
			p.pos++
			switch c {
			case '"', '\\', 'b', 'f', 'n', 'r', 't':
			case 'u':
				if len(p.data)-p.pos < 4 {
					return exchangecontent.ErrInvalidEvidence
				}
				digits := p.data[p.pos : p.pos+4]
				for _, v := range digits {
					if !(v >= '0' && v <= '9' || v >= 'a' && v <= 'f') {
						return exchangecontent.ErrInvalidEvidence
					}
				}
				v, err := strconv.ParseUint(string(digits), 16, 16)
				if err != nil {
					return exchangecontent.ErrInvalidEvidence
				}
				p.pos += 4
				if v == 0x2028 || v == 0x2029 {
					width = 3
				} else if v == 0x26 || v == 0x3c || v == 0x3e {
				} else if v > 31 || v == 8 || v == 9 || v == 10 || v == 12 || v == 13 {
					return exchangecontent.ErrInvalidEvidence
				}
			default:
				return exchangecontent.ErrInvalidEvidence
			}
		} else if c < 0x20 || c == '<' || c == '>' || c == '&' {
			return exchangecontent.ErrInvalidEvidence
		} else if c >= utf8.RuneSelf {
			r, size := utf8.DecodeRune(p.data[p.pos-1:])
			if size == 1 || r == 0x2028 || r == 0x2029 {
				return exchangecontent.ErrInvalidEvidence
			}
			p.pos += size - 1
			width = uint64(size)
		}
		if width > math.MaxInt64-n {
			return exchangecontent.ErrInvalidEvidence
		}
		n += width
		if maximum > 0 && n > uint64(maximum) || logical && n > p.maxBytes-p.bytes {
			return exchangecontent.ErrInvalidEvidence
		}
	}
	return exchangecontent.ErrInvalidEvidence
}
func (p *manifestAdmission) object(shape string) error {
	if err := p.take('{'); err != nil {
		return err
	}
	fields := manifestShapes[shape]
	last := -1
	var seen uint64
	if shape == "response" {
		if err := p.charge(0, uint64(reflect.TypeFor[exchangecontent.Response]().Size())); err != nil {
			return err
		}
	}
	for p.pos < len(p.data) && p.data[p.pos] != '}' {
		if last >= 0 {
			if err := p.take(','); err != nil {
				return err
			}
		}
		start := p.pos
		if err := p.stringValue(false, 64); err != nil {
			return err
		}
		key := string(p.data[start+1 : p.pos-1])
		index := -1
		for i, f := range fields {
			if f.name == key {
				index = i
				break
			}
		}
		if index <= last {
			return exchangecontent.ErrInvalidEvidence
		}
		last = index
		seen |= 1 << index
		f := fields[index]
		if err := p.take(':'); err != nil {
			return err
		}
		switch f.kind {
		case "string":
			if err := p.stringValue(!f.noLogical, f.maximum); err != nil {
				return err
			}
		case "object":
			if err := p.object(f.child); err != nil {
				return err
			}
		case "array":
			if p.literal("null") {
				continue
			}
			if err := p.take('['); err != nil {
				return err
			}
			count := 0
			for p.pos < len(p.data) && p.data[p.pos] != ']' {
				if count >= f.maximum {
					return exchangecontent.ErrInvalidEvidence
				}
				if count > 0 {
					if err := p.take(','); err != nil {
						return err
					}
				}
				count++
				size := uint64(reflect.TypeFor[exchangecontent.ToolDefinition]().Size())
				if f.child == "evidence" {
					size = uint64(reflect.TypeFor[protocolcore.ProtocolEvidenceValue]().Size())
				}
				if err := p.charge(0, size); err != nil {
					return err
				}
				if err := p.object(f.child); err != nil {
					return err
				}
			}
			if err := p.take(']'); err != nil {
				return err
			}
		case "bool":
			if !p.literal("true") && !p.literal("false") {
				return exchangecontent.ErrInvalidEvidence
			}
		case "int", "uint":
			start := p.pos
			for p.pos < len(p.data) && (p.data[p.pos] == '-' || p.data[p.pos] >= '0' && p.data[p.pos] <= '9') {
				p.pos++
				if p.pos-start > 20 {
					return exchangecontent.ErrInvalidEvidence
				}
			}
			value := string(p.data[start:p.pos])
			if f.kind == "uint" {
				v, err := strconv.ParseUint(value, 10, 64)
				if err != nil || strconv.FormatUint(v, 10) != value {
					return exchangecontent.ErrInvalidEvidence
				}
			} else {
				v, err := strconv.ParseInt(value, 10, 64)
				if err != nil || v < 0 || strconv.FormatInt(v, 10) != value {
					return exchangecontent.ErrInvalidEvidence
				}
			}
		}
	}
	for i, f := range fields {
		if !f.optional && seen&(1<<i) == 0 {
			return exchangecontent.ErrInvalidEvidence
		}
	}
	return p.take('}')
}

func admitStoredManifest(ctx context.Context, data []byte, limits *exchangecontent.SourceLimits) (manifestAdmission, error) {
	p := manifestAdmission{ctx: ctx, data: data}
	if ctx == nil || len(data) == 0 || len(data) > exchangecontent.MaxEncodedBytes {
		return p, exchangecontent.ErrInvalidEvidence
	}
	if err := ctx.Err(); err != nil {
		return p, err
	}
	if limits != nil {
		p.maxBytes, p.maxCells = limits.RetainedBytes, limits.StructureBytes
		var err error
		p.capacity, err = storedMaterializationBound(*limits)
		if err != nil {
			return p, err
		}
	} else {
		p.maxBytes = exchangecontent.MaxEncodedBytes
		p.maxCells = uint64(reflect.TypeFor[exchangecontent.Record]().Size()+reflect.TypeFor[exchangecontent.Response]().Size()) + protocolcore.MaxToolCount*uint64(reflect.TypeFor[exchangecontent.ToolDefinition]().Size()) + 2*protocolcore.MaxProtocolEvidenceValues*uint64(reflect.TypeFor[protocolcore.ProtocolEvidenceValue]().Size())
		p.capacity = 8*exchangecontent.MaxEncodedBytes + 4*p.maxCells + 4096
	}
	if err := p.charge(0, uint64(reflect.TypeFor[exchangecontent.Record]().Size())); err != nil {
		return p, err
	}
	if err := p.object("record"); err != nil {
		return p, err
	}
	if p.pos != len(data) {
		return p, exchangecontent.ErrInvalidEvidence
	}
	// Input/driver, Decode buffer, encoder growth and canonical output overlap;
	// strings reserve2x, metadata cells/growth4x. All arithmetic is bounded
	// against the independently derived capacity before typed Decode begins.
	left := p.capacity
	for _, pair := range [][2]uint64{{uint64(len(data)), 6}, {p.bytes, 2}, {p.cells, 4}, {4096, 1}} {
		if pair[0] > left/pair[1] {
			return p, exchangecontent.ErrInvalidEvidence
		}
		left -= pair[0] * pair[1]
	}
	return p, ctx.Err()
}

func decodeStoredContentManifestWithin(ctx context.Context, encoded []byte, limits *exchangecontent.SourceLimits) (storedExchangeContentManifest, error) {
	if _, err := admitStoredManifest(ctx, encoded, limits); err != nil {
		return storedExchangeContentManifest{}, err
	}
	var m storedExchangeContentManifest
	if err := json.Unmarshal(encoded, &m); err != nil {
		return m, exchangecontent.ErrInvalidEvidence
	}
	validID := func(s string, n int) bool {
		return s != "" && len(s) <= n && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\r\n\x00")
	}
	if !validID(m.ExchangeID, exchangecontent.MaxExchangeIDBytes) || m.Parent.Validate() != nil || m.Frozen.Validate() != nil || (m.Mode != environment.ContentRecordingFull && m.Mode != environment.ContentRecordingMetadataOnly) || m.RecordedAt.IsZero() || !m.ExpiresAt.After(m.RecordedAt) || m.Request.RequestedModel == "" || m.Request.EffectiveModel == "" {
		return m, exchangecontent.ErrInvalidEvidence
	}
	for _, t := range m.Request.Tools {
		if !validID(t.Name, protocolcore.MaxToolNameBytes) || t.Namespace != "" && !validID(t.Namespace, protocolcore.MaxToolNamespaceBytes) {
			return m, exchangecontent.ErrInvalidEvidence
		}
	}
	if protocolcore.ValidateProtocolEvidence(m.Request.ProtocolEvidence) != nil {
		return m, exchangecontent.ErrInvalidEvidence
	}
	if v := m.Response; v != nil {
		if !validID(v.ID, 512) || v.RequestedModel == "" || v.EffectiveModel == "" || v.ReportedModel == "" || protocolcore.StopReason(v.StopReason).Validate() != nil || protocolcore.ValidateProtocolEvidence(v.ProtocolEvidence) != nil {
			return m, exchangecontent.ErrInvalidEvidence
		}
	}
	canonical, err := json.Marshal(m)
	if err != nil || !bytes.Equal(canonical, encoded) {
		return m, exchangecontent.ErrInvalidEvidence
	}
	if err := ctx.Err(); err != nil {
		return storedExchangeContentManifest{}, err
	}
	return m, nil
}
