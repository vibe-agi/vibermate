package pathcapability_test

import (
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/pathcapability"
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
		dialect  access.Dialect
		method   string
		path     string
		expected access.OperationPayloadClass
	}{
		{
			name:     "Anthropic messages create",
			dialect:  access.DialectAnthropicMessages,
			method:   http.MethodPost,
			path:     "/v1/messages",
			expected: access.OperationPayloadClientSemantic,
		},
		{
			name:     "Anthropic count tokens",
			dialect:  access.DialectAnthropicMessages,
			method:   http.MethodPost,
			path:     "/v1/messages/count_tokens",
			expected: access.OperationPayloadClientSemantic,
		},
		{
			name:     "OpenAI responses create",
			dialect:  access.DialectOpenAIResponses,
			method:   http.MethodPost,
			path:     "/v1/responses",
			expected: access.OperationPayloadClientSemantic,
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
			access.DialectAnthropicMessages,
			method,
			"/api/claude_code/settings",
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
			access.OperationPayloadUnknown {
			t.Fatalf("uncatalogued payload class = %q", got)
		}
		if capability.PayloadClass().AllowsOriginalOrigin() {
			t.Fatal("uncatalogued request was admitted to the original origin")
		}
	}
}
