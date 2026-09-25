package desktopcontrol

import (
	"net/http"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

type environmentDryRunInput struct {
	Source         string               `json:"source"`
	Revision       environment.Revision `json:"revision"`
	ClientOrigin   string               `json:"clientOrigin"`
	Method         string               `json:"method"`
	Path           string               `json:"path"`
	RawQuery       string               `json:"rawQuery,omitempty"`
	ClientProtocol string               `json:"clientProtocol"`
	Body           string               `json:"body"`
}

type environmentDryRunResponse struct {
	Schema            string                `json:"schema"`
	Source            string                `json:"source"`
	PublishedRevision environment.Revision  `json:"publishedRevision"`
	DraftRevision     environment.Revision  `json:"draftRevision,omitempty"`
	Result            exchange.DryRunResult `json:"result"`
}

func (handler *Handler) dryRunEnvironment(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	id, idErr := environment.NewEnvironmentID(request.PathValue("environmentId"))
	body, bodyErr := readJSONBody(request)
	var input environmentDryRunInput
	if idErr != nil || bodyErr != nil || decodeStrictJSON(body, &input) != nil ||
		(input.Source != "published" && input.Source != "draft") || input.Revision == 0 ||
		!wireprofile.ApplicationProtocol(input.ClientProtocol).Valid() || input.Body == "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	origin, err := originidentity.ParseClientOrigin(input.ClientOrigin)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	var snapshot environment.EnvironmentSnapshot
	var publishedRevision environment.Revision
	if input.Source == "draft" {
		var draft environment.Draft
		draft, err = handler.environments.GetDraft(request.Context(), id)
		if err == nil && draft.Revision != input.Revision {
			err = environment.ErrRevisionConflict
		}
		if err == nil {
			snapshot, err = handler.environments.GetDraftSnapshot(request.Context(), id, input.Revision)
			publishedRevision = draft.BaseRevision
		}
	} else {
		snapshot, err = handler.environments.GetRevision(request.Context(), id, input.Revision)
		publishedRevision = input.Revision
	}
	if err != nil {
		spec := classifyEnvironmentError(err)
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	if snapshot.State() != environment.StateActive {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonDryRunEnvironmentDisabled)
		return
	}
	plan, err := snapshot.ResolveRequest(origin, environment.RequestFacts{
		Target: protocolspec.RequestTarget{
			Method: input.Method, Path: input.Path, RawQuery: input.RawQuery,
			Transport: protocolspec.ClientOperationTransportHTTP,
		},
		DownstreamProtocol: wireprofile.ApplicationProtocol(input.ClientProtocol),
	})
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonDryRunFlowNotMatched)
		return
	}
	operationPlan := plan.Operation()
	operation, err := exchange.NewClientOperationEvidence(
		operationPlan.ID(), operationPlan.Revision(), input.Method, input.Path, input.RawQuery,
	)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonDryRunInputInvalid)
		return
	}
	var requestOptions []exchange.ClientRequestOption
	if plan.PreservesOriginalDestination() {
		// No client credential or observed wire Headers enter this preview.
		requestOptions = append(requestOptions, exchange.WithOriginalHeaders(http.Header{}))
	}
	clientRequest, err := exchange.NewClientRequest(
		"configuration-dry-run", plan, operation, []byte(input.Body),
		exchange.ReplayClass(operationPlan.ReplayClass()),
		wireprofile.ApplicationProtocol(input.ClientProtocol),
		requestOptions...,
	)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonDryRunInputInvalid)
		return
	}
	result, err := handler.dryRun(request.Context(), clientRequest)
	if err != nil {
		reason := ReasonInvalidRequest
		switch exchange.ReasonOf(err) {
		case exchange.ReasonAccountSelectorFailed:
			reason = ReasonDryRunSelectorFailed
		case exchange.ReasonMessageTransformFailed:
			reason = ReasonDryRunTransformFailed
		case exchange.ReasonInvalidExchangeRequest, exchange.ReasonUnsupportedClientInput:
			reason = ReasonDryRunInputInvalid
		}
		writeProblem(writer, http.StatusUnprocessableEntity, reason)
		return
	}
	response := environmentDryRunResponse{
		Schema: "vibermate-environment-dry-run-v1",
		Source: input.Source, PublishedRevision: publishedRevision, Result: result,
	}
	if input.Source == "draft" {
		response.DraftRevision = input.Revision
	}
	writeJSON(writer, http.StatusOK, response)
}
