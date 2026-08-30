// Package desktopcontrol exposes the bounded Desktop control slice. It
// returns stable reason and i18n keys; localized user copy remains in catalogs.
package desktopcontrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/codelibrary"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/evidencearchive"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/modelcatalog"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

const (
	maxControlBodyBytes = 1 << 20
	minIdempotencyBytes = 16
	maxIdempotencyBytes = 128
)

type ReasonCode string

const (
	ReasonUnauthorized                       ReasonCode = "control_unauthorized"
	ReasonRouteNotFound                      ReasonCode = "control_route_not_found"
	ReasonInvalidRequest                     ReasonCode = "invalid_control_request"
	ReasonRevisionConflict                   ReasonCode = "revision_conflict"
	ReasonEnvironmentNotFound                ReasonCode = "environment_not_found"
	ReasonEnvironmentDraftNotFound           ReasonCode = "environment_draft_not_found"
	ReasonProjectionUnavailable              ReasonCode = "environment_projection_unavailable"
	ReasonRuntimeUnavailable                 ReasonCode = "runtime_unavailable"
	ReasonApprovalNotFound                   ReasonCode = "approval_not_found"
	ReasonProbeFailed                        ReasonCode = "offline_probe_failed"
	ReasonConnectionNotFound                 ReasonCode = "connection_not_found"
	ReasonExchangeNotFound                   ReasonCode = "exchange_not_found"
	ReasonEnvironmentSystemOwned             ReasonCode = "environment_system_owned"
	ReasonEnvironmentPreviewStale            ReasonCode = "environment_preview_stale"
	ReasonCaptureNotFound                    ReasonCode = "capture_not_found"
	ReasonCaptureAssignmentNotFound          ReasonCode = "capture_assignment_not_found"
	ReasonCaptureUnavailable                 ReasonCode = "capture_unavailable"
	ReasonEnvironmentUnavailable             ReasonCode = "environment_unavailable"
	ReasonUpstreamEndpointNotFound           ReasonCode = "upstream_endpoint_not_found"
	ReasonUpstreamEndpointConflict           ReasonCode = "upstream_endpoint_conflict"
	ReasonUpstreamEndpointUnavailable        ReasonCode = "upstream_endpoint_unavailable"
	ReasonProviderAccountNotFound            ReasonCode = "provider_account_not_found"
	ReasonProviderAccountConflict            ReasonCode = "provider_account_conflict"
	ReasonProviderAccountInUse               ReasonCode = "provider_account_in_use"
	ReasonProviderAccountUnavailable         ReasonCode = "provider_account_unavailable"
	ReasonRawEvidenceNotFound                ReasonCode = "raw_evidence_not_found"
	ReasonRawEvidenceUnavailable             ReasonCode = "raw_evidence_unavailable"
	ReasonModelCatalogUnavailable            ReasonCode = "model_catalog_unavailable"
	ReasonModelCatalogTimeout                ReasonCode = "model_catalog_timeout"
	ReasonModelCatalogAuthenticationRejected ReasonCode = "model_catalog_authentication_rejected"
	ReasonMessageTransformTestFailed         ReasonCode = "message_transform_test_failed"
	ReasonAccountSelectorTestFailed          ReasonCode = "account_selector_test_failed"
	ReasonCodeLibraryNotFound                ReasonCode = "code_library_not_found"
	ReasonCodeLibraryConflict                ReasonCode = "code_library_conflict"
	ReasonCodeLibraryUnavailable             ReasonCode = "code_library_unavailable"
	ReasonEgressProfileNotFound              ReasonCode = "egress_profile_not_found"
	ReasonEgressProfileConflict              ReasonCode = "egress_profile_conflict"
	ReasonEgressProfileUnavailable           ReasonCode = "egress_profile_unavailable"
)

type StatusReader interface {
	Status() productruntime.RuntimeStatus
}

type ReadinessReader interface {
	Ready() bool
}

// SystemClock is available to standalone control-contract tests and tools.
// Product composition passes its single runtime Clock instead.
type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now()
}

type OfflineActions interface {
	OfflineHoldSnapshot() offlinehold.Snapshot
	EnterOfflineHold(context.Context, uint64) (offlinehold.Snapshot, error)
	ResumeOfflineHold(context.Context, uint64) (offlinehold.Snapshot, error)
}

// ConversationIndexer adds exact client-owned session and actor evidence to
// the rebuildable Conversation index. A resolver failure must not make the
// underlying Activity journal unreadable, so callers treat Reindex as an
// additive refresh and Identity as the durable read boundary.
type ConversationIndexer interface {
	Reindex(context.Context, activity.ConversationIndexRequest) error
	Identity(context.Context, string) (agentconversation.ClientIdentity, error)
}

type Options struct {
	Readiness           ReadinessReader
	Status              StatusReader
	Environments        environment.Controller
	Assignments         captureassignment.Controller
	Activities          activity.Runtime
	ConversationIndexer ConversationIndexer
	Contents            exchangecontent.Reader
	Connections         connectionevent.Reader
	Egress              egressaudit.Reader
	Approvals           toolapproval.Controller
	Endpoints           upstreamendpoint.Controller
	Accounts            provideraccount.Controller
	CodeLibrary         codelibrary.Controller
	EgressProfiles      egressprofile.Controller
	Models              modelcatalog.Reader
	ClientModels        modelcatalog.ProviderMetadataReader
	RawEvidence         rawevidence.Reader
	Offline             OfflineActions
	// ConnectionRules is the outbound firewall a person edits. A runtime
	// built without one keeps evaluating the rules it started with.
	ConnectionRules ConnectionRuleController
	// CaptureRuns is the read side of what is captured. It is not a control
	// path: it carries no capability in either direction.
	CaptureRuns    capturerun.Reader
	ManualCaptures manualcapture.Controller
	Archive        resourcedeletion.Archive
	ArchiveBarrier evidencearchive.ClearBarrier
	Clock          Clock
}

type Handler struct {
	readiness           ReadinessReader
	status              StatusReader
	environments        environment.Controller
	assignments         captureassignment.Controller
	activities          activity.Runtime
	conversationIndexer ConversationIndexer
	contents            exchangecontent.Reader
	connections         connectionevent.Reader
	egress              egressaudit.Reader
	approvals           toolapproval.Controller
	endpoints           upstreamendpoint.Controller
	accounts            provideraccount.Controller
	codeLibrary         codelibrary.Controller
	egressProfiles      egressprofile.Controller
	models              modelcatalog.Reader
	clientModels        modelcatalog.ProviderMetadataReader
	rawEvidence         rawevidence.Reader
	offline             OfflineActions

	connectionRules ConnectionRuleController
	archive         resourcedeletion.Archive
	archiveBarrier  evidencearchive.ClearBarrier
	captureRuns     capturerun.Reader
	manualCaptures  manualcapture.Controller
	clock           Clock

	idempotent *idempotencyCache
	mux        *http.ServeMux
}

type StatusResponse struct {
	Generation string                       `json:"generation"`
	Ready      bool                         `json:"ready"`
	APIVersion string                       `json:"apiVersion"`
	StatusKey  string                       `json:"statusKey"`
	Runtime    productruntime.RuntimeStatus `json:"runtime"`
}

type ApprovalDecisionInput struct {
	Decision   toolapproval.Decision `json:"decision"`
	Scope      string                `json:"scope"`
	ReasonCode string                `json:"reasonCode,omitempty"`
}

func New(options Options) (*Handler, error) {
	if options.Readiness == nil ||
		options.Status == nil ||
		options.Environments == nil ||
		options.Assignments == nil ||
		options.Activities == nil ||
		options.Contents == nil ||
		options.Connections == nil ||
		options.Egress == nil ||
		options.Approvals == nil ||
		options.Endpoints == nil ||
		options.Accounts == nil ||
		options.Offline == nil ||
		options.Clock == nil {
		return nil, errors.New("Desktop control dependencies are incomplete")
	}
	handler := &Handler{
		readiness:           options.Readiness,
		status:              options.Status,
		environments:        options.Environments,
		assignments:         options.Assignments,
		activities:          options.Activities,
		conversationIndexer: options.ConversationIndexer,
		contents:            options.Contents,
		connections:         options.Connections,
		egress:              options.Egress,
		approvals:           options.Approvals,
		endpoints:           options.Endpoints,
		accounts:            options.Accounts,
		codeLibrary:         options.CodeLibrary,
		egressProfiles:      options.EgressProfiles,
		models:              options.Models,
		clientModels:        options.ClientModels,
		rawEvidence:         options.RawEvidence,
		offline:             options.Offline,
		connectionRules:     options.ConnectionRules,
		archive:             options.Archive,
		archiveBarrier:      options.ArchiveBarrier,
		captureRuns:         options.CaptureRuns,
		manualCaptures:      options.ManualCaptures,
		clock:               options.Clock,
		idempotent:          newIdempotencyCache(),
		mux:                 http.NewServeMux(),
	}
	handler.mux.HandleFunc("GET /api/v1/status", handler.getStatus)
	handler.mux.HandleFunc("GET /api/v1/offline-hold", handler.getOfflineHold)
	handler.mux.HandleFunc(
		"POST /api/v1/offline-hold/actions/enter",
		handler.enterOfflineHold,
	)
	handler.mux.HandleFunc(
		"POST /api/v1/offline-hold/actions/resume",
		handler.resumeOfflineHold,
	)
	handler.mux.HandleFunc("GET /api/v1/environments", handler.listEnvironments)
	handler.mux.HandleFunc(
		"POST /api/v1/message-transforms/actions/test",
		handler.testMessageTransform,
	)
	handler.mux.HandleFunc(
		"POST /api/v1/account-selectors/actions/test",
		handler.testAccountSelector,
	)
	if handler.codeLibrary != nil {
		handler.mux.HandleFunc("GET /api/v1/code-library", handler.listCodeLibrary)
		handler.mux.HandleFunc("POST /api/v1/code-library/collections", handler.createCodeLibraryCollection)
		handler.mux.HandleFunc("PUT /api/v1/code-library/transforms/{transformId}", handler.publishCodeLibraryTransform)
		handler.mux.HandleFunc(
			"GET /api/v1/code-library/transforms/{transformId}/revisions/{transformRevision}",
			handler.getCodeLibraryTransformRevision,
		)
		handler.mux.HandleFunc(
			"PUT /api/v1/code-library/account-selectors/{selectorId}",
			handler.publishCodeLibraryAccountSelector,
		)
		handler.mux.HandleFunc(
			"GET /api/v1/code-library/account-selectors/{selectorId}/revisions/{selectorRevision}",
			handler.getCodeLibraryAccountSelectorRevision,
		)
	}
	if handler.egressProfiles != nil {
		handler.mux.HandleFunc("GET /api/v1/egress-profiles", handler.listEgressProfiles)
		handler.mux.HandleFunc("PUT /api/v1/egress-profiles/{egressId}", handler.publishEgressProfile)
		handler.mux.HandleFunc(
			"GET /api/v1/egress-profiles/{egressId}/revisions/{egressRevision}",
			handler.getEgressProfileRevision,
		)
	}
	handler.mux.HandleFunc("GET /api/v1/upstream-endpoints", handler.listUpstreamEndpoints)
	handler.mux.HandleFunc("POST /api/v1/upstream-endpoints", handler.createUpstreamEndpoint)
	handler.mux.HandleFunc("GET /api/v1/upstream-endpoints/{endpointId}", handler.getUpstreamEndpoint)
	if handler.models != nil {
		handler.mux.HandleFunc(
			"GET /api/v1/upstream-endpoints/{endpointId}/models",
			handler.getUpstreamEndpointModels,
		)
	}
	if handler.clientModels != nil {
		handler.mux.HandleFunc("GET /api/v1/client-models", handler.getClientModels)
	}
	handler.mux.HandleFunc("GET /api/v1/provider-accounts", handler.listProviderAccounts)
	handler.mux.HandleFunc("POST /api/v1/provider-accounts", handler.createProviderAccount)
	handler.mux.HandleFunc("GET /api/v1/provider-accounts/{accountId}", handler.getProviderAccount)
	handler.mux.HandleFunc("DELETE /api/v1/provider-accounts/{accountId}", handler.deleteProviderAccount)
	handler.mux.HandleFunc("DELETE /api/v1/environments/{environmentId}", handler.deleteEnvironment)
	handler.mux.HandleFunc("DELETE /api/v1/upstream-endpoints/{endpointId}", handler.deleteUpstreamEndpoint)
	handler.mux.HandleFunc("DELETE /api/v1/captures/{captureKey}", handler.deleteCapture)
	handler.mux.HandleFunc("POST /api/v1/evidence/actions/clear", handler.clearArchive)
	handler.mux.HandleFunc("PUT /api/v1/provider-accounts/{accountId}/credential", handler.replaceProviderAccountCredential)
	handler.mux.HandleFunc("GET /api/v1/environments/{environmentId}", handler.getEnvironment)
	handler.mux.HandleFunc("GET /api/v1/environments/{environmentId}/draft", handler.getEnvironmentDraft)
	handler.mux.HandleFunc("PUT /api/v1/environments/{environmentId}/draft", handler.putEnvironmentDraft)
	handler.mux.HandleFunc("POST /api/v1/environments/{environmentId}/draft/actions/preview", handler.previewEnvironmentDraft)
	handler.mux.HandleFunc("POST /api/v1/environments/{environmentId}/draft/actions/publish", handler.publishEnvironmentDraft)
	handler.mux.HandleFunc("GET /api/v1/environments/{environmentId}/revisions/{environmentRevision}", handler.getEnvironmentRevision)
	handler.mux.HandleFunc("GET /api/v1/activities", handler.listActivities)
	handler.mux.HandleFunc("GET /api/v1/conversations", handler.listConversations)
	handler.mux.HandleFunc(
		"GET /api/v1/exchanges/{exchangeId}",
		handler.getExchange,
	)
	if handler.rawEvidence != nil {
		handler.mux.HandleFunc(
			"GET /api/v1/exchanges/{exchangeId}/raw-evidence",
			handler.listRawEvidence,
		)
		handler.mux.HandleFunc(
			"POST /api/v1/raw-evidence/{envelopeId}/actions/reveal",
			handler.revealRawEvidence,
		)
	}
	handler.mux.HandleFunc("GET /api/v1/connections", handler.listConnections)
	handler.mux.HandleFunc(
		"GET /api/v1/egress-attempts",
		handler.listEgressAttempts,
	)
	handler.mux.HandleFunc(
		"GET /api/v1/connections/{connectionId}",
		handler.getConnection,
	)
	handler.mux.HandleFunc(
		"GET /api/v1/policies/connections",
		handler.getConnectionRules,
	)
	handler.mux.HandleFunc(
		"PATCH /api/v1/policies/connections",
		handler.replaceConnectionRules,
	)
	handler.mux.HandleFunc("/api/v1/policies/connections", handler.invalidRoute)
	handler.mux.HandleFunc("GET /api/v1/captures", handler.listCaptures)
	handler.mux.HandleFunc("GET /api/v1/captures/{captureKey}", handler.getCapture)
	handler.mux.HandleFunc("GET /api/v1/captures/{captureKey}/environment-assignment", handler.getCaptureEnvironmentAssignment)
	handler.mux.HandleFunc(
		"POST /api/v1/captures/{captureKey}/environment-assignment/actions/apply-latest",
		handler.applyLatestCaptureEnvironment,
	)
	handler.mux.HandleFunc("GET /api/v1/approvals", handler.listApprovals)
	handler.mux.HandleFunc(
		"GET /api/v1/approvals/{approvalId}",
		handler.getApproval,
	)
	handler.mux.HandleFunc(
		"POST /api/v1/approvals/{approvalId}/actions/decide",
		handler.decideApproval,
	)
	handler.mux.HandleFunc("/api/v1/status", handler.invalidRoute)
	handler.mux.HandleFunc("/api/v1/offline-hold", handler.invalidRoute)
	handler.mux.HandleFunc(
		"/api/v1/offline-hold/actions/{action}",
		handler.invalidRoute,
	)
	handler.mux.HandleFunc("/api/v1/activities", handler.invalidRoute)
	handler.mux.HandleFunc(
		"/api/v1/exchanges/{exchangeId}",
		handler.invalidRoute,
	)
	handler.mux.HandleFunc(
		"/api/v1/exchanges/{exchangeId}/raw-evidence",
		handler.invalidRoute,
	)
	handler.mux.HandleFunc(
		"/api/v1/raw-evidence/{envelopeId}/actions/{action}",
		handler.invalidRoute,
	)
	handler.mux.HandleFunc("/api/v1/connections", handler.invalidRoute)
	handler.mux.HandleFunc(
		"/api/v1/connections/{connectionId}",
		handler.invalidRoute,
	)
	handler.mux.HandleFunc("/api/v1/approvals", handler.invalidRoute)
	handler.mux.HandleFunc("/api/v1/message-transforms/", handler.invalidRoute)
	handler.mux.HandleFunc("/api/v1/account-selectors/", handler.invalidRoute)
	handler.mux.HandleFunc("/api/v1/code-library", handler.invalidRoute)
	handler.mux.HandleFunc("/api/v1/code-library/", handler.invalidRoute)
	handler.mux.HandleFunc("/api/v1/provider-accounts", handler.invalidRoute)
	handler.mux.HandleFunc("/api/v1/provider-accounts/{accountId}", handler.invalidRoute)
	handler.mux.HandleFunc("/api/v1/provider-accounts/{accountId}/{remainder}", handler.invalidRoute)
	handler.mux.HandleFunc(
		"/api/v1/approvals/{approvalId}",
		handler.invalidRoute,
	)
	handler.mux.HandleFunc(
		"/api/v1/approvals/{approvalId}/actions/{action}",
		handler.invalidRoute,
	)
	handler.mux.HandleFunc("/", handler.invalidRoute)
	return handler, nil
}

func (handler *Handler) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	handler.mux.ServeHTTP(writer, request)
}

func (handler *Handler) RequiredScope(request *http.Request) Scope {
	if request == nil {
		return ""
	}
	switch {
	case request.Method == http.MethodGet:
		return ScopeRead
	case request.Method == http.MethodPut ||
		request.Method == http.MethodPost ||
		request.Method == http.MethodPatch ||
		request.Method == http.MethodDelete:
		return ScopeWrite
	default:
		return ""
	}
}

func (handler *Handler) getStatus(
	writer http.ResponseWriter,
	_ *http.Request,
) {
	status := handler.status.Status()
	writeJSON(writer, http.StatusOK, StatusResponse{
		Generation: status.InstanceID,
		Ready:      handler.readiness.Ready(),
		APIVersion: "v1",
		StatusKey:  "runtime.state." + string(status.State),
		Runtime:    status,
	})
}

func (handler *Handler) getOfflineHold(
	writer http.ResponseWriter,
	_ *http.Request,
) {
	writeJSON(writer, http.StatusOK, handler.offline.OfflineHoldSnapshot())
}

func (handler *Handler) enterOfflineHold(
	writer http.ResponseWriter,
	request *http.Request,
) {
	handler.offlineMutation(writer, request, "enter", func(
		ctx context.Context,
		expected uint64,
	) (offlinehold.Snapshot, error) {
		return handler.offline.EnterOfflineHold(ctx, expected)
	})
}

func (handler *Handler) resumeOfflineHold(
	writer http.ResponseWriter,
	request *http.Request,
) {
	handler.offlineMutation(writer, request, "resume", func(
		ctx context.Context,
		expected uint64,
	) (offlinehold.Snapshot, error) {
		return handler.offline.ResumeOfflineHold(ctx, expected)
	})
}

func (handler *Handler) offlineMutation(
	writer http.ResponseWriter,
	request *http.Request,
	action string,
	run func(context.Context, uint64) (offlinehold.Snapshot, error),
) {
	expected, key, err := mutationHeaders(request)
	if err != nil || !emptyBody(request.Body) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := sha256.Sum256([]byte(
		request.Method + "\x00" + request.URL.Path + "\x00" +
			strconv.FormatUint(expected, 10),
	))
	response, err := handler.idempotent.execute(
		request.Context(),
		key,
		fingerprint,
		func() cachedResponse {
			snapshot, actionErr := run(request.Context(), expected)
			if actionErr != nil &&
				!(action == "resume" &&
					snapshot.State == offlinehold.StateHeld &&
					snapshot.LastProbeReason != "") {
				return problemResponse(classifyOfflineError(actionErr))
			}
			kind := activity.KindOfflineHoldEntered
			if action == "resume" {
				kind = activity.KindOfflineHoldResumed
			}
			status := activity.StatusSucceeded
			reason := ""
			if actionErr != nil {
				status = activity.StatusFailed
				reason = string(snapshot.LastProbeReason)
			}
			handler.recordActivity(request.Context(), activity.Event{
				Kind:       kind,
				SubjectID:  strconv.FormatUint(snapshot.Revision, 10),
				Status:     status,
				ReasonCode: reason,
			})
			return jsonResponse(http.StatusOK, snapshot)
		},
	)
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonRevisionConflict)
		return
	}
	writeCached(writer, response)
}

func (handler *Handler) listActivities(
	writer http.ResponseWriter,
	request *http.Request,
) {
	query, err := parseActivityListQuery(request.URL.RawQuery)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	handler.refreshConversationIndex(request.Context(), activity.ConversationIndexRequest{
		Limit:           1,
		CaptureRunID:    query.captureRunID,
		ManualCaptureID: query.manualCaptureID,
	})
	page, err := handler.activities.ListExchanges(
		request.Context(),
		activity.PageRequest{
			BeforeSequence:           query.beforeSequence,
			Limit:                    query.limit,
			CaptureRunID:             query.captureRunID,
			ManualCaptureID:          query.manualCaptureID,
			EnvironmentID:            query.environmentID,
			ConversationProjectionID: query.conversationID,
		},
	)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	view, err := activityPageOf(page)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	if err := handler.attachActivityIdentities(request.Context(), page, &view); err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	// A request preview is additive list chrome, not Activity authority. One
	// damaged or temporarily unreadable retained transcript must not hide the
	// rest of a Capture. The Exchange detail endpoint still performs the full
	// integrity check and reports the exact evidence failure when it is opened.
	_ = handler.attachActivityRequestPreviews(request.Context(), &view)
	writeJSON(writer, http.StatusOK, view)
}

func (handler *Handler) listConversations(
	writer http.ResponseWriter,
	request *http.Request,
) {
	values := request.URL.Query()
	limit := 50
	var err error
	if values.Has("limit") {
		limit, err = strconv.Atoi(values.Get("limit"))
	}
	before := int64(0)
	if values.Has("cursor") {
		before, err = parseConversationCursor(values.Get("cursor"))
	}
	if err != nil || limit < 1 || limit > activity.MaxPageSize ||
		!onlyQueryKeys(
			values,
			"limit",
			"cursor",
			"captureRunId",
			"manualCaptureId",
		) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	indexRequest := activity.ConversationIndexRequest{
		BeforeFirstSequence: before,
		Limit:               limit,
		CaptureRunID:        values.Get("captureRunId"),
		ManualCaptureID:     values.Get("manualCaptureId"),
	}
	if indexRequest.Validate() != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	handler.refreshConversationIndex(request.Context(), indexRequest)
	page, err := handler.activities.ListConversations(
		request.Context(),
		indexRequest,
	)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	view, err := conversationPageOf(page)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	if err := handler.attachConversationIdentities(request.Context(), page, &view); err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (handler *Handler) refreshConversationIndex(
	ctx context.Context,
	request activity.ConversationIndexRequest,
) {
	if handler.conversationIndexer == nil || request.Validate() != nil {
		return
	}
	// Client-local state is enrichment rather than Activity authority. A file
	// being appended, moved, or temporarily unavailable must never turn the
	// audit journal into a 503 response; unresolved Exchanges remain isolated.
	_ = handler.conversationIndexer.Reindex(ctx, request)
}

func (handler *Handler) attachActivityIdentities(
	ctx context.Context,
	page activity.Page,
	view *ActivityPage,
) error {
	if handler.conversationIndexer == nil || view == nil {
		return nil
	}
	if len(page.Items) != len(view.Items) {
		return errors.New("Activity identity projection length does not match")
	}
	for index, record := range page.Items {
		identity, err := handler.conversationIndexer.Identity(ctx, record.SubjectID)
		switch {
		case err == nil:
			cloned := identity.Clone()
			view.Items[index].Conversation.ClientIdentity = &cloned
			if err := view.Items[index].Validate(); err != nil {
				return err
			}
		case errors.Is(err, activity.ErrExchangeNotFound):
			continue
		default:
			return err
		}
	}
	return nil
}

func (handler *Handler) attachActivityRequestPreviews(
	ctx context.Context,
	view *ActivityPage,
) error {
	if view == nil || len(view.Items) == 0 {
		return nil
	}
	exchangeIDs := make([]string, len(view.Items))
	for index := range view.Items {
		exchangeIDs[index] = view.Items[index].ID
	}
	previews, err := handler.contents.RequestPreviews(ctx, exchangeIDs)
	if err != nil {
		return err
	}
	for index := range view.Items {
		preview, exists := previews[view.Items[index].ID]
		if !exists {
			continue
		}
		view.Items[index].RequestPreview = &preview
		if err := view.Items[index].Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (handler *Handler) attachConversationIdentities(
	ctx context.Context,
	page activity.ConversationPage,
	view *ConversationPage,
) error {
	if handler.conversationIndexer == nil || view == nil {
		return nil
	}
	if len(page.Items) != len(view.Items) {
		return errors.New("Conversation identity projection length does not match")
	}
	for index, item := range page.Items {
		identity, err := handler.conversationIndexer.Identity(ctx, item.Latest.SubjectID)
		switch {
		case err == nil:
			cloned := identity.Clone()
			view.Items[index].Conversation.ClientIdentity = &cloned
			view.Items[index].Latest.Conversation.ClientIdentity = &cloned
			if err := view.Items[index].Conversation.Validate(); err != nil {
				return err
			}
			if err := view.Items[index].Latest.Validate(); err != nil {
				return err
			}
		case errors.Is(err, activity.ErrExchangeNotFound):
			continue
		default:
			return err
		}
	}
	return nil
}

func onlyQueryKeys(values url.Values, allowed ...string) bool {
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key, entries := range values {
		if _, found := set[key]; !found || len(entries) != 1 || entries[0] == "" {
			return false
		}
	}
	return true
}

func (handler *Handler) listConnections(
	writer http.ResponseWriter,
	request *http.Request,
) {
	values := request.URL.Query()
	for name, entries := range values {
		if (name != "limit" && name != "cursor" && name != "ingressId" && name != "view") ||
			len(entries) != 1 {
			writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
			return
		}
	}
	if entries, present := values["ingressId"]; present && entries[0] == "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	latestPerConnection := false
	if entries, present := values["view"]; present {
		if entries[0] != "latest" {
			writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
			return
		}
		latestPerConnection = true
	}
	limit, err := queryLimit(request, 50)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	var before int64
	if raw := request.URL.Query().Get("cursor"); raw != "" {
		before, err = connectionevent.ParseCursor(raw)
		if err != nil {
			writeProblem(
				writer,
				http.StatusUnprocessableEntity,
				ReasonInvalidRequest,
			)
			return
		}
	}
	page, err := handler.connections.List(
		request.Context(),
		connectionevent.PageRequest{
			BeforeSequence:      before,
			Limit:               limit,
			IngressID:           values.Get("ingressId"),
			LatestPerConnection: latestPerConnection,
		},
	)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	writeJSON(writer, http.StatusOK, page)
}

func (handler *Handler) getConnection(
	writer http.ResponseWriter,
	request *http.Request,
) {
	timeline, err := handler.connections.Timeline(
		request.Context(),
		request.PathValue("connectionId"),
	)
	switch {
	case errors.Is(err, connectionevent.ErrNotFound):
		writeProblem(writer, http.StatusNotFound, ReasonConnectionNotFound)
	case err != nil:
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
	default:
		writeJSON(writer, http.StatusOK, timeline)
	}
}

func (handler *Handler) listApprovals(
	writer http.ResponseWriter,
	request *http.Request,
) {
	limit, err := queryLimit(request, 50)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	state := toolapproval.State(request.URL.Query().Get("state"))
	page, err := handler.approvals.ListApprovals(
		request.Context(),
		toolapproval.PageRequest{State: state, Limit: limit},
	)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	writeJSON(writer, http.StatusOK, page)
}

func (handler *Handler) getApproval(
	writer http.ResponseWriter,
	request *http.Request,
) {
	view, err := handler.approvals.GetApproval(
		request.Context(),
		request.PathValue("approvalId"),
	)
	if err != nil {
		spec := problemSpec{
			status: http.StatusServiceUnavailable,
			reason: ReasonRuntimeUnavailable,
		}
		if errors.Is(err, toolapproval.ErrNotFound) {
			spec.status = http.StatusNotFound
			spec.reason = ReasonApprovalNotFound
		}
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (handler *Handler) decideApproval(
	writer http.ResponseWriter,
	request *http.Request,
) {
	expected, key, err := mutationHeaders(request)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	body, err := readJSONBody(request)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	var input ApprovalDecisionInput
	if decodeStrictJSON(body, &input) != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	view, err := handler.approvals.DecideApproval(
		request.Context(),
		toolapproval.DecisionCommand{
			ApprovalID:       request.PathValue("approvalId"),
			ExpectedRevision: expected,
			IdempotencyKey:   key,
			Decision:         input.Decision,
			Scope:            input.Scope,
			ReasonCode:       input.ReasonCode,
		},
	)
	if err != nil {
		spec := problemSpec{
			status: http.StatusUnprocessableEntity,
			reason: ReasonInvalidRequest,
		}
		switch {
		case errors.Is(err, toolapproval.ErrNotFound):
			spec.status = http.StatusNotFound
			spec.reason = ReasonApprovalNotFound
		case errors.Is(err, toolapproval.ErrRevisionConflict):
			spec.status = http.StatusConflict
			spec.reason = ReasonRevisionConflict
		}
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	status := activity.StatusSucceeded
	reason := ""
	if view.State == toolapproval.StateDenied {
		status = activity.StatusFailed
		reason = view.TerminalReason
	}
	handler.recordActivity(request.Context(), activity.Event{
		Kind:       activity.KindApprovalResolved,
		SubjectID:  view.ID,
		Status:     status,
		ReasonCode: reason,
	})
	writeJSON(writer, http.StatusOK, view)
}

func (handler *Handler) invalidRoute(
	writer http.ResponseWriter,
	_ *http.Request,
) {
	writeProblem(writer, http.StatusNotFound, ReasonRouteNotFound)
}

func (handler *Handler) recordActivity(
	parent context.Context,
	event activity.Event,
) {
	ctx, cancel := context.WithTimeout(
		context.WithoutCancel(parent),
		time.Second,
	)
	defer cancel()
	_, _ = handler.activities.Record(ctx, event)
}

func (runtime *RuntimeOfflineAdapter) OfflineHoldSnapshot() offlinehold.Snapshot {
	return runtime.Runtime.Status().OfflineHold
}

// RuntimeOfflineAdapter keeps the control handler on the safe ProductRuntime
// resume method rather than exposing a caller-selected Prober.
type RuntimeOfflineAdapter struct {
	Runtime *productruntime.Runtime
}

func (runtime *RuntimeOfflineAdapter) EnterOfflineHold(
	ctx context.Context,
	expected uint64,
) (offlinehold.Snapshot, error) {
	return runtime.Runtime.EnterOfflineHold(ctx, expected)
}

func (runtime *RuntimeOfflineAdapter) ResumeOfflineHold(
	ctx context.Context,
	expected uint64,
) (offlinehold.Snapshot, error) {
	return runtime.Runtime.ResumeOfflineHold(ctx, expected)
}

type problemSpec struct {
	status int
	reason ReasonCode
}

func classifyEnvironmentError(err error) problemSpec {
	switch {
	case errors.Is(err, environment.ErrEnvironmentNotFound):
		return problemSpec{
			status: http.StatusNotFound,
			reason: ReasonEnvironmentNotFound,
		}
	case errors.Is(err, environment.ErrDraftNotFound):
		return problemSpec{
			status: http.StatusNotFound,
			reason: ReasonEnvironmentDraftNotFound,
		}
	case errors.Is(err, environment.ErrInvalidEnvironment),
		errors.Is(err, environment.ErrInvalidTransition):
		return problemSpec{
			status: http.StatusUnprocessableEntity,
			reason: ReasonInvalidRequest,
		}
	case errors.Is(err, environment.ErrRevisionConflict):
		return problemSpec{
			status: http.StatusConflict,
			reason: ReasonRevisionConflict,
		}
	case errors.Is(err, environment.ErrSystemEnvironment):
		return problemSpec{
			status: http.StatusConflict,
			reason: ReasonEnvironmentSystemOwned,
		}
	case errors.Is(err, environment.ErrPreviewStale):
		return problemSpec{
			status: http.StatusConflict,
			reason: ReasonEnvironmentPreviewStale,
		}
	case errors.Is(err, environment.ErrProjectionUnavailable),
		errors.Is(err, environment.ErrProjectionNotRestored),
		errors.Is(err, environment.ErrTransitionUnavailable),
		errors.Is(err, environment.ErrCommitOutcomeUnknown):
		return problemSpec{status: http.StatusServiceUnavailable, reason: ReasonProjectionUnavailable}
	default:
		return problemSpec{status: http.StatusServiceUnavailable, reason: ReasonRuntimeUnavailable}
	}
}

func classifyOfflineError(err error) problemSpec {
	spec := problemSpec{
		status: http.StatusConflict,
		reason: ReasonRevisionConflict,
	}
	if errors.Is(err, offlinehold.ErrInvalidRequest) {
		spec.status = http.StatusUnprocessableEntity
		spec.reason = ReasonInvalidRequest
	} else if errors.Is(err, offlinehold.ErrCoordinatorStopping) {
		spec.status = http.StatusServiceUnavailable
		spec.reason = ReasonRuntimeUnavailable
	}
	return spec
}

func mutationHeaders(request *http.Request) (uint64, string, error) {
	rawRevision := request.Header.Get("If-Match")
	key := request.Header.Get("Idempotency-Key")
	if rawRevision == "" ||
		len(key) < minIdempotencyBytes ||
		len(key) > maxIdempotencyBytes ||
		strings.TrimSpace(key) != key {
		return 0, "", errors.New("mutation headers are invalid")
	}
	revision, err := strconv.ParseUint(rawRevision, 10, 64)
	if err != nil {
		return 0, "", errors.New("If-Match is invalid")
	}
	return revision, key, nil
}

func readJSONBody(request *http.Request) ([]byte, error) {
	if request == nil || request.Body == nil {
		return nil, errors.New("JSON body is missing")
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, errors.New("JSON Content-Type is invalid")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxControlBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxControlBodyBytes {
		return nil, errors.New("JSON body is invalid")
	}
	return body, nil
}

func decodeStrictJSON(body []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("JSON body contains trailing data")
	}
	return nil
}

func emptyBody(body io.Reader) bool {
	if body == nil {
		return true
	}
	data, err := io.ReadAll(io.LimitReader(body, 1))
	return err == nil && len(data) == 0
}

func queryLimit(request *http.Request, fallback int) (int, error) {
	raw := request.URL.Query().Get("limit")
	if raw == "" {
		return fallback, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 || limit > 200 {
		return 0, errors.New("page limit is invalid")
	}
	return limit, nil
}

func jsonResponse(status int, value any) cachedResponse {
	body, err := json.Marshal(value)
	if err != nil {
		return problemResponse(problemSpec{
			status: http.StatusInternalServerError,
			reason: ReasonRuntimeUnavailable,
		})
	}
	body = append(body, '\n')
	return cachedResponse{
		status:      status,
		contentType: "application/json",
		body:        body,
	}
}

func problemResponse(spec problemSpec) cachedResponse {
	body, _ := json.Marshal(problemBody(spec.status, spec.reason))
	return cachedResponse{
		status:      spec.status,
		contentType: "application/problem+json",
		body:        append(body, '\n'),
	}
}

func writeProblem(
	writer http.ResponseWriter,
	status int,
	reason ReasonCode,
) {
	writeCached(writer, problemResponse(problemSpec{
		status: status,
		reason: reason,
	}))
}

func problemBody(status int, reason ReasonCode) any {
	return struct {
		Type   string     `json:"type"`
		Title  string     `json:"title"`
		Status int        `json:"status"`
		Code   ReasonCode `json:"code"`
	}{
		Type:   "urn:vibermate:error:" + strings.ReplaceAll(string(reason), "_", "-"),
		Title:  http.StatusText(status),
		Status: status,
		Code:   reason,
	}
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writeCached(writer, jsonResponse(status, value))
}

func writeCached(writer http.ResponseWriter, response cachedResponse) {
	writer.Header().Set("Content-Type", response.contentType)
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(response.status)
	_, _ = writer.Write(response.body)
}

// listEgressAttempts serves the per-request half of the audit. A connection
// answers who connected where from here; this answers where each request
// actually went, which a persistent connection cannot express.
func (handler *Handler) listEgressAttempts(
	writer http.ResponseWriter,
	request *http.Request,
) {
	limit, err := queryLimit(request, egressaudit.DefaultPageLimit)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	page, err := handler.egress.List(
		request.Context(),
		egressaudit.PageRequest{
			Limit:        limit,
			AfterCursor:  request.URL.Query().Get("cursor"),
			ConnectionID: request.URL.Query().Get("connectionId"),
			ExchangeID:   request.URL.Query().Get("exchangeId"),
			ParentKind:   egressaudit.ParentKind(request.URL.Query().Get("parentKind")),
			ParentID:     request.URL.Query().Get("parentId"),
			Purpose:      egressaudit.EgressPurpose(request.URL.Query().Get("purpose")),
		},
	)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	// An attempt is immutable evidence with unexported fields, so it is
	// rendered through its explicit contract rather than serialized directly.
	writeJSON(writer, http.StatusOK, egressaudit.PageViewOf(page))
}
