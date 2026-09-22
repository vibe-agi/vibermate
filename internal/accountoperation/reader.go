// Package accountoperation executes explicitly authorized service-account
// reads. Captured and owner-authorized requests share leases, transport and
// safe facts, but never share or substitute their authorization scopes.
package accountoperation

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/upstreamservice"
)

var ErrUnavailable = errors.New("upstream account read is unavailable")
var ErrInvalidResponse = errors.New("upstream account response is invalid")

type CredentialAuthority interface {
	Get(context.Context, provideraccount.ID) (provideraccount.View, error)
	AcquireOwnedReadCredential(context.Context, provideraccount.ID) (providerauth.Lease, originidentity.ProviderOrigin, error)
	AcquireReadCredential(context.Context, environment.RequestPlan) (providerauth.Lease, error)
}

// ReadOwned is only exposed by an owner-authorized management endpoint. Unlike
// a Capture query it needs no profile association and cannot grant one.
func (reader *Reader) ReadOwned(ctx context.Context, id provideraccount.ID, operation string) (Result, error) {
	view, err := reader.accounts.Get(ctx, id)
	if err != nil {
		return Result{}, err
	}
	read, err := upstreamservice.ResolveRead(view.Account.Origin, operation)
	if err != nil {
		return Result{}, err
	}
	lease, origin, err := reader.accounts.AcquireOwnedReadCredential(ctx, id)
	if err != nil {
		return Result{}, err
	}
	if lease == nil {
		return Result{}, ErrUnavailable
	}
	defer lease.Release()
	if origin != read.Origin() {
		return Result{}, ErrUnavailable
	}
	request, err := providertransport.NewOwnedAccountRead(origin, operation, lease)
	if err != nil {
		return Result{}, err
	}
	return reader.execute(ctx, request, read, lease)
}

type Transport interface {
	ReadAccount(context.Context, providertransport.AccountReadRequest) (*http.Response, error)
}
type Options struct {
	Accounts  CredentialAuthority
	Transport Transport
	Clock     func() time.Time
}
type Reader struct {
	accounts  CredentialAuthority
	transport Transport
	clock     func() time.Time
}

func New(options Options) (*Reader, error) {
	if options.Accounts == nil || options.Transport == nil {
		return nil, ErrUnavailable
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	return &Reader{accounts: options.Accounts, transport: options.Transport, clock: options.Clock}, nil
}

type Source struct {
	ConnectionID string
	OperationID  string
}
type Facts struct {
	upstreamservice.Facts
	AccountID       string    `json:"accountId"`
	CredentialEpoch uint64    `json:"credentialEpoch"`
	Origin          string    `json:"origin"`
	AdapterID       string    `json:"adapterId"`
	AdapterRevision uint64    `json:"adapterRevision"`
	ObservedAt      time.Time `json:"observedAt"`
	State           string    `json:"state"`
}
type Result struct {
	Body  []byte
	Facts Facts
}

func (reader *Reader) ReadCaptured(ctx context.Context, plan environment.RequestPlan, source Source) (Result, error) {
	route, _, err := plan.AccountReadTarget()
	if err != nil {
		return Result{}, err
	}
	read, err := upstreamservice.ResolveRead(route.ProviderTarget().Origin, plan.Operation().ID().String())
	if err != nil {
		return Result{}, err
	}
	if read.RequiresHistoryPermission() && !route.AllowsAccountHistory() {
		return Result{}, upstreamservice.ErrHistoryDenied
	}
	lease, err := reader.accounts.AcquireReadCredential(ctx, plan)
	if err != nil {
		return Result{}, err
	}
	if lease == nil {
		return Result{}, ErrUnavailable
	}
	defer lease.Release()
	request, err := providertransport.NewCapturedAccountRead(plan, lease, source.ConnectionID, source.OperationID)
	if err != nil {
		return Result{}, err
	}
	return reader.execute(ctx, request, read, lease)
}

func (reader *Reader) execute(ctx context.Context, request providertransport.AccountReadRequest, read upstreamservice.Read, lease providerauth.Lease) (Result, error) {
	response, err := reader.transport.ReadAccount(ctx, request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{}, ErrUnavailable
	}
	if response == nil || response.Body == nil {
		return Result{}, ErrInvalidResponse
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Result{}, ErrUnavailable
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return Result{}, ErrInvalidResponse
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20+1))
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if err != nil || len(body) > 2<<20 {
		return Result{}, ErrInvalidResponse
	}
	facts, err := read.Project(body)
	if err != nil {
		return Result{}, ErrInvalidResponse
	}
	account, ok := lease.Account()
	if !ok {
		return Result{}, ErrInvalidResponse
	}
	return Result{Body: body, Facts: Facts{AccountID: account.ID, CredentialEpoch: account.CredentialEpoch,
		Origin: read.Origin().String(), AdapterID: read.AdapterID(), AdapterRevision: read.AdapterRevision(), ObservedAt: reader.clock().UTC(),
		State: "known", Facts: facts}}, nil
}
