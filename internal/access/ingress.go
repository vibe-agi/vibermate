package access

import (
	"errors"
	"fmt"
)

var (
	ErrAgentEndpointNotConfigured = errors.New("AgentEndpoint is not configured")
	ErrAgentEndpointConflict      = errors.New("ClientOrigin belongs to another Access")
)

// IngressBinding is the immutable identity evidence frozen by an ingress
// connection before it issues a certificate or admits an Exchange.
type IngressBinding struct {
	accessID         AccessID
	accessName       string
	endpointID       AgentEndpointID
	endpointRevision Revision
	clientOrigin     ClientOrigin
	dialect          Dialect
	revision         Revision
	planHash         PlanHash
}

func (binding IngressBinding) AccessID() AccessID {
	return binding.accessID
}

// AccessName is frozen display evidence from the same Access revision that
// authorized this connection. It is never used as an identity or lookup key.
func (binding IngressBinding) AccessName() string {
	return binding.accessName
}

func (binding IngressBinding) AgentEndpointID() AgentEndpointID {
	return binding.endpointID
}

func (binding IngressBinding) AgentEndpointRevision() Revision {
	return binding.endpointRevision
}

func (binding IngressBinding) ClientOrigin() ClientOrigin {
	return binding.clientOrigin
}

func (binding IngressBinding) ClientDialect() Dialect {
	return binding.dialect
}

func (binding IngressBinding) AccessRevision() Revision {
	return binding.revision
}

func (binding IngressBinding) PlanHash() PlanHash {
	return binding.planHash
}

func (binding IngressBinding) validate() error {
	if err := binding.accessID.validate(); err != nil {
		return err
	}
	if err := validateBoundedText(
		"Access name",
		binding.accessName,
		MaxAccessNameBytes,
		false,
	); err != nil {
		return err
	}
	if err := binding.endpointID.validate(); err != nil {
		return err
	}
	if err := binding.clientOrigin.validate(); err != nil {
		return err
	}
	if binding.dialect == "" || binding.endpointRevision == 0 ||
		binding.revision == 0 || binding.planHash.IsZero() {
		return fmt.Errorf("%w: ingress binding is incomplete", ErrInvalidAccessPlan)
	}
	return nil
}

func (binding IngressBinding) Validate() error {
	return binding.validate()
}

// ValidateSnapshot proves that a connection-time AgentEndpoint identity still
// belongs to the current Access plan. Provider-only plan changes remain valid
// for an existing connection; endpoint ID, origin, or dialect changes do not.
func (binding IngressBinding) ValidateSnapshot(
	snapshot AccessPlanSnapshot,
) error {
	if err := binding.validate(); err != nil {
		return err
	}
	if err := snapshot.validate(); err != nil {
		return err
	}
	endpoint := snapshot.AgentEndpoint()
	if binding.accessID != snapshot.AccessID() ||
		snapshot.Binding().Status != AccessStatusEnabled ||
		binding.endpointID != endpoint.ID ||
		binding.endpointRevision != endpoint.Revision ||
		binding.clientOrigin != endpoint.ClientOrigin ||
		binding.dialect != endpoint.ClientDialect {
		return fmt.Errorf(
			"%w: connection AgentEndpoint no longer matches the active Access plan",
			ErrAgentEndpointNotConfigured,
		)
	}
	return nil
}

// ValidateCurrent proves that a connection-time binding still names the same
// enabled AgentEndpoint identity returned by the active projection. Revision
// and PlanHash may advance when only provider-side plan data changes.
func (binding IngressBinding) ValidateCurrent(
	current IngressBinding,
) error {
	if err := binding.validate(); err != nil {
		return err
	}
	if err := current.validate(); err != nil {
		return err
	}
	if binding.accessID != current.accessID ||
		binding.endpointID != current.endpointID ||
		binding.endpointRevision != current.endpointRevision ||
		binding.clientOrigin != current.clientOrigin ||
		binding.dialect != current.dialect {
		return fmt.Errorf(
			"%w: connection AgentEndpoint no longer matches the active ingress binding",
			ErrAgentEndpointNotConfigured,
		)
	}
	return nil
}

// IngressBinding returns immutable connection identity evidence for this
// snapshot. It does not grant authority to bypass a later current-plan check.
func (snapshot AccessPlanSnapshot) IngressBinding() IngressBinding {
	return ingressBindingFromSnapshot(snapshot)
}

// IngressResolver resolves only exact registered ClientOrigin identities. It
// shares the same atomic projection as SnapshotResolver and cannot expose a
// second configuration authority.
type IngressResolver interface {
	ResolveClientOrigin(ClientOrigin) (IngressBinding, error)
}

// IngressCatalogReader returns enabled, healthy endpoint authorities for
// child-only NO_PROXY hardening. It does not expose an Access mutation path.
type IngressCatalogReader interface {
	ActiveClientAuthorities() ([]string, error)
}

func ingressBindingFromSnapshot(snapshot AccessPlanSnapshot) IngressBinding {
	endpoint := snapshot.AgentEndpoint()
	return IngressBinding{
		accessID:         snapshot.AccessID(),
		accessName:       snapshot.Binding().Name,
		endpointID:       endpoint.ID,
		endpointRevision: endpoint.Revision,
		clientOrigin:     endpoint.ClientOrigin,
		dialect:          endpoint.ClientDialect,
		revision:         snapshot.Revision(),
		planHash:         snapshot.PlanHash(),
	}
}
