package desktopcontrol

import (
	"bytes"
	"crypto/sha256"
	"net/http"
	"strconv"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/environment"
)

type EnvironmentListResponse struct {
	Items []EnvironmentResponse `json:"items"`
}

type EnvironmentResponse struct {
	ID               environment.EnvironmentID           `json:"id"`
	Name             string                              `json:"name"`
	State            environment.State                   `json:"state"`
	Revision         environment.Revision                `json:"revision"`
	Digest           string                              `json:"digest"`
	SystemOwned      bool                                `json:"systemOwned"`
	ClientEndpoints  []environment.ClientEndpoint        `json:"clientEndpoints"`
	PluginBindings   []environment.PluginBinding         `json:"pluginBindings"`
	BudgetPolicy     environment.BudgetPolicy            `json:"budgetPolicy"`
	EgressPolicy     environment.EnvironmentEgressPolicy `json:"egressPolicy"`
	ContentRecording environment.ContentRecordingPolicy  `json:"contentRecording"`
	PolicySet        environment.PolicySet               `json:"policySet"`
}

type EnvironmentDraftResponse struct {
	EnvironmentID   environment.EnvironmentID `json:"environmentId"`
	BaseRevision    environment.Revision      `json:"baseRevision"`
	DraftRevision   environment.Revision      `json:"draftRevision"`
	CandidateDigest string                    `json:"candidateDigest"`
	Candidate       EnvironmentResponse       `json:"candidate"`
}

type EnvironmentDraftInput struct {
	ExpectedDraftRevision environment.Revision                `json:"expectedDraftRevision"`
	Name                  string                              `json:"name"`
	State                 environment.State                   `json:"state"`
	ClientEndpoints       []environment.ClientEndpoint        `json:"clientEndpoints"`
	PluginBindings        []environment.PluginBinding         `json:"pluginBindings"`
	BudgetPolicy          environment.BudgetPolicy            `json:"budgetPolicy"`
	EgressPolicy          environment.EnvironmentEgressPolicy `json:"egressPolicy"`
	ContentRecording      environment.ContentRecordingPolicy  `json:"contentRecording"`
	PolicySet             *environment.PolicySet              `json:"policySet,omitempty"`
}

type EnvironmentImpactResponse struct {
	EnvironmentID          environment.EnvironmentID               `json:"environmentId"`
	BaseRevision           environment.Revision                    `json:"baseRevision"`
	DraftRevision          environment.Revision                    `json:"draftRevision"`
	CandidateDigest        string                                  `json:"candidateDigest"`
	Classification         environment.CompatibilityClassification `json:"classification"`
	HotSwitchCount         int                                     `json:"hotSwitchCount"`
	ReconnectRequiredCount int                                     `json:"reconnectRequiredCount"`
	RestartRequiredCount   int                                     `json:"restartRequiredCount"`
	Affected               []EnvironmentImpactCaptureResponse      `json:"affected"`
}

type EnvironmentImpactCaptureResponse struct {
	CaptureKind    string                                  `json:"captureKind"`
	CaptureID      string                                  `json:"captureId"`
	Classification environment.CompatibilityClassification `json:"classification"`
}

type EnvironmentPublishResponse struct {
	Outcome     environment.CommitOutcome `json:"outcome"`
	Environment EnvironmentResponse       `json:"environment"`
	Impact      EnvironmentImpactResponse `json:"impact"`
}

func environmentResponseOf(snapshot environment.EnvironmentSnapshot) EnvironmentResponse {
	aggregate := snapshot.Aggregate()
	return environmentResponseOfAggregate(aggregate, snapshot.Digest().String(), snapshot.SystemOwned())
}

func environmentResponseOfAggregate(aggregate environment.Environment, digest string, systemOwned bool) EnvironmentResponse {
	clientEndpoints := make([]environment.ClientEndpoint, len(aggregate.ClientEndpoints))
	copy(clientEndpoints, aggregate.ClientEndpoints)
	pluginBindings := make([]environment.PluginBinding, len(aggregate.PluginBindings))
	copy(pluginBindings, aggregate.PluginBindings)
	return EnvironmentResponse{
		ID: aggregate.ID, Name: aggregate.Name, State: aggregate.State,
		Revision: aggregate.Revision, Digest: digest,
		SystemOwned: systemOwned, ClientEndpoints: clientEndpoints,
		PluginBindings: pluginBindings, BudgetPolicy: aggregate.BudgetPolicy,
		EgressPolicy: aggregate.EgressPolicy, ContentRecording: aggregate.ContentRecording,
		PolicySet: aggregate.EffectivePolicySet(),
	}
}

func draftResponseOf(draft environment.Draft) EnvironmentDraftResponse {
	return EnvironmentDraftResponse{
		EnvironmentID: draft.EnvironmentID, BaseRevision: draft.BaseRevision,
		DraftRevision: draft.Revision, CandidateDigest: draft.CandidateDigest.String(),
		Candidate: environmentResponseOfAggregate(draft.Candidate, draft.CandidateDigest.String(), false),
	}
}

func impactResponseOf(preview environment.ImpactPreview) EnvironmentImpactResponse {
	affected := make([]EnvironmentImpactCaptureResponse, len(preview.Affected))
	for index, item := range preview.Affected {
		affected[index] = EnvironmentImpactCaptureResponse{
			CaptureKind: string(item.Capture.Capture.Kind), CaptureID: item.Capture.Capture.ID,
			Classification: item.Classification,
		}
	}
	return EnvironmentImpactResponse{
		EnvironmentID: preview.EnvironmentID, BaseRevision: preview.BaseRevision,
		DraftRevision: preview.DraftRevision, CandidateDigest: preview.CandidateDigest.String(),
		Classification: preview.Classification, HotSwitchCount: preview.HotSwitchCount,
		ReconnectRequiredCount: preview.ReconnectRequiredCount,
		RestartRequiredCount:   preview.RestartRequiredCount, Affected: affected,
	}
}

func (handler *Handler) listEnvironments(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	snapshots, err := handler.environments.List(request.Context())
	if err != nil {
		spec := classifyEnvironmentError(err)
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	response := EnvironmentListResponse{Items: make([]EnvironmentResponse, len(snapshots))}
	for index, snapshot := range snapshots {
		response.Items[index] = environmentResponseOf(snapshot)
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) getEnvironment(writer http.ResponseWriter, request *http.Request) {
	handler.getEnvironmentAtRevision(writer, request, 0)
}

func (handler *Handler) getEnvironmentRevision(writer http.ResponseWriter, request *http.Request) {
	revision, err := strconv.ParseUint(request.PathValue("environmentRevision"), 10, 64)
	if err != nil || revision == 0 || revision > uint64(environment.MaxRevision) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	handler.getEnvironmentAtRevision(writer, request, environment.Revision(revision))
}

func (handler *Handler) getEnvironmentAtRevision(writer http.ResponseWriter, request *http.Request, revision environment.Revision) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	id, err := environment.NewEnvironmentID(request.PathValue("environmentId"))
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	var snapshot environment.EnvironmentSnapshot
	if revision == 0 {
		snapshot, err = handler.environments.Get(request.Context(), id)
	} else {
		snapshot, err = handler.environments.GetRevision(request.Context(), id, revision)
	}
	if err != nil {
		spec := classifyEnvironmentError(err)
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	writer.Header().Set("ETag", strconv.Quote("revision-"+strconv.FormatUint(uint64(snapshot.Revision()), 10)))
	writeJSON(writer, http.StatusOK, environmentResponseOf(snapshot))
}

func (handler *Handler) getEnvironmentDraft(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	id, err := environment.NewEnvironmentID(request.PathValue("environmentId"))
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	draft, err := handler.environments.GetDraft(request.Context(), id)
	if err != nil {
		spec := classifyEnvironmentError(err)
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	writer.Header().Set("ETag", strconv.Quote("draft-"+strconv.FormatUint(uint64(draft.Revision), 10)))
	writeJSON(writer, http.StatusOK, draftResponseOf(draft))
}

func (handler *Handler) putEnvironmentDraft(writer http.ResponseWriter, request *http.Request) {
	expectedBase, key, err := mutationHeaders(request)
	body, bodyErr := readJSONBody(request)
	if err != nil || bodyErr != nil || expectedBase >= uint64(environment.MaxRevision) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	id, idErr := environment.NewEnvironmentID(request.PathValue("environmentId"))
	var input EnvironmentDraftInput
	if idErr != nil || decodeStrictJSON(body, &input) != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := sha256.Sum256(bytes.Join([][]byte{[]byte(request.Method), []byte(request.URL.Path), []byte(strconv.FormatUint(expectedBase, 10)), body}, []byte{0}))
	response, err := handler.idempotent.execute(request.Context(), key, fingerprint, func() cachedResponse {
		candidate := environment.Environment{
			ID: id, Name: input.Name, State: input.State,
			Revision: environment.Revision(expectedBase + 1), ClientEndpoints: input.ClientEndpoints,
			PluginBindings: input.PluginBindings, BudgetPolicy: input.BudgetPolicy,
			EgressPolicy: input.EgressPolicy, ContentRecording: input.ContentRecording,
			PolicySet: input.PolicySet,
		}
		draft, saveErr := handler.environments.SaveDraft(request.Context(), environment.DraftCommand{
			ExpectedBaseRevision:  environment.Revision(expectedBase),
			ExpectedDraftRevision: input.ExpectedDraftRevision, Candidate: candidate,
		})
		if saveErr != nil {
			return problemResponse(classifyEnvironmentError(saveErr))
		}
		return jsonResponse(http.StatusOK, draftResponseOf(draft))
	})
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonRevisionConflict)
		return
	}
	writeCached(writer, response)
}

func (handler *Handler) previewEnvironmentDraft(writer http.ResponseWriter, request *http.Request) {
	handler.environmentDraftAction(writer, request, false)
}

func (handler *Handler) publishEnvironmentDraft(writer http.ResponseWriter, request *http.Request) {
	handler.environmentDraftAction(writer, request, true)
}

func (handler *Handler) environmentDraftAction(writer http.ResponseWriter, request *http.Request, publish bool) {
	draftRevision, key, err := mutationHeaders(request)
	if err != nil || draftRevision == 0 || draftRevision > uint64(environment.MaxRevision) || !emptyBody(request.Body) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	id, idErr := environment.NewEnvironmentID(request.PathValue("environmentId"))
	if idErr != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := sha256.Sum256([]byte(request.Method + "\x00" + request.URL.Path + "\x00" + strconv.FormatUint(draftRevision, 10)))
	response, err := handler.idempotent.execute(request.Context(), key, fingerprint, func() cachedResponse {
		preview, previewErr := handler.environments.Preview(request.Context(), id, environment.Revision(draftRevision))
		if previewErr != nil {
			return problemResponse(classifyEnvironmentError(previewErr))
		}
		if !publish {
			return jsonResponse(http.StatusOK, impactResponseOf(preview))
		}
		result, publishErr := handler.environments.Publish(request.Context(), preview)
		if publishErr != nil {
			return problemResponse(classifyEnvironmentError(publishErr))
		}
		snapshot, getErr := handler.environments.GetRevision(request.Context(), id, result.ActualRevision)
		if getErr != nil {
			return problemResponse(classifyEnvironmentError(getErr))
		}
		handler.recordActivity(request.Context(), activity.Event{
			Kind: activity.KindEnvironmentApplied, EnvironmentID: snapshot.ID(),
			EnvironmentRevision: snapshot.Revision(), EnvironmentDigest: snapshot.Digest().String(),
			SubjectID: snapshot.ID().String(), Status: activity.StatusSucceeded,
		})
		return jsonResponse(http.StatusOK, EnvironmentPublishResponse{
			Outcome: result.Outcome, Environment: environmentResponseOf(snapshot), Impact: impactResponseOf(preview),
		})
	})
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonRevisionConflict)
		return
	}
	writeCached(writer, response)
}
