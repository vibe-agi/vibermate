package clientadapter

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// designedRecognizedClientPolicies is the table ADR 0026 arrives at once every
// grant has its installed-client proof. The live table does not grant Claude
// Code its workspace tools yet, so the index, the fallback projection and the
// canary list are also exercised against this fixture through the unexported
// delegates.
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

	// Claude Code has its stream fallback grant, proven by C1, and its
	// workspace tools grant, proven by C2. Codex has its keepalive grant,
	// proven by C4, and its unconditional WebSocket fallback, proven by C3.
	// Each row names its identity proof (C5). The table is now the one ADR
	// 0026 designs.
	claudeFallback := Canary{"internal/runlauncher", "TestInstalledClaudeLeavesStreamFallbackToCore"}
	claudeTools := Canary{"internal/runlauncher", "TestInstalledClaudeWorkspaceToolsMatchTheClassifier"}
	claudeIdentity := Canary{"internal/clientadapter", "TestInstalledClaudeIsRecognizedByItsSigner"}
	codexKeepalive := Canary{"internal/loopbackproxy", "TestInstalledCodexAcceptsTheDataKeepalive"}
	codexFallback := Canary{"internal/loopbackproxy", "TestInstalledCodexFallsBackToHTTPAfterProxy426"}
	codexIdentity := Canary{"internal/clientadapter", "TestInstalledCodexIsRecognizedByItsSigner"}
	want := []RecognizedClientPolicy{
		{
			ClientID:       "claude-code",
			IdentityCanary: claudeIdentity,
			Grants: []ProtectionGrant{
				{FeatureCoreOwnedStreamingFallback, ProofLaunchInstruction, claudeFallback},
				{FeatureStructuredWorkspaceTools, ProofRequestDefinitions, claudeTools},
			},
		},
		{
			ClientID:       "codex-cli",
			IdentityCanary: codexIdentity,
			Grants: []ProtectionGrant{
				{FeatureResponsesDataKeepalive, ProofStreamFrame, codexKeepalive},
			},
			Unconditional: []ProtectionGrant{
				{FeatureResponsesWebSocketHTTPFallback, ProofOperationCatalog, codexFallback},
			},
		},
	}
	got := RecognizedClientPolicies()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RecognizedClientPolicies() = %+v, want %+v", got, want)
	}
	if designed := designedRecognizedClientPolicies(); !reflect.DeepEqual(want, designed) {
		t.Fatalf("the pinned table %+v differs from the designed table %+v", want, designed)
	}
	for clientID, features := range map[string]Feature{
		"claude-code": FeatureCoreOwnedStreamingFallback | FeatureStructuredWorkspaceTools,
		"codex-cli":   FeatureResponsesDataKeepalive,
		"other":       0,
		"":            0,
	} {
		if got := RecognizedPolicyFeatures(clientID); got != features {
			t.Fatalf("RecognizedPolicyFeatures(%q) = %b, want %b", clientID, got, features)
		}
	}
	for _, testCase := range []struct {
		clientID string
		goos     string
		want     []Canary
	}{
		{"codex-cli", "linux", []Canary{codexKeepalive, codexFallback}},
		{"codex-cli", "darwin", []Canary{codexKeepalive, codexFallback, codexIdentity}},
		{"claude-code", "linux", []Canary{claudeFallback, claudeTools}},
		{"claude-code", "darwin", []Canary{claudeFallback, claudeTools, claudeIdentity}},
		{"other", "darwin", nil},
		{"", "darwin", nil},
	} {
		if canaries := ProtectionCanaries(testCase.clientID, testCase.goos); !reflect.DeepEqual(canaries, testCase.want) {
			t.Fatalf("ProtectionCanaries(%q, %q) = %+v, want %+v",
				testCase.clientID, testCase.goos, canaries, testCase.want)
		}
	}

	got[0].ClientID = "changed"
	got[0].Grants[0].Canary.Test = "changed"
	got[0].Grants = append(got[0].Grants, ProtectionGrant{Feature: FeatureStructuredWorkspaceTools})
	got[1].Grants[0].Feature = FeatureStructuredWorkspaceTools
	got[1].Grants = append(got[1].Grants, ProtectionGrant{Feature: FeatureCoreOwnedStreamingFallback})
	got[1].Unconditional[0].Canary.Test = "changed"
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

// installedTestPrefixes is the name prefix of each client's installed-client
// tests. The canary and pull-request CI select those tests by it.
var installedTestPrefixes = map[string]string{
	"claude-code": "TestInstalledClaude",
	"codex-cli":   "TestInstalledCodex",
}

// recognizedPolicyViolations applies the rules a compiled-in table cannot
// check at start-up (ADR 0026).
func recognizedPolicyViolations(policies []RecognizedClientPolicy) []string {
	signerIDs := map[string]bool{}
	for _, signer := range BuiltInCatalog().signers {
		signerIDs[signer.ID] = true
	}
	prefixes := installedTestPrefixes
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
		// A row that names only its client needs no proof yet. Once it grants
		// anything, the identity canary is the darwin proof that the newest
		// release is still the client the row describes, so it is required:
		// ProtectionCanaries would otherwise drop it without a word.
		if policy.IdentityCanary != (Canary{}) ||
			len(policy.Grants)+len(policy.Unconditional) > 0 {
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
		{"grants without an identity canary", func(policies []RecognizedClientPolicy) []RecognizedClientPolicy {
			policies[0].IdentityCanary = Canary{}
			return policies
		}},
		{"an unconditional entry without an identity canary", func(policies []RecognizedClientPolicy) []RecognizedClientPolicy {
			policies[1].Grants = nil
			policies[1].IdentityCanary = Canary{}
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
	// The live table grants Claude Code the fallback, under the same rule.
	if policy := RecognizedStreamingFallbackPolicy(
		"claude-code", LaunchNodeEnvProxy, InstallNativeSingleBinary,
	); policy != StreamingFallbackCoreOwned {
		t.Fatalf("the live table gave a signed Claude Code %q", policy)
	}
	for _, testCase := range []struct {
		clientID string
		recipe   LaunchRecipe
		shape    InstallShape
	}{
		{"claude-code", LaunchNodeEnvProxy, InstallNPMWrapperNativeChild},
		{"claude-code", LaunchCodexResponsesHTTP, InstallNativeSingleBinary},
		{"codex-cli", LaunchCodexResponsesHTTP, InstallNativeSingleBinary},
		{"codex-cli", LaunchNodeEnvProxy, InstallNativeSingleBinary},
	} {
		if policy := RecognizedStreamingFallbackPolicy(
			testCase.clientID, testCase.recipe, testCase.shape,
		); policy != StreamingFallbackClientDefault {
			t.Fatalf("the live table gave %+v %q", testCase, policy)
		}
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

	// The live table names both clients, grants Claude Code its stream
	// fallback and workspace tools and Codex its keepalive; a stored run at
	// another revision names neither.
	live, err := NewRecognizedClients(catalog, ProtectionsOn)
	if err != nil {
		t.Fatal(err)
	}
	for label, want := range map[string]RecognizedClient{
		"claude": {ID: "claude-code", Policy: claude, Protections: claude},
		"codex": {
			ID:          "codex-cli",
			Policy:      FeatureResponsesDataKeepalive,
			Protections: FeatureResponsesDataKeepalive,
		},
	} {
		got, found := live.Client(catalog.Revision(), label)
		if !found || got != want || got.SwitchedOff() {
			t.Fatalf("live Client(%q) = %+v, %v, want %+v", label, got, found, want)
		}
		if got, found := live.Client(catalog.Revision()-1, label); found || got != (RecognizedClient{}) {
			t.Fatalf("live Client(%d, %q) = %+v, %v", catalog.Revision()-1, label, got, found)
		}
	}
}

// The policy table is validated by tests, not at start-up, so the index masks
// what it reads with the known bits: a bit no reader understands can never
// reach an admission, which refuses unknown bits.
func TestRecognizedPolicyFeaturesCarryOnlyKnownBits(t *testing.T) {
	t.Parallel()

	const unknown Feature = 1 << 40
	policies := designedRecognizedClientPolicies()
	policies[1].Grants[0].Feature |= unknown
	policies[1].Grants = append(policies[1].Grants, ProtectionGrant{
		unknown << 1, ProofStreamFrame,
		Canary{"internal/loopbackproxy", "TestInstalledCodexAcceptsAnUnknownBit"},
	})
	if got := policyFeatures(policies, "codex-cli"); got != FeatureResponsesDataKeepalive {
		t.Fatalf("policyFeatures() = %b, want only the keepalive bit %b", got, FeatureResponsesDataKeepalive)
	}
	for _, state := range []ProtectionSwitch{ProtectionsOn, ProtectionsOff} {
		clients, err := newRecognizedClients(BuiltInCatalog(), state, policies)
		if err != nil {
			t.Fatal(err)
		}
		got, found := clients.Client(BuiltInCatalog().Revision(), "codex")
		if !found || got.Policy != FeatureResponsesDataKeepalive ||
			got.Policy&^knownFeatures != 0 || got.Protections&^knownFeatures != 0 {
			t.Fatalf("switch %d: Client(codex) = %+v, %v", state, got, found)
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
	// The live table: the switch withdraws Claude Code's fallback and
	// workspace tools and Codex's keepalive, and keeps each policy.
	live, err := NewRecognizedClients(catalog, ProtectionsOff)
	if err != nil {
		t.Fatal(err)
	}
	for label, want := range map[string]RecognizedClient{
		"claude": {ID: "claude-code", Policy: claude},
		"codex":  {ID: "codex-cli", Policy: FeatureResponsesDataKeepalive},
	} {
		if got, found := live.Client(catalog.Revision(), label); !found || got != want || !got.SwitchedOff() {
			t.Fatalf("live Client(%q) with the switch off = %+v, %v, want %+v switched off", label, got, found, want)
		}
	}
	// A client the policy grants nothing is not switched off, because there
	// was nothing to withdraw.
	empty, err := newRecognizedClients(catalog, ProtectionsOff, []RecognizedClientPolicy{
		{ClientID: "claude-code"}, {ClientID: "codex-cli"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, found := empty.Client(catalog.Revision(), "claude"); !found ||
		got != (RecognizedClient{ID: "claude-code"}) || got.SwitchedOff() {
		t.Fatalf("an empty policy with the switch off = %+v, %v", got, found)
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

// canaryWorkflowPath is the daily canary that re-proves every policy entry.
var canaryWorkflowPath = filepath.Join(".github", "workflows", "client-canary.yml")

// TestEveryProtectionNamesItsDailyCanary is the static guard of ADR 0026: a
// policy entry must name a test that exists, that the daily canary selects
// and judges on both operating systems, and that both compile. A -run pattern
// that matches nothing still passes, and a darwin-only test file never
// compiles on the ubuntu canary.
func TestEveryProtectionNamesItsDailyCanary(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	encoded, err := os.ReadFile(filepath.Join(root, canaryWorkflowPath))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(encoded)
	if problems := dailyCanaryProblems(RecognizedClientPolicies(), root, workflow); len(problems) != 0 {
		t.Fatalf("the policy and the daily canary disagree:\n%s", strings.Join(problems, "\n"))
	}
	if problems := dailyCanaryProblems(designedRecognizedClientPolicies(), root, workflow); len(problems) != 0 {
		t.Fatalf("the designed policy and the daily canary disagree:\n%s", strings.Join(problems, "\n"))
	}

	// Each rule must be able to fail.
	fixtureRoot := t.TempDir()
	fixtureDirectory := filepath.Join(fixtureRoot, "internal", "fixture")
	if err := os.MkdirAll(fixtureDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{
		"plain_test.go": "package fixture\n\nimport \"testing\"\n\n" +
			"type suite struct{}\n\n" +
			"func (suite) TestInstalledClaudeMethod(t *testing.T) {}\n\n" +
			"func TestInstalledClaudePlain(t *testing.T) {}\n",
		"tagged_test.go": "// A comment before the constraint.\n\n//go:build linux || darwin\n\n" +
			"package fixture\n\nimport \"testing\"\n\nfunc TestInstalledClaudeTagged(t *testing.T) {}\n",
		"platform_darwin_test.go": "package fixture\n\nimport \"testing\"\n\n" +
			"func TestInstalledClaudeDarwinOnly(t *testing.T) {}\n",
		"helper.go": "package fixture\n\nimport \"testing\"\n\n" +
			"func TestInstalledClaudeOutsideATestFile(t *testing.T) {}\n",
	} {
		if err := os.WriteFile(filepath.Join(fixtureDirectory, name), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(fixtureRoot, canaryWorkflowPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	// The fixture's canary names its package in the Claude jobs, so a fixture
	// case fails only on the rule it is about.
	fixtureWorkflow := strings.ReplaceAll(workflow, "./internal/runlauncher", "./internal/runlauncher ./internal/fixture")
	if fixtureWorkflow == workflow {
		t.Fatal("the canary workflow names no ./internal/runlauncher")
	}
	fixtureCanary := func(test string) func([]RecognizedClientPolicy) []RecognizedClientPolicy {
		return func(policies []RecognizedClientPolicy) []RecognizedClientPolicy {
			policies[0].Grants[0].Canary = Canary{"internal/fixture", test}
			return policies
		}
	}
	// The fixture root holds no other package, so only problems about the
	// fixture count.
	for _, problem := range dailyCanaryProblems(
		fixtureCanary("TestInstalledClaudePlain")(RecognizedClientPolicies()), fixtureRoot, fixtureWorkflow,
	) {
		if strings.Contains(problem, "internal/fixture") || strings.Contains(problem, "TestInstalledClaudePlain") {
			t.Fatalf("the fixture's plain test was refused: %s", problem)
		}
	}
	replace := func(old, replacement string) func(string) string {
		return func(text string) string {
			if !strings.Contains(text, old) {
				t.Fatalf("the canary workflow no longer holds %q", old)
			}
			return strings.ReplaceAll(text, old, replacement)
		}
	}
	unchanged := func(policies []RecognizedClientPolicy) []RecognizedClientPolicy { return policies }
	for _, testCase := range []struct {
		name     string
		policies func([]RecognizedClientPolicy) []RecognizedClientPolicy
		workflow func(string) string
		fixture  bool
		want     string
	}{
		{
			name: "a canary that names no function",
			policies: func(policies []RecognizedClientPolicy) []RecognizedClientPolicy {
				policies[0].Grants[1].Canary.Test = "TestInstalledClaudeDoesNotExist"
				return policies
			},
			want: "no test function TestInstalledClaudeDoesNotExist",
		},
		{
			name: "a canary in the wrong package",
			policies: func(policies []RecognizedClientPolicy) []RecognizedClientPolicy {
				policies[1].Grants[0].Canary.Package = "internal/runlauncher"
				return policies
			},
			want: "no test function TestInstalledCodexAcceptsTheDataKeepalive in internal/runlauncher",
		},
		{
			name: "a canary in a package that does not exist",
			policies: func(policies []RecognizedClientPolicy) []RecognizedClientPolicy {
				policies[1].IdentityCanary.Package = "internal/absent"
				return policies
			},
			want: "no test function TestInstalledCodexIsRecognizedByItsSigner in internal/absent",
		},
		{
			name: "a canary with another client's prefix",
			policies: func(policies []RecognizedClientPolicy) []RecognizedClientPolicy {
				policies[0].Grants[0].Canary = Canary{"internal/loopbackproxy", "TestInstalledCodexAcceptsTheDataKeepalive"}
				return policies
			},
			want: "lacks the prefix TestInstalledClaude",
		},
		{
			name:     "a canary that is a method",
			policies: fixtureCanary("TestInstalledClaudeMethod"),
			fixture:  true,
			want:     "no test function TestInstalledClaudeMethod",
		},
		{
			name:     "a canary outside a test file",
			policies: fixtureCanary("TestInstalledClaudeOutsideATestFile"),
			fixture:  true,
			want:     "no test function TestInstalledClaudeOutsideATestFile",
		},
		{
			name:     "a canary in a file with a build constraint",
			policies: fixtureCanary("TestInstalledClaudeTagged"),
			fixture:  true,
			want:     "tagged_test.go has a build constraint",
		},
		{
			name:     "a canary in a file named for one system",
			policies: fixtureCanary("TestInstalledClaudeDarwinOnly"),
			fixture:  true,
			want:     "platform_darwin_test.go does not build on linux/amd64",
		},
		{
			name:     "no job verifies a client on an operating system",
			workflow: replace("verify --client claude-code --os linux", "verify --client claude-code --os linux-latest"),
			want:     "no job of the daily canary runs vibermate-canary-proof verify --client claude-code --os linux",
		},
		{
			name: "swapped operating systems",
			workflow: func(text string) string {
				text = replace("verify --client codex-cli --os linux", "verify --client codex-cli --os SWAP")(text)
				text = replace("verify --client codex-cli --os darwin", "verify --client codex-cli --os linux")(text)
				return replace("verify --client codex-cli --os SWAP", "verify --client codex-cli --os darwin")(text)
			},
			want: "verifies codex-cli on darwin but runs on ubuntu-latest",
		},
		{
			name: "two jobs verify one client on one system",
			workflow: replace("verify --client claude-code --os darwin",
				"verify --client claude-code --os linux"),
			want: "2 jobs of the daily canary run vibermate-canary-proof verify --client claude-code --os linux",
		},
		{
			name:     "a job that does not name a canary's package",
			workflow: replace("./internal/runlauncher ./internal/clientadapter", "./internal/runlauncher"),
			want:     "does not name ./internal/clientadapter",
		},
		{
			name:     "a job that names a package only as a prefix",
			workflow: replace("./internal/loopbackproxy ./internal/clientadapter", "./internal/loopbackproxyx ./internal/clientadapter"),
			want:     "does not name ./internal/loopbackproxy",
		},
		{
			name:     "a job that does not select a canary",
			workflow: replace("-run '^TestInstalledClaude'", "-run '^TestInstalledClaudeCompletes'"),
			want:     "does not select TestInstalledClaudeLeavesStreamFallbackToCore",
		},
		{
			name:     "a job that selects without the proof checker's pattern",
			workflow: replace("tests --client codex-cli --os darwin", "tests --client claude-code --os darwin"),
			want:     "does not select TestInstalledCodexAcceptsTheDataKeepalive",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			root, text, policies := root, workflow, RecognizedClientPolicies()
			if testCase.fixture {
				root, text = fixtureRoot, fixtureWorkflow
			}
			if testCase.policies == nil {
				testCase.policies = unchanged
			}
			if testCase.workflow != nil {
				text = testCase.workflow(text)
			}
			problems := dailyCanaryProblems(testCase.policies(policies), root, text)
			for _, problem := range problems {
				if strings.Contains(problem, testCase.want) {
					return
				}
			}
			t.Fatalf("%s: no problem says %q; got %v", testCase.name, testCase.want, problems)
		})
	}
}

// dailyCanaryProblems checks every policy entry against the test sources
// under root and the text of the canary workflow.
func dailyCanaryProblems(policies []RecognizedClientPolicy, root, workflow string) []string {
	var problems []string
	jobs := canaryWorkflowJobs(workflow)
	checked := map[Canary]bool{}
	for _, policy := range policies {
		prefix := installedTestPrefixes[policy.ClientID]
		entries := []Canary{policy.IdentityCanary}
		for _, grant := range append(append([]ProtectionGrant(nil), policy.Grants...), policy.Unconditional...) {
			entries = append(entries, grant.Canary)
		}
		for _, canary := range entries {
			if canary == (Canary{}) {
				continue
			}
			if prefix == "" || !strings.HasPrefix(canary.Test, prefix) {
				problems = append(problems, fmt.Sprintf(
					"%s: canary %s lacks the prefix %s the canary selects",
					policy.ClientID, canary.Test, prefix,
				))
			}
			if !checked[canary] {
				checked[canary] = true
				problems = append(problems, canaryTestProblems(root, canary)...)
			}
		}
		for _, goos := range []string{"linux", "darwin"} {
			problems = append(problems, canaryJobProblems(
				jobs, policy.ClientID, goos, prefix,
				protectionCanaries(policies, policy.ClientID, goos),
			)...)
		}
	}
	return problems
}

// canaryTestProblems requires the canary's function in a _test.go file of its
// package, in a file that builds on both systems the canary runs.
func canaryTestProblems(root string, canary Canary) []string {
	directory := filepath.Join(root, filepath.FromSlash(canary.Package))
	paths, err := filepath.Glob(filepath.Join(directory, "*_test.go"))
	if err != nil {
		return []string{err.Error()}
	}
	for _, path := range paths {
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return []string{fmt.Sprintf("%s: %v", path, err)}
		}
		declared := false
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Recv == nil && function.Name.Name == canary.Test {
				declared = true
			}
		}
		if !declared {
			continue
		}
		var problems []string
		name := filepath.Base(path)
		for _, group := range file.Comments {
			for _, comment := range group.List {
				if comment.Pos() < file.Package &&
					(constraint.IsGoBuild(comment.Text) || constraint.IsPlusBuild(comment.Text)) {
					problems = append(problems, fmt.Sprintf(
						"%s: %s has a build constraint, so one canary may never compile %s",
						canary.Package, name, canary.Test,
					))
				}
			}
		}
		for _, target := range [][2]string{{"linux", "amd64"}, {"darwin", "arm64"}} {
			context := build.Default
			context.GOOS, context.GOARCH = target[0], target[1]
			if matched, err := context.MatchFile(directory, name); err != nil || !matched {
				problems = append(problems, fmt.Sprintf(
					"%s: %s does not build on %s/%s, so that canary never compiles %s",
					canary.Package, name, target[0], target[1], canary.Test,
				))
			}
		}
		return problems
	}
	return []string{fmt.Sprintf("no test function %s in %s", canary.Test, canary.Package)}
}

// canaryWorkflowJob is one job of the canary workflow, its text with line
// continuations joined and white space collapsed.
type canaryWorkflowJob struct {
	name   string
	runsOn string
	text   string
}

var (
	canaryJobKey    = regexp.MustCompile(`^  ([A-Za-z_][A-Za-z0-9_-]*):\s*$`)
	canaryJobRunsOn = regexp.MustCompile(`^    runs-on:\s*(\S+)\s*$`)
	canaryRunPrefix = regexp.MustCompile(`-run ["']\^([A-Za-z0-9_]+)["']`)
)

func canaryWorkflowJobs(workflow string) []canaryWorkflowJob {
	var jobs []canaryWorkflowJob
	var current *canaryWorkflowJob
	var body []string
	flush := func() {
		if current != nil {
			joined := strings.ReplaceAll(strings.Join(body, "\n"), "\\\n", " ")
			current.text = " " + strings.Join(strings.Fields(joined), " ") + " "
			jobs = append(jobs, *current)
		}
		current, body = nil, nil
	}
	inJobs := false
	for _, line := range strings.Split(workflow, "\n") {
		if line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "#") {
			flush()
			inJobs = strings.TrimSpace(line) == "jobs:"
			continue
		}
		if !inJobs {
			continue
		}
		if match := canaryJobKey.FindStringSubmatch(line); match != nil {
			flush()
			current = &canaryWorkflowJob{name: match[1]}
			continue
		}
		if current == nil {
			continue
		}
		body = append(body, line)
		if match := canaryJobRunsOn.FindStringSubmatch(line); match != nil && current.runsOn == "" {
			current.runsOn = match[1]
		}
	}
	flush()
	return jobs
}

// canaryJobProblems requires exactly one job that judges clientID on goos, on
// a runner of that system, that selects every required canary and names its
// package.
func canaryJobProblems(
	jobs []canaryWorkflowJob,
	clientID, goos, prefix string,
	canaries []Canary,
) []string {
	if len(canaries) == 0 {
		return nil
	}
	verify := "vibermate-canary-proof verify --client " + clientID + " --os " + goos
	var matching []canaryWorkflowJob
	for _, job := range jobs {
		if strings.Contains(job.text, "/"+verify+" ") {
			matching = append(matching, job)
		}
	}
	switch len(matching) {
	case 0:
		return []string{fmt.Sprintf("no job of the daily canary runs %s", verify)}
	case 1:
	default:
		return []string{fmt.Sprintf("%d jobs of the daily canary run %s", len(matching), verify)}
	}
	job := matching[0]
	var problems []string
	runner := map[string]string{"linux": "ubuntu-", "darwin": "macos-"}[goos]
	if !strings.HasPrefix(job.runsOn, runner) {
		problems = append(problems, fmt.Sprintf(
			"job %s verifies %s on %s but runs on %s", job.name, clientID, goos, job.runsOn,
		))
	}
	byChecker := strings.Contains(job.text,
		"/vibermate-canary-proof tests --client "+clientID+" --os "+goos+")")
	var prefixes []string
	for _, match := range canaryRunPrefix.FindAllStringSubmatch(job.text, -1) {
		prefixes = append(prefixes, match[1])
	}
	for _, canary := range canaries {
		selected := byChecker
		for _, runPrefix := range prefixes {
			if strings.HasPrefix(canary.Test, runPrefix) && strings.HasPrefix(runPrefix, prefix) {
				selected = true
			}
		}
		if !selected {
			problems = append(problems, fmt.Sprintf(
				"job %s does not select %s", job.name, canary.Test,
			))
		}
		named := regexp.MustCompile(`[\s"'(]` + regexp.QuoteMeta("./"+canary.Package) + `[\s"')]`)
		if !named.MatchString(job.text) {
			problems = append(problems, fmt.Sprintf(
				"job %s does not name ./%s, where %s lives", job.name, canary.Package, canary.Test,
			))
		}
	}
	return problems
}
