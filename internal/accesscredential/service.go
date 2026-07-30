// Package accesscredential binds write-only credential control resources to
// the sole active AccessPlanSnapshot and the host SecretStore.
package accesscredential

import (
	"context"
	"errors"
	"fmt"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

var (
	ErrInvalidCredential  = errors.New("credential request is invalid")
	ErrCredentialNotFound = errors.New("credential is not configured in the active Access plan")
)

type View struct {
	CredentialID   string               `json:"credentialId"`
	ProfileID      string               `json:"profileId"`
	SecretState    secretstore.State    `json:"secretState"`
	SecretRevision secretstore.Revision `json:"secretRevision"`
}

type ReplaceCommand struct {
	AccessID         access.AccessID
	ProfileID        access.EndpointProfileID
	CredentialID     access.AccountBindingID
	ExpectedRevision secretstore.Revision
	Value            *secretstore.Value
}

type Controller interface {
	GetCredential(
		context.Context,
		access.AccessID,
		access.EndpointProfileID,
		access.AccountBindingID,
	) (View, error)
	ReplaceSecret(context.Context, ReplaceCommand) (View, error)
}

type Service struct {
	resolver access.SnapshotResolver
	secrets  secretstore.Store
}

var _ Controller = (*Service)(nil)

func New(
	resolver access.SnapshotResolver,
	secrets secretstore.Store,
) (*Service, error) {
	if resolver == nil || secrets == nil {
		return nil, errors.New("credential service dependencies are incomplete")
	}
	return &Service{resolver: resolver, secrets: secrets}, nil
}

func (service *Service) GetCredential(
	ctx context.Context,
	accessID access.AccessID,
	profileID access.EndpointProfileID,
	credentialID access.AccountBindingID,
) (View, error) {
	if ctx == nil {
		return View{}, errors.New("credential read context is nil")
	}
	binding, err := service.resolveBinding(accessID, profileID, credentialID)
	if err != nil {
		return View{}, err
	}
	reference, err := secretstore.ParseReference(binding.SecretRef.String())
	if err != nil {
		return View{}, fmt.Errorf(
			"%w: active credential SecretRef is invalid",
			ErrInvalidCredential,
		)
	}
	metadata, err := service.secrets.Inspect(ctx, reference)
	if err != nil {
		switch {
		case errors.Is(err, secretstore.ErrUnavailable),
			errors.Is(err, secretstore.ErrLocked),
			errors.Is(err, secretstore.ErrDenied):
			return view(binding, secretstore.Metadata{
				State: secretstore.StateUnavailable,
			}), nil
		default:
			return View{}, err
		}
	}
	if err := metadata.Validate(); err != nil {
		return View{}, fmt.Errorf(
			"%w: SecretStore returned invalid metadata",
			secretstore.ErrUnavailable,
		)
	}
	return view(binding, metadata), nil
}

func (service *Service) ReplaceSecret(
	ctx context.Context,
	command ReplaceCommand,
) (View, error) {
	if ctx == nil {
		return View{}, errors.New("credential replacement context is nil")
	}
	if command.Value == nil ||
		command.ExpectedRevision > secretstore.MaxRevision {
		return View{}, ErrInvalidCredential
	}
	binding, err := service.resolveBinding(
		command.AccessID,
		command.ProfileID,
		command.CredentialID,
	)
	if err != nil {
		return View{}, err
	}
	reference, err := secretstore.ParseReference(binding.SecretRef.String())
	if err != nil {
		return View{}, fmt.Errorf(
			"%w: active credential SecretRef is invalid",
			ErrInvalidCredential,
		)
	}
	metadata, err := service.secrets.Replace(ctx, secretstore.ReplaceCommand{
		Reference:        reference,
		ExpectedRevision: command.ExpectedRevision,
		Value:            command.Value,
	})
	if err != nil {
		return View{}, err
	}
	if err := metadata.Validate(); err != nil ||
		(metadata.State != secretstore.StateConfigured &&
			metadata.State != secretstore.StateUnavailable) ||
		metadata.Revision == 0 {
		return View{}, fmt.Errorf(
			"%w: SecretStore returned an invalid replacement result",
			secretstore.ErrUnavailable,
		)
	}
	return view(binding, metadata), nil
}

func (service *Service) resolveBinding(
	accessID access.AccessID,
	profileID access.EndpointProfileID,
	credentialID access.AccountBindingID,
) (access.ProviderAccountBinding, error) {
	snapshot, err := service.resolver.ResolveAccess(accessID)
	if err != nil {
		if errors.Is(err, access.ErrAccessNotConfigured) {
			return access.ProviderAccountBinding{}, ErrCredentialNotFound
		}
		return access.ProviderAccountBinding{}, err
	}
	profileFound := false
	for _, profile := range snapshot.EndpointProfiles() {
		if profile.ID == profileID && profile.AccessID == accessID {
			profileFound = true
			break
		}
	}
	if !profileFound {
		return access.ProviderAccountBinding{}, ErrCredentialNotFound
	}
	for _, binding := range snapshot.AccountBindings() {
		if binding.ID == credentialID &&
			binding.AccessID == accessID &&
			binding.ProfileID == profileID {
			return binding, nil
		}
	}
	return access.ProviderAccountBinding{}, ErrCredentialNotFound
}

func view(
	binding access.ProviderAccountBinding,
	metadata secretstore.Metadata,
) View {
	return View{
		CredentialID:   binding.ID.String(),
		ProfileID:      binding.ProfileID.String(),
		SecretState:    metadata.State,
		SecretRevision: metadata.Revision,
	}
}
