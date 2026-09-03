package transportprofile

import (
	"slices"

	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

type FallbackReason string

const (
	FallbackNone                         FallbackReason = ""
	FallbackObservationUnavailable       FallbackReason = "observation_unavailable"
	FallbackClientHelloUnsupported       FallbackReason = "client_hello_unsupported"
	FallbackApplicationProtocolMissing   FallbackReason = "application_protocol_unavailable"
	FallbackObservedTLSHandshakeRejected FallbackReason = "observed_tls_handshake_rejected"
	FallbackCapturedTLSHandshakeRejected FallbackReason = "captured_tls_handshake_rejected"
)

type ProfileEvidence struct {
	Ref      string
	Revision wireprofile.Revision
	Source   wireprofile.TransportFingerprintSource
}

// Evidence records selection and negotiated protocol facts without retaining
// raw ClientHello bytes, credentials, request bodies, or provider text.
type Evidence struct {
	requested                ProfileEvidence
	effective                ProfileEvidence
	fallbackChain            []ProfileEvidence
	fallbackReason           FallbackReason
	clientOfferedALPN        []string
	downstreamNegotiatedALPN string
	upstreamOfferedALPN      []string
	upstreamNegotiatedALPN   string
	httpTransport            wireprofile.HTTPTransportKind
}

func (evidence Evidence) Requested() ProfileEvidence {
	return evidence.requested
}

func (evidence Evidence) Effective() ProfileEvidence {
	return evidence.effective
}

func (evidence Evidence) FallbackChain() []ProfileEvidence {
	return slices.Clone(evidence.fallbackChain)
}

func (evidence Evidence) FallbackReason() FallbackReason {
	return evidence.fallbackReason
}

func (evidence Evidence) UsedFallback() bool {
	return evidence.fallbackReason != FallbackNone
}

func (evidence Evidence) ClientOfferedALPN() []string {
	return slices.Clone(evidence.clientOfferedALPN)
}

func (evidence Evidence) DownstreamNegotiatedALPN() string {
	return evidence.downstreamNegotiatedALPN
}

func (evidence Evidence) UpstreamOfferedALPN() []string {
	return slices.Clone(evidence.upstreamOfferedALPN)
}

func (evidence Evidence) UpstreamNegotiatedALPN() string {
	return evidence.upstreamNegotiatedALPN
}

func (evidence Evidence) HTTPTransport() wireprofile.HTTPTransportKind {
	return evidence.httpTransport
}

func (evidence Evidence) Clone() Evidence {
	cloned := evidence
	cloned.fallbackChain = slices.Clone(evidence.fallbackChain)
	cloned.clientOfferedALPN = slices.Clone(evidence.clientOfferedALPN)
	cloned.upstreamOfferedALPN = slices.Clone(evidence.upstreamOfferedALPN)
	return cloned
}

func newEvidence(
	requested wireprofile.TransportFingerprintTemplate,
	observation Observation,
) Evidence {
	return Evidence{
		requested:                profileEvidence(requested),
		clientOfferedALPN:        observation.OfferedALPN(),
		downstreamNegotiatedALPN: observation.DownstreamNegotiatedALPN(),
		httpTransport:            requested.HTTPTransport(),
	}
}

func profileEvidence(
	template wireprofile.TransportFingerprintTemplate,
) ProfileEvidence {
	return ProfileEvidence{
		Ref:      template.Ref().String(),
		Revision: template.Revision(),
		Source:   template.Source(),
	}
}
