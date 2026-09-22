package desktopcontrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

type ProviderAccountKind string

const (
	ProviderAccountKindAnthropicAPIKey ProviderAccountKind = "anthropic_api_key"
	ProviderAccountKindBearerToken     ProviderAccountKind = "bearer_token"
	ProviderAccountKindCodexOAuth      ProviderAccountKind = "codex_oauth"
)

type CodexOAuthResponse struct {
	ChatGPTAccountID string           `json:"chatgptAccountId"`
	Email            string           `json:"email,omitempty"`
	UserID           string           `json:"userId,omitempty"`
	PlanType         string           `json:"planType,omitempty"`
	FedRAMP          bool             `json:"fedRamp"`
	ExpiresAt        string           `json:"expiresAt,omitempty"`
	LastRefresh      string           `json:"lastRefresh"`
	State            codexoauth.State `json:"state"`
}

// ProviderTokenInfoResponse contains display-only JWT claims, not verified
// identity or credential health. Never add raw claims or token bytes here.
type ProviderTokenInfoResponse struct {
	ChatGPTAccountID string `json:"chatgptAccountId,omitempty"`
	Email            string `json:"email,omitempty"`
	UserID           string `json:"userId,omitempty"`
	PlanType         string `json:"planType,omitempty"`
	IssuedAt         string `json:"issuedAt,omitempty"`
	AuthenticatedAt  string `json:"authenticatedAt,omitempty"`
	ExpiresAt        string `json:"expiresAt,omitempty"`
}

type ProviderAccountResponse struct {
	ID                  string                      `json:"id"`
	DisplayName         string                      `json:"displayName"`
	Note                string                      `json:"note"`
	NoteRevision        uint64                      `json:"noteRevision"`
	CredentialOrigin    string                      `json:"credentialOrigin"`
	LinkedEndpointIDs   []upstreamendpoint.ID       `json:"linkedEndpointIds"`
	AssociationRevision uint64                      `json:"associationRevision"`
	Kind                ProviderAccountKind         `json:"kind"`
	RealmID             string                      `json:"realmId"`
	State               provideraccount.State       `json:"state"`
	Revision            uint64                      `json:"revision"`
	CredentialState     provideraccount.HealthState `json:"credentialState"`
	CredentialEpoch     uint64                      `json:"credentialEpoch"`
	SetHeaderNames      []string                    `json:"setHeaderNames"`
	DeleteHeaderNames   []string                    `json:"deleteHeaderNames"`
	CodexOAuth          *CodexOAuthResponse         `json:"codexOAuth,omitempty"`
	TokenInfo           *ProviderTokenInfoResponse  `json:"tokenInfo,omitempty"`
}

type ProviderAccountPage struct {
	Items []ProviderAccountResponse `json:"items"`
}

type ProviderAccountCreateInput struct {
	ID                 string              `json:"id"`
	DisplayName        string              `json:"displayName"`
	UpstreamEndpointID string              `json:"upstreamEndpointId"`
	Unlinked           bool                `json:"unlinked"`
	Kind               ProviderAccountKind `json:"kind"`
	Secret             string              `json:"secret"`
	CodexAuthJSON      string              `json:"codexAuthJson"`
	SetHeaders         map[string]string   `json:"setHeaders"`
	DeleteHeaders      []string            `json:"deleteHeaders"`
}

type ProviderAccountCredentialInput struct {
	Secret        string            `json:"secret"`
	CodexAuthJSON string            `json:"codexAuthJson"`
	SetHeaders    map[string]string `json:"setHeaders"`
	DeleteHeaders []string          `json:"deleteHeaders"`
}

type ProviderAccountReferenceResponse struct {
	EnvironmentID       string `json:"environmentId"`
	EnvironmentName     string `json:"environmentName"`
	EnvironmentRevision uint64 `json:"environmentRevision"`
	RouteID             string `json:"routeId"`
	RouteRevision       uint64 `json:"routeRevision"`
}

type ProviderAccountDeleteResponse struct {
	Deleted        bool                               `json:"deleted"`
	ReferenceCount uint64                             `json:"referenceCount"`
	References     []ProviderAccountReferenceResponse `json:"references"`
}

const providerAccountReferenceLimit = 50

func (handler *Handler) listProviderAccounts(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	views, err := handler.accounts.List(request.Context())
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonProviderAccountUnavailable)
		return
	}
	page := ProviderAccountPage{Items: make([]ProviderAccountResponse, len(views))}
	for index, view := range views {
		response, responseErr := handler.providerAccountResponse(request.Context(), view)
		if responseErr != nil {
			writeProblem(writer, http.StatusServiceUnavailable, ReasonProviderAccountUnavailable)
			return
		}
		page.Items[index] = response
	}
	writeJSON(writer, http.StatusOK, page)
}

func (handler *Handler) getProviderAccount(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	id, err := provideraccount.NewID(request.PathValue("accountId"))
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	view, err := handler.accounts.Get(request.Context(), id)
	if err != nil {
		spec := classifyProviderAccountError(err)
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	response, err := handler.providerAccountResponse(request.Context(), view)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonProviderAccountUnavailable)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) createProviderAccount(writer http.ResponseWriter, request *http.Request) {
	expected, key, headerErr := mutationHeaders(request)
	body, bodyErr := readJSONBody(request)
	defer clear(body)
	var input ProviderAccountCreateInput
	if headerErr != nil || bodyErr != nil || expected != 0 || decodeStrictJSON(body, &input) != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	id, idErr := provideraccount.NewID(input.ID)
	endpointID, endpointErr := upstreamendpoint.NewID(input.UpstreamEndpointID)
	driver, kindErr := providerAccountKindAuthority(input.Kind)
	secret, secretErr := providerAccountCredentialValue(
		driver,
		input.Secret,
		input.CodexAuthJSON,
		input.SetHeaders,
		input.DeleteHeaders,
	)
	input.Secret = ""
	input.CodexAuthJSON = ""
	clearHeaderValues(input.SetHeaders)
	input.SetHeaders = nil
	input.DeleteHeaders = nil
	if idErr != nil || endpointErr != nil || kindErr != nil || secretErr != nil {
		if secret != nil {
			secret.Destroy()
		}
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	defer secret.Destroy()
	fingerprint := sha256.Sum256(bytes.Join([][]byte{
		[]byte(request.Method), []byte(request.URL.Path), []byte("0"), body,
	}, []byte{0}))
	response, err := handler.idempotent.execute(request.Context(), key, fingerprint, func() cachedResponse {
		view, createErr := handler.accounts.Create(request.Context(), provideraccount.CreateCommand{
			ID: id, DisplayName: input.DisplayName, UpstreamEndpointID: endpointID, Unlinked: input.Unlinked,
			Driver: driver, Secret: secret,
		})
		if createErr != nil {
			return problemResponse(classifyProviderAccountError(createErr))
		}
		result, responseErr := handler.providerAccountResponse(request.Context(), view)
		if responseErr != nil {
			return problemResponse(problemSpec{status: http.StatusServiceUnavailable, reason: ReasonProviderAccountUnavailable})
		}
		return jsonResponse(http.StatusCreated, result)
	})
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonProviderAccountConflict)
		return
	}
	writeCached(writer, response)
}

func (handler *Handler) replaceProviderAccountCredential(writer http.ResponseWriter, request *http.Request) {
	expected, key, headerErr := mutationHeaders(request)
	body, bodyErr := readJSONBody(request)
	defer clear(body)
	var input ProviderAccountCredentialInput
	id, idErr := provideraccount.NewID(request.PathValue("accountId"))
	if headerErr != nil || bodyErr != nil || idErr != nil ||
		decodeStrictJSON(body, &input) != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	current, currentErr := handler.accounts.Get(request.Context(), id)
	if currentErr != nil {
		spec := classifyProviderAccountError(currentErr)
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	secret, secretErr := providerAccountCredentialValue(
		current.Account.Driver,
		input.Secret,
		input.CodexAuthJSON,
		input.SetHeaders,
		input.DeleteHeaders,
	)
	input.Secret = ""
	input.CodexAuthJSON = ""
	clearHeaderValues(input.SetHeaders)
	input.SetHeaders = nil
	input.DeleteHeaders = nil
	if secretErr != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	defer secret.Destroy()
	fingerprint := sha256.Sum256(bytes.Join([][]byte{
		[]byte(request.Method), []byte(request.URL.Path),
		[]byte(strconv.FormatUint(expected, 10)), body,
	}, []byte{0}))
	response, err := handler.idempotent.execute(request.Context(), key, fingerprint, func() cachedResponse {
		view, replaceErr := handler.accounts.ReplaceSecret(request.Context(), provideraccount.ReplaceSecretCommand{
			ID: id, ExpectedCredentialEpoch: expected, Secret: secret,
		})
		if replaceErr != nil {
			return problemResponse(classifyProviderAccountError(replaceErr))
		}
		result, responseErr := handler.providerAccountResponse(request.Context(), view)
		if responseErr != nil {
			return problemResponse(problemSpec{status: http.StatusServiceUnavailable, reason: ReasonProviderAccountUnavailable})
		}
		return jsonResponse(http.StatusOK, result)
	})
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonProviderAccountConflict)
		return
	}
	writeCached(writer, response)
}

func (handler *Handler) deleteProviderAccount(writer http.ResponseWriter, request *http.Request) {
	expected, key, headerErr := mutationHeaders(request)
	id, idErr := provideraccount.NewID(request.PathValue("accountId"))
	if headerErr != nil || idErr != nil || !emptyBody(request.Body) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := sha256.Sum256([]byte(
		request.Method + "\x00" + request.URL.Path + "\x00" + strconv.FormatUint(expected, 10),
	))
	response, err := handler.idempotent.execute(request.Context(), key, fingerprint, func() cachedResponse {
		result, deleteErr := handler.accounts.Delete(request.Context(), provideraccount.DeleteCommand{
			ID: id, ExpectedCredentialEpoch: expected,
		})
		if deleteErr != nil {
			return problemResponse(classifyProviderAccountError(deleteErr))
		}
		referenceCount := len(result.References)
		visibleReferences := result.References
		if len(visibleReferences) > providerAccountReferenceLimit {
			visibleReferences = visibleReferences[:providerAccountReferenceLimit]
		}
		references := make([]ProviderAccountReferenceResponse, len(visibleReferences))
		for index, reference := range visibleReferences {
			references[index] = ProviderAccountReferenceResponse{
				EnvironmentID:       reference.EnvironmentID.String(),
				EnvironmentName:     reference.EnvironmentName,
				EnvironmentRevision: uint64(reference.EnvironmentRevision),
				RouteID:             reference.RouteID.String(),
				RouteRevision:       uint64(reference.RouteRevision),
			}
		}
		return jsonResponse(http.StatusOK, ProviderAccountDeleteResponse{
			Deleted: result.Deleted, ReferenceCount: uint64(referenceCount), References: references,
		})
	})
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonProviderAccountConflict)
		return
	}
	writeCached(writer, response)
}

func providerAccountResponseOf(view provideraccount.View) (ProviderAccountResponse, error) {
	kind, err := providerAccountKindOf(view.Account)
	if err != nil || view.Account.Validate() != nil || view.Health.Validate() != nil {
		return ProviderAccountResponse{}, provideraccount.ErrInvalidAccount
	}
	return ProviderAccountResponse{
		ID: view.Account.ID.String(), DisplayName: view.Account.DisplayName,
		Note: view.Account.Note, NoteRevision: view.Account.NoteRevision,
		CredentialOrigin: view.Account.Origin.String(), LinkedEndpointIDs: view.Account.Associations.IDs(), AssociationRevision: view.Account.AssociationRevision,
		Kind: kind, RealmID: view.Account.RealmID, State: view.Account.State,
		Revision: view.Account.Revision, CredentialState: view.Health.State,
		CredentialEpoch: view.Health.CredentialEpoch,
		SetHeaderNames:  append([]string{}, view.SetHeaderNames...),
		DeleteHeaderNames: append(
			[]string{},
			view.DeleteHeaderNames...,
		),
	}, nil
}

func (handler *Handler) providerAccountResponse(
	ctx context.Context,
	view provideraccount.View,
) (ProviderAccountResponse, error) {
	response, err := providerAccountResponseOf(view)
	if err != nil || view.Health.State != provideraccount.HealthReady {
		return response, err
	}
	if view.Account.Driver == providerauth.StaticHeaderDriverRef() &&
		upstreamendpoint.IsChatGPTCodexOrigin(view.Account.Origin) && handler.codexOAuth != nil {
		profile, inspectErr := handler.codexOAuth.InspectAccessToken(ctx, view.Account.SecretRef, secretstore.Revision(view.Health.CredentialEpoch))
		if inspectErr == nil {
			response.TokenInfo = providerTokenInfoResponseOf(profile)
		}
		// Display metadata is optional; an opaque token or a concurrent credential
		// update must not make an otherwise readable account disappear.
		return response, nil
	}
	if view.Account.Driver != providerauth.CodexOAuthDriverRef() {
		return response, nil
	}
	if handler.codexOAuth == nil {
		return ProviderAccountResponse{}, codexoauth.ErrRefreshUnavailable
	}
	oauth, err := handler.codexOAuth.Inspect(
		ctx,
		view.Account.SecretRef,
		secretstore.Revision(view.Health.CredentialEpoch),
	)
	if err != nil {
		return ProviderAccountResponse{}, err
	}
	profile := oauth.Profile
	projection := &CodexOAuthResponse{
		ChatGPTAccountID: profile.AccountID,
		Email:            profile.Email, UserID: profile.UserID, PlanType: profile.PlanType,
		FedRAMP:     profile.FedRAMP,
		LastRefresh: profile.LastRefresh.UTC().Format(time.RFC3339Nano),
		State:       oauth.State,
	}
	if !profile.ExpiresAt.IsZero() {
		projection.ExpiresAt = profile.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	response.CodexOAuth = projection
	response.TokenInfo = providerTokenInfoResponseOf(profile)
	return response, nil
}

func providerTokenInfoResponseOf(profile codexoauth.Profile) *ProviderTokenInfoResponse {
	projection := ProviderTokenInfoResponse{
		ChatGPTAccountID: profile.AccountID, Email: profile.Email,
		UserID: profile.UserID, PlanType: profile.PlanType,
	}
	if !profile.IssuedAt.IsZero() {
		projection.IssuedAt = profile.IssuedAt.UTC().Format(time.RFC3339Nano)
	}
	if !profile.AuthenticatedAt.IsZero() {
		projection.AuthenticatedAt = profile.AuthenticatedAt.UTC().Format(time.RFC3339Nano)
	}
	if !profile.ExpiresAt.IsZero() {
		projection.ExpiresAt = profile.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	if projection == (ProviderTokenInfoResponse{}) {
		return nil
	}
	return &projection
}

func providerAccountCredentialValue(
	driver providerauth.DriverRef,
	credential string,
	codexAuthJSON string,
	setHeaders map[string]string,
	deleteHeaders []string,
) (*secretstore.Value, error) {
	switch driver {
	case providerauth.CodexOAuthDriverRef():
		if credential != "" || codexAuthJSON == "" {
			return nil, provideraccount.ErrInvalidAccount
		}
		encodedImport := []byte(codexAuthJSON)
		defer clear(encodedImport)
		imported, err := codexoauth.ImportAuthJSON(encodedImport)
		if err != nil {
			return nil, err
		}
		defer imported.Destroy()
		encodedCredential, err := imported.MarshalBinary()
		if err != nil {
			return nil, err
		}
		defer clear(encodedCredential)
		credential = string(encodedCredential)
	case providerauth.StaticHeaderDriverRef(), providerauth.AnthropicAPIKeyDriverRef():
		if credential == "" || codexAuthJSON != "" {
			return nil, provideraccount.ErrInvalidAccount
		}
	default:
		return nil, provideraccount.ErrInvalidAccount
	}
	material, err := providerauth.NewMaterial(credential, setHeaders, deleteHeaders)
	if err != nil {
		return nil, err
	}
	defer material.Destroy()
	if err := material.HeaderPolicy().ValidateForDriver(driver); err != nil {
		return nil, err
	}
	encoded, err := material.MarshalBinary()
	if err != nil {
		return nil, err
	}
	defer clear(encoded)
	return secretstore.NewValue(encoded)
}

func clearHeaderValues(headers map[string]string) {
	for name := range headers {
		headers[name] = ""
	}
}

func providerAccountKindAuthority(kind ProviderAccountKind) (providerauth.DriverRef, error) {
	switch kind {
	case ProviderAccountKindAnthropicAPIKey:
		return providerauth.AnthropicAPIKeyDriverRef(), nil
	case ProviderAccountKindBearerToken:
		return providerauth.StaticHeaderDriverRef(), nil
	case ProviderAccountKindCodexOAuth:
		return providerauth.CodexOAuthDriverRef(), nil
	default:
		return providerauth.DriverRef{}, provideraccount.ErrInvalidAccount
	}
}

func providerAccountKindOf(account provideraccount.Account) (ProviderAccountKind, error) {
	switch {
	case account.Driver == providerauth.AnthropicAPIKeyDriverRef():
		return ProviderAccountKindAnthropicAPIKey, nil
	case account.Driver == providerauth.StaticHeaderDriverRef():
		return ProviderAccountKindBearerToken, nil
	case account.Driver == providerauth.CodexOAuthDriverRef():
		return ProviderAccountKindCodexOAuth, nil
	default:
		return "", provideraccount.ErrInvalidAccount
	}
}

func classifyProviderAccountError(err error) problemSpec {
	switch {
	case errors.Is(err, provideraccount.ErrAccountNotFound):
		return problemSpec{status: http.StatusNotFound, reason: ReasonProviderAccountNotFound}
	case errors.Is(err, provideraccount.ErrRevisionConflict):
		return problemSpec{status: http.StatusConflict, reason: ReasonProviderAccountConflict}
	case errors.Is(err, provideraccount.ErrOperationInProgress):
		return problemSpec{status: http.StatusConflict, reason: ReasonProviderAccountConflict}
	case errors.Is(err, provideraccount.ErrAccountInUse):
		return problemSpec{status: http.StatusConflict, reason: ReasonProviderAccountInUse}
	case errors.Is(err, provideraccount.ErrInvalidAccount), errors.Is(err, provideraccount.ErrEndpointMismatch):
		return problemSpec{status: http.StatusUnprocessableEntity, reason: ReasonInvalidRequest}
	case errors.Is(err, upstreamendpoint.ErrEndpointNotFound):
		return problemSpec{status: http.StatusNotFound, reason: ReasonUpstreamEndpointNotFound}
	default:
		return problemSpec{status: http.StatusServiceUnavailable, reason: ReasonProviderAccountUnavailable}
	}
}
