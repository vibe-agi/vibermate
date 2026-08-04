package exchange

import (
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
)

func mustCorrelationAccessID(t *testing.T) access.AccessID {
	t.Helper()

	accessID, err := access.NewAccessID("access-correlation")
	if err != nil {
		t.Fatal(err)
	}
	return accessID
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
	return NewClientRequest(
		"exchange-correlation",
		testIngressBinding(t, mustCorrelationAccessID(t)),
		mustAnthropicOperationEvidence(t),
		[]byte(`{"model":"m","max_tokens":1,"messages":[]}`),
		ReplayGenerationCostOnly,
		access.ApplicationProtocolHTTP1,
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
		request.IngressProfileRef() != "manual-capture/capture-run-1" {
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

	plain, err := NewClientRequest(
		"exchange-uncorrelated",
		testIngressBinding(t, mustCorrelationAccessID(t)),
		mustAnthropicOperationEvidence(t),
		[]byte(`{"model":"m","max_tokens":1,"messages":[]}`),
		ReplayGenerationCostOnly,
		access.ApplicationProtocolHTTP1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if plain.CaptureRunRef() != "" || plain.ManualCaptureRef() != "" ||
		plain.IngressProfileRef() != "" || plain.ConnectionRef() != "" {
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
