// Package pathcapability classifies one canonical request path before an
// ingress can select semantic decoding or any external egress.
package pathcapability

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	pathpkg "path"
	"slices"
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

type Definition struct {
	Dialect        access.Dialect
	Method         string
	Path           string
	Kind           Kind
	BodyKind       BodyKind
	MaxBodyBytes   int64
	ReplayClass    exchange.ReplayClass
	FeatureFlags   []string
	AllowedQueries []string
	EgressBearing  bool
}

type routeKey struct {
	dialect access.Dialect
	path    string
}

type Catalog struct {
	byPath map[routeKey]map[string]Capability
}

func NewCatalog(definitions []Definition) (*Catalog, error) {
	if len(definitions) == 0 {
		return nil, errors.New("PathCapability definitions are empty")
	}
	catalog := &Catalog{
		byPath: make(map[routeKey]map[string]Capability),
	}
	for _, definition := range definitions {
		if definition.Dialect == "" ||
			!validMethod(definition.Method) ||
			!canonicalPath(definition.Path) ||
			definition.Kind == KindOpaque ||
			definition.Kind == KindUnsupported ||
			definition.MaxBodyBytes <= 0 ||
			definition.MaxBodyBytes > providertransport.MaxProviderRequestBytes {
			return nil, errors.New("PathCapability definition is invalid")
		}
		allowedQueries := slices.Clone(definition.AllowedQueries)
		slices.Sort(allowedQueries)
		for index, query := range allowedQueries {
			if !canonicalRawQuery(query) ||
				(index > 0 && query == allowedQueries[index-1]) {
				return nil, errors.New(
					"PathCapability allowed query is invalid",
				)
			}
		}
		switch definition.Kind {
		case KindSemantic, KindAuxiliary:
		default:
			return nil, errors.New("PathCapability definition kind is invalid")
		}
		switch definition.BodyKind {
		case BodyJSON, BodyOpaque:
		default:
			return nil, errors.New("PathCapability body kind is invalid")
		}
		key := routeKey{dialect: definition.Dialect, path: definition.Path}
		methods := catalog.byPath[key]
		if methods == nil {
			methods = make(map[string]Capability)
			catalog.byPath[key] = methods
		}
		if _, duplicate := methods[definition.Method]; duplicate {
			return nil, errors.New("PathCapability definition is duplicated")
		}
		methods[definition.Method] = Capability{
			kind:           definition.Kind,
			method:         definition.Method,
			path:           definition.Path,
			bodyKind:       definition.BodyKind,
			maxBodyBytes:   definition.MaxBodyBytes,
			replayClass:    definition.ReplayClass,
			featureFlags:   slices.Clone(definition.FeatureFlags),
			allowedQueries: allowedQueries,
			egressBearing:  definition.EgressBearing,
		}
	}
	return catalog, nil
}

func NewM0Catalog() (*Catalog, error) {
	return NewCatalog([]Definition{
		{
			Dialect:        access.DialectAnthropicMessages,
			Method:         http.MethodPost,
			Path:           "/v1/messages",
			Kind:           KindSemantic,
			BodyKind:       BodyJSON,
			MaxBodyBytes:   providertransport.MaxProviderRequestBytes,
			ReplayClass:    exchange.ReplayGenerationCostOnly,
			FeatureFlags:   []string{"messages", "streaming", "tool_calls"},
			AllowedQueries: []string{"beta=true"},
			EgressBearing:  true,
		},
		{
			Dialect:        access.DialectAnthropicMessages,
			Method:         http.MethodPost,
			Path:           "/v1/messages/count_tokens",
			Kind:           KindAuxiliary,
			BodyKind:       BodyJSON,
			MaxBodyBytes:   providertransport.MaxProviderRequestBytes,
			ReplayClass:    exchange.ReplaySafe,
			FeatureFlags:   []string{"token_count"},
			AllowedQueries: []string{"beta=true"},
			EgressBearing:  true,
		},
	})
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

func canonicalRawQuery(value string) bool {
	if value == "" || len(value) > 2048 || !utf8.ValidString(value) {
		return false
	}
	parsed, err := url.ParseQuery(value)
	return err == nil && parsed.Encode() == value
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
