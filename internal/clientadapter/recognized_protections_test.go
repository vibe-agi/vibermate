package clientadapter

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// designedRecognizedClientPolicies is the table ADR 0026 arrives at once every
// grant has its installed-client proof. The live table grants nothing yet, so
// the index, the fallback projection and the canary list are exercised against
// this fixture through the unexported delegates.
func designedRecognizedClientPolicies() []RecognizedClientPolicy {
	return []RecognizedClientPolicy{
		{
			ClientID:       "claude-code",
			IdentityCanary: Canary{"internal/clientadapter", "TestInstalledClaudeIsRecognizedByItsSigner"},
			Grants: []ProtectionGrant{
				{FeatureCoreOwnedStreamingFallback, ProofLaunchInstruction,
					Canary{"internal/runlauncher", "TestInstalledClaudeLeavesStreamFallbackToCore"}},
				{FeatureStructuredWorkspaceTools, ProofRequestDefinitions,
					Canary{"internal/runlauncher", "TestInstalledClaudeWorkspaceToolsMatchTheClassifier"}},
			},
		},
		{
			ClientID:       "codex-cli",
			IdentityCanary: Canary{"internal/clientadapter", "TestInstalledCodexIsRecognizedByItsSigner"},
			Grants: []ProtectionGrant{
				{FeatureResponsesDataKeepalive, ProofStreamFrame,
					Canary{"internal/loopbackproxy", "TestInstalledCodexAcceptsTheDataKeepalive"}},
			},
			Unconditional: []ProtectionGrant{
				{FeatureResponsesWebSocketHTTPFallback, ProofOperationCatalog,
					Canary{"internal/loopbackproxy", "TestInstalledCodexFallsBackToHTTPAfterProxy426"}},
			},
		},
	}
}

func TestRecognizedClientPolicyIsPinned(t *testing.T) {
	t.Parallel()

	// Both clients are named and nothing is granted: every reader of a
	// recognized basis stays inactive until a grant arrives with its proof.
	want := []RecognizedClientPolicy{
		{ClientID: "claude-code"},
		{ClientID: "codex-cli"},
	}
	got := RecognizedClientPolicies()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RecognizedClientPolicies() = %+v, want %+v", got, want)
	}
	for _, clientID := range []string{"claude-code", "codex-cli", "other", ""} {
		if features := RecognizedPolicyFeatures(clientID); features != 0 {
			t.Fatalf("RecognizedPolicyFeatures(%q) = %b, want 0", clientID, features)
		}
		for _, goos := range []string{"linux", "darwin"} {
			if canaries := ProtectionCanaries(clientID, goos); len(canaries) != 0 {
				t.Fatalf("ProtectionCanaries(%q, %q) = %+v, want none", clientID, goos, canaries)
			}
		}
	}

	got[0].ClientID = "changed"
	got[1].Grants = append(got[1].Grants, ProtectionGrant{Feature: FeatureResponsesDataKeepalive})
	if again := RecognizedClientPolicies(); !reflect.DeepEqual(again, want) {
		t.Fatalf("a caller changed the policy table through its copy: %+v", again)
	}

	designed := designedRecognizedClientPolicies()
	cloned := cloneRecognizedClientPolicies(designed)
	if !reflect.DeepEqual(cloned, designed) {
		t.Fatalf("clone = %+v, want %+v", cloned, designed)
	}
	cloned[0].Grants[0].Feature = FeatureResponsesDataKeepalive
	cloned[1].Unconditional[0].Canary.Test = "changed"
	if !reflect.DeepEqual(designed, designedRecognizedClientPolicies()) {
		t.Fatal("a copy shares its grants with the table it was taken from")
	}
}

// recognizedPolicyViolations applies the rules a compiled-in table cannot
// check at start-up (ADR 0026).
func recognizedPolicyViolations(policies []RecognizedClientPolicy) []string {
	signerIDs := map[string]bool{}
	for _, signer := range BuiltInCatalog().signers {
		signerIDs[signer.ID] = true
	}
	prefixes := map[string]string{
		"claude-code": "TestInstalledClaude",
		"codex-cli":   "TestInstalledCodex",
	}
	var violations []string
	seen := map[string]bool{}
	for _, policy := range policies {
		if seen[policy.ClientID] {
			violations = append(violations, policy.ClientID+": duplicate client")
		}
		seen[policy.ClientID] = true
		if !signerIDs[policy.ClientID] {
			violations = append(violations, policy.ClientID+": not a built-in signer")
		}
		prefix := prefixes[policy.ClientID]
		checkCanary := func(where string, canary Canary) {
			if canary.Package == "" || canary.Test == "" ||
				prefix == "" || !strings.HasPrefix(canary.Test, prefix) {
				violations = append(violations, fmt.Sprintf(
					"%s: %s canary %+v lacks the client's prefix",
					policy.ClientID, where, canary,
				))
			}
		}
		if policy.IdentityCanary != (Canary{}) {
			checkCanary("identity", policy.IdentityCanary)
		}
		for _, grant := range policy.Grants {
			if !grant.Feature.Single() {
				violations = append(violations, fmt.Sprintf(
					"%s: grant %b is not one known bit", policy.ClientID, grant.Feature,
				))
			}
			if grant.Feature == FeatureResponsesWebSocketHTTPFallback {
				violations = append(violations, policy.ClientID+
					": the WebSocket fallback is decided for every client and is never granted")
			}
			if grant.Feature == FeatureCoreOwnedStreamingFallback &&
				policy.ClientID != "claude-code" {
				violations = append(violations, policy.ClientID+
					": core_owned exists only for claude-code")
			}
			checkCanary("grant", grant.Canary)
		}
		for _, grant := range policy.Unconditional {
			if grant.Feature != FeatureResponsesWebSocketHTTPFallback {
				violations = append(violations, fmt.Sprintf(
					"%s: unconditional %b is not decided for every client",
					policy.ClientID, grant.Feature,
				))
			}
			checkCanary("unconditional", grant.Canary)
		}
	}
	return violations
}

func TestRecognizedClientPoliciesAreConsistent(t *testing.T) {
	t.Parallel()

	if violations := recognizedPolicyViolations(RecognizedClientPolicies()); len(violations) != 0 {
		t.Fatalf("the policy table is inconsistent: %v", violations)
	}
	if violations := recognizedPolicyViolations(designedRecognizedClientPolicies()); len(violations) != 0 {
		t.Fatalf("the designed table is inconsistent: %v", violations)
	}

	// Each rule must be able to fail.
	for _, testCase := range []struct {
		name   string
		mutate func([]RecognizedClientPolicy) []RecognizedClientPolicy
	}{
		{"a grant of two bits", func(policies []RecognizedClientPolicy) []RecognizedClientPolicy {
			policies[0].Grants[0].Feature |= FeatureStructuredWorkspaceTools
			return policies
		}},
		{"a grant of an unknown bit", func(policies []RecognizedClientPolicy) []RecognizedClientPolicy {
			policies[0].Grants[0].Feature = 1 << 40
			return policies
		}},
		{"the WebSocket fallback as a grant", func(policies []RecognizedClientPolicy) []RecognizedClientPolicy {
			policies[1].Grants = append(policies[1].Grants, policies[1].Unconditional[0])
			return policies
		}},
		{"core_owned for Codex", func(policies []RecognizedClientPolicy) []RecognizedClientPolicy {
			policies[1].Grants = append(policies[1].Grants, ProtectionGrant{
				FeatureCoreOwnedStreamingFallback, ProofLaunchInstruction,
				Canary{"internal/runlauncher", "TestInstalledCodexFallback"},
			})
			return policies
		}},
		{"a duplicate client", func(policies []RecognizedClientPolicy) []RecognizedClientPolicy {
			return append(policies, RecognizedClientPolicy{ClientID: "codex-cli"})
		}},
		{"a client no signer names", func(policies []RecognizedClientPolicy) []RecognizedClientPolicy {
			return append(policies, RecognizedClientPolicy{ClientID: "gemini-cli"})
		}},
		{"another client's canary", func(policies []RecognizedClientPolicy) []RecognizedClientPolicy {
			policies[0].Grants[1].Canary.Test = "TestInstalledCodexWorkspaceTools"
			return policies
		}},
		{"a grant without a canary", func(policies []RecognizedClientPolicy) []RecognizedClientPolicy {
			policies[1].Grants[0].Canary = Canary{}
			return policies
		}},
		{"an identity canary without the prefix", func(policies []RecognizedClientPolicy) []RecognizedClientPolicy {
			policies[1].IdentityCanary.Test = "TestCodexIsRecognized"
			return policies
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			policies := testCase.mutate(designedRecognizedClientPolicies())
			if violations := recognizedPolicyViolations(policies); len(violations) == 0 {
				t.Fatalf("%s was accepted", testCase.name)
			}
		})
	}
}

func TestFeatureIncludesRejectsZeroUnknownAndMultiBitQueries(t *testing.T) {
	t.Parallel()

	known := []Feature{
		FeatureResponsesWebSocketHTTPFallback,
		FeatureCoreOwnedStreamingFallback,
		FeatureStructuredWorkspaceTools,
		FeatureResponsesDataKeepalive,
	}
	names := []string{
		"responses_websocket_http_fallback",
		"core_owned_streaming_fallback",
		"structured_workspace_tools",
		"responses_data_keepalive",
	}
	const unknown Feature = 1 << 40
	for index, feature := range known {
		if !feature.Single() || feature.Name() != names[index] {
			t.Fatalf("%b: Single() = %v, Name() = %q", feature, feature.Single(), feature.Name())
		}
		if !knownFeatures.Includes(feature) || !feature.Includes(feature) {
			t.Fatalf("%b does not include itself", feature)
		}
		if (knownFeatures &^ feature).Includes(feature) {
			t.Fatalf("a set without %b includes it", feature)
		}
		if feature.Includes(unknown) || (feature | unknown).Includes(unknown) {
			t.Fatalf("an unknown bit was included alongside %b", feature)
		}
	}
	multi := FeatureCoreOwnedStreamingFallback | FeatureStructuredWorkspaceTools
	for _, feature := range []Feature{0, unknown, multi, knownFeatures, unknown | FeatureStructuredWorkspaceTools} {
		if feature.Single() || feature.Name() != "" {
			t.Fatalf("%b: Single() = %v, Name() = %q", feature, feature.Single(), feature.Name())
		}
	}
	if knownFeatures.Includes(0) || Feature(0).Includes(0) {
		t.Fatal("a zero query was included")
	}
	if !knownFeatures.Includes(multi) || FeatureStructuredWorkspaceTools.Includes(multi) {
		t.Fatal("a multi-bit query must be included only when every bit is carried")
	}
	if knownFeatures.Includes(unknown) || (knownFeatures | unknown).Includes(unknown|FeatureStructuredWorkspaceTools) {
		t.Fatal("a query with an unknown bit was included")
	}
	// Includes gives a query the meaning Evidence.Supports gives it.
	evidence := Evidence{Features: FeatureResponsesDataKeepalive}
	for _, feature := range append(known, 0, unknown, multi) {
		if evidence.Supports(feature) != evidence.Features.Includes(feature) {
			t.Fatalf("Supports and Includes disagree on %b", feature)
		}
	}
}

func TestProtectionBasisNames(t *testing.T) {
	t.Parallel()

	for basis, name := range map[ProtectionBasis]string{
		ProtectionNone:             "none",
		ProtectionExactRelease:     "exact_release",
		ProtectionRecognizedClient: "recognized_client",
	} {
		if !basis.Valid() || basis.String() != name {
			t.Fatalf("%d: Valid() = %v, String() = %q", basis, basis.Valid(), basis.String())
		}
	}
	if invalid := ProtectionRecognizedClient + 1; invalid.Valid() || invalid.String() != "" {
		t.Fatalf("%d is valid or named", invalid)
	}
}

func TestRecognizedStreamingFallbackNeedsClaudeItsRecipeAndASignedBinary(t *testing.T) {
	t.Parallel()

	designed := designedRecognizedClientPolicies()
	if policy := streamingFallbackPolicyFor(
		designed, "claude-code", LaunchNodeEnvProxy, InstallNativeSingleBinary,
	); policy != StreamingFallbackCoreOwned {
		t.Fatalf("a granted, signed Claude Code got %q", policy)
	}
	for _, testCase := range []struct {
		name     string
		clientID string
		recipe   LaunchRecipe
		shape    InstallShape
	}{
		{"a wrapper shape", "claude-code", LaunchNodeEnvProxy, InstallNPMWrapperNativeChild},
		{"another recipe", "claude-code", LaunchCodexResponsesHTTP, InstallNativeSingleBinary},
		{"the generic recipe", "claude-code", LaunchGeneric, InstallNativeSingleBinary},
		{"Codex", "codex-cli", LaunchNodeEnvProxy, InstallNativeSingleBinary},
		{"an unknown client", "other", LaunchNodeEnvProxy, InstallNativeSingleBinary},
		{"no client", "", LaunchNodeEnvProxy, InstallNativeSingleBinary},
	} {
		if policy := streamingFallbackPolicyFor(
			designed, testCase.clientID, testCase.recipe, testCase.shape,
		); policy != StreamingFallbackClientDefault {
			t.Fatalf("%s got %q", testCase.name, policy)
		}
	}
	// The live table grants no fallback yet.
	if policy := RecognizedStreamingFallbackPolicy(
		"claude-code", LaunchNodeEnvProxy, InstallNativeSingleBinary,
	); policy != StreamingFallbackClientDefault {
		t.Fatalf("the live table gave Claude Code %q", policy)
	}
}

func TestRecognizedClientsNameOnlyTheCurrentRevision(t *testing.T) {
	t.Parallel()

	catalog := BuiltInCatalog()
	clients, err := newRecognizedClients(catalog, ProtectionsOn, designedRecognizedClientPolicies())
	if err != nil {
		t.Fatal(err)
	}
	claude := FeatureCoreOwnedStreamingFallback | FeatureStructuredWorkspaceTools
	// The built-in signers are all darwin/arm64, and they are indexed on
	// every GOOS: a Linux Server stores runs darwin companions recognized.
	for label, want := range map[string]RecognizedClient{
		"claude": {ID: "claude-code", Policy: claude, Protections: claude},
		"codex": {
			ID:          "codex-cli",
			Policy:      FeatureResponsesDataKeepalive,
			Protections: FeatureResponsesDataKeepalive,
		},
	} {
		got, found := clients.Client(catalog.Revision(), label)
		if !found || got != want || got.SwitchedOff() {
			t.Fatalf("Client(%d, %q) = %+v, %v, want %+v", catalog.Revision(), label, got, found, want)
		}
	}
	for _, testCase := range []struct {
		revision CatalogRevision
		label    string
	}{
		{catalog.Revision() - 1, "claude"},
		{catalog.Revision() + 1, "claude"},
		{catalog.Revision() - 1, "codex"},
		{0, "claude"},
		{catalog.Revision(), "Claude"},
		{catalog.Revision(), "claude-code"},
		{catalog.Revision(), ""},
		{catalog.Revision(), "other"},
	} {
		if got, found := clients.Client(testCase.revision, testCase.label); found || got != (RecognizedClient{}) {
			t.Fatalf("Client(%d, %q) = %+v, %v", testCase.revision, testCase.label, got, found)
		}
	}
	var zero RecognizedClients
	for _, revision := range []CatalogRevision{0, catalog.Revision()} {
		if got, found := zero.Client(revision, "claude"); found || got != (RecognizedClient{}) {
			t.Fatalf("the zero index named %+v at revision %d", got, revision)
		}
	}

	// The live table names both clients and grants neither anything.
	live, err := NewRecognizedClients(catalog, ProtectionsOn)
	if err != nil {
		t.Fatal(err)
	}
	for label, id := range map[string]string{"claude": "claude-code", "codex": "codex-cli"} {
		got, found := live.Client(catalog.Revision(), label)
		if !found || got != (RecognizedClient{ID: id}) {
			t.Fatalf("live Client(%q) = %+v, %v", label, got, found)
		}
	}
}

func TestRecognizedClientsRefuseAnInvalidCatalogOrSwitch(t *testing.T) {
	t.Parallel()

	if _, err := NewRecognizedClients(Catalog{}, ProtectionsOn); err == nil {
		t.Fatal("the zero catalog was indexed")
	}
	if _, err := NewRecognizedClients(BuiltInCatalog(), ProtectionsOff+1); err == nil {
		t.Fatal("an unknown switch state was accepted")
	}
	exactOnly, err := NewCatalog(3, []Release{ClaudeCode221220DarwinARM64()})
	if err != nil {
		t.Fatal(err)
	}
	clients, err := newRecognizedClients(exactOnly, ProtectionsOn, designedRecognizedClientPolicies())
	if err != nil {
		t.Fatal(err)
	}
	if got, found := clients.Client(3, "claude"); found {
		t.Fatalf("a catalog without signers named %+v", got)
	}
}

func TestRecognizedClientsWithholdAuthorityFromWrapperShapes(t *testing.T) {
	t.Parallel()

	wrapper := ClaudeCodeSignerDarwin()
	wrapper.InstallShape = InstallNPMWrapperNativeChild
	wrapper.CanonicalEntrypointName = "cli.js"
	wrapper.ArtifactRoot = ".."
	wrapper.SignedRelativePath = "../node_modules/claude-native/claude"
	catalog, err := NewCatalogWithSigners(
		9,
		[]Release{ClaudeCode221220DarwinARM64()},
		[]Signer{ClaudeCodeSignerDarwin(), wrapper, CodexCLISignerDarwin(), CodexCLINativeSignerDarwin()},
	)
	if err != nil {
		t.Fatal(err)
	}
	policies := designedRecognizedClientPolicies()
	// An authority-bearing grant to Codex is withheld for the same reason.
	policies[1].Grants = append(policies[1].Grants, ProtectionGrant{
		FeatureStructuredWorkspaceTools, ProofRequestDefinitions,
		Canary{"internal/runlauncher", "TestInstalledCodexWorkspaceTools"},
	})
	clients, err := newRecognizedClients(catalog, ProtectionsOn, policies)
	if err != nil {
		t.Fatal(err)
	}
	for label, want := range map[string]RecognizedClient{
		"claude": {ID: "claude-code"},
		"codex": {
			ID:          "codex-cli",
			Policy:      FeatureResponsesDataKeepalive,
			Protections: FeatureResponsesDataKeepalive,
		},
	} {
		if got, found := clients.Client(9, label); !found || got != want {
			t.Fatalf("Client(%q) = %+v, %v, want %+v", label, got, found, want)
		}
	}
}

func TestProtectionSwitchParsing(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		value      string
		state      ProtectionSwitch
		recognized bool
	}{
		{"", ProtectionsOn, true},
		{"on", ProtectionsOn, true},
		{"ON", ProtectionsOn, true},
		{" On ", ProtectionsOn, true},
		{"1", ProtectionsOn, true},
		{"true", ProtectionsOn, true},
		{"Yes", ProtectionsOn, true},
		{"off", ProtectionsOff, true},
		{"OFF", ProtectionsOff, true},
		{"\toff\n", ProtectionsOff, true},
		{"0", ProtectionsOff, true},
		{"false", ProtectionsOff, true},
		{"FALSE", ProtectionsOff, true},
		{"no", ProtectionsOff, true},
		{"banana", ProtectionsOn, false},
		{"disabled", ProtectionsOn, false},
		{"o ff", ProtectionsOn, false},
		{"2", ProtectionsOn, false},
	} {
		state, recognized := ParseProtectionSwitch(testCase.value)
		if state != testCase.state || recognized != testCase.recognized {
			t.Fatalf("ParseProtectionSwitch(%q) = %d, %v", testCase.value, state, recognized)
		}
	}

	for _, testCase := range []struct {
		name        string
		environment []string
		state       ProtectionSwitch
		value       string
		recognized  bool
	}{
		{"unset", []string{"PATH=/bin"}, ProtectionsOn, "", true},
		{"nil", nil, ProtectionsOn, "", true},
		{"off", []string{RecognizedProtectionsVariable + "=off"}, ProtectionsOff, "off", true},
		{"the last assignment wins", []string{
			RecognizedProtectionsVariable + "=off",
			RecognizedProtectionsVariable + "=on",
		}, ProtectionsOn, "on", true},
		{"another casing is another variable", []string{
			RecognizedProtectionsVariable + "=0",
			strings.ToLower(RecognizedProtectionsVariable) + "=on",
		}, ProtectionsOff, "0", true},
		{"an entry without a value is skipped", []string{
			RecognizedProtectionsVariable + "=no",
			RecognizedProtectionsVariable,
		}, ProtectionsOff, "no", true},
		{"an unrecognized value", []string{RecognizedProtectionsVariable + "=banana"}, ProtectionsOn, "banana", false},
	} {
		state, value, recognized := ProtectionSwitchFromEnvironment(testCase.environment)
		if state != testCase.state || value != testCase.value || recognized != testCase.recognized {
			t.Fatalf("%s: ProtectionSwitchFromEnvironment() = %d, %q, %v",
				testCase.name, state, value, recognized)
		}
	}
}

func TestRecognizedClientsHonourTheSwitch(t *testing.T) {
	t.Parallel()

	catalog := BuiltInCatalog()
	clients, err := newRecognizedClients(catalog, ProtectionsOff, designedRecognizedClientPolicies())
	if err != nil {
		t.Fatal(err)
	}
	claude := FeatureCoreOwnedStreamingFallback | FeatureStructuredWorkspaceTools
	for label, want := range map[string]RecognizedClient{
		"claude": {ID: "claude-code", Policy: claude},
		"codex":  {ID: "codex-cli", Policy: FeatureResponsesDataKeepalive},
	} {
		got, found := clients.Client(catalog.Revision(), label)
		if !found || got != want || !got.SwitchedOff() {
			t.Fatalf("Client(%q) = %+v, %v, want %+v switched off", label, got, found, want)
		}
	}
	// A client the policy grants nothing is not switched off: there was
	// nothing to withdraw.
	live, err := NewRecognizedClients(catalog, ProtectionsOff)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := live.Client(catalog.Revision(), "claude"); got.SwitchedOff() {
		t.Fatalf("an empty policy reads as switched off: %+v", got)
	}
	if (RecognizedClient{}).SwitchedOff() {
		t.Fatal("no client reads as switched off")
	}
}

func TestProtectionCanariesPerOperatingSystem(t *testing.T) {
	t.Parallel()

	designed := designedRecognizedClientPolicies()
	for _, testCase := range []struct {
		clientID string
		goos     string
		want     []Canary
	}{
		{"claude-code", "linux", []Canary{
			{"internal/runlauncher", "TestInstalledClaudeLeavesStreamFallbackToCore"},
			{"internal/runlauncher", "TestInstalledClaudeWorkspaceToolsMatchTheClassifier"},
		}},
		{"claude-code", "darwin", []Canary{
			{"internal/runlauncher", "TestInstalledClaudeLeavesStreamFallbackToCore"},
			{"internal/runlauncher", "TestInstalledClaudeWorkspaceToolsMatchTheClassifier"},
			{"internal/clientadapter", "TestInstalledClaudeIsRecognizedByItsSigner"},
		}},
		{"codex-cli", "linux", []Canary{
			{"internal/loopbackproxy", "TestInstalledCodexAcceptsTheDataKeepalive"},
			{"internal/loopbackproxy", "TestInstalledCodexFallsBackToHTTPAfterProxy426"},
		}},
		{"codex-cli", "darwin", []Canary{
			{"internal/loopbackproxy", "TestInstalledCodexAcceptsTheDataKeepalive"},
			{"internal/loopbackproxy", "TestInstalledCodexFallsBackToHTTPAfterProxy426"},
			{"internal/clientadapter", "TestInstalledCodexIsRecognizedByItsSigner"},
		}},
		{"other", "darwin", nil},
		{"", "darwin", nil},
	} {
		got := protectionCanaries(designed, testCase.clientID, testCase.goos)
		if !reflect.DeepEqual(got, testCase.want) {
			t.Fatalf("protectionCanaries(%q, %q) = %+v, want %+v",
				testCase.clientID, testCase.goos, got, testCase.want)
		}
	}
}
