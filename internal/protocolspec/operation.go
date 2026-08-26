// Package protocolspec owns immutable protocol and operation values shared by
// Environment compilation and the two wire edges. It owns no product
// assignment, routing, credential, transport, or listener authority.
package protocolspec

import (
	"errors"
	"fmt"
	"net/url"
	pathpkg "path"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxIdentifierBytes           = 128
	MaxOperationMethods          = 8
	MaxOperationPath             = 2048
	MaxOperationQueries          = 32
	MaxOperationBody    int64    = 64 << 20
	MaxRevision         Revision = 1<<63 - 1
)

var ErrInvalidSpecification = errors.New("protocol specification is invalid")

var (
	ErrInvalidRequestTarget      = errors.New("request target is not canonical")
	ErrOperationNotCatalogued    = errors.New("request operation is not in the catalog")
	ErrOperationContractMismatch = errors.New("request does not satisfy the catalogued operation")
	ErrAmbiguousOperation        = errors.New("request matches more than one catalogued operation")
)

type Revision uint64

type ClientOperationID struct{ value string }
type CodecPairID struct{ value string }

func NewClientOperationID(value string) (ClientOperationID, error) {
	if err := validateIdentifier("client operation ID", value); err != nil {
		return ClientOperationID{}, err
	}
	return ClientOperationID{value: value}, nil
}

func NewCodecPairID(value string) (CodecPairID, error) {
	if err := validateIdentifier("codec pair ID", value); err != nil {
		return CodecPairID{}, err
	}
	return CodecPairID{value: value}, nil
}

func (id ClientOperationID) String() string { return id.value }
func (id CodecPairID) String() string       { return id.value }

func validateIdentifier(label, value string) error {
	if value == "" || len(value) > MaxIdentifierBytes || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %s is not canonical", ErrInvalidSpecification, label)
	}
	for _, character := range value {
		if unicode.IsControl(character) || character > unicode.MaxASCII ||
			!(character >= 'a' && character <= 'z') &&
				!(character >= '0' && character <= '9') &&
				character != '-' && character != '_' && character != '.' {
			return fmt.Errorf("%w: %s contains a non-canonical character", ErrInvalidSpecification, label)
		}
	}
	return nil
}

type Dialect string

const (
	DialectAnthropicMessages Dialect = "anthropic-messages"
	DialectOpenAIResponses   Dialect = "openai-responses"
	DialectOpenAIChat        Dialect = "openai-chat"
)

func (dialect Dialect) Valid() bool {
	switch dialect {
	case DialectAnthropicMessages, DialectOpenAIResponses, DialectOpenAIChat:
		return true
	default:
		return false
	}
}

type ProviderCapability string

const (
	ProviderCapabilityMessages  ProviderCapability = "messages"
	ProviderCapabilityStreaming ProviderCapability = "streaming"
	ProviderCapabilityToolCalls ProviderCapability = "tool_calls"
)

func (capability ProviderCapability) Valid() bool {
	switch capability {
	case ProviderCapabilityMessages, ProviderCapabilityStreaming, ProviderCapabilityToolCalls:
		return true
	default:
		return false
	}
}

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

type OperationPayloadClass string

const (
	OperationPayloadNone           OperationPayloadClass = "none"
	OperationPayloadControl        OperationPayloadClass = "control"
	OperationPayloadClientData     OperationPayloadClass = "client_data"
	OperationPayloadClientSemantic OperationPayloadClass = "client_semantic"
	OperationPayloadUnknown        OperationPayloadClass = "unknown"
)

func (class OperationPayloadClass) CarriesClientPayload() bool {
	return class != OperationPayloadNone && class != OperationPayloadControl
}

func (class OperationPayloadClass) AllowsOriginalOrigin() bool {
	return class == OperationPayloadNone || class == OperationPayloadControl
}

func (class OperationPayloadClass) validCatalogValue() bool {
	switch class {
	case OperationPayloadNone, OperationPayloadControl,
		OperationPayloadClientData, OperationPayloadClientSemantic:
		return true
	default:
		return false
	}
}

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
	// AllowedQueryKeys admits canonical query strings containing only these
	// keys while leaving their control-plane values dynamic. Exact
	// AllowedQueries remain useful when the value itself is part of the wire
	// contract (for example beta=true).
	AllowedQueryKeys []string
	PayloadClass     OperationPayloadClass
	EgressBearing    bool
}

type ClientOperationDefinition struct {
	id               ClientOperationID
	revision         Revision
	clientDialect    Dialect
	methods          []string
	pathPattern      string
	pathMatch        ClientOperationPathMatch
	kind             ClientOperationKind
	transport        ClientOperationTransport
	bodyKind         ClientOperationBodyKind
	replayClass      ClientReplayClass
	codecFeature     CodecFeature
	maxBodyBytes     int64
	allowedQueries   []string
	allowedQueryKeys []string
	payloadClass     OperationPayloadClass
	egressBearing    bool
}

func NewClientOperationDefinition(options ClientOperationOptions) (ClientOperationDefinition, error) {
	definition := ClientOperationDefinition{
		id: options.ID, revision: options.Revision, clientDialect: options.ClientDialect,
		methods: slices.Clone(options.Methods), pathPattern: options.PathPattern,
		pathMatch: options.PathMatch, kind: options.Kind, transport: options.Transport,
		bodyKind: options.BodyKind, replayClass: options.ReplayClass,
		codecFeature: options.CodecFeature, maxBodyBytes: options.MaxBodyBytes,
		allowedQueries:   slices.Clone(options.AllowedQueries),
		allowedQueryKeys: slices.Clone(options.AllowedQueryKeys),
		payloadClass:     options.PayloadClass,
		egressBearing:    options.EgressBearing,
	}
	sort.Strings(definition.methods)
	sort.Strings(definition.allowedQueries)
	sort.Strings(definition.allowedQueryKeys)
	if err := definition.Validate(); err != nil {
		return ClientOperationDefinition{}, err
	}
	return definition, nil
}

func (definition ClientOperationDefinition) ID() ClientOperationID  { return definition.id }
func (definition ClientOperationDefinition) Revision() Revision     { return definition.revision }
func (definition ClientOperationDefinition) ClientDialect() Dialect { return definition.clientDialect }
func (definition ClientOperationDefinition) Methods() []string {
	return slices.Clone(definition.methods)
}
func (definition ClientOperationDefinition) PathPattern() string { return definition.pathPattern }
func (definition ClientOperationDefinition) PathMatch() ClientOperationPathMatch {
	return definition.pathMatch
}
func (definition ClientOperationDefinition) Kind() ClientOperationKind { return definition.kind }
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
func (definition ClientOperationDefinition) MaxBodyBytes() int64 { return definition.maxBodyBytes }
func (definition ClientOperationDefinition) AllowedQueries() []string {
	return slices.Clone(definition.allowedQueries)
}
func (definition ClientOperationDefinition) AllowedQueryKeys() []string {
	return slices.Clone(definition.allowedQueryKeys)
}
func (definition ClientOperationDefinition) AllowsRawQuery(rawQuery string) bool {
	return allowsRawQuery(
		definition.allowedQueries,
		definition.allowedQueryKeys,
		rawQuery,
	)
}
func (definition ClientOperationDefinition) PayloadClass() OperationPayloadClass {
	return definition.payloadClass
}
func (definition ClientOperationDefinition) EgressBearing() bool { return definition.egressBearing }

func (definition ClientOperationDefinition) Validate() error {
	if err := validateIdentifier("client operation ID", definition.id.value); err != nil ||
		definition.revision == 0 || definition.revision > MaxRevision || !definition.clientDialect.Valid() {
		return ErrInvalidSpecification
	}
	if len(definition.methods) == 0 || len(definition.methods) > MaxOperationMethods {
		return ErrInvalidSpecification
	}
	for index, method := range definition.methods {
		if !validMethod(method) || index > 0 && method == definition.methods[index-1] {
			return ErrInvalidSpecification
		}
	}
	if !canonicalPath(definition.pathPattern) {
		return ErrInvalidSpecification
	}
	switch definition.pathMatch {
	case ClientOperationPathExact:
	case ClientOperationPathPrefix:
		if definition.pathPattern == "/" {
			return ErrInvalidSpecification
		}
	default:
		return ErrInvalidSpecification
	}
	switch definition.kind {
	case ClientOperationSemantic:
		if definition.pathMatch != ClientOperationPathExact {
			return ErrInvalidSpecification
		}
	case ClientOperationAuxiliary, ClientOperationOpaque, ClientOperationUnsupported:
	default:
		return ErrInvalidSpecification
	}
	switch definition.transport {
	case ClientOperationTransportHTTP:
	case ClientOperationTransportWebSocket:
		if definition.kind != ClientOperationUnsupported || definition.pathMatch != ClientOperationPathExact ||
			len(definition.methods) != 1 || definition.methods[0] != "GET" ||
			definition.bodyKind != ClientOperationBodyNone || definition.egressBearing {
			return ErrInvalidSpecification
		}
	default:
		return ErrInvalidSpecification
	}
	switch definition.bodyKind {
	case ClientOperationBodyNone:
		if definition.maxBodyBytes != 0 {
			return ErrInvalidSpecification
		}
	case ClientOperationBodyJSON, ClientOperationBodyMultipart,
		ClientOperationBodyBytes, ClientOperationBodyStream:
		if definition.maxBodyBytes <= 0 || definition.maxBodyBytes > MaxOperationBody {
			return ErrInvalidSpecification
		}
	default:
		return ErrInvalidSpecification
	}
	if !definition.payloadClass.validCatalogValue() ||
		definition.bodyKind == ClientOperationBodyNone && definition.payloadClass.CarriesClientPayload() ||
		definition.kind == ClientOperationSemantic && definition.payloadClass != OperationPayloadClientSemantic {
		return ErrInvalidSpecification
	}
	switch definition.replayClass {
	case ClientReplaySafe, ClientReplayIdempotencyKeyed, ClientReplayGenerationCostOnly,
		ClientReplaySideEffectPossible, ClientReplayNonReplayable, ClientReplayUnknown:
	default:
		return ErrInvalidSpecification
	}
	switch definition.kind {
	case ClientOperationSemantic, ClientOperationAuxiliary:
		if definition.codecFeature == "" || definition.kind == ClientOperationSemantic && !definition.egressBearing {
			return ErrInvalidSpecification
		}
	case ClientOperationOpaque, ClientOperationUnsupported:
		if definition.codecFeature != "" {
			return ErrInvalidSpecification
		}
	}
	if len(definition.allowedQueries) > MaxOperationQueries ||
		len(definition.allowedQueryKeys) > MaxOperationQueries {
		return ErrInvalidSpecification
	}
	for index, query := range definition.allowedQueries {
		if !canonicalQuery(query) || index > 0 && query == definition.allowedQueries[index-1] {
			return ErrInvalidSpecification
		}
	}
	for index, key := range definition.allowedQueryKeys {
		if !canonicalQueryKey(key) ||
			index > 0 && key == definition.allowedQueryKeys[index-1] {
			return ErrInvalidSpecification
		}
	}
	if definition.kind == ClientOperationUnsupported && definition.egressBearing {
		return ErrInvalidSpecification
	}
	return nil
}

func (definition ClientOperationDefinition) Clone() ClientOperationDefinition {
	cloned := definition
	cloned.methods = slices.Clone(definition.methods)
	cloned.allowedQueries = slices.Clone(definition.allowedQueries)
	cloned.allowedQueryKeys = slices.Clone(definition.allowedQueryKeys)
	return cloned
}

type RequestTarget struct {
	Method    string
	Path      string
	RawPath   string
	RawQuery  string
	Transport ClientOperationTransport
}

func (target RequestTarget) Validate() error {
	if !validMethod(target.Method) || !canonicalPath(target.Path) || target.RawPath != "" ||
		(target.Transport != ClientOperationTransportHTTP && target.Transport != ClientOperationTransportWebSocket) ||
		(target.RawQuery != "" && !wellFormedRawQuery(target.RawQuery)) {
		return ErrInvalidRequestTarget
	}
	return nil
}

// Match reports both whether the complete operation contract matches and
// whether the path alone is known. Callers use pathKnown to distinguish an
// unknown operation from a known path with the wrong method, query, or
// transport without guessing a fallback codec.
func (definition ClientOperationDefinition) Match(target RequestTarget) (matched bool, pathKnown bool, err error) {
	if err := target.Validate(); err != nil {
		return false, false, ErrInvalidRequestTarget
	}
	switch definition.PathMatch() {
	case ClientOperationPathExact:
		pathKnown = target.Path == definition.PathPattern()
	case ClientOperationPathPrefix:
		pathKnown = target.Path == definition.PathPattern() ||
			strings.HasPrefix(target.Path, definition.PathPattern()+"/")
	default:
		return false, false, ErrInvalidSpecification
	}
	if !pathKnown || target.Transport != definition.Transport() ||
		!slices.Contains(definition.Methods(), target.Method) {
		return false, pathKnown, nil
	}
	if !definition.AllowsRawQuery(target.RawQuery) {
		return false, true, nil
	}
	return true, true, nil
}

// SelectOperation applies the catalog's exact-before-longest-prefix rule to
// an already-frozen operation slice. A known path with the wrong method,
// query, or transport never falls through to a broader prefix operation.
func SelectOperation(
	operations []ClientOperationDefinition,
	target RequestTarget,
) (ClientOperationDefinition, error) {
	if err := target.Validate(); err != nil {
		return ClientOperationDefinition{}, err
	}
	var candidates []ClientOperationDefinition
	for _, operation := range operations {
		if operation.PathMatch() == ClientOperationPathExact &&
			operation.PathPattern() == target.Path {
			candidates = append(candidates, operation)
		}
	}
	if len(candidates) == 0 {
		longest := 0
		for _, operation := range operations {
			if operation.PathMatch() != ClientOperationPathPrefix ||
				(target.Path != operation.PathPattern() &&
					!strings.HasPrefix(target.Path, operation.PathPattern()+"/")) {
				continue
			}
			length := len(operation.PathPattern())
			if length > longest {
				longest = length
				candidates = candidates[:0]
			}
			if length == longest {
				candidates = append(candidates, operation)
			}
		}
	}
	if len(candidates) == 0 {
		return ClientOperationDefinition{}, ErrOperationNotCatalogued
	}
	var matched []ClientOperationDefinition
	for _, operation := range candidates {
		complete, _, err := operation.Match(target)
		if err != nil {
			return ClientOperationDefinition{}, err
		}
		if complete {
			matched = append(matched, operation)
		}
	}
	switch len(matched) {
	case 0:
		return ClientOperationDefinition{}, ErrOperationContractMismatch
	case 1:
		return matched[0].Clone(), nil
	default:
		return ClientOperationDefinition{}, ErrAmbiguousOperation
	}
}

type CodecPlan struct {
	id                   CodecPairID
	revision             Revision
	clientDialect        Dialect
	providerDialect      Dialect
	clientOperations     []ClientOperationDefinition
	requiredCapabilities []ProviderCapability
}

func NewCodecPlan(
	id CodecPairID,
	revision Revision,
	clientDialect Dialect,
	providerDialect Dialect,
	definitions []ClientOperationDefinition,
	required []ProviderCapability,
) (CodecPlan, error) {
	if err := validateIdentifier("codec pair ID", id.value); err != nil || revision == 0 ||
		revision > MaxRevision || !clientDialect.Valid() || !providerDialect.Valid() || len(definitions) == 0 {
		return CodecPlan{}, ErrInvalidSpecification
	}
	operations := make([]ClientOperationDefinition, 0, len(definitions))
	seen := make(map[ClientOperationID]struct{}, len(definitions))
	for _, definition := range definitions {
		if definition.ClientDialect() != clientDialect {
			return CodecPlan{}, ErrInvalidSpecification
		}
		if _, duplicate := seen[definition.ID()]; duplicate {
			return CodecPlan{}, ErrInvalidSpecification
		}
		seen[definition.ID()] = struct{}{}
		if err := definition.Validate(); err != nil {
			return CodecPlan{}, err
		}
		operations = append(operations, definition.Clone())
	}
	return CodecPlan{
		id: id, revision: revision, clientDialect: clientDialect,
		providerDialect: providerDialect, clientOperations: operations,
		requiredCapabilities: slices.Clone(required),
	}, nil
}

func (plan CodecPlan) ID() CodecPairID          { return plan.id }
func (plan CodecPlan) Revision() Revision       { return plan.revision }
func (plan CodecPlan) ClientDialect() Dialect   { return plan.clientDialect }
func (plan CodecPlan) ProviderDialect() Dialect { return plan.providerDialect }
func (plan CodecPlan) ClientOperations() []ClientOperationDefinition {
	return cloneOperationDefinitions(plan.clientOperations)
}
func (plan CodecPlan) RequiredCapabilities() []ProviderCapability {
	return slices.Clone(plan.requiredCapabilities)
}
func (plan CodecPlan) Valid() bool {
	return plan.id.value != "" && plan.revision != 0 && plan.clientDialect.Valid() &&
		plan.providerDialect.Valid() && len(plan.clientOperations) != 0
}

func canonicalPath(value string) bool {
	if value == "" || len(value) > MaxOperationPath || !utf8.ValidString(value) || value[0] != '/' ||
		strings.ContainsAny(value, "\\%\x00\r\n\t") || pathpkg.Clean(value) != value ||
		value != "/" && strings.HasSuffix(value, "/") {
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
		if (character < 'A' || character > 'Z') && character != '-' {
			return false
		}
	}
	return true
}

func canonicalQuery(value string) bool {
	if value == "" || len(value) > 2048 || !utf8.ValidString(value) {
		return false
	}
	parsed, err := url.ParseQuery(value)
	return err == nil && parsed.Encode() == value
}

func canonicalQueryKey(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	return url.QueryEscape(value) == value
}

func allowsRawQuery(exactQueries, allowedKeys []string, rawQuery string) bool {
	if rawQuery == "" {
		return true
	}
	if canonicalQuery(rawQuery) && slices.Contains(exactQueries, rawQuery) {
		return true
	}
	if len(allowedKeys) == 0 {
		return false
	}
	keys, valid := rawQueryKeys(rawQuery)
	if !valid {
		return false
	}
	for _, key := range keys {
		if !slices.Contains(allowedKeys, key) {
			return false
		}
	}
	return len(keys) > 0
}

func wellFormedRawQuery(value string) bool {
	_, valid := parseRawQueryKeys(value, false)
	return valid
}

// rawQueryKeys validates each field's encoding while deliberately leaving
// field order out of the contract. HTTP query ordering is not semantic, but
// duplicate keys and alternate encodings are: rejecting both keeps admission
// deterministic without forcing clients to sort independent parameters.
func rawQueryKeys(value string) ([]string, bool) {
	return parseRawQueryKeys(value, true)
}

func parseRawQueryKeys(value string, rejectDuplicates bool) ([]string, bool) {
	if value == "" || len(value) > 2048 || !utf8.ValidString(value) {
		return nil, false
	}
	keys := make([]string, 0, strings.Count(value, "&")+1)
	seen := make(map[string]struct{})
	for _, field := range strings.Split(value, "&") {
		rawKey, rawValue, present := strings.Cut(field, "=")
		if !present || rawKey == "" {
			return nil, false
		}
		key, keyErr := url.QueryUnescape(rawKey)
		decodedValue, valueErr := url.QueryUnescape(rawValue)
		if keyErr != nil || valueErr != nil ||
			url.QueryEscape(key) != rawKey ||
			url.QueryEscape(decodedValue) != rawValue {
			return nil, false
		}
		if _, duplicate := seen[key]; rejectDuplicates && duplicate {
			return nil, false
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys, true
}
