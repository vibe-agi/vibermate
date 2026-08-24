package desktopcontrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/vibe-agi/vibermate/internal/modelcatalog"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
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

type UpstreamModelCatalogResponse struct {
	EndpointID         string               `json:"endpointId"`
	EndpointRevision   uint64               `json:"endpointRevision"`
	AccountID          string               `json:"accountId"`
	AccountRevision    uint64               `json:"accountRevision"`
	CredentialEpoch    uint64               `json:"credentialEpoch"`
	ObservedAt         string               `json:"observedAt"`
	AvailabilitySource string               `json:"availabilitySource"`
	Models             []modelcatalog.Model `json:"models"`
}

type UpstreamEndpointCreateInput struct {
	ID               string   `json:"id"`
	DisplayName      string   `json:"displayName"`
	Origin           string   `json:"origin"`
	BackendProtocols []string `json:"backendProtocols"`
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

func (handler *Handler) getUpstreamEndpointModels(writer http.ResponseWriter, request *http.Request) {
	values := request.URL.Query()
	if len(values) < 1 || len(values) > 2 {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	accountValues, hasAccount := values["accountId"]
	if !hasAccount || len(accountValues) != 1 {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	accountID, accountErr := provideraccount.NewID(accountValues[0])
	if accountErr != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	refresh := false
	if entries, found := values["refresh"]; found {
		if len(entries) != 1 || entries[0] != "1" {
			writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
			return
		}
		refresh = true
	}
	for key := range values {
		if key != "accountId" && key != "refresh" {
			writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
			return
		}
	}
	id, err := upstreamendpoint.NewID(request.PathValue("endpointId"))
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	snapshot, err := handler.models.Discover(request.Context(), id, accountID, refresh)
	if err != nil {
		spec := classifyModelCatalogError(err)
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	writeJSON(writer, http.StatusOK, UpstreamModelCatalogResponse{
		EndpointID:         snapshot.EndpointID.String(),
		EndpointRevision:   snapshot.EndpointRevision,
		AccountID:          snapshot.AccountID.String(),
		AccountRevision:    snapshot.AccountRevision,
		CredentialEpoch:    snapshot.CredentialEpoch,
		ObservedAt:         snapshot.ObservedAt.Format(time.RFC3339Nano),
		AvailabilitySource: snapshot.AvailabilitySource,
		Models:             snapshot.Models,
	})
}

func classifyModelCatalogError(err error) problemSpec {
	var endpointHTTPError *modelcatalog.EndpointHTTPError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return problemSpec{status: http.StatusGatewayTimeout, reason: ReasonModelCatalogTimeout}
	case errors.Is(err, upstreamendpoint.ErrEndpointNotFound):
		return problemSpec{status: http.StatusNotFound, reason: ReasonUpstreamEndpointNotFound}
	case errors.Is(err, modelcatalog.ErrInvalidCatalog):
		return problemSpec{status: http.StatusUnprocessableEntity, reason: ReasonInvalidRequest}
	case errors.Is(err, provideraccount.ErrAccountNotFound):
		return problemSpec{status: http.StatusNotFound, reason: ReasonProviderAccountNotFound}
	case errors.Is(err, provideraccount.ErrInvalidAccount),
		errors.Is(err, provideraccount.ErrEndpointMismatch),
		errors.Is(err, provideraccount.ErrRealmMismatch):
		return problemSpec{status: http.StatusUnprocessableEntity, reason: ReasonInvalidRequest}
	case errors.Is(err, provideraccount.ErrAccountDisabled),
		errors.Is(err, provideraccount.ErrCredentialMissing):
		return problemSpec{status: http.StatusConflict, reason: ReasonProviderAccountConflict}
	case errors.As(err, &endpointHTTPError) &&
		(endpointHTTPError.StatusCode == http.StatusUnauthorized ||
			endpointHTTPError.StatusCode == http.StatusForbidden):
		return problemSpec{status: http.StatusBadGateway, reason: ReasonModelCatalogAuthenticationRejected}
	default:
		return problemSpec{status: http.StatusServiceUnavailable, reason: ReasonModelCatalogUnavailable}
	}
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
	command, protocolErr := upstreamEndpointCreateCommand(
		id,
		input.DisplayName,
		origin,
		input.BackendProtocols,
	)
	if idErr != nil || originErr != nil || protocolErr != nil {
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
	backendProtocols []string,
) (upstreamendpoint.CreateCommand, error) {
	const (
		anthropicMessages = "anthropic_messages"
		openAIResponses   = "openai_responses"
		openAIChat        = "openai_chat"
	)
	if len(backendProtocols) == 0 || len(backendProtocols) > 3 {
		return upstreamendpoint.CreateCommand{}, upstreamendpoint.ErrInvalidEndpoint
	}
	seen := make(map[string]struct{}, len(backendProtocols))
	for _, protocol := range backendProtocols {
		switch protocol {
		case anthropicMessages, openAIResponses, openAIChat:
		default:
			return upstreamendpoint.CreateCommand{}, upstreamendpoint.ErrInvalidEndpoint
		}
		if _, duplicate := seen[protocol]; duplicate {
			return upstreamendpoint.CreateCommand{}, upstreamendpoint.ErrInvalidEndpoint
		}
		seen[protocol] = struct{}{}
	}
	protocols := make([]string, 0, len(seen))
	for _, protocol := range []string{anthropicMessages, openAIResponses, openAIChat} {
		if _, selected := seen[protocol]; selected {
			protocols = append(protocols, protocol)
		}
	}
	drivers := []providerauth.DriverRef{providerauth.StaticHeaderDriverRef()}
	if _, supportsAnthropic := seen[anthropicMessages]; supportsAnthropic {
		drivers = append([]providerauth.DriverRef{providerauth.AnthropicAPIKeyDriverRef()}, drivers...)
	}
	command := upstreamendpoint.CreateCommand{
		ID: id, DisplayName: displayName, Origin: origin,
		RealmID: id.String(), BackendProtocols: protocols, Drivers: drivers,
		Capabilities: []protocolspec.ProviderCapability{
			protocolspec.ProviderCapabilityMessages,
			protocolspec.ProviderCapabilityStreaming,
			protocolspec.ProviderCapabilityToolCalls,
		},
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
		kinds = append(kinds, ProviderAccountKindBearerToken)
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
