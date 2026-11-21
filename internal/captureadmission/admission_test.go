package captureadmission

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturecredential"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

type frozenRunAuthorizer struct {
	want       string
	evidence   capturerun.Evidence
	err        error
	authorized int
}

type frozenManualAuthorizer struct {
	want       string
	evidence   manualcapture.Evidence
	err        error
	authorized int
}

func (authorizer *frozenManualAuthorizer) AuthorizeProxy(
	_ context.Context,
	credential manualcapture.ProxyCredential,
) (manualcapture.Evidence, error) {
	authorizer.authorized++
	if credential.Value() != authorizer.want {
		return manualcapture.Evidence{}, manualcapture.ErrCredentialRejected
	}
	return authorizer.evidence, authorizer.err
}

func (authorizer *frozenRunAuthorizer) AuthorizeProxy(
	_ context.Context,
	capability capturerun.ProxyCapability,
) (capturerun.Evidence, error) {
	authorizer.authorized++
	if capability.Value() != authorizer.want {
		return capturerun.Evidence{}, capturerun.ErrCapabilityRejected
	}
	return authorizer.evidence, authorizer.err
}

func testCredentialValue(t *testing.T, kind capturecredential.Kind) string {
	t.Helper()
	credential, err := capturecredential.New(
		kind,
		[]byte(strings.Repeat("c", capturecredential.EntropyBytes)),
	)
	if err != nil {
		t.Fatal(err)
	}
	return credential.Value()
}

func testWorkspace(t *testing.T) workspaceidentity.Scope {
	t.Helper()
	machineValue := base64.RawURLEncoding.EncodeToString(
		[]byte(strings.Repeat("m", 32)),
	)
	workspaceValue := base64.RawURLEncoding.EncodeToString(
		[]byte(strings.Repeat("w", 32)),
	)
	machineID, err := workspaceidentity.ParseMachineID(machineValue)
	if err != nil {
		t.Fatal(err)
	}
	workspaceID, err := workspaceidentity.ParseWorkspaceID(workspaceValue)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := workspaceidentity.NewScope(
		machineID,
		workspaceID,
		"project",
		workspaceidentity.EvidenceLocalLauncher,
		1,
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func TestManagedRunAuthorizationProducesRouteNeutralAdmission(t *testing.T) {
	t.Parallel()
	value := testCredentialValue(t, capturecredential.KindManagedRun)
	runs := &frozenRunAuthorizer{
		want: value,
		evidence: capturerun.Evidence{
			RunID:         "run-one",
			RuntimeUserID: "user.AAAAAAAAAAAAAAAAAAAAAAAAAAA", RuntimeUsername: "member",
			ExecutableLabel: "claude",
			Workspace:       testWorkspace(t),
		},
	}
	manuals := &frozenManualAuthorizer{err: manualcapture.ErrCredentialRejected}
	authorizer, err := NewAuthorizer(runs, manuals)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := NewProxyCredential(value)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := authorizer.Authorize(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	if admission.Kind() != KindManagedRun ||
		admission.AdmissionRef() != "capture-run/run-one" ||
		admission.CredentialRevision() != 1 ||
		admission.AttributionConfidence() != AttributionConfigured ||
		admission.SourceLabel() != "claude" || runs.authorized != 1 ||
		manuals.authorized != 0 {
		t.Fatalf("admission = %#v, authorizations = %d", admission, runs.authorized)
	}
	if owner, ok := admission.RuntimeUserID(); !ok || owner != "user.AAAAAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("authorized evidence lost owner: %q %v", owner, ok)
	}
	if runID, ok := admission.CaptureRunID(); !ok || runID != "run-one" {
		t.Fatalf("CaptureRun identity = %q, %v", runID, ok)
	}
	if reference, referenceErr := admission.CaptureReference(); referenceErr != nil ||
		reference.Kind != captureidentity.KindManagedRun || reference.ID != "run-one" {
		t.Fatalf("Capture reference = %+v, %v", reference, referenceErr)
	}
	if _, ok := admission.ManualCaptureID(); ok {
		t.Fatal("managed admission asserted ManualCapture identity")
	}
	wantMachineID := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("m", 32)))
	if machineID, ok := admission.MachineID(); !ok || machineID.String() != wantMachineID {
		t.Fatalf("machine identity = %q, %v", machineID, ok)
	}
	wantWorkspaceID := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("w", 32)))
	if workspaceID, ok := admission.WorkspaceID(); !ok || workspaceID.String() != wantWorkspaceID {
		t.Fatalf("workspace identity = %q, %v", workspaceID, ok)
	}
	if scope, ok := admission.WorkspaceScope(); !ok || scope.Validate() != nil {
		t.Fatalf("workspace scope = %#v, %v", scope, ok)
	}
}

func TestVerifiedFeatureEvidenceIsFrozenInsideAdmission(t *testing.T) {
	t.Parallel()
	catalog := clientadapter.BuiltInCatalog()
	adapter, ok := catalog.ExpectedEvidence("codex-cli", "0.145.0")
	if !ok {
		t.Fatal("built-in Codex evidence is unavailable")
	}
	admission, err := NewManagedRun(ManagedRunEvidence{
		CaptureRunID: "run-verified",
		SourceLabel:  "codex",
		Adapter:      &adapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter.Features = 0
	if admission.AttributionConfidence() != AttributionVerified ||
		!admission.Supports(clientadapter.FeatureResponsesWebSocketHTTPFallback) {
		t.Fatalf("verified admission lost frozen adapter evidence: %#v", admission)
	}
}

func TestWorkspaceAuthoritySeparatesLocalEvidenceFromPathClaims(t *testing.T) {
	t.Parallel()
	local := testWorkspace(t)
	remote, err := workspaceidentity.NewScope(local.MachineID(), local.WorkspaceID(), "project",
		workspaceidentity.EvidenceRegisteredCompanion, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		scope workspaceidentity.Scope
		root  string
		want  string
	}{
		{"local", local, "/workspace/project", "/workspace/project"},
		{"remote", remote, "/workspace/project", ""},
		{"unscoped", workspaceidentity.Scope{}, "/workspace/project", ""},
		{"local-without-root", local, "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			admission, err := NewManagedRun(ManagedRunEvidence{
				CaptureRunID: "run-authority", SourceLabel: "claude",
				Workspace: test.scope, WorkspaceRoot: test.root,
			})
			if err != nil {
				t.Fatal(err)
			}
			if root, ok := admission.LocalWorkspaceRoot(); root != test.want || ok != (test.want != "") {
				t.Fatalf("local filesystem authority = %q, %v; want %q", root, ok, test.want)
			}
			if root, ok := admission.WorkspaceRoot(); root != test.root || ok != (test.root != "") {
				t.Fatalf("display/redaction path claim = %q, %v; want %q", root, ok, test.root)
			}
		})
	}
	manual, err := NewManual("manual-authority", 1, "Claude")
	if err != nil {
		t.Fatal(err)
	}
	if root, ok := manual.LocalWorkspaceRoot(); root != "" || ok {
		t.Fatalf("manual capture acquired filesystem authority = %q, %v", root, ok)
	}
}

func TestManualAdmissionCannotInventManagedEvidence(t *testing.T) {
	t.Parallel()
	admission, err := NewManual("manual-one", 3, "Codex App")
	if err != nil {
		t.Fatal(err)
	}
	if admission.Kind() != KindManual ||
		admission.AdmissionRef() != "manual-capture/manual-one" ||
		admission.CredentialRevision() != 3 ||
		admission.AttributionConfidence() != AttributionConfigured {
		t.Fatalf("manual admission = %#v", admission)
	}
	if _, ok := admission.CaptureRunID(); ok {
		t.Fatal("manual admission asserted CaptureRun identity")
	}
	if id, ok := admission.ManualCaptureID(); !ok || id != "manual-one" {
		t.Fatalf("ManualCapture identity = %q, %v", id, ok)
	}
	if _, ok := admission.MachineID(); ok {
		t.Fatal("manual admission asserted machine identity")
	}
	if _, ok := admission.WorkspaceScope(); ok {
		t.Fatal("manual admission asserted workspace identity")
	}
	if admission.Supports(clientadapter.FeatureResponsesWebSocketHTTPFallback) {
		t.Fatal("manual admission acquired client adapter capability")
	}
}

func TestCredentialAndMalformedEvidenceFailClosed(t *testing.T) {
	t.Parallel()
	if _, err := NewProxyCredential("not-a-capability"); !errors.Is(err, ErrCredentialRejected) {
		t.Fatalf("malformed credential error = %v", err)
	}
	value := testCredentialValue(t, capturecredential.KindManagedRun)
	credential, err := NewProxyCredential(value)
	if err != nil {
		t.Fatal(err)
	}
	if rendered := fmt.Sprintf("%#v %v", credential, credential); strings.Contains(rendered, value) {
		t.Fatalf("formatted credential disclosed bearer value: %q", rendered)
	}
	authorizer, err := NewAuthorizer(&frozenRunAuthorizer{
		want: value,
		evidence: capturerun.Evidence{
			RunID: "run-invalid",
		},
	}, &frozenManualAuthorizer{err: manualcapture.ErrCredentialRejected})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.Authorize(context.Background(), credential); !errors.Is(err, ErrInvalidAdmission) {
		t.Fatalf("incomplete run evidence error = %v", err)
	}
}

func TestManagedRunAuthorizerPreservesCredentialRejection(t *testing.T) {
	t.Parallel()
	value := testCredentialValue(t, capturecredential.KindManagedRun)
	credential, _ := NewProxyCredential(value)
	authorizer, err := NewAuthorizer(&frozenRunAuthorizer{
		want: value,
		err:  capturerun.ErrCapabilityRejected,
	}, &frozenManualAuthorizer{err: manualcapture.ErrCredentialRejected})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.Authorize(context.Background(), credential); !errors.Is(err, ErrCredentialRejected) ||
		!errors.Is(err, capturerun.ErrCapabilityRejected) {
		t.Fatalf("authorization error = %v", err)
	}
}

func TestManualCredentialSelectsOnlyManualAuthority(t *testing.T) {
	t.Parallel()
	value := testCredentialValue(t, capturecredential.KindManualCapture)
	id, err := manualcapture.ParseID("manual-one")
	if err != nil {
		t.Fatal(err)
	}
	runs := &frozenRunAuthorizer{err: capturerun.ErrCapabilityRejected}
	manuals := &frozenManualAuthorizer{
		want: value,
		evidence: manualcapture.Evidence{
			ManualCaptureID:    id,
			CredentialRevision: 4,
			DisplayName:        "Desktop app",
			Owner:              manualcapture.NewLocalOwnerScope(),
		},
	}
	authorizer, err := NewAuthorizer(runs, manuals)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := NewProxyCredential(value)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := authorizer.Authorize(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	if admission.Kind() != KindManual ||
		admission.AdmissionRef() != "manual-capture/manual-one" ||
		admission.CredentialRevision() != 4 ||
		admission.SourceLabel() != "Desktop app" ||
		manuals.authorized != 1 || runs.authorized != 0 {
		t.Fatalf(
			"admission=%#v run calls=%d manual calls=%d",
			admission,
			runs.authorized,
			manuals.authorized,
		)
	}
}

func TestManagedAdmissionPreservesTrustedRuntimeUser(t *testing.T) {
	admission, err := NewManagedRun(ManagedRunEvidence{CaptureRunID: "run-owned", SourceLabel: "client", RuntimeUserID: "user.AAAAAAAAAAAAAAAAAAAAAAAAAAA", RuntimeUsername: "member"})
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := admission.RuntimeUserID(); !ok || id != "user.AAAAAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("trusted owner dropped: %q %v", id, ok)
	}
}

var everyFeature = []clientadapter.Feature{
	clientadapter.FeatureResponsesWebSocketHTTPFallback,
	clientadapter.FeatureCoreOwnedStreamingFallback,
	clientadapter.FeatureStructuredWorkspaceTools,
	clientadapter.FeatureResponsesDataKeepalive,
}

// The recognized basis answers only for bits the recognized-client policy
// grants and the operator switch leaves on. It never changes Supports, the
// confidence or the exact path, and a verified run never falls through to it.
func TestRecognizedAdmissionMatrix(t *testing.T) {
	t.Parallel()

	tools := clientadapter.FeatureStructuredWorkspaceTools
	fallback := clientadapter.FeatureCoreOwnedStreamingFallback
	keepalive := clientadapter.FeatureResponsesDataKeepalive
	claudePolicy := tools | fallback
	for _, testCase := range []struct {
		name       string
		label      string
		client     clientadapter.RecognizedClient
		recognized clientadapter.Feature
	}{
		{"claude granted", "claude",
			clientadapter.RecognizedClient{ID: "claude-code", Policy: claudePolicy, Protections: claudePolicy}, claudePolicy},
		{"claude switched off", "claude",
			clientadapter.RecognizedClient{ID: "claude-code", Policy: claudePolicy}, 0},
		{"claude under the empty policy", "claude",
			clientadapter.RecognizedClient{ID: "claude-code"}, 0},
		{"codex granted", "codex",
			clientadapter.RecognizedClient{ID: "codex-cli", Policy: keepalive, Protections: keepalive}, keepalive},
		{"codex switched off", "codex",
			clientadapter.RecognizedClient{ID: "codex-cli", Policy: keepalive}, 0},
		{"no client named", "claude", clientadapter.RecognizedClient{}, 0},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			admission, err := NewManagedRun(ManagedRunEvidence{
				CaptureRunID:     "run-recognized",
				SourceLabel:      testCase.label,
				Workspace:        testWorkspace(t),
				WorkspaceRoot:    "/workspace/project",
				RecognizedClient: testCase.client,
			})
			if err != nil {
				t.Fatal(err)
			}
			if admission.AttributionConfidence() != AttributionConfigured {
				t.Fatalf("confidence = %q", admission.AttributionConfidence())
			}
			for _, feature := range everyFeature {
				if admission.Supports(feature) {
					t.Fatalf("Supports(%s) is true on a recognized run", feature.Name())
				}
				want := clientadapter.ProtectionNone
				if testCase.recognized&feature != 0 {
					want = clientadapter.ProtectionRecognizedClient
				}
				if got := admission.ProtectionBasis(feature); got != want {
					t.Fatalf("ProtectionBasis(%s) = %s, want %s", feature.Name(), got, want)
				}
			}
			for _, query := range []clientadapter.Feature{0, tools | fallback, 1 << 40} {
				if got := admission.ProtectionBasis(query); got != clientadapter.ProtectionNone {
					t.Fatalf("ProtectionBasis(%b) = %s", query, got)
				}
			}
		})
	}

	catalog := clientadapter.BuiltInCatalog()
	for _, release := range []struct {
		id, version, goos, arch string
	}{
		{"codex-cli", "0.145.0", "linux", "amd64"},
		{"codex-cli", "0.159.2", "darwin", "arm64"},
		{"claude-code", "2.1.220", "darwin", "arm64"},
	} {
		adapter, ok := catalog.ExpectedEvidenceForPlatform(release.id, release.version, release.goos, release.arch)
		if !ok {
			t.Fatalf("%s %s evidence is unavailable", release.id, release.version)
		}
		admission, err := NewManagedRun(ManagedRunEvidence{
			CaptureRunID: "run-verified",
			SourceLabel:  "client",
			Adapter:      &adapter,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, feature := range everyFeature {
			want := clientadapter.ProtectionNone
			if adapter.Supports(feature) {
				want = clientadapter.ProtectionExactRelease
			}
			if got := admission.ProtectionBasis(feature); got != want {
				t.Fatalf("%s %s: ProtectionBasis(%s) = %s, want %s",
					release.id, release.version, feature.Name(), got, want)
			}
			if admission.Supports(feature) != adapter.Supports(feature) {
				t.Fatalf("%s %s: Supports(%s) changed", release.id, release.version, feature.Name())
			}
		}
	}
	// 0.145.0 carries only the WebSocket bit, so it gains no tools.
	adapter, _ := catalog.ExpectedEvidenceForPlatform("codex-cli", "0.145.0", "linux", "amd64")
	admission, err := NewManagedRun(ManagedRunEvidence{
		CaptureRunID: "run-verified", SourceLabel: "codex", Adapter: &adapter,
		Workspace: testWorkspace(t), WorkspaceRoot: "/workspace/project",
	})
	if err != nil {
		t.Fatal(err)
	}
	if admission.ProtectionBasis(tools) != clientadapter.ProtectionNone {
		t.Fatal("an exact release without the tools bit gained tools")
	}

	manual, err := NewManual("manual-recognized", 1, "Claude")
	if err != nil {
		t.Fatal(err)
	}
	for _, feature := range everyFeature {
		if got := manual.ProtectionBasis(feature); got != clientadapter.ProtectionNone {
			t.Fatalf("manual ProtectionBasis(%s) = %s", feature.Name(), got)
		}
	}
}

func TestAdmissionRefusesInconsistentRecognizedClients(t *testing.T) {
	t.Parallel()

	catalog := clientadapter.BuiltInCatalog()
	adapter, ok := catalog.ExpectedEvidenceForPlatform("claude-code", "2.1.220", "darwin", "arm64")
	if !ok {
		t.Fatal("built-in Claude Code evidence is unavailable")
	}
	tools := clientadapter.FeatureStructuredWorkspaceTools
	for _, testCase := range []struct {
		name     string
		adapter  *clientadapter.Evidence
		client   clientadapter.RecognizedClient
		accepted bool
	}{
		{"with release evidence", &adapter,
			clientadapter.RecognizedClient{ID: "claude-code", Policy: tools, Protections: tools}, false},
		{"with release evidence and no grant", &adapter,
			clientadapter.RecognizedClient{ID: "claude-code"}, false},
		{"an unknown policy bit", nil,
			clientadapter.RecognizedClient{ID: "claude-code", Policy: tools | 1<<40}, false},
		{"an unknown protection bit", nil,
			clientadapter.RecognizedClient{ID: "claude-code", Policy: tools, Protections: 1 << 40}, false},
		{"protections beyond the policy", nil,
			clientadapter.RecognizedClient{ID: "claude-code", Policy: tools,
				Protections: tools | clientadapter.FeatureCoreOwnedStreamingFallback}, false},
		{"protections without a client", nil,
			clientadapter.RecognizedClient{Policy: tools, Protections: tools}, false},
		{"a consistent client", nil,
			clientadapter.RecognizedClient{ID: "claude-code", Policy: tools, Protections: tools}, true},
		{"a switched-off client", nil,
			clientadapter.RecognizedClient{ID: "claude-code", Policy: tools}, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NewManagedRun(ManagedRunEvidence{
				CaptureRunID:     "run-inconsistent",
				SourceLabel:      "claude",
				Adapter:          testCase.adapter,
				RecognizedClient: testCase.client,
			})
			if testCase.accepted != (err == nil) {
				t.Fatalf("NewManagedRun() error = %v, want accepted %v", err, testCase.accepted)
			}
			if err != nil && !errors.Is(err, ErrInvalidAdmission) {
				t.Fatalf("error = %v, want ErrInvalidAdmission", err)
			}
		})
	}

	manual, err := NewManual("manual-recognized", 1, "Claude")
	if err != nil {
		t.Fatal(err)
	}
	manual.recognizedClient = clientadapter.RecognizedClient{ID: "claude-code"}
	if err := manual.Validate(); !errors.Is(err, ErrInvalidAdmission) {
		t.Fatalf("a manual admission naming a client validated: %v", err)
	}
}

// The authorizer carries the run's recognized client into the admission, and
// a run that names none still authorizes exactly as before.
func TestManagedRunAuthorizationCarriesTheRecognizedClient(t *testing.T) {
	t.Parallel()

	catalog := clientadapter.BuiltInCatalog()
	adapter, ok := catalog.ExpectedEvidenceForPlatform("claude-code", "2.1.220", "darwin", "arm64")
	if !ok {
		t.Fatal("built-in Claude Code evidence is unavailable")
	}
	tools := clientadapter.FeatureStructuredWorkspaceTools
	for _, testCase := range []struct {
		name    string
		adapter *clientadapter.Evidence
		client  clientadapter.RecognizedClient
		want    clientadapter.ProtectionBasis
	}{
		{"recognized", nil,
			clientadapter.RecognizedClient{ID: "claude-code", Policy: tools, Protections: tools},
			clientadapter.ProtectionRecognizedClient},
		{"recognized under the empty policy", nil,
			clientadapter.RecognizedClient{ID: "claude-code"}, clientadapter.ProtectionNone},
		{"verified", &adapter, clientadapter.RecognizedClient{}, clientadapter.ProtectionExactRelease},
		{"unverified, unknown or stale", nil, clientadapter.RecognizedClient{}, clientadapter.ProtectionNone},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			value := testCredentialValue(t, capturecredential.KindManagedRun)
			runs := &frozenRunAuthorizer{
				want: value,
				evidence: capturerun.Evidence{
					RunID:            "run-carrier",
					ExecutableLabel:  "claude",
					CWD:              "/workspace/project",
					Workspace:        testWorkspace(t),
					Adapter:          testCase.adapter,
					RecognizedClient: testCase.client,
				},
			}
			authorizer, err := NewAuthorizer(runs, &frozenManualAuthorizer{err: manualcapture.ErrCredentialRejected})
			if err != nil {
				t.Fatal(err)
			}
			credential, err := NewProxyCredential(value)
			if err != nil {
				t.Fatal(err)
			}
			admission, err := authorizer.Authorize(context.Background(), credential)
			if err != nil {
				t.Fatal(err)
			}
			if got := admission.ProtectionBasis(tools); got != testCase.want {
				t.Fatalf("ProtectionBasis(tools) = %s, want %s", got, testCase.want)
			}
		})
	}
}
