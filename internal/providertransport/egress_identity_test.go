package providertransport

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func validRequestOptions(t *testing.T) RequestOptions {
	t.Helper()

	secretRef, err := secretstore.ParseReference("secret://provider/account")
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
	plan := testRequestPlan(t)
	return RequestOptions{
		RequestID:       "request-egress-identity",
		ExchangeID:      "exchange-egress-identity",
		ParentAttemptID: "attempt-egress-identity",
		EgressAttemptID: "egress-egress-identity",
		TargetRef:       "target-egress-identity",
		Target:          testTarget("provider.example", 443),
		Provenance:      plan.provenance,
		Action:          action,
		Method:          http.MethodPost,
		RelativePath:    "chat/completions",
		Headers:         http.Header{},
		Body:            []byte(`{"model":"gpt-provider-model"}`),
		CredentialMode:  providerauth.CredentialManaged,
		AccountRef:      testAccountRef(),
		SecretRef:       secretRef,
		AuthDriverRef:   providerauth.StaticHeaderDriverRef(),
		WireProfile:     plan.wireProfile,
		ClientProtocol:  wireprofile.ApplicationProtocolHTTP1,
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
	options.WireProfile = testRequestPlanWithWireProfile(
		t,
		"https://provider.example:443/v1",
		wireprofile.ClaudeCodeUpstreamWireProfileRef(),
	).wireProfile
	options.ClientProtocol = wireprofile.ApplicationProtocolHTTP2
	if _, err := NewRequest(options); err == nil ||
		!strings.Contains(err.Error(), "does not support the client HTTP protocol") {
		t.Fatalf("missing HTTP/2 variant error = %v", err)
	}
}

func TestProviderProbeIdentitySeparatesFrozenRouteRevisions(t *testing.T) {
	t.Parallel()

	target := testTarget("provider.example", 443)
	first := testRequestProvenance(t)
	second, err := NewUpstreamRequestProvenance(
		first.EnvironmentID(),
		first.EnvironmentRevision(),
		first.EnvironmentDigest(),
		first.RouteID(),
		first.RouteRevision()+1,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstProbe, err := NewProbeTarget("provider-target", first, target)
	if err != nil {
		t.Fatal(err)
	}
	secondProbe, err := NewProbeTarget("provider-target", second, target)
	if err != nil {
		t.Fatal(err)
	}
	if firstProbe.TargetRef != "provider-target" ||
		secondProbe.TargetRef != "provider-target" {
		t.Fatal("provider target reference was overloaded with plan identity")
	}
	if firstProbe.PlanDigest == secondProbe.PlanDigest {
		t.Fatal("different frozen Route revisions coalesced into one plan digest")
	}
	for _, probe := range []offlinehold.ProbeTarget{firstProbe, secondProbe} {
		if probe.PlanRevision != uint64(first.EnvironmentRevision()) ||
			len(probe.PlanDigest) != 64 {
			t.Fatalf("provider probe omitted immutable plan identity: %+v", probe)
		}
	}
}

func TestProviderRequestExposesOnlyFrozenEnvironmentRouteAndAccountReferences(t *testing.T) {
	t.Parallel()

	options := validRequestOptions(t)
	frozen, err := NewRequest(options)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Provenance() != options.Provenance {
		t.Fatal("request provenance changed while freezing")
	}
	account, managed := frozen.AccountRef()
	if !managed || account != options.AccountRef {
		t.Fatalf("frozen account = %+v, managed=%v", account, managed)
	}
}
