// Package accessapply translates the control-plane apply DTO into the one
// authoritative Access aggregate. It is a transport boundary; it does not
// persist or publish a second configuration model.
package accessapply

import (
	"errors"
	"fmt"

	"github.com/vibe-agi/vibermate/internal/access"
)

type Input struct {
	ExpectedRevision uint64                `json:"expectedRevision"`
	Access           AccessInput           `json:"access"`
	AgentEndpoint    AgentEndpointInput    `json:"agentEndpoint"`
	Profiles         []ProfileInput        `json:"profiles"`
	ProviderTargets  []ProviderTargetInput `json:"providerTargets"`
	AccountBindings  []AccountBindingInput `json:"accountBindings"`
	RouteSets        []RouteSetInput       `json:"routeSets"`
	EgressPolicy     EgressPolicyInput     `json:"egressPolicy"`
	PluginPlan       PluginPlanInput       `json:"pluginPlan"`
}

type AccessInput struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	Status            string   `json:"status"`
	AgentEndpointID   string   `json:"agentEndpointId"`
	DefaultRouteSetID string   `json:"defaultRouteSetId"`
	ProfileIDs        []string `json:"profileIds"`
	EgressPolicyID    string   `json:"egressPolicyId"`
}

type AgentEndpointInput struct {
	ID            string `json:"id"`
	ClientOrigin  string `json:"clientOrigin"`
	ClientDialect string `json:"clientDialect"`
}

type ProfileInput struct {
	ID                      string           `json:"id"`
	Name                    string           `json:"name"`
	Description             string           `json:"description"`
	BackendDialect          string           `json:"backendDialect"`
	TargetID                string           `json:"targetId"`
	UpstreamWireProfileRef  string           `json:"upstreamWireProfileRef"`
	DefaultModelPolicy      ModelPolicyInput `json:"defaultModelPolicy"`
	AccountBindingIDs       []string         `json:"accountBindingIds"`
	DefaultAccountBindingID string           `json:"defaultAccountBindingId"`
}

type ModelPolicyInput struct {
	Mode       string `json:"mode"`
	FixedModel string `json:"fixedModel,omitempty"`
	MappingRef string `json:"mappingRef,omitempty"`
}

type ProviderTargetInput struct {
	ID           string   `json:"id"`
	ProfileID    string   `json:"profileId"`
	Origin       string   `json:"origin"`
	Protocol     string   `json:"protocol"`
	Capabilities []string `json:"capabilities"`
}

type AccountBindingInput struct {
	ID            string `json:"id"`
	ProfileID     string `json:"profileId"`
	Label         string `json:"label"`
	SecretRef     string `json:"secretRef"`
	AuthDriverRef string `json:"authDriverRef"`
	Enabled       bool   `json:"enabled"`
}

type RouteSetInput struct {
	ID                  string   `json:"id"`
	CandidateProfileIDs []string `json:"candidateProfileIds"`
}

type EgressPolicyInput struct {
	ID   string `json:"id"`
	Mode string `json:"mode"`
}

type PluginPlanInput struct {
	Mode       string   `json:"mode"`
	BindingIDs []string `json:"bindingIds"`
}

type Result struct {
	Outcome  access.WriteOutcome `json:"outcome"`
	Revision access.Revision     `json:"revision"`
}

func BuildCommand(
	pathAccessID string,
	input Input,
) (access.WriteCommand, error) {
	accessID, err := access.NewAccessID(input.Access.ID)
	if err != nil {
		return access.WriteCommand{}, err
	}
	if pathAccessID != accessID.String() {
		return access.WriteCommand{}, errors.New("path Access ID differs from the apply body")
	}
	expected := access.Revision(input.ExpectedRevision)
	if uint64(expected) != input.ExpectedRevision ||
		expected >= access.MaxRevision {
		return access.WriteCommand{}, access.ErrInvalidAccess
	}
	revision := expected + 1
	endpointID, err := access.NewAgentEndpointID(input.AgentEndpoint.ID)
	if err != nil {
		return access.WriteCommand{}, err
	}
	clientOrigin, err := access.NewClientOrigin(input.AgentEndpoint.ClientOrigin)
	if err != nil {
		return access.WriteCommand{}, err
	}
	profileIDs, err := profileIdentifiers(input.Access.ProfileIDs)
	if err != nil {
		return access.WriteCommand{}, err
	}
	defaultRouteSetID, err := access.NewRouteSetID(input.Access.DefaultRouteSetID)
	if err != nil {
		return access.WriteCommand{}, err
	}
	egressPolicyID, err := access.NewEgressPolicyID(input.EgressPolicy.ID)
	if err != nil {
		return access.WriteCommand{}, err
	}
	bindingEndpointID, err := access.NewAgentEndpointID(input.Access.AgentEndpointID)
	if err != nil {
		return access.WriteCommand{}, err
	}
	bindingEgressID, err := access.NewEgressPolicyID(input.Access.EgressPolicyID)
	if err != nil {
		return access.WriteCommand{}, err
	}
	profiles, err := buildProfiles(accessID, revision, input.Profiles)
	if err != nil {
		return access.WriteCommand{}, err
	}
	targets, err := buildTargets(accessID, revision, input.ProviderTargets)
	if err != nil {
		return access.WriteCommand{}, err
	}
	accounts, err := buildAccounts(accessID, revision, input.AccountBindings)
	if err != nil {
		return access.WriteCommand{}, err
	}
	routeSets, err := buildRouteSets(accessID, revision, input.RouteSets)
	if err != nil {
		return access.WriteCommand{}, err
	}
	pluginBindings := make([]access.PluginBindingID, len(input.PluginPlan.BindingIDs))
	for index, value := range input.PluginPlan.BindingIDs {
		pluginBindings[index], err = access.NewPluginBindingID(value)
		if err != nil {
			return access.WriteCommand{}, err
		}
	}
	command := access.WriteCommand{
		ExpectedRevision: expected,
		Aggregate: access.Aggregate{
			Binding: access.AccessBinding{
				ID:                accessID,
				Revision:          revision,
				Name:              input.Access.Name,
				Description:       input.Access.Description,
				Status:            access.AccessStatus(input.Access.Status),
				AgentEndpointID:   bindingEndpointID,
				DefaultRouteSetID: defaultRouteSetID,
				ProfileIDs:        profileIDs,
				EgressPolicyID:    bindingEgressID,
			},
			AgentEndpoint: access.AgentEndpoint{
				ID:            endpointID,
				Revision:      revision,
				AccessID:      accessID,
				ClientOrigin:  clientOrigin,
				ClientDialect: access.Dialect(input.AgentEndpoint.ClientDialect),
			},
			Profiles:        profiles,
			ProviderTargets: targets,
			AccountBindings: accounts,
			RouteSets:       routeSets,
			EgressPolicy: access.AccessEgressPolicy{
				ID:       egressPolicyID,
				Revision: revision,
				AccessID: accessID,
				Mode:     access.EgressMode(input.EgressPolicy.Mode),
			},
			PluginPlan: access.PluginPlan{
				Revision:   revision,
				AccessID:   accessID,
				Mode:       access.PluginPlanMode(input.PluginPlan.Mode),
				BindingIDs: pluginBindings,
			},
		},
	}
	if err := command.Aggregate.Validate(); err != nil {
		return access.WriteCommand{}, fmt.Errorf("validate Access apply input: %w", err)
	}
	return command, nil
}

func buildProfiles(
	accessID access.AccessID,
	revision access.Revision,
	inputs []ProfileInput,
) ([]access.EndpointProfile, error) {
	profiles := make([]access.EndpointProfile, len(inputs))
	for index, input := range inputs {
		id, err := access.NewEndpointProfileID(input.ID)
		if err != nil {
			return nil, err
		}
		targetID, err := access.NewProviderTargetID(input.TargetID)
		if err != nil {
			return nil, err
		}
		transportProfile, err := access.NewUpstreamWireProfileRef(
			input.UpstreamWireProfileRef,
		)
		if err != nil {
			return nil, err
		}
		accountIDs, err := accountIdentifiers(input.AccountBindingIDs)
		if err != nil {
			return nil, err
		}
		defaultAccountID, err := access.NewAccountBindingID(
			input.DefaultAccountBindingID,
		)
		if err != nil {
			return nil, err
		}
		modelPolicy, err := buildModelPolicy(revision, input.DefaultModelPolicy)
		if err != nil {
			return nil, err
		}
		profiles[index] = access.EndpointProfile{
			ID:                      id,
			Revision:                revision,
			AccessID:                accessID,
			Name:                    input.Name,
			Description:             input.Description,
			BackendDialect:          access.Dialect(input.BackendDialect),
			TargetID:                targetID,
			UpstreamWireProfileRef:  transportProfile,
			DefaultModelPolicy:      modelPolicy,
			AccountBindingIDs:       accountIDs,
			DefaultAccountBindingID: defaultAccountID,
		}
	}
	return profiles, nil
}

func buildModelPolicy(
	revision access.Revision,
	input ModelPolicyInput,
) (access.ModelPolicy, error) {
	policy := access.ModelPolicy{
		Revision: revision,
		Mode:     access.ModelPolicyMode(input.Mode),
	}
	var err error
	if input.FixedModel != "" {
		policy.FixedModel, err = access.NewModelName(input.FixedModel)
		if err != nil {
			return access.ModelPolicy{}, err
		}
	}
	if input.MappingRef != "" {
		policy.MappingRef, err = access.NewModelMappingRef(input.MappingRef)
		if err != nil {
			return access.ModelPolicy{}, err
		}
	}
	return policy, policy.Validate()
}

func buildTargets(
	accessID access.AccessID,
	revision access.Revision,
	inputs []ProviderTargetInput,
) ([]access.ProviderTarget, error) {
	targets := make([]access.ProviderTarget, len(inputs))
	for index, input := range inputs {
		id, err := access.NewProviderTargetID(input.ID)
		if err != nil {
			return nil, err
		}
		profileID, err := access.NewEndpointProfileID(input.ProfileID)
		if err != nil {
			return nil, err
		}
		origin, err := access.NewProviderOrigin(input.Origin)
		if err != nil {
			return nil, err
		}
		capabilities := make(
			[]access.ProviderCapability,
			len(input.Capabilities),
		)
		for capabilityIndex, capability := range input.Capabilities {
			capabilities[capabilityIndex] = access.ProviderCapability(capability)
		}
		targets[index] = access.ProviderTarget{
			ID:           id,
			Revision:     revision,
			AccessID:     accessID,
			ProfileID:    profileID,
			Origin:       origin,
			Protocol:     access.Dialect(input.Protocol),
			Capabilities: capabilities,
		}
	}
	return targets, nil
}

func buildAccounts(
	accessID access.AccessID,
	revision access.Revision,
	inputs []AccountBindingInput,
) ([]access.ProviderAccountBinding, error) {
	accounts := make([]access.ProviderAccountBinding, len(inputs))
	for index, input := range inputs {
		id, err := access.NewAccountBindingID(input.ID)
		if err != nil {
			return nil, err
		}
		profileID, err := access.NewEndpointProfileID(input.ProfileID)
		if err != nil {
			return nil, err
		}
		secretRef, err := access.NewSecretRef(input.SecretRef)
		if err != nil {
			return nil, err
		}
		authDriver, err := access.NewAuthDriverRef(input.AuthDriverRef)
		if err != nil {
			return nil, err
		}
		accounts[index] = access.ProviderAccountBinding{
			ID:            id,
			Revision:      revision,
			AccessID:      accessID,
			ProfileID:     profileID,
			Label:         input.Label,
			SecretRef:     secretRef,
			AuthDriverRef: authDriver,
			Enabled:       input.Enabled,
		}
	}
	return accounts, nil
}

func buildRouteSets(
	accessID access.AccessID,
	revision access.Revision,
	inputs []RouteSetInput,
) ([]access.RouteSet, error) {
	routeSets := make([]access.RouteSet, len(inputs))
	for index, input := range inputs {
		id, err := access.NewRouteSetID(input.ID)
		if err != nil {
			return nil, err
		}
		profileIDs, err := profileIdentifiers(input.CandidateProfileIDs)
		if err != nil {
			return nil, err
		}
		routeSets[index] = access.RouteSet{
			ID:                  id,
			Revision:            revision,
			AccessID:            accessID,
			CandidateProfileIDs: profileIDs,
		}
	}
	return routeSets, nil
}

func profileIdentifiers(values []string) ([]access.EndpointProfileID, error) {
	identifiers := make([]access.EndpointProfileID, len(values))
	for index, value := range values {
		identifier, err := access.NewEndpointProfileID(value)
		if err != nil {
			return nil, err
		}
		identifiers[index] = identifier
	}
	return identifiers, nil
}

func accountIdentifiers(values []string) ([]access.AccountBindingID, error) {
	identifiers := make([]access.AccountBindingID, len(values))
	for index, value := range values {
		identifier, err := access.NewAccountBindingID(value)
		if err != nil {
			return nil, err
		}
		identifiers[index] = identifier
	}
	return identifiers, nil
}
