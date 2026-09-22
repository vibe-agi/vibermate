package providertransport

import (
	"context"
	"errors"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamservice"
)

// AccountReadRequest freezes one declared operation and an already authorized
// short-lived credential. It cannot carry client headers, bodies or URLs.
type AccountReadRequest struct {
	read       upstreamservice.Read
	credential providerauth.Lease
	targetRef  string
	captured   *capturedAccountRead
}

type capturedAccountRead struct {
	plan         environment.RequestPlan
	connectionID string
	operationID  string
}

func NewCapturedAccountRead(plan environment.RequestPlan, credential providerauth.Lease, connectionID, operationID string) (AccountReadRequest, error) {
	route, selected, err := plan.AccountReadTarget()
	if err != nil {
		return AccountReadRequest{}, err
	}
	read, err := upstreamservice.ResolveRead(route.ProviderTarget().Origin, plan.Operation().ID().String())
	if err != nil {
		return AccountReadRequest{}, err
	}
	if read.RequiresHistoryPermission() && !route.AllowsAccountHistory() {
		return AccountReadRequest{}, upstreamservice.ErrHistoryDenied
	}
	if credential == nil || credential.Mode() != providerauth.CredentialManaged {
		return AccountReadRequest{}, errors.New("managed account credential is required")
	}
	actual, ok := credential.Account()
	if !ok || actual.Validate() != nil || actual.ID != selected.ID || actual.Revision != uint64(selected.Revision) || actual.RealmID != selected.RealmID ||
		connectionID == "" || operationID == "" {
		return AccountReadRequest{}, errors.New("account read credential scope is invalid")
	}
	return AccountReadRequest{read: read, credential: credential, targetRef: route.ProviderTarget().ID,
		captured: &capturedAccountRead{plan: plan, connectionID: connectionID, operationID: operationID}}, nil
}

func NewOwnedAccountRead(origin originidentity.ProviderOrigin, operation string, credential providerauth.Lease) (AccountReadRequest, error) {
	read, err := upstreamservice.ResolveRead(origin, operation)
	if err != nil {
		return AccountReadRequest{}, err
	}
	if credential == nil || credential.Mode() != providerauth.CredentialManaged {
		return AccountReadRequest{}, errors.New("managed account credential is required")
	}
	account, ok := credential.Account()
	if !ok || account.Validate() != nil {
		return AccountReadRequest{}, errors.New("account read credential scope is invalid")
	}
	return AccountReadRequest{read: read, credential: credential, targetRef: account.ID}, nil
}

// ReadAccount uses exactly the same Hold, authenticated strict transport and
// terminal-audit lifecycle as the other runtime-owned service operations.
func (client *Client) ReadAccount(ctx context.Context, request AccountReadRequest) (*http.Response, error) {
	if request.read.Validate() != nil || request.credential == nil || request.targetRef == "" {
		return nil, upstreamservice.ErrUnsupported
	}
	target, err := NewTarget(request.read.Origin())
	if err != nil {
		return nil, err
	}
	spec := runtimeFetchSpec{
		purpose: egressaudit.PurposeUpstreamAccountRead, targetRef: request.targetRef,
		target: target, relativePath: request.read.Path(), credential: request.credential,
		captured: request.captured,
	}
	if request.captured != nil {
		spec.purpose = egressaudit.PurposeRouteOperation
	}
	return client.fetchRuntimeJSON(ctx, spec)
}
