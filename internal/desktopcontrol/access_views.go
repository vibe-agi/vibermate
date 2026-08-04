package desktopcontrol

import (
	"net/http"
	"sort"
	"strconv"

	"github.com/vibe-agi/vibermate/internal/access"
)

// AccessListResponse is a durable catalog, not the active-plan projection.
// Draft and disabled Accesses therefore remain visible and editable.
type AccessListResponse struct {
	Items []AccessSummaryResponse `json:"items"`
}

type AccessSummaryResponse struct {
	AccessID      string              `json:"accessId"`
	Name          string              `json:"name"`
	Description   string              `json:"description"`
	Status        access.AccessStatus `json:"status"`
	Revision      access.Revision     `json:"revision"`
	ClientOrigin  string              `json:"clientOrigin"`
	ClientDialect access.Dialect      `json:"clientDialect"`
}

// AccessDetailResponse is the complete non-secret editable projection. The
// aggregate revision is transport state for If-Match; it is not a field a
// person edits. Credential SecretRefs deliberately stay behind this boundary.
type AccessDetailResponse struct {
	Revision        access.Revision                `json:"revision"`
	Access          AccessBindingResponse          `json:"access"`
	AgentEndpoint   AgentEndpointResponse          `json:"agentEndpoint"`
	Profiles        []AccessProfileResponse        `json:"profiles"`
	ProviderTargets []AccessProviderTargetResponse `json:"providerTargets"`
	AccountBindings []AccessAccountBindingResponse `json:"accountBindings"`
	RouteSets       []AccessRouteSetResponse       `json:"routeSets"`
	EgressPolicy    AccessEgressPolicyResponse     `json:"egressPolicy"`
	PluginPlan      AccessPluginPlanResponse       `json:"pluginPlan"`
}

type AccessBindingResponse struct {
	ID                string              `json:"id"`
	Name              string              `json:"name"`
	Description       string              `json:"description"`
	Status            access.AccessStatus `json:"status"`
	AgentEndpointID   string              `json:"agentEndpointId"`
	DefaultRouteSetID string              `json:"defaultRouteSetId"`
	ProfileIDs        []string            `json:"profileIds"`
	EgressPolicyID    string              `json:"egressPolicyId"`
}

type AgentEndpointResponse struct {
	ID            string         `json:"id"`
	ClientOrigin  string         `json:"clientOrigin"`
	ClientDialect access.Dialect `json:"clientDialect"`
}

type AccessProfileResponse struct {
	ID                      string                    `json:"id"`
	Name                    string                    `json:"name"`
	Description             string                    `json:"description"`
	BackendDialect          access.Dialect            `json:"backendDialect"`
	TargetID                string                    `json:"targetId"`
	UpstreamWireProfileRef  string                    `json:"upstreamWireProfileRef"`
	DefaultModelPolicy      AccessModelPolicyResponse `json:"defaultModelPolicy"`
	AccountBindingIDs       []string                  `json:"accountBindingIds"`
	DefaultAccountBindingID string                    `json:"defaultAccountBindingId"`
}

type AccessModelPolicyResponse struct {
	Mode       access.ModelPolicyMode `json:"mode"`
	FixedModel string                 `json:"fixedModel,omitempty"`
	MappingRef string                 `json:"mappingRef,omitempty"`
}

type AccessProviderTargetResponse struct {
	ID           string                      `json:"id"`
	ProfileID    string                      `json:"profileId"`
	Origin       string                      `json:"origin"`
	Protocol     access.Dialect              `json:"protocol"`
	Capabilities []access.ProviderCapability `json:"capabilities"`
}

// AccessAccountBindingResponse identifies a credential without disclosing
// either its value or its internal SecretRef. An edit workflow must preserve
// the existing reference server-side, or use the dedicated replace-secret
// action; it must never ask the browser to round-trip that reference.
type AccessAccountBindingResponse struct {
	ID             string               `json:"id"`
	ProfileID      string               `json:"profileId"`
	Label          string               `json:"label"`
	AuthDriverRef  string               `json:"authDriverRef"`
	Enabled        bool                 `json:"enabled"`
	SecretHandling AccessSecretHandling `json:"secretHandling"`
}

type AccessSecretHandling string

const AccessSecretHandlingPreserveExisting AccessSecretHandling = "preserve_existing"

type AccessRouteSetResponse struct {
	ID                  string                `json:"id"`
	CandidateProfileIDs []string              `json:"candidateProfileIds"`
	Fallback            access.FallbackPolicy `json:"fallback"`
}

type AccessEgressPolicyResponse struct {
	ID   string            `json:"id"`
	Mode access.EgressMode `json:"mode"`
}

type AccessPluginPlanResponse struct {
	Mode       access.PluginPlanMode `json:"mode"`
	BindingIDs []string              `json:"bindingIds"`
}

func (handler *Handler) listAccesses(
	writer http.ResponseWriter,
	request *http.Request,
) {
	aggregates, err := handler.accessCatalog.ListAccesses(request.Context())
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	items := make([]AccessSummaryResponse, len(aggregates))
	for index, aggregate := range aggregates {
		items[index] = accessSummaryResponseOf(aggregate)
	}
	sort.Slice(items, func(left, right int) bool {
		return items[left].AccessID < items[right].AccessID
	})
	writeJSON(writer, http.StatusOK, AccessListResponse{Items: items})
}

func (handler *Handler) getAccess(
	writer http.ResponseWriter,
	request *http.Request,
) {
	accessID, err := access.NewAccessID(request.PathValue("accessId"))
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	aggregate, exists, err := handler.accessCatalog.ReadAccess(
		request.Context(),
		accessID,
	)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	if !exists {
		writeProblem(writer, http.StatusNotFound, ReasonAccessNotConfigured)
		return
	}
	writer.Header().Set(
		"ETag",
		`"revision-`+strconv.FormatUint(uint64(aggregate.Binding.Revision), 10)+`"`,
	)
	writeJSON(writer, http.StatusOK, accessDetailResponseOf(aggregate))
}

func accessSummaryResponseOf(aggregate access.Aggregate) AccessSummaryResponse {
	return AccessSummaryResponse{
		AccessID:      aggregate.Binding.ID.String(),
		Name:          aggregate.Binding.Name,
		Description:   aggregate.Binding.Description,
		Status:        aggregate.Binding.Status,
		Revision:      aggregate.Binding.Revision,
		ClientOrigin:  aggregate.AgentEndpoint.ClientOrigin.String(),
		ClientDialect: aggregate.AgentEndpoint.ClientDialect,
	}
}

func accessDetailResponseOf(aggregate access.Aggregate) AccessDetailResponse {
	profiles := make([]AccessProfileResponse, len(aggregate.Profiles))
	for index, profile := range aggregate.Profiles {
		accountBindingIDs := make([]string, len(profile.AccountBindingIDs))
		for idIndex, id := range profile.AccountBindingIDs {
			accountBindingIDs[idIndex] = id.String()
		}
		profiles[index] = AccessProfileResponse{
			ID:                     profile.ID.String(),
			Name:                   profile.Name,
			Description:            profile.Description,
			BackendDialect:         profile.BackendDialect,
			TargetID:               profile.TargetID.String(),
			UpstreamWireProfileRef: profile.UpstreamWireProfileRef.String(),
			DefaultModelPolicy: AccessModelPolicyResponse{
				Mode:       profile.DefaultModelPolicy.Mode,
				FixedModel: profile.DefaultModelPolicy.FixedModel.String(),
				MappingRef: profile.DefaultModelPolicy.MappingRef.String(),
			},
			AccountBindingIDs:       accountBindingIDs,
			DefaultAccountBindingID: profile.DefaultAccountBindingID.String(),
		}
	}
	targets := make([]AccessProviderTargetResponse, len(aggregate.ProviderTargets))
	for index, target := range aggregate.ProviderTargets {
		targets[index] = AccessProviderTargetResponse{
			ID:           target.ID.String(),
			ProfileID:    target.ProfileID.String(),
			Origin:       target.Origin.String(),
			Protocol:     target.Protocol,
			Capabilities: append([]access.ProviderCapability(nil), target.Capabilities...),
		}
	}
	bindings := make([]AccessAccountBindingResponse, len(aggregate.AccountBindings))
	for index, binding := range aggregate.AccountBindings {
		bindings[index] = AccessAccountBindingResponse{
			ID:             binding.ID.String(),
			ProfileID:      binding.ProfileID.String(),
			Label:          binding.Label,
			AuthDriverRef:  binding.AuthDriverRef.String(),
			Enabled:        binding.Enabled,
			SecretHandling: AccessSecretHandlingPreserveExisting,
		}
	}
	routeSets := make([]AccessRouteSetResponse, len(aggregate.RouteSets))
	for index, routeSet := range aggregate.RouteSets {
		candidateIDs := make([]string, len(routeSet.CandidateProfileIDs))
		for idIndex, id := range routeSet.CandidateProfileIDs {
			candidateIDs[idIndex] = id.String()
		}
		routeSets[index] = AccessRouteSetResponse{
			ID:                  routeSet.ID.String(),
			CandidateProfileIDs: candidateIDs,
			Fallback:            routeSet.FallbackMode(),
		}
	}
	profileIDs := make([]string, len(aggregate.Binding.ProfileIDs))
	for index, id := range aggregate.Binding.ProfileIDs {
		profileIDs[index] = id.String()
	}
	pluginBindingIDs := make([]string, len(aggregate.PluginPlan.BindingIDs))
	for index, id := range aggregate.PluginPlan.BindingIDs {
		pluginBindingIDs[index] = id.String()
	}
	return AccessDetailResponse{
		Revision: aggregate.Binding.Revision,
		Access: AccessBindingResponse{
			ID:                aggregate.Binding.ID.String(),
			Name:              aggregate.Binding.Name,
			Description:       aggregate.Binding.Description,
			Status:            aggregate.Binding.Status,
			AgentEndpointID:   aggregate.Binding.AgentEndpointID.String(),
			DefaultRouteSetID: aggregate.Binding.DefaultRouteSetID.String(),
			ProfileIDs:        profileIDs,
			EgressPolicyID:    aggregate.Binding.EgressPolicyID.String(),
		},
		AgentEndpoint: AgentEndpointResponse{
			ID:            aggregate.AgentEndpoint.ID.String(),
			ClientOrigin:  aggregate.AgentEndpoint.ClientOrigin.String(),
			ClientDialect: aggregate.AgentEndpoint.ClientDialect,
		},
		Profiles:        profiles,
		ProviderTargets: targets,
		AccountBindings: bindings,
		RouteSets:       routeSets,
		EgressPolicy: AccessEgressPolicyResponse{
			ID:   aggregate.EgressPolicy.ID.String(),
			Mode: aggregate.EgressPolicy.Mode,
		},
		PluginPlan: AccessPluginPlanResponse{
			Mode:       aggregate.PluginPlan.Mode,
			BindingIDs: pluginBindingIDs,
		},
	}
}
