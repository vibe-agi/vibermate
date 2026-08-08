package pathcapability_test

import (
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/pathcapability"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func payloadClassCatalog(t *testing.T) *pathcapability.Catalog {
	t.Helper()

	builtIn, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := pathcapability.NewCatalog(builtIn.Definitions())
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestClassifyCarriesTheCataloguedPayloadClass(t *testing.T) {
	t.Parallel()

	catalog := payloadClassCatalog(t)
	for _, testCase := range []struct {
		name     string
		dialect  protocolspec.Dialect
		method   string
		path     string
		expected protocolspec.OperationPayloadClass
	}{
		{
			name:     "Anthropic messages create",
			dialect:  protocolspec.DialectAnthropicMessages,
			method:   http.MethodPost,
			path:     "/v1/messages",
			expected: protocolspec.OperationPayloadClientSemantic,
		},
		{
			name:     "Anthropic count tokens",
			dialect:  protocolspec.DialectAnthropicMessages,
			method:   http.MethodPost,
			path:     "/v1/messages/count_tokens",
			expected: protocolspec.OperationPayloadClientSemantic,
		},
		{
			name:     "OpenAI responses create",
			dialect:  protocolspec.DialectOpenAIResponses,
			method:   http.MethodPost,
			path:     "/v1/responses",
			expected: protocolspec.OperationPayloadClientSemantic,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			capability, err := catalog.Classify(
				testCase.dialect,
				testCase.method,
				testCase.path,
				"",
				"",
			)
			if err != nil {
				t.Fatal(err)
			}
			if got := capability.PayloadClass(); got != testCase.expected {
				t.Fatalf("payload class = %q, want %q", got, testCase.expected)
			}
		})
	}
}

// An uncatalogued path has no proven payload class, so it must report unknown
// rather than inheriting a permissive default.
func TestClassifyReportsUnknownPayloadClassForUncataloguedPaths(t *testing.T) {
	t.Parallel()

	catalog := payloadClassCatalog(t)
	for _, method := range []string{
		http.MethodGet,
		http.MethodPost,
	} {
		capability, err := catalog.Classify(
			protocolspec.DialectAnthropicMessages,
			method,
			"/api/claude_code/not_catalogued",
			"",
			"",
		)
		if err != nil {
			t.Fatal(err)
		}
		if capability.Kind() != pathcapability.KindOpaque {
			t.Fatalf("uncatalogued kind = %q", capability.Kind())
		}
		if got := capability.PayloadClass(); got !=
			protocolspec.OperationPayloadUnknown {
			t.Fatalf("uncatalogued payload class = %q", got)
		}
		if capability.PayloadClass().AllowsOriginalOrigin() {
			t.Fatal("uncatalogued request was admitted to the original origin")
		}
	}
}
