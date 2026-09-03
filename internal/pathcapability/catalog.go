// Package pathcapability classifies one canonical request path before an
// ingress can select semantic decoding or any external egress.
package pathcapability

import (
	"errors"
	"fmt"
	pathpkg "path"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providertransport"
)

type Kind string

const (
	KindSemantic    Kind = "semantic"
	KindAuxiliary   Kind = "auxiliary"
	KindOpaque      Kind = "opaque"
	KindUnsupported Kind = "unsupported"
)

type ReasonCode string

const (
	ReasonInvalidRequestTarget ReasonCode = "invalid_request_target"
	ReasonUnsupportedMethod    ReasonCode = "unsupported_method"
	ReasonUnsupportedQuery     ReasonCode = "unsupported_query"
	ReasonUnsupportedDialect   ReasonCode = "unsupported_client_dialect"
)

var ErrUnsupported = errors.New("request path capability is unsupported")

type Failure struct {
	Code ReasonCode
	err  error
}

func (failure *Failure) Error() string {
	if failure == nil {
		return "<nil>"
	}
	if failure.err == nil {
		return fmt.Sprintf("path capability failed: code=%s", failure.Code)
	}
	return fmt.Sprintf("path capability failed: code=%s: %v", failure.Code, failure.err)
}

func (failure *Failure) Unwrap() error {
	if failure == nil {
		return nil
	}
	return errors.Join(ErrUnsupported, failure.err)
}

func ReasonOf(err error) ReasonCode {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
}

type BodyKind string

const (
	BodyJSON   BodyKind = "json"
	BodyOpaque BodyKind = "opaque"
)

type Capability struct {
	operationID   protocolspec.ClientOperationID
	revision      protocolspec.Revision
	kind          Kind
	transport     protocolspec.ClientOperationTransport
	method        string
	path          string
	bodyKind      BodyKind
	maxBodyBytes  int64
	replayClass   exchange.ReplayClass
	featureFlags  []string
	operation     protocolspec.ClientOperationDefinition
	payloadClass  protocolspec.OperationPayloadClass
	egressBearing bool
}

func (capability Capability) OperationID() protocolspec.ClientOperationID {
	return capability.operationID
}

func (capability Capability) Revision() protocolspec.Revision {
	return capability.revision
}

func (capability Capability) Kind() Kind {
	return capability.kind
}

func (capability Capability) Transport() protocolspec.ClientOperationTransport {
	return capability.transport
}

func (capability Capability) Method() string {
	return capability.method
}

func (capability Capability) Path() string {
	return capability.path
}

func (capability Capability) BodyKind() BodyKind {
	return capability.bodyKind
}

func (capability Capability) MaxBodyBytes() int64 {
	return capability.maxBodyBytes
}

func (capability Capability) ReplayClass() exchange.ReplayClass {
	return capability.replayClass
}

func (capability Capability) FeatureFlags() []string {
	return slices.Clone(capability.featureFlags)
}

// PayloadClass reports the frozen catalog answer for what a request of this
// operation carries. An uncatalogued request reports
// protocolspec.OperationPayloadUnknown.
func (capability Capability) PayloadClass() protocolspec.OperationPayloadClass {
	return capability.payloadClass
}

func (capability Capability) EgressBearing() bool {
	return capability.egressBearing
}

type routeKey struct {
	dialect protocolspec.Dialect
	path    string
}

type prefixRoute struct {
	path    string
	methods map[string]Capability
}

type Catalog struct {
	byPath   map[routeKey]map[string]Capability
	prefixes map[protocolspec.Dialect][]prefixRoute
}

func NewCatalog(
	definitions []protocolspec.ClientOperationDefinition,
) (*Catalog, error) {
	if len(definitions) == 0 {
		return nil, errors.New("PathCapability definitions are empty")
	}
	catalog := &Catalog{
		byPath:   make(map[routeKey]map[string]Capability),
		prefixes: make(map[protocolspec.Dialect][]prefixRoute),
	}
	for _, definition := range definitions {
		if err := definition.Validate(); err != nil {
			return nil, fmt.Errorf("PathCapability definition is invalid: %w", err)
		}
		kind, err := pathKind(definition.Kind())
		if err != nil {
			return nil, err
		}
		bodyKind, err := pathBodyKind(definition.BodyKind())
		if err != nil {
			return nil, err
		}
		replayClass, err := pathReplayClass(definition.ReplayClass())
		if err != nil {
			return nil, err
		}
		var featureFlags []string
		if definition.CodecFeature() != "" {
			featureFlags = []string{string(definition.CodecFeature())}
		}
		methods := make(map[string]Capability, len(definition.Methods()))
		for _, method := range definition.Methods() {
			methods[method] = Capability{
				operationID:   definition.ID(),
				revision:      definition.Revision(),
				kind:          kind,
				transport:     definition.Transport(),
				method:        method,
				path:          definition.PathPattern(),
				bodyKind:      bodyKind,
				maxBodyBytes:  definition.MaxBodyBytes(),
				replayClass:   replayClass,
				featureFlags:  slices.Clone(featureFlags),
				operation:     definition.Clone(),
				payloadClass:  definition.PayloadClass(),
				egressBearing: definition.EgressBearing(),
			}
		}
		switch definition.PathMatch() {
		case protocolspec.ClientOperationPathExact:
			key := routeKey{
				dialect: definition.ClientDialect(),
				path:    definition.PathPattern(),
			}
			existing, found := catalog.byPath[key]
			if !found {
				existing = make(map[string]Capability)
				catalog.byPath[key] = existing
			}
			for method, capability := range methods {
				if _, duplicate := existing[method]; duplicate {
					return nil, errors.New(
						"PathCapability exact method is duplicated",
					)
				}
				existing[method] = capability
			}
		case protocolspec.ClientOperationPathPrefix:
			for _, existing := range catalog.prefixes[definition.ClientDialect()] {
				if existing.path == definition.PathPattern() {
					return nil, errors.New(
						"PathCapability prefix definition is duplicated",
					)
				}
			}
			catalog.prefixes[definition.ClientDialect()] = append(
				catalog.prefixes[definition.ClientDialect()],
				prefixRoute{
					path:    definition.PathPattern(),
					methods: methods,
				},
			)
		default:
			return nil, errors.New(
				"PathCapability path match is unsupported",
			)
		}
	}
	for dialect := range catalog.prefixes {
		sort.Slice(
			catalog.prefixes[dialect],
			func(left, right int) bool {
				return len(catalog.prefixes[dialect][left].path) >
					len(catalog.prefixes[dialect][right].path)
			},
		)
	}
	return catalog, nil
}

// Classify rejects alternate escaping and non-canonical paths before looking
// up a dialect capability. Unknown canonical paths are opaque; known paths
// with the wrong method or query fail closed.
func (catalog *Catalog) Classify(
	dialect protocolspec.Dialect,
	method string,
	requestPath string,
	rawPath string,
	rawQuery string,
) (Capability, error) {
	if catalog == nil {
		return Capability{}, errors.New("PathCapability catalog is nil")
	}
	if dialect == "" {
		return Capability{}, &Failure{
			Code: ReasonUnsupportedDialect,
			err:  errors.New("client dialect is empty"),
		}
	}
	if !validMethod(method) {
		return Capability{}, &Failure{
			Code: ReasonUnsupportedMethod,
			err:  errors.New("HTTP method is not canonical"),
		}
	}
	if rawPath != "" || !canonicalPath(requestPath) {
		return Capability{}, &Failure{
			Code: ReasonInvalidRequestTarget,
			err:  errors.New("request path is not canonical"),
		}
	}
	methods, known := catalog.byPath[routeKey{
		dialect: dialect,
		path:    requestPath,
	}]
	if !known {
		for _, prefix := range catalog.prefixes[dialect] {
			if requestPath == prefix.path ||
				strings.HasPrefix(requestPath, prefix.path+"/") {
				methods = prefix.methods
				known = true
				break
			}
		}
	}
	if !known {
		return Capability{
			kind:         KindOpaque,
			transport:    protocolspec.ClientOperationTransportHTTP,
			method:       method,
			path:         requestPath,
			bodyKind:     BodyOpaque,
			maxBodyBytes: providertransport.MaxProviderRequestBytes,
			replayClass:  exchange.ReplayUnknown,
			// No catalog entry claims this path, so nothing proves what the
			// request carries.
			payloadClass:  protocolspec.OperationPayloadUnknown,
			egressBearing: true,
		}, nil
	}
	capability, supported := methods[method]
	if !supported {
		return Capability{}, &Failure{
			Code: ReasonUnsupportedMethod,
			err:  errors.New("known path does not support this method"),
		}
	}
	if !capability.operation.AllowsRawQuery(rawQuery) {
		return Capability{}, &Failure{
			Code: ReasonUnsupportedQuery,
			err:  errors.New("known path does not accept a query"),
		}
	}
	return capability, nil
}

func pathKind(kind protocolspec.ClientOperationKind) (Kind, error) {
	switch kind {
	case protocolspec.ClientOperationSemantic:
		return KindSemantic, nil
	case protocolspec.ClientOperationAuxiliary:
		return KindAuxiliary, nil
	case protocolspec.ClientOperationOpaque:
		return KindOpaque, nil
	case protocolspec.ClientOperationUnsupported:
		return KindUnsupported, nil
	default:
		return "", errors.New("PathCapability operation kind is invalid")
	}
}

func pathBodyKind(
	kind protocolspec.ClientOperationBodyKind,
) (BodyKind, error) {
	switch kind {
	case protocolspec.ClientOperationBodyJSON:
		return BodyJSON, nil
	case protocolspec.ClientOperationBodyNone,
		protocolspec.ClientOperationBodyMultipart,
		protocolspec.ClientOperationBodyBytes,
		protocolspec.ClientOperationBodyStream:
		return BodyOpaque, nil
	default:
		return "", errors.New("PathCapability body kind is invalid")
	}
}

func pathReplayClass(
	class protocolspec.ClientReplayClass,
) (exchange.ReplayClass, error) {
	switch class {
	case protocolspec.ClientReplaySafe:
		return exchange.ReplaySafe, nil
	case protocolspec.ClientReplayIdempotencyKeyed:
		return exchange.ReplayIdempotencyKeyed, nil
	case protocolspec.ClientReplayGenerationCostOnly:
		return exchange.ReplayGenerationCostOnly, nil
	case protocolspec.ClientReplaySideEffectPossible:
		return exchange.ReplaySideEffectPossible, nil
	case protocolspec.ClientReplayNonReplayable:
		return exchange.ReplayNonReplayable, nil
	case protocolspec.ClientReplayUnknown:
		return exchange.ReplayUnknown, nil
	default:
		return "", errors.New("PathCapability replay class is invalid")
	}
}

func canonicalPath(value string) bool {
	if value == "" ||
		len(value) > 2048 ||
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

func validMethod(value string) bool {
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
