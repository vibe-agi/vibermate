package access

import (
	"errors"
	"net/url"
	pathpkg "path"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	MaxClientOperationMethods = 8
	MaxClientOperationPath    = 2048
	MaxClientOperationQueries = 32
	MaxClientOperationBody    = 64 << 20
)

type ClientOperationPathMatch string

const (
	ClientOperationPathExact  ClientOperationPathMatch = "exact"
	ClientOperationPathPrefix ClientOperationPathMatch = "prefix"
)

type ClientOperationKind string

const (
	ClientOperationSemantic    ClientOperationKind = "semantic"
	ClientOperationAuxiliary   ClientOperationKind = "auxiliary"
	ClientOperationOpaque      ClientOperationKind = "opaque"
	ClientOperationUnsupported ClientOperationKind = "unsupported"
)

type ClientOperationBodyKind string

const (
	ClientOperationBodyNone      ClientOperationBodyKind = "none"
	ClientOperationBodyJSON      ClientOperationBodyKind = "json"
	ClientOperationBodyMultipart ClientOperationBodyKind = "multipart"
	ClientOperationBodyBytes     ClientOperationBodyKind = "bytes"
	ClientOperationBodyStream    ClientOperationBodyKind = "stream"
)

type ClientOperationTransport string

const (
	ClientOperationTransportHTTP      ClientOperationTransport = "http"
	ClientOperationTransportWebSocket ClientOperationTransport = "websocket"
)

type ClientReplayClass string

const (
	ClientReplaySafe               ClientReplayClass = "safe"
	ClientReplayIdempotencyKeyed   ClientReplayClass = "idempotency_keyed"
	ClientReplayGenerationCostOnly ClientReplayClass = "generation_cost_only"
	ClientReplaySideEffectPossible ClientReplayClass = "side_effect_possible"
	ClientReplayNonReplayable      ClientReplayClass = "non_replayable"
	ClientReplayUnknown            ClientReplayClass = "unknown"
)

type CodecFeature string

type ClientOperationOptions struct {
	ID             ClientOperationID
	Revision       Revision
	ClientDialect  Dialect
	Methods        []string
	PathPattern    string
	PathMatch      ClientOperationPathMatch
	Kind           ClientOperationKind
	Transport      ClientOperationTransport
	BodyKind       ClientOperationBodyKind
	ReplayClass    ClientReplayClass
	CodecFeature   CodecFeature
	MaxBodyBytes   int64
	AllowedQueries []string
	EgressBearing  bool
}

// ClientOperationDefinition is one immutable operation-catalog entry. It
// classifies the client-side wire contract without selecting a provider
// target, credential, transport, or codec implementation.
type ClientOperationDefinition struct {
	id             ClientOperationID
	revision       Revision
	clientDialect  Dialect
	methods        []string
	pathPattern    string
	pathMatch      ClientOperationPathMatch
	kind           ClientOperationKind
	transport      ClientOperationTransport
	bodyKind       ClientOperationBodyKind
	replayClass    ClientReplayClass
	codecFeature   CodecFeature
	maxBodyBytes   int64
	allowedQueries []string
	egressBearing  bool
}

func NewClientOperationDefinition(
	options ClientOperationOptions,
) (ClientOperationDefinition, error) {
	definition := ClientOperationDefinition{
		id:             options.ID,
		revision:       options.Revision,
		clientDialect:  options.ClientDialect,
		methods:        slices.Clone(options.Methods),
		pathPattern:    options.PathPattern,
		pathMatch:      options.PathMatch,
		kind:           options.Kind,
		transport:      options.Transport,
		bodyKind:       options.BodyKind,
		replayClass:    options.ReplayClass,
		codecFeature:   options.CodecFeature,
		maxBodyBytes:   options.MaxBodyBytes,
		allowedQueries: slices.Clone(options.AllowedQueries),
		egressBearing:  options.EgressBearing,
	}
	sort.Strings(definition.methods)
	sort.Strings(definition.allowedQueries)
	if err := definition.Validate(); err != nil {
		return ClientOperationDefinition{}, err
	}
	return definition, nil
}

func (definition ClientOperationDefinition) ID() ClientOperationID {
	return definition.id
}

func (definition ClientOperationDefinition) Revision() Revision {
	return definition.revision
}

func (definition ClientOperationDefinition) ClientDialect() Dialect {
	return definition.clientDialect
}

func (definition ClientOperationDefinition) Methods() []string {
	return slices.Clone(definition.methods)
}

func (definition ClientOperationDefinition) PathPattern() string {
	return definition.pathPattern
}

func (definition ClientOperationDefinition) PathMatch() ClientOperationPathMatch {
	return definition.pathMatch
}

func (definition ClientOperationDefinition) Kind() ClientOperationKind {
	return definition.kind
}

func (definition ClientOperationDefinition) Transport() ClientOperationTransport {
	return definition.transport
}

func (definition ClientOperationDefinition) BodyKind() ClientOperationBodyKind {
	return definition.bodyKind
}

func (definition ClientOperationDefinition) ReplayClass() ClientReplayClass {
	return definition.replayClass
}

func (definition ClientOperationDefinition) CodecFeature() CodecFeature {
	return definition.codecFeature
}

func (definition ClientOperationDefinition) MaxBodyBytes() int64 {
	return definition.maxBodyBytes
}

func (definition ClientOperationDefinition) AllowedQueries() []string {
	return slices.Clone(definition.allowedQueries)
}

func (definition ClientOperationDefinition) EgressBearing() bool {
	return definition.egressBearing
}

func (definition ClientOperationDefinition) Validate() error {
	if err := definition.id.validate(); err != nil {
		return err
	}
	if err := validateRevision(
		"client operation",
		definition.revision,
	); err != nil {
		return err
	}
	if definition.clientDialect == "" {
		return errors.New("client operation dialect is empty")
	}
	if len(definition.methods) == 0 ||
		len(definition.methods) > MaxClientOperationMethods {
		return errors.New("client operation methods are invalid")
	}
	for index, method := range definition.methods {
		if !validClientOperationMethod(method) {
			return errors.New("client operation method is invalid")
		}
		if index > 0 && method == definition.methods[index-1] {
			return errors.New("client operation method is duplicated")
		}
	}
	if !canonicalClientOperationPath(definition.pathPattern) {
		return errors.New("client operation path pattern is invalid")
	}
	switch definition.pathMatch {
	case ClientOperationPathExact:
	case ClientOperationPathPrefix:
		if definition.pathPattern == "/" {
			return errors.New("client operation prefix cannot match every path")
		}
	default:
		return errors.New("client operation path match is invalid")
	}
	switch definition.kind {
	case ClientOperationSemantic:
		if definition.pathMatch != ClientOperationPathExact {
			return errors.New("semantic client operation must use an exact path")
		}
	case ClientOperationAuxiliary,
		ClientOperationOpaque,
		ClientOperationUnsupported:
	default:
		return errors.New("client operation kind is invalid")
	}
	switch definition.transport {
	case ClientOperationTransportHTTP:
	case ClientOperationTransportWebSocket:
		if definition.kind != ClientOperationUnsupported ||
			definition.pathMatch != ClientOperationPathExact ||
			len(definition.methods) != 1 ||
			definition.methods[0] != "GET" ||
			definition.bodyKind != ClientOperationBodyNone ||
			definition.egressBearing {
			return errors.New(
				"WebSocket client operation must be an exact bodyless unsupported GET",
			)
		}
	default:
		return errors.New("client operation transport is invalid")
	}
	switch definition.bodyKind {
	case ClientOperationBodyNone:
		if definition.maxBodyBytes != 0 {
			return errors.New("bodyless client operation has a body limit")
		}
	case ClientOperationBodyJSON,
		ClientOperationBodyMultipart,
		ClientOperationBodyBytes,
		ClientOperationBodyStream:
		if definition.maxBodyBytes <= 0 ||
			definition.maxBodyBytes > MaxClientOperationBody {
			return errors.New("client operation body limit is invalid")
		}
	default:
		return errors.New("client operation body kind is invalid")
	}
	switch definition.replayClass {
	case ClientReplaySafe,
		ClientReplayIdempotencyKeyed,
		ClientReplayGenerationCostOnly,
		ClientReplaySideEffectPossible,
		ClientReplayNonReplayable,
		ClientReplayUnknown:
	default:
		return errors.New("client operation replay class is invalid")
	}
	switch definition.kind {
	case ClientOperationSemantic, ClientOperationAuxiliary:
		if definition.codecFeature == "" {
			return errors.New("translated client operation codec feature is empty")
		}
		if definition.kind == ClientOperationSemantic &&
			!definition.egressBearing {
			return errors.New("semantic client operation must bear egress")
		}
	case ClientOperationOpaque, ClientOperationUnsupported:
		if definition.codecFeature != "" {
			return errors.New("non-translated client operation has a codec feature")
		}
	}
	if len(definition.allowedQueries) > MaxClientOperationQueries {
		return errors.New("client operation query catalog exceeds the limit")
	}
	for index, query := range definition.allowedQueries {
		if !canonicalClientOperationQuery(query) {
			return errors.New("client operation allowed query is invalid")
		}
		if index > 0 && query == definition.allowedQueries[index-1] {
			return errors.New("client operation allowed query is duplicated")
		}
	}
	if definition.kind == ClientOperationUnsupported &&
		definition.egressBearing {
		return errors.New("unsupported client operation cannot bear egress")
	}
	return nil
}

func cloneClientOperationDefinition(
	definition ClientOperationDefinition,
) ClientOperationDefinition {
	cloned := definition
	cloned.methods = slices.Clone(definition.methods)
	cloned.allowedQueries = slices.Clone(definition.allowedQueries)
	return cloned
}

func canonicalClientOperationPath(value string) bool {
	if value == "" ||
		len(value) > MaxClientOperationPath ||
		!utf8.ValidString(value) ||
		value[0] != '/' ||
		strings.ContainsAny(value, "\\%\x00\r\n\t") ||
		pathpkg.Clean(value) != value ||
		(value != "/" && strings.HasSuffix(value, "/")) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}

func validClientOperationMethod(value string) bool {
	if value == "" || strings.ToUpper(value) != value {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') &&
			character != '-' {
			return false
		}
	}
	return true
}

func canonicalClientOperationQuery(value string) bool {
	if value == "" || len(value) > 2048 || !utf8.ValidString(value) {
		return false
	}
	parsed, err := url.ParseQuery(value)
	return err == nil && parsed.Encode() == value
}
