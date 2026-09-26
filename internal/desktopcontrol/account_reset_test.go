package desktopcontrol

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/accountoperation"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
)

type resetControlFixture struct {
	calls  int
	result accountoperation.Redemption
	err    error
}

func (fixture *resetControlFixture) ReadOwned(context.Context, provideraccount.ID, string) (accountoperation.Result, error) {
	return accountoperation.Result{}, nil
}

func (fixture *resetControlFixture) RedeemOwned(_ context.Context, id provideraccount.ID, revision uint64, creditID string) (accountoperation.Redemption, error) {
	fixture.calls++
	if id != "account-b" || revision != 1 || creditID != "credit-1" {
		return accountoperation.Redemption{}, provideraccount.ErrRevisionConflict
	}
	return fixture.result, fixture.err
}

func TestManualResetIsOwnerScopedAndDoesNotDuplicateOneConfirmedRequest(t *testing.T) {
	fixture := &resetControlFixture{result: accountoperation.Redemption{AccountID: "account-b", CredentialEpoch: 2, CreditID: "credit-1", Outcome: "reset", WindowsReset: 2}}
	handler := &Handler{accountReads: fixture, idempotent: newIdempotencyCache()}
	const path = "/api/v1/provider-accounts/account-b/actions/redeem-reset-credit"
	const id = "123e4567-e89b-42d3-a456-426614174000"
	call := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.SetPathValue("accountId", "account-b")
		request.Header.Set("If-Match", "1")
		request.Header.Set("Idempotency-Key", id)
		request.Header.Set("Content-Type", "application/json")
		if handler.RequiredScope(request) != ScopeWrite {
			t.Fatal("reset was admitted with a read-only management capability")
		}
		response := httptest.NewRecorder()
		handler.redeemResetCredit(response, request)
		return response
	}
	const input = `{"creditId":"credit-1"}`
	first, replay := call(input), call(input)
	if first.Code != http.StatusOK || replay.Body.String() != first.Body.String() || fixture.calls != 1 ||
		first.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("reset action did not deduplicate: %d, %d, calls=%d", first.Code, replay.Code, fixture.calls)
	}
	for _, forbidden := range []string{"access_token", "refresh_token", "redeemRequestId"} {
		if strings.Contains(first.Body.String(), forbidden) {
			t.Fatal("reset receipt included request secret or raw upstream data")
		}
	}
	if got := call(`{"creditId":"credit-2"}`); got.Code != http.StatusConflict || fixture.calls != 1 {
		t.Fatal("one idempotency key was reused for a different credit")
	}
	for _, invalid := range []string{
		`{"creditId":"bad\nidentity"}`,
		`{"creditId":"credit-1","redeemRequestId":"external-id"}`,
		`{"creditId":"credit-1","upstreamUrl":"https://example.test"}`,
	} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(invalid))
		request.SetPathValue("accountId", "account-b")
		request.Header.Set("If-Match", "1")
		request.Header.Set("Idempotency-Key", "other-control-key-123")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.redeemResetCredit(response, request)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid input status=%d", response.Code)
		}
	}
}
