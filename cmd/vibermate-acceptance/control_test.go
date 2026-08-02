package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/accessapply"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
)

func TestAssemblyAccessKeepsClientAndProviderIdentitySeparate(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		client  acceptanceClientID
		origin  string
		dialect access.Dialect
	}{
		{
			name:    "Claude",
			client:  acceptanceClientClaudeCode,
			origin:  "https://api.anthropic.com",
			dialect: access.DialectAnthropicMessages,
		},
		{
			name:    "Codex",
			client:  acceptanceClientCodexCLI,
			origin:  "https://api.openai.com",
			dialect: access.DialectOpenAIResponses,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			assertAssemblyAccessClientEdge(
				t,
				test.client,
				test.origin,
				test.dialect,
			)
		})
	}
}

func assertAssemblyAccessClientEdge(
	t *testing.T,
	client acceptanceClientID,
	clientOrigin string,
	clientDialect access.Dialect,
) {
	t.Helper()

	config := config{
		clientID:       client,
		accessID:       "Acc-001",
		providerOrigin: "https://api.openai.com/v1",
		providerModel:  "fixed-provider-model",
		secretRef:      "secret://provider/acceptance",
	}
	input, err := assemblyAccess(config, 0)
	if err != nil {
		t.Fatal(err)
	}
	if input.AgentEndpoint.ClientOrigin != clientOrigin ||
		input.AgentEndpoint.ClientDialect != string(clientDialect) {
		t.Fatalf("client origin = %q", input.AgentEndpoint.ClientOrigin)
	}
	if len(input.ProviderTargets) != 1 ||
		input.ProviderTargets[0].Origin != config.providerOrigin ||
		input.ProviderTargets[0].Origin == input.AgentEndpoint.ClientOrigin {
		t.Fatalf(
			"provider targets = %+v",
			input.ProviderTargets,
		)
	}
	if len(input.AccountBindings) != 1 ||
		input.AccountBindings[0].SecretRef != config.secretRef {
		t.Fatalf("account bindings = %+v", input.AccountBindings)
	}
	if input.Access.ID != config.accessID ||
		input.AgentEndpoint.ID != "Acc-001-agent" ||
		input.Profiles[0].ID != "Acc-001-openai" ||
		input.AccountBindings[0].ID != "Acc-001-account" {
		t.Fatalf("derived identifiers = %+v", input)
	}
	command, err := accessapply.BuildCommand(config.accessID, input)
	if err != nil {
		t.Fatal(err)
	}
	if command.ExpectedRevision != 0 ||
		command.Aggregate.Binding.Revision != 1 {
		t.Fatalf("command = %+v", command)
	}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), config.secretRef) ||
		strings.Contains(string(payload), `"secretValue"`) ||
		strings.Contains(string(payload), `"credential"`) {
		t.Fatal("Acceptance Access did not preserve the SecretRef-only boundary")
	}
}

func TestAcceptanceConnectionRuleSetAllowsOnlyTheFixedClientOrigin(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		id   acceptanceClientID
		host string
	}{
		{
			name: "Claude",
			id:   acceptanceClientClaudeCode,
			host: "api.anthropic.com",
		},
		{
			name: "Codex",
			id:   acceptanceClientCodexCLI,
			host: "api.openai.com",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fallback := desktopcontrol.ConnectionRuleInput{
				ID:       "default.ask",
				Decision: "ask",
				Match:    "any",
			}
			input, err := acceptanceConnectionRuleSet(
				config{clientID: test.id},
				desktopcontrol.ConnectionRuleSetResponse{
					Revision: 1,
					Rules:    []desktopcontrol.ConnectionRuleInput{},
					Default:  fallback,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(input.Rules) != 1 {
				t.Fatalf("rules = %+v", input.Rules)
			}
			rule := input.Rules[0]
			if rule.ID != acceptanceConnectionRuleID ||
				rule.Priority != 100 ||
				rule.Decision != "allow" ||
				rule.Match != "exact_host_port" ||
				rule.Host != test.host ||
				rule.Port != 443 {
				t.Fatalf("rule = %+v", rule)
			}
			if input.Default != fallback {
				t.Fatalf("default changed: %+v", input.Default)
			}
		})
	}
}

func TestAcceptanceConnectionRuleSetRefusesPreauthorizedInput(t *testing.T) {
	t.Parallel()

	_, err := acceptanceConnectionRuleSet(
		config{clientID: acceptanceClientClaudeCode},
		desktopcontrol.ConnectionRuleSetResponse{
			Revision: 2,
			Rules: []desktopcontrol.ConnectionRuleInput{{
				ID:       "unexpected.allow",
				Priority: 100,
				Decision: "allow",
				Match:    "exact_host_port",
				Host:     "api.anthropic.com",
				Port:     443,
			}},
			Default: desktopcontrol.ConnectionRuleInput{
				ID:       "default.ask",
				Decision: "ask",
				Match:    "any",
			},
		},
	)
	if err == nil {
		t.Fatal("preauthorized connection rules were accepted")
	}
}
