package providertransport

import (
	"context"
	"errors"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

type CredentialEvidence struct {
	DriverRef  string
	HeaderName string
	SecretRead bool
}

type Authenticator interface {
	Ref() access.AuthDriverRef
	Apply(
		context.Context,
		*http.Request,
		secretstore.Reference,
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

func (*StaticBearerAuthenticator) Ref() access.AuthDriverRef {
	return access.StaticHeaderAuthDriverRef()
}

func (authenticator *StaticBearerAuthenticator) Apply(
	ctx context.Context,
	request *http.Request,
	reference secretstore.Reference,
	target Target,
) (CredentialEvidence, error) {
	if ctx == nil || request == nil || request.URL == nil {
		return CredentialEvidence{}, errors.New("final provider request is missing")
	}
	if err := target.validateRequestIdentity(request); err != nil {
		return CredentialEvidence{}, err
	}
	value, err := authenticator.secrets.Read(ctx, reference)
	value, err = secretstore.ValidateReaderResult(value, err)
	if err != nil {
		return CredentialEvidence{}, err
	}
	defer value.Destroy()
	secret, err := value.CopyBytes()
	if err != nil {
		return CredentialEvidence{}, err
	}
	defer clear(secret)
	stripProviderCredentialHeaders(request.Header)
	request.Header.Set("Authorization", "Bearer "+string(secret))
	return CredentialEvidence{
		DriverRef:  authenticator.Ref().String(),
		HeaderName: "authorization",
		SecretRead: true,
	}, nil
}

func stripProviderCredentialHeaders(header http.Header) {
	for _, name := range []string{
		"Authorization",
		"Proxy-Authorization",
		"Cookie",
		"X-Api-Key",
		"Api-Key",
	} {
		header.Del(name)
	}
}
