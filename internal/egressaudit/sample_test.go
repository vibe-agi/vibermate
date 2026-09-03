package egressaudit_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
)

// The window renders these shapes. A hand-typed fixture keeps passing after
// the runtime stops sending the field it describes, so the fixture is
// generated from the runtime instead.
func TestEgressSamplesDescribeWhatTheRuntimeSends(t *testing.T) {
	started := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	blind, err := egressaudit.New(egressaudit.NewInput{
		ID:           "egress-blind-sample",
		ConnectionID: "connection-blind-sample",
		Purpose:      egressaudit.PurposeBlindTunnel,
		PayloadClass: egressaudit.PayloadOpaqueTunnel,
		Parent: egressaudit.ParentRef{
			Kind: egressaudit.ParentBlindConnection,
			ID:   "connection-blind-sample",
		},
		Caller:       egressaudit.CallerCore,
		TargetOrigin: "https://files.example.com:443",
		Decision: egressaudit.BuiltInDirectDecision(
			egressaudit.AuthorityNetwork,
		),
		StartedAt: started,
	})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := blind.Finish(egressaudit.TerminalInput{
		Outcome:     egressaudit.OutcomeCompleted,
		BytesOut:    2048,
		BytesIn:     16384,
		CompletedAt: started.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := egressaudit.New(egressaudit.NewInput{
		ID:           "egress-provider-sample",
		ConnectionID: "connection-model-sample",
		Purpose:      egressaudit.PurposeProviderAttempt,
		PayloadClass: egressaudit.PayloadClientSemantic,
		Parent: egressaudit.ParentRef{
			Kind:       egressaudit.ParentUpstreamAttempt,
			ID:         "attempt-sample",
			ExchangeID: "exchange-sample",
		},
		Caller:       egressaudit.CallerCore,
		TargetOrigin: "https://api.anthropic.com:443",
		Decision: egressaudit.DecisionRef{
			PolicyID:       "environment-work",
			PolicyRevision: 4,
			Authority:      egressaudit.AuthorityEnvironment,
			RuleID:         "anthropic-direct",
			ProxyID:        "direct",
		},
		StartedAt: started.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	page := egressaudit.PageViewOf(egressaudit.Page{
		Items: []egressaudit.Record{
			{Sequence: 1, Attempt: finished},
			{Sequence: 2, Attempt: provider},
		},
	})
	encoded, err := json.MarshalIndent(page.Items, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join("..", "..", "api", "samples", "egress-attempts.json")
	if os.Getenv("VIBERMATE_UPDATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read egress samples: %v", err)
	}
	if string(current) != string(encoded) {
		t.Fatalf(
			"the outbound samples the window renders are stale; "+
				"rerun with VIBERMATE_UPDATE=1\n--- stored\n%s\n--- current\n%s",
			current,
			encoded,
		)
	}
}
