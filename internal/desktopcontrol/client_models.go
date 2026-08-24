package desktopcontrol

import (
	"errors"
	"net/http"
	"strings"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/modelcatalog"
)

// ClientModelResponse is descriptive request-side metadata. ID is the exact
// model value written by the Agent protocol; CanonicalID remains the models.dev
// identity used only for attribution and metadata refreshes.
type ClientModelResponse struct {
	ID               string   `json:"id"`
	CanonicalID      string   `json:"canonicalId"`
	DisplayName      string   `json:"displayName"`
	Description      string   `json:"description"`
	Family           string   `json:"family"`
	Reasoning        bool     `json:"reasoning"`
	ToolCalls        bool     `json:"toolCalls"`
	StructuredOutput bool     `json:"structuredOutput"`
	Attachments      bool     `json:"attachments"`
	OpenWeights      bool     `json:"openWeights"`
	ContextLimit     int64    `json:"contextLimit"`
	OutputLimit      int64    `json:"outputLimit"`
	InputModalities  []string `json:"inputModalities"`
	OutputModalities []string `json:"outputModalities"`
	KnowledgeCutoff  string   `json:"knowledgeCutoff"`
	ReleaseDate      string   `json:"releaseDate"`
}

type ClientModelCatalogResponse struct {
	Protocol       string                `json:"protocol"`
	ProviderID     string                `json:"providerId"`
	MetadataSource string                `json:"metadataSource"`
	Models         []ClientModelResponse `json:"models"`
}

func (handler *Handler) getClientModels(writer http.ResponseWriter, request *http.Request) {
	values := request.URL.Query()
	protocolValues, found := values["protocol"]
	if len(values) != 1 || !found || len(protocolValues) != 1 {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	protocol := environment.ClientProtocol(protocolValues[0])
	providerID, ok := clientModelProvider(protocol)
	if !ok {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	items, err := handler.clientModels.ListProvider(request.Context(), providerID)
	if err != nil {
		spec := classifyClientModelCatalogError(err)
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	prefix := providerID + "/"
	models := make([]ClientModelResponse, len(items))
	for index, item := range items {
		if !strings.HasPrefix(item.CanonicalID, prefix) || len(item.CanonicalID) == len(prefix) {
			writeProblem(writer, http.StatusServiceUnavailable, ReasonModelCatalogUnavailable)
			return
		}
		models[index] = clientModelResponseOf(strings.TrimPrefix(item.CanonicalID, prefix), item)
	}
	writeJSON(writer, http.StatusOK, ClientModelCatalogResponse{
		Protocol:       string(protocol),
		ProviderID:     providerID,
		MetadataSource: modelcatalog.MetadataSourceModelsDev,
		Models:         models,
	})
}

func clientModelProvider(protocol environment.ClientProtocol) (string, bool) {
	switch protocol {
	case environment.ClientProtocolAnthropicMessages:
		return "anthropic", true
	case environment.ClientProtocolOpenAIResponses, environment.ClientProtocolOpenAIChat:
		return "openai", true
	default:
		return "", false
	}
}

func clientModelResponseOf(id string, item modelcatalog.Metadata) ClientModelResponse {
	return ClientModelResponse{
		ID: id, CanonicalID: item.CanonicalID, DisplayName: item.DisplayName,
		Description: item.Description, Family: item.Family, Reasoning: item.Reasoning,
		ToolCalls: item.ToolCalls, StructuredOutput: item.StructuredOutput,
		Attachments: item.Attachments, OpenWeights: item.OpenWeights,
		ContextLimit: item.ContextLimit, OutputLimit: item.OutputLimit,
		InputModalities:  append([]string(nil), item.InputModalities...),
		OutputModalities: append([]string(nil), item.OutputModalities...),
		KnowledgeCutoff:  item.KnowledgeCutoff, ReleaseDate: item.ReleaseDate,
	}
}

func classifyClientModelCatalogError(err error) problemSpec {
	if errors.Is(err, modelcatalog.ErrInvalidCatalog) {
		return problemSpec{status: http.StatusServiceUnavailable, reason: ReasonModelCatalogUnavailable}
	}
	return problemSpec{status: http.StatusServiceUnavailable, reason: ReasonModelCatalogUnavailable}
}
