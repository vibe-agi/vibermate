package clientadapter

import (
	"errors"
	"strings"
)

// This file is the only place that says what a client recognized by its
// publisher's signature receives (ADR 0026). It is code, not catalog data: it
// does not enter Catalog.Digest, so the catalog revision, companion lockstep
// and acceptance reports are unaffected by any change here.

// ProtectionProof names the capability that makes a grant hold for every build
// of a client without knowing which build it is (ADR 0026).
type ProtectionProof string

const (
	// The client honours a launch instruction; a build that does not know it
	// ignores it and behaves as it did without the protection.
	ProofLaunchInstruction ProtectionProof = "launch_instruction"
	// Each request's own definitions and each call's arguments must match what
	// the classifier understands; a build that differs is asked or refused.
	ProofRequestDefinitions ProtectionProof = "request_definitions"
	// A fixed, content-free frame every Codex already receives on another path;
	// a build that does not renew on it idles exactly as on a comment.
	ProofStreamFrame ProtectionProof = "stream_frame"
	// Decided by the operation catalog for every client; only the client half
	// is re-proven.
	ProofOperationCatalog ProtectionProof = "operation_catalog"
)

// Canary names the installed-client test that re-proves one entry against the
// newest published release every day.
type Canary struct {
	Package string // repository-relative, for example "internal/runlauncher"
	Test    string // a TestInstalledClaude* or TestInstalledCodex* function
}

// ProtectionGrant is one protection a policy row grants, with the capability
// that proves it and the daily test that re-proves it.
type ProtectionGrant struct {
	Feature Feature
	Proof   ProtectionProof
	Canary  Canary
}

// RecognizedClientPolicy is keyed by the catalogued signer client ID, never by
// anything a client reports about itself.
type RecognizedClientPolicy struct {
	ClientID string
	// IdentityCanary proves on darwin/arm64 that the newest release is still
	// recognized by its publisher's signature.
	IdentityCanary Canary
	Grants         []ProtectionGrant
	// Unconditional protections need no grant; their client half is still
	// re-proven daily.
	Unconditional []ProtectionGrant
}

// recognizedClientPolicies names both publisher-recognized clients. Each
// grant is added only after the installed-client proof that re-proves it has
// passed against a real release; Claude Code's workspace tools are not
// granted yet, so the tool gate's recognized basis stays inactive.
var recognizedClientPolicies = [...]RecognizedClientPolicy{
	{
		ClientID:       "claude-code",
		IdentityCanary: Canary{"internal/clientadapter", "TestInstalledClaudeIsRecognizedByItsSigner"},
		Grants: []ProtectionGrant{
			{FeatureCoreOwnedStreamingFallback, ProofLaunchInstruction,
				Canary{"internal/runlauncher", "TestInstalledClaudeLeavesStreamFallbackToCore"}},
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

// authorityBearingFeatures change what a client may do without a question or
// how it behaves, so they go only to a client whose every catalogued signer
// signs the launched file itself (native_single_binary). The Codex wrapper
// shape signs a native child, not the codex.js that is launched.
const authorityBearingFeatures = FeatureCoreOwnedStreamingFallback |
	FeatureStructuredWorkspaceTools

// RecognizedClientPolicies returns a deep copy of the policy table, for tests,
// the proof checker and the canary-name guard.
func RecognizedClientPolicies() []RecognizedClientPolicy {
	return cloneRecognizedClientPolicies(recognizedClientPolicies[:])
}

func cloneRecognizedClientPolicies(
	policies []RecognizedClientPolicy,
) []RecognizedClientPolicy {
	cloned := make([]RecognizedClientPolicy, len(policies))
	for index, policy := range policies {
		cloned[index] = RecognizedClientPolicy{
			ClientID:       policy.ClientID,
			IdentityCanary: policy.IdentityCanary,
			Grants:         cloneProtectionGrants(policy.Grants),
			Unconditional:  cloneProtectionGrants(policy.Unconditional),
		}
	}
	return cloned
}

func cloneProtectionGrants(grants []ProtectionGrant) []ProtectionGrant {
	if grants == nil {
		return nil
	}
	cloned := make([]ProtectionGrant, len(grants))
	copy(cloned, grants)
	return cloned
}

// RecognizedPolicyFeatures is the union of the Grants the policy gives
// clientID, and 0 for any client the policy does not name.
func RecognizedPolicyFeatures(clientID string) Feature {
	return policyFeatures(recognizedClientPolicies[:], clientID)
}

func policyFeatures(policies []RecognizedClientPolicy, clientID string) Feature {
	var features Feature
	if clientID == "" {
		return 0
	}
	for _, policy := range policies {
		if policy.ClientID != clientID {
			continue
		}
		for _, grant := range policy.Grants {
			features |= grant.Feature
		}
	}
	return features & knownFeatures
}

// RecognizedStreamingFallbackPolicy is the launch-time projection of the
// policy for a recognized grant. It is core_owned only when the policy grants
// clientID the streaming fallback, the recipe is node_env_proxy and the signed
// file is the launched file; this mirrors the wire rule that core_owned exists
// only for claude-code with node_env_proxy.
func RecognizedStreamingFallbackPolicy(
	clientID string,
	recipe LaunchRecipe,
	shape InstallShape,
) StreamingFallbackPolicy {
	return streamingFallbackPolicyFor(
		recognizedClientPolicies[:],
		clientID,
		recipe,
		shape,
	)
}

func streamingFallbackPolicyFor(
	policies []RecognizedClientPolicy,
	clientID string,
	recipe LaunchRecipe,
	shape InstallShape,
) StreamingFallbackPolicy {
	if recipe == LaunchNodeEnvProxy &&
		shape == InstallNativeSingleBinary &&
		policyFeatures(policies, clientID).Includes(
			FeatureCoreOwnedStreamingFallback,
		) {
		return StreamingFallbackCoreOwned
	}
	return StreamingFallbackClientDefault
}

// ProtectionCanaries lists the installed-client tests that must pass for
// clientID on goos: every Grants and Unconditional canary, plus the identity
// canary on darwin, where the recognized tier exists. An entry that names no
// test is skipped.
func ProtectionCanaries(clientID, goos string) []Canary {
	return protectionCanaries(recognizedClientPolicies[:], clientID, goos)
}

func protectionCanaries(
	policies []RecognizedClientPolicy,
	clientID string,
	goos string,
) []Canary {
	var canaries []Canary
	if clientID == "" {
		return canaries
	}
	for _, policy := range policies {
		if policy.ClientID != clientID {
			continue
		}
		for _, grant := range policy.Grants {
			if grant.Canary != (Canary{}) {
				canaries = append(canaries, grant.Canary)
			}
		}
		for _, grant := range policy.Unconditional {
			if grant.Canary != (Canary{}) {
				canaries = append(canaries, grant.Canary)
			}
		}
		if goos == "darwin" && policy.IdentityCanary != (Canary{}) {
			canaries = append(canaries, policy.IdentityCanary)
		}
	}
	return canaries
}

// Includes reports whether every bit of other is a known Feature carried by
// features. A zero or unknown query is never included; this is the meaning
// Evidence.Supports gives a query.
func (features Feature) Includes(other Feature) bool {
	return other != 0 &&
		other&^knownFeatures == 0 &&
		features&other == other
}

// Single reports whether features is exactly one known bit.
func (features Feature) Single() bool {
	return features != 0 &&
		features&^knownFeatures == 0 &&
		features&(features-1) == 0
}

// Name is a stable snake_case name for a single known bit, and empty for
// anything else.
func (features Feature) Name() string {
	switch features {
	case FeatureResponsesWebSocketHTTPFallback:
		return "responses_websocket_http_fallback"
	case FeatureCoreOwnedStreamingFallback:
		return "core_owned_streaming_fallback"
	case FeatureStructuredWorkspaceTools:
		return "structured_workspace_tools"
	case FeatureResponsesDataKeepalive:
		return "responses_data_keepalive"
	default:
		return ""
	}
}

// ProtectionBasis says on what ground one connection may use one protection.
type ProtectionBasis uint8

const (
	ProtectionNone ProtectionBasis = iota
	// The run's digest-verified release carries the Feature.
	ProtectionExactRelease
	// The Runtime recognized the run's client by its publisher's signature and
	// the recognized-client policy grants the Feature.
	ProtectionRecognizedClient
)

// String is the stable wire name of a valid basis, and empty otherwise.
func (basis ProtectionBasis) String() string {
	switch basis {
	case ProtectionNone:
		return "none"
	case ProtectionExactRelease:
		return "exact_release"
	case ProtectionRecognizedClient:
		return "recognized_client"
	default:
		return ""
	}
}

func (basis ProtectionBasis) Valid() bool {
	return basis <= ProtectionRecognizedClient
}

// RecognizedClient names the client a publisher-recognized run is, as this
// Runtime's own catalog established it, and what it receives now.
type RecognizedClient struct {
	ID          string  // catalogued signer client ID; empty: the run names no client
	Policy      Feature // the policy's grants for ID under this catalog
	Protections Feature // Policy, or 0 when the operator switch is off
}

// SwitchedOff reports that the operator switch withdrew what the policy would
// otherwise grant this client.
func (client RecognizedClient) SwitchedOff() bool {
	return client.ID != "" && client.Policy != 0 && client.Protections == 0
}

// RecognizedClients maps a stored (catalog revision, executable label) to the
// one client recognition evaluated for that label. It is built once at
// start-up from the host's catalog; the zero value names no client.
type RecognizedClients struct {
	revision CatalogRevision
	labels   map[string]RecognizedClient
}

// NewRecognizedClients indexes every signer of every platform in catalog: a
// Server stores runs that darwin companions recognized. The label, not the
// platform, is what recognition was evaluated against.
func NewRecognizedClients(
	catalog Catalog,
	state ProtectionSwitch,
) (RecognizedClients, error) {
	return newRecognizedClients(catalog, state, recognizedClientPolicies[:])
}

func newRecognizedClients(
	catalog Catalog,
	state ProtectionSwitch,
	policies []RecognizedClientPolicy,
) (RecognizedClients, error) {
	if state != ProtectionsOn && state != ProtectionsOff {
		return RecognizedClients{}, errors.New(
			"recognized-client protection switch is invalid",
		)
	}
	// Normalize exactly as ValidateCompanionAttestation does, so the index
	// and the companion check read the same catalog.
	normalized, err := NewCatalogWithSigners(
		catalog.revision,
		catalog.releases,
		catalog.signers,
	)
	if err != nil {
		return RecognizedClients{}, err
	}
	labels := make(map[string]RecognizedClient, len(normalized.signers))
	launchedFileUnsigned := make(map[string]bool, len(normalized.signers))
	for _, signer := range normalized.signers {
		existing, seen := labels[signer.InvocationLabel]
		if seen && existing.ID != signer.ID {
			return RecognizedClients{}, errors.New(
				"client signer catalog names two clients under one invocation label",
			)
		}
		labels[signer.InvocationLabel] = RecognizedClient{ID: signer.ID}
		if signer.InstallShape != InstallNativeSingleBinary {
			launchedFileUnsigned[signer.InvocationLabel] = true
		}
	}
	for label, client := range labels {
		client.Policy = policyFeatures(policies, client.ID)
		if launchedFileUnsigned[label] {
			client.Policy &^= authorityBearingFeatures
		}
		if state == ProtectionsOn {
			client.Protections = client.Policy
		}
		labels[label] = client
	}
	return RecognizedClients{
		revision: normalized.revision,
		labels:   labels,
	}, nil
}

// Client answers only for the catalog revision the index was built from. A
// run stored under another revision, an unknown label or the zero index names
// no client.
func (clients RecognizedClients) Client(
	revision CatalogRevision,
	label string,
) (RecognizedClient, bool) {
	if !clients.revision.Valid() || revision != clients.revision {
		return RecognizedClient{}, false
	}
	client, found := clients.labels[label]
	return client, found
}

// RecognizedProtectionsVariable withdraws every recognized-basis protection
// when it is off. Exact releases are unaffected.
const RecognizedProtectionsVariable = "VIBERMATE_RECOGNIZED_CLIENT_PROTECTIONS"

// ProtectionSwitch is the operator's state for recognized-basis protections.
type ProtectionSwitch uint8

const (
	ProtectionsOn ProtectionSwitch = iota // zero value: the default
	ProtectionsOff
)

// ParseProtectionSwitch reads unset, empty, "on", "1", "true" and "yes" as on,
// and "off", "0", "false" and "no" as off, trimmed and in any case. Any other
// value keeps protections on and returns recognized=false so the caller can
// say so.
func ParseProtectionSwitch(
	value string,
) (state ProtectionSwitch, recognized bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "on", "1", "true", "yes":
		return ProtectionsOn, true
	case "off", "0", "false", "no":
		return ProtectionsOff, true
	default:
		return ProtectionsOn, false
	}
}

// ProtectionSwitchFromEnvironment reads the last assignment of the variable in
// an exact-case environment list, as the child would see it.
func ProtectionSwitchFromEnvironment(
	environment []string,
) (state ProtectionSwitch, value string, recognized bool) {
	for _, entry := range environment {
		key, entryValue, ok := strings.Cut(entry, "=")
		if ok && key == RecognizedProtectionsVariable {
			value = entryValue
		}
	}
	state, recognized = ParseProtectionSwitch(value)
	return state, value, recognized
}
