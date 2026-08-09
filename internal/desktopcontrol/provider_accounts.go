package desktopcontrol

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"net/http"
	"strconv"

	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

type ProviderAccountKind string

const (
	ProviderAccountKindAnthropicAPIKey ProviderAccountKind = "anthropic_api_key"
	ProviderAccountKindClaudeOAuth     ProviderAccountKind = "claude_oauth_token"
	ProviderAccountKindOpenAIAPIKey    ProviderAccountKind = "openai_api_key"
)

type ProviderAccountResponse struct {
	ID              string                      `json:"id"`
	DisplayName     string                      `json:"displayName"`
	Kind            ProviderAccountKind         `json:"kind"`
	RealmID         string                      `json:"realmId"`
	State           provideraccount.State       `json:"state"`
	Revision        uint64                      `json:"revision"`
	CredentialState provideraccount.HealthState `json:"credentialState"`
	CredentialEpoch uint64                      `json:"credentialEpoch"`
}

type ProviderAccountPage struct {
	Items []ProviderAccountResponse `json:"items"`
}

type ProviderAccountCreateInput struct {
	ID          string              `json:"id"`
	DisplayName string              `json:"displayName"`
	Kind        ProviderAccountKind `json:"kind"`
	Secret      string              `json:"secret"`
}

type ProviderAccountCredentialInput struct {
	Secret string `json:"secret"`
}

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
		response, responseErr := providerAccountResponseOf(view)
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
	response, err := providerAccountResponseOf(view)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonProviderAccountUnavailable)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) createProviderAccount(writer http.ResponseWriter, request *http.Request) {
	expected, key, headerErr := mutationHeaders(request)
	body, bodyErr := readJSONBody(request)
	var input ProviderAccountCreateInput
	if headerErr != nil || bodyErr != nil || expected != 0 || decodeStrictJSON(body, &input) != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	id, idErr := provideraccount.NewID(input.ID)
	realmID, driver, kindErr := providerAccountKindAuthority(input.Kind)
	secret, secretErr := secretstore.NewValue([]byte(input.Secret))
	input.Secret = ""
	if idErr != nil || kindErr != nil || secretErr != nil {
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
			ID: id, DisplayName: input.DisplayName, RealmID: realmID,
			Driver: driver, Secret: secret,
		})
		if createErr != nil {
			return problemResponse(classifyProviderAccountError(createErr))
		}
		result, responseErr := providerAccountResponseOf(view)
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
	var input ProviderAccountCredentialInput
	id, idErr := provideraccount.NewID(request.PathValue("accountId"))
	if headerErr != nil || bodyErr != nil || idErr != nil ||
		decodeStrictJSON(body, &input) != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	secret, secretErr := secretstore.NewValue([]byte(input.Secret))
	input.Secret = ""
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
		result, responseErr := providerAccountResponseOf(view)
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

func providerAccountResponseOf(view provideraccount.View) (ProviderAccountResponse, error) {
	kind, err := providerAccountKindOf(view.Account)
	if err != nil || view.Account.Validate() != nil || view.Health.Validate() != nil {
		return ProviderAccountResponse{}, provideraccount.ErrInvalidAccount
	}
	return ProviderAccountResponse{
		ID: view.Account.ID.String(), DisplayName: view.Account.DisplayName,
		Kind: kind, RealmID: view.Account.RealmID, State: view.Account.State,
		Revision: view.Account.Revision, CredentialState: view.Health.State,
		CredentialEpoch: view.Health.CredentialEpoch,
	}, nil
}

func providerAccountKindAuthority(kind ProviderAccountKind) (string, providerauth.DriverRef, error) {
	switch kind {
	case ProviderAccountKindAnthropicAPIKey:
		return "anthropic.official", providerauth.AnthropicAPIKeyDriverRef(), nil
	case ProviderAccountKindClaudeOAuth:
		return "anthropic.official", providerauth.StaticHeaderDriverRef(), nil
	case ProviderAccountKindOpenAIAPIKey:
		return "openai.platform", providerauth.StaticHeaderDriverRef(), nil
	default:
		return "", providerauth.DriverRef{}, provideraccount.ErrInvalidAccount
	}
}

func providerAccountKindOf(account provideraccount.Account) (ProviderAccountKind, error) {
	switch {
	case account.RealmID == "anthropic.official" && account.Driver == providerauth.AnthropicAPIKeyDriverRef():
		return ProviderAccountKindAnthropicAPIKey, nil
	case account.RealmID == "anthropic.official" && account.Driver == providerauth.StaticHeaderDriverRef():
		return ProviderAccountKindClaudeOAuth, nil
	case account.RealmID == "openai.platform" && account.Driver == providerauth.StaticHeaderDriverRef():
		return ProviderAccountKindOpenAIAPIKey, nil
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
	case errors.Is(err, provideraccount.ErrInvalidAccount):
		return problemSpec{status: http.StatusUnprocessableEntity, reason: ReasonInvalidRequest}
	default:
		return problemSpec{status: http.StatusServiceUnavailable, reason: ReasonProviderAccountUnavailable}
	}
}
