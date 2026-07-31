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

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/exchange"
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
	operationID    access.ClientOperationID
	revision       access.Revision
	kind           Kind
	method         string
	path           string
	bodyKind       BodyKind
	maxBodyBytes   int64
	replayClass    exchange.ReplayClass
	featureFlags   []string
	allowedQueries []string
	egressBearing  bool
}

func (capability Capability) OperationID() access.ClientOperationID {
	return capability.operationID
}

func (capability Capability) Revision() access.Revision {
	return capability.revision
}

func (capability Capability) Kind() Kind {
	return capability.kind
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

func (capability Capability) EgressBearing() bool {
	return capability.egressBearing
}

type routeKey struct {
	dialect access.Dialect
	path    string
}

type prefixRoute struct {
	path    string
	methods map[string]Capability
}

type Catalog struct {
	byPath   map[routeKey]map[string]Capability
	prefixes map[access.Dialect][]prefixRoute
}

func NewCatalog(
	definitions []access.ClientOperationDefinition,
) (*Catalog, error) {
	if len(definitions) == 0 {
		return nil, errors.New("PathCapability definitions are empty")
	}
	catalog := &Catalog{
		byPath:   make(map[routeKey]map[string]Capability),
		prefixes: make(map[access.Dialect][]prefixRoute),
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
				operationID:    definition.ID(),
				revision:       definition.Revision(),
				kind:           kind,
				method:         method,
				path:           definition.PathPattern(),
				bodyKind:       bodyKind,
				maxBodyBytes:   definition.MaxBodyBytes(),
				replayClass:    replayClass,
				featureFlags:   slices.Clone(featureFlags),
				allowedQueries: definition.AllowedQueries(),
				egressBearing:  definition.EgressBearing(),
			}
		}
		switch definition.PathMatch() {
		case access.ClientOperationPathExact:
			key := routeKey{
				dialect: definition.ClientDialect(),
				path:    definition.PathPattern(),
			}
			if _, duplicate := catalog.byPath[key]; duplicate {
				return nil, errors.New(
					"PathCapability exact definition is duplicated",
				)
			}
			catalog.byPath[key] = methods
		case access.ClientOperationPathPrefix:
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
	dialect access.Dialect,
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
			kind:          KindOpaque,
			method:        method,
			path:          requestPath,
			bodyKind:      BodyOpaque,
			maxBodyBytes:  providertransport.MaxProviderRequestBytes,
			replayClass:   exchange.ReplayUnknown,
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
	if rawQuery != "" && !slices.Contains(capability.allowedQueries, rawQuery) {
		return Capability{}, &Failure{
			Code: ReasonUnsupportedQuery,
			err:  errors.New("known path does not accept a query"),
		}
	}
	return capability, nil
}

func pathKind(kind access.ClientOperationKind) (Kind, error) {
	switch kind {
	case access.ClientOperationSemantic:
		return KindSemantic, nil
	case access.ClientOperationAuxiliary:
		return KindAuxiliary, nil
	case access.ClientOperationOpaque:
		return KindOpaque, nil
	case access.ClientOperationUnsupported:
		return KindUnsupported, nil
	default:
		return "", errors.New("PathCapability operation kind is invalid")
	}
}

func pathBodyKind(
	kind access.ClientOperationBodyKind,
) (BodyKind, error) {
	switch kind {
	case access.ClientOperationBodyJSON:
		return BodyJSON, nil
	case access.ClientOperationBodyNone,
		access.ClientOperationBodyMultipart,
		access.ClientOperationBodyBytes,
		access.ClientOperationBodyStream:
		return BodyOpaque, nil
	default:
		return "", errors.New("PathCapability body kind is invalid")
	}
}

func pathReplayClass(
	class access.ClientReplayClass,
) (exchange.ReplayClass, error) {
	switch class {
	case access.ClientReplaySafe:
		return exchange.ReplaySafe, nil
	case access.ClientReplayIdempotencyKeyed:
		return exchange.ReplayIdempotencyKeyed, nil
	case access.ClientReplayGenerationCostOnly:
		return exchange.ReplayGenerationCostOnly, nil
	case access.ClientReplaySideEffectPossible:
		return exchange.ReplaySideEffectPossible, nil
	case access.ClientReplayNonReplayable:
		return exchange.ReplayNonReplayable, nil
	case access.ClientReplayUnknown:
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
