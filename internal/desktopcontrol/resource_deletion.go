package desktopcontrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"strconv"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

// ErrHolderLookupUnavailable refuses a delete whose holders cannot be
// consulted. Treating an unanswerable question as a clear answer is the one
// outcome these guards exist to prevent.
var ErrHolderLookupUnavailable = errors.New("deletion holder lookup is unavailable")

// deletionHolderLimit bounds what a refusal returns. A user resolves holders
// one at a time, so a page of them is a working list; a thousand is a wall.
// The count is reported in full so the response never pretends the list is
// exhaustive when it is not.
const deletionHolderLimit = 20

// DeletionHolderResponse is one reason a delete did not happen.
type DeletionHolderResponse struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

// DeletionResponse is the single shape every destructive operation answers
// with. Four resources can be deleted and they differ only in what holds them,
// so they differ in nothing a client has to special-case.
type DeletionResponse struct {
	Deleted     bool                     `json:"deleted"`
	HolderCount uint64                   `json:"holderCount"`
	Holders     []DeletionHolderResponse `json:"holders"`
	// Released reports what a completed delete actually freed. It is absent
	// when nothing was deleted, and absent for resources that release nothing
	// measurable.
	Released *DeletionReleasedResponse `json:"released,omitempty"`
}

// DeletionReleasedResponse is the receipt for a delete that removed evidence.
type DeletionReleasedResponse struct {
	Exchanges   uint64 `json:"exchanges"`
	Envelopes   uint64 `json:"envelopes"`
	Activities  uint64 `json:"activities"`
	Connections uint64 `json:"connections"`
	Attempts    uint64 `json:"attempts"`
	Approvals   uint64 `json:"approvals"`
	Assignments uint64 `json:"assignments"`
	Captures    uint64 `json:"captures"`
}

func deletionResponseOf(
	result resourcedeletion.Result,
	released *DeletionReleasedResponse,
) DeletionResponse {
	holders := result.Holders
	total := uint64(len(holders))
	if len(holders) > deletionHolderLimit {
		holders = holders[:deletionHolderLimit]
	}
	rendered := make([]DeletionHolderResponse, 0, len(holders))
	for _, holder := range holders {
		rendered = append(rendered, DeletionHolderResponse{
			Kind:   string(holder.Kind),
			ID:     holder.ID,
			Label:  holder.Label,
			Detail: holder.Detail,
		})
	}
	response := DeletionResponse{
		Deleted:     result.Deleted,
		HolderCount: total,
		Holders:     rendered,
	}
	if result.Deleted {
		response.Released = released
	}
	return response
}

func (handler *Handler) deleteEnvironment(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if handler.environments == nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	id, idErr := environment.NewEnvironmentID(request.PathValue("environmentId"))
	expected, key, headerErr := mutationHeaders(request)
	if idErr != nil || headerErr != nil || !emptyBody(request.Body) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	handler.respondToDeletion(writer, request, key, expected, func() cachedResponse {
		result, err := handler.environments.Delete(request.Context(), id)
		if err != nil {
			return problemResponse(classifyEnvironmentError(err))
		}
		return jsonResponse(http.StatusOK, deletionResponseOf(result, nil))
	})
}

func (handler *Handler) deleteUpstreamEndpoint(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if handler.endpoints == nil || handler.environments == nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	id, idErr := upstreamendpoint.NewID(request.PathValue("endpointId"))
	expected, key, headerErr := mutationHeaders(request)
	if idErr != nil || headerErr != nil || !emptyBody(request.Body) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	handler.respondToDeletion(writer, request, key, expected, func() cachedResponse {
		result, err := handler.endpoints.Delete(
			request.Context(), id,
			func(ctx context.Context, endpointID upstreamendpoint.ID) (
				[]resourcedeletion.Holder, error,
			) {
				return handler.environments.HoldersForEndpoint(ctx, endpointID.String())
			},
			handler.ownedAccountHolders,
		)
		if err != nil {
			return problemResponse(classifyUpstreamEndpointError(err))
		}
		return jsonResponse(http.StatusOK, deletionResponseOf(result, nil))
	})
}

// ownedAccountHolders names the Accounts an Endpoint owns. Their credentials
// live in the host SecretStore, so removing the owner silently would destroy
// something the user never named.
func (handler *Handler) ownedAccountHolders(
	ctx context.Context,
	id upstreamendpoint.ID,
) ([]resourcedeletion.Holder, error) {
	if handler.accounts == nil {
		return nil, ErrHolderLookupUnavailable
	}
	views, err := handler.accounts.List(ctx)
	if err != nil {
		return nil, err
	}
	holders := make([]resourcedeletion.Holder, 0)
	for _, view := range views {
		if view.Account.UpstreamEndpointID != id {
			continue
		}
		holders = append(holders, resourcedeletion.Holder{
			Kind:   resourcedeletion.KindOwnedAccount,
			ID:     view.Account.ID.String(),
			Label:  view.Account.DisplayName,
			Detail: id.String(),
		})
	}
	return holders, nil
}

// respondToDeletion runs one destructive operation behind the idempotency cache
// so a retried request cannot delete twice, and answers a replayed key with the
// original outcome rather than a second, different one.
func (handler *Handler) respondToDeletion(
	writer http.ResponseWriter,
	request *http.Request,
	key string,
	expected uint64,
	run func() cachedResponse,
) {
	fingerprint := sha256.Sum256(bytes.Join([][]byte{
		[]byte(request.Method),
		[]byte(request.URL.Path),
		[]byte(strconv.FormatUint(expected, 10)),
	}, []byte{0}))
	response, err := handler.idempotent.execute(
		request.Context(), key, fingerprint, run,
	)
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonRevisionConflict)
		return
	}
	writeCached(writer, response)
}

func (handler *Handler) deleteCapture(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if handler.archive == nil || handler.archiveBarrier == nil ||
		handler.captureRuns == nil || handler.manualCaptures == nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	reference, refErr := captureidentity.ParseKey(request.PathValue("captureKey"))
	expected, key, headerErr := mutationHeaders(request)
	if refErr != nil || headerErr != nil || !emptyBody(request.Body) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	handler.respondToDeletion(writer, request, key, expected, func() cachedResponse {
		holders, err := handler.runningCaptureHolders(request.Context(), reference)
		if err != nil {
			return problemResponse(problemSpec{
				status: http.StatusServiceUnavailable, reason: ReasonRuntimeUnavailable,
			})
		}
		if len(holders) != 0 {
			refused, refuseErr := resourcedeletion.Refused(holders)
			if refuseErr != nil {
				return problemResponse(problemSpec{
					status: http.StatusInternalServerError, reason: ReasonInvalidRequest,
				})
			}
			return jsonResponse(http.StatusOK, deletionResponseOf(refused, nil))
		}
		released, err := handler.archive.DeleteCapture(
			request.Context(), string(reference.Kind), reference.ID,
		)
		if err != nil {
			if errors.Is(err, resourcedeletion.ErrTargetNotFound) {
				return problemResponse(problemSpec{
					status: http.StatusNotFound, reason: ReasonCaptureNotFound,
				})
			}
			return problemResponse(problemSpec{
				status: http.StatusServiceUnavailable, reason: ReasonRuntimeUnavailable,
			})
		}
		return jsonResponse(http.StatusOK, deletionResponseOf(
			resourcedeletion.Completed(), releasedResponseOf(released),
		))
	})
}

func (handler *Handler) clearArchive(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if handler.archive == nil || handler.captureRuns == nil || handler.manualCaptures == nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	expected, key, headerErr := mutationHeaders(request)
	if headerErr != nil || !emptyBody(request.Body) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	handler.respondToDeletion(writer, request, key, expected, func() cachedResponse {
		release, err := handler.archiveBarrier.BeginClear(request.Context())
		if err != nil {
			return problemResponse(problemSpec{
				status: http.StatusServiceUnavailable, reason: ReasonRuntimeUnavailable,
			})
		}
		defer release()

		holders, err := handler.runningCaptureHolders(
			request.Context(), captureidentity.Reference{},
		)
		if err != nil {
			return problemResponse(problemSpec{
				status: http.StatusServiceUnavailable, reason: ReasonRuntimeUnavailable,
			})
		}
		if len(holders) != 0 {
			refused, refuseErr := resourcedeletion.Refused(holders)
			if refuseErr != nil {
				return problemResponse(problemSpec{
					status: http.StatusInternalServerError, reason: ReasonInvalidRequest,
				})
			}
			return jsonResponse(http.StatusOK, deletionResponseOf(refused, nil))
		}
		released, err := handler.archive.ClearEvidence(request.Context())
		if err != nil {
			return problemResponse(problemSpec{
				status: http.StatusServiceUnavailable, reason: ReasonRuntimeUnavailable,
			})
		}
		return jsonResponse(http.StatusOK, deletionResponseOf(
			resourcedeletion.Completed(), releasedResponseOf(released),
		))
	})
}

// runningCaptureHolders reports Captures still admitting traffic.
//
// Deleting evidence underneath a live Capture would race its own writer: the
// rows would go while the batch that is about to add more is still in flight.
// An empty target asks the same question of every Capture, which is what a
// full archive clear needs.
func (handler *Handler) runningCaptureHolders(
	ctx context.Context,
	target captureidentity.Reference,
) ([]resourcedeletion.Holder, error) {
	if target.ID != "" {
		switch target.Kind {
		case captureidentity.KindManagedRun:
			run, err := handler.captureRuns.GetRun(ctx, target.ID)
			if errors.Is(err, capturerun.ErrNotFound) {
				return []resourcedeletion.Holder{}, nil
			}
			if err != nil {
				return nil, err
			}
			return managedRunHolders([]capturerun.View{run}), nil
		case captureidentity.KindManualCapture:
			id, err := manualcapture.ParseID(target.ID)
			if err != nil {
				return nil, err
			}
			capture, err := handler.manualCaptures.Get(
				ctx, manualcapture.NewLocalOwnerScope(), id,
			)
			if errors.Is(err, manualcapture.ErrNotFound) {
				return []resourcedeletion.Holder{}, nil
			}
			if err != nil {
				return nil, err
			}
			return manualCaptureHolders([]manualcapture.View{capture}), nil
		default:
			return nil, captureidentity.ErrInvalidReference
		}
	}

	holders := make([]resourcedeletion.Holder, 0)
	var runCursor *capturerun.PageCursor
	for {
		page, err := handler.captureRuns.ListRuns(ctx, capturerun.PageRequest{
			Limit:  capturerun.MaxPageLimit,
			Cursor: runCursor,
		})
		if err != nil {
			return nil, err
		}
		holders = append(holders, managedRunHolders(page.Items)...)
		if len(page.Items) < capturerun.MaxPageLimit {
			break
		}
		last := page.Items[len(page.Items)-1]
		runCursor = &capturerun.PageCursor{
			Running:            managedRunIsActive(last),
			UpdatedAt:          last.UpdatedAt,
			AfterID:            last.ID,
			IncludeAtUpdatedAt: true,
		}
	}

	var manualCursor *manualcapture.PageCursor
	for {
		page, err := handler.manualCaptures.List(ctx, manualcapture.PageRequest{
			Owner:  manualcapture.NewLocalOwnerScope(),
			Limit:  manualcapture.MaxPageLimit,
			Cursor: manualCursor,
		})
		if err != nil {
			return nil, err
		}
		holders = append(holders, manualCaptureHolders(page.Items)...)
		if len(page.Items) < manualcapture.MaxPageLimit {
			break
		}
		last := page.Items[len(page.Items)-1]
		manualCursor = &manualcapture.PageCursor{
			Running:            last.State == manualcapture.StateActive,
			UpdatedAt:          last.UpdatedAt,
			AfterID:            last.ID,
			IncludeAtUpdatedAt: true,
		}
	}
	return holders, nil
}

func managedRunIsActive(run capturerun.View) bool {
	return run.State == capturerun.StateCreated || run.State == capturerun.StateAttached
}

func managedRunHolders(runs []capturerun.View) []resourcedeletion.Holder {
	holders := make([]resourcedeletion.Holder, 0)
	for _, run := range runs {
		if !managedRunIsActive(run) {
			continue
		}
		holders = append(holders, resourcedeletion.Holder{
			Kind:   resourcedeletion.KindRunningCapture,
			ID:     "managed_run:" + run.ID,
			Label:  run.ExecutableLabel,
			Detail: string(run.State),
		})
	}
	return holders
}

func manualCaptureHolders(captures []manualcapture.View) []resourcedeletion.Holder {
	holders := make([]resourcedeletion.Holder, 0)
	for _, capture := range captures {
		if capture.State != manualcapture.StateActive {
			continue
		}
		holders = append(holders, resourcedeletion.Holder{
			Kind:   resourcedeletion.KindRunningCapture,
			ID:     "manual_capture:" + capture.ID,
			Label:  capture.DisplayName,
			Detail: string(capture.State),
		})
	}
	return holders
}

func releasedResponseOf(released resourcedeletion.Released) *DeletionReleasedResponse {
	return &DeletionReleasedResponse{
		Exchanges:   released.Exchanges,
		Envelopes:   released.Envelopes,
		Activities:  released.Activities,
		Connections: released.Connections,
		Attempts:    released.Attempts,
		Approvals:   released.Approvals,
		Assignments: released.Assignments,
		Captures:    released.Captures,
	}
}
