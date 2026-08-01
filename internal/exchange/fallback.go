package exchange

import (
	"context"
	"errors"

	"github.com/vibe-agi/vibermate/internal/access"
)

// mayTryNextCandidate decides whether a failed attempt may be tried against
// the next candidate in the RouteSet.
//
// Design 02 §12 allows this only before a first byte has been committed
// downstream, with no unresolved tool call, and only when the policy
// explicitly permits the duplicate billing and possible upstream side effects
// a second attempt brings. A timeout, a 429, or a 5xx does not prove the
// upstream did not process the request, so every one of these has to hold at
// once — the transport's opinion is not enough on its own.
func mayTryNextCandidate(
	policy access.FallbackPolicy,
	candidates int,
	attempted int,
	ledger *CommitLedger,
	replay ReplayClass,
	cause error,
) bool {
	if !policy.Allows() || attempted+1 >= candidates {
		return false
	}
	if !replayableAcrossCandidates(replay) || !retryableFailure(cause) {
		return false
	}
	if ledger == nil {
		return false
	}
	snapshot := ledger.Snapshot()
	// Anything the client has already received makes this answer committed. A
	// second attempt would send a second beginning to somebody reading the
	// first.
	if snapshot.DownstreamSemanticBytes > 0 ||
		snapshot.DownstreamSemanticWrites > 0 ||
		snapshot.DownstreamHoldEnvelope ||
		snapshot.DownstreamOrdinaryHeaders ||
		snapshot.DownstreamTerminal {
		return false
	}
	// A tool call the client is still holding open belongs to the attempt that
	// produced it.
	return len(snapshot.DownstreamToolKeys()) == 0
}

// replayableAcrossCandidates reports whether sending this request again is
// something the request itself permits.
//
// `generation_cost_only` is replayable at a price, and the policy is where
// that price was accepted. The classes below it are not: a request that may
// have had an effect upstream cannot be made twice on a guess.
func replayableAcrossCandidates(replay ReplayClass) bool {
	switch replay {
	case ReplaySafe, ReplayIdempotencyKeyed, ReplayGenerationCostOnly:
		return true
	default:
		return false
	}
}

// retryableFailure reports whether a different candidate could plausibly
// answer where this one did not.
//
// A refusal of the request, a plan that cannot serve it, or a client that went
// away are all answers a second candidate would repeat.
func retryableFailure(cause error) bool {
	if cause == nil {
		return false
	}
	if errors.Is(cause, context.Canceled) ||
		errors.Is(cause, context.DeadlineExceeded) ||
		errors.Is(cause, ErrRuntimeStopping) {
		return false
	}
	switch ReasonOf(cause) {
	case ReasonProviderTransportFailed,
		ReasonProviderResponseIdle,
		ReasonProviderStatusRejected,
		ReasonProviderCredentialUnavailable,
		ReasonTransportRetryExhausted:
		return true
	default:
		return false
	}
}
