package pathcapability_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/pathcapability"
)

func TestM0PathCapabilityClassifiesSemanticAuxiliaryAndOpaque(t *testing.T) {
	t.Parallel()

	catalog := m0Catalog(t)
	semantic, err := catalog.Classify(
		access.DialectAnthropicMessages,
		http.MethodPost,
		"/v1/messages",
		"",
		"",
	)
	if err != nil ||
		semantic.Kind() != pathcapability.KindSemantic ||
		semantic.BodyKind() != pathcapability.BodyJSON ||
		semantic.ReplayClass() != exchange.ReplayGenerationCostOnly ||
		!semantic.EgressBearing() {
		t.Fatalf("semantic capability=%+v err=%v", semantic, err)
	}
	betaSemantic, err := catalog.Classify(
		access.DialectAnthropicMessages,
		http.MethodPost,
		"/v1/messages",
		"",
		"beta=true",
	)
	if err != nil || betaSemantic.Kind() != pathcapability.KindSemantic {
		t.Fatalf("beta semantic capability=%+v err=%v", betaSemantic, err)
	}
	flags := semantic.FeatureFlags()
	flags[0] = "mutated"
	if semantic.FeatureFlags()[0] != "messages" {
		t.Fatal("feature flag getter aliases the compiled catalog")
	}

	auxiliary, err := catalog.Classify(
		access.DialectAnthropicMessages,
		http.MethodPost,
		"/v1/messages/count_tokens",
		"",
		"",
	)
	if err != nil ||
		auxiliary.Kind() != pathcapability.KindAuxiliary ||
		auxiliary.ReplayClass() != exchange.ReplaySafe {
		t.Fatalf("auxiliary capability=%+v err=%v", auxiliary, err)
	}
	betaAuxiliary, err := catalog.Classify(
		access.DialectAnthropicMessages,
		http.MethodPost,
		"/v1/messages/count_tokens",
		"",
		"beta=true",
	)
	if err != nil || betaAuxiliary.Kind() != pathcapability.KindAuxiliary {
		t.Fatalf("beta auxiliary capability=%+v err=%v", betaAuxiliary, err)
	}
	opaque, err := catalog.Classify(
		access.DialectAnthropicMessages,
		http.MethodGet,
		"/v1/unknown",
		"",
		"page=1",
	)
	if err != nil ||
		opaque.Kind() != pathcapability.KindOpaque ||
		opaque.ReplayClass() != exchange.ReplayUnknown {
		t.Fatalf("opaque capability=%+v err=%v", opaque, err)
	}
}

func TestM0PathCapabilityFailsClosedForKnownOrNonCanonicalTargets(
	t *testing.T,
) {
	t.Parallel()

	catalog := m0Catalog(t)
	tests := []struct {
		name     string
		method   string
		path     string
		rawPath  string
		rawQuery string
		reason   pathcapability.ReasonCode
	}{
		{
			name:   "wrong method",
			method: http.MethodGet,
			path:   "/v1/messages",
			reason: pathcapability.ReasonUnsupportedMethod,
		},
		{
			name:     "known query",
			method:   http.MethodPost,
			path:     "/v1/messages",
			rawQuery: "debug=true",
			reason:   pathcapability.ReasonUnsupportedQuery,
		},
		{
			name:     "near-match beta query",
			method:   http.MethodPost,
			path:     "/v1/messages",
			rawQuery: "beta=false",
			reason:   pathcapability.ReasonUnsupportedQuery,
		},
		{
			name:     "extended beta query",
			method:   http.MethodPost,
			path:     "/v1/messages",
			rawQuery: "beta=true&debug=true",
			reason:   pathcapability.ReasonUnsupportedQuery,
		},
		{
			name:    "alternate escape",
			method:  http.MethodPost,
			path:    "/v1/messages",
			rawPath: "/v1/%6dessages",
			reason:  pathcapability.ReasonInvalidRequestTarget,
		},
		{
			name:   "dot segment",
			method: http.MethodPost,
			path:   "/v1/../v1/messages",
			reason: pathcapability.ReasonInvalidRequestTarget,
		},
		{
			name:   "lowercase method",
			method: "post",
			path:   "/v1/messages",
			reason: pathcapability.ReasonUnsupportedMethod,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := catalog.Classify(
				access.DialectAnthropicMessages,
				test.method,
				test.path,
				test.rawPath,
				test.rawQuery,
			)
			if !errors.Is(err, pathcapability.ErrUnsupported) ||
				pathcapability.ReasonOf(err) != test.reason {
				t.Fatalf("Classify() error=%v reason=%q", err, pathcapability.ReasonOf(err))
			}
		})
	}
}

func TestM0PathCapabilityIsolatesResponsesFromOtherOpenAIOperations(
	t *testing.T,
) {
	t.Parallel()

	catalog := m0Catalog(t)
	responses, err := catalog.Classify(
		access.DialectOpenAIResponses,
		http.MethodPost,
		"/v1/responses",
		"",
		"",
	)
	if err != nil ||
		responses.Kind() != pathcapability.KindSemantic ||
		responses.OperationID().String() !=
			operationcatalog.OpenAIResponsesCreateID ||
		responses.Revision() != 1 ||
		responses.BodyKind() != pathcapability.BodyJSON ||
		responses.Transport() != access.ClientOperationTransportHTTP ||
		responses.ReplayClass() != exchange.ReplayGenerationCostOnly {
		t.Fatalf("Responses capability=%+v err=%v", responses, err)
	}
	websocket, err := catalog.Classify(
		access.DialectOpenAIResponses,
		http.MethodGet,
		"/v1/responses",
		"",
		"",
	)
	if err != nil ||
		websocket.Kind() != pathcapability.KindUnsupported ||
		websocket.OperationID().String() !=
			operationcatalog.OpenAIResponsesWebSocketUnsupportedID ||
		websocket.Transport() != access.ClientOperationTransportWebSocket ||
		websocket.EgressBearing() {
		t.Fatalf("Responses WebSocket capability=%+v err=%v", websocket, err)
	}

	models, err := catalog.Classify(
		access.DialectOpenAIResponses,
		http.MethodGet,
		"/v1/models",
		"",
		"client=codex",
	)
	if err != nil || models.Kind() != pathcapability.KindOpaque {
		t.Fatalf("models capability=%+v err=%v", models, err)
	}

	for _, path := range []string{
		"/v1/responses/response-1",
		"/v1/responses/response-1/input_items",
		"/v1/uploads",
		"/v1/uploads/upload-1/parts",
		"/v1/files",
		"/v1/batches",
		"/v1/audio/transcriptions",
		"/v1/images/generations",
		"/v1/videos",
		"/v1/realtime",
		"/v1/realtime/calls",
		"/v1/chat/completions",
		"/v1/completions",
		"/v1/embeddings",
	} {
		capability, classifyErr := catalog.Classify(
			access.DialectOpenAIResponses,
			http.MethodPost,
			path,
			"",
			"",
		)
		if classifyErr != nil ||
			capability.Kind() != pathcapability.KindUnsupported {
			t.Fatalf(
				"path %q capability=%+v err=%v",
				path,
				capability,
				classifyErr,
			)
		}
	}

	unknown, err := catalog.Classify(
		access.DialectOpenAIResponses,
		http.MethodGet,
		"/codex/client/settings",
		"",
		"",
	)
	if err != nil || unknown.Kind() != pathcapability.KindOpaque {
		t.Fatalf("unknown control capability=%+v err=%v", unknown, err)
	}
}

func TestM0PathCapabilityRejectsWrongResponsesMethodAndQuery(
	t *testing.T,
) {
	t.Parallel()

	catalog := m0Catalog(t)
	for _, test := range []struct {
		name     string
		method   string
		rawQuery string
		reason   pathcapability.ReasonCode
	}{
		{
			name:   "wrong method",
			method: http.MethodPatch,
			reason: pathcapability.ReasonUnsupportedMethod,
		},
		{
			name:     "query",
			method:   http.MethodPost,
			rawQuery: "background=true",
			reason:   pathcapability.ReasonUnsupportedQuery,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := catalog.Classify(
				access.DialectOpenAIResponses,
				test.method,
				"/v1/responses",
				"",
				test.rawQuery,
			)
			if !errors.Is(err, pathcapability.ErrUnsupported) ||
				pathcapability.ReasonOf(err) != test.reason {
				t.Fatalf(
					"Classify() error=%v reason=%q",
					err,
					pathcapability.ReasonOf(err),
				)
			}
		})
	}
}

func m0Catalog(t *testing.T) *pathcapability.Catalog {
	t.Helper()
	operations, err := operationcatalog.M0()
	if err != nil {
		t.Fatalf("construct operation catalog: %v", err)
	}
	catalog, err := pathcapability.NewCatalog(operations.Definitions())
	if err != nil {
		t.Fatalf("construct PathCapability catalog: %v", err)
	}
	return catalog
}
