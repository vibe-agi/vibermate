package serverhost

import (
	"context"

	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

type runtimeUserAuthenticator struct{ users *runtimeuser.Manager }

func (authenticator runtimeUserAuthenticator) Authenticate(
	ctx context.Context,
	token string,
) (controlprincipal.Principal, bool) {
	if authenticator.users == nil {
		return controlprincipal.Principal{}, false
	}
	identity, err := authenticator.users.Authenticate(ctx, token)
	if err != nil {
		return controlprincipal.Principal{}, false
	}
	principal, err := controlprincipal.New(controlprincipal.Attributes{
		ID:                 "runtime-user:" + string(identity.SessionID),
		Kind:               controlprincipal.KindRuntimeUser,
		MachineID:          identity.MachineID.String(),
		DeviceName:         identity.DeviceName,
		RuntimeUserID:      string(identity.User.ID),
		LoginSessionID:     string(identity.SessionID),
		CredentialRevision: 1,
		AllowedGrantKinds: []controlprincipal.GrantKind{
			controlprincipal.GrantCaptureRun,
		},
	})
	return principal, err == nil
}

var _ interface {
	Authenticate(context.Context, string) (controlprincipal.Principal, bool)
} = runtimeUserAuthenticator{}
