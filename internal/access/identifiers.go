package access

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	pathpkg "path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxAccessIDBytes             = 128
	MaxResourceIDBytes           = 128
	MaxAccessNameBytes           = 256
	MaxDescriptionBytes          = 4096
	MaxLabelBytes                = 256
	MaxOriginBytes               = 2048
	MaxSecretRefBytes            = 1024
	MaxModelNameBytes            = 256
	MaxRevision         Revision = 1<<63 - 1
)

// Revision is a monotonic revision within its owning resource.
type Revision uint64

type AccessID struct{ value string }
type AgentEndpointID struct{ value string }
type EndpointProfileID struct{ value string }
type ProviderTargetID struct{ value string }
type AccountBindingID struct{ value string }
type RouteSetID struct{ value string }
type EgressPolicyID struct{ value string }
type PluginBindingID struct{ value string }
type ModelMappingRef struct{ value string }
type AuthDriverRef struct{ value string }
type CodecPairID struct{ value string }
type ClientOperationID struct{ value string }
type TransportProfileRef struct{ value string }
type UpstreamWireProfileRef struct{ value string }

func NewAccessID(value string) (AccessID, error) {
	if err := validateIdentifier("Access ID", value, MaxAccessIDBytes); err != nil {
		return AccessID{}, err
	}
	return AccessID{value: value}, nil
}

func NewAgentEndpointID(value string) (AgentEndpointID, error) {
	if err := validateIdentifier("AgentEndpoint ID", value, MaxResourceIDBytes); err != nil {
		return AgentEndpointID{}, err
	}
	return AgentEndpointID{value: value}, nil
}

func NewEndpointProfileID(value string) (EndpointProfileID, error) {
	if err := validateIdentifier("EndpointProfile ID", value, MaxResourceIDBytes); err != nil {
		return EndpointProfileID{}, err
	}
	return EndpointProfileID{value: value}, nil
}

func NewProviderTargetID(value string) (ProviderTargetID, error) {
	if err := validateIdentifier("ProviderTarget ID", value, MaxResourceIDBytes); err != nil {
		return ProviderTargetID{}, err
	}
	return ProviderTargetID{value: value}, nil
}

func NewAccountBindingID(value string) (AccountBindingID, error) {
	if err := validateIdentifier("account binding ID", value, MaxResourceIDBytes); err != nil {
		return AccountBindingID{}, err
	}
	return AccountBindingID{value: value}, nil
}

func NewRouteSetID(value string) (RouteSetID, error) {
	if err := validateIdentifier("RouteSet ID", value, MaxResourceIDBytes); err != nil {
		return RouteSetID{}, err
	}
	return RouteSetID{value: value}, nil
}

func NewEgressPolicyID(value string) (EgressPolicyID, error) {
	if err := validateIdentifier("egress policy ID", value, MaxResourceIDBytes); err != nil {
		return EgressPolicyID{}, err
	}
	return EgressPolicyID{value: value}, nil
}

func NewPluginBindingID(value string) (PluginBindingID, error) {
	if err := validateIdentifier("plugin binding ID", value, MaxResourceIDBytes); err != nil {
		return PluginBindingID{}, err
	}
	return PluginBindingID{value: value}, nil
}

func NewModelMappingRef(value string) (ModelMappingRef, error) {
	if err := validateIdentifier("model mapping reference", value, MaxResourceIDBytes); err != nil {
		return ModelMappingRef{}, err
	}
	return ModelMappingRef{value: value}, nil
}

func NewAuthDriverRef(value string) (AuthDriverRef, error) {
	if err := validateIdentifier("AuthDriver reference", value, MaxResourceIDBytes); err != nil {
		return AuthDriverRef{}, err
	}
	return AuthDriverRef{value: value}, nil
}

func NewCodecPairID(value string) (CodecPairID, error) {
	if err := validateIdentifier("codec pair ID", value, MaxResourceIDBytes); err != nil {
		return CodecPairID{}, err
	}
	return CodecPairID{value: value}, nil
}

func NewClientOperationID(value string) (ClientOperationID, error) {
	if err := validateIdentifier(
		"client operation ID",
		value,
		MaxResourceIDBytes,
	); err != nil {
		return ClientOperationID{}, err
	}
	return ClientOperationID{value: value}, nil
}

func NewTransportProfileRef(value string) (TransportProfileRef, error) {
	if err := validateIdentifier(
		"transport fingerprint profile reference",
		value,
		MaxResourceIDBytes,
	); err != nil {
		return TransportProfileRef{}, err
	}
	return TransportProfileRef{value: value}, nil
}

func NewUpstreamWireProfileRef(value string) (UpstreamWireProfileRef, error) {
	if err := validateIdentifier(
		"upstream wire profile reference",
		value,
		MaxResourceIDBytes,
	); err != nil {
		return UpstreamWireProfileRef{}, err
	}
	return UpstreamWireProfileRef{value: value}, nil
}

func (id AccessID) String() string          { return id.value }
func (id AgentEndpointID) String() string   { return id.value }
func (id EndpointProfileID) String() string { return id.value }
func (id ProviderTargetID) String() string  { return id.value }
func (id AccountBindingID) String() string  { return id.value }
func (id RouteSetID) String() string        { return id.value }
func (id EgressPolicyID) String() string    { return id.value }
func (id PluginBindingID) String() string   { return id.value }
func (ref ModelMappingRef) String() string  { return ref.value }
func (ref AuthDriverRef) String() string    { return ref.value }
func (id CodecPairID) String() string       { return id.value }
func (id ClientOperationID) String() string { return id.value }
func (ref TransportProfileRef) String() string {
	return ref.value
}
func (ref UpstreamWireProfileRef) String() string { return ref.value }

func (id AccessID) validate() error {
	return validateIdentifier("Access ID", id.value, MaxAccessIDBytes)
}
func (id AgentEndpointID) validate() error {
	return validateIdentifier("AgentEndpoint ID", id.value, MaxResourceIDBytes)
}
func (id EndpointProfileID) validate() error {
	return validateIdentifier("EndpointProfile ID", id.value, MaxResourceIDBytes)
}
func (id ProviderTargetID) validate() error {
	return validateIdentifier("ProviderTarget ID", id.value, MaxResourceIDBytes)
}
func (id AccountBindingID) validate() error {
	return validateIdentifier("account binding ID", id.value, MaxResourceIDBytes)
}
func (id RouteSetID) validate() error {
	return validateIdentifier("RouteSet ID", id.value, MaxResourceIDBytes)
}
func (id EgressPolicyID) validate() error {
	return validateIdentifier("egress policy ID", id.value, MaxResourceIDBytes)
}
func (id PluginBindingID) validate() error {
	return validateIdentifier("plugin binding ID", id.value, MaxResourceIDBytes)
}
func (ref ModelMappingRef) validate() error {
	return validateIdentifier("model mapping reference", ref.value, MaxResourceIDBytes)
}
func (ref AuthDriverRef) validate() error {
	return validateIdentifier("AuthDriver reference", ref.value, MaxResourceIDBytes)
}
func (id CodecPairID) validate() error {
	return validateIdentifier("codec pair ID", id.value, MaxResourceIDBytes)
}
func (id ClientOperationID) validate() error {
	return validateIdentifier("client operation ID", id.value, MaxResourceIDBytes)
}
func (ref TransportProfileRef) validate() error {
	return validateIdentifier(
		"transport fingerprint profile reference",
		ref.value,
		MaxResourceIDBytes,
	)
}
func (ref UpstreamWireProfileRef) validate() error {
	return validateIdentifier(
		"upstream wire profile reference",
		ref.value,
		MaxResourceIDBytes,
	)
}

func validateIdentifier(label, value string, limit int) error {
	if value == "" {
		return fmt.Errorf("%w: %s is empty", ErrInvalidAccess, label)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s is not valid UTF-8", ErrInvalidAccess, label)
	}
	if len(value) > limit {
		return fmt.Errorf("%w: %s exceeds the byte limit", ErrInvalidAccess, label)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %s has surrounding whitespace", ErrInvalidAccess, label)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: %s contains a control character", ErrInvalidAccess, label)
		}
	}
	return nil
}

// Dialect identifies one supported wire protocol without selecting an
// implementation through a string registry.
type Dialect string

const (
	DialectAnthropicMessages Dialect = "anthropic-messages"
	DialectOpenAIResponses   Dialect = "openai-responses"
	DialectOpenAIChat        Dialect = "openai-chat"
)

type ProviderCapability string

const (
	ProviderCapabilityMessages  ProviderCapability = "messages"
	ProviderCapabilityStreaming ProviderCapability = "streaming"
	ProviderCapabilityToolCalls ProviderCapability = "tool_calls"
)

type AccessStatus string

const (
	AccessStatusDraft    AccessStatus = "draft"
	AccessStatusEnabled  AccessStatus = "enabled"
	AccessStatusDisabled AccessStatus = "disabled"
)

type ModelPolicyMode string

const (
	ModelPolicyModePassthrough ModelPolicyMode = "passthrough"
	ModelPolicyModeFixed       ModelPolicyMode = "fixed"
	ModelPolicyModeMap         ModelPolicyMode = "map"
)

type EgressMode string

const EgressModeDirect EgressMode = "direct"

type ProviderTransportKind string

const (
	ProviderTransportStrictTLS         ProviderTransportKind = "strict_tls"
	ProviderTransportLoopbackCleartext ProviderTransportKind = "loopback_cleartext"
)

type PluginPlanMode string

const PluginPlanModePassThrough PluginPlanMode = "pass_through"

const (
	AuthDriverStaticHeaderValue    = "static_header"
	AuthDriverAnthropicAPIKeyValue = "anthropic_api_key"
)
const (
	TransportProfileObservedClientH1Value = "observed-client-strict-h1"
	TransportProfileObservedClientH2Value = "observed-client-strict-h2"
	TransportProfileStandardH1Value       = "standard-strict-h1"
	TransportProfileStandardH2Value       = "standard-strict-h2"
	TransportProfileClaudeCodeH1Value     = "claude-code-2.1.220-darwin-arm64-strict-h1"
)

const (
	UpstreamWireProfileFollowClientValue = "follow-client"
	UpstreamWireProfileClaudeCodeValue   = "claude-code"
)

func StaticHeaderAuthDriverRef() AuthDriverRef {
	return AuthDriverRef{value: AuthDriverStaticHeaderValue}
}

func AnthropicAPIKeyAuthDriverRef() AuthDriverRef {
	return AuthDriverRef{value: AuthDriverAnthropicAPIKeyValue}
}

func ObservedClientH1TransportProfileRef() TransportProfileRef {
	return TransportProfileRef{value: TransportProfileObservedClientH1Value}
}

func ObservedClientH2TransportProfileRef() TransportProfileRef {
	return TransportProfileRef{value: TransportProfileObservedClientH2Value}
}

func StandardH1TransportProfileRef() TransportProfileRef {
	return TransportProfileRef{value: TransportProfileStandardH1Value}
}

func StandardH2TransportProfileRef() TransportProfileRef {
	return TransportProfileRef{value: TransportProfileStandardH2Value}
}

func ClaudeCodeH1TransportProfileRef() TransportProfileRef {
	return TransportProfileRef{value: TransportProfileClaudeCodeH1Value}
}

func FollowClientUpstreamWireProfileRef() UpstreamWireProfileRef {
	return UpstreamWireProfileRef{value: UpstreamWireProfileFollowClientValue}
}

func ClaudeCodeUpstreamWireProfileRef() UpstreamWireProfileRef {
	return UpstreamWireProfileRef{value: UpstreamWireProfileClaudeCodeValue}
}

type ModelName struct{ value string }

func NewModelName(value string) (ModelName, error) {
	if err := validateBoundedText("model name", value, MaxModelNameBytes, false); err != nil {
		return ModelName{}, err
	}
	return ModelName{value: value}, nil
}

func (name ModelName) String() string { return name.value }

func (name ModelName) validate() error {
	return validateBoundedText("model name", name.value, MaxModelNameBytes, false)
}

type SecretRef struct{ value string }

func NewSecretRef(value string) (SecretRef, error) {
	if len(value) == 0 || len(value) > MaxSecretRefBytes || !utf8.ValidString(value) {
		return SecretRef{}, fmt.Errorf("%w: SecretRef is invalid", ErrInvalidAccess)
	}
	parsed, err := url.Parse(value)
	if err != nil ||
		parsed.Scheme != "secret" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" ||
		(parsed.Host == "" && strings.Trim(parsed.Path, "/") == "") {
		return SecretRef{}, fmt.Errorf("%w: SecretRef must be an opaque secret:// reference", ErrInvalidAccess)
	}
	return SecretRef{value: value}, nil
}

func (ref SecretRef) String() string { return ref.value }

func (ref SecretRef) validate() error {
	_, err := NewSecretRef(ref.value)
	return err
}

type ClientOrigin struct {
	value         string
	httpAuthority string
	tlsServerName string
	port          uint16
}

func NewClientOrigin(value string) (ClientOrigin, error) {
	origin, err := parseOrigin(value, false, false)
	if err != nil {
		return ClientOrigin{}, fmt.Errorf("%w: ClientOrigin: %w", ErrInvalidAccess, err)
	}
	return ClientOrigin{
		value:         origin.value,
		httpAuthority: origin.httpAuthority,
		tlsServerName: origin.tlsServerName,
		port:          origin.port,
	}, nil
}

func (origin ClientOrigin) String() string        { return origin.value }
func (origin ClientOrigin) HTTPAuthority() string { return origin.httpAuthority }
func (origin ClientOrigin) TLSServerName() string { return origin.tlsServerName }
func (origin ClientOrigin) Port() uint16          { return origin.port }
func (origin ClientOrigin) EndpointAuthority() string {
	return net.JoinHostPort(origin.tlsServerName, strconv.Itoa(int(origin.port)))
}

func (origin ClientOrigin) validate() error {
	parsed, err := NewClientOrigin(origin.value)
	if err != nil {
		return err
	}
	if parsed != origin {
		return fmt.Errorf("%w: ClientOrigin is not canonical", ErrInvalidAccess)
	}
	return nil
}

type ProviderOrigin struct {
	value         string
	scheme        string
	basePath      string
	httpAuthority string
	networkHost   string
	tlsServerName string
	port          uint16
	transportKind ProviderTransportKind
}

func NewProviderOrigin(value string) (ProviderOrigin, error) {
	origin, err := parseOrigin(value, true, true)
	if err != nil {
		return ProviderOrigin{}, fmt.Errorf("%w: ProviderTarget origin: %w", ErrInvalidAccess, err)
	}
	return ProviderOrigin{
		value:         origin.value,
		scheme:        origin.scheme,
		basePath:      origin.basePath,
		httpAuthority: origin.httpAuthority,
		networkHost:   origin.networkHost,
		tlsServerName: origin.tlsServerName,
		port:          origin.port,
		transportKind: origin.transportKind,
	}, nil
}

func (origin ProviderOrigin) String() string        { return origin.value }
func (origin ProviderOrigin) Scheme() string        { return origin.scheme }
func (origin ProviderOrigin) BasePath() string      { return origin.basePath }
func (origin ProviderOrigin) HTTPAuthority() string { return origin.httpAuthority }
func (origin ProviderOrigin) NetworkHost() string   { return origin.networkHost }
func (origin ProviderOrigin) TLSServerName() string { return origin.tlsServerName }
func (origin ProviderOrigin) Port() uint16          { return origin.port }
func (origin ProviderOrigin) TransportKind() ProviderTransportKind {
	return origin.transportKind
}
func (origin ProviderOrigin) EndpointAuthority() string {
	return net.JoinHostPort(origin.networkHost, strconv.Itoa(int(origin.port)))
}

func (origin ProviderOrigin) validate() error {
	parsed, err := NewProviderOrigin(origin.value)
	if err != nil {
		return err
	}
	if parsed != origin {
		return fmt.Errorf("%w: ProviderTarget origin is not canonical", ErrInvalidAccess)
	}
	return nil
}

type parsedOrigin struct {
	value         string
	scheme        string
	basePath      string
	httpAuthority string
	networkHost   string
	tlsServerName string
	port          uint16
	transportKind ProviderTransportKind
}

func parseOrigin(
	value string,
	allowBasePath bool,
	allowLoopbackHTTP bool,
) (parsedOrigin, error) {
	if value == "" || len(value) > MaxOriginBytes || !utf8.ValidString(value) {
		return parsedOrigin{}, errorsForOrigin("origin is empty or exceeds the byte limit")
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" {
		return parsedOrigin{}, errorsForOrigin("origin is not an absolute URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return parsedOrigin{}, errorsForOrigin("origin cannot contain user info, query, or fragment")
	}
	if parsed.RawPath != "" {
		return parsedOrigin{}, errorsForOrigin("origin path cannot use alternate escaping")
	}

	var (
		defaultPort   uint16
		transportKind ProviderTransportKind
	)
	switch parsed.Scheme {
	case "https":
		defaultPort = 443
		transportKind = ProviderTransportStrictTLS
	case "http":
		if !allowLoopbackHTTP {
			return parsedOrigin{}, errorsForOrigin("origin scheme must be https")
		}
		address, addressErr := netip.ParseAddr(parsed.Hostname())
		if addressErr != nil ||
			!address.IsLoopback() ||
			address.Is4In6() {
			return parsedOrigin{}, errorsForOrigin(
				"cleartext provider origin must use a literal loopback IP",
			)
		}
		defaultPort = 80
		transportKind = ProviderTransportLoopbackCleartext
	default:
		return parsedOrigin{}, errorsForOrigin("origin scheme is unsupported")
	}

	authority, networkHost, port, err := canonicalAuthority(parsed, defaultPort)
	if err != nil {
		return parsedOrigin{}, err
	}
	basePath := parsed.Path
	if basePath == "/" {
		basePath = ""
	}
	if !allowBasePath && basePath != "" {
		return parsedOrigin{}, errorsForOrigin("ClientOrigin cannot contain a path")
	}
	if basePath != "" {
		if !strings.HasPrefix(basePath, "/") ||
			pathpkg.Clean(basePath) != basePath ||
			strings.HasSuffix(basePath, "/") {
			return parsedOrigin{}, errorsForOrigin("provider base path is not canonical")
		}
	}
	tlsServerName := networkHost
	if transportKind == ProviderTransportLoopbackCleartext {
		tlsServerName = ""
	}
	return parsedOrigin{
		value:         parsed.Scheme + "://" + authority + basePath,
		scheme:        parsed.Scheme,
		basePath:      basePath,
		httpAuthority: authority,
		networkHost:   networkHost,
		tlsServerName: tlsServerName,
		port:          port,
		transportKind: transportKind,
	}, nil
}

func canonicalAuthority(
	parsed *url.URL,
	defaultPort uint16,
) (string, string, uint16, error) {
	host := strings.ToLower(parsed.Hostname())
	if host == "" || strings.Contains(host, "%") {
		return "", "", 0, errorsForOrigin("origin host is invalid")
	}
	if address, err := netip.ParseAddr(host); err == nil {
		host = address.String()
	} else if err := validateDNSName(host); err != nil {
		return "", "", 0, err
	}
	port := parsed.Port()
	if port != "" {
		number, err := strconv.ParseUint(port, 10, 16)
		if err != nil || number == 0 {
			return "", "", 0, errorsForOrigin("origin port is invalid")
		}
		return net.JoinHostPort(host, port), host, uint16(number), nil
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]", host, defaultPort, nil
	}
	return host, host, defaultPort, nil
}

func validateDNSName(host string) error {
	if len(host) > 253 || strings.HasSuffix(host, ".") || strings.Contains(host, "*") {
		return errorsForOrigin("origin DNS name is invalid")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 ||
			label[0] == '-' || label[len(label)-1] == '-' {
			return errorsForOrigin("origin DNS label is invalid")
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') &&
				(character < '0' || character > '9') &&
				character != '-' {
				return errorsForOrigin("origin DNS name must be canonical ASCII")
			}
		}
	}
	return nil
}

func errorsForOrigin(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidAccess, message)
}

func validateBoundedText(label, value string, limit int, allowEmpty bool) error {
	if (!allowEmpty && value == "") || !utf8.ValidString(value) || len(value) > limit {
		return fmt.Errorf("%w: %s is invalid", ErrInvalidAccess, label)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %s has surrounding whitespace", ErrInvalidAccess, label)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: %s contains a control character", ErrInvalidAccess, label)
		}
	}
	return nil
}
