package exchange

import (
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// A failure has to say where in the request's shape it happened. A path is
// field names and indices; it is what makes "my client cannot connect"
// answerable without rebuilding the runtime.
func TestAFailureCarriesTheStructuralPath(t *testing.T) {
	t.Parallel()

	cause := protocolcore.NewFailure(
		protocolcore.ReasonInvalidClientRequest,
		"$.messages[1].role",
		errors.New("message role is unsupported"),
	)
	failure := newFailure(ReasonInvalidExchangeRequest, "exchange-1", 0, cause)
	if failure.ClientPath != "$.messages[1].role" {
		t.Fatalf("path = %q", failure.ClientPath)
	}
	if ClientPathOf(failure) != "$.messages[1].role" {
		t.Fatalf("ClientPathOf = %q", ClientPathOf(failure))
	}
}

// A path is structure. Anything that could carry a value is refused, because
// a diagnostic that leaks content is worse than no diagnostic.
func TestAFailurePathRefusesAnythingThatCouldCarryContent(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		`$.messages[0].content = "my secret prompt"`,
		"$.model\nsk-live-key",
		"$." + string(make([]byte, 512)),
	} {
		cause := protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			path,
			errors.New("boom"),
		)
		failure := newFailure(ReasonInvalidExchangeRequest, "exchange-1", 0, cause)
		if failure.ClientPath != "" {
			t.Fatalf("a path that could carry content survived: %q", failure.ClientPath)
		}
	}
}

// A failure with no protocol cause has no path to report, and must not invent
// one.
func TestAFailureWithoutAProtocolCauseHasNoPath(t *testing.T) {
	t.Parallel()

	failure := newFailure(
		ReasonProviderTransportFailed,
		"exchange-1",
		0,
		errors.New("dial failed"),
	)
	if failure.ClientPath != "" {
		t.Fatalf("path = %q", failure.ClientPath)
	}
}
