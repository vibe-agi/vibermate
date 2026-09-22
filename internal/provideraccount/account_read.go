package provideraccount

import (
	"context"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/providerauth"
)

// AcquireReadCredential uses frozen Capture authority, never a lookup of the
// current Environment. Revocation and pre-lease OAuth refresh remain shared.
func (manager *Manager) AcquireReadCredential(ctx context.Context, plan environment.RequestPlan) (providerauth.Lease, error) {
	if ctx == nil {
		return nil, ErrInvalidAccount
	}
	route, account, err := plan.AccountReadTarget()
	if err != nil {
		return nil, err
	}
	id, err := NewID(account.ID)
	if err != nil {
		return nil, err
	}
	return manager.acquire(ctx, accountLeaseScope{
		id: id, accountRevision: uint64(account.Revision), realmID: account.RealmID,
		upstreamEndpointID: account.UpstreamEndpointID, upstreamEndpointRevision: uint64(account.UpstreamEndpointRevision),
		upstreamEndpointOrigin: route.ProviderTarget().Origin,
	})
}

// AcquireOwnedReadCredential is reserved for the management-owner boundary.
// An Account Link grants a profile use of an account; the owner's independent
// account inspection neither needs a link nor creates one for Captures.
func (manager *Manager) AcquireOwnedReadCredential(ctx context.Context, id ID) (providerauth.Lease, originidentity.ProviderOrigin, error) {
	if ctx == nil {
		return nil, originidentity.ProviderOrigin{}, ErrInvalidAccount
	}
	account, err := manager.account(id)
	if err != nil {
		return nil, originidentity.ProviderOrigin{}, err
	}
	lease, err := manager.acquire(ctx, accountLeaseScope{id: id, accountRevision: account.Revision, realmID: account.RealmID,
		upstreamEndpointOrigin: account.Origin, ownerRead: true})
	return lease, account.Origin, err
}
