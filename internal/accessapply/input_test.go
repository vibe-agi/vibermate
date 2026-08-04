package accessapply_test

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/accessapply"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
)

func TestBuildCommandCreatesOneCompleteTypedAggregateWithoutSecretValue(t *testing.T) {
	t.Parallel()

	input := validInput()
	command, err := accessapply.BuildCommand("access-control", input)
	if err != nil {
		t.Fatal(err)
	}
	if command.ExpectedRevision != 0 ||
		command.Aggregate.Binding.Revision != 1 ||
		command.Aggregate.Binding.ID.String() != "access-control" ||
		command.Aggregate.AgentEndpoint.ClientOrigin.String() !=
			"https://agent.example.test:443" ||
		len(command.Aggregate.Profiles) != 1 ||
		len(command.Aggregate.ProviderTargets) != 1 ||
		len(command.Aggregate.AccountBindings) != 1 ||
		command.Aggregate.AccountBindings[0].SecretRef.String() !=
			"secret://provider/access-control" ||
		command.Aggregate.AccountBindings[0].AuthDriverRef !=
			access.StaticHeaderAuthDriverRef() ||
		command.Aggregate.PluginPlan.Mode !=
			access.PluginPlanModePassThrough {
		t.Fatalf("typed Access command = %+v", command)
	}
	input.Profiles[0].AccountBindingIDs[0] = "mutated"
	if command.Aggregate.Profiles[0].AccountBindingIDs[0].String() !=
		"access-control-account" {
		t.Fatal("caller mutation changed the typed Access command")
	}
}

func TestBuildCommandRejectsPathMismatchAndDanglingOwnership(t *testing.T) {
	t.Parallel()

	input := validInput()
	if _, err := accessapply.BuildCommand("different-access", input); err == nil {
		t.Fatal("BuildCommand() accepted a mismatched path Access ID")
	}
	input = validInput()
	input.Access.AgentEndpointID = "different-endpoint"
	command, err := accessapply.BuildCommand("access-control", input)
	if err != nil {
		t.Fatal(err)
	}
	compiler := testCompiler(t)
	if _, err := compiler.Compile(command.Aggregate); err == nil {
		t.Fatal("compiler accepted dangling AgentEndpoint ownership")
	}
}

func TestBuildCommandCompilesLiteralLoopbackProviderOrigin(t *testing.T) {
	t.Parallel()

	input := validInput()
	input.ProviderTargets[0].Origin = "http://127.0.0.1:23333/v1"
	command, err := accessapply.BuildCommand("access-control", input)
	if err != nil {
		t.Fatalf("BuildCommand() error = %v", err)
	}
	plan, err := testCompiler(t).Compile(command.Aggregate)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	targets := plan.ProviderTargets()
	if len(targets) != 1 ||
		targets[0].TransportKind() !=
			access.ProviderTransportLoopbackCleartext {
		t.Fatalf("compiled provider targets = %+v", targets)
	}
}

func validInput() accessapply.Input {
	return accessapply.Input{
		ExpectedRevision: 0,
		Access: accessapply.AccessInput{
			ID:                "access-control",
			Name:              "Control Access",
			Description:       "Executable control Access",
			Status:            "enabled",
			AgentEndpointID:   "access-control-endpoint",
			DefaultRouteSetID: "access-control-routes",
			ProfileIDs:        []string{"access-control-profile"},
			EgressPolicyID:    "access-control-egress",
		},
		AgentEndpoint: accessapply.AgentEndpointInput{
			ID:            "access-control-endpoint",
			ClientOrigin:  "https://agent.example.test:443",
			ClientDialect: "anthropic-messages",
		},
		Profiles: []accessapply.ProfileInput{{
			ID:                     "access-control-profile",
			Name:                   "OpenAI Chat",
			Description:            "Fixed profile",
			BackendDialect:         "openai-chat",
			TargetID:               "access-control-target",
			UpstreamWireProfileRef: access.UpstreamWireProfileFollowClientValue,
			DefaultModelPolicy: accessapply.ModelPolicyInput{
				Mode:       "fixed",
				FixedModel: "gpt-4.1-mini",
			},
			AccountBindingIDs: []string{
				"access-control-account",
			},
			DefaultAccountBindingID: "access-control-account",
		}},
		ProviderTargets: []accessapply.ProviderTargetInput{{
			ID:        "access-control-target",
			ProfileID: "access-control-profile",
			Origin:    "https://api.openai.com:443/v1",
			Protocol:  "openai-chat",
			Capabilities: []string{
				"messages",
				"streaming",
				"tool_calls",
			},
		}},
		AccountBindings: []accessapply.AccountBindingInput{{
			ID:            "access-control-account",
			ProfileID:     "access-control-profile",
			Label:         "Primary",
			SecretRef:     "secret://provider/access-control",
			AuthDriverRef: "static_header",
			Enabled:       true,
		}},
		RouteSets: []accessapply.RouteSetInput{{
			ID:                  "access-control-routes",
			CandidateProfileIDs: []string{"access-control-profile"},
		}},
		EgressPolicy: accessapply.EgressPolicyInput{
			ID:   "access-control-egress",
			Mode: "direct",
		},
		PluginPlan: accessapply.PluginPlanInput{
			Mode:       "pass_through",
			BindingIDs: []string{},
		},
	}
}

func testCompiler(t *testing.T) *access.Compiler {
	t.Helper()
	codecID, err := access.NewCodecPairID(
		"anthropic-messages-to-openai-chat",
	)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := access.NewCatalog(access.CatalogOptions{
		Capabilities: access.PlanCapabilities{
			MaxEndpointProfiles: 1,
			MaxAccountBindings:  1,
			MaxRouteSets:        1,
		},
		ClientOperations: operations.Definitions(),
		CodecPairs: []access.CodecPairDefinition{{
			ID:              codecID,
			Revision:        1,
			ClientDialect:   access.DialectAnthropicMessages,
			ProviderDialect: access.DialectOpenAIChat,
			ClientOperationIDs: operations.SemanticOperationIDs(
				access.DialectAnthropicMessages,
			),
			RequiredCapabilities: []access.ProviderCapability{
				access.ProviderCapabilityMessages,
				access.ProviderCapabilityStreaming,
				access.ProviderCapabilityToolCalls,
			},
		}},
		AuthDrivers: []access.AuthDriverDefinition{{
			Ref:      access.StaticHeaderAuthDriverRef(),
			Revision: 1,
		}},
		EgressModes: []access.EgressModeDefinition{{
			Mode:     access.EgressModeDirect,
			Revision: 1,
		}},
		PluginPlanModes: []access.PluginPlanModeDefinition{{
			Mode:     access.PluginPlanModePassThrough,
			Revision: 1,
		}},
		ModelPolicyModes: []access.ModelPolicyModeDefinition{{
			Mode:     access.ModelPolicyModeFixed,
			Revision: 1,
		}},
		TransportProfiles: []access.TransportFingerprintDefinition{
			access.ObservedClientH1TransportFingerprintDefinition(),
			access.StandardH1TransportFingerprintDefinition(),
			access.ClaudeCodeH1TransportFingerprintDefinition(),
		},
		UpstreamWireProfiles: access.BuiltInUpstreamWireProfileDefinitions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := access.NewCompiler(catalog)
	if err != nil {
		t.Fatal(err)
	}
	return compiler
}
