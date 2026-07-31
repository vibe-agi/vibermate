package access

import (
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/vibe-agi/vibermate/internal/certidentity"
)

var (
	ErrLeafIssuanceInvalid      = errors.New("leaf issuance request is invalid")
	ErrLeafIssuanceUnauthorized = errors.New("leaf issuance is not authorized")
	ErrLeafSANUnsupported       = errors.New("leaf SAN kind is unsupported")
	ErrLeafAdmissionConsumed    = errors.New("leaf issuance admission was already consumed")
)

// LeafIssuanceIntent is connection evidence offered to the sole active Access
// projection. It is not signing authority.
type LeafIssuanceIntent struct {
	rootRevision certidentity.RootRevision
	binding      IngressBinding
	san          certidentity.SubjectAlternativeName
	algorithm    certidentity.LeafKeyAlgorithm
}

func NewLeafIssuanceIntent(
	rootRevision certidentity.RootRevision,
	binding IngressBinding,
	san certidentity.SubjectAlternativeName,
	algorithm certidentity.LeafKeyAlgorithm,
) (LeafIssuanceIntent, error) {
	intent := LeafIssuanceIntent{
		rootRevision: rootRevision,
		binding:      binding,
		san:          san,
		algorithm:    algorithm,
	}
	if err := intent.validate(); err != nil {
		return LeafIssuanceIntent{}, err
	}
	return intent, nil
}

func (intent LeafIssuanceIntent) validate() error {
	if !intent.rootRevision.Valid() || intent.binding.validate() != nil ||
		!intent.san.Valid() || !intent.algorithm.Valid() {
		return ErrLeafIssuanceInvalid
	}
	if intent.san.Value() != intent.binding.clientOrigin.TLSServerName() {
		return fmt.Errorf(
			"%w: SAN does not match ClientOrigin TLS identity",
			ErrLeafIssuanceInvalid,
		)
	}
	return nil
}

// LeafIssuanceRequest freezes the complete authorization identity selected by
// the active projection. It has no exported constructor and cannot authorize
// signing without its one-use admission.
type LeafIssuanceRequest struct {
	rootRevision     certidentity.RootRevision
	accessID         AccessID
	endpointID       AgentEndpointID
	endpointRevision Revision
	clientOrigin     ClientOrigin
	san              certidentity.SubjectAlternativeName
	algorithm        certidentity.LeafKeyAlgorithm
}

func (request LeafIssuanceRequest) RootRevision() certidentity.RootRevision {
	return request.rootRevision
}

func (request LeafIssuanceRequest) AccessID() AccessID {
	return request.accessID
}

func (request LeafIssuanceRequest) AgentEndpointID() AgentEndpointID {
	return request.endpointID
}

func (request LeafIssuanceRequest) AgentEndpointRevision() Revision {
	return request.endpointRevision
}

func (request LeafIssuanceRequest) ClientOrigin() ClientOrigin {
	return request.clientOrigin
}

func (request LeafIssuanceRequest) SAN() certidentity.SubjectAlternativeName {
	return request.san
}

func (request LeafIssuanceRequest) Algorithm() certidentity.LeafKeyAlgorithm {
	return request.algorithm
}

func (request LeafIssuanceRequest) validate() error {
	if !request.rootRevision.Valid() || request.accessID.validate() != nil ||
		request.endpointID.validate() != nil || request.endpointRevision == 0 ||
		request.clientOrigin.validate() != nil || !request.san.Valid() ||
		!request.algorithm.Valid() ||
		request.san.Value() != request.clientOrigin.TLSServerName() {
		return ErrLeafIssuanceInvalid
	}
	return nil
}

type leafAdmissionState struct {
	projection *AtomicSnapshotProjection
	request    LeafIssuanceRequest
	consumed   atomic.Bool
}

// LeafIssuanceAdmission is a one-use projection-owned grant. Copies share the
// same consumed state and therefore cannot replay one admission.
type LeafIssuanceAdmission struct {
	state *leafAdmissionState
}

// ClaimForIssuance consumes the grant. Calling it outside the leaf authority
// can only destroy the grant; a bare returned request is not accepted for
// signing.
func (admission LeafIssuanceAdmission) ClaimForIssuance() (
	LeafIssuanceRequest,
	error,
) {
	if admission.state == nil || admission.state.projection == nil ||
		admission.state.request.validate() != nil {
		return LeafIssuanceRequest{}, ErrLeafIssuanceUnauthorized
	}
	if !admission.state.consumed.CompareAndSwap(false, true) {
		return LeafIssuanceRequest{}, ErrLeafAdmissionConsumed
	}
	return admission.state.request, nil
}

// Cacheable reports whether the exact endpoint authorization is still active.
// It is checked while the leaf authority serializes cache publication against
// invalidation, so a pre-revocation worker cannot resurrect an old entry.
func (admission LeafIssuanceAdmission) Cacheable() bool {
	return admission.state != nil && admission.state.projection != nil &&
		admission.state.projection.leafRequestCurrent(admission.state.request)
}

// LeafIssuanceAdmitter is the only production boundary that can turn current
// connection evidence into a signing admission.
type LeafIssuanceAdmitter interface {
	AdmitLeaf(LeafIssuanceIntent) (LeafIssuanceAdmission, error)
}

// LeafCacheInvalidation identifies obsolete derived entries. It is not an
// endpoint configuration or an authorization decision.
type LeafCacheInvalidation struct {
	accessID         AccessID
	endpointID       AgentEndpointID
	endpointRevision Revision
	clientOrigin     ClientOrigin
}

func (invalidation LeafCacheInvalidation) AccessID() AccessID {
	return invalidation.accessID
}

func (invalidation LeafCacheInvalidation) AgentEndpointID() AgentEndpointID {
	return invalidation.endpointID
}

func (invalidation LeafCacheInvalidation) AgentEndpointRevision() Revision {
	return invalidation.endpointRevision
}

func (invalidation LeafCacheInvalidation) ClientOrigin() ClientOrigin {
	return invalidation.clientOrigin
}

func (invalidation LeafCacheInvalidation) validate() error {
	if invalidation.accessID.validate() != nil ||
		invalidation.endpointID.validate() != nil ||
		invalidation.endpointRevision == 0 ||
		invalidation.clientOrigin.validate() != nil {
		return ErrLeafIssuanceInvalid
	}
	return nil
}

// LeafCacheInvalidator synchronously removes obsolete derived certificates
// after active projection publication. It never decides signing authority.
type LeafCacheInvalidator interface {
	InvalidateLeafCache(LeafCacheInvalidation)
}

func leafInvalidationFromSnapshot(
	snapshot AccessPlanSnapshot,
) (LeafCacheInvalidation, bool) {
	if snapshot.validate() != nil ||
		snapshot.Binding().Status != AccessStatusEnabled {
		return LeafCacheInvalidation{}, false
	}
	endpoint := snapshot.AgentEndpoint()
	return LeafCacheInvalidation{
		accessID:         snapshot.AccessID(),
		endpointID:       endpoint.ID,
		endpointRevision: endpoint.Revision,
		clientOrigin:     endpoint.ClientOrigin,
	}, true
}

func sameLeafAuthorization(
	left, right AccessPlanSnapshot,
) bool {
	if left.validate() != nil || right.validate() != nil ||
		left.Binding().Status != AccessStatusEnabled ||
		right.Binding().Status != AccessStatusEnabled {
		return false
	}
	leftEndpoint := left.AgentEndpoint()
	rightEndpoint := right.AgentEndpoint()
	return left.AccessID() == right.AccessID() &&
		leftEndpoint.ID == rightEndpoint.ID &&
		leftEndpoint.Revision == rightEndpoint.Revision &&
		leftEndpoint.ClientOrigin == rightEndpoint.ClientOrigin &&
		leftEndpoint.ClientDialect == rightEndpoint.ClientDialect
}
