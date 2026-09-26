// Package accountoperation executes explicitly authorized service-account
// reads. Captured and owner-authorized requests share leases, transport and
// safe facts, but never share or substitute their authorization scopes.
package accountoperation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
var ErrResetUnconfirmed = errors.New("Codex reset outcome could not be confirmed")

type CredentialAuthority interface {
	Get(context.Context, provideraccount.ID) (provideraccount.View, error)
	AcquireOwnedCredential(context.Context, provideraccount.ID) (providerauth.Lease, originidentity.ProviderOrigin, error)
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
	lease, origin, err := reader.accounts.AcquireOwnedCredential(ctx, id)
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
	result, err := reader.execute(ctx, request, read, lease)
	if err != nil || operation != upstreamservice.CodexRateLimits ||
		view.Account.Driver != providerauth.CodexOAuthDriverRef() ||
		result.Facts.RateLimitResets == nil || result.Facts.RateLimitResets.AvailableCount == 0 {
		return result, err
	}
	// Credit details are enrichment for the owner, never part of a captured
	// client query. A failed secondary read leaves the known inline count intact.
	details, err := upstreamservice.ResolveRead(origin, upstreamservice.CodexResetCreditDetails)
	if err != nil {
		return result, nil
	}
	detailsRequest, err := providertransport.NewOwnedAccountRead(origin, details.ID(), lease)
	if err != nil {
		return result, nil
	}
	detailResult, err := reader.execute(ctx, detailsRequest, details, lease)
	if err == nil && detailResult.Facts.RateLimitResets != nil {
		result.Facts.RateLimitResets = detailResult.Facts.RateLimitResets
	}
	return result, nil
}

type Transport interface {
	ReadAccount(context.Context, providertransport.AccountReadRequest) (*http.Response, error)
	ConsumeResetCredit(context.Context, providertransport.ResetRedemption) (*http.Response, error)
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

type Redemption struct {
	AccountID       string `json:"accountId"`
	CredentialEpoch uint64 `json:"credentialEpoch"`
	CreditID        string `json:"creditId"`
	Outcome         string `json:"outcome"`
	WindowsReset    int    `json:"windowsReset"`
}

// RedeemOwned consumes one identified, available banked reset for an owner.
// It never accepts a captured-client operation or a caller-supplied URL.
func (reader *Reader) RedeemOwned(ctx context.Context, id provideraccount.ID, expectedRevision uint64, creditID string) (Redemption, error) {
	requestID, err := providertransport.ResetRequestID(id.String(), creditID)
	if err != nil {
		return Redemption{}, err
	}
	view, err := reader.accounts.Get(ctx, id)
	if err != nil {
		return Redemption{}, err
	}
	if expectedRevision == 0 || view.Account.Revision != expectedRevision {
		return Redemption{}, provideraccount.ErrRevisionConflict
	}
	if view.Account.Driver != providerauth.CodexOAuthDriverRef() {
		return Redemption{}, upstreamservice.ErrUnsupported
	}
	details, err := upstreamservice.ResolveRead(view.Account.Origin, upstreamservice.CodexResetCreditDetails)
	if err != nil {
		return Redemption{}, err
	}
	lease, origin, err := reader.accounts.AcquireOwnedCredential(ctx, id)
	if err != nil {
		return Redemption{}, err
	}
	if lease == nil {
		return Redemption{}, ErrUnavailable
	}
	defer lease.Release()
	if origin != details.Origin() {
		return Redemption{}, ErrUnavailable
	}
	readRequest, err := providertransport.NewOwnedAccountRead(origin, details.ID(), lease)
	if err != nil {
		return Redemption{}, err
	}
	facts, err := reader.execute(ctx, readRequest, details, lease)
	if err != nil {
		return Redemption{}, err
	}
	selected := false
	if resets := facts.Facts.RateLimitResets; resets != nil && resets.AvailableCount > 0 {
		for _, credit := range resets.Details {
			if credit.ID != creditID || credit.ResetType != "codex_rate_limits" || credit.Status != "available" {
				continue
			}
			if credit.ExpiresAt != "" {
				expiresAt, err := time.Parse(time.RFC3339, credit.ExpiresAt)
				if err != nil || !expiresAt.After(reader.clock().UTC()) {
					continue
				}
			}
			selected = true
			break
		}
	}
	if !selected {
		return Redemption{}, upstreamservice.ErrUnsupported
	}
	command, err := providertransport.NewOwnedResetRedemption(origin, creditID, requestID, lease)
	if err != nil {
		return Redemption{}, err
	}
	response, err := reader.transport.ConsumeResetCredit(ctx, command)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return Redemption{}, fmt.Errorf("%w: %w", ErrResetUnconfirmed, err)
	}
	if response == nil || response.Body == nil {
		return Redemption{}, ErrResetUnconfirmed
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Redemption{}, ErrResetUnconfirmed
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return Redemption{}, ErrResetUnconfirmed
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10+1))
	if err != nil || len(body) > 64<<10 {
		return Redemption{}, ErrResetUnconfirmed
	}
	var outcome struct {
		Code         string `json:"code"`
		WindowsReset int    `json:"windows_reset"`
	}
	if json.Unmarshal(body, &outcome) != nil || outcome.WindowsReset < 0 || outcome.WindowsReset > 64 {
		return Redemption{}, ErrResetUnconfirmed
	}
	switch outcome.Code {
	case "reset", "nothing_to_reset", "no_credit", "already_redeemed":
	default:
		return Redemption{}, ErrResetUnconfirmed
	}
	account, ok := lease.Account()
	if !ok || account.ID != id.String() {
		return Redemption{}, ErrInvalidResponse
	}
	return Redemption{AccountID: account.ID, CredentialEpoch: account.CredentialEpoch,
		CreditID: creditID, Outcome: outcome.Code, WindowsReset: outcome.WindowsReset}, nil
}
