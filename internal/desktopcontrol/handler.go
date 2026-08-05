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
	"strconv"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/accessapply"
	"github.com/vibe-agi/vibermate/internal/accesscredential"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/workspaceroute"
)

const (
	maxControlBodyBytes = 1 << 20
	minIdempotencyBytes = 16
	maxIdempotencyBytes = 128
)

type ReasonCode string

const (
	ReasonUnauthorized              ReasonCode = "control_unauthorized"
	ReasonRouteNotFound             ReasonCode = "control_route_not_found"
	ReasonInvalidRequest            ReasonCode = "invalid_control_request"
	ReasonRevisionConflict          ReasonCode = "revision_conflict"
	ReasonAccessNotConfigured       ReasonCode = "access_not_configured"
	ReasonProjectionUnavailable     ReasonCode = "access_projection_unavailable"
	ReasonRuntimeUnavailable        ReasonCode = "runtime_unavailable"
	ReasonApprovalNotFound          ReasonCode = "approval_not_found"
	ReasonProbeFailed               ReasonCode = "offline_probe_failed"
	ReasonCredentialNotFound        ReasonCode = "credential_not_found"
	ReasonCredentialNotConfigured   ReasonCode = "credential_not_configured"
	ReasonCredentialValueInvalid    ReasonCode = "credential_value_invalid"
	ReasonConnectionNotFound        ReasonCode = "connection_not_found"
	ReasonExchangeNotFound          ReasonCode = "exchange_not_found"
	ReasonSecretStoreUnavailable    ReasonCode = "secret_store_unavailable"
	ReasonSecretStoreReadOnly       ReasonCode = "secret_store_read_only"
	ReasonWorkspaceRouteNotFound    ReasonCode = "workspace_route_not_found"
	ReasonWorkspaceRouteUnavailable ReasonCode = "workspace_route_unavailable"
	ReasonCaptureRunRestartRequired ReasonCode = "capture_run_restart_required"
)

type StatusReader interface {
	Status() productruntime.RuntimeStatus
}

type ReadinessReader interface {
	Ready() bool
}

type OfflineActions interface {
	OfflineHoldSnapshot() offlinehold.Snapshot
	EnterOfflineHold(context.Context, uint64) (offlinehold.Snapshot, error)
	ResumeOfflineHold(context.Context, uint64) (offlinehold.Snapshot, error)
}

type Options struct {
	Readiness     ReadinessReader
	Status        StatusReader
	Accesses      access.Writer
	AccessCatalog access.AggregateCatalog
	Resolver      access.SnapshotResolver
	Credentials   accesscredential.Controller
	Activities    activity.Runtime
	Connections   connectionevent.Reader
	Egress        egressaudit.Reader
	Approvals     toolapproval.Controller
	Offline       OfflineActions
	// ConnectionRules is the outbound firewall a person edits. A runtime
	// built without one keeps evaluating the rules it started with.
	ConnectionRules ConnectionRuleController
	// CaptureRuns is the read side of what is captured. It is not a control
	// path: it carries no capability in either direction.
	CaptureRuns     capturerun.Reader
	WorkspaceRoutes workspaceroute.Controller
}

type Handler struct {
	readiness     ReadinessReader
	status        StatusReader
	accesses      access.Writer
	accessCatalog access.AggregateCatalog
	resolver      access.SnapshotResolver
	credentials   accesscredential.Controller
	activities    activity.Runtime
	connections   connectionevent.Reader
	egress        egressaudit.Reader
	approvals     toolapproval.Controller
	offline       OfflineActions

	connectionRules ConnectionRuleController
	captureRuns     capturerun.Reader
	workspaceRoutes workspaceroute.Controller

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

type AccessApplyResponse struct {
	Outcome          access.WriteOutcome    `json:"outcome"`
	Revision         access.Revision        `json:"revision"`
	ApplicationState AccessApplicationState `json:"applicationState"`
	PlanHash         string                 `json:"planHash,omitempty"`
}

type AccessApplicationState string

const (
	AccessApplicationStateActive      AccessApplicationState = "active"
	AccessApplicationStateUnavailable AccessApplicationState = "unavailable"
)

type AccessPlanSummaryResponse struct {
	AccessID        string                        `json:"accessId"`
	Revision        access.Revision               `json:"revision"`
	PlanHash        string                        `json:"planHash"`
	Profiles        []string                      `json:"profiles"`
	AccountBindings []AccessPlanAccountBindingRef `json:"accountBindings"`
}

type AccessPlanAccountBindingRef struct {
	ID        string `json:"id"`
	ProfileID string `json:"profileId"`
}

type ApprovalDecisionInput struct {
	Decision   toolapproval.Decision `json:"decision"`
	Scope      string                `json:"scope"`
	ReasonCode string                `json:"reasonCode,omitempty"`
}

type CredentialSecretInput struct {
	Secret string `json:"secret"`
}

func New(options Options) (*Handler, error) {
	if options.Readiness == nil ||
		options.Status == nil ||
		options.Accesses == nil ||
		options.AccessCatalog == nil ||
		options.Resolver == nil ||
		options.Credentials == nil ||
		options.Activities == nil ||
		options.Connections == nil ||
		options.Egress == nil ||
		options.Approvals == nil ||
		options.Offline == nil {
		return nil, errors.New("Desktop control dependencies are incomplete")
	}
	handler := &Handler{
		readiness:       options.Readiness,
		status:          options.Status,
		accesses:        options.Accesses,
		accessCatalog:   options.AccessCatalog,
		resolver:        options.Resolver,
		credentials:     options.Credentials,
		activities:      options.Activities,
		connections:     options.Connections,
		egress:          options.Egress,
		approvals:       options.Approvals,
		offline:         options.Offline,
		connectionRules: options.ConnectionRules,
		captureRuns:     options.CaptureRuns,
		workspaceRoutes: options.WorkspaceRoutes,
		idempotent:      newIdempotencyCache(),
		mux:             http.NewServeMux(),
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
	handler.mux.HandleFunc(
		"GET /api/v1/accesses",
		handler.listAccesses,
	)
	handler.mux.HandleFunc(
		"GET /api/v1/accesses/{accessId}",
		handler.getAccess,
	)
	handler.mux.HandleFunc(
		"PUT /api/v1/accesses/{accessId}/actions/apply",
		handler.applyAccess,
	)
	handler.mux.HandleFunc(
		"POST /api/v1/accesses/{accessId}/actions/add-candidate",
		handler.addAccessCandidate,
	)
	handler.mux.HandleFunc(
		"GET /api/v1/accesses/{accessId}/plan",
		handler.getAccessPlan,
	)
	handler.mux.HandleFunc(
		"GET /api/v1/accesses/{accessId}/profiles/{profileId}/credentials/{credentialId}",
		handler.getCredential,
	)
	handler.mux.HandleFunc(
		"POST /api/v1/accesses/{accessId}/profiles/{profileId}/credentials/{credentialId}/actions/replace-secret",
		handler.replaceCredentialSecret,
	)
	handler.mux.HandleFunc(
		"POST /api/v1/accesses/{accessId}/profiles/{profileId}/actions/select-candidate",
		handler.selectAccessCandidate,
	)
	handler.mux.HandleFunc("GET /api/v1/activities", handler.listActivities)
	handler.mux.HandleFunc(
		"GET /api/v1/exchanges/{exchangeId}",
		handler.getExchange,
	)
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
	handler.mux.HandleFunc(
		"GET /api/v1/capture-runs",
		handler.listCaptureRuns,
	)
	handler.mux.HandleFunc(
		"GET /api/v1/workspace-route-bindings",
		handler.listWorkspaceRouteBindings,
	)
	handler.mux.HandleFunc(
		"GET /api/v1/workspace-route-bindings/{bindingId}",
		handler.getWorkspaceRouteBinding,
	)
	handler.mux.HandleFunc(
		"PATCH /api/v1/workspace-route-bindings/{bindingId}",
		handler.updateWorkspaceRouteBinding,
	)
	handler.mux.HandleFunc(
		"/api/v1/workspace-route-bindings/{bindingId}",
		handler.invalidRoute,
	)
	handler.mux.HandleFunc(
		"/api/v1/workspace-route-bindings",
		handler.invalidRoute,
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
	handler.mux.HandleFunc(
		"/api/v1/accesses",
		handler.invalidRoute,
	)
	handler.mux.HandleFunc(
		"/api/v1/accesses/{accessId}/actions/{action}",
		handler.invalidRoute,
	)
	handler.mux.HandleFunc(
		"/api/v1/accesses/{accessId}/plan",
		handler.invalidRoute,
	)
	handler.mux.HandleFunc(
		"/api/v1/accesses/{accessId}/profiles/{profileId}/credentials/{credentialId}",
		handler.invalidRoute,
	)
	handler.mux.HandleFunc(
		"/api/v1/accesses/{accessId}/profiles/{profileId}/credentials/{credentialId}/actions/{action}",
		handler.invalidRoute,
	)
	handler.mux.HandleFunc(
		"/api/v1/accesses/{accessId}/profiles/{profileId}/actions/{action}",
		handler.invalidRoute,
	)
	handler.mux.HandleFunc(
		"/api/v1/accesses/{accessId}",
		handler.invalidRoute,
	)
	handler.mux.HandleFunc("/api/v1/activities", handler.invalidRoute)
	handler.mux.HandleFunc(
		"/api/v1/exchanges/{exchangeId}",
		handler.invalidRoute,
	)
	handler.mux.HandleFunc("/api/v1/connections", handler.invalidRoute)
	handler.mux.HandleFunc(
		"/api/v1/connections/{connectionId}",
		handler.invalidRoute,
	)
	handler.mux.HandleFunc("/api/v1/approvals", handler.invalidRoute)
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

func (handler *Handler) applyAccess(
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
	fingerprint := sha256.Sum256(bytes.Join(
		[][]byte{
			[]byte(request.Method),
			[]byte(request.URL.Path),
			[]byte(strconv.FormatUint(expected, 10)),
			body,
		},
		[]byte{0},
	))
	response, err := handler.idempotent.execute(
		request.Context(),
		key,
		fingerprint,
		func() cachedResponse {
			var input accessapply.Input
			if decodeStrictJSON(body, &input) != nil ||
				input.ExpectedRevision != expected ||
				input.Access.Status != string(access.AccessStatusEnabled) {
				return problemResponse(problemSpec{
					status: http.StatusUnprocessableEntity,
					reason: ReasonInvalidRequest,
				})
			}
			if spec := handler.preserveExistingAccountSecretRefs(
				request.Context(),
				request.PathValue("accessId"),
				access.Revision(expected),
				&input,
			); spec != nil {
				return problemResponse(*spec)
			}
			command, buildErr := accessapply.BuildCommand(
				request.PathValue("accessId"),
				input,
			)
			if buildErr != nil {
				return problemResponse(classifyAccessError(buildErr))
			}
			result, writeErr := handler.accesses.WriteAccess(
				request.Context(),
				command,
			)
			if writeErr != nil {
				if result.Outcome == access.WriteOutcomeCommitted &&
					errors.Is(writeErr, access.ErrProjectionUnavailable) {
					handler.recordActivity(request.Context(), activity.Event{
						Kind:       activity.KindAccessApplied,
						AccessID:   command.Aggregate.Binding.ID,
						SubjectID:  strconv.FormatUint(uint64(result.Revision), 10),
						Status:     activity.StatusFailed,
						ReasonCode: string(access.ReasonProjectionUnavailable),
					})
					return jsonResponse(http.StatusOK, AccessApplyResponse{
						Outcome:          result.Outcome,
						Revision:         result.Revision,
						ApplicationState: AccessApplicationStateUnavailable,
					})
				}
				return problemResponse(classifyAccessError(writeErr))
			}
			handler.recordActivity(request.Context(), activity.Event{
				Kind:      activity.KindAccessApplied,
				AccessID:  command.Aggregate.Binding.ID,
				SubjectID: strconv.FormatUint(uint64(result.Revision), 10),
				Status:    activity.StatusSucceeded,
			})
			return jsonResponse(http.StatusOK, AccessApplyResponse{
				Outcome:          result.Outcome,
				Revision:         result.Revision,
				ApplicationState: AccessApplicationStateActive,
				PlanHash:         result.PlanHash.String(),
			})
		},
	)
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonRevisionConflict)
		return
	}
	writeCached(writer, response)
}

func (handler *Handler) getAccessPlan(
	writer http.ResponseWriter,
	request *http.Request,
) {
	accessID, err := access.NewAccessID(request.PathValue("accessId"))
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	snapshot, err := handler.resolver.ResolveAccess(accessID)
	if err != nil {
		spec := classifyAccessError(err)
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	profiles := snapshot.EndpointProfiles()
	profileIDs := make([]string, len(profiles))
	for index, profile := range profiles {
		profileIDs[index] = profile.ID.String()
	}
	bindings := snapshot.AccountBindings()
	bindingRefs := make([]AccessPlanAccountBindingRef, len(bindings))
	for index, binding := range bindings {
		bindingRefs[index] = AccessPlanAccountBindingRef{
			ID:        binding.ID.String(),
			ProfileID: binding.ProfileID.String(),
		}
	}
	writer.Header().Set(
		"ETag",
		`"revision-`+strconv.FormatUint(uint64(snapshot.Revision()), 10)+`"`,
	)
	writeJSON(writer, http.StatusOK, AccessPlanSummaryResponse{
		AccessID:        snapshot.AccessID().String(),
		Revision:        snapshot.Revision(),
		PlanHash:        snapshot.PlanHash().String(),
		Profiles:        profileIDs,
		AccountBindings: bindingRefs,
	})
}

func (handler *Handler) getCredential(
	writer http.ResponseWriter,
	request *http.Request,
) {
	accessID, profileID, credentialID, err := credentialPath(request)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	view, err := handler.credentials.GetCredential(
		request.Context(),
		accessID,
		profileID,
		credentialID,
	)
	if err != nil {
		spec := classifyCredentialError(err)
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	writer.Header().Set(
		"ETag",
		`"revision-`+strconv.FormatUint(uint64(view.SecretRevision), 10)+`"`,
	)
	writeJSON(writer, http.StatusOK, view)
}

func (handler *Handler) replaceCredentialSecret(
	writer http.ResponseWriter,
	request *http.Request,
) {
	expected, key, err := mutationHeaders(request)
	if err != nil || expected > uint64(secretstore.MaxRevision) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	accessID, profileID, credentialID, err := credentialPath(request)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	body, err := readJSONBody(request)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	defer clear(body)
	fingerprint := sha256.Sum256(bytes.Join(
		[][]byte{
			[]byte(request.Method),
			[]byte(request.URL.Path),
			[]byte(strconv.FormatUint(expected, 10)),
			body,
		},
		[]byte{0},
	))
	response, err := handler.idempotent.execute(
		request.Context(),
		key,
		fingerprint,
		func() cachedResponse {
			var input CredentialSecretInput
			if decodeStrictJSON(body, &input) != nil {
				return problemResponse(problemSpec{
					status: http.StatusUnprocessableEntity,
					reason: ReasonInvalidRequest,
				})
			}
			secretBytes := []byte(input.Secret)
			input.Secret = ""
			defer clear(secretBytes)
			value, valueErr := secretstore.NewValue(secretBytes)
			if valueErr != nil {
				return problemResponse(problemSpec{
					status: http.StatusUnprocessableEntity,
					reason: ReasonCredentialValueInvalid,
				})
			}
			defer value.Destroy()
			view, replaceErr := handler.credentials.ReplaceSecret(
				request.Context(),
				accesscredential.ReplaceCommand{
					AccessID:         accessID,
					ProfileID:        profileID,
					CredentialID:     credentialID,
					ExpectedRevision: secretstore.Revision(expected),
					Value:            value,
				},
			)
			if replaceErr != nil {
				return problemResponse(classifyCredentialError(replaceErr))
			}
			handler.recordActivity(request.Context(), activity.Event{
				Kind:      activity.KindCredentialSecretReplaced,
				AccessID:  accessID,
				SubjectID: credentialID.String(),
				Status:    activity.StatusSucceeded,
			})
			return jsonResponse(http.StatusOK, view)
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
	page, err := handler.activities.ListExchanges(
		request.Context(),
		activity.PageRequest{
			BeforeSequence: query.beforeSequence,
			Limit:          query.limit,
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
	writeJSON(writer, http.StatusOK, view)
}

func (handler *Handler) listConnections(
	writer http.ResponseWriter,
	request *http.Request,
) {
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
			BeforeSequence: before,
			Limit:          limit,
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

// ReasonCaptureRunsUnavailable reports a runtime built without a capture read.
const ReasonCaptureRunsUnavailable ReasonCode = "capture_runs_unavailable"

// listCaptureRuns answers "is my client actually going through vibermate".
// Until this existed, the only way to know was to watch traffic appear
// somewhere else and infer it.
func (handler *Handler) listCaptureRuns(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if handler.captureRuns == nil {
		writeProblem(
			writer,
			http.StatusServiceUnavailable,
			ReasonCaptureRunsUnavailable,
		)
		return
	}
	limit, err := queryLimit(request, capturerun.DefaultPageLimit)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	page, err := handler.captureRuns.ListRuns(
		request.Context(),
		capturerun.PageRequest{Limit: limit},
	)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	writeJSON(writer, http.StatusOK, CaptureRunAuditPageOf(page))
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
	accessID, _ := access.NewAccessID(view.AccessID)
	status := activity.StatusSucceeded
	reason := ""
	if view.State == toolapproval.StateDenied {
		status = activity.StatusFailed
		reason = view.TerminalReason
	}
	handler.recordActivity(request.Context(), activity.Event{
		Kind:       activity.KindApprovalResolved,
		AccessID:   accessID,
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

func classifyAccessError(err error) problemSpec {
	if errors.Is(err, access.ErrAccessNotConfigured) {
		return problemSpec{
			status: http.StatusNotFound,
			reason: ReasonAccessNotConfigured,
		}
	}
	spec := problemSpec{
		status: http.StatusUnprocessableEntity,
		reason: ReasonInvalidRequest,
	}
	var failure *access.Failure
	if errors.As(err, &failure) {
		switch failure.Code {
		case access.ReasonRevisionConflict:
			spec.status = http.StatusConflict
			spec.reason = ReasonRevisionConflict
		case access.ReasonProjectionUnavailable,
			access.ReasonCommitOutcomeUnknown:
			spec.status = http.StatusServiceUnavailable
			spec.reason = ReasonProjectionUnavailable
		case access.ReasonAccessRuntimeStopping:
			spec.status = http.StatusServiceUnavailable
			spec.reason = ReasonRuntimeUnavailable
		}
	}
	return spec
}

func classifyCredentialError(err error) problemSpec {
	switch {
	case errors.Is(err, accesscredential.ErrCredentialNotFound):
		return problemSpec{
			status: http.StatusNotFound,
			reason: ReasonCredentialNotFound,
		}
	case errors.Is(err, accesscredential.ErrInvalidCredential):
		return problemSpec{
			status: http.StatusUnprocessableEntity,
			reason: ReasonCredentialValueInvalid,
		}
	case errors.Is(err, secretstore.ErrRevisionConflict),
		errors.Is(err, secretstore.ErrRevisionExhausted):
		return problemSpec{
			status: http.StatusConflict,
			reason: ReasonRevisionConflict,
		}
	case errors.Is(err, secretstore.ErrReadOnly):
		return problemSpec{
			status: http.StatusConflict,
			reason: ReasonSecretStoreReadOnly,
		}
	case errors.Is(err, secretstore.ErrUnavailable),
		errors.Is(err, secretstore.ErrLocked),
		errors.Is(err, secretstore.ErrDenied):
		return problemSpec{
			status: http.StatusServiceUnavailable,
			reason: ReasonSecretStoreUnavailable,
		}
	case errors.Is(err, access.ErrProjectionUnavailable),
		errors.Is(err, access.ErrAccessRuntimeStopping):
		return classifyAccessError(err)
	default:
		return problemSpec{
			status: http.StatusUnprocessableEntity,
			reason: ReasonInvalidRequest,
		}
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

func credentialPath(
	request *http.Request,
) (
	access.AccessID,
	access.EndpointProfileID,
	access.AccountBindingID,
	error,
) {
	accessID, err := access.NewAccessID(request.PathValue("accessId"))
	if err != nil {
		return access.AccessID{}, access.EndpointProfileID{}, access.AccountBindingID{}, err
	}
	profileID, err := access.NewEndpointProfileID(request.PathValue("profileId"))
	if err != nil {
		return access.AccessID{}, access.EndpointProfileID{}, access.AccountBindingID{}, err
	}
	credentialID, err := access.NewAccountBindingID(
		request.PathValue("credentialId"),
	)
	if err != nil {
		return access.AccessID{}, access.EndpointProfileID{}, access.AccountBindingID{}, err
	}
	return accessID, profileID, credentialID, nil
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
