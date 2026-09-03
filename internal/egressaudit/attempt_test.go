package egressaudit_test

import (
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func baseInput() egressaudit.NewInput {
	return egressaudit.NewInput{
		ID:           "egress-1",
		ConnectionID: "connection-1",
		Purpose:      egressaudit.PurposeProviderAttempt,
		PayloadClass: egressaudit.PayloadClientSemantic,
		Parent: egressaudit.ParentRef{
			Kind:       egressaudit.ParentUpstreamAttempt,
			ID:         "attempt-1",
			ExchangeID: "exchange-1",
		},
		Caller:       egressaudit.CallerCore,
		TargetOrigin: "https://provider.example:443",
		Decision: egressaudit.DecisionRef{
			PolicyID:       "policy-1",
			PolicyRevision: 1,
			Authority:      egressaudit.AuthorityEnvironment,
			RuleID:         "rule-1",
			ProxyID:        "direct",
		},
		StartedAt: time.Unix(1785600000, 0).UTC(),
	}
}

func TestNewFreezesACompleteAttempt(t *testing.T) {
	t.Parallel()

	attempt, err := egressaudit.New(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Purpose() != egressaudit.PurposeProviderAttempt ||
		attempt.PayloadClass() != egressaudit.PayloadClientSemantic ||
		attempt.Parent().ExchangeID != "exchange-1" ||
		attempt.Decision().Authority != egressaudit.AuthorityEnvironment ||
		attempt.TargetOrigin() != "https://provider.example:443" {
		t.Fatalf("frozen attempt = %+v", attempt)
	}
	if attempt.ReusedTransport() {
		t.Fatal("a fresh attempt reported a reused transport")
	}
}

// Each purpose maps to exactly one policy authority. A mismatch means the
// caller resolved the wrong configuration, so it is refused rather than
// recorded.
func TestPurposeAndAuthorityMustAgree(t *testing.T) {
	t.Parallel()

	expected := map[egressaudit.EgressPurpose]egressaudit.PolicyAuthorityKind{
		egressaudit.PurposeProviderAttempt:        egressaudit.AuthorityEnvironment,
		egressaudit.PurposeUpstreamModelDiscovery: egressaudit.AuthorityRuntime,
		egressaudit.PurposeModelMetadataDirectory: egressaudit.AuthorityRuntime,
		egressaudit.PurposeRouteOperation:         egressaudit.AuthorityEnvironment,
		egressaudit.PurposeOriginalOrigin:         egressaudit.AuthorityNetwork,
		egressaudit.PurposeAgentProbe:             egressaudit.AuthorityNetwork,
		egressaudit.PurposeBlindTunnel:            egressaudit.AuthorityNetwork,
		egressaudit.PurposeAuxiliaryLLM:           egressaudit.AuthorityRuntime,
		egressaudit.PurposeLanguageTransform:      egressaudit.AuthorityRuntime,
		egressaudit.PurposePluginCatalogSync:      egressaudit.AuthorityRuntime,
		egressaudit.PurposePluginArtifactFetch:    egressaudit.AuthorityRuntime,
		egressaudit.PurposeUpdate:                 egressaudit.AuthorityRuntime,
	}
	seen := make(map[egressaudit.EgressPurpose]struct{}, len(expected))
	for _, purpose := range egressaudit.Purposes() {
		if _, duplicate := seen[purpose]; duplicate {
			t.Fatalf("purpose %q appears more than once", purpose)
		}
		seen[purpose] = struct{}{}
		authority, exists := expected[purpose]
		if !exists {
			t.Fatalf("purpose %q is absent from the expected authority catalog", purpose)
		}
		got, err := egressaudit.AuthorityForPurpose(purpose)
		if err != nil {
			t.Fatalf("purpose %q has no authority: %v", purpose, err)
		}
		if got != authority {
			t.Fatalf("purpose %q authority = %q, want %q", purpose, got, authority)
		}
	}
	if len(seen) != len(expected) {
		t.Fatalf("authority catalog covers %d purposes, want %d", len(seen), len(expected))
	}
	if _, err := egressaudit.AuthorityForPurpose("invented"); err == nil {
		t.Fatal("an unknown purpose resolved an authority")
	}

	input := baseInput()
	input.Decision.Authority = egressaudit.AuthorityRuntime
	if _, err := egressaudit.New(input); err == nil {
		t.Fatal("a mismatched purpose and authority were accepted")
	}
}

func TestPluginDistributionPurposeIsCoreOwnedRuntime(t *testing.T) {
	t.Parallel()

	for _, purpose := range []egressaudit.EgressPurpose{
		egressaudit.PurposePluginCatalogSync,
		egressaudit.PurposePluginArtifactFetch,
	} {
		purpose := purpose
		t.Run(string(purpose), func(t *testing.T) {
			t.Parallel()

			input := baseInput()
			input.ConnectionID = ""
			input.Purpose = purpose
			input.PayloadClass = egressaudit.PayloadRuntime
			input.Parent = egressaudit.ParentRef{
				Kind: egressaudit.ParentRuntimeAction,
				ID:   "plugin-distribution-action-1",
			}
			input.Decision.Authority = egressaudit.AuthorityRuntime
			if _, err := egressaudit.New(input); err != nil {
				t.Fatalf("core-owned plugin distribution was refused: %v", err)
			}

			plugin := input
			plugin.Caller = egressaudit.CallerPlugin
			plugin.CallerID = "plugin-1"
			if _, err := egressaudit.New(plugin); err == nil {
				t.Fatal("a plugin forged a core-owned distribution purpose")
			}

			wrongPayload := input
			wrongPayload.PayloadClass = egressaudit.PayloadControl
			if _, err := egressaudit.New(wrongPayload); err == nil {
				t.Fatal("plugin distribution recorded a non-runtime payload class")
			}
		})
	}
}

// An original-origin or agent-probe record may never claim client payload.
func TestOriginalOriginRefusesClientPayload(t *testing.T) {
	t.Parallel()

	for _, purpose := range []egressaudit.EgressPurpose{
		egressaudit.PurposeOriginalOrigin,
		egressaudit.PurposeAgentProbe,
	} {
		for _, class := range []egressaudit.PayloadClass{
			egressaudit.PayloadClientSemantic,
			egressaudit.PayloadClientData,
		} {
			input := baseInput()
			input.Purpose = purpose
			input.PayloadClass = class
			input.Decision.Authority = egressaudit.AuthorityNetwork
			input.Parent = egressaudit.ParentRef{
				Kind: egressaudit.ParentOriginalRequest,
				ID:   "original-1",
			}
			if _, err := egressaudit.New(input); err == nil {
				t.Fatalf("%q recorded payload class %q", purpose, class)
			}
		}
	}
}

func TestParentCombinationsAreExhaustivelyValidated(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name         string
		purpose      egressaudit.EgressPurpose
		authority    egressaudit.PolicyAuthorityKind
		payloadClass egressaudit.PayloadClass
		connectionID string
		parent       egressaudit.ParentRef
		valid        bool
	}{
		{
			name:         "provider attempt without an Exchange",
			purpose:      egressaudit.PurposeProviderAttempt,
			authority:    egressaudit.AuthorityEnvironment,
			payloadClass: egressaudit.PayloadClientSemantic,
			connectionID: "connection-1",
			parent: egressaudit.ParentRef{
				Kind: egressaudit.ParentUpstreamAttempt,
				ID:   "attempt-1",
			},
		},
		{
			name:         "profile operation carrying an Exchange",
			purpose:      egressaudit.PurposeRouteOperation,
			authority:    egressaudit.AuthorityEnvironment,
			payloadClass: egressaudit.PayloadClientSemantic,
			connectionID: "connection-1",
			parent: egressaudit.ParentRef{
				Kind:       egressaudit.ParentClientOperation,
				ID:         "operation-1",
				ExchangeID: "exchange-1",
			},
		},
		{
			name:         "profile operation without an Exchange",
			purpose:      egressaudit.PurposeRouteOperation,
			authority:    egressaudit.AuthorityEnvironment,
			payloadClass: egressaudit.PayloadClientSemantic,
			connectionID: "connection-1",
			parent: egressaudit.ParentRef{
				Kind: egressaudit.ParentClientOperation,
				ID:   "operation-1",
			},
			valid: true,
		},
		{
			name:         "blind tunnel without a connection",
			purpose:      egressaudit.PurposeBlindTunnel,
			authority:    egressaudit.AuthorityNetwork,
			payloadClass: egressaudit.PayloadOpaqueTunnel,
			parent: egressaudit.ParentRef{
				Kind: egressaudit.ParentBlindConnection,
				ID:   "connection-1",
			},
		},
		{
			name:         "runtime action without a connection",
			purpose:      egressaudit.PurposeAuxiliaryLLM,
			authority:    egressaudit.AuthorityRuntime,
			payloadClass: egressaudit.PayloadRuntime,
			parent: egressaudit.ParentRef{
				Kind: egressaudit.ParentRuntimeAction,
				ID:   "runtime-action-1",
			},
			valid: true,
		},
		{
			name:         "runtime action with the wrong parent kind",
			purpose:      egressaudit.PurposeUpdate,
			authority:    egressaudit.AuthorityRuntime,
			payloadClass: egressaudit.PayloadRuntime,
			parent: egressaudit.ParentRef{
				Kind: egressaudit.ParentUpstreamAttempt,
				ID:   "attempt-1",
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			input := baseInput()
			input.Purpose = testCase.purpose
			input.Decision.Authority = testCase.authority
			input.PayloadClass = testCase.payloadClass
			input.ConnectionID = testCase.connectionID
			input.Parent = testCase.parent
			_, err := egressaudit.New(input)
			if testCase.valid && err != nil {
				t.Fatalf("valid combination was refused: %v", err)
			}
			if !testCase.valid && err == nil {
				t.Fatal("invalid parent combination was accepted")
			}
		})
	}
}

// ADR-0015 section 10 forbids encoding containment across identities.
func TestAttemptIdentityDoesNotEncodeItsParent(t *testing.T) {
	t.Parallel()

	input := baseInput()
	input.ID = "exchange-1/egress-1"
	if _, err := egressaudit.New(input); err == nil {
		t.Fatal("an attempt ID encoding its Exchange was accepted")
	}
}

func TestPluginCallerRequiresAnIdentityAndCoreRefusesOne(t *testing.T) {
	t.Parallel()

	plugin := baseInput()
	plugin.Caller = egressaudit.CallerPlugin
	if _, err := egressaudit.New(plugin); err == nil {
		t.Fatal("a plugin caller without an identity was accepted")
	}

	core := baseInput()
	core.CallerID = "some-plugin"
	if _, err := egressaudit.New(core); err == nil {
		t.Fatal("a core caller carrying a plugin identity was accepted")
	}
}

// The record is evidence about a transport attempt, never about its content.
func TestAttemptCarriesNoPayloadClassBearingContent(t *testing.T) {
	t.Parallel()

	attempt, err := egressaudit.New(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := attempt.Finish(egressaudit.TerminalInput{
		Outcome:     egressaudit.OutcomeCompleted,
		BytesOut:    128,
		BytesIn:     4096,
		CompletedAt: time.Unix(1785600002, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Outcome() != egressaudit.OutcomeCompleted ||
		terminal.BytesOut() != 128 ||
		terminal.BytesIn() != 4096 {
		t.Fatalf("terminal attempt = %+v", terminal)
	}
	if _, err := terminal.Finish(egressaudit.TerminalInput{
		Outcome:     egressaudit.OutcomeFailed,
		CompletedAt: time.Unix(1785600003, 0).UTC(),
	}); err == nil {
		t.Fatal("a terminal attempt was finished twice")
	}
}

func TestReusedTransportIsRecordedNotOverwritten(t *testing.T) {
	t.Parallel()

	input := baseInput()
	input.ReusedTransport = true
	attempt, err := egressaudit.New(input)
	if err != nil {
		t.Fatal(err)
	}
	if !attempt.ReusedTransport() {
		t.Fatal("pool reuse was not recorded")
	}
}

func TestUnknownPayloadClassCannotBeRecorded(t *testing.T) {
	t.Parallel()

	input := baseInput()
	input.PayloadClass = egressaudit.PayloadClass(
		string(protocolspec.OperationPayloadUnknown),
	)
	input.Purpose = egressaudit.PurposeOriginalOrigin
	input.Decision.Authority = egressaudit.AuthorityNetwork
	input.Parent = egressaudit.ParentRef{
		Kind: egressaudit.ParentOriginalRequest,
		ID:   "original-1",
	}
	if _, err := egressaudit.New(input); err == nil {
		t.Fatal("the unknown payload class reached an egress record")
	}
}
