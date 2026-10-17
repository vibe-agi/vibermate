package rawevidence

import (
	"crypto/sha256"
	"encoding/json"
	"hash"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/recording"
)

type CaptureMetadata struct {
	Representation                            string
	Context                                   Context
	Layer                                     Layer
	Method, Scheme, Authority, Path, RawQuery string
	StatusCode                                int
	Headers                                   http.Header
	ProtectedHeaderNames                      []string
	ContentType, ContentEncoding              string
}
type TerminalMetadata struct {
	Trailers         http.Header
	Complete         bool
	IncompleteReason string
}

// CaptureObserver supports the transitional context-owned scope lifecycle. The
// stamped API is for control composition once it publishes recording tokens.
type CaptureObserver interface {
	Capture(CaptureMetadata, int) *CaptureBuffer
}

type CaptureBuffer struct {
	mu          sync.Mutex
	manager     *Manager
	reservation *recording.Reservation
	stamp       recording.Stamp
	stamped     bool
	record      StoredEnvelope
	payload     Payload
	protected   []string
	body        []byte
	frames      []Frame
	hash        hash.Hash
	total       int64
	weight      int64
	maximum     int
	frozen      bool
	done        bool
}

// observationCharge includes retained metadata, redaction scratch, JSON's worst
// case six-byte escaping, encoder overlap, record descriptors and driver binds.
// Keep this conservative reservation until settlement rather than pretending
// len(encoded) bounds the temporary encoding workspace.
func observationCharge(o Observation) int64 {
	n := int64(32<<10) + int64(len(o.Body)) + int64(len(o.Frames))*1024
	for _, value := range []string{o.ScopeID, o.ExchangeID, o.ConnectionID, o.AttemptID, o.EnvironmentID, o.EnvironmentDigest, o.ClientEndpointID, o.UpstreamEndpointID, o.ProtocolPlanID, o.RouteID, o.AccountID, o.Method, o.Scheme, o.Authority, o.Path, o.RawQuery, o.ContentType, o.ContentEncoding, o.Representation} {
		n += int64(len(value)) * 2
	}
	for _, h := range []http.Header{o.Headers, o.Trailers} {
		for k, vs := range h {
			n += int64(len(k))*32 + 1024
			for _, v := range vs {
				n += int64(len(v))*32 + 2048
			}
		}
	}
	for _, name := range o.ProtectedHeaderNames {
		n += int64(len(name))*16 + 256
	}
	return n
}

func (m *Manager) TryCapture(stamp recording.Stamp, meta CaptureMetadata) *CaptureBuffer {
	if m == nil {
		return nil
	}
	if !stamp.Current() {
		m.budget.RecordOffer(recording.Raw, recording.DroppedStale, 0)
		return nil
	}
	if !stampMatches(stamp, meta.Context) {
		m.budget.RecordOffer(recording.Raw, recording.DroppedInvalid, 0)
		return nil
	}
	b := m.Capture(meta, DefaultMaximumBodyBytes)
	if b != nil {
		b.stamp = stamp
		b.stamped = true
	}
	return b
}

func (m *Manager) Capture(meta CaptureMetadata, maximum int) *CaptureBuffer {
	if m == nil {
		return nil
	}
	o := Observation{
		Context:              meta.Context,
		Layer:                meta.Layer,
		Method:               meta.Method,
		Scheme:               meta.Scheme,
		Authority:            meta.Authority,
		Path:                 meta.Path,
		RawQuery:             meta.RawQuery,
		StatusCode:           meta.StatusCode,
		Headers:              meta.Headers,
		ProtectedHeaderNames: meta.ProtectedHeaderNames,
		ContentType:          meta.ContentType,
		ContentEncoding:      meta.ContentEncoding,
		Complete:             true,
		Representation:       "http_message",
		ObservedAt:           m.clock.Now().UTC(),
	}
	if meta.Representation != "" {
		o.Representation = meta.Representation
	}
	weight := observationCharge(o)
	r, ok := m.budget.TryReserve(recording.Raw, weight)
	if !ok {
		m.budget.RecordOffer(recording.Raw, recording.DroppedCapacity, 0)
		return nil
	}
	reject := func(result recording.OfferResult) *CaptureBuffer {
		r.Release()
		m.budget.RecordOffer(recording.Raw, result, 0)
		return nil
	}
	if o.validate() != nil {
		return reject(recording.DroppedInvalid)
	}
	if meta.Context.Recording == RecordingOff {
		return reject(recording.DroppedUnavailable)
	}
	m.admissionMu.Lock()
	open := m.accepting
	m.scopeMu.Lock()
	state := m.scopes[scopeKey{kind: meta.Context.ScopeKind, id: meta.Context.ScopeID}]
	if state != nil && state.phase == scopeSealed {
		open = false
	}
	m.scopeMu.Unlock()
	m.admissionMu.Unlock()
	if !open {
		return reject(recording.DroppedClosed)
	}
	draft, err := m.prepare(o)
	if err != nil {
		return reject(recording.DroppedInvalid)
	}
	var payload Payload
	if len(draft.PayloadMetadata) > 0 {
		var stored storedPayloadMetadata
		if json.Unmarshal(draft.PayloadMetadata, &stored) != nil {
			return reject(recording.DroppedInvalid)
		}
		payload = Payload{Version: 1, Headers: stored.Headers}
	} else {
		payload.Version = 1
	}
	draft.PayloadMetadata = nil
	if maximum < 0 {
		maximum = 0
	}
	maximum = min(maximum, DefaultMaximumBodyBytes)
	protected := slices.Clone(meta.ProtectedHeaderNames)
	for i := range protected {
		protected[i] = strings.Clone(protected[i])
	}
	return &CaptureBuffer{manager: m, reservation: r, record: draft, payload: payload, protected: protected, hash: sha256.New(), weight: weight, maximum: maximum}
}

func (b *CaptureBuffer) ObserveBytes(data []byte, kind FrameKind) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return
	}
	b.total += int64(len(data))
	_, _ = b.hash.Write(data)
	if b.frozen || b.record.PayloadState == PayloadMetadataOnly {
		return
	}
	n := min(len(data), b.maximum-len(b.body))
	if n == 0 {
		if len(data) > 0 {
			b.frozen = true
		}
		return
	}
	if kind != "" && kind != FrameData && kind != FrameKeepalive && kind != FrameAbort {
		b.frozen = true
		return
	}
	oldBody, oldFrames := cap(b.body), cap(b.frames)
	newBody := oldBody
	if len(b.body)+n > oldBody {
		newBody = min(b.maximum, max(len(b.body)+n, max(4096, oldBody*2)))
	}
	newFrames := oldFrames
	if kind != "" && len(b.frames) == oldFrames {
		newFrames = max(8, oldFrames*2)
	}
	var charge int64
	if newBody != oldBody {
		charge += int64(newBody)
	}
	// Frame capacity also reserves its future JSON and encoder workspace.
	if newFrames != oldFrames {
		charge += int64(newFrames) * 1024
	}
	if !b.reservation.TryGrow(charge) {
		b.frozen = true
		return
	}
	b.weight += charge
	if newBody != oldBody {
		next := make([]byte, len(b.body), newBody)
		copy(next, b.body)
		b.body = next
		b.reservation.Shrink(int64(oldBody))
		b.weight -= int64(oldBody)
	}
	if newFrames != oldFrames {
		next := make([]Frame, len(b.frames), newFrames)
		copy(next, b.frames)
		b.frames = next
		b.reservation.Shrink(int64(oldFrames) * 1024)
		b.weight -= int64(oldFrames) * 1024
	}
	offset := len(b.body)
	b.body = append(b.body, data[:n]...)
	if kind != "" {
		b.frames = append(b.frames, Frame{Kind: kind, Offset: int64(offset), Length: int64(n)})
	}
	if n < len(data) {
		b.frozen = true
	}
}

func (b *CaptureBuffer) Finish(terminal TerminalMetadata) recording.OfferResult {
	if b == nil {
		return recording.DroppedUnavailable
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return recording.DroppedClosed
	}
	b.done = true
	result := recording.DroppedInvalid
	defer func() {
		b.manager.budget.RecordOffer(recording.Raw, result, uint64(len(b.body)+len(b.record.PayloadMetadata)))
		if result != recording.Accepted {
			b.reservation.Release()
		}
		b.body = nil
		b.frames = nil
		b.payload = Payload{}
		b.record = StoredEnvelope{}
		b.protected = nil
		b.hash = nil
	}()
	if b.stamped && !b.stamp.Current() {
		result = recording.DroppedStale
		return result
	}
	if (!terminal.Complete && !validObservationReason(terminal.IncompleteReason)) || (terminal.Complete && terminal.IncompleteReason != "") {
		return result
	}
	extra := observationCharge(Observation{Trailers: terminal.Trailers}) - (32 << 10)
	if !b.reservation.TryGrow(extra) {
		result = recording.DroppedCapacity
		return result
	}
	b.weight += extra
	b.record.TrailerCount = headerValueCount(terminal.Trailers)
	b.record.BodyBytes = b.total
	copy(b.record.BodySHA256[:], b.hash.Sum(nil))
	b.record.DigestScope = DigestObservedPrefix
	if terminal.Complete {
		b.record.DigestScope = DigestFull
	}
	if b.record.PayloadState != PayloadMetadataOnly {
		b.record.Body = b.body
		b.record.PayloadState = PayloadCaptured
		if !terminal.Complete || b.total != int64(len(b.body)) {
			b.record.PayloadState = PayloadTruncated
			b.record.PayloadReason = terminal.IncompleteReason
			if b.record.PayloadReason == "" {
				b.record.PayloadReason = "response_payload_limit"
				if b.record.Layer == LayerClientIngress || b.record.Layer == LayerProviderEgress || b.record.Layer == LayerTransformRequestInput {
					b.record.PayloadReason = "request_payload_limit"
				}
				if b.record.Layer == LayerTransformResponseInput {
					b.record.PayloadReason = "transform_input_limit"
				}
			}
		}
		b.payload.Trailers = canonicalHeadersWithProtected(terminal.Trailers, b.manager.redactor, protectedHeaderSet(b.protected))
		b.payload.Frames = b.frames
		encoded, err := b.payload.MarshalMetadata()
		if err != nil {
			return result
		}
		b.record.PayloadMetadata = encoded
		b.record.RedactedCredentialFields = redactedNamesOf(b.payload.Headers, b.payload.Trailers)
	}
	_, result = b.manager.publish(b.record, b.reservation, b.weight, b.stamp)
	return result
}

func (b *CaptureBuffer) Discard() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return
	}
	b.done = true
	b.reservation.Release()
	b.manager.budget.RecordOffer(recording.Raw, recording.DroppedClosed, uint64(len(b.body)))
	b.body = nil
	b.frames = nil
	b.payload = Payload{}
	b.record = StoredEnvelope{}
	b.protected = nil
	b.hash = nil
}

func stampMatches(stamp recording.Stamp, ctx Context) bool {
	switch ctx.ScopeKind {
	case ScopeRuntime:
		return stamp.MatchesCapture(captureidentity.Reference{})
	case ScopeManagedRun:
		return stamp.MatchesCapture(captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: ctx.ScopeID})
	case ScopeManualCapture:
		return stamp.MatchesCapture(captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: ctx.ScopeID})
	default:
		return false
	}
}

func validObservationReason(reason string) bool {
	switch reason {
	case "response_read_failed", "response_closed_before_eof", "response_payload_limit", "request_payload_limit", "transform_input_limit", "recording_metadata_only", "transport_failed", "incomplete_response", "response_stream_unavailable":
		return true
	default:
		return false
	}
}
