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
	options := access.CatalogOptions{
		Capabilities: access.PlanCapabilities{
			MaxEndpointProfiles: 1,
			MaxAccountBindings:  1,
			MaxRouteSets:        1,
		},
		CodecPairs: []access.CodecPairDefinition{{
			ID:                   codecID,
			Revision:             1,
			ClientDialect:        access.DialectAnthropicMessages,
			ProviderDialect:      access.DialectOpenAIChat,
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
		TransportProfiles: []access.TransportFingerprintDefinition{
			access.ObservedClientH1TransportFingerprintDefinition(),
			access.StandardH1TransportFingerprintDefinition(),
		},
	}
	catalog, err := access.NewCatalog(options)
	if err != nil {
		t.Fatalf("construct catalog: %v", err)
	}
	required[0] = "mutated"
	options.CodecPairs[0].RequiredCapabilities = nil
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
	transport := plan.TransportFingerprintPlan()
	if transport.Requested().ALPN()[0] != access.ApplicationProtocolHTTP1 ||
		len(transport.Fallbacks()) != 1 {
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
				reference, err := access.NewTransportProfileRef("unknown-transport")
				if err != nil {
					t.Fatalf("construct unknown transport profile reference: %v", err)
				}
				aggregate.Profiles[0].TransportProfileRef = reference
			},
			specific: access.ErrUnknownTransportProfile,
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
				options.TransportProfiles[1].FallbackRefs = []access.TransportProfileRef{
					access.ObservedClientH1TransportProfileRef(),
				}
			},
		},
		{
			name: "HTTP2 transport",
			mutate: func(options *access.CatalogOptions) {
				options.TransportProfiles[0].HTTPTransport =
					access.HTTPTransportKind("http2")
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
