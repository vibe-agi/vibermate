package operationcatalog_test

import (
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/pathcapability"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

// Real Claude Code traffic reaches non-model paths on the model origin. While
// they are uncatalogued they classify as unknown, which is the class the
// ingress gate is least able to reason about. Cataloguing the observed
// bodyless control probes turns them into proven no-payload operations.
func TestObservedClaudeControlPlaneOperationsAreCatalogued(t *testing.T) {
	t.Parallel()

	builtIn, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := pathcapability.NewCatalog(builtIn.Definitions())
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/claude_code/settings"},
		{http.MethodGet, "/api/claude_code/policy_limits"},
		{http.MethodHead, "/api/hello"},
		{http.MethodGet, "/api/hello"},
	} {
		t.Run(testCase.method+" "+testCase.path, func(t *testing.T) {
			t.Parallel()

			capability, err := catalog.Classify(
				protocolspec.DialectAnthropicMessages,
				testCase.method,
				testCase.path,
				"",
				"",
			)
			if err != nil {
				t.Fatal(err)
			}
			if capability.Kind() != pathcapability.KindOpaque {
				t.Fatalf("kind = %q, want opaque", capability.Kind())
			}
			if got := capability.PayloadClass(); got !=
				protocolspec.OperationPayloadNone {
				t.Fatalf("payload class = %q, want none", got)
			}
			if !capability.PayloadClass().AllowsOriginalOrigin() {
				t.Fatal("a proven control probe was refused the original origin")
			}
			if capability.OperationID().String() == "" {
				t.Fatal("a catalogued operation has no identity")
			}
		})
	}
}

// Cataloguing a path does not open it to other methods; a body-bearing method
// on a control path stays outside the proven set.
func TestControlPlanePathsDoNotAcceptBodyBearingMethods(t *testing.T) {
	t.Parallel()

	builtIn, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := pathcapability.NewCatalog(builtIn.Definitions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Classify(
		protocolspec.DialectAnthropicMessages,
		http.MethodPost,
		"/api/claude_code/settings",
		"",
		"",
	); err == nil {
		t.Fatal("a POST to a control path was classified as known")
	}
}

// Real Codex 0.145.0 in its ChatGPT-login shape reaches a model path and
// several control-plane paths on the same origin. Design 10 section 5.1
// records them. Classifying by host alone would send the control plane into
// the model pipeline.
func TestObservedCodexOperationsAreCatalogued(t *testing.T) {
	t.Parallel()

	builtIn, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := pathcapability.NewCatalog(builtIn.Definitions())
	if err != nil {
		t.Fatal(err)
	}

	model, err := catalog.Classify(
		protocolspec.DialectOpenAIResponses,
		http.MethodPost,
		"/backend-api/codex/responses",
		"",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if model.Kind() != pathcapability.KindSemantic {
		t.Fatalf("ChatGPT-login model path kind = %q", model.Kind())
	}
	if model.PayloadClass() != protocolspec.OperationPayloadClientSemantic {
		t.Fatalf("ChatGPT-login model payload class = %q", model.PayloadClass())
	}

	for _, path := range []string{
		"/backend-api/codex/models",
		"/backend-api/plugins/featured",
		"/backend-api/ps/plugins/installed",
		"/backend-api/ps/plugins/list",
		"/backend-api/ps/plugins/suggested",
	} {
		probe, probeErr := catalog.Classify(
			protocolspec.DialectOpenAIResponses,
			http.MethodGet,
			path,
			"",
			"",
		)
		if probeErr != nil {
			t.Fatalf("%s: %v", path, probeErr)
		}
		if probe.Kind() != pathcapability.KindOpaque ||
			probe.PayloadClass() != protocolspec.OperationPayloadNone {
			t.Fatalf(
				"%s kind=%q payload=%q",
				path,
				probe.Kind(),
				probe.PayloadClass(),
			)
		}
	}
}

// The observed MCP and analytics operations carry a request body, and nothing
// verifies it holds no prompt or tool data. Declaring them control would
// assert exactly that, so they stay unclassified and fail closed.
func TestObservedCodexBodyBearingControlPathsStayUnclassified(t *testing.T) {
	t.Parallel()

	builtIn, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := pathcapability.NewCatalog(builtIn.Definitions())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/backend-api/ps/mcp",
		"/backend-api/codex/analytics-events/events",
	} {
		capability, classifyErr := catalog.Classify(
			protocolspec.DialectOpenAIResponses,
			http.MethodPost,
			path,
			"",
			"",
		)
		if classifyErr != nil {
			continue
		}
		if capability.PayloadClass() != protocolspec.OperationPayloadUnknown {
			t.Fatalf(
				"%s was classified as %q without body verification",
				path,
				capability.PayloadClass(),
			)
		}
	}
}
