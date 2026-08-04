package capturecontrol

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturegrant"
	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
)

const maxManualCaptureCreateBytes = 16 << 10

type ManualCaptureAuthority interface {
	GetManualCaptureContext(
		context.Context,
		controlprincipal.Principal,
	) (capturegrant.ManualCaptureContext, error)
	IssueManualCapture(
		context.Context,
		controlprincipal.Principal,
		capturegrant.ManualCaptureRequest,
	) (capturegrant.ManualCaptureGrant, error)
	ListManualCaptures(
		context.Context,
		controlprincipal.Principal,
		int,
	) (manualcapture.Page, error)
	GetManualCapture(
		context.Context,
		controlprincipal.Principal,
		manualcapture.ID,
	) (manualcapture.View, error)
	RotateManualCapture(
		context.Context,
		controlprincipal.Principal,
		capturegrant.ManualCaptureRotateRequest,
	) (capturegrant.ManualCaptureGrant, error)
	RevokeManualCapture(
		context.Context,
		controlprincipal.Principal,
		capturegrant.ManualCaptureRevokeRequest,
	) (manualcapture.View, error)
}

// ManualHandler owns one HTTP contract shared by the local CLI and Desktop
// App. The Host supplies an already-authenticated principal explicitly; this
// type cannot infer ownership from a request body or proxy credential.
type ManualHandler struct {
	authority ManualCaptureAuthority
	mux       *http.ServeMux
}

type ManualCaptureCreateRequest struct {
	DisplayName       string                    `json:"displayName"`
	ClientClass       manualcapture.ClientClass `json:"clientClass"`
	Lifetime          manualcapture.Lifetime    `json:"lifetime"`
	ExpiresInSeconds  *int64                    `json:"expiresInSeconds,omitempty"`
	ConfirmationToken string                    `json:"confirmationToken"`
}

type RootPublicDelivery struct {
	Kind        string `json:"kind"`
	DERSHA256   string `json:"derSha256"`
	Fingerprint string `json:"fingerprint"`
	PEMPath     string `json:"pemPath"`
}

type ManualCaptureContext struct {
	ConfirmationToken       string             `json:"confirmationToken"`
	ProxyAddress            string             `json:"proxyAddress"`
	Root                    RootPublicDelivery `json:"root"`
	DefaultTemporarySeconds int64              `json:"defaultTemporarySeconds"`
	MaxTemporarySeconds     int64              `json:"maxTemporarySeconds"`
}

// ManualCaptureView is the user-facing projection. The credential epoch is a
// Core-only linearization detail and is deliberately not product vocabulary.
type ManualCaptureView struct {
	ID               string                    `json:"id"`
	IngressProfileID string                    `json:"ingressProfileId"`
	DisplayName      string                    `json:"displayName"`
	ClientClass      manualcapture.ClientClass `json:"clientClass"`
	Lifetime         manualcapture.Lifetime    `json:"lifetime"`
	State            manualcapture.State       `json:"state"`
	Observation      manualcapture.Observation `json:"observation"`
	CreatedAt        time.Time                 `json:"createdAt"`
	UpdatedAt        time.Time                 `json:"updatedAt"`
	ExpiresAt        *time.Time                `json:"expiresAt,omitempty"`
	LastObservedAt   *time.Time                `json:"lastObservedAt,omitempty"`
}

type ManualCapturePage struct {
	Items []ManualCaptureView `json:"items"`
}

type ManualCaptureGrant struct {
	Capture       ManualCaptureView  `json:"capture"`
	ProxyAddress  string             `json:"proxyAddress"`
	ProxyUsername string             `json:"proxyUsername"`
	ProxyPassword string             `json:"proxyPassword"`
	Root          RootPublicDelivery `json:"root"`
}

func NewManualHandler(authority ManualCaptureAuthority) (*ManualHandler, error) {
	if authority == nil {
		return nil, errors.New("ManualCapture control authority is required")
	}
	handler := &ManualHandler{authority: authority, mux: http.NewServeMux()}
	handler.mux.HandleFunc("GET /api/v1/manual-captures/context", handler.context)
	handler.mux.HandleFunc("GET /api/v1/manual-captures", handler.list)
	handler.mux.HandleFunc("POST /api/v1/manual-captures", handler.create)
	handler.mux.HandleFunc("GET /api/v1/manual-captures/{manualCaptureId}", handler.get)
	handler.mux.HandleFunc(
		"POST /api/v1/manual-captures/{manualCaptureId}/actions/rotate-credential",
		handler.rotate,
	)
	handler.mux.HandleFunc(
		"POST /api/v1/manual-captures/{manualCaptureId}/actions/revoke",
		handler.revoke,
	)
	handler.mux.HandleFunc("/", handler.invalidRoute)
	return handler, nil
}

// ServeHTTP authenticates no credential. The caller must have done so and
// must pass the resulting immutable principal on this call.
func (handler *ManualHandler) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
	principal controlprincipal.Principal,
) {
	if handler == nil || request == nil || !principal.Valid() ||
		!principal.Allows(controlprincipal.GrantManualCapture) {
		writeProblem(writer, http.StatusForbidden, ReasonCaptureGrantNotAllowed)
		return
	}
	request = request.WithContext(withManualPrincipal(request.Context(), principal))
	handler.mux.ServeHTTP(writer, request)
}

func (handler *ManualHandler) context(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if !emptyBody(request.Body) || request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidManualCapture)
		return
	}
	result, err := handler.authority.GetManualCaptureContext(
		request.Context(),
		manualPrincipal(request.Context()),
	)
	if err != nil {
		handler.writeFailure(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, manualContextWire(result))
}

func (handler *ManualHandler) list(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if !emptyBody(request.Body) || request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidManualCapture)
		return
	}
	page, err := handler.authority.ListManualCaptures(
		request.Context(),
		manualPrincipal(request.Context()),
		manualcapture.DefaultPageLimit,
	)
	if err != nil {
		handler.writeFailure(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, manualPageWire(page))
}

func (handler *ManualHandler) create(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidManualCapture)
		return
	}
	var input ManualCaptureCreateRequest
	if err := decodeJSON(request, &input, maxManualCaptureCreateBytes); err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidManualCapture)
		return
	}
	expiresIn, ok := manualCaptureExpiry(input)
	if !ok {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidManualCapture)
		return
	}
	grant, err := handler.authority.IssueManualCapture(
		request.Context(),
		manualPrincipal(request.Context()),
		capturegrant.ManualCaptureRequest{
			DisplayName:       input.DisplayName,
			ClientClass:       input.ClientClass,
			Lifetime:          input.Lifetime,
			ExpiresIn:         expiresIn,
			ConfirmationToken: input.ConfirmationToken,
		},
	)
	if err != nil {
		handler.writeFailure(writer, err)
		return
	}
	setManualCaptureETag(writer, grant.Capture.Capture)
	writeJSON(writer, http.StatusCreated, manualGrantWire(grant))
}

func (handler *ManualHandler) get(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if !emptyBody(request.Body) || request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidManualCapture)
		return
	}
	id, err := manualcapture.ParseID(request.PathValue("manualCaptureId"))
	if err != nil {
		writeProblem(writer, http.StatusNotFound, ReasonManualCaptureNotFound)
		return
	}
	view, err := handler.authority.GetManualCapture(
		request.Context(),
		manualPrincipal(request.Context()),
		id,
	)
	if err != nil {
		handler.writeFailure(writer, err)
		return
	}
	setManualCaptureETag(writer, view)
	writeJSON(writer, http.StatusOK, manualViewWire(view))
}

func (handler *ManualHandler) rotate(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if !emptyBody(request.Body) || request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidManualCapture)
		return
	}
	id, expectedTag, ok := manualMutationCoordinates(request)
	if !ok {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidManualCapture)
		return
	}
	current, err := handler.authority.GetManualCapture(
		request.Context(),
		manualPrincipal(request.Context()),
		id,
	)
	if err != nil {
		handler.writeFailure(writer, err)
		return
	}
	if expectedTag != manualCaptureETag(current) {
		writeProblem(writer, http.StatusConflict, ReasonManualCaptureConflict)
		return
	}
	grant, err := handler.authority.RotateManualCapture(
		request.Context(),
		manualPrincipal(request.Context()),
		capturegrant.ManualCaptureRotateRequest{
			ID:                         id,
			ExpectedCredentialRevision: current.CredentialRevision,
		},
	)
	if err != nil {
		handler.writeFailure(writer, err)
		return
	}
	setManualCaptureETag(writer, grant.Capture.Capture)
	writeJSON(writer, http.StatusOK, manualGrantWire(grant))
}

func (handler *ManualHandler) revoke(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if !emptyBody(request.Body) || request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidManualCapture)
		return
	}
	id, expectedTag, ok := manualMutationCoordinates(request)
	if !ok {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidManualCapture)
		return
	}
	current, err := handler.authority.GetManualCapture(
		request.Context(),
		manualPrincipal(request.Context()),
		id,
	)
	if err != nil {
		handler.writeFailure(writer, err)
		return
	}
	if expectedTag != manualCaptureETag(current) {
		writeProblem(writer, http.StatusConflict, ReasonManualCaptureConflict)
		return
	}
	if _, err := handler.authority.RevokeManualCapture(
		request.Context(),
		manualPrincipal(request.Context()),
		capturegrant.ManualCaptureRevokeRequest{
			ID:                         id,
			ExpectedCredentialRevision: current.CredentialRevision,
		},
	); err != nil {
		handler.writeFailure(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *ManualHandler) invalidRoute(
	writer http.ResponseWriter,
	_ *http.Request,
) {
	writeProblem(writer, http.StatusNotFound, ReasonInvalidRoute)
}

func (handler *ManualHandler) writeFailure(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, capturegrant.ErrPrincipalUnauthorized):
		writeProblem(writer, http.StatusForbidden, ReasonCaptureGrantNotAllowed)
	case errors.Is(err, capturegrant.ErrInvalidManualCapture):
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidManualCapture)
	case errors.Is(err, capturegrant.ErrManualCaptureNotFound):
		writeProblem(writer, http.StatusNotFound, ReasonManualCaptureNotFound)
	case errors.Is(err, capturegrant.ErrManualCaptureConflict):
		writeProblem(writer, http.StatusConflict, ReasonManualCaptureConflict)
	default:
		writeProblem(writer, http.StatusServiceUnavailable, ReasonManualCaptureUnavailable)
	}
}

func manualCaptureExpiry(input ManualCaptureCreateRequest) (time.Duration, bool) {
	switch input.Lifetime {
	case manualcapture.LifetimeUntilRevoked:
		return 0, input.ExpiresInSeconds == nil
	case manualcapture.LifetimeTemporary:
		if input.ExpiresInSeconds == nil ||
			*input.ExpiresInSeconds < int64(manualcapture.MinimumTemporaryLifetime/time.Second) ||
			*input.ExpiresInSeconds > int64(manualcapture.MaximumTemporaryLifetime/time.Second) {
			return 0, false
		}
		return time.Duration(*input.ExpiresInSeconds) * time.Second, true
	default:
		return 0, false
	}
}

func manualMutationCoordinates(
	request *http.Request,
) (manualcapture.ID, string, bool) {
	id, err := manualcapture.ParseID(request.PathValue("manualCaptureId"))
	values := request.Header.Values("If-Match")
	request.Header.Del("If-Match")
	if err != nil || len(values) != 1 || !validManualCaptureETag(values[0]) {
		return manualcapture.ID{}, "", false
	}
	return id, values[0], true
}

func manualContextWire(context capturegrant.ManualCaptureContext) ManualCaptureContext {
	return ManualCaptureContext{
		ConfirmationToken:       context.ConfirmationToken,
		ProxyAddress:            context.ProxyAddress,
		Root:                    rootDeliveryWire(context),
		DefaultTemporarySeconds: int64(context.DefaultTemporaryLifetime / time.Second),
		MaxTemporarySeconds:     int64(context.MaximumTemporaryLifetime / time.Second),
	}
}

func manualGrantWire(grant capturegrant.ManualCaptureGrant) ManualCaptureGrant {
	return ManualCaptureGrant{
		Capture:       manualViewWire(grant.Capture.Capture),
		ProxyAddress:  grant.Context.ProxyAddress,
		ProxyUsername: manualcapture.ProxyUsername,
		ProxyPassword: grant.Capture.Credential.Value(),
		Root:          rootDeliveryWire(grant.Context),
	}
}

func rootDeliveryWire(context capturegrant.ManualCaptureContext) RootPublicDelivery {
	return RootPublicDelivery{
		Kind:        "local_path",
		DERSHA256:   context.RootIdentity.Digest().String(),
		Fingerprint: context.RootIdentity.Fingerprint(),
		PEMPath:     context.RootCertificate.Path(),
	}
}

func manualPageWire(page manualcapture.Page) ManualCapturePage {
	result := ManualCapturePage{Items: make([]ManualCaptureView, 0, len(page.Items))}
	for _, item := range page.Items {
		result.Items = append(result.Items, manualViewWire(item))
	}
	return result
}

func manualViewWire(view manualcapture.View) ManualCaptureView {
	return ManualCaptureView{
		ID:               view.ID,
		IngressProfileID: view.IngressProfileID,
		DisplayName:      view.DisplayName,
		ClientClass:      view.ClientClass,
		Lifetime:         view.Lifetime,
		State:            view.State,
		Observation:      view.Observation,
		CreatedAt:        view.CreatedAt,
		UpdatedAt:        view.UpdatedAt,
		ExpiresAt:        view.ExpiresAt,
		LastObservedAt:   view.LastObservedAt,
	}
}

func setManualCaptureETag(writer http.ResponseWriter, view manualcapture.View) {
	writer.Header().Set("ETag", manualCaptureETag(view))
}

func manualCaptureETag(view manualcapture.View) string {
	value := fmt.Sprintf(
		"vibermate:manual-capture-state:v1\x00%s\x00%d\x00%s",
		view.ID,
		view.CredentialRevision,
		view.State,
	)
	digest := sha256.Sum256([]byte(value))
	return `"mc_` + base64.RawURLEncoding.EncodeToString(digest[:]) + `"`
}

func validManualCaptureETag(value string) bool {
	const prefix = `"mc_`
	if len(value) != len(prefix)+base64.RawURLEncoding.EncodedLen(sha256.Size)+1 ||
		!strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, `"`) {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(
		strings.TrimSuffix(strings.TrimPrefix(value, prefix), `"`),
	)
	return err == nil
}

type manualPrincipalContextKey struct{}

func withManualPrincipal(
	ctx context.Context,
	principal controlprincipal.Principal,
) context.Context {
	return context.WithValue(ctx, manualPrincipalContextKey{}, principal)
}

func manualPrincipal(ctx context.Context) controlprincipal.Principal {
	principal, _ := ctx.Value(manualPrincipalContextKey{}).(controlprincipal.Principal)
	return principal
}
