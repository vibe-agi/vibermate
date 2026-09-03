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

	for _, test := range []struct {
		path    string
		payload protocolspec.OperationPayloadClass
	}{
		// All five admit query values, so all five are control. Recording the
		// plugins probes as none said their egress carried nothing from the
		// client, while each forwards client-chosen query values to the original
		// origin.
		{"/backend-api/codex/models", protocolspec.OperationPayloadControl},
		{"/backend-api/plugins/featured", protocolspec.OperationPayloadControl},
		{"/backend-api/ps/plugins/installed", protocolspec.OperationPayloadControl},
		{"/backend-api/ps/plugins/list", protocolspec.OperationPayloadControl},
		{"/backend-api/ps/plugins/suggested", protocolspec.OperationPayloadControl},
	} {
		probe, probeErr := catalog.Classify(
			protocolspec.DialectOpenAIResponses,
			http.MethodGet,
			test.path,
			"",
			"",
		)
		if probeErr != nil {
			t.Fatalf("%s: %v", test.path, probeErr)
		}
		if probe.Kind() != pathcapability.KindOpaque ||
			probe.PayloadClass() != test.payload {
			t.Fatalf(
				"%s kind=%q payload=%q",
				test.path,
				probe.Kind(),
				probe.PayloadClass(),
			)
		}
	}

	versionedModelsProbe, err := catalog.Classify(
		protocolspec.DialectOpenAIResponses,
		http.MethodGet,
		"/backend-api/codex/models",
		"",
		"client_version=0.147.0",
	)
	if err != nil {
		t.Fatalf("versioned models probe: %v", err)
	}
	if versionedModelsProbe.Kind() != pathcapability.KindOpaque ||
		versionedModelsProbe.PayloadClass() != protocolspec.OperationPayloadControl {
		t.Fatalf(
			"versioned models probe kind=%q payload=%q",
			versionedModelsProbe.Kind(),
			versionedModelsProbe.PayloadClass(),
		)
	}
	if _, err := catalog.Classify(
		protocolspec.DialectOpenAIResponses,
		http.MethodGet,
		"/backend-api/codex/models",
		"",
		"client_version=0.147.0&prompt=secret",
	); pathcapability.ReasonOf(err) != pathcapability.ReasonUnsupportedQuery {
		t.Fatalf("unexpected models query was not refused: %v", err)
	}

	for _, test := range []struct {
		path     string
		query    string
		badQuery string
	}{
		{
			path:     "/backend-api/plugins/featured",
			query:    "platform=codex",
			badQuery: "platform=codex&prompt=secret",
		},
		{
			path:  "/backend-api/ps/plugins/installed",
			query: "scope=USER&includeDownloadUrls=true",
			badQuery: "scope=USER&includeDownloadUrls=true&" +
				"prompt=secret",
		},
		{
			path:     "/backend-api/ps/plugins/list",
			query:    "scope=GLOBAL&limit=200&pageToken=next-page",
			badQuery: "scope=GLOBAL&limit=200&pageToken=next-page&prompt=secret",
		},
		{
			path:     "/backend-api/ps/plugins/suggested",
			query:    "scope=GLOBAL",
			badQuery: "scope=GLOBAL&prompt=secret",
		},
	} {
		probe, classifyErr := catalog.Classify(
			protocolspec.DialectOpenAIResponses,
			http.MethodGet,
			test.path,
			"",
			test.query,
		)
		if classifyErr != nil {
			t.Fatalf("%s?%s: %v", test.path, test.query, classifyErr)
		}
		// These are the probes that admit query values, which is what makes them
		// control: the admitted keys are client-chosen data on the wire, and the
		// class is recorded as egress evidence.
		if probe.Kind() != pathcapability.KindOpaque ||
			probe.PayloadClass() != protocolspec.OperationPayloadControl {
			t.Fatalf(
				"%s?%s kind=%q payload=%q",
				test.path,
				test.query,
				probe.Kind(),
				probe.PayloadClass(),
			)
		}
		if _, classifyErr = catalog.Classify(
			protocolspec.DialectOpenAIResponses,
			http.MethodGet,
			test.path,
			"",
			test.badQuery,
		); pathcapability.ReasonOf(classifyErr) !=
			pathcapability.ReasonUnsupportedQuery {
			t.Fatalf(
				"unexpected plugin query %s?%s was not refused: %v",
				test.path,
				test.badQuery,
				classifyErr,
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
