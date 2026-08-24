package captureassignment

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/certidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/originidentity"
)

var (
	ErrLeafIssuanceInvalid      = errors.New("leaf issuance request is invalid")
	ErrLeafIssuanceUnauthorized = errors.New("leaf issuance is not authorized")
	ErrLeafAdmissionConsumed    = errors.New("leaf issuance admission was already consumed")
)

// LeafIssuanceRequest is the immutable identity authorized by one current
// Capture connection. It has no exported constructor and cannot authorize a
// signature without the one-use admission that owns it.
type LeafIssuanceRequest struct {
	rootRevision        certidentity.RootRevision
	capture             captureidentity.Reference
	assignmentRevision  Revision
	environmentID       environment.EnvironmentID
	environmentRevision environment.Revision
	endpointID          environment.ClientEndpointID
	endpointRevision    environment.Revision
	clientOrigin        originidentity.ClientOrigin
	san                 certidentity.SubjectAlternativeName
	algorithm           certidentity.LeafKeyAlgorithm
}

func (request LeafIssuanceRequest) RootRevision() certidentity.RootRevision {
	return request.rootRevision
}
func (request LeafIssuanceRequest) Capture() captureidentity.Reference { return request.capture }
func (request LeafIssuanceRequest) AssignmentRevision() Revision {
	return request.assignmentRevision
}
func (request LeafIssuanceRequest) EnvironmentID() environment.EnvironmentID {
	return request.environmentID
}
func (request LeafIssuanceRequest) EnvironmentRevision() environment.Revision {
	return request.environmentRevision
}
func (request LeafIssuanceRequest) ClientEndpointID() environment.ClientEndpointID {
	return request.endpointID
}
func (request LeafIssuanceRequest) ClientEndpointRevision() environment.Revision {
	return request.endpointRevision
}
func (request LeafIssuanceRequest) ClientOrigin() originidentity.ClientOrigin {
	return request.clientOrigin
}
func (request LeafIssuanceRequest) SAN() certidentity.SubjectAlternativeName { return request.san }
func (request LeafIssuanceRequest) Algorithm() certidentity.LeafKeyAlgorithm {
	return request.algorithm
}

func (request LeafIssuanceRequest) validate() error {
	if !request.rootRevision.Valid() || request.capture.Validate() != nil ||
		request.assignmentRevision == 0 || request.assignmentRevision > MaxRevision ||
		request.environmentID == "" || request.environmentRevision == 0 ||
		request.endpointID == "" || request.endpointRevision == 0 ||
		request.clientOrigin.Validate() != nil || !request.san.Valid() ||
		request.san.Kind() != certidentity.SANKindDNS ||
		request.san.Value() != request.clientOrigin.Host() || !request.algorithm.Valid() {
		return ErrLeafIssuanceInvalid
	}
	return nil
}

type leafAdmissionState struct {
	manager      *Manager
	captureState *captureState
	connectionID string
	connection   *connectionRecord
	request      LeafIssuanceRequest
	consumed     atomic.Bool
}

// LeafIssuanceAdmission is projection-owned and one-use. Copies share the
// same atomic consumption state, so copying cannot replay the grant.
type LeafIssuanceAdmission struct{ state *leafAdmissionState }

func (admission LeafIssuanceAdmission) ClaimForIssuance() (LeafIssuanceRequest, error) {
	if admission.state == nil || admission.state.manager == nil ||
		admission.state.request.validate() != nil {
		return LeafIssuanceRequest{}, ErrLeafIssuanceUnauthorized
	}
	if !admission.state.consumed.CompareAndSwap(false, true) {
		return LeafIssuanceRequest{}, ErrLeafAdmissionConsumed
	}
	return admission.state.request, nil
}

// Cacheable is rechecked after generation while the CA serializes cache
// publication. A transition that blocks or removes the source connection
// therefore prevents an obsolete worker from reviving a revoked cache entry.
func (admission LeafIssuanceAdmission) Cacheable() bool {
	state := admission.state
	return state != nil && state.manager != nil && state.manager.leafRequestCurrent(state)
}

func (lease *ConnectionLease) AdmitLeaf(
	ctx context.Context,
	rootRevision certidentity.RootRevision,
	san certidentity.SubjectAlternativeName,
	algorithm certidentity.LeafKeyAlgorithm,
) (LeafIssuanceAdmission, error) {
	if lease == nil || lease.manager == nil {
		return LeafIssuanceAdmission{}, ErrLeafIssuanceUnauthorized
	}
	return lease.manager.admitLeaf(ctx, lease.capture, lease.id, rootRevision, san, algorithm)
}

func (manager *Manager) admitLeaf(
	ctx context.Context,
	capture captureidentity.Reference,
	connectionID string,
	rootRevision certidentity.RootRevision,
	san certidentity.SubjectAlternativeName,
	algorithm certidentity.LeafKeyAlgorithm,
) (LeafIssuanceAdmission, error) {
	finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return LeafIssuanceAdmission{}, err
	}
	defer finish()
	if !rootRevision.Valid() || !san.Valid() || !algorithm.Valid() {
		return LeafIssuanceAdmission{}, ErrLeafIssuanceInvalid
	}
	state := manager.state(capture)
	state.mu.Lock()
	defer state.mu.Unlock()
	connection := state.connections[connectionID]
	if connection == nil || state.shutdown || state.poisoned ||
		connection.binding.Mode != environment.ConnectionModeSemantic {
		return LeafIssuanceAdmission{}, ErrLeafIssuanceUnauthorized
	}
	assignment := connection.assignment
	snapshot := connection.snapshot
	if assignment.Validate() != nil || assignment.Capture != capture ||
		snapshot.ID() != assignment.EnvironmentID ||
		snapshot.Revision() != assignment.LaunchAuthority.InitialEnvironmentRevision() ||
		snapshot.Digest() != assignment.LaunchAuthority.InitialEnvironmentDigest() {
		return LeafIssuanceAdmission{}, ErrLeafIssuanceUnauthorized
	}
	if environment.ValidateConnectionBinding(snapshot, connection.binding) != nil {
		return LeafIssuanceAdmission{}, ErrLeafIssuanceUnauthorized
	}
	endpoint, exists := snapshot.LookupCompiledClientOrigin(connection.binding.ClientOrigin)
	if !exists || endpoint.ID() != connection.binding.ClientEndpointID ||
		san.Kind() != certidentity.SANKindDNS || san.Value() != connection.binding.ClientOrigin.Host() {
		return LeafIssuanceAdmission{}, ErrLeafIssuanceUnauthorized
	}
	request := LeafIssuanceRequest{
		rootRevision: rootRevision, capture: capture,
		assignmentRevision: assignment.Revision,
		environmentID:      assignment.EnvironmentID, environmentRevision: snapshot.Revision(),
		endpointID: endpoint.ID(), endpointRevision: endpoint.Revision(),
		clientOrigin: connection.binding.ClientOrigin, san: san, algorithm: algorithm,
	}
	if request.validate() != nil {
		return LeafIssuanceAdmission{}, ErrLeafIssuanceInvalid
	}
	return LeafIssuanceAdmission{state: &leafAdmissionState{
		manager: manager, captureState: state, connectionID: connectionID,
		connection: connection, request: request,
	}}, nil
}

func (manager *Manager) leafRequestCurrent(admission *leafAdmissionState) bool {
	if manager == nil || admission == nil || admission.captureState == nil || admission.connection == nil {
		return false
	}
	state := admission.captureState
	state.mu.Lock()
	defer state.mu.Unlock()
	current := state.connections[admission.connectionID]
	return !state.shutdown && !state.poisoned && current == admission.connection &&
		current.binding.Mode == environment.ConnectionModeSemantic &&
		current.binding.ClientOrigin == admission.request.clientOrigin &&
		current.binding.ClientEndpointID == admission.request.endpointID
}
