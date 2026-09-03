package originaltransport_test

import (
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func payloadClassRequestOptions(
	t *testing.T,
) originaltransport.RequestOptions {
	t.Helper()

	origin, err := originidentity.ParseClientOrigin("https://api.anthropic.com:443")
	if err != nil {
		t.Fatal(err)
	}
	return originaltransport.RequestOptions{
		RequestID:    "run-1/exchange-1",
		Kind:         offlinehold.EgressOpaque,
		Origin:       origin,
		Method:       http.MethodGet,
		Path:         "/api/claude_code/settings",
		Headers:      http.Header{"Authorization": []string{"Bearer client"}},
		PayloadClass: protocolspec.OperationPayloadControl,
		ConnectionID: "connection-test",
		ParentID:     "original-request-test",
	}
}

func TestOriginalRequestAcceptsProvenNoPayloadClasses(t *testing.T) {
	t.Parallel()

	for _, class := range []protocolspec.OperationPayloadClass{
		protocolspec.OperationPayloadNone,
		protocolspec.OperationPayloadControl,
	} {
		options := payloadClassRequestOptions(t)
		options.PayloadClass = class
		request, err := originaltransport.NewRequest(options)
		if err != nil {
			t.Fatalf("payload class %q was rejected: %v", class, err)
		}
		if got := request.PayloadClass(); got != class {
			t.Fatalf("frozen payload class = %q", got)
		}
	}
}

// The transport is the last boundary before the client's own credential
// leaves the process, so it re-proves admission instead of trusting its
// caller.
func TestOriginalRequestRejectsClientPayloadClasses(t *testing.T) {
	t.Parallel()

	for _, class := range []protocolspec.OperationPayloadClass{
		protocolspec.OperationPayloadClientSemantic,
		protocolspec.OperationPayloadClientData,
		protocolspec.OperationPayloadClass(""),
		protocolspec.OperationPayloadClass("prompt"),
	} {
		options := payloadClassRequestOptions(t)
		options.PayloadClass = class
		if _, err := originaltransport.NewRequest(options); err == nil {
			t.Fatalf("payload class %q reached original-origin transport", class)
		}
	}
}

// An unclassified bodyless request is admitted only because the empty body
// proves no client payload travels. The connection-policy Goal replaces this
// with an explicit allow/deny/ask decision.
func TestOriginalRequestAdmitsUnclassifiedOnlyWithoutABody(t *testing.T) {
	t.Parallel()

	options := payloadClassRequestOptions(t)
	options.PayloadClass = protocolspec.OperationPayloadUnknown
	if _, err := originaltransport.NewRequest(options); err != nil {
		t.Fatalf("bodyless unclassified request was rejected: %v", err)
	}

	options.Method = http.MethodPost
	options.Body = []byte(`{"messages":[]}`)
	if _, err := originaltransport.NewRequest(options); err == nil {
		t.Fatal("unclassified request carried a body to the original origin")
	}
}

// A catalogued control operation is verified to hold no client data, so its
// own small control body stays admissible.
func TestOriginalRequestAllowsAControlBody(t *testing.T) {
	t.Parallel()

	options := payloadClassRequestOptions(t)
	options.Method = http.MethodPost
	options.Body = []byte(`{"acknowledged":true}`)
	if _, err := originaltransport.NewRequest(options); err != nil {
		t.Fatalf("catalogued control body was rejected: %v", err)
	}
}
