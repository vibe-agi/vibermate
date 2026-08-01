package exchange

import (
	"github.com/vibe-agi/vibermate/internal/access"
	"strings"
	"testing"
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
	runID string,
	connectionID string,
	options ...ClientRequestOption,
) (ClientRequest, error) {
	t.Helper()

	all := append(
		[]ClientRequestOption{WithIngressCorrelation(runID, connectionID)},
		options...,
	)
	return NewClientRequest(
		"exchange-correlation",
		testIngressBinding(t, mustCorrelationAccessID(t)),
		mustAnthropicOperationEvidence(t),
		[]byte(`{"model":"m","max_tokens":1,"messages":[]}`),
		ReplayGenerationCostOnly,
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
	if got := request.CaptureRunRef(); got != "capture-run-1" {
		t.Fatalf("CaptureRun reference = %q", got)
	}
	if got := request.ConnectionRef(); got != "connection-1" {
		t.Fatalf("connection reference = %q", got)
	}
}

func TestExchangeIDDoesNotEncodeAnotherIdentity(t *testing.T) {
	t.Parallel()

	const (
		runID        = "capture-run-1"
		connectionID = "connection-1"
	)
	request, err := correlatedRequest(t, runID, connectionID)
	if err != nil {
		t.Fatal(err)
	}
	exchangeID := request.ExchangeID()
	for _, other := range []string{runID, connectionID} {
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
	)
	if err != nil {
		t.Fatal(err)
	}
	if plain.CaptureRunRef() != "" || plain.ConnectionRef() != "" {
		t.Fatal("an uncorrelated request reported a reference")
	}

	for _, invalid := range []struct {
		name         string
		runID        string
		connectionID string
	}{
		{name: "blank run", runID: " ", connectionID: "connection-1"},
		{name: "blank connection", runID: "capture-run-1", connectionID: " "},
		{name: "empty run", runID: "", connectionID: "connection-1"},
		{name: "empty connection", runID: "capture-run-1", connectionID: ""},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			t.Parallel()

			if _, err := correlatedRequest(
				t,
				invalid.runID,
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
		WithIngressCorrelation("capture-run-2", "connection-2"),
	); err == nil {
		t.Fatal("duplicate ingress correlation was accepted")
	}
}
