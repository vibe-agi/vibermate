// Package rawevidence owns append-only HTTP envelope evidence.
//
// It intentionally models the HTTP message visible at ViberMate's Go
// boundary. Body bytes and repeated header values are exact. Header field
// spelling and cross-field order are normalized by net/http before this
// boundary and are therefore never claimed to be a packet capture.
package rawevidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"
)

const (
	MaxIdentityBytes        = 512
	MaxReasonBytes          = 128
	DefaultMaximumBodyBytes = 16 << 20
	DefaultObservationLimit = 2 * time.Second

	maxSQLiteInteger          uint64 = 1<<63 - 1
	maxEnvironmentDigestBytes        = 128
	maxHTTPMethodBytes               = 32
	maxHTTPSchemeBytes               = 16
	maxHTTPMetadataBytes             = 4096
	httpCanonicalization             = "go_net_http_v1"
)

var (
	ErrEnvelopeNotFound   = errors.New("raw evidence envelope was not found")
	ErrPayloadUnavailable = errors.New("raw evidence payload is unavailable")
	ErrInvalidReveal      = errors.New("raw evidence reveal request is invalid")
	ErrInvalidRead        = errors.New("raw evidence read request is invalid")
)

type Layer string

const (
	LayerClientIngress          Layer = "client_ingress"
	LayerTransformRequestInput  Layer = "transform_request_input"
	LayerProviderEgress         Layer = "provider_egress"
	LayerProviderResponse       Layer = "provider_response"
	LayerTransformResponseInput Layer = "transform_response_input"
	LayerClientDownstream       Layer = "client_downstream"
)

func (layer Layer) Valid() bool {
	switch layer {
	case LayerClientIngress, LayerTransformRequestInput, LayerProviderEgress,
		LayerProviderResponse, LayerTransformResponseInput, LayerClientDownstream:
		return true
	default:
		return false
	}
}

type ScopeKind string

const (
	ScopeRuntime       ScopeKind = "runtime"
	ScopeManagedRun    ScopeKind = "managed_run"
	ScopeManualCapture ScopeKind = "manual_capture"
)

func (kind ScopeKind) Valid() bool {
	switch kind {
	case ScopeRuntime, ScopeManagedRun, ScopeManualCapture:
		return true
	default:
		return false
	}
}

type RecordingMode string

const (
	RecordingFull         RecordingMode = "full"
	RecordingMetadataOnly RecordingMode = "metadata_only"
	RecordingOff          RecordingMode = "off"
)

func (mode RecordingMode) Valid() bool {
	switch mode {
	case RecordingFull, RecordingMetadataOnly, RecordingOff:
		return true
	default:
		return false
	}
}

type PayloadState string

const (
	PayloadCaptured     PayloadState = "captured"
	PayloadMetadataOnly PayloadState = "metadata_only"
	PayloadTruncated    PayloadState = "truncated"
	PayloadUnavailable  PayloadState = "unavailable"
)

func (state PayloadState) Valid() bool {
	switch state {
	case PayloadCaptured, PayloadMetadataOnly,
		PayloadTruncated, PayloadUnavailable:
		return true
	default:
		return false
	}
}

type DigestScope string

const (
	DigestFull           DigestScope = "full_body"
	DigestObservedPrefix DigestScope = "observed_prefix"
	DigestUnavailable    DigestScope = "unavailable"
)

func (scope DigestScope) Valid() bool {
	switch scope {
	case DigestFull, DigestObservedPrefix, DigestUnavailable:
		return true
	default:
		return false
	}
}

type FrameKind string

const (
	FrameData      FrameKind = "data"
	FrameKeepalive FrameKind = "keepalive"
	FrameAbort     FrameKind = "abort"
)

type Frame struct {
	Kind   FrameKind `json:"kind"`
	Offset int64     `json:"offset"`
	Length int64     `json:"length"`
}

func (frame Frame) validate(bodyBytes int) error {
	switch frame.Kind {
	case FrameData, FrameKeepalive, FrameAbort:
	default:
		return errors.New("raw evidence frame kind is invalid")
	}
	if frame.Offset < 0 || frame.Length <= 0 ||
		frame.Offset+frame.Length > int64(bodyBytes) {
		return errors.New("raw evidence frame range is invalid")
	}
	return nil
}

// Context carries only frozen authority references. Raw message content belongs
// to Payload, and credential header values are removed before a Payload exists.
type Context struct {
	ScopeKind                ScopeKind
	ScopeID                  string
	ExchangeID               string
	ConnectionID             string
	AttemptID                string
	EnvironmentID            string
	EnvironmentRevision      uint64
	EnvironmentDigest        string
	ClientEndpointID         string
	ClientEndpointRevision   uint64
	UpstreamEndpointID       string
	UpstreamEndpointRevision uint64
	ProtocolPlanID           string
	ProtocolPlanRevision     uint64
	RouteID                  string
	RouteRevision            uint64
	AccountID                string
	AccountRevision          uint64
	CredentialEpoch          uint64
	Recording                RecordingMode
	RetentionDays            uint16
}

func (value Context) validate() error {
	if !value.ScopeKind.Valid() || !validIdentity(value.ExchangeID) ||
		!value.Recording.Valid() {
		return errors.New("raw evidence context is invalid")
	}
	if value.ScopeKind == ScopeRuntime {
		if value.ScopeID != "" {
			return errors.New("runtime raw evidence cannot name a capture scope")
		}
	} else if !validIdentity(value.ScopeID) {
		return errors.New("raw evidence capture scope is invalid")
	}
	for _, identity := range []string{
		value.ConnectionID, value.AttemptID, value.EnvironmentID,
		value.ClientEndpointID, value.UpstreamEndpointID,
		value.ProtocolPlanID, value.RouteID,
		value.AccountID,
	} {
		if identity != "" && !validIdentity(identity) {
			return errors.New("raw evidence authority identity is invalid")
		}
	}
	for _, revision := range []uint64{
		value.EnvironmentRevision, value.ClientEndpointRevision,
		value.UpstreamEndpointRevision, value.ProtocolPlanRevision,
		value.RouteRevision, value.AccountRevision, value.CredentialEpoch,
	} {
		if revision > maxSQLiteInteger {
			return errors.New("raw evidence authority revision is invalid")
		}
	}
	if !validOptionalMetadata(
		value.EnvironmentDigest,
		maxEnvironmentDigestBytes,
	) {
		return errors.New("raw evidence environment digest is invalid")
	}
	if value.Recording == RecordingOff {
		if value.RetentionDays != 0 {
			return errors.New("disabled raw evidence has retention")
		}
	} else if value.RetentionDays == 0 {
		return errors.New("enabled raw evidence has no retention")
	}
	return nil
}

// Validate permits an upstream boundary to reject an unfrozen or internally
// inconsistent evidence context before any network byte is sent.
func (value Context) Validate() error { return value.validate() }

// Observation is one bounded HTTP message at a real ViberMate boundary.
// Complete reports whether Body covers the entire message body. A false value
// must carry one stable reason and is never silently upgraded to a full digest.
type Observation struct {
	Context
	Layer      Layer
	ObservedAt time.Time
	Method     string
	StatusCode int
	Scheme     string
	Authority  string
	Path       string
	RawQuery   string
	Headers    http.Header
	Trailers   http.Header
	// ProtectedHeaderNames extends the fail-closed credential-name predicate for
	// exact Account-owned Header assignments. Values named here are redacted at
	// observation admission even when their names do not look credential-like.
	ProtectedHeaderNames []string
	Body                 []byte
	// TotalBodyBytes is the number of bytes observed for the complete message.
	// Zero derives from len(Body). It may exceed len(Body) when Body is a
	// retained prefix of a fully hashed streaming message.
	TotalBodyBytes int64
	// BodySHA256 may carry a digest calculated while streaming bytes that were
	// not all retained in Body. DigestAvailable distinguishes it from the zero
	// value. Complete observations do not need to supply it.
	BodySHA256      [sha256.Size]byte
	DigestAvailable bool
	// FullDigestAvailable is true only when BodySHA256 covers the complete
	// message, even if Body retains only a bounded prefix.
	FullDigestAvailable bool
	Frames              []Frame
	Complete            bool
	Unavailable         bool
	IncompleteReason    string
	Representation      string
	ContentType         string
	ContentEncoding     string
}

func (value Observation) validate() error {
	if err := value.Context.validate(); err != nil {
		return err
	}
	if !value.Layer.Valid() ||
		(value.ObservedAt.IsZero() == false && value.ObservedAt.Location() == nil) {
		return errors.New("raw evidence observation is invalid")
	}
	if value.Method != "" &&
		(len(value.Method) > maxHTTPMethodBytes || !validMethod(value.Method)) {
		return errors.New("raw evidence HTTP method is invalid")
	}
	if value.StatusCode != 0 && (value.StatusCode < 100 || value.StatusCode > 599) {
		return errors.New("raw evidence HTTP status is invalid")
	}
	if !validOptionalMetadata(value.Scheme, maxHTTPSchemeBytes) {
		return errors.New("raw evidence HTTP scheme is invalid")
	}
	for _, item := range []string{
		value.Authority, value.Path, value.RawQuery,
		value.Representation, value.ContentType, value.ContentEncoding,
	} {
		if !validOptionalMetadata(item, maxHTTPMetadataBytes) {
			return errors.New("raw evidence HTTP metadata is invalid")
		}
	}
	if len(value.ProtectedHeaderNames) > 64 {
		return errors.New("raw evidence has too many protected Header names")
	}
	protected := make(map[string]struct{}, len(value.ProtectedHeaderNames))
	for _, name := range value.ProtectedHeaderNames {
		canonical := http.CanonicalHeaderKey(name)
		key := strings.ToLower(canonical)
		if canonical == "" || canonical != name || len(name) > 256 ||
			!httpguts.ValidHeaderFieldName(name) {
			return errors.New("raw evidence protected Header name is invalid")
		}
		if _, duplicate := protected[key]; duplicate {
			return errors.New("raw evidence protected Header name is duplicated")
		}
		protected[key] = struct{}{}
	}
	if value.Complete && value.IncompleteReason != "" {
		return errors.New("complete raw evidence has an incomplete reason")
	}
	if !value.Complete && !validReason(value.IncompleteReason) {
		return errors.New("incomplete raw evidence has no stable reason")
	}
	if value.Unavailable && (value.Complete || len(value.Body) != 0 ||
		value.TotalBodyBytes != 0 || value.DigestAvailable ||
		value.FullDigestAvailable || len(value.Frames) != 0) {
		return errors.New("unavailable raw evidence contains body evidence")
	}
	if value.FullDigestAvailable && !value.DigestAvailable {
		return errors.New("raw evidence full digest is not available")
	}
	totalBodyBytes := value.TotalBodyBytes
	if totalBodyBytes == 0 {
		totalBodyBytes = int64(len(value.Body))
	}
	if totalBodyBytes < int64(len(value.Body)) ||
		(value.Complete && totalBodyBytes != int64(len(value.Body))) {
		return errors.New("raw evidence body byte count is invalid")
	}
	if value.DigestAvailable && value.BodySHA256 == [sha256.Size]byte{} {
		return errors.New("raw evidence supplied body digest is invalid")
	}
	for _, frame := range value.Frames {
		if err := frame.validate(len(value.Body)); err != nil {
			return err
		}
	}
	return nil
}

// HeaderField preserves a header's name, ordering and multiplicity as wire
// evidence. A credential field carries Redacted instead of Values: the value
// itself is replaced at the observation boundary and never reaches storage.
type HeaderField struct {
	Name     string          `json:"name"`
	Values   []string        `json:"values,omitempty"`
	Redacted []RedactedValue `json:"redacted,omitempty"`
}

// Payload is the complete observed HTTP message, assembled for readers. The
// store keeps its metadata and its body in separate columns: a []byte inside a
// JSON document is base64-expanded, and body bytes are the bulk of every
// observation.
type Payload struct {
	Version  uint8
	Headers  []HeaderField
	Trailers []HeaderField
	Body     []byte
	Frames   []Frame
}

// storedPayloadMetadata is a Payload without its body.
type storedPayloadMetadata struct {
	Version  uint8         `json:"version"`
	Headers  []HeaderField `json:"headers"`
	Trailers []HeaderField `json:"trailers,omitempty"`
	Frames   []Frame       `json:"frames,omitempty"`
}

// payloadOf builds the metadata half of the stored payload and reports, in
// canonical order, the credential header fields whose values it removed. The
// names are
// returned rather than recomputed by a second traversal so the stored list is a
// projection of the redaction that happened, not a parallel claim about it —
// and so an envelope that never built a payload cannot report one.
//
// It deliberately does not take the body. Bodies are content-addressed
// separately and MarshalMetadata excludes them, so a body handed to this
// function could only be copied and dropped — which is what it used to do, once
// per observation, for messages the schema allows to reach 32 MiB.
func payloadOf(
	observation Observation,
	frames []Frame,
	redactor Redactor,
) (Payload, []string, error) {
	// A payload cannot be built without a bound redactor. Credential header
	// removal is not a property of correct wiring; it fails closed here so a
	// construction mistake cannot silently store a value.
	if !redactor.bound() {
		return Payload{}, nil, errors.New("raw evidence redactor is not bound")
	}
	protected := protectedHeaderSet(observation.ProtectedHeaderNames)
	headers := canonicalHeadersWithProtected(observation.Headers, redactor, protected)
	trailers := canonicalHeadersWithProtected(observation.Trailers, redactor, protected)
	return Payload{
		Version:  1,
		Headers:  headers,
		Trailers: trailers,
		Frames:   slices.Clone(frames),
	}, redactedNamesOf(headers, trailers), nil
}

// redactedNamesOf lists, in canonical order, the fields the payload it was built
// from actually lost values for.
func redactedNamesOf(sets ...[]HeaderField) []string {
	seen := make(map[string]struct{})
	for _, fields := range sets {
		for _, field := range fields {
			if len(field.Redacted) > 0 {
				seen[field.Name] = struct{}{}
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func protectedHeaderSet(names []string) map[string]struct{} {
	if len(names) == 0 {
		return nil
	}
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		result[strings.ToLower(name)] = struct{}{}
	}
	return result
}

func canonicalHeaders(
	headers http.Header,
	redactor Redactor,
) []HeaderField {
	return canonicalHeadersWithProtected(headers, redactor, nil)
}

func canonicalHeadersWithProtected(
	headers http.Header,
	redactor Redactor,
	protected map[string]struct{},
) []HeaderField {
	if len(headers) == 0 {
		return nil
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, http.CanonicalHeaderKey(name))
	}
	sort.Strings(names)
	fields := make([]HeaderField, 0, len(names))
	for _, name := range names {
		_, force := protected[strings.ToLower(name)]
		fields = append(fields, redactor.protectedField(name, headers.Values(name), force))
	}
	return fields
}

// MarshalMetadata renders everything about the message except its body.
func (payload Payload) MarshalMetadata() ([]byte, error) {
	if payload.Version != 1 {
		return nil, errors.New("raw evidence payload version is invalid")
	}
	return json.Marshal(storedPayloadMetadata{
		Version:  payload.Version,
		Headers:  payload.Headers,
		Trailers: payload.Trailers,
		Frames:   payload.Frames,
	})
}

// DecodePayload rejoins stored metadata with stored body bytes.
func DecodePayload(encodedMetadata, body []byte) (Payload, error) {
	decoder := json.NewDecoder(bytes.NewReader(encodedMetadata))
	decoder.DisallowUnknownFields()
	var metadata storedPayloadMetadata
	if err := decoder.Decode(&metadata); err != nil {
		return Payload{}, fmt.Errorf("decode raw evidence payload: %w", err)
	}
	if metadata.Version != 1 {
		return Payload{}, errors.New("raw evidence payload version is unsupported")
	}
	return Payload{
		Version:  metadata.Version,
		Headers:  metadata.Headers,
		Trailers: metadata.Trailers,
		Body:     body,
		Frames:   metadata.Frames,
	}, nil
}

// StoredEnvelope contains safe searchable metadata plus the observed payload.
// The payload is stored in the clear, and credential header values were removed
// before it existed.
type StoredEnvelope struct {
	EnvelopeID               string
	WriterID                 string
	Watermark                uint64
	Layer                    Layer
	ScopeKind                ScopeKind
	ScopeID                  string
	ExchangeID               string
	ConnectionID             string
	AttemptID                string
	EnvironmentID            string
	EnvironmentRevision      uint64
	EnvironmentDigest        string
	ClientEndpointID         string
	ClientEndpointRevision   uint64
	UpstreamEndpointID       string
	UpstreamEndpointRevision uint64
	ProtocolPlanID           string
	ProtocolPlanRevision     uint64
	RouteID                  string
	RouteRevision            uint64
	AccountID                string
	AccountRevision          uint64
	CredentialEpoch          uint64
	ObservedAt               time.Time
	ExpiresAt                time.Time
	Method                   string
	StatusCode               int
	Scheme                   string
	Authority                string
	Path                     string
	RawQuery                 string
	ContentType              string
	ContentEncoding          string
	Representation           string
	Canonicalization         string
	HeaderCount              int
	TrailerCount             int
	BodyBytes                int64
	BodySHA256               [sha256.Size]byte
	DigestScope              DigestScope
	PayloadState             PayloadState
	PayloadReason            string
	RedactedCredentialFields []string
	// PayloadMetadata is the marshalled headers, trailers and frames of a
	// captured or truncated observation, and empty otherwise. Body carries the
	// observed body bytes unchanged. Both are stored in the clear:
	// INV-STORE-DISCLOSED forbids application-layer field encryption, and
	// credential header values were removed before either existed. Body is the
	// observation as sent, so a secret the client put inside it is retained.
	PayloadMetadata []byte
	Body            []byte
}

// EnvelopeMetadata is the only Raw envelope shape safe for ordinary control
// reads. It cannot carry a payload by construction.
type EnvelopeMetadata struct {
	EnvelopeID               string
	Layer                    Layer
	ScopeKind                ScopeKind
	ScopeID                  string
	ExchangeID               string
	ConnectionID             string
	AttemptID                string
	EnvironmentID            string
	EnvironmentRevision      uint64
	EnvironmentDigest        string
	ClientEndpointID         string
	ClientEndpointRevision   uint64
	UpstreamEndpointID       string
	UpstreamEndpointRevision uint64
	ProtocolPlanID           string
	ProtocolPlanRevision     uint64
	RouteID                  string
	RouteRevision            uint64
	AccountID                string
	AccountRevision          uint64
	CredentialEpoch          uint64
	ObservedAt               time.Time
	ExpiresAt                time.Time
	Method                   string
	StatusCode               int
	Scheme                   string
	Authority                string
	Path                     string
	RawQuery                 string
	ContentType              string
	ContentEncoding          string
	Representation           string
	Canonicalization         string
	HeaderCount              int
	TrailerCount             int
	BodyBytes                int64
	BodySHA256               [sha256.Size]byte
	DigestScope              DigestScope
	PayloadState             PayloadState
	PayloadReason            string
	RedactedCredentialFields []string
}

func MetadataOf(value StoredEnvelope) EnvelopeMetadata {
	return EnvelopeMetadata{
		EnvelopeID: value.EnvelopeID, Layer: value.Layer,
		ScopeKind: value.ScopeKind, ScopeID: value.ScopeID,
		ExchangeID: value.ExchangeID, ConnectionID: value.ConnectionID,
		AttemptID: value.AttemptID, EnvironmentID: value.EnvironmentID,
		EnvironmentRevision:      value.EnvironmentRevision,
		EnvironmentDigest:        value.EnvironmentDigest,
		ClientEndpointID:         value.ClientEndpointID,
		ClientEndpointRevision:   value.ClientEndpointRevision,
		UpstreamEndpointID:       value.UpstreamEndpointID,
		UpstreamEndpointRevision: value.UpstreamEndpointRevision,
		ProtocolPlanID:           value.ProtocolPlanID,
		ProtocolPlanRevision:     value.ProtocolPlanRevision,
		RouteID:                  value.RouteID, RouteRevision: value.RouteRevision,
		AccountID: value.AccountID, AccountRevision: value.AccountRevision,
		CredentialEpoch: value.CredentialEpoch,
		ObservedAt:      value.ObservedAt, ExpiresAt: value.ExpiresAt,
		Method: value.Method, StatusCode: value.StatusCode,
		Scheme: value.Scheme, Authority: value.Authority, Path: value.Path,
		RawQuery: value.RawQuery, ContentType: value.ContentType,
		ContentEncoding:  value.ContentEncoding,
		Representation:   value.Representation,
		Canonicalization: value.Canonicalization,
		HeaderCount:      value.HeaderCount, TrailerCount: value.TrailerCount,
		BodyBytes: value.BodyBytes, BodySHA256: value.BodySHA256,
		DigestScope: value.DigestScope, PayloadState: value.PayloadState,
		PayloadReason:            value.PayloadReason,
		RedactedCredentialFields: slices.Clone(value.RedactedCredentialFields),
	}
}

func (value StoredEnvelope) Validate() error {
	if !validIdentity(value.EnvelopeID) || !validIdentity(value.WriterID) ||
		value.Watermark == 0 || value.Watermark > maxSQLiteInteger ||
		!value.Layer.Valid() ||
		!value.ScopeKind.Valid() || !validIdentity(value.ExchangeID) ||
		value.ObservedAt.IsZero() || value.ExpiresAt.IsZero() ||
		!value.ExpiresAt.After(value.ObservedAt) ||
		value.ExpiresAt.UnixMilli() <= value.ObservedAt.UnixMilli() ||
		!value.PayloadState.Valid() || !value.DigestScope.Valid() ||
		value.HeaderCount < 0 || value.TrailerCount < 0 || value.BodyBytes < 0 {
		return errors.New("stored raw evidence metadata is invalid")
	}
	if value.ScopeKind == ScopeRuntime {
		if value.ScopeID != "" {
			return errors.New("runtime raw evidence has a capture scope")
		}
	} else if !validIdentity(value.ScopeID) {
		return errors.New("stored raw evidence scope is invalid")
	}
	for _, identity := range []string{
		value.ConnectionID, value.AttemptID, value.EnvironmentID,
		value.ClientEndpointID, value.UpstreamEndpointID,
		value.ProtocolPlanID, value.RouteID, value.AccountID,
	} {
		if !validOptionalIdentity(identity) {
			return errors.New("stored raw evidence authority identity is invalid")
		}
	}
	for _, revision := range []uint64{
		value.EnvironmentRevision, value.ClientEndpointRevision,
		value.UpstreamEndpointRevision, value.ProtocolPlanRevision,
		value.RouteRevision, value.AccountRevision, value.CredentialEpoch,
	} {
		if revision > maxSQLiteInteger {
			return errors.New("stored raw evidence revision is invalid")
		}
	}
	if !validOptionalMetadata(
		value.EnvironmentDigest,
		maxEnvironmentDigestBytes,
	) {
		return errors.New("stored raw evidence environment digest is invalid")
	}
	if value.Method != "" &&
		(len(value.Method) > maxHTTPMethodBytes || !validMethod(value.Method)) {
		return errors.New("stored raw evidence HTTP method is invalid")
	}
	if value.StatusCode != 0 &&
		(value.StatusCode < 100 || value.StatusCode > 599) {
		return errors.New("stored raw evidence HTTP status is invalid")
	}
	if !validOptionalMetadata(value.Scheme, maxHTTPSchemeBytes) {
		return errors.New("stored raw evidence HTTP scheme is invalid")
	}
	for _, metadata := range []string{
		value.Authority, value.Path, value.RawQuery, value.ContentType,
		value.ContentEncoding, value.Representation,
	} {
		if !validOptionalMetadata(metadata, maxHTTPMetadataBytes) {
			return errors.New("stored raw evidence HTTP metadata is invalid")
		}
	}
	if value.Canonicalization != httpCanonicalization {
		return errors.New("stored raw evidence canonicalization is invalid")
	}
	if value.PayloadState == PayloadCaptured || value.PayloadState == PayloadTruncated {
		if len(value.PayloadMetadata) == 0 {
			return errors.New("captured raw evidence has no payload metadata")
		}
		// A captured observation retained the whole message, so every byte it
		// counted must be a byte it stored. Without this an envelope can claim
		// BodyBytes while storing nothing, and a read returns an empty body with
		// a success outcome.
		if int64(len(value.Body)) > value.BodyBytes {
			return errors.New("raw evidence stores more bytes than it observed")
		}
		if value.PayloadState == PayloadCaptured {
			if int64(len(value.Body)) != value.BodyBytes {
				return errors.New(
					"captured raw evidence stored fewer bytes than it counted",
				)
			}
			// body_sha256 is observation evidence recorded independently of the
			// stored bytes. Reassembly is verified against the body row's own key,
			// which proves the chunks join; only this comparison proves they are
			// the bytes the envelope says were observed. A truncated observation is
			// excluded because its digest may cover a message it only kept a
			// prefix of.
			if value.DigestScope == DigestFull &&
				sha256.Sum256(value.Body) != value.BodySHA256 {
				return errors.New(
					"captured raw evidence body does not match its recorded digest",
				)
			}
		}
	} else if len(value.PayloadMetadata) != 0 || len(value.Body) != 0 {
		return errors.New("uncaptured raw evidence carries a payload")
	}
	if value.DigestScope == DigestUnavailable {
		if value.BodySHA256 != [sha256.Size]byte{} {
			return errors.New("unavailable raw digest contains bytes")
		}
	} else if value.BodySHA256 == [sha256.Size]byte{} && value.BodyBytes != 0 {
		return errors.New("raw evidence body digest is empty")
	}
	if value.PayloadReason != "" && !validReason(value.PayloadReason) {
		return errors.New("raw evidence payload reason is invalid")
	}
	return nil
}

type Watermark struct {
	WriterID string
	Sequence uint64
}

func (watermark Watermark) Valid() bool {
	return validIdentity(watermark.WriterID) && watermark.Sequence != 0 &&
		watermark.Sequence <= maxSQLiteInteger
}

type WriterSession struct {
	WriterID             string
	StartedAt            time.Time
	MaximumUnflushedTime time.Duration
}

func (session WriterSession) Validate() error {
	if !validIdentity(session.WriterID) || session.StartedAt.IsZero() ||
		session.MaximumUnflushedTime <= 0 {
		return errors.New("raw evidence writer session is invalid")
	}
	return nil
}

type Recovery struct {
	RecoveredUncleanWriters uint64
	PurgedExpiredEnvelopes  uint64
	MaximumPossibleLoss     time.Duration
}

type RevealOutcome string

const (
	RevealSucceeded   RevealOutcome = "succeeded"
	RevealUnavailable RevealOutcome = "unavailable"
)

func (outcome RevealOutcome) Valid() bool {
	return outcome == RevealSucceeded || outcome == RevealUnavailable
}

type RevealAudit struct {
	EnvelopeID string
	ExchangeID string
	ActorID    string
	Outcome    RevealOutcome
	OccurredAt time.Time
}

func (audit RevealAudit) Validate() error {
	if !validIdentity(audit.EnvelopeID) || !validIdentity(audit.ExchangeID) ||
		!validIdentity(audit.ActorID) ||
		!audit.Outcome.Valid() || audit.OccurredAt.IsZero() {
		return errors.New("raw evidence reveal audit is invalid")
	}
	return nil
}

type RevealRequest struct {
	EnvelopeID string
	ActorID    string
}

func (request RevealRequest) Validate() error {
	if !validIdentity(request.EnvelopeID) || !validIdentity(request.ActorID) {
		return ErrInvalidReveal
	}
	return nil
}

type RevealedEnvelope struct {
	Metadata EnvelopeMetadata
	Payload  Payload
}

type Repository interface {
	// RedactionSalt returns the database's stable redaction salt, creating it
	// on first use. It is not a secret and never leaves this database; its
	// purpose is to keep a redacted digest from matching a corpus assembled
	// elsewhere.
	RedactionSalt(context.Context) ([]byte, error)
	AppendBatch(context.Context, []StoredEnvelope, time.Time) error
	ListExchange(context.Context, string) ([]StoredEnvelope, error)
	GetEnvelope(context.Context, string) (StoredEnvelope, error)
	AppendRevealAudit(context.Context, RevealAudit) error
	BeginWriterSession(context.Context, WriterSession) (Recovery, error)
	CloseWriterSession(context.Context, string, time.Time) error
}

type Observer interface {
	Observe(context.Context, Observation) (Watermark, error)
}

// ScopeLease keeps one capture-scoped proxy request inside the raw-evidence
// terminal barrier until every boundary observation for that request has been
// admitted. Release is idempotent.
type ScopeLease interface {
	Release()
}

// TerminalScope is returned only after new request admission for the capture
// has been closed, all already-admitted requests have completed, and their
// final raw-evidence watermark is durable. The capture authority must Commit
// after its terminal state is durable, or Abort when that mutation fails.
type TerminalScope interface {
	Commit()
	Abort()
}

// ScopeLifecycle coordinates request admission with a Capture terminal
// transition. It is deliberately separate from Observe: provider boundaries
// need only Observer, while the authenticated proxy owns request lifetimes.
type ScopeLifecycle interface {
	BeginScope(context.Context, ScopeKind, string) (ScopeLease, error)
	PrepareTerminalScope(context.Context, ScopeKind, string) (TerminalScope, error)
}

// RequestRecorder is the complete raw-evidence dependency of authenticated
// proxy ingress.
type RequestRecorder interface {
	Observer
	ScopeLifecycle
}

type Reader interface {
	ListExchange(context.Context, string) ([]EnvelopeMetadata, error)
	Reveal(context.Context, RevealRequest) (RevealedEnvelope, error)
	Recovery() Recovery
}

func validIdentity(value string) bool {
	return value != "" && len(value) <= MaxIdentityBytes &&
		utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n\t")
}

func validOptionalIdentity(value string) bool {
	return value == "" || validIdentity(value)
}

func validOptionalMetadata(value string, maximumBytes int) bool {
	return len(value) <= maximumBytes && utf8.ValidString(value) &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func validReason(value string) bool {
	if value == "" || len(value) > MaxReasonBytes {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validMethod(value string) bool {
	if value == "" || value != strings.ToUpper(value) {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && character != '-' {
			return false
		}
	}
	return true
}
