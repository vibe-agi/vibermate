package operationcatalog_test

import (
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/pathcapability"
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
				access.DialectAnthropicMessages,
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
				access.OperationPayloadNone {
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
		access.DialectAnthropicMessages,
		http.MethodPost,
		"/api/claude_code/settings",
		"",
		"",
	); err == nil {
		t.Fatal("a POST to a control path was classified as known")
	}
}
