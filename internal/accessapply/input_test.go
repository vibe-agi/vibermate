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
		len(command.Aggregate.Profiles) != 2 ||
		len(command.Aggregate.ProviderTargets) != 2 ||
		len(command.Aggregate.AccountBindings) != 1 ||
		command.Aggregate.AccountBindings[0].SecretRef.String() !=
			"secret://provider/access-control" ||
		command.Aggregate.AccountBindings[0].AuthDriverRef !=
			access.StaticHeaderAuthDriverRef() ||
		command.Aggregate.PluginPlan.Mode !=
			access.PluginPlanModePassThrough {
		t.Fatalf("typed Access command = %+v", command)
	}
	if command.Aggregate.Profiles[1].ID != access.OriginalPassthroughProfileID() ||
		command.Aggregate.Profiles[1].CredentialSource !=
			access.CredentialSourceClientPassthrough ||
		command.Aggregate.ProviderTargets[1].ID !=
			access.OriginalPassthroughTargetID() {
		t.Fatalf("system original passthrough = %+v", command.Aggregate)
	}
	if len(command.Aggregate.RouteSets) != 1 ||
		len(command.Aggregate.RouteSets[0].CandidateProfileIDs) != 2 ||
		command.Aggregate.RouteSets[0].CandidateProfileIDs[0].String() !=
			"access-control-profile" ||
		command.Aggregate.RouteSets[0].CandidateProfileIDs[1] !=
			access.OriginalPassthroughProfileID() ||
		command.Aggregate.RouteSets[0].FallbackMode() != access.FallbackDisabled {
		t.Fatalf("approved route choices = %+v", command.Aggregate.RouteSets)
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
	if len(targets) != 2 ||
		targets[0].TransportKind() !=
			access.ProviderTransportLoopbackCleartext ||
		targets[1].TransportKind() != access.ProviderTransportStrictTLS {
		t.Fatalf("compiled provider targets = %+v", targets)
	}
}

func TestBuildCommandCreatesExecutableOriginalRouteWithoutProviderConfiguration(
	t *testing.T,
) {
	t.Parallel()

	input := validInput()
	input.Access.DefaultRouteSetID = ""
	input.Access.ProfileIDs = nil
	input.Profiles = nil
	input.ProviderTargets = nil
	input.AccountBindings = nil
	input.RouteSets = nil
	command, err := accessapply.BuildCommand("access-control", input)
	if err != nil {
		t.Fatalf("BuildCommand() error = %v", err)
	}
	plan, err := testCompiler(t).Compile(command.Aggregate)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	candidate, found := plan.Candidate(0)
	if !found ||
		plan.CandidateCount() != 1 ||
		candidate.Kind() != access.EndpointProfileOriginalPassthrough ||
		candidate.ProfileID() != access.OriginalPassthroughProfileID() ||
		candidate.Target().Target().Origin.String() !=
			input.AgentEndpoint.ClientOrigin ||
		len(plan.AccountBindings()) != 0 {
		t.Fatalf("original route plan = %+v", plan)
	}
}

func TestBuildCommandRejectsIgnoredSystemAndManagedRelationshipFields(
	t *testing.T,
) {
	t.Parallel()

	input := validInput()
	input.Access.ProfileIDs = append(
		input.Access.ProfileIDs,
		"unowned-profile",
	)
	if _, err := accessapply.BuildCommand("access-control", input); err == nil {
		t.Fatal("BuildCommand() ignored an unowned managed profile reference")
	}

	input = validInput()
	input.RouteSets[0].CandidateProfileIDs = append(
		input.RouteSets[0].CandidateProfileIDs,
		access.OriginalPassthroughProfileID().String(),
	)
	if _, err := accessapply.BuildCommand("access-control", input); err == nil {
		t.Fatal("BuildCommand() accepted a caller-submitted system profile")
	}

	input = validInput()
	input.Access.ProfileIDs = nil
	if _, err := accessapply.BuildCommand("access-control", input); err == nil {
		t.Fatal("BuildCommand() ignored a missing managed profile reference")
	}

	input = validInput()
	input.Access.ProfileIDs = nil
	input.Profiles = nil
	input.ProviderTargets = nil
	input.AccountBindings = nil
	input.RouteSets = nil
	input.Access.DefaultRouteSetID = "caller-selected-system-route"
	if _, err := accessapply.BuildCommand("access-control", input); err == nil {
		t.Fatal("BuildCommand() accepted a caller-selected system route ID")
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
	passthroughCodecID, err := access.NewCodecPairID(
		"anthropic-messages-original-passthrough",
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
			MaxEndpointProfiles:          2,
			MaxAccountBindings:           1,
			MaxRouteSets:                 1,
			AllowMultipleRouteCandidates: true,
		},
		ClientOperations: operations.Definitions(),
		CodecPairs: []access.CodecPairDefinition{
			{
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
			},
			{
				ID:              passthroughCodecID,
				Revision:        1,
				ClientDialect:   access.DialectAnthropicMessages,
				ProviderDialect: access.DialectAnthropicMessages,
				ClientOperationIDs: operations.SemanticOperationIDs(
					access.DialectAnthropicMessages,
				),
				RequiredCapabilities: []access.ProviderCapability{
					access.ProviderCapabilityMessages,
					access.ProviderCapabilityStreaming,
					access.ProviderCapabilityToolCalls,
				},
			},
		},
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
		ModelPolicyModes: []access.ModelPolicyModeDefinition{
			{Mode: access.ModelPolicyModePassthrough, Revision: 1},
			{Mode: access.ModelPolicyModeFixed, Revision: 1},
		},
		TransportProfiles:    access.BuiltInTransportFingerprintDefinitions(),
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
