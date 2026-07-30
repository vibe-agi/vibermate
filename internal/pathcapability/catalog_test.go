package pathcapability_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/pathcapability"
)

func TestM0PathCapabilityClassifiesSemanticAuxiliaryAndOpaque(t *testing.T) {
	t.Parallel()

	catalog, err := pathcapability.NewM0Catalog()
	if err != nil {
		t.Fatal(err)
	}
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

	catalog, err := pathcapability.NewM0Catalog()
	if err != nil {
		t.Fatal(err)
	}
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
