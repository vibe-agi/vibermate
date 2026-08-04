package access_test

import (
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
)

func TestCompilerProducesDeterministicExecutablePlan(t *testing.T) {
	t.Parallel()

	compiler := testCompiler(t)
	accessID := newAccessID(t, "access-compiler")
	aggregate := testAggregate(t, accessID, 1, "Compiled")
	first, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatalf("compile first plan: %v", err)
	}

	// Capability order is not semantic and is canonicalized before hashing.
	reordered := aggregate.Clone()
	reordered.ProviderTargets[0].Capabilities[0],
		reordered.ProviderTargets[0].Capabilities[2] =
		reordered.ProviderTargets[0].Capabilities[2],
		reordered.ProviderTargets[0].Capabilities[0]
	second, err := compiler.Compile(reordered)
	if err != nil {
		t.Fatalf("compile reordered plan: %v", err)
	}
	if first.PlanHash() != second.PlanHash() {
		t.Fatalf(
			"deterministic hashes differ: %s and %s",
			first.PlanHash(),
			second.PlanHash(),
		)
	}
	assertCompletePlan(t, first, 1, "Compiled")

	aggregate.Binding.Name = "Mutated input"
	aggregate.ProviderTargets[0].Capabilities = nil
	if first.Binding().Name != "Compiled" ||
		len(first.ProviderTargets()[0].Target().Capabilities) != 3 {
		t.Fatal("compiler retained aliases to its input aggregate")
	}
	assertGetterIsolation(t, first)
}

func TestCompilerFreezesUpstreamWireProfileAsOnePlanAuthority(t *testing.T) {
	t.Parallel()

	compiler := testCompiler(t)
	aggregate := testAggregate(
		t,
		newAccessID(t, "access-wire-profile"),
		1,
		"Wire profile",
	)
	follow, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	followProfile := follow.UpstreamWireProfile()
	followVariant, ok := followProfile.Variant(access.ApplicationProtocolHTTP1)
	if !ok ||
		followProfile.Ref() != access.FollowClientUpstreamWireProfileRef() ||
		followProfile.Mode() != access.UpstreamWireModeFollowClient ||
		followProfile.Product() != "" ||
		followVariant.UserAgentPolicy() != access.UserAgentPolicyFollowClient ||
		followVariant.SemanticUserAgent() != "" ||
		followVariant.TransportFingerprintPlan().Requested().Ref() !=
			access.ObservedClientH1TransportProfileRef() ||
		len(followVariant.TransportFingerprintPlan().Fallbacks()) != 0 {
		t.Fatalf("compiled follow-client wire profile = %+v", followProfile)
	}
	followH2Variant, ok := followProfile.Variant(access.ApplicationProtocolHTTP2)
	if !ok ||
		followH2Variant.UserAgentPolicy() != access.UserAgentPolicyFollowClient ||
		followH2Variant.TransportFingerprintPlan().Requested().Ref() !=
			access.ObservedClientH2TransportProfileRef() ||
		followH2Variant.TransportFingerprintPlan().Requested().HTTPTransport() !=
			access.HTTPTransportHTTP2 {
		t.Fatalf("compiled follow-client HTTP/2 variant = %+v", followH2Variant)
	}

	aggregate.Profiles[0].UpstreamWireProfileRef =
		access.ClaudeCodeUpstreamWireProfileRef()
	emulated, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	if follow.PlanHash() == emulated.PlanHash() {
		t.Fatal("different upstream wire profiles produced the same plan hash")
	}
	emulatedProfile := emulated.UpstreamWireProfile()
	emulatedVariant, ok := emulatedProfile.Variant(access.ApplicationProtocolHTTP1)
	if !ok ||
		emulatedProfile.Ref() != access.ClaudeCodeUpstreamWireProfileRef() ||
		emulatedProfile.Mode() != access.UpstreamWireModeEmulateProduct ||
		emulatedProfile.Product() != access.UpstreamWireProductClaudeCode ||
		emulatedVariant.UserAgentPolicy() != access.UserAgentPolicyConstant ||
		emulatedVariant.SemanticUserAgent() == "" ||
		emulatedVariant.TransportFingerprintPlan().Requested().Ref() !=
			access.ClaudeCodeH1TransportProfileRef() {
		t.Fatalf("compiled emulated wire profile = %+v", emulatedProfile)
	}
}

func TestCompilerFreezesExactResponsesOperationInPlanHash(t *testing.T) {
	t.Parallel()

	options := testCatalogOptions(t)
	responsesCodecID, err := access.NewCodecPairID(
		"openai-responses-to-openai-chat",
	)
	if err != nil {
		t.Fatalf("construct Responses codec ID: %v", err)
	}
	responsesOperation := options.ClientOperations[1]
	catalog, err := access.NewCatalog(options)
	if err != nil {
		t.Fatalf("construct compiler catalog: %v", err)
	}
	compiler, err := access.NewCompiler(catalog)
	if err != nil {
		t.Fatalf("construct compiler: %v", err)
	}

	anthropicAggregate := testAggregate(
		t,
		newAccessID(t, "access-operation-anthropic"),
		1,
		"Anthropic",
	)
	anthropicPlan, err := compiler.Compile(anthropicAggregate)
	if err != nil {
		t.Fatalf("compile Anthropic plan: %v", err)
	}
	responsesAggregate := testAggregate(
		t,
		newAccessID(t, "access-operation-responses"),
		1,
		"Responses",
	)
	responsesAggregate.AgentEndpoint.ClientDialect =
		access.DialectOpenAIResponses
	responsesPlan, err := compiler.Compile(responsesAggregate)
	if err != nil {
		t.Fatalf("compile Responses plan: %v", err)
	}

	codec := responsesPlan.CodecPlan()
	operations := codec.ClientOperations()
	if codec.ID() != responsesCodecID ||
		codec.Revision() != 1 ||
		codec.ClientDialect() != access.DialectOpenAIResponses ||
		len(operations) != 1 ||
		operations[0].ID() != responsesOperation.ID() ||
		operations[0].Revision() != 1 ||
		operations[0].PathMatch() != access.ClientOperationPathExact ||
		operations[0].PathPattern() != "/v1/responses" ||
		operations[0].Kind() != access.ClientOperationSemantic ||
		operations[0].CodecFeature() != "responses" {
		t.Fatalf("compiled Responses codec operation = %+v", codec)
	}
	if methods := operations[0].Methods(); len(methods) != 1 ||
		methods[0] != "POST" {
		t.Fatalf("compiled Responses methods = %v", methods)
	}
	if responsesPlan.PlanHash() == anthropicPlan.PlanHash() {
		t.Fatal("different client operations produced the same PlanHash")
	}
	foundOperationDependency := false
	for _, dependency := range responsesPlan.DependencyRevisions() {
		if dependency.Kind == access.DependencyClientOperation {
			foundOperationDependency = true
			if dependency.ID != responsesOperation.ID().String() ||
				dependency.Revision != responsesOperation.Revision() {
				t.Fatalf("client operation dependency = %+v", dependency)
			}
		}
	}
	if !foundOperationDependency {
		t.Fatal("client operation dependency is missing")
	}

	methods := operations[0].Methods()
	methods[0] = "DELETE"
	if responsesPlan.CodecPlan().ClientOperations()[0].Methods()[0] != "POST" {
		t.Fatal("client operation getter aliases the active plan")
	}
}

func TestCatalogRejectsMissingOrMismatchedCodecOperations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*testing.T, *access.CatalogOptions)
	}{
		{
			name: "missing operation",
			mutate: func(t *testing.T, options *access.CatalogOptions) {
				missing, err := access.NewClientOperationID("missing-operation")
				if err != nil {
					t.Fatal(err)
				}
				options.CodecPairs[0].ClientOperationIDs =
					[]access.ClientOperationID{missing}
			},
		},
		{
			name: "mismatched dialect",
			mutate: func(t *testing.T, options *access.CatalogOptions) {
				options.ClientOperations[0] = mustClientOperationDefinition(
					t,
					"responses-operation",
					access.DialectOpenAIResponses,
					"POST",
					"/v1/responses",
					"responses",
				)
				options.CodecPairs[0].ClientOperationIDs =
					[]access.ClientOperationID{
						options.ClientOperations[0].ID(),
					}
			},
		},
		{
			name: "duplicate operation reference",
			mutate: func(_ *testing.T, options *access.CatalogOptions) {
				operationID := options.ClientOperations[0].ID()
				options.CodecPairs[0].ClientOperationIDs =
					[]access.ClientOperationID{operationID, operationID}
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			options := testCatalogOptions(t)
			test.mutate(t, &options)
			if _, err := access.NewCatalog(options); err == nil {
				t.Fatal("invalid client operation catalog was accepted")
			}
		})
	}
}

func TestCompilerMarksLiteralLoopbackCleartextTarget(t *testing.T) {
	t.Parallel()

	compiler := testCompiler(t)
	aggregate := testAggregate(
		t,
		newAccessID(t, "access-loopback-cleartext"),
		1,
		"Loopback",
	)
	origin, err := access.NewProviderOrigin(
		"http://127.0.0.1:23333/v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	aggregate.ProviderTargets[0].Origin = origin
	plan, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	targets := plan.ProviderTargets()
	if len(targets) != 1 {
		t.Fatalf("compiled targets = %d", len(targets))
	}
	target := targets[0]
	if target.TransportKind() !=
		access.ProviderTransportLoopbackCleartext ||
		target.NetworkHost() != "127.0.0.1" ||
		target.HTTPAuthority() != "127.0.0.1:23333" ||
		target.TLSServerName() != "" ||
		target.Port() != 23333 {
		t.Fatalf(
			"compiled loopback target = kind=%q host=%q authority=%q SNI=%q port=%d",
			target.TransportKind(),
			target.NetworkHost(),
			target.HTTPAuthority(),
			target.TLSServerName(),
			target.Port(),
		)
	}
}

func TestCompilerCatalogDoesNotRetainInputAliases(t *testing.T) {
	t.Parallel()

	codecID, err := access.NewCodecPairID("anthropic-messages-to-openai-chat")
	if err != nil {
		t.Fatalf("construct codec ID: %v", err)
	}
	required := []access.ProviderCapability{
		access.ProviderCapabilityMessages,
		access.ProviderCapabilityStreaming,
		access.ProviderCapabilityToolCalls,
	}
	operation := mustClientOperationDefinition(
		t,
		"anthropic-messages-create",
		access.DialectAnthropicMessages,
		"POST",
		"/v1/messages",
		"messages",
	)
	options := access.CatalogOptions{
		Capabilities: access.PlanCapabilities{
			MaxEndpointProfiles: 1,
			MaxAccountBindings:  1,
			MaxRouteSets:        1,
		},
		ClientOperations: []access.ClientOperationDefinition{operation},
		CodecPairs: []access.CodecPairDefinition{{
			ID:              codecID,
			Revision:        1,
			ClientDialect:   access.DialectAnthropicMessages,
			ProviderDialect: access.DialectOpenAIChat,
			ClientOperationIDs: []access.ClientOperationID{
				operation.ID(),
			},
			RequiredCapabilities: required,
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
		TransportProfiles:    access.BuiltInTransportFingerprintDefinitions(),
		UpstreamWireProfiles: access.BuiltInUpstreamWireProfileDefinitions(),
	}
	catalog, err := access.NewCatalog(options)
	if err != nil {
		t.Fatalf("construct catalog: %v", err)
	}
	required[0] = "mutated"
	options.CodecPairs[0].RequiredCapabilities = nil
	options.CodecPairs[0].ClientOperationIDs = nil
	options.ClientOperations = nil
	options.TransportProfiles[0].ALPN[0] = access.ApplicationProtocolHTTP2
	options.TransportProfiles[0].FallbackRefs = nil
	compiler, err := access.NewCompiler(catalog)
	if err != nil {
		t.Fatalf("construct compiler: %v", err)
	}
	plan, err := compiler.Compile(
		testAggregate(t, newAccessID(t, "access-catalog-alias"), 1, "Catalog"),
	)
	if err != nil {
		t.Fatalf("compile after catalog input mutation: %v", err)
	}
	if len(plan.CodecPlan().RequiredCapabilities()) != 3 {
		t.Fatalf(
			"required capabilities = %v",
			plan.CodecPlan().RequiredCapabilities(),
		)
	}
	if operations := plan.CodecPlan().ClientOperations(); len(operations) != 1 ||
		operations[0].PathPattern() != "/v1/messages" {
		t.Fatalf("client operation catalog retained input aliases: %+v", operations)
	}
	variant, ok := plan.UpstreamWireProfile().Variant(access.ApplicationProtocolHTTP1)
	if !ok {
		t.Fatal("compiled plan has no HTTP/1.1 wire variant")
	}
	transport := variant.TransportFingerprintPlan()
	if transport.Requested().ALPN()[0] != access.ApplicationProtocolHTTP1 ||
		len(transport.Fallbacks()) != 0 {
		t.Fatalf(
			"transport catalog retained input aliases: requested=%+v fallbacks=%+v",
			transport.Requested(),
			transport.Fallbacks(),
		)
	}
}

func TestCompilerFailsClosedForInvalidExecutableReferences(t *testing.T) {
	t.Parallel()

	compiler := testCompiler(t)
	otherAccessID := newAccessID(t, "access-other")
	tests := []struct {
		name     string
		mutate   func(*testing.T, *access.Aggregate)
		specific error
	}{
		{
			name: "invalid endpoint reference",
			mutate: func(t *testing.T, aggregate *access.Aggregate) {
				aggregate.Binding.AgentEndpointID = mustAgentEndpointID(t, "missing-endpoint")
			},
			specific: access.ErrOwnershipViolation,
		},
		{
			name: "cross Access profile",
			mutate: func(_ *testing.T, aggregate *access.Aggregate) {
				aggregate.Profiles[0].AccessID = otherAccessID
			},
			specific: access.ErrOwnershipViolation,
		},
		{
			name: "dangling account",
			mutate: func(t *testing.T, aggregate *access.Aggregate) {
				missing := mustAccountBindingID(t, "missing-account")
				aggregate.Profiles[0].AccountBindingIDs = []access.AccountBindingID{
					missing,
				}
				aggregate.Profiles[0].DefaultAccountBindingID = missing
			},
			specific: access.ErrDanglingReference,
		},
		{
			name: "invalid target",
			mutate: func(_ *testing.T, aggregate *access.Aggregate) {
				aggregate.ProviderTargets[0].Origin = access.ProviderOrigin{}
			},
			specific: access.ErrInvalidAccess,
		},
		{
			name: "unknown client dialect",
			mutate: func(_ *testing.T, aggregate *access.Aggregate) {
				aggregate.AgentEndpoint.ClientDialect = "unknown-client"
			},
			specific: access.ErrUnknownDialect,
		},
		{
			name: "unknown provider dialect",
			mutate: func(_ *testing.T, aggregate *access.Aggregate) {
				aggregate.Profiles[0].BackendDialect = "unknown-provider"
				aggregate.ProviderTargets[0].Protocol = "unknown-provider"
			},
			specific: access.ErrUnknownDialect,
		},
		{
			name: "unknown egress",
			mutate: func(_ *testing.T, aggregate *access.Aggregate) {
				aggregate.EgressPolicy.Mode = "unknown-egress"
			},
			specific: access.ErrUnknownEgressMode,
		},
		{
			name: "missing codec capability",
			mutate: func(_ *testing.T, aggregate *access.Aggregate) {
				aggregate.ProviderTargets[0].Capabilities = []access.ProviderCapability{
					access.ProviderCapabilityMessages,
					access.ProviderCapabilityStreaming,
				}
			},
			specific: access.ErrCapabilityMismatch,
		},
		{
			name: "unknown auth driver",
			mutate: func(t *testing.T, aggregate *access.Aggregate) {
				ref, err := access.NewAuthDriverRef("unknown-auth")
				if err != nil {
					t.Fatalf("construct unknown AuthDriver reference: %v", err)
				}
				aggregate.AccountBindings[0].AuthDriverRef = ref
			},
			specific: access.ErrUnknownAuthDriver,
		},
		{
			name: "non pass-through plugin mode",
			mutate: func(_ *testing.T, aggregate *access.Aggregate) {
				aggregate.PluginPlan.Mode = "active-plugin-chain"
			},
			specific: access.ErrUnknownPluginPlanMode,
		},
		{
			name: "unknown transport profile",
			mutate: func(t *testing.T, aggregate *access.Aggregate) {
				reference, err := access.NewUpstreamWireProfileRef("unknown-wire-profile")
				if err != nil {
					t.Fatalf("construct unknown transport profile reference: %v", err)
				}
				aggregate.Profiles[0].UpstreamWireProfileRef = reference
			},
			specific: access.ErrUnknownUpstreamWireProfile,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			aggregate := testAggregate(
				t,
				newAccessID(t, "access-invalid-"+testNameID(test.name)),
				1,
				"Invalid",
			)
			test.mutate(t, &aggregate)
			plan, err := compiler.Compile(aggregate)
			if !plan.PlanHash().IsZero() {
				t.Fatalf("invalid aggregate produced plan hash %s", plan.PlanHash())
			}
			if !errors.Is(err, access.ErrInvalidAccessPlan) {
				t.Fatalf("compile error = %v, want invalid plan", err)
			}
			if !errors.Is(err, test.specific) {
				t.Fatalf("compile error = %v, want %v", err, test.specific)
			}
		})
	}
}

func TestCatalogFailsClosedForInvalidTransportProfiles(t *testing.T) {
	t.Parallel()

	unknownReference, err := access.NewTransportProfileRef("unknown-fallback")
	if err != nil {
		t.Fatalf("construct unknown fallback reference: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*access.CatalogOptions)
		want   error
	}{
		{
			name: "unknown fallback",
			mutate: func(options *access.CatalogOptions) {
				options.TransportProfiles[0].FallbackRefs = []access.TransportProfileRef{
					unknownReference,
				}
			},
			want: access.ErrUnknownTransportProfile,
		},
		{
			name: "fallback cycle",
			mutate: func(options *access.CatalogOptions) {
				options.TransportProfiles[0].FallbackRefs = []access.TransportProfileRef{
					access.StandardH1TransportProfileRef(),
				}
				options.TransportProfiles[2].FallbackRefs = []access.TransportProfileRef{
					access.ObservedClientH1TransportProfileRef(),
				}
			},
		},
		{
			name: "invalid HTTP transport",
			mutate: func(options *access.CatalogOptions) {
				options.TransportProfiles[0].HTTPTransport =
					access.HTTPTransportKind("http3")
				options.TransportProfiles[0].ALPN = []access.ApplicationProtocol{
					access.ApplicationProtocolHTTP2,
				}
			},
		},
		{
			name: "HTTP2 ALPN on HTTP1 transport",
			mutate: func(options *access.CatalogOptions) {
				options.TransportProfiles[0].ALPN = []access.ApplicationProtocol{
					access.ApplicationProtocolHTTP2,
				}
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			options := testCatalogOptions(t)
			test.mutate(&options)
			_, err := access.NewCatalog(options)
			if err == nil {
				t.Fatal("invalid transport profile constructed a catalog")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("catalog error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestOriginsRejectAmbiguousNetworkIdentity(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"http://api.openai.com/v1",
		"http://localhost:23333/v1",
		"http://192.168.1.10:23333/v1",
		"http://10.0.0.8:23333/v1",
		"http://[::ffff:127.0.0.1]:23333/v1",
		"https://user@api.openai.com/v1",
		"https://*.openai.com/v1",
		"https://api.openai.com/v1/",
		"https://api.openai.com/v1?target=other",
	} {
		if _, err := access.NewProviderOrigin(value); err == nil {
			t.Fatalf("ProviderOrigin accepted %q", value)
		}
	}
	if _, err := access.NewClientOrigin("https://api.anthropic.com/v1"); err == nil {
		t.Fatal("ClientOrigin accepted a provider path")
	}
	if _, err := access.NewClientOrigin("http://127.0.0.1:23333"); err == nil {
		t.Fatal("ClientOrigin accepted cleartext HTTP")
	}
}

func TestProviderOriginAcceptsOnlyLiteralLoopbackCleartext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value     string
		canonical string
		host      string
		authority string
		port      uint16
	}{
		{
			value:     "http://127.0.0.1:23333/v1",
			canonical: "http://127.0.0.1:23333/v1",
			host:      "127.0.0.1",
			authority: "127.0.0.1:23333",
			port:      23333,
		},
		{
			value:     "http://[::1]:23333/v1",
			canonical: "http://[::1]:23333/v1",
			host:      "::1",
			authority: "[::1]:23333",
			port:      23333,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.value, func(t *testing.T) {
			origin, err := access.NewProviderOrigin(test.value)
			if err != nil {
				t.Fatalf("NewProviderOrigin() error = %v", err)
			}
			if origin.String() != test.canonical ||
				origin.Scheme() != "http" ||
				origin.NetworkHost() != test.host ||
				origin.HTTPAuthority() != test.authority ||
				origin.EndpointAuthority() != test.authority ||
				origin.TLSServerName() != "" ||
				origin.Port() != test.port ||
				origin.TransportKind() !=
					access.ProviderTransportLoopbackCleartext {
				t.Fatalf("ProviderOrigin = %#v", origin)
			}
		})
	}
}

func testNameID(name string) string {
	value := make([]byte, 0, len(name))
	for _, character := range []byte(name) {
		if character == ' ' {
			value = append(value, '-')
			continue
		}
		value = append(value, character)
	}
	return string(value)
}
