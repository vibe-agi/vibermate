package exchange

import (
	"context"
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
)

func transportFailure() error {
	return newFailure(
		ReasonProviderTransportFailed,
		"exchange-1",
		0,
		errors.New("upstream connection dropped"),
	)
}

// The whole point of the rule: once bytes have reached the client the answer
// is committed, and a second attempt would send a second beginning to somebody
// already reading the first.
func TestNothingIsRetriedAfterTheClientHasReceivedAnything(t *testing.T) {
	t.Parallel()

	ledger := &CommitLedger{}
	if err := ledger.RecordSemanticWrite(12); err != nil {
		t.Fatal(err)
	}
	if mayTryNextCandidate(
		access.FallbackPreFirstByteIdempotentOnly,
		2,
		0,
		ledger,
		ReplaySafe,
		transportFailure(),
	) {
		t.Fatal("a committed answer was retried")
	}
}

// The policy is where the duplicate billing and possible upstream side effects
// of a second attempt were accepted. Without it there is no second attempt,
// whatever the transport thinks.
func TestFallbackHappensOnlyWhenThePolicyAllowsIt(t *testing.T) {
	t.Parallel()

	if mayTryNextCandidate(
		access.FallbackDisabled,
		2,
		0,
		&CommitLedger{},
		ReplaySafe,
		transportFailure(),
	) {
		t.Fatal("a disabled policy fell back")
	}
	if !mayTryNextCandidate(
		access.FallbackPreFirstByteIdempotentOnly,
		2,
		0,
		&CommitLedger{},
		ReplaySafe,
		transportFailure(),
	) {
		t.Fatal("an allowed policy did not fall back")
	}
}

// A request that may have had an effect upstream cannot be made twice on a
// guess. A dropped connection does not prove the upstream ignored it.
func TestARequestThatMayHaveHadAnEffectIsNotRepeated(t *testing.T) {
	t.Parallel()

	for _, replay := range []ReplayClass{
		ReplaySideEffectPossible,
		ReplayNonReplayable,
		ReplayUnknown,
	} {
		if mayTryNextCandidate(
			access.FallbackPreFirstByteIdempotentOnly,
			2,
			0,
			&CommitLedger{},
			replay,
			transportFailure(),
		) {
			t.Fatalf("%q was repeated", replay)
		}
	}
	for _, replay := range []ReplayClass{
		ReplaySafe,
		ReplayIdempotencyKeyed,
		ReplayGenerationCostOnly,
	} {
		if !mayTryNextCandidate(
			access.FallbackPreFirstByteIdempotentOnly,
			2,
			0,
			&CommitLedger{},
			replay,
			transportFailure(),
		) {
			t.Fatalf("%q was not retried", replay)
		}
	}
}

// A refusal of the request is an answer the next candidate would repeat, and a
// client that went away is not waiting for one.
func TestOnlyAFailureAnotherCandidateCouldAnswerIsRetried(t *testing.T) {
	t.Parallel()

	for _, reason := range []ReasonCode{
		ReasonInvalidExchangeRequest,
		ReasonUnsupportedClientInput,
		ReasonUnsupportedAccessPlan,
		ReasonProviderRequestInvalid,
		ReasonDownstreamDisconnected,
		ReasonToolDecisionRejected,
	} {
		if mayTryNextCandidate(
			access.FallbackPreFirstByteIdempotentOnly,
			2,
			0,
			&CommitLedger{},
			ReplaySafe,
			newFailure(reason, "exchange-1", 0, errors.New("boom")),
		) {
			t.Fatalf("%q was retried", reason)
		}
	}
	if mayTryNextCandidate(
		access.FallbackPreFirstByteIdempotentOnly,
		2,
		0,
		&CommitLedger{},
		ReplaySafe,
		context.Canceled,
	) {
		t.Fatal("a cancelled Exchange was retried")
	}
}

// The last candidate is the last one. A policy that allowed another attempt
// with nothing left to try would loop.
func TestTheLastCandidateEndsTheAttempts(t *testing.T) {
	t.Parallel()

	if mayTryNextCandidate(
		access.FallbackPreFirstByteIdempotentOnly,
		2,
		1,
		&CommitLedger{},
		ReplaySafe,
		transportFailure(),
	) {
		t.Fatal("the attempts did not end with the candidates")
	}
}
