package providertransport

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

type CredentialEvidence struct {
	DriverRef            string
	HeaderName           string
	SecretRead           bool
	ProtectedHeaderNames []string
	// Only the built-in credential readers can attest to this explicit
	// account-owned override. It carries no value into transport diagnostics.
	userAgentMutation accountUserAgentMutation
}

type accountUserAgentMutation uint8

const (
	accountUserAgentUnchanged accountUserAgentMutation = iota
	accountUserAgentSet
	accountUserAgentDelete
)

func userAgentMutationOf(policy providerauth.HeaderPolicy) accountUserAgentMutation {
	for _, assignment := range policy.Set {
		if assignment.Name == "User-Agent" {
			return accountUserAgentSet
		}
	}
	for _, name := range policy.Delete {
		if name == "User-Agent" {
			return accountUserAgentDelete
		}
	}
	return accountUserAgentUnchanged
}

type Authenticator interface {
	Ref() providerauth.DriverRef
	Apply(
		context.Context,
		*http.Request,
		secretstore.Reference,
		secretstore.Revision,
		Target,
	) (CredentialEvidence, error)
}

type StaticBearerAuthenticator struct {
	secrets secretstore.Reader
}

var _ Authenticator = (*StaticBearerAuthenticator)(nil)

func NewStaticBearerAuthenticator(
	secrets secretstore.Reader,
) (*StaticBearerAuthenticator, error) {
	if secrets == nil {
		return nil, errors.New("secret reader is nil")
	}
	return &StaticBearerAuthenticator{secrets: secrets}, nil
}

// AnthropicAPIKeyAuthenticator applies the credential shape required by the
// Anthropic Messages API and by compatible relays that implement that API.
// It deliberately has a distinct plan identity from bearer authentication so
// the selected header cannot be inferred from a provider hostname.
type AnthropicAPIKeyAuthenticator struct {
	secrets secretstore.Reader
}

var _ Authenticator = (*AnthropicAPIKeyAuthenticator)(nil)

func NewAnthropicAPIKeyAuthenticator(
	secrets secretstore.Reader,
) (*AnthropicAPIKeyAuthenticator, error) {
	if secrets == nil {
		return nil, errors.New("secret reader is nil")
	}
	return &AnthropicAPIKeyAuthenticator{secrets: secrets}, nil
}

func (*AnthropicAPIKeyAuthenticator) Ref() providerauth.DriverRef {
	return providerauth.AnthropicAPIKeyDriverRef()
}

func (authenticator *AnthropicAPIKeyAuthenticator) Apply(
	ctx context.Context,
	request *http.Request,
	reference secretstore.Reference,
	revision secretstore.Revision,
	target Target,
) (CredentialEvidence, error) {
	if ctx == nil || request == nil || request.URL == nil {
		return CredentialEvidence{}, errors.New("final provider request is missing")
	}
	if err := target.validateRequestIdentity(request); err != nil {
		return CredentialEvidence{}, err
	}
	value, err := authenticator.secrets.ReadAtRevision(ctx, reference, revision)
	value, err = secretstore.ValidateReaderResult(value, err)
	if err != nil {
		return CredentialEvidence{}, err
	}
	defer value.Destroy()
	encoded, err := value.CopyBytes()
	if err != nil {
		return CredentialEvidence{}, err
	}
	defer clear(encoded)
	material, err := providerauth.ParseMaterial(encoded)
	if err != nil {
		return CredentialEvidence{}, err
	}
	defer material.Destroy()
	secret := material.CredentialBytes()
	defer clear(secret)
	stripProviderCredentialHeaders(request.Header)
	if err := material.HeaderPolicy().Apply(request.Header); err != nil {
		return CredentialEvidence{}, err
	}
	request.Header.Set("X-Api-Key", string(secret))
	return CredentialEvidence{
		DriverRef:            authenticator.Ref().String(),
		HeaderName:           "x-api-key",
		SecretRead:           true,
		ProtectedHeaderNames: protectedHeaderNames("X-Api-Key", material.HeaderPolicy()),
		userAgentMutation:    userAgentMutationOf(material.HeaderPolicy()),
	}, nil
}

func (*StaticBearerAuthenticator) Ref() providerauth.DriverRef {
	return providerauth.StaticHeaderDriverRef()
}

func (authenticator *StaticBearerAuthenticator) Apply(
	ctx context.Context,
	request *http.Request,
	reference secretstore.Reference,
	revision secretstore.Revision,
	target Target,
) (CredentialEvidence, error) {
	if ctx == nil || request == nil || request.URL == nil {
		return CredentialEvidence{}, errors.New("final provider request is missing")
	}
	if err := target.validateRequestIdentity(request); err != nil {
		return CredentialEvidence{}, err
	}
	value, err := authenticator.secrets.ReadAtRevision(ctx, reference, revision)
	value, err = secretstore.ValidateReaderResult(value, err)
	if err != nil {
		return CredentialEvidence{}, err
	}
	defer value.Destroy()
	encoded, err := value.CopyBytes()
	if err != nil {
		return CredentialEvidence{}, err
	}
	defer clear(encoded)
	material, err := providerauth.ParseMaterial(encoded)
	if err != nil {
		return CredentialEvidence{}, err
	}
	defer material.Destroy()
	secret := material.CredentialBytes()
	defer clear(secret)
	stripProviderCredentialHeaders(request.Header)
	if err := material.HeaderPolicy().Apply(request.Header); err != nil {
		return CredentialEvidence{}, err
	}
	request.Header.Set("Authorization", "Bearer "+string(secret))
	protected := protectedHeaderNames("Authorization", material.HeaderPolicy())
	if upstreamendpoint.IsChatGPTCodexOrigin(target.Origin()) {
		// The account identity must come from this selected credential epoch,
		// never from the client's previous login. An explicit account-owned
		// header policy (including Delete) takes precedence over token metadata.
		policy := material.HeaderPolicy()
		explicit := false
		for _, assignment := range policy.Set {
			explicit = explicit || assignment.Name == chatGPTAccountHeader
		}
		for _, name := range policy.Delete {
			explicit = explicit || name == chatGPTAccountHeader
		}
		if !explicit {
			if accountID := chatGPTAccountID(secret); accountID != "" {
				request.Header.Set(chatGPTAccountHeader, accountID)
				protected = append(protected, chatGPTAccountHeader)
				sort.Strings(protected)
			}
		}
	}
	return CredentialEvidence{
		DriverRef:            authenticator.Ref().String(),
		HeaderName:           "authorization",
		SecretRead:           true,
		ProtectedHeaderNames: protected,
		userAgentMutation:    userAgentMutationOf(material.HeaderPolicy()),
	}, nil
}

func protectedHeaderNames(primary string, policy providerauth.HeaderPolicy) []string {
	names := policy.SensitiveHeaderNames()
	primary = http.CanonicalHeaderKey(primary)
	found := false
	for _, name := range names {
		if name == primary {
			found = true
			break
		}
	}
	if !found {
		names = append(names, primary)
	}
	sort.Strings(names)
	return names
}

func stripProtectedCredentialHeaders(
	header http.Header,
	protectedHeaderNames []string,
) {
	stripProviderCredentialHeaders(header)
	for _, name := range protectedHeaderNames {
		header.Del(name)
	}
}

func stripProviderCredentialHeaders(header http.Header) {
	for _, name := range []string{
		"Authorization",
		"Proxy-Authorization",
		"Cookie",
		"X-Api-Key",
		"Api-Key",
		chatGPTAccountHeader,
	} {
		header.Del(name)
	}
}
