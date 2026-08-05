package desktopcontrol

import (
	"bytes"
	"crypto/sha256"
	"net/http"
	"strconv"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/activity"
)

type AccessDeletionPreviewResponse struct {
	AccessID                string                   `json:"accessId"`
	Name                    string                   `json:"name"`
	Revision                access.Revision          `json:"revision"`
	Status                  access.AccessStatus      `json:"status"`
	WorkspaceBindingCount   int                      `json:"workspaceBindingCount"`
	ActiveCaptureRunCount   int                      `json:"activeCaptureRunCount"`
	ProxyClientBindingCount int                      `json:"proxyClientBindingCount"`
	ExclusiveSecretCount    int                      `json:"exclusiveSecretCount"`
	SharedSecretCount       int                      `json:"sharedSecretCount"`
	ImpactToken             string                   `json:"impactToken"`
	Blockers                []access.DeletionBlocker `json:"blockers"`
}

type AccessDeletionInput struct {
	ImpactToken             string `json:"impactToken"`
	RetireWorkspaceBindings bool   `json:"retireWorkspaceBindings"`
}

type AccessDeletionResponse struct {
	Outcome  access.DeleteOutcome `json:"outcome"`
	Revision access.Revision      `json:"revision"`
}

func (handler *Handler) previewAccessDeletion(
	writer http.ResponseWriter,
	request *http.Request,
) {
	expected, err := expectedRevisionHeader(request)
	if err != nil || expected == 0 {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	accessID, err := access.NewAccessID(request.PathValue("accessId"))
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	preview, err := handler.accessDeletion.PreviewDeleteAccess(
		request.Context(),
		access.PreviewDeletionCommand{
			AccessID:         accessID,
			ExpectedRevision: access.Revision(expected),
			ObservedAt:       handler.clock.Now().UTC(),
		},
	)
	if err != nil {
		spec := classifyAccessError(err)
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	writeJSON(writer, http.StatusOK, AccessDeletionPreviewResponse{
		AccessID:                preview.AccessID.String(),
		Name:                    preview.Name,
		Revision:                preview.Revision,
		Status:                  preview.Status,
		WorkspaceBindingCount:   preview.WorkspaceBindingCount,
		ActiveCaptureRunCount:   preview.ActiveCaptureRunCount,
		ProxyClientBindingCount: preview.ProxyClientBindingCount,
		ExclusiveSecretCount:    preview.ExclusiveSecretCount,
		SharedSecretCount:       preview.SharedSecretCount,
		ImpactToken:             preview.ImpactToken.String(),
		Blockers:                append([]access.DeletionBlocker(nil), preview.Blockers...),
	})
}

func (handler *Handler) deleteAccess(
	writer http.ResponseWriter,
	request *http.Request,
) {
	expectedValue, key, err := mutationHeaders(request)
	if err != nil || expectedValue == 0 {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	body, err := readJSONBody(request)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	var input AccessDeletionInput
	if decodeStrictJSON(body, &input) != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	impactToken, err := access.ParseDeletionImpactToken(input.ImpactToken)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	accessID, err := access.NewAccessID(request.PathValue("accessId"))
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := sha256.Sum256(bytes.Join([][]byte{
		[]byte(request.Method),
		[]byte(request.URL.Path),
		[]byte(strconv.FormatUint(expectedValue, 10)),
		body,
	}, []byte{0}))
	response, err := handler.idempotent.execute(
		request.Context(),
		key,
		fingerprint,
		func() cachedResponse {
			result, deleteErr := handler.accessDeletion.DeleteAccess(
				request.Context(),
				access.DeleteCommand{
					AccessID:                accessID,
					ExpectedRevision:        access.Revision(expectedValue),
					ExpectedImpactToken:     impactToken,
					RetireWorkspaceBindings: input.RetireWorkspaceBindings,
					DeletedAt:               handler.clock.Now().UTC(),
				},
			)
			if deleteErr != nil {
				return problemResponse(classifyAccessError(deleteErr))
			}
			if result.Outcome == access.DeleteOutcomeCommitted {
				handler.recordActivity(request.Context(), activity.Event{
					Kind:      activity.KindAccessDeleted,
					AccessID:  accessID,
					SubjectID: strconv.FormatUint(uint64(result.Revision), 10),
					Status:    activity.StatusSucceeded,
				})
			}
			return jsonResponse(http.StatusOK, AccessDeletionResponse{
				Outcome:  access.DeleteOutcomeCommitted,
				Revision: result.Revision,
			})
		},
	)
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonRevisionConflict)
		return
	}
	writeCached(writer, response)
}

func expectedRevisionHeader(request *http.Request) (uint64, error) {
	if request == nil || len(request.Header.Values("If-Match")) != 1 {
		return 0, access.ErrInvalidAccess
	}
	revision, err := strconv.ParseUint(request.Header.Get("If-Match"), 10, 64)
	if err != nil {
		return 0, access.ErrInvalidAccess
	}
	return revision, nil
}
