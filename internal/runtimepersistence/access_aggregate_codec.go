package runtimepersistence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/vibe-agi/vibermate/internal/access"
)

const accessAggregateFormatVersion int64 = 1

type aggregatePayload struct {
	Status            string                  `json:"status"`
	AgentEndpointID   string                  `json:"agentEndpointId"`
	DefaultRouteSetID string                  `json:"defaultRouteSetId"`
	ProfileIDs        []string                `json:"profileIds"`
	EgressPolicyID    string                  `json:"egressPolicyId"`
	AgentEndpoint     agentEndpointPayload    `json:"agentEndpoint"`
	Profiles          []profilePayload        `json:"profiles"`
	ProviderTargets   []providerTargetPayload `json:"providerTargets"`
	AccountBindings   []accountPayload        `json:"accountBindings"`
	RouteSets         []routeSetPayload       `json:"routeSets"`
	EgressPolicy      egressPolicyPayload     `json:"egressPolicy"`
	PluginPlan        pluginPlanPayload       `json:"pluginPlan"`
}

type agentEndpointPayload struct {
	ID            string          `json:"id"`
	Revision      access.Revision `json:"revision"`
	AccessID      string          `json:"accessId"`
	ClientOrigin  string          `json:"clientOrigin"`
	ClientDialect string          `json:"clientDialect"`
}

type modelPolicyPayload struct {
	Revision   access.Revision `json:"revision"`
	Mode       string          `json:"mode"`
	FixedModel string          `json:"fixedModel"`
	MappingRef string          `json:"mappingRef"`
}

type profilePayload struct {
	ID                      string             `json:"id"`
	Revision                access.Revision    `json:"revision"`
	AccessID                string             `json:"accessId"`
	Kind                    string             `json:"kind"`
	CredentialSource        string             `json:"credentialSource"`
	ProcessingMode          string             `json:"processingMode"`
	Name                    string             `json:"name"`
	Description             string             `json:"description"`
	BackendDialect          string             `json:"backendDialect"`
	TargetID                string             `json:"targetId"`
	UpstreamWireProfileRef  string             `json:"upstreamWireProfileRef"`
	DefaultModelPolicy      modelPolicyPayload `json:"defaultModelPolicy"`
	AccountBindingIDs       []string           `json:"accountBindingIds"`
	DefaultAccountBindingID string             `json:"defaultAccountBindingId"`
}

type providerTargetPayload struct {
	ID           string                      `json:"id"`
	Revision     access.Revision             `json:"revision"`
	AccessID     string                      `json:"accessId"`
	ProfileID    string                      `json:"profileId"`
	Origin       string                      `json:"origin"`
	Protocol     string                      `json:"protocol"`
	Capabilities []access.ProviderCapability `json:"capabilities"`
}

type accountPayload struct {
	ID            string          `json:"id"`
	Revision      access.Revision `json:"revision"`
	AccessID      string          `json:"accessId"`
	ProfileID     string          `json:"profileId"`
	Label         string          `json:"label"`
	SecretRef     string          `json:"secretRef"`
	AuthDriverRef string          `json:"authDriverRef"`
	Enabled       bool            `json:"enabled"`
}

type routeSetPayload struct {
	ID                  string          `json:"id"`
	Revision            access.Revision `json:"revision"`
	AccessID            string          `json:"accessId"`
	CandidateProfileIDs []string        `json:"candidateProfileIds"`
	// Fallback is omitted on a route set written before it existed, and an
	// absent policy reads as disabled.
	Fallback string `json:"fallback,omitempty"`
}

type egressPolicyPayload struct {
	ID       string          `json:"id"`
	Revision access.Revision `json:"revision"`
	AccessID string          `json:"accessId"`
	Mode     string          `json:"mode"`
}

type pluginPlanPayload struct {
	Revision   access.Revision `json:"revision"`
	AccessID   string          `json:"accessId"`
	Mode       string          `json:"mode"`
	BindingIDs []string        `json:"bindingIds"`
}

func encodeAccessAggregatePayload(aggregate access.Aggregate) ([]byte, error) {
	payload := aggregatePayload{
		Status:            string(aggregate.Binding.Status),
		AgentEndpointID:   aggregate.Binding.AgentEndpointID.String(),
		DefaultRouteSetID: aggregate.Binding.DefaultRouteSetID.String(),
		ProfileIDs:        endpointProfileIDStrings(aggregate.Binding.ProfileIDs),
		EgressPolicyID:    aggregate.Binding.EgressPolicyID.String(),
		AgentEndpoint: agentEndpointPayload{
			ID:            aggregate.AgentEndpoint.ID.String(),
			Revision:      aggregate.AgentEndpoint.Revision,
			AccessID:      aggregate.AgentEndpoint.AccessID.String(),
			ClientOrigin:  aggregate.AgentEndpoint.ClientOrigin.String(),
			ClientDialect: string(aggregate.AgentEndpoint.ClientDialect),
		},
		Profiles:        make([]profilePayload, 0, len(aggregate.Profiles)),
		ProviderTargets: make([]providerTargetPayload, 0, len(aggregate.ProviderTargets)),
		AccountBindings: make([]accountPayload, 0, len(aggregate.AccountBindings)),
		RouteSets:       make([]routeSetPayload, 0, len(aggregate.RouteSets)),
		EgressPolicy: egressPolicyPayload{
			ID:       aggregate.EgressPolicy.ID.String(),
			Revision: aggregate.EgressPolicy.Revision,
			AccessID: aggregate.EgressPolicy.AccessID.String(),
			Mode:     string(aggregate.EgressPolicy.Mode),
		},
		PluginPlan: pluginPlanPayload{
			Revision:   aggregate.PluginPlan.Revision,
			AccessID:   aggregate.PluginPlan.AccessID.String(),
			Mode:       string(aggregate.PluginPlan.Mode),
			BindingIDs: pluginBindingIDStrings(aggregate.PluginPlan.BindingIDs),
		},
	}
	for _, profile := range aggregate.Profiles {
		payload.Profiles = append(payload.Profiles, profilePayload{
			ID:                     profile.ID.String(),
			Revision:               profile.Revision,
			AccessID:               profile.AccessID.String(),
			Kind:                   string(profile.Kind),
			CredentialSource:       string(profile.CredentialSource),
			ProcessingMode:         string(profile.ProcessingMode),
			Name:                   profile.Name,
			Description:            profile.Description,
			BackendDialect:         string(profile.BackendDialect),
			TargetID:               profile.TargetID.String(),
			UpstreamWireProfileRef: profile.UpstreamWireProfileRef.String(),
			DefaultModelPolicy: modelPolicyPayload{
				Revision:   profile.DefaultModelPolicy.Revision,
				Mode:       string(profile.DefaultModelPolicy.Mode),
				FixedModel: profile.DefaultModelPolicy.FixedModel.String(),
				MappingRef: profile.DefaultModelPolicy.MappingRef.String(),
			},
			AccountBindingIDs:       accountBindingIDStrings(profile.AccountBindingIDs),
			DefaultAccountBindingID: profile.DefaultAccountBindingID.String(),
		})
	}
	for _, target := range aggregate.ProviderTargets {
		payload.ProviderTargets = append(payload.ProviderTargets, providerTargetPayload{
			ID:           target.ID.String(),
			Revision:     target.Revision,
			AccessID:     target.AccessID.String(),
			ProfileID:    target.ProfileID.String(),
			Origin:       target.Origin.String(),
			Protocol:     string(target.Protocol),
			Capabilities: append([]access.ProviderCapability(nil), target.Capabilities...),
		})
	}
	for _, account := range aggregate.AccountBindings {
		payload.AccountBindings = append(payload.AccountBindings, accountPayload{
			ID:            account.ID.String(),
			Revision:      account.Revision,
			AccessID:      account.AccessID.String(),
			ProfileID:     account.ProfileID.String(),
			Label:         account.Label,
			SecretRef:     account.SecretRef.String(),
			AuthDriverRef: account.AuthDriverRef.String(),
			Enabled:       account.Enabled,
		})
	}
	for _, routeSet := range aggregate.RouteSets {
		payload.RouteSets = append(payload.RouteSets, routeSetPayload{
			ID:                  routeSet.ID.String(),
			Revision:            routeSet.Revision,
			AccessID:            routeSet.AccessID.String(),
			CandidateProfileIDs: endpointProfileIDStrings(routeSet.CandidateProfileIDs),
			Fallback:            string(routeSet.FallbackMode()),
		})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode Access aggregate payload: %w", err)
	}
	return encoded, nil
}

func decodeAccessAggregate(
	accessIDText string,
	revision int64,
	name string,
	description string,
	formatVersion int64,
	encoded []byte,
) (access.Aggregate, error) {
	if formatVersion != accessAggregateFormatVersion {
		return access.Aggregate{}, fmt.Errorf(
			"%w: unsupported Access aggregate format=%d",
			access.ErrInvalidRepositoryState,
			formatVersion,
		)
	}
	var payload aggregatePayload
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return access.Aggregate{}, fmt.Errorf(
			"%w: decode Access aggregate payload: %w",
			access.ErrInvalidRepositoryState,
			err,
		)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return access.Aggregate{}, fmt.Errorf(
			"%w: Access aggregate payload has trailing data",
			access.ErrInvalidRepositoryState,
		)
	}

	accessID, err := access.NewAccessID(accessIDText)
	if err != nil {
		return access.Aggregate{}, invalidRepositoryValue("Access ID", err)
	}
	if revision <= 0 {
		return access.Aggregate{}, fmt.Errorf(
			"%w: accessId=%q revision=%d",
			access.ErrInvalidRepositoryState,
			accessIDText,
			revision,
		)
	}
	endpointID, err := access.NewAgentEndpointID(payload.AgentEndpointID)
	if err != nil {
		return access.Aggregate{}, invalidRepositoryValue("AgentEndpoint reference", err)
	}
	defaultRouteSetID, err := access.NewRouteSetID(payload.DefaultRouteSetID)
	if err != nil {
		return access.Aggregate{}, invalidRepositoryValue("default RouteSet reference", err)
	}
	egressPolicyID, err := access.NewEgressPolicyID(payload.EgressPolicyID)
	if err != nil {
		return access.Aggregate{}, invalidRepositoryValue("egress policy reference", err)
	}
	profileIDs, err := decodeEndpointProfileIDs(payload.ProfileIDs)
	if err != nil {
		return access.Aggregate{}, err
	}
	agentEndpoint, err := decodeAgentEndpoint(payload.AgentEndpoint)
	if err != nil {
		return access.Aggregate{}, err
	}
	profiles, err := decodeProfiles(payload.Profiles)
	if err != nil {
		return access.Aggregate{}, err
	}
	targets, err := decodeProviderTargets(payload.ProviderTargets)
	if err != nil {
		return access.Aggregate{}, err
	}
	accounts, err := decodeAccounts(payload.AccountBindings)
	if err != nil {
		return access.Aggregate{}, err
	}
	routeSets, err := decodeRouteSets(payload.RouteSets)
	if err != nil {
		return access.Aggregate{}, err
	}
	egressPolicy, err := decodeEgressPolicy(payload.EgressPolicy)
	if err != nil {
		return access.Aggregate{}, err
	}
	pluginPlan, err := decodePluginPlan(payload.PluginPlan)
	if err != nil {
		return access.Aggregate{}, err
	}
	aggregate := access.Aggregate{
		Binding: access.AccessBinding{
			ID:                accessID,
			Revision:          access.Revision(revision),
			Name:              name,
			Description:       description,
			Status:            access.AccessStatus(payload.Status),
			AgentEndpointID:   endpointID,
			DefaultRouteSetID: defaultRouteSetID,
			ProfileIDs:        profileIDs,
			EgressPolicyID:    egressPolicyID,
		},
		AgentEndpoint:   agentEndpoint,
		Profiles:        profiles,
		ProviderTargets: targets,
		AccountBindings: accounts,
		RouteSets:       routeSets,
		EgressPolicy:    egressPolicy,
		PluginPlan:      pluginPlan,
	}
	if err := aggregate.Validate(); err != nil {
		return access.Aggregate{}, fmt.Errorf(
			"%w: accessId=%q: %w",
			access.ErrInvalidRepositoryState,
			accessIDText,
			err,
		)
	}
	return aggregate, nil
}

func decodeAgentEndpoint(payload agentEndpointPayload) (access.AgentEndpoint, error) {
	id, err := access.NewAgentEndpointID(payload.ID)
	if err != nil {
		return access.AgentEndpoint{}, invalidRepositoryValue("AgentEndpoint ID", err)
	}
	accessID, err := access.NewAccessID(payload.AccessID)
	if err != nil {
		return access.AgentEndpoint{}, invalidRepositoryValue("AgentEndpoint Access ID", err)
	}
	origin, err := access.NewClientOrigin(payload.ClientOrigin)
	if err != nil {
		return access.AgentEndpoint{}, invalidRepositoryValue("ClientOrigin", err)
	}
	return access.AgentEndpoint{
		ID:            id,
		Revision:      payload.Revision,
		AccessID:      accessID,
		ClientOrigin:  origin,
		ClientDialect: access.Dialect(payload.ClientDialect),
	}, nil
}

func decodeProfiles(payloads []profilePayload) ([]access.EndpointProfile, error) {
	profiles := make([]access.EndpointProfile, 0, len(payloads))
	for _, payload := range payloads {
		id, err := access.NewEndpointProfileID(payload.ID)
		if err != nil {
			return nil, invalidRepositoryValue("EndpointProfile ID", err)
		}
		accessID, err := access.NewAccessID(payload.AccessID)
		if err != nil {
			return nil, invalidRepositoryValue("EndpointProfile Access ID", err)
		}
		targetID, err := access.NewProviderTargetID(payload.TargetID)
		if err != nil {
			return nil, invalidRepositoryValue("ProviderTarget reference", err)
		}
		transportProfile, err := access.NewUpstreamWireProfileRef(
			payload.UpstreamWireProfileRef,
		)
		if err != nil {
			return nil, invalidRepositoryValue(
				"transport fingerprint profile reference",
				err,
			)
		}
		accountIDs, err := decodeAccountBindingIDs(payload.AccountBindingIDs)
		if err != nil {
			return nil, err
		}
		var defaultAccountID access.AccountBindingID
		if payload.DefaultAccountBindingID != "" {
			defaultAccountID, err = access.NewAccountBindingID(payload.DefaultAccountBindingID)
			if err != nil {
				return nil, invalidRepositoryValue("default account reference", err)
			}
		}
		policy, err := decodeModelPolicy(payload.DefaultModelPolicy)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, access.EndpointProfile{
			ID:                      id,
			Revision:                payload.Revision,
			AccessID:                accessID,
			Kind:                    access.EndpointProfileKind(payload.Kind),
			CredentialSource:        access.CredentialSource(payload.CredentialSource),
			ProcessingMode:          access.ProfileProcessingMode(payload.ProcessingMode),
			Name:                    payload.Name,
			Description:             payload.Description,
			BackendDialect:          access.Dialect(payload.BackendDialect),
			TargetID:                targetID,
			UpstreamWireProfileRef:  transportProfile,
			DefaultModelPolicy:      policy,
			AccountBindingIDs:       accountIDs,
			DefaultAccountBindingID: defaultAccountID,
		})
	}
	return profiles, nil
}

func decodeModelPolicy(payload modelPolicyPayload) (access.ModelPolicy, error) {
	policy := access.ModelPolicy{
		Revision: payload.Revision,
		Mode:     access.ModelPolicyMode(payload.Mode),
	}
	var err error
	if payload.FixedModel != "" {
		policy.FixedModel, err = access.NewModelName(payload.FixedModel)
		if err != nil {
			return access.ModelPolicy{}, invalidRepositoryValue("fixed model", err)
		}
	}
	if payload.MappingRef != "" {
		policy.MappingRef, err = access.NewModelMappingRef(payload.MappingRef)
		if err != nil {
			return access.ModelPolicy{}, invalidRepositoryValue("model mapping reference", err)
		}
	}
	return policy, nil
}

func decodeProviderTargets(
	payloads []providerTargetPayload,
) ([]access.ProviderTarget, error) {
	targets := make([]access.ProviderTarget, 0, len(payloads))
	for _, payload := range payloads {
		id, err := access.NewProviderTargetID(payload.ID)
		if err != nil {
			return nil, invalidRepositoryValue("ProviderTarget ID", err)
		}
		accessID, err := access.NewAccessID(payload.AccessID)
		if err != nil {
			return nil, invalidRepositoryValue("ProviderTarget Access ID", err)
		}
		profileID, err := access.NewEndpointProfileID(payload.ProfileID)
		if err != nil {
			return nil, invalidRepositoryValue("ProviderTarget profile ID", err)
		}
		origin, err := access.NewProviderOrigin(payload.Origin)
		if err != nil {
			return nil, invalidRepositoryValue("ProviderTarget origin", err)
		}
		targets = append(targets, access.ProviderTarget{
			ID:           id,
			Revision:     payload.Revision,
			AccessID:     accessID,
			ProfileID:    profileID,
			Origin:       origin,
			Protocol:     access.Dialect(payload.Protocol),
			Capabilities: append([]access.ProviderCapability(nil), payload.Capabilities...),
		})
	}
	return targets, nil
}

func decodeAccounts(payloads []accountPayload) ([]access.ProviderAccountBinding, error) {
	accounts := make([]access.ProviderAccountBinding, 0, len(payloads))
	for _, payload := range payloads {
		id, err := access.NewAccountBindingID(payload.ID)
		if err != nil {
			return nil, invalidRepositoryValue("account binding ID", err)
		}
		accessID, err := access.NewAccessID(payload.AccessID)
		if err != nil {
			return nil, invalidRepositoryValue("account Access ID", err)
		}
		profileID, err := access.NewEndpointProfileID(payload.ProfileID)
		if err != nil {
			return nil, invalidRepositoryValue("account profile ID", err)
		}
		secretRef, err := access.NewSecretRef(payload.SecretRef)
		if err != nil {
			return nil, invalidRepositoryValue("SecretRef", err)
		}
		authDriver, err := access.NewAuthDriverRef(payload.AuthDriverRef)
		if err != nil {
			return nil, invalidRepositoryValue("AuthDriver reference", err)
		}
		accounts = append(accounts, access.ProviderAccountBinding{
			ID:            id,
			Revision:      payload.Revision,
			AccessID:      accessID,
			ProfileID:     profileID,
			Label:         payload.Label,
			SecretRef:     secretRef,
			AuthDriverRef: authDriver,
			Enabled:       payload.Enabled,
		})
	}
	return accounts, nil
}

func decodeRouteSets(payloads []routeSetPayload) ([]access.RouteSet, error) {
	routeSets := make([]access.RouteSet, 0, len(payloads))
	for _, payload := range payloads {
		id, err := access.NewRouteSetID(payload.ID)
		if err != nil {
			return nil, invalidRepositoryValue("RouteSet ID", err)
		}
		accessID, err := access.NewAccessID(payload.AccessID)
		if err != nil {
			return nil, invalidRepositoryValue("RouteSet Access ID", err)
		}
		candidates, err := decodeEndpointProfileIDs(payload.CandidateProfileIDs)
		if err != nil {
			return nil, err
		}
		routeSets = append(routeSets, access.RouteSet{
			ID:                  id,
			Revision:            payload.Revision,
			AccessID:            accessID,
			CandidateProfileIDs: candidates,
			Fallback:            access.FallbackPolicy(payload.Fallback),
		})
	}
	return routeSets, nil
}

func decodeEgressPolicy(payload egressPolicyPayload) (access.AccessEgressPolicy, error) {
	id, err := access.NewEgressPolicyID(payload.ID)
	if err != nil {
		return access.AccessEgressPolicy{}, invalidRepositoryValue("egress policy ID", err)
	}
	accessID, err := access.NewAccessID(payload.AccessID)
	if err != nil {
		return access.AccessEgressPolicy{}, invalidRepositoryValue("egress Access ID", err)
	}
	return access.AccessEgressPolicy{
		ID:       id,
		Revision: payload.Revision,
		AccessID: accessID,
		Mode:     access.EgressMode(payload.Mode),
	}, nil
}

func decodePluginPlan(payload pluginPlanPayload) (access.PluginPlan, error) {
	accessID, err := access.NewAccessID(payload.AccessID)
	if err != nil {
		return access.PluginPlan{}, invalidRepositoryValue("plugin plan Access ID", err)
	}
	bindingIDs := make([]access.PluginBindingID, 0, len(payload.BindingIDs))
	for _, value := range payload.BindingIDs {
		id, idErr := access.NewPluginBindingID(value)
		if idErr != nil {
			return access.PluginPlan{}, invalidRepositoryValue("plugin binding reference", idErr)
		}
		bindingIDs = append(bindingIDs, id)
	}
	return access.PluginPlan{
		Revision:   payload.Revision,
		AccessID:   accessID,
		Mode:       access.PluginPlanMode(payload.Mode),
		BindingIDs: bindingIDs,
	}, nil
}

func decodeEndpointProfileIDs(values []string) ([]access.EndpointProfileID, error) {
	ids := make([]access.EndpointProfileID, 0, len(values))
	for _, value := range values {
		id, err := access.NewEndpointProfileID(value)
		if err != nil {
			return nil, invalidRepositoryValue("EndpointProfile reference", err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func decodeAccountBindingIDs(values []string) ([]access.AccountBindingID, error) {
	ids := make([]access.AccountBindingID, 0, len(values))
	for _, value := range values {
		id, err := access.NewAccountBindingID(value)
		if err != nil {
			return nil, invalidRepositoryValue("account binding reference", err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func endpointProfileIDStrings(ids []access.EndpointProfileID) []string {
	values := make([]string, len(ids))
	for index, id := range ids {
		values[index] = id.String()
	}
	return values
}

func accountBindingIDStrings(ids []access.AccountBindingID) []string {
	values := make([]string, len(ids))
	for index, id := range ids {
		values[index] = id.String()
	}
	return values
}

func pluginBindingIDStrings(ids []access.PluginBindingID) []string {
	values := make([]string, len(ids))
	for index, id := range ids {
		values[index] = id.String()
	}
	return values
}

func invalidRepositoryValue(label string, err error) error {
	return fmt.Errorf("%w: decode %s: %w", access.ErrInvalidRepositoryState, label, err)
}
