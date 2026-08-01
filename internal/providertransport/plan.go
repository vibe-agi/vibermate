package providertransport

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	pathpkg "path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
)

const (
	MaxProviderRequestBytes = 16 << 20
	maxRequestIdentityBytes = 1024
)

type Target struct {
	origin        string
	scheme        string
	httpAuthority string
	networkHost   string
	tlsServerName string
	basePath      string
	port          uint16
	transportKind access.ProviderTransportKind
}

func NewTarget(compiled access.CompiledProviderTarget) (Target, error) {
	resource := compiled.Target()
	origin := resource.Origin
	parsed, err := url.Parse(origin.String())
	if err != nil {
		return Target{}, fmt.Errorf("parse compiled provider target: %w", err)
	}
	if parsed.Scheme != origin.Scheme() ||
		parsed.Host == "" ||
		parsed.Hostname() != origin.NetworkHost() ||
		origin.HTTPAuthority() != compiled.HTTPAuthority() ||
		origin.NetworkHost() != compiled.NetworkHost() ||
		origin.TLSServerName() != compiled.TLSServerName() ||
		origin.BasePath() != compiled.BasePath() ||
		origin.Port() != compiled.Port() ||
		origin.TransportKind() != compiled.TransportKind() {
		return Target{}, errors.New("compiled provider target network identities disagree")
	}
	return Target{
		origin:        origin.String(),
		scheme:        parsed.Scheme,
		httpAuthority: compiled.HTTPAuthority(),
		networkHost:   compiled.NetworkHost(),
		tlsServerName: compiled.TLSServerName(),
		basePath:      compiled.BasePath(),
		port:          compiled.Port(),
		transportKind: compiled.TransportKind(),
	}, nil
}

func (target Target) Origin() string {
	return target.origin
}

func (target Target) HTTPAuthority() string {
	return target.httpAuthority
}

func (target Target) NetworkHost() string {
	return target.networkHost
}

func (target Target) TLSServerName() string {
	return target.tlsServerName
}

func (target Target) TransportKind() access.ProviderTransportKind {
	return target.transportKind
}

func (target Target) BasePath() string {
	return target.basePath
}

func (target Target) endpointAuthority() (string, error) {
	if err := target.validate(); err != nil {
		return "", errors.New("provider target endpoint authority is invalid")
	}
	return net.JoinHostPort(
		target.networkHost,
		fmt.Sprintf("%d", target.port),
	), nil
}

func (target Target) validate() error {
	if target.origin == "" {
		return errors.New("provider target is empty")
	}
	origin, err := access.NewProviderOrigin(target.origin)
	if err != nil ||
		origin.Scheme() != target.scheme ||
		origin.HTTPAuthority() != target.httpAuthority ||
		origin.NetworkHost() != target.networkHost ||
		origin.TLSServerName() != target.tlsServerName ||
		origin.BasePath() != target.basePath ||
		origin.Port() != target.port ||
		origin.TransportKind() != target.transportKind {
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
	if request.URL.Scheme != target.scheme ||
		request.URL.Host != target.httpAuthority ||
		request.Host != target.httpAuthority {
		return errors.New("provider request identity is not frozen")
	}
	return nil
}

// NewProbeTarget binds a logical provider target to the exact immutable Access
// plan and network identities used by the corresponding provider request.
func NewProbeTarget(
	targetRef string,
	revision access.Revision,
	planHash access.PlanHash,
	target Target,
) (offlinehold.ProbeTarget, error) {
	if err := validateOpaqueIdentity("provider target reference", targetRef); err != nil {
		return offlinehold.ProbeTarget{}, err
	}
	if revision == 0 || planHash.IsZero() {
		return offlinehold.ProbeTarget{}, errors.New(
			"provider probe target has no Access plan identity",
		)
	}
	if err := target.validate(); err != nil {
		return offlinehold.ProbeTarget{}, err
	}
	probeTransport, err := target.probeTransportKind()
	if err != nil {
		return offlinehold.ProbeTarget{}, err
	}
	frozen := offlinehold.ProbeTarget{
		Kind:           offlinehold.EgressProvider,
		Transport:      probeTransport,
		TargetRef:      targetRef,
		NetworkOrigin:  target.origin,
		HTTPAuthority:  target.httpAuthority,
		TLSServerName:  target.tlsServerName,
		AccessRevision: uint64(revision),
		PlanHash:       planHash.String(),
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
	switch target.transportKind {
	case access.ProviderTransportStrictTLS:
		return offlinehold.ProbeTransportStrictTLS, nil
	case access.ProviderTransportLoopbackCleartext:
		return offlinehold.ProbeTransportLoopbackCleartext, nil
	default:
		return "", errors.New("provider target transport kind is unsupported")
	}
}

type RequestOptions struct {
	RequestID      string
	TargetRef      string
	Target         Target
	AccessRevision access.Revision
	PlanHash       access.PlanHash
	Action         *offlinehold.ActionLease
	Method         string
	RelativePath   string
	Headers        http.Header
	Body           []byte
	SecretRef      access.SecretRef
	AuthDriverRef  access.AuthDriverRef
	TransportPlan  access.CompiledTransportFingerprintPlan
	ClientHello    transportprofile.Observation
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
	target          Target
	probeTarget     offlinehold.ProbeTarget
	action          *offlinehold.ActionLease
	method          string
	relativePath    string
	headers         http.Header
	body            []byte
	secretReference secretstore.Reference
	authDriverRef   access.AuthDriverRef
	transportPlan   access.CompiledTransportFingerprintPlan
	clientHello     transportprofile.Observation
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
		options.AccessRevision,
		options.PlanHash,
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
	reference, err := secretstore.ParseReference(options.SecretRef.String())
	if err != nil {
		return Request{}, err
	}
	if options.AuthDriverRef.String() == "" {
		return Request{}, errors.New("provider AuthDriver reference is empty")
	}
	requestedTransport := options.TransportPlan.Requested()
	if requestedTransport.Ref().String() == "" ||
		requestedTransport.Revision() == 0 ||
		requestedTransport.HTTPTransport() != access.HTTPTransportHTTP1 ||
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
		target:          options.Target,
		probeTarget:     probeTarget,
		action:          options.Action,
		method:          options.Method,
		relativePath:    relativePath,
		headers:         options.Headers.Clone(),
		body:            bytes.Clone(options.Body),
		secretReference: reference,
		authDriverRef:   options.AuthDriverRef,
		transportPlan:   options.TransportPlan,
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

func (request Request) Headers() http.Header {
	return request.headers.Clone()
}

func (request Request) Body() []byte {
	return bytes.Clone(request.body)
}

func (request Request) AuthDriverRef() access.AuthDriverRef {
	return request.authDriverRef
}

func (request Request) buildURL() *url.URL {
	path := pathpkg.Join(request.target.basePath, request.relativePath)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return &url.URL{
		Scheme: request.target.scheme,
		Host:   request.target.httpAuthority,
		Path:   path,
	}
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

func (request Request) ConnectionID() string    { return request.connectionID }
func (request Request) ExchangeID() string      { return request.exchangeID }
func (request Request) ParentAttemptID() string { return request.parentAttemptID }

// EgressAttemptID is the identity of the outbound itself, minted separately
// from the attempt that owns it.
func (request Request) EgressAttemptID() string { return request.egressAttemptID }
