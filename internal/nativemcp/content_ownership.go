package nativemcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/evidencechunk"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/recording"
)

// ContentInput is a closed synchronous borrow. It cannot retain an execution
// object, lease, callback, authority, session or reader in the recording queue.
type ContentInput struct {
	identity   nativetool.ContentIdentity
	observedAt time.Time
	part       nativetool.ContentPart
	raw        []byte
	profile    ContentProfile
	limits     Limits
}
type ContentRecorder interface {
	ObserveNativeContent(recording.Stamp, ContentInput) recording.OfferResult
}
type OwnedContent struct {
	observation nativetool.ContentObservation
	bytes       uint64
}

func (o *OwnedContent) Observation() nativetool.ContentObservation { return o.observation }
func (o *OwnedContent) RetainedBytes() uint64                      { return o.bytes }

var ErrContentOwnershipCapacity = errors.New("native content ownership capacity")
var errContentSuppressed = errors.New("native content suppressed")

type contentPreparation struct {
	reservation *recording.Reservation
	limits      Limits
}

func (p contentPreparation) grow(n int64) error {
	if !p.reservation.TryGrow(n) {
		return ErrContentOwnershipCapacity
	}
	return nil
}
func (p contentPreparation) object(raw []byte) (map[string]json.RawMessage, error) {
	if err := p.grow(jsonAllocation(raw)); err != nil {
		return nil, err
	}
	return decodeObject(raw, p.limits.ResultBytes, p.limits.Depth)
}
func (p contentPreparation) elements(raw []byte) ([]json.RawMessage, error) {
	if err := p.grow(jsonAllocation(raw)); err != nil {
		return nil, err
	}
	v, ok := contentElements(raw)
	if !ok {
		return nil, errProtocol
	}
	return v, nil
}

// Each path parse is charged before Split/ReplaceAll and each nested decode.
// Scratch is conservatively kept charged through worker completion, even once
// unreachable. There is no allocation-after-refusal fallback or raw fallback.
func (p contentPreparation) contains(object map[string]json.RawMessage, path string) (bool, error) {
	if err := p.grow(int64(32*len(path) + 128)); err != nil {
		return false, err
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	var current json.RawMessage
	for i, part := range parts {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		var raw json.RawMessage
		if i == 0 {
			var ok bool
			raw, ok = object[part]
			if !ok {
				return false, nil
			}
		} else {
			current = bytes.TrimSpace(current)
			if len(current) == 0 {
				return false, nil
			}
			switch current[0] {
			case '{':
				v, err := p.object(current)
				if err != nil {
					return false, err
				}
				var ok bool
				raw, ok = v[part]
				if !ok {
					return false, nil
				}
			case '[':
				v, err := p.elements(current)
				if err != nil {
					return false, err
				}
				index, err := strconv.Atoi(part)
				if err != nil || index < 0 || index >= len(v) || strconv.Itoa(index) != part {
					return false, nil
				}
				raw = v[index]
			default:
				return false, nil
			}
		}
		if i == len(parts)-1 {
			return true, nil
		}
		current = raw
	}
	return false, nil
}

// JSON output can escape each input byte into at most six bytes. Charge both
// encoder growth (at most 2x output) and returned output (1x), plus map sorting
// descriptors. Nested sanitized values are measured again at their NEW size.
func (p contentPreparation) marshalObject(v map[string]json.RawMessage) ([]byte, error) {
	n := int64(2)
	for key, raw := range v {
		n += int64(6*len(key) + 6*len(raw) + 4)
	}
	if err := p.grow(3*n + int64(128*len(v)) + 4096); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
func (p contentPreparation) marshalElements(v []json.RawMessage) ([]byte, error) {
	n := int64(2)
	for _, raw := range v {
		n += int64(6*len(raw) + 1)
	}
	if err := p.grow(3*n + int64(32*len(v)) + 4096); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
func (p contentPreparation) result(raw []byte, object map[string]json.RawMessage) ([]byte, error) {
	// The required executor result validation remains separate. This validates
	// the closed helper's own input with the already measured parser bound.
	if err := p.grow(jsonAllocation(raw)); err != nil {
		return nil, err
	}
	if err := validateMethodResult(nativetool.RequestToolsCall, rpc{object: map[string]json.RawMessage{"result": raw}}, p.limits); err != nil {
		return nil, err
	}
	changed := false
	if _, ok := object["_meta"]; ok {
		delete(object, "_meta")
		changed = true
	}
	if content, ok := object["content"]; ok {
		elements, err := p.elements(content)
		if err != nil {
			return nil, err
		}
		for i, element := range elements {
			block, err := p.object(element)
			if err != nil {
				return nil, err
			}
			blockChanged := false
			if _, ok := block["_meta"]; ok {
				delete(block, "_meta")
				blockChanged = true
			}
			// Decode the tiny type string under the same block parser allowance.
			if kind, _ := text(block["type"]); kind == "resource" {
				resource, err := p.object(block["resource"])
				if err != nil {
					return nil, err
				}
				if _, ok := resource["_meta"]; ok {
					delete(resource, "_meta")
					block["resource"], err = p.marshalObject(resource)
					if err != nil {
						return nil, err
					}
					blockChanged = true
				}
			}
			if blockChanged {
				elements[i], err = p.marshalObject(block)
				if err != nil {
					return nil, err
				}
				changed = true
			}
		}
		if changed {
			object["content"], err = p.marshalElements(elements)
			if err != nil {
				return nil, err
			}
		}
	}
	if !changed {
		return raw, nil
	}
	return p.marshalObject(object)
}

// OwnContentWithin owns the whole preparation/queue/worker peak. The caller
// retains its reservation on EVERY return, transferring only after publication.
func OwnContentWithin(stamp recording.Stamp, in ContentInput, reservation *recording.Reservation) (*OwnedContent, error) {
	if reservation == nil || !stamp.Current() || !stamp.MatchesCapture(in.identity.Scope.Capture) || stamp.Fence() != in.identity.Scope.Fence || len(in.raw) == 0 || int64(len(in.raw)) > in.limits.ResultBytes || in.limits.validate() != nil || in.observedAt.IsZero() || in.observedAt.Before(in.identity.CreatedAt) || (in.part != nativetool.ContentToolArguments && in.part != nativetool.ContentToolResult) {
		return nil, nativetool.ErrInvalid
	}
	if in.profile.contract == "" || in.profile.definition == (nativetool.Digest{}) {
		return nil, errContentSuppressed
	}
	for _, paths := range [][]string{in.profile.arguments, in.profile.result} {
		if len(paths) > 64 {
			return nil, nativetool.ErrInvalid
		}
		for _, path := range paths {
			if len(path) == 0 || len(path) > 512 || path[0] != '/' {
				return nil, nativetool.ErrInvalid
			}
		}
	}
	p := contentPreparation{reservation: reservation, limits: in.limits}
	// Bounded identity validation (including URI parsing), detached identity and
	// queue/writer scalar envelopes have no payload-sized allocations.
	if err := p.grow(32 << 10); err != nil {
		return nil, err
	}
	if err := in.identity.Validate(); err != nil {
		return nil, err
	}
	object, err := p.object(in.raw)
	if err != nil {
		return nil, err
	}
	paths := in.profile.arguments
	if in.part == nativetool.ContentToolResult {
		paths = in.profile.result
	}
	for _, path := range paths {
		present, err := p.contains(object, path)
		if err != nil {
			return nil, err
		}
		if present {
			return nil, errContentSuppressed
		}
	}
	body := in.raw
	if in.part == nativetool.ContentToolResult {
		body, err = p.result(body, object)
		if err != nil {
			return nil, err
		}
	}
	if len(body) > 16<<20 {
		return nil, nativetool.ErrInvalid
	}
	// Clone even unchanged safe input; encoder buffers are never assumed to
	// transfer their capacity implicitly. Allow allocator size-class rounding.
	if err := p.grow(int64(len(body) + len(body)/4 + 128)); err != nil {
		return nil, err
	}
	body = bytes.Clone(body)
	// Split can grow its descriptor array; 4*24 bytes per maximum chunk covers
	// its old/new backing overlap. Three 32-byte manifests cover owned/SQL/driver
	// copies. Four maximum chunks cover EncodeAll's returned buffer and driver
	// overlap even when identity encoding is selected. Shared codec caches are
	// fixed owner memory, not per-observation payloads.
	chunks := int64(len(body)/evidencechunk.MinimumBytes + 1)
	if err := p.grow(chunks*(4*24+3*32) + 4*evidencechunk.MaximumBytes + 64*1024); err != nil {
		return nil, err
	}
	i := in.identity
	i.OperationID = nativetool.ID(strings.Clone(string(i.OperationID)))
	i.Scope.Capture = captureidentity.Reference{Kind: captureidentity.Kind(strings.Clone(string(i.Scope.Capture.Kind))), ID: strings.Clone(i.Scope.Capture.ID)}
	i.EnvironmentID = environment.EnvironmentID(strings.Clone(string(i.EnvironmentID)))
	i.Kind = nativetool.RequestKind(strings.Clone(string(i.Kind)))
	i.ToolName = strings.Clone(i.ToolName)
	i.TargetOrigin = strings.Clone(i.TargetOrigin)
	i.CredentialOwner = strings.Clone(i.CredentialOwner)
	i.Recording.Mode = environment.ContentRecordingMode(strings.Clone(string(i.Recording.Mode)))
	i.CreatedAt = i.CreatedAt.UTC()
	i.ExpiresAt = i.ExpiresAt.UTC()
	size := uint64(unsafe.Sizeof(OwnedContent{})) + uint64(len(body)+len(i.OperationID)+len(i.Scope.Capture.Kind)+len(i.Scope.Capture.ID)+len(i.EnvironmentID)+len(i.Kind)+len(i.ToolName)+len(i.TargetOrigin)+len(i.CredentialOwner)+len(i.Recording.Mode)+len(in.part))
	return &OwnedContent{observation: nativetool.ContentObservation{Identity: i, ObservedAt: in.observedAt.UTC(), Part: nativetool.ContentPart(strings.Clone(string(in.part))), Body: body}, bytes: size}, nil
}
