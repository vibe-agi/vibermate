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
	ID                environment.EnvironmentID           `json:"id"`
	Name              string                              `json:"name"`
	State             environment.State                   `json:"state"`
	Revision          environment.Revision                `json:"revision"`
	Digest            string                              `json:"digest"`
	SystemOwned       bool                                `json:"systemOwned"`
	ClientEndpoints   []environment.ClientEndpoint        `json:"clientEndpoints"`
	PluginBindings    []environment.PluginBinding         `json:"pluginBindings"`
	BudgetPolicy      environment.BudgetPolicy            `json:"budgetPolicy"`
	ContentRecording  environment.ContentRecordingPolicy  `json:"contentRecording"`
	LaunchEnvironment environment.LaunchEnvironmentPolicy `json:"launchEnvironment"`
	PolicySet         environment.PolicySet               `json:"policySet"`
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
	ContentRecording      environment.ContentRecordingPolicy  `json:"contentRecording"`
	LaunchEnvironment     environment.LaunchEnvironmentPolicy `json:"launchEnvironment"`
	PolicySet             *environment.PolicySet              `json:"policySet,omitempty"`
}

type EnvironmentImpactResponse struct {
	EnvironmentID      environment.EnvironmentID          `json:"environmentId"`
	BaseRevision       environment.Revision               `json:"baseRevision"`
	DraftRevision      environment.Revision               `json:"draftRevision"`
	CandidateDigest    string                             `json:"candidateDigest"`
	ContinuingCaptures []EnvironmentImpactCaptureResponse `json:"continuingCaptures"`
}

type EnvironmentImpactCaptureResponse struct {
	CaptureKind string `json:"captureKind"`
	CaptureID   string `json:"captureId"`
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
	return EnvironmentResponse{
		ID: aggregate.ID, Name: aggregate.Name, State: aggregate.State,
		Revision: aggregate.Revision, Digest: digest,
		SystemOwned:       systemOwned,
		ClientEndpoints:   environmentControlEndpoints(aggregate.ClientEndpoints),
		PluginBindings:    controlCollection(aggregate.PluginBindings),
		BudgetPolicy:      aggregate.BudgetPolicy,
		ContentRecording:  aggregate.ContentRecording,
		LaunchEnvironment: aggregate.LaunchEnvironment.Clone(),
		PolicySet:         aggregate.EffectivePolicySet(),
	}
}

// Environment owns several nested collections whose in-process zero value is
// nil. The Control API has a stricter wire contract: a collection is always an
// array/object, including when empty. Build that boundary representation here
// without changing the immutable aggregate or its candidate digest.
func environmentControlEndpoints(
	values []environment.ClientEndpoint,
) []environment.ClientEndpoint {
	endpoints := make([]environment.ClientEndpoint, len(values))
	for endpointIndex, sourceEndpoint := range values {
		endpoint := sourceEndpoint
		endpoint.ProtocolPlans = make(
			[]environment.ClientProtocolPlan,
			len(sourceEndpoint.ProtocolPlans),
		)
		for planIndex, sourcePlan := range sourceEndpoint.ProtocolPlans {
			plan := sourcePlan
			plan.PluginBindings = controlCollection(sourcePlan.PluginBindings)
			if sourcePlan.Destination.Upstream != nil {
				sourceUpstream := sourcePlan.Destination.Upstream
				upstream := *sourceUpstream
				upstream.RouteSet.CandidateRouteIDs = controlCollection(
					sourceUpstream.RouteSet.CandidateRouteIDs,
				)
				upstream.Routes = make(
					[]environment.UpstreamRoute,
					len(sourceUpstream.Routes),
				)
				for routeIndex, sourceRoute := range sourceUpstream.Routes {
					route := sourceRoute
					route.ProviderTarget.Capabilities = controlCollection(
						sourceRoute.ProviderTarget.Capabilities,
					)
					route.AccountPolicy.CandidateAccountIDs = controlCollection(
						sourceRoute.AccountPolicy.CandidateAccountIDs,
					)
					route.AccountPolicy.AccountRevisions = make(
						map[string]environment.Revision,
						len(sourceRoute.AccountPolicy.AccountRevisions),
					)
					for accountID, revision := range sourceRoute.AccountPolicy.AccountRevisions {
						route.AccountPolicy.AccountRevisions[accountID] = revision
					}
					route.ModelPolicy.Mappings = controlCollection(
						sourceRoute.ModelPolicy.Mappings,
					)
					route.PluginBindings = controlCollection(sourceRoute.PluginBindings)
					upstream.Routes[routeIndex] = route
				}
				plan.Destination.Upstream = &upstream
			}
			endpoint.ProtocolPlans[planIndex] = plan
		}
		endpoints[endpointIndex] = endpoint
	}
	return endpoints
}

func controlCollection[T any](values []T) []T {
	result := make([]T, len(values))
	copy(result, values)
	return result
}

func draftResponseOf(draft environment.Draft) EnvironmentDraftResponse {
	return EnvironmentDraftResponse{
		EnvironmentID: draft.EnvironmentID, BaseRevision: draft.BaseRevision,
		DraftRevision: draft.Revision, CandidateDigest: draft.CandidateDigest.String(),
		Candidate: environmentResponseOfAggregate(draft.Candidate, draft.CandidateDigest.String(), false),
	}
}

func impactResponseOf(preview environment.ImpactPreview) EnvironmentImpactResponse {
	continuing := make([]EnvironmentImpactCaptureResponse, len(preview.ContinuingCaptures))
	for index, item := range preview.ContinuingCaptures {
		continuing[index] = EnvironmentImpactCaptureResponse{
			CaptureKind: string(item.Capture.Kind), CaptureID: item.Capture.ID,
		}
	}
	return EnvironmentImpactResponse{
		EnvironmentID: preview.EnvironmentID, BaseRevision: preview.BaseRevision,
		DraftRevision: preview.DraftRevision, CandidateDigest: preview.CandidateDigest.String(),
		ContinuingCaptures: continuing,
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
			ContentRecording:  input.ContentRecording,
			LaunchEnvironment: input.LaunchEnvironment.Clone(),
			PolicySet:         input.PolicySet,
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
