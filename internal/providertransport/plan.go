package providertransport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	pathpkg "path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

const (
	MaxProviderRequestBytes = 16 << 20
	maxRequestIdentityBytes = 1024
)

type Target struct {
	origin originidentity.ProviderOrigin
}

func NewTarget(origin originidentity.ProviderOrigin) (Target, error) {
	if err := origin.Validate(); err != nil {
		return Target{}, errors.New("provider target origin is invalid")
	}
	return Target{origin: origin}, nil
}

func (target Target) Origin() originidentity.ProviderOrigin {
	return target.origin
}

func (target Target) HTTPAuthority() string {
	return target.origin.HTTPAuthority()
}

func (target Target) NetworkHost() string {
	return target.origin.Host()
}

func (target Target) TLSServerName() string {
	if target.origin.Transport() == originidentity.ProviderTransportStrictTLS {
		return target.origin.Host()
	}
	return ""
}

func (target Target) TransportKind() originidentity.ProviderTransport {
	return target.origin.Transport()
}

func (target Target) BasePath() string {
	return target.origin.BasePath()
}

func (target Target) endpointAuthority() (string, error) {
	if err := target.validate(); err != nil {
		return "", errors.New("provider target endpoint authority is invalid")
	}
	return target.origin.EndpointAuthority(), nil
}

func (target Target) validate() error {
	if err := target.origin.Validate(); err != nil {
		return errors.New("provider target is not canonical")
	}
	return nil
}

func (target Target) validateRequestIdentity(request *http.Request) error {
	if request == nil || request.URL == nil {
		return errors.New("provider request identity is incomplete")
	}
	if err := target.validate(); err != nil {
		return err
	}
	if request.URL.Scheme != target.origin.Scheme() ||
		request.URL.Host != target.origin.HTTPAuthority() ||
		request.Host != target.origin.HTTPAuthority() {
		return errors.New("provider request identity is not frozen")
	}
	return nil
}

// RequestProvenance binds one outbound to the immutable Environment and Route
// that authorized it. It contains no mutable aggregate, account secret, or
// client payload.
type RequestProvenance struct {
	environmentID       environment.EnvironmentID
	environmentRevision environment.Revision
	environmentDigest   environment.CandidateDigest
	routeID             environment.UpstreamRouteID
	routeRevision       environment.Revision
}

func NewRequestProvenance(
	environmentID environment.EnvironmentID,
	environmentRevision environment.Revision,
	environmentDigest environment.CandidateDigest,
	routeID environment.UpstreamRouteID,
	routeRevision environment.Revision,
) (RequestProvenance, error) {
	provenance := RequestProvenance{
		environmentID: environmentID, environmentRevision: environmentRevision,
		environmentDigest: environmentDigest, routeID: routeID, routeRevision: routeRevision,
	}
	if err := provenance.validate(); err != nil {
		return RequestProvenance{}, err
	}
	return provenance, nil
}

func (provenance RequestProvenance) EnvironmentID() environment.EnvironmentID {
	return provenance.environmentID
}
func (provenance RequestProvenance) EnvironmentRevision() environment.Revision {
	return provenance.environmentRevision
}
func (provenance RequestProvenance) EnvironmentDigest() environment.CandidateDigest {
	return provenance.environmentDigest
}
func (provenance RequestProvenance) RouteID() environment.UpstreamRouteID {
	return provenance.routeID
}
func (provenance RequestProvenance) RouteRevision() environment.Revision {
	return provenance.routeRevision
}

func (provenance RequestProvenance) validate() error {
	environmentID, environmentErr := environment.NewEnvironmentID(provenance.environmentID.String())
	routeID, routeErr := environment.NewUpstreamRouteID(provenance.routeID.String())
	digest, digestErr := environment.ParseCandidateDigest(provenance.environmentDigest.String())
	if environmentErr != nil || environmentID != provenance.environmentID ||
		routeErr != nil || routeID != provenance.routeID ||
		digestErr != nil || digest != provenance.environmentDigest ||
		provenance.environmentDigest == (environment.CandidateDigest{}) ||
		provenance.environmentRevision == 0 || provenance.environmentRevision > environment.MaxRevision ||
		provenance.routeRevision == 0 || provenance.routeRevision > environment.MaxRevision {
		return errors.New("provider request provenance is invalid")
	}
	return nil
}

func (provenance RequestProvenance) probePlanDigest() string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		provenance.environmentID.String(),
		fmt.Sprintf("%d", provenance.environmentRevision),
		provenance.environmentDigest.String(),
		provenance.routeID.String(),
		fmt.Sprintf("%d", provenance.routeRevision),
	}, "\x00")))
	return hex.EncodeToString(digest[:])
}

// NewProbeTarget binds a provider network identity to one frozen Environment
// and Route. TargetRef remains the caller's stable target reference while the
// explicit plan revision and digest prevent distinct frozen plans from
// coalescing in Offline Hold.
func NewProbeTarget(
	targetRef string,
	provenance RequestProvenance,
	target Target,
) (offlinehold.ProbeTarget, error) {
	if err := validateOpaqueIdentity("provider target reference", targetRef); err != nil {
		return offlinehold.ProbeTarget{}, err
	}
	if err := provenance.validate(); err != nil {
		return offlinehold.ProbeTarget{}, err
	}
	if err := target.validate(); err != nil {
		return offlinehold.ProbeTarget{}, err
	}
	probeTransport, err := target.probeTransportKind()
	if err != nil {
		return offlinehold.ProbeTarget{}, err
	}
	frozen := offlinehold.ProbeTarget{
		Kind:          offlinehold.EgressProvider,
		Transport:     probeTransport,
		TargetRef:     targetRef,
		NetworkOrigin: target.origin.String(),
		HTTPAuthority: target.origin.HTTPAuthority(),
		TLSServerName: target.TLSServerName(),
		PlanRevision:  uint64(provenance.environmentRevision),
		PlanDigest:    provenance.probePlanDigest(),
	}
	if err := frozen.Validate(); err != nil {
		return offlinehold.ProbeTarget{}, err
	}
	return frozen, nil
}

func (target Target) probeTransportKind() (
	offlinehold.ProbeTransportKind,
	error,
) {
	switch target.origin.Transport() {
	case originidentity.ProviderTransportStrictTLS:
		return offlinehold.ProbeTransportStrictTLS, nil
	case originidentity.ProviderTransportLoopbackCleartext:
		return offlinehold.ProbeTransportLoopbackCleartext, nil
	default:
		return "", errors.New("provider target transport kind is unsupported")
	}
}

type RequestOptions struct {
	RequestID       string
	TargetRef       string
	Target          Target
	Provenance      RequestProvenance
	Action          *offlinehold.ActionLease
	Method          string
	RelativePath    string
	Headers         http.Header
	Body            []byte
	RawQuery        string
	CredentialMode  providerauth.CredentialMode
	ClientOrigin    originidentity.ClientOrigin
	AccountRef      providerauth.AccountRef
	SecretRef       secretstore.Reference
	AuthDriverRef   providerauth.DriverRef
	WireProfile     wireprofile.CompiledUpstreamWireProfile
	ClientProtocol  wireprofile.ApplicationProtocol
	ClientUserAgent string
	ClientHello     transportprofile.Observation
	// ConnectionID, ExchangeID, and ParentAttemptID associate this outbound
	// with the client connection, the Exchange, and the upstream attempt that
	// caused it. They travel as typed references so no identity encodes
	// containment of another.
	ConnectionID    string
	ExchangeID      string
	ParentAttemptID string
	// EgressAttemptID identifies the outbound itself. It is minted separately
	// from ParentAttemptID, because an identity that repeats its parent
	// encodes containment.
	EgressAttemptID string
}

// Request is a frozen provider representation. Its URL, authority, body,
// credential reference, and auth driver cannot be mutated after construction.
type Request struct {
	connectionID    string
	exchangeID      string
	parentAttemptID string
	egressAttemptID string
	requestID       string
	targetRef       string
	provenance      RequestProvenance
	target          Target
	probeTarget     offlinehold.ProbeTarget
	action          *offlinehold.ActionLease
	method          string
	relativePath    string
	headers         http.Header
	body            []byte
	rawQuery        string
	credentialMode  providerauth.CredentialMode
	accountRef      providerauth.AccountRef
	secretReference secretstore.Reference
	authDriverRef   providerauth.DriverRef
	wireProfile     wireprofile.CompiledUpstreamWireProfile
	wireVariant     wireprofile.CompiledUpstreamWireVariant
	clientProtocol  wireprofile.ApplicationProtocol
	clientUserAgent string
	clientHello     transportprofile.Observation
}

// WirePresentationEvidence is the redacted product-level presentation chosen
// before any secret lookup or outbound dial. It contains no raw ClientHello,
// request header, credential, or payload.
type WirePresentationEvidence struct {
	RequestedRef     string
	EffectiveRef     string
	Revision         wireprofile.Revision
	Mode             wireprofile.UpstreamWireMode
	Product          wireprofile.UpstreamWireProduct
	ClientProtocol   wireprofile.ApplicationProtocol
	UpstreamProtocol wireprofile.ApplicationProtocol
	EvidenceDigest   string
}

func NewWirePresentationEvidence(
	profile wireprofile.CompiledUpstreamWireProfile,
	clientProtocol wireprofile.ApplicationProtocol,
) WirePresentationEvidence {
	evidence := WirePresentationEvidence{
		RequestedRef:   profile.Ref().String(),
		Revision:       profile.Revision(),
		Mode:           profile.Mode(),
		Product:        profile.Product(),
		ClientProtocol: clientProtocol,
	}
	variant, available := profile.Variant(clientProtocol)
	if !available {
		return evidence
	}
	evidence.EffectiveRef = profile.Ref().String()
	evidence.UpstreamProtocol = variant.Protocol()
	evidence.EvidenceDigest = variant.EvidenceDigest()
	return evidence
}

func NewRequest(options RequestOptions) (Request, error) {
	if err := validateOpaqueIdentity("provider request ID", options.RequestID); err != nil {
		return Request{}, err
	}
	if err := validateOpaqueIdentity("provider target reference", options.TargetRef); err != nil {
		return Request{}, err
	}
	probeTarget, err := NewProbeTarget(
		options.TargetRef,
		options.Provenance,
		options.Target,
	)
	if err != nil {
		return Request{}, err
	}
	if options.Action == nil {
		return Request{}, errors.New("provider request has no data-plane Action lease")
	}
	// A downstream connection is optional: a runtime-originated Exchange has
	// none. The Exchange and attempt identities are always present.
	for label, value := range map[string]string{
		"provider Exchange ID":       options.ExchangeID,
		"provider parent attempt ID": options.ParentAttemptID,
		"provider egress attempt ID": options.EgressAttemptID,
	} {
		if err := validateOpaqueIdentity(label, value); err != nil {
			return Request{}, err
		}
	}
	if options.ConnectionID != "" {
		if err := validateOpaqueIdentity(
			"provider connection ID",
			options.ConnectionID,
		); err != nil {
			return Request{}, err
		}
	}
	// ADR-0015 §10: an outbound attempt's identity is independent of the
	// attempt it belongs to. Reusing one value for both makes the identity
	// encode its parent, which the audit refuses, which means the outbound
	// cannot be recorded, which means it must not go out.
	if options.EgressAttemptID == options.ParentAttemptID {
		return Request{}, errors.New(
			"provider egress attempt ID repeats its parent attempt ID",
		)
	}
	if options.Method != http.MethodPost {
		return Request{}, errors.New("provider request method must be POST")
	}
	relativePath, err := canonicalRelativePath(options.RelativePath)
	if err != nil {
		return Request{}, err
	}
	if len(options.Body) == 0 || len(options.Body) > MaxProviderRequestBytes {
		return Request{}, errors.New("provider request body has an invalid size")
	}
	if err := validateRawQuery(options.RawQuery); err != nil {
		return Request{}, err
	}
	var reference secretstore.Reference
	switch options.CredentialMode {
	case providerauth.CredentialManaged:
		reference, err = secretstore.ParseReference(options.SecretRef.String())
		if err != nil {
			return Request{}, err
		}
		if reference != options.SecretRef {
			return Request{}, errors.New("provider secret reference is not canonical")
		}
		driver, driverErr := providerauth.NewDriverRef(options.AuthDriverRef.String())
		if driverErr != nil || driver != options.AuthDriverRef {
			return Request{}, errors.New("provider AuthDriver reference is empty")
		}
		if err := options.AccountRef.Validate(); err != nil {
			return Request{}, errors.New("provider account reference is invalid")
		}
		if options.ClientOrigin.String() != "" {
			return Request{}, errors.New(
				"managed provider request carries a client origin authority",
			)
		}
	case providerauth.CredentialClientPassthrough:
		clientOrigin, originErr := originidentity.ParseClientOrigin(options.ClientOrigin.String())
		if originErr != nil || clientOrigin != options.ClientOrigin {
			return Request{}, errors.New(
				"client passthrough origin is not canonical",
			)
		}
		if options.Target.origin.String() != options.ClientOrigin.String() ||
			options.Target.origin.BasePath() != "" {
			return Request{}, errors.New(
				"client passthrough target differs from the exact client origin",
			)
		}
		if options.SecretRef.String() != "" ||
			options.AuthDriverRef.String() != "" ||
			options.AccountRef != (providerauth.AccountRef{}) {
			return Request{}, errors.New(
				"client passthrough request carries managed credential authority",
			)
		}
	default:
		return Request{}, errors.New("provider credential source is invalid")
	}
	if options.WireProfile.Ref().String() == "" ||
		options.WireProfile.Revision() == 0 {
		return Request{}, errors.New(
			"provider upstream wire profile is incomplete",
		)
	}
	if !options.ClientProtocol.Valid() {
		return Request{}, errors.New("provider client HTTP protocol is invalid")
	}
	if options.ClientUserAgent != "" &&
		!validPresentationUserAgent(options.ClientUserAgent) {
		return Request{}, errors.New("provider client User-Agent is invalid")
	}
	wireVariant, available := options.WireProfile.Variant(options.ClientProtocol)
	if !available {
		return Request{}, errors.New(
			"upstream presentation does not support the client HTTP protocol",
		)
	}
	requestedTransport := wireVariant.TransportFingerprintPlan().Requested()
	expectedTransport := wireprofile.HTTPTransportHTTP1
	if options.ClientProtocol == wireprofile.ApplicationProtocolHTTP2 {
		expectedTransport = wireprofile.HTTPTransportHTTP2
	}
	if options.Target.origin.Transport() == originidentity.ProviderTransportLoopbackCleartext &&
		expectedTransport == wireprofile.HTTPTransportHTTP2 {
		return Request{}, errors.New(
			"loopback cleartext provider does not support HTTP/2 presentation",
		)
	}
	if requestedTransport.Ref().String() == "" ||
		requestedTransport.Revision() == 0 ||
		requestedTransport.HTTPTransport() != expectedTransport ||
		len(requestedTransport.ALPN()) == 0 {
		return Request{}, errors.New(
			"provider transport fingerprint plan is incomplete",
		)
	}
	return Request{
		connectionID:    options.ConnectionID,
		exchangeID:      options.ExchangeID,
		parentAttemptID: options.ParentAttemptID,
		egressAttemptID: options.EgressAttemptID,
		requestID:       options.RequestID,
		targetRef:       options.TargetRef,
		provenance:      options.Provenance,
		target:          options.Target,
		probeTarget:     probeTarget,
		action:          options.Action,
		method:          options.Method,
		relativePath:    relativePath,
		headers:         options.Headers.Clone(),
		body:            bytes.Clone(options.Body),
		rawQuery:        options.RawQuery,
		credentialMode:  options.CredentialMode,
		accountRef:      options.AccountRef,
		secretReference: reference,
		authDriverRef:   options.AuthDriverRef,
		wireProfile:     options.WireProfile,
		wireVariant:     wireVariant,
		clientProtocol:  options.ClientProtocol,
		clientUserAgent: options.ClientUserAgent,
		clientHello:     options.ClientHello,
	}, nil
}

func (request Request) RequestID() string {
	return request.requestID
}

func (request Request) TargetRef() string {
	return request.targetRef
}

func (request Request) Target() Target {
	return request.target
}

func (request Request) ProbeTarget() offlinehold.ProbeTarget {
	return request.probeTarget
}

func (request Request) Method() string {
	return request.method
}

func (request Request) RelativePath() string {
	return request.relativePath
}

func (request Request) RawQuery() string { return request.rawQuery }

func (request Request) Headers() http.Header {
	return request.headers.Clone()
}

func (request Request) Body() []byte {
	return bytes.Clone(request.body)
}

func (request Request) Provenance() RequestProvenance { return request.provenance }

func (request Request) CredentialMode() providerauth.CredentialMode {
	return request.credentialMode
}

func (request Request) AccountRef() (providerauth.AccountRef, bool) {
	return request.accountRef, request.credentialMode == providerauth.CredentialManaged
}

func (request Request) AuthDriverRef() providerauth.DriverRef {
	return request.authDriverRef
}

func (request Request) WirePresentationEvidence() WirePresentationEvidence {
	return NewWirePresentationEvidence(
		request.wireProfile,
		request.clientProtocol,
	)
}

func (request Request) buildURL() *url.URL {
	path := pathpkg.Join(request.target.origin.BasePath(), request.relativePath)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return &url.URL{
		Scheme:   request.target.origin.Scheme(),
		Host:     request.target.origin.HTTPAuthority(),
		Path:     path,
		RawQuery: request.rawQuery,
	}
}

func validateRawQuery(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 4096 || !utf8.ValidString(value) ||
		strings.ContainsAny(value, "#?") {
		return errors.New("provider request query is invalid")
	}
	if _, err := url.ParseQuery(value); err != nil {
		return errors.New("provider request query is invalid")
	}
	return nil
}

func canonicalRelativePath(value string) (string, error) {
	if value == "" ||
		!utf8.ValidString(value) ||
		strings.HasPrefix(value, "/") ||
		strings.ContainsAny(value, "%?#\\") {
		return "", errors.New("provider request path must be a relative URL path")
	}
	cleaned := pathpkg.Clean(value)
	if cleaned == "." ||
		cleaned != value ||
		strings.HasPrefix(cleaned, "../") ||
		cleaned == ".." {
		return "", errors.New("provider request path is not canonical")
	}
	return cleaned, nil
}

func validateOpaqueIdentity(label, value string) error {
	if value == "" ||
		len(value) > maxRequestIdentityBytes ||
		!utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is invalid", label)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s contains a control character", label)
		}
	}
	return nil
}

func validPresentationUserAgent(value string) bool {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}

func (request Request) ConnectionID() string    { return request.connectionID }
func (request Request) ExchangeID() string      { return request.exchangeID }
func (request Request) ParentAttemptID() string { return request.parentAttemptID }

// EgressAttemptID is the identity of the outbound itself, minted separately
// from the attempt that owns it.
func (request Request) EgressAttemptID() string { return request.egressAttemptID }
