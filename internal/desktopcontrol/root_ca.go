package desktopcontrol

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktoptrust"
)

type RootCAResponse struct {
	RootRevision       uint64    `json:"rootRevision"`
	Fingerprint        string    `json:"fingerprint"`
	Algorithm          string    `json:"algorithm"`
	NotBefore          time.Time `json:"notBefore"`
	NotAfter           time.Time `json:"notAfter"`
	RootValid          bool      `json:"rootValid"`
	CertificatePresent string    `json:"certificatePresence"`
	TrustDecision      string    `json:"trustDecision"`
	EvidenceRevision   string    `json:"evidenceRevision,omitempty"`
	ObservedAt         time.Time `json:"observedAt"`
	Available          bool      `json:"available"`
	Reason             string    `json:"reason,omitempty"`
}

type RootCAActionResponse struct {
	Status          RootCAResponse `json:"status"`
	ResultStatus    string         `json:"resultStatus"`
	Reason          string         `json:"reason"`
	Completed       bool           `json:"completed"`
	RestartRequired bool           `json:"restartRequired,omitempty"`
}

type RootCAMaterialResponse struct {
	RootRevision         uint64 `json:"rootRevision"`
	Fingerprint          string `json:"fingerprint"`
	CertificateDERBase64 string `json:"certificateDerBase64"`
}

func (handler *Handler) getRootCA(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	status, err := handler.rootTrust.Status(request.Context())
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, classifyRootTrustReason(err))
		return
	}
	writeJSON(writer, http.StatusOK, rootCAResponseOf(status))
}

func (handler *Handler) getRootCAMaterial(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	material, err := handler.rootTrust.Material(request.Context())
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, classifyRootTrustReason(err))
		return
	}
	writeJSON(writer, http.StatusOK, RootCAMaterialResponse{
		RootRevision:         material.RootRevision,
		Fingerprint:          material.Fingerprint,
		CertificateDERBase64: base64.StdEncoding.EncodeToString(material.CertificateDER),
	})
}

func (handler *Handler) replaceRootCA(
	writer http.ResponseWriter,
	request *http.Request,
) {
	handler.rootCAMutation(writer, request, "replace", func(
		ctx context.Context,
	) (desktoptrust.ActionResult, error) {
		return handler.rootTrust.Replace(ctx)
	})
}

func (handler *Handler) rootCAMutation(
	writer http.ResponseWriter,
	request *http.Request,
	action string,
	apply func(context.Context) (desktoptrust.ActionResult, error),
) {
	expected, key, headerErr := mutationHeaders(request)
	if headerErr != nil || !emptyBody(request.Body) || request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := sha256.Sum256([]byte(
		"vibermate:root-ca:" + action + ":" + strconv.FormatUint(expected, 10),
	))
	response, err := handler.idempotent.execute(
		request.Context(), key, fingerprint,
		func() cachedResponse {
			current, statusErr := handler.rootTrust.Status(request.Context())
			if statusErr != nil {
				return problemResponse(classifyRootTrustProblem(statusErr))
			}
			if current.RootRevision != expected {
				return problemResponse(problemSpec{
					status: http.StatusConflict,
					reason: ReasonRootTrustConflict,
				})
			}
			result, actionErr := apply(request.Context())
			if actionErr != nil {
				return problemResponse(classifyRootTrustProblem(actionErr))
			}
			return jsonResponse(http.StatusOK, RootCAActionResponse{
				Status:          rootCAResponseOf(result.Status),
				ResultStatus:    string(result.ResultStatus),
				Reason:          string(result.Reason),
				Completed:       result.Completed,
				RestartRequired: result.RestartRequired,
			})
		},
	)
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonRootTrustConflict)
		return
	}
	writeCached(writer, response)
}

func rootCAResponseOf(status desktoptrust.Status) RootCAResponse {
	return RootCAResponse{
		RootRevision:       status.RootRevision,
		Fingerprint:        status.Fingerprint,
		Algorithm:          status.Algorithm,
		NotBefore:          status.NotBefore,
		NotAfter:           status.NotAfter,
		RootValid:          status.RootValid,
		CertificatePresent: string(status.CertificatePresent),
		TrustDecision:      string(status.TrustDecision),
		EvidenceRevision:   string(status.EvidenceRevision),
		ObservedAt:         status.ObservedAt,
		Available:          status.Available,
		Reason:             status.Reason,
	}
}

func classifyRootTrustProblem(err error) problemSpec {
	status := http.StatusServiceUnavailable
	reason := classifyRootTrustReason(err)
	if reason == ReasonRootTrustConflict ||
		reason == ReasonRootResetActiveCaptures ||
		reason == ReasonRootResetRequiresRemoval {
		status = http.StatusConflict
	} else if reason == ReasonRootTrustPermissionDenied ||
		reason == ReasonRootTrustUserCancelled {
		status = http.StatusForbidden
	}
	return problemSpec{
		status: status,
		reason: reason,
	}
}

func classifyRootTrustReason(err error) ReasonCode {
	if err == nil {
		return ReasonRootTrustUnavailable
	}
	if desktoptrust.IsActiveCaptureError(err) {
		return ReasonRootResetActiveCaptures
	}
	if desktoptrust.IsResetPending(err) {
		return ReasonRootTrustConflict
	}
	if desktoptrust.IsResetRemovalRequired(err) {
		return ReasonRootResetRequiresRemoval
	}
	switch desktoptrust.ErrorReason(err) {
	case "user_cancelled":
		return ReasonRootTrustUserCancelled
	case "permission_denied":
		return ReasonRootTrustPermissionDenied
	case "plan_stale", "operation_in_progress":
		return ReasonRootTrustConflict
	}
	if desktoptrust.IsUnsupported(err) {
		return ReasonRootTrustUnsupported
	}
	return ReasonRootTrustUnavailable
}
