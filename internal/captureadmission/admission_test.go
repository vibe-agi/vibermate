package captureadmission

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturecredential"
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
			RunID:           "run-one",
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
		admission.IngressProfileID() != "capture-run/run-one" ||
		admission.CredentialRevision() != 1 ||
		admission.AttributionConfidence() != AttributionConfigured ||
		admission.SourceLabel() != "claude" || runs.authorized != 1 ||
		manuals.authorized != 0 {
		t.Fatalf("admission = %#v, authorizations = %d", admission, runs.authorized)
	}
	if runID, ok := admission.CaptureRunID(); !ok || runID != "run-one" {
		t.Fatalf("CaptureRun identity = %q, %v", runID, ok)
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

func TestManualAdmissionCannotInventManagedEvidence(t *testing.T) {
	t.Parallel()
	admission, err := NewManual("manual-one", 3, "Codex App")
	if err != nil {
		t.Fatal(err)
	}
	if admission.Kind() != KindManual ||
		admission.IngressProfileID() != "manual-capture/manual-one" ||
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
		admission.IngressProfileID() != "manual-capture/manual-one" ||
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
