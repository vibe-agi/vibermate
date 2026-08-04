package access

import "errors"

var ErrDownstreamProtocolUnavailable = errors.New(
	"Access plan has no common downstream application protocol",
)

// DownstreamProtocolResolver derives the application protocols that can be
// kept unchanged across every profile selectable on one ingress connection.
// It reads the active Access projection; it is not a second routing authority.
type DownstreamProtocolResolver interface {
	ResolveDownstreamProtocols(IngressBinding) ([]ApplicationProtocol, error)
}

// ResolveDownstreamProtocols returns the server-preference-ordered protocol
// intersection for the current Access plan. A TLS connection can carry more
// than one request and a workspace route can change between requests, so the
// handshake may only select a protocol supported by every selectable profile.
func (p *AtomicSnapshotProjection) ResolveDownstreamProtocols(
	binding IngressBinding,
) ([]ApplicationProtocol, error) {
	if p == nil || binding.validate() != nil {
		return nil, ErrDownstreamProtocolUnavailable
	}
	state := p.state.Load()
	current, exists := state.byOrigin[binding.clientOrigin.EndpointAuthority()]
	if !exists || current.ValidateCurrent(binding) != nil {
		return nil, ErrAgentEndpointNotConfigured
	}
	if _, unavailable := state.unavailable[current.AccessID()]; unavailable {
		return nil, ErrProjectionUnavailable
	}
	snapshot, exists := state.byAccess[current.AccessID()]
	if !exists || snapshot.Binding().Status != AccessStatusEnabled ||
		binding.ValidateSnapshot(snapshot) != nil {
		return nil, ErrAgentEndpointNotConfigured
	}
	return commonDownstreamProtocols(snapshot)
}

func commonDownstreamProtocols(
	snapshot AccessPlanSnapshot,
) ([]ApplicationProtocol, error) {
	if len(snapshot.candidates) == 0 {
		return nil, ErrDownstreamProtocolUnavailable
	}
	http1 := true
	http2 := true
	for _, candidate := range snapshot.candidates {
		http1 = http1 && candidateSupportsDownstreamProtocol(
			candidate,
			ApplicationProtocolHTTP1,
		)
		http2 = http2 && candidateSupportsDownstreamProtocol(
			candidate,
			ApplicationProtocolHTTP2,
		)
	}
	protocols := make([]ApplicationProtocol, 0, 2)
	if http2 {
		protocols = append(protocols, ApplicationProtocolHTTP2)
	}
	if http1 {
		protocols = append(protocols, ApplicationProtocolHTTP1)
	}
	if len(protocols) == 0 {
		return nil, ErrDownstreamProtocolUnavailable
	}
	return protocols, nil
}

func candidateSupportsDownstreamProtocol(
	candidate CompiledCandidate,
	protocol ApplicationProtocol,
) bool {
	variant, exists := candidate.wireProfile.Variant(protocol)
	if !exists {
		return false
	}
	requested := variant.TransportFingerprintPlan().Requested()
	switch protocol {
	case ApplicationProtocolHTTP1:
		return requested.HTTPTransport() == HTTPTransportHTTP1
	case ApplicationProtocolHTTP2:
		return requested.HTTPTransport() == HTTPTransportHTTP2 &&
			candidate.target.TransportKind() == ProviderTransportStrictTLS
	default:
		return false
	}
}
