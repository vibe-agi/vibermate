package desktopcontrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/codelibrary"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
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

type EnvironmentAccountActivationResponse struct {
	Environment         EnvironmentResponse         `json:"environment"`
	RouteID             environment.UpstreamRouteID `json:"routeId"`
	AccountID           string                      `json:"accountId"`
	RunningCaptureCount int                         `json:"runningCaptureCount"`
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
			plan.Transforms = controlCollection(sourcePlan.Transforms)
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
					route.AccountPolicy.Accounts = controlCollection(
						sourceRoute.AccountPolicy.Accounts,
					)
					if sourceRoute.AccountPolicy.Selector != nil {
						selector := *sourceRoute.AccountPolicy.Selector
						route.AccountPolicy.Selector = &selector
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
		if resolveErr := handler.resolvePublishedEgressProfiles(request.Context(), input.ClientEndpoints); resolveErr != nil {
			return problemResponse(classifyEgressProfileError(resolveErr))
		}
		if resolveErr := handler.resolvePublishedTransforms(request.Context(), input.ClientEndpoints); resolveErr != nil {
			return problemResponse(classifyCodeLibraryError(resolveErr))
		}
		if resolveErr := handler.resolvePublishedAccountPolicies(request.Context(), input.ClientEndpoints); resolveErr != nil {
			return problemResponse(classifyEnvironmentAccountPolicyError(resolveErr))
		}
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

func (handler *Handler) resolvePublishedEgressProfiles(
	ctx context.Context,
	endpoints []environment.ClientEndpoint,
) error {
	direct := egressprofile.Direct()
	for endpointIndex := range endpoints {
		for planIndex := range endpoints[endpointIndex].ProtocolPlans {
			profile := &endpoints[endpointIndex].ProtocolPlans[planIndex].EgressProfile
			if profile.Equal(direct) {
				*profile = direct
				continue
			}
			if handler.egressProfiles == nil {
				return egressprofile.ErrInvalidProfile
			}
			published, err := handler.egressProfiles.GetRevision(
				ctx, profile.ID, profile.Revision,
			)
			if err != nil {
				return err
			}
			if !published.Equal(*profile) {
				return egressprofile.ErrInvalidProfile
			}
			*profile = published
		}
	}
	return nil
}

func (handler *Handler) resolvePublishedTransforms(
	ctx context.Context,
	endpoints []environment.ClientEndpoint,
) error {
	for endpointIndex := range endpoints {
		for planIndex := range endpoints[endpointIndex].ProtocolPlans {
			transforms := endpoints[endpointIndex].ProtocolPlans[planIndex].Transforms
			if len(transforms) != 0 && handler.codeLibrary == nil {
				return codelibrary.ErrInvalidLibrary
			}
			for transformIndex, submitted := range transforms {
				published, err := handler.codeLibrary.GetTransformRevision(
					ctx, submitted.ID, submitted.Revision,
				)
				if err != nil {
					return err
				}
				if !published.Equal(submitted) {
					return codelibrary.ErrInvalidLibrary
				}
				transforms[transformIndex] = published
			}
		}
	}
	return nil
}

func (handler *Handler) resolvePublishedAccountPolicies(
	ctx context.Context,
	endpoints []environment.ClientEndpoint,
) error {
	var accounts map[string]provideraccount.View
	loadAccounts := func() error {
		if accounts != nil {
			return nil
		}
		if handler.accounts == nil {
			return provideraccount.ErrInvalidAccount
		}
		views, err := handler.accounts.List(ctx)
		if err != nil {
			return err
		}
		accounts = make(map[string]provideraccount.View, len(views))
		for _, view := range views {
			accounts[view.Account.ID.String()] = view
		}
		return nil
	}
	for endpointIndex := range endpoints {
		for planIndex := range endpoints[endpointIndex].ProtocolPlans {
			upstream := endpoints[endpointIndex].ProtocolPlans[planIndex].Destination.Upstream
			if upstream == nil {
				continue
			}
			if err := loadAccounts(); err != nil {
				return err
			}
			for routeIndex := range upstream.Routes {
				route := &upstream.Routes[routeIndex]
				policy := &route.AccountPolicy
				if len(policy.Accounts) == 0 {
					return provideraccount.ErrInvalidAccount
				}
				for index, selected := range policy.Accounts {
					view, found := accounts[selected.ID]
					if !found {
						return provideraccount.ErrAccountNotFound
					}
					if err := routeAccountMembershipError(view, route.ProviderTarget); err != nil {
						return err
					}
					policy.Accounts[index] = environment.RouteAccountReference{
						ID: view.Account.ID.String(), Revision: environment.Revision(view.Account.Revision),
						DisplayName: view.Account.DisplayName,
					}
				}
				switch policy.Mode {
				case environment.AccountSelectionFixed:
					if policy.FixedAccountID == "" || policy.Selector != nil {
						return environment.ErrInvalidEnvironment
					}
				case environment.AccountSelectionJavaScript:
					if policy.FixedAccountID != "" || policy.Selector == nil || handler.codeLibrary == nil {
						return codelibrary.ErrInvalidLibrary
					}
					published, err := handler.codeLibrary.GetAccountSelectorRevision(
						ctx,
						policy.Selector.ID,
						policy.Selector.Revision,
					)
					if err != nil {
						return err
					}
					if !published.Equal(*policy.Selector) {
						return codelibrary.ErrInvalidLibrary
					}
					selector := published
					policy.Selector = &selector
					sort.Slice(policy.Accounts, func(left, right int) bool {
						return policy.Accounts[left].ID < policy.Accounts[right].ID
					})
				default:
					return environment.ErrInvalidEnvironment
				}
			}
		}
	}
	return nil
}

// routeAccountMembershipError is the structural rule for a Route Account Set:
// the Account exists for this exact Endpoint origin and is linked to it.
// Disabled state and credential health are runtime facts. They never shrink
// an explicit set or block an unrelated configuration edit; a lease on such an
// Account fails explicitly when a request actually selects it.
func routeAccountMembershipError(view provideraccount.View, target environment.ProviderTarget) error {
	account := view.Account
	if account.Origin != target.Origin ||
		!account.Associations.Contains(upstreamendpoint.ID(target.ID)) {
		return provideraccount.ErrEndpointMismatch
	}
	return nil
}

// routeAccountError is the stricter rule for making an Account the active
// choice now: it must also be enabled and hold a ready credential.
func routeAccountError(view provideraccount.View, target environment.ProviderTarget) error {
	if err := routeAccountMembershipError(view, target); err != nil {
		return err
	}
	if view.Account.State != provideraccount.StateActive {
		return provideraccount.ErrAccountDisabled
	}
	if view.Health.State != provideraccount.HealthReady {
		return provideraccount.ErrCredentialMissing
	}
	return nil
}

func classifyEnvironmentAccountPolicyError(err error) problemSpec {
	switch {
	case errors.Is(err, codelibrary.ErrInvalidLibrary),
		errors.Is(err, codelibrary.ErrCollectionNotFound),
		errors.Is(err, codelibrary.ErrSelectorNotFound),
		errors.Is(err, codelibrary.ErrRevisionConflict):
		return classifyCodeLibraryError(err)
	case errors.Is(err, provideraccount.ErrInvalidAccount),
		errors.Is(err, provideraccount.ErrAccountNotFound),
		errors.Is(err, provideraccount.ErrEndpointMismatch),
		errors.Is(err, provideraccount.ErrAccountDisabled),
		errors.Is(err, provideraccount.ErrCredentialMissing),
		errors.Is(err, provideraccount.ErrManagerClosing):
		return classifyProviderAccountError(err)
	default:
		return classifyEnvironmentError(err)
	}
}

func (handler *Handler) previewEnvironmentDraft(writer http.ResponseWriter, request *http.Request) {
	handler.environmentDraftAction(writer, request, false)
}

func (handler *Handler) publishEnvironmentDraft(writer http.ResponseWriter, request *http.Request) {
	handler.environmentDraftAction(writer, request, true)
}

func (handler *Handler) activateEnvironmentRouteAccount(writer http.ResponseWriter, request *http.Request) {
	expected, key, headerErr := mutationHeaders(request)
	body, bodyErr := readJSONBody(request)
	var input struct {
		AccountID *string `json:"accountId"`
	}
	id, idErr := environment.NewEnvironmentID(request.PathValue("environmentId"))
	routeID, routeErr := environment.NewUpstreamRouteID(request.PathValue("routeId"))
	if headerErr != nil || bodyErr != nil || idErr != nil || routeErr != nil ||
		expected == 0 || expected >= uint64(environment.MaxRevision) ||
		request.URL.RawQuery != "" || decodeStrictJSON(body, &input) != nil || input.AccountID == nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	accountID, err := provideraccount.NewID(*input.AccountID)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := sha256.Sum256(bytes.Join([][]byte{
		[]byte(request.Method), []byte(request.URL.Path), []byte(strconv.FormatUint(expected, 10)), body,
	}, []byte{0}))
	response, err := handler.idempotent.execute(request.Context(), key, fingerprint, func() cachedResponse {
		account, accountErr := handler.accounts.Get(request.Context(), accountID)
		if accountErr != nil {
			return problemResponse(classifyEnvironmentAccountPolicyError(accountErr))
		}
		current, getErr := handler.environments.Get(request.Context(), id)
		if getErr != nil {
			return problemResponse(classifyEnvironmentError(getErr))
		}
		if current.Revision() != environment.Revision(expected) {
			return problemResponse(problemSpec{status: http.StatusConflict, reason: ReasonRevisionConflict})
		}
		route, exists := current.FixedRoute(routeID)
		if !exists {
			return problemResponse(classifyEnvironmentError(environment.ErrInvalidEnvironment))
		}
		if accountErr := routeAccountError(account, route.ProviderTarget()); accountErr != nil {
			return problemResponse(classifyEnvironmentAccountPolicyError(accountErr))
		}
		candidate, changed, switchErr := environment.ActivateRouteAccount(
			current.Aggregate(), routeID, environment.RouteAccountReference{
				ID: account.Account.ID.String(), Revision: environment.Revision(account.Account.Revision),
				DisplayName: account.Account.DisplayName,
			},
		)
		if switchErr != nil {
			return problemResponse(classifyEnvironmentError(switchErr))
		}
		if !changed {
			return jsonResponse(http.StatusOK, EnvironmentAccountActivationResponse{
				Environment: environmentResponseOf(current), RouteID: routeID, AccountID: *input.AccountID,
			})
		}
		draft, saveErr := handler.environments.SaveDraft(request.Context(), environment.DraftCommand{
			ExpectedBaseRevision: environment.Revision(expected), Candidate: candidate,
		})
		if saveErr != nil {
			return problemResponse(classifyEnvironmentError(saveErr))
		}
		preview, previewErr := handler.environments.Preview(request.Context(), id, draft.Revision)
		if previewErr != nil {
			return problemResponse(classifyEnvironmentError(previewErr))
		}
		result, publishErr := handler.environments.Publish(request.Context(), preview)
		if publishErr != nil {
			return problemResponse(classifyEnvironmentError(publishErr))
		}
		snapshot, readErr := handler.environments.GetRevision(request.Context(), id, result.ActualRevision)
		if readErr != nil {
			return problemResponse(classifyEnvironmentError(readErr))
		}
		handler.recordActivity(request.Context(), activity.Event{
			Kind: activity.KindEnvironmentApplied, EnvironmentID: snapshot.ID(),
			EnvironmentRevision: snapshot.Revision(), EnvironmentDigest: snapshot.Digest().String(),
			SubjectID: snapshot.ID().String(), Status: activity.StatusSucceeded,
		})
		return jsonResponse(http.StatusOK, EnvironmentAccountActivationResponse{
			Environment: environmentResponseOf(snapshot), RouteID: routeID, AccountID: *input.AccountID,
			RunningCaptureCount: len(preview.ContinuingCaptures),
		})
	})
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonRevisionConflict)
		return
	}
	writeCached(writer, response)
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
