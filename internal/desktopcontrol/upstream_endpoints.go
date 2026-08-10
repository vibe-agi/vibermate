package desktopcontrol

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"net/http"
	"slices"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

type UpstreamEndpointKind string

const (
	UpstreamEndpointKindAnthropic        UpstreamEndpointKind = "anthropic"
	UpstreamEndpointKindOpenAICompatible UpstreamEndpointKind = "openai_compatible"
)

type UpstreamEndpointResponse struct {
	ID               string                            `json:"id"`
	DisplayName      string                            `json:"displayName"`
	Origin           string                            `json:"origin"`
	RealmID          string                            `json:"realmId"`
	BackendProtocols []string                          `json:"backendProtocols"`
	Capabilities     []protocolspec.ProviderCapability `json:"capabilities"`
	AccountKinds     []ProviderAccountKind             `json:"accountKinds"`
	State            upstreamendpoint.State            `json:"state"`
	Revision         uint64                            `json:"revision"`
}

type UpstreamEndpointPage struct {
	Items []UpstreamEndpointResponse `json:"items"`
}

type UpstreamEndpointCreateInput struct {
	ID          string               `json:"id"`
	DisplayName string               `json:"displayName"`
	Origin      string               `json:"origin"`
	Kind        UpstreamEndpointKind `json:"kind"`
}

func (handler *Handler) listUpstreamEndpoints(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	items, err := handler.endpoints.List(request.Context())
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonUpstreamEndpointUnavailable)
		return
	}
	page := UpstreamEndpointPage{Items: make([]UpstreamEndpointResponse, len(items))}
	for index, endpoint := range items {
		response, responseErr := upstreamEndpointResponseOf(endpoint)
		if responseErr != nil {
			writeProblem(writer, http.StatusServiceUnavailable, ReasonUpstreamEndpointUnavailable)
			return
		}
		page.Items[index] = response
	}
	writeJSON(writer, http.StatusOK, page)
}

func (handler *Handler) getUpstreamEndpoint(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	id, err := upstreamendpoint.NewID(request.PathValue("endpointId"))
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	endpoint, err := handler.endpoints.Get(request.Context(), id)
	if err != nil {
		spec := classifyUpstreamEndpointError(err)
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	response, err := upstreamEndpointResponseOf(endpoint)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonUpstreamEndpointUnavailable)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) createUpstreamEndpoint(writer http.ResponseWriter, request *http.Request) {
	expected, key, headerErr := mutationHeaders(request)
	body, bodyErr := readJSONBody(request)
	var input UpstreamEndpointCreateInput
	if headerErr != nil || bodyErr != nil || expected != 0 || decodeStrictJSON(body, &input) != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	id, idErr := upstreamendpoint.NewID(input.ID)
	origin, originErr := originidentity.ParseProviderOrigin(input.Origin)
	command, kindErr := upstreamEndpointCreateCommand(id, input.DisplayName, origin, input.Kind)
	if idErr != nil || originErr != nil || kindErr != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := sha256.Sum256(bytes.Join([][]byte{
		[]byte(request.Method), []byte(request.URL.Path), []byte("0"), body,
	}, []byte{0}))
	response, err := handler.idempotent.execute(request.Context(), key, fingerprint, func() cachedResponse {
		endpoint, createErr := handler.endpoints.Create(request.Context(), command)
		if createErr != nil {
			return problemResponse(classifyUpstreamEndpointError(createErr))
		}
		result, responseErr := upstreamEndpointResponseOf(endpoint)
		if responseErr != nil {
			return problemResponse(problemSpec{status: http.StatusServiceUnavailable, reason: ReasonUpstreamEndpointUnavailable})
		}
		return jsonResponse(http.StatusCreated, result)
	})
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonUpstreamEndpointConflict)
		return
	}
	writeCached(writer, response)
}

func upstreamEndpointCreateCommand(
	id upstreamendpoint.ID,
	displayName string,
	origin originidentity.ProviderOrigin,
	kind UpstreamEndpointKind,
) (upstreamendpoint.CreateCommand, error) {
	command := upstreamendpoint.CreateCommand{
		ID: id, DisplayName: displayName, Origin: origin,
		Capabilities: []protocolspec.ProviderCapability{
			protocolspec.ProviderCapabilityMessages,
			protocolspec.ProviderCapabilityStreaming,
			protocolspec.ProviderCapabilityToolCalls,
		},
	}
	switch kind {
	case UpstreamEndpointKindAnthropic:
		command.RealmID = "anthropic.official"
		command.BackendProtocols = []string{"anthropic_messages"}
		command.Drivers = []providerauth.DriverRef{
			providerauth.AnthropicAPIKeyDriverRef(), providerauth.StaticHeaderDriverRef(),
		}
	case UpstreamEndpointKindOpenAICompatible:
		command.RealmID = "openai.platform"
		command.BackendProtocols = []string{"openai_responses", "openai_chat"}
		command.Drivers = []providerauth.DriverRef{providerauth.StaticHeaderDriverRef()}
	default:
		return upstreamendpoint.CreateCommand{}, upstreamendpoint.ErrInvalidEndpoint
	}
	return command, nil
}

func upstreamEndpointResponseOf(endpoint upstreamendpoint.Endpoint) (UpstreamEndpointResponse, error) {
	if endpoint.Validate() != nil {
		return UpstreamEndpointResponse{}, upstreamendpoint.ErrInvalidEndpoint
	}
	return UpstreamEndpointResponse{
		ID: endpoint.ID.String(), DisplayName: endpoint.DisplayName, Origin: endpoint.Origin.String(),
		RealmID: endpoint.RealmID, BackendProtocols: append([]string(nil), endpoint.BackendProtocols...),
		Capabilities: append([]protocolspec.ProviderCapability(nil), endpoint.Capabilities...),
		AccountKinds: endpointAccountKinds(endpoint), State: endpoint.State, Revision: endpoint.Revision,
	}, nil
}

func endpointAccountKinds(endpoint upstreamendpoint.Endpoint) []ProviderAccountKind {
	kinds := make([]ProviderAccountKind, 0, 2)
	if slices.Contains(endpoint.Drivers, providerauth.AnthropicAPIKeyDriverRef()) {
		kinds = append(kinds, ProviderAccountKindAnthropicAPIKey)
	}
	if slices.Contains(endpoint.Drivers, providerauth.StaticHeaderDriverRef()) {
		if endpoint.RealmID == "anthropic.official" {
			kinds = append(kinds, ProviderAccountKindClaudeOAuth)
		} else {
			kinds = append(kinds, ProviderAccountKindOpenAIAPIKey)
		}
	}
	return kinds
}

func classifyUpstreamEndpointError(err error) problemSpec {
	switch {
	case errors.Is(err, upstreamendpoint.ErrEndpointNotFound):
		return problemSpec{status: http.StatusNotFound, reason: ReasonUpstreamEndpointNotFound}
	case errors.Is(err, upstreamendpoint.ErrRevisionConflict):
		return problemSpec{status: http.StatusConflict, reason: ReasonUpstreamEndpointConflict}
	case errors.Is(err, upstreamendpoint.ErrInvalidEndpoint):
		return problemSpec{status: http.StatusUnprocessableEntity, reason: ReasonInvalidRequest}
	default:
		return problemSpec{status: http.StatusServiceUnavailable, reason: ReasonUpstreamEndpointUnavailable}
	}
}
