package exchange

import "testing"

// Design 12 section 6.1 keeps two commit axes: the upstream axis decides
// whether a request may already have been billed or produced a side effect,
// and the downstream axis decides whether the client has formed an
// irrevocable understanding. A 502, 503, or 504 is a response, so something
// upstream handled the request; for a generation_cost_only operation a resend
// therefore bills the user a second time. Design 02 permits that only when the
// attempt policy explicitly allows repeat billing.
func TestResendAfterAProviderResponseRequiresExplicitAllowance(t *testing.T) {
	t.Parallel()

	ledger := &CommitLedger{}
	if err := ledger.RecordUpstreamSend(128); err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecordHoldEnvelope(); err != nil {
		t.Fatal(err)
	}
	ledger.RecordUpstreamResponse()

	allowed, reason := ledger.CanTransportResend(ReplayGenerationCostOnly, false)
	if allowed {
		t.Fatal("a billed request was resent without an explicit allowance")
	}
	if reason != RetryBlockedUpstreamProcessed {
		t.Fatalf("block reason = %q", reason)
	}

	allowed, reason = ledger.CanTransportResend(ReplayGenerationCostOnly, true)
	if !allowed || reason != RetryAllowed {
		t.Fatalf("explicit allowance was refused: allowed=%v reason=%q",
			allowed, reason)
	}
}

// A request the provider never answered has not been proved billed, so a
// bounded transport retry stays available without a repeat-billing decision.
func TestResendBeforeAnyProviderResponseStaysAvailable(t *testing.T) {
	t.Parallel()

	ledger := &CommitLedger{}
	if err := ledger.RecordUpstreamSend(128); err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecordHoldEnvelope(); err != nil {
		t.Fatal(err)
	}

	allowed, reason := ledger.CanTransportResend(ReplayGenerationCostOnly, false)
	if !allowed || reason != RetryAllowed {
		t.Fatalf("pre-response resend was blocked: allowed=%v reason=%q",
			allowed, reason)
	}
}

// The downstream guards keep priority: once the client can see semantics, no
// allowance re-enables a resend.
func TestDownstreamCommitmentOutranksTheAllowance(t *testing.T) {
	t.Parallel()

	ledger := &CommitLedger{}
	if err := ledger.RecordUpstreamSend(128); err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecordHoldEnvelope(); err != nil {
		t.Fatal(err)
	}
	ledger.RecordUpstreamResponse()
	if err := ledger.RecordSemanticWrite(16); err != nil {
		t.Fatal(err)
	}

	if allowed, _ := ledger.CanTransportResend(
		ReplayGenerationCostOnly,
		true,
	); allowed {
		t.Fatal("an explicit allowance overrode downstream commitment")
	}
}

// The repeat-billing decision belongs to policy and defaults to refusing.
func TestDefaultHoldPolicyRefusesRepeatBilling(t *testing.T) {
	t.Parallel()

	if DefaultHoldPolicy().AllowResendAfterProviderResponse {
		t.Fatal("the default policy allows repeat billing")
	}
}
