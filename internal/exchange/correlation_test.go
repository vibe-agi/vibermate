package exchange

import (
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func mustCorrelationPlan(t *testing.T) environment.RequestPlan {
	t.Helper()
	return mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "gpt-provider",
		accounts: []testAccount{{
			id: "account.correlation", revision: 1, epoch: 1,
		}},
		preferred: "account.correlation",
	})
}

func correlatedRequest(
	t *testing.T,
	manualCaptureID string,
	connectionID string,
	options ...ClientRequestOption,
) (ClientRequest, error) {
	t.Helper()
	admission, err := captureadmission.NewManual(
		manualCaptureID,
		1,
		"Manual capture",
	)
	if err != nil {
		return ClientRequest{}, err
	}

	all := append(
		[]ClientRequestOption{WithIngressCorrelation(admission, connectionID)},
		options...,
	)
	plan := mustCorrelationPlan(t)
	operation := plan.Operation()
	evidence, err := NewClientOperationEvidence(
		operation.ID(),
		operation.Revision(),
		"POST",
		"/v1/messages",
		"",
	)
	if err != nil {
		return ClientRequest{}, err
	}
	return NewClientRequest(
		"exchange-correlation",
		plan,
		evidence,
		[]byte(`{"model":"m","max_tokens":1,"messages":[]}`),
		ReplayGenerationCostOnly,
		wireprofile.ApplicationProtocolHTTP1,
		all...,
	)
}

// ADR-0015 section 10 forbids encoding containment in an identity string.
// Association is expressed only by typed references.
func TestClientRequestCarriesTypedCorrelationRefs(t *testing.T) {
	t.Parallel()

	request, err := correlatedRequest(t, "capture-run-1", "connection-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := request.ManualCaptureRef(); got != "capture-run-1" {
		t.Fatalf("ManualCapture reference = %q", got)
	}
	if request.CaptureRunRef() != "" ||
		request.CaptureAdmissionRef() != "manual-capture/capture-run-1" {
		t.Fatalf("route-neutral references are inconsistent")
	}
	if got := request.ConnectionRef(); got != "connection-1" {
		t.Fatalf("connection reference = %q", got)
	}
}

func TestExchangeIDDoesNotEncodeAnotherIdentity(t *testing.T) {
	t.Parallel()

	const (
		captureID    = "capture-run-1"
		connectionID = "connection-1"
	)
	request, err := correlatedRequest(t, captureID, connectionID)
	if err != nil {
		t.Fatal(err)
	}
	exchangeID := request.ExchangeID()
	for _, other := range []string{captureID, connectionID} {
		if strings.Contains(exchangeID, other) {
			t.Fatalf(
				"Exchange ID %q encodes identity %q as a substring",
				exchangeID,
				other,
			)
		}
	}
}

func TestCorrelationIsOptionalButValidatedWhenPresent(t *testing.T) {
	t.Parallel()

	plan := mustCorrelationPlan(t)
	operation := plan.Operation()
	evidence, err := NewClientOperationEvidence(
		operation.ID(), operation.Revision(), "POST", "/v1/messages", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := NewClientRequest(
		"exchange-uncorrelated",
		plan,
		evidence,
		[]byte(`{"model":"m","max_tokens":1,"messages":[]}`),
		ReplayGenerationCostOnly,
		wireprofile.ApplicationProtocolHTTP1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if plain.CaptureRunRef() != "" || plain.ManualCaptureRef() != "" ||
		plain.CaptureAdmissionRef() != "" || plain.ConnectionRef() != "" {
		t.Fatal("an uncorrelated request reported a reference")
	}

	for _, invalid := range []struct {
		name         string
		captureID    string
		connectionID string
	}{
		{name: "blank capture", captureID: " ", connectionID: "connection-1"},
		{name: "blank connection", captureID: "capture-one", connectionID: " "},
		{name: "empty capture", captureID: "", connectionID: "connection-1"},
		{name: "empty connection", captureID: "capture-one", connectionID: ""},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			t.Parallel()

			if _, err := correlatedRequest(
				t,
				invalid.captureID,
				invalid.connectionID,
			); err == nil {
				t.Fatal("invalid correlation reference was accepted")
			}
		})
	}
}

func TestCorrelationOptionCannotBeDuplicated(t *testing.T) {
	t.Parallel()

	if _, err := correlatedRequest(
		t,
		"capture-run-1",
		"connection-1",
		WithIngressCorrelation(
			mustManualAdmission(t, "capture-run-2"),
			"connection-2",
		),
	); err == nil {
		t.Fatal("duplicate ingress correlation was accepted")
	}
}

func TestClientProtocolEvidenceIsValidatedAndCloned(t *testing.T) {
	t.Parallel()

	evidence := []protocolcore.ProtocolEvidenceValue{
		{Name: "claude.agent_id", Value: "agent-1"},
		{Name: "claude.session_id", Value: "session-1"},
	}
	request, err := correlatedRequest(
		t,
		"capture-run-1",
		"connection-1",
		WithClientProtocolEvidence(evidence),
	)
	if err != nil {
		t.Fatal(err)
	}
	evidence[0].Value = "mutated"
	got := request.ClientProtocolEvidence()
	got[1].Value = "also-mutated"
	if reread := request.ClientProtocolEvidence(); reread[0].Value != "agent-1" || reread[1].Value != "session-1" {
		t.Fatalf("client protocol evidence aliases caller state: %#v", reread)
	}
	if _, err := correlatedRequest(
		t,
		"capture-run-2",
		"connection-2",
		WithClientProtocolEvidence([]protocolcore.ProtocolEvidenceValue{
			{Name: "claude.session_id", Value: "session-1"},
			{Name: "claude.agent_id", Value: "agent-1"},
		}),
	); err == nil {
		t.Fatal("non-canonical client protocol evidence was accepted")
	}
}

func mustManualAdmission(
	t *testing.T,
	id string,
) captureadmission.Admission {
	t.Helper()
	admission, err := captureadmission.NewManual(id, 1, "Manual capture")
	if err != nil {
		t.Fatal(err)
	}
	return admission
}

func TestClientRequestCarriesOnlyValidatedAnthropicBetaHeader(t *testing.T) {
	t.Parallel()

	request, err := correlatedRequest(
		t,
		"capture-run-beta",
		"connection-beta",
		WithAnthropicBetaHeader(
			"claude-code-20250219,interleaved-thinking-2025-05-14",
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	headers := request.protocolHeaders()
	if headers.Get("Anthropic-Beta") !=
		"claude-code-20250219,interleaved-thinking-2025-05-14" ||
		len(headers) != 1 {
		t.Fatalf("protocol headers = %#v", headers)
	}

	for _, invalid := range []string{
		"",
		"beta\r\nAuthorization: secret",
		"beta,",
		"beta token with spaces",
	} {
		if _, err := correlatedRequest(
			t,
			"capture-run-invalid-beta",
			"connection-invalid-beta",
			WithAnthropicBetaHeader(invalid),
		); err == nil {
			t.Fatalf("invalid Anthropic beta header %q was accepted", invalid)
		}
	}

	if _, err := correlatedRequest(
		t,
		"capture-run-duplicate-beta",
		"connection-duplicate-beta",
		WithAnthropicBetaHeader("one-2025-01-01"),
		WithAnthropicBetaHeader("two-2025-01-02"),
	); err == nil {
		t.Fatal("duplicate Anthropic beta header option was accepted")
	}
}
