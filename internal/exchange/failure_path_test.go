package exchange

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/providertransport"
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

func TestProviderFailureIsNotClassifiedAsLocalIdleTimeout(t *testing.T) {
	if budget := DefaultStreamBudgets(); budget.ProviderProgressTimeout != 5*time.Minute || budget.ProviderProgressTimeout != providertransport.DefaultTransportTimeouts().ResponseIdle || budget.KeepaliveInterval >= budget.ProviderProgressTimeout {
		t.Fatalf("provider progress budget preempts native reasoning: %+v", budget)
	}
	pipeline := &Pipeline{}
	failure := pipeline.classifyStreamError(context.Background(), "exchange", 200,
		protocolcore.NewProviderFailure("$.error", []byte(`{"code":"server_error","message":"private"}`)))
	if failure.Code != ReasonProviderResponseFailed || failure.ProviderErrorCode != "server_error" {
		t.Fatalf("provider failure classification: %+v", failure)
	}
	for _, err := range []error{ErrProviderSemanticIdle, providertransport.ErrProviderResponseIdle} {
		idle := pipeline.classifyStreamError(context.Background(), "exchange", 200, err)
		if idle.Code != ReasonProviderResponseIdle || idle.ProviderErrorCode != "" {
			t.Fatalf("local idle timeout classification: %+v", idle)
		}
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

func TestProviderFailureReasonsSurviveWithoutBodyEvidence(t *testing.T) {
	for reason, want := range map[protocolcore.Reason]ReasonCode{
		protocolcore.ReasonProviderResponseFailed:  ReasonProviderResponseFailed,
		protocolcore.ReasonTruncatedEventStream:    ReasonProviderStreamTruncated,
		protocolcore.ReasonMalformedEventStream:    ReasonProviderStreamMalformed,
		protocolcore.ReasonStreamStateViolation:    ReasonProviderStreamStateInvalid,
		protocolcore.ReasonStreamLimitExceeded:     ReasonProviderStreamLimitExceeded,
		protocolcore.ReasonUnsupportedProviderData: ReasonProviderOutputUnsupported,
		protocolcore.ReasonToolCallIncomplete:      ReasonProviderToolCallIncomplete,
	} {
		cause := protocolcore.NewFailure(reason, "$", errors.New("private upstream detail"))
		failure := newFailure(ReasonProviderResponseInvalid, "exchange-1", 200, cause)
		if ReasonOf(failure) != want || failure.ProtocolReason != reason || failure.ClientPath != "$" {
			t.Fatalf("%s lost its structural classification: %v", reason, failure)
		}
		if got := newFailure(ReasonMessageTransformFailed, "exchange-1", 200, cause); got.Code != ReasonMessageTransformFailed {
			t.Fatalf("a different failing boundary was relabeled: %v", got)
		}
	}
}
