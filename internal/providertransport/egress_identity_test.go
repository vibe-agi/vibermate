package providertransport

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

func validRequestOptions(t *testing.T) RequestOptions {
	t.Helper()

	secretRef, err := access.NewSecretRef("secret://provider/account")
	if err != nil {
		t.Fatal(err)
	}
	gate := newStartedGate(t)
	action, err := gate.BeginAction(
		context.Background(),
		offlinehold.ActionRequest{ActionID: "action-egress-identity"},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(action.Release)
	plan := testRequestAccessPlan(t)
	return RequestOptions{
		RequestID:        "request-egress-identity",
		ExchangeID:       "exchange-egress-identity",
		ParentAttemptID:  "attempt-egress-identity",
		EgressAttemptID:  "egress-egress-identity",
		TargetRef:        "target-egress-identity",
		Target:           testTarget("provider.example", 443),
		AccessRevision:   plan.Revision(),
		PlanHash:         plan.PlanHash(),
		Action:           action,
		Method:           http.MethodPost,
		RelativePath:     "chat/completions",
		Headers:          http.Header{},
		Body:             []byte(`{"model":"gpt-provider-model"}`),
		CredentialSource: access.CredentialSourceManagedAccount,
		SecretRef:        secretRef,
		AuthDriverRef:    access.StaticHeaderAuthDriverRef(),
		WireProfile:      plan.UpstreamWireProfile(),
		ClientProtocol:   access.ApplicationProtocolHTTP1,
	}
}

// ADR-0015 §10 requires an outbound attempt's identity to be independent of
// the attempt it belongs to. The pipeline was passing one value for both, so
// every provider request failed to record its outbound and the whole Exchange
// failed with it. The fake provider used by every other test never reached
// this code, so nothing caught it until a real request went out.
func TestAProviderRequestCarriesItsOwnEgressIdentity(t *testing.T) {
	t.Parallel()

	options := validRequestOptions(t)
	options.EgressAttemptID = options.ParentAttemptID
	if _, err := NewRequest(options); err == nil {
		t.Fatal("an egress identity equal to its parent was accepted")
	}

	options = validRequestOptions(t)
	frozen, err := NewRequest(options)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.EgressAttemptID() == frozen.ParentAttemptID() {
		t.Fatal("the egress identity is its parent")
	}
	if frozen.EgressAttemptID() != options.EgressAttemptID {
		t.Fatalf("egress identity = %q", frozen.EgressAttemptID())
	}
}

// An outbound that cannot be identified cannot be recorded, and an outbound
// that is not recorded must not go out.
func TestAProviderRequestWithoutAnEgressIdentityIsRefused(t *testing.T) {
	t.Parallel()

	options := validRequestOptions(t)
	options.EgressAttemptID = ""
	if _, err := NewRequest(options); err == nil {
		t.Fatal("a request with no egress identity was accepted")
	}
}

func TestProviderRequestRejectsMissingProtocolVariantBeforeExecution(t *testing.T) {
	t.Parallel()

	options := validRequestOptions(t)
	options.WireProfile = testRequestAccessPlanWithWireProfile(
		t,
		"https://provider.example:443/v1",
		access.DialectOpenAIChat,
		access.ClaudeCodeUpstreamWireProfileRef(),
	).UpstreamWireProfile()
	options.ClientProtocol = access.ApplicationProtocolHTTP2
	if _, err := NewRequest(options); err == nil ||
		!strings.Contains(err.Error(), "does not support the client HTTP protocol") {
		t.Fatalf("missing HTTP/2 variant error = %v", err)
	}
}
