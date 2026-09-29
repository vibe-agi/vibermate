package serverhost

import (
	"context"

	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

type runtimeUserAuthenticator struct {
	users *runtimeuser.Manager
	// isOwner identifies the Server Owner, whose sessions hold every
	// Environment regardless of the stored member policy.
	isOwner func(runtimeuser.UserID) bool
}

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
		ID:              "runtime-user:" + string(identity.SessionID),
		Kind:            controlprincipal.KindRuntimeUser,
		MachineID:       identity.MachineID.String(),
		DeviceName:      identity.DeviceName,
		RuntimeUserID:   string(identity.User.ID),
		RuntimeUsername: identity.User.Username,
		LoginSessionID:  string(identity.SessionID),
		RuntimeUserPolicy: effectiveRuntimeUserPolicy(
			identity.User.Policy,
			authenticator.isOwner != nil && authenticator.isOwner(identity.User.ID),
		),
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

// effectiveRuntimeUserPolicy grants the Owner every Environment while keeping
// its usage warnings; a member keeps exactly its stored grants.
func effectiveRuntimeUserPolicy(policy runtimeuser.Policy, owner bool) runtimeuser.Policy {
	if !owner {
		return policy
	}
	all, err := runtimeuser.NewPolicy(true, nil, policy.DailyAgentAPICallWarning, policy.DailyTokenWarning)
	if err != nil {
		return policy
	}
	return all
}
