package exchange

import (
	"slices"
	"testing"
)

func TestCommitLedgerSeparatesHoldEnvelopeFromSemanticCommit(t *testing.T) {
	t.Parallel()

	var ledger CommitLedger
	if err := ledger.RecordHoldEnvelope(); err != nil {
		t.Fatal(err)
	}
	if allowed, reason := ledger.CanTransportResend(
		ReplayGenerationCostOnly,
		true,
	); !allowed || reason != RetryAllowed {
		t.Fatalf("pre-semantic resend = %v, %q; want allowed", allowed, reason)
	}
	if err := ledger.RecordSemanticWrite(7); err != nil {
		t.Fatal(err)
	}
	if allowed, reason := ledger.CanTransportResend(
		ReplayGenerationCostOnly,
		true,
	); allowed || reason != RetryBlockedDownstreamSemantics {
		t.Fatalf(
			"post-semantic resend = %v, %q; want semantic block",
			allowed,
			reason,
		)
	}
}

func TestCommitLedgerBlocksUnsafeReplayAndDefendsToolKeyOutput(t *testing.T) {
	t.Parallel()

	var ledger CommitLedger
	if err := ledger.RecordHoldEnvelope(); err != nil {
		t.Fatal(err)
	}
	if allowed, reason := ledger.CanTransportResend(
		ReplaySideEffectPossible,
		true,
	); allowed || reason != RetryBlockedReplayClass {
		t.Fatalf("unsafe resend = %v, %q", allowed, reason)
	}
	if err := ledger.RecordToolExposure([]string{"source:call-1"}); err != nil {
		t.Fatal(err)
	}
	snapshot := ledger.Snapshot()
	keys := snapshot.DownstreamToolKeys()
	keys[0] = "mutated"
	if got := ledger.Snapshot().DownstreamToolKeys(); !slices.Equal(
		got,
		[]string{"source:call-1"},
	) {
		t.Fatalf("ledger tool keys were aliased: %v", got)
	}
}
