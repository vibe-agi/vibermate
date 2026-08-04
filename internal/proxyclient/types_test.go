package proxyclient

import (
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/controlprincipal"
)

func TestCredentialsUseDisjointCanonicalNamespacesAndRedactFormatting(t *testing.T) {
	t.Parallel()
	entropy := make([]byte, CredentialBytes)
	for index := range entropy {
		entropy[index] = byte(index + 1)
	}
	encoded := base64.RawURLEncoding.EncodeToString(entropy)
	enrollment, err := ParseEnrollmentCredential("enroll_" + encoded)
	if err != nil {
		t.Fatal(err)
	}
	control, err := ParseControlCredential("control_" + encoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseEnrollmentCredential(control.Value()); err == nil {
		t.Fatal("control credential parsed as enrollment credential")
	}
	if _, err := ParseControlCredential(enrollment.Value()); err == nil {
		t.Fatal("enrollment credential parsed as control credential")
	}
	for _, formatted := range []string{
		fmt.Sprint(enrollment),
		fmt.Sprintf("%#v", enrollment),
		fmt.Sprint(control),
		fmt.Sprintf("%#v", control),
	} {
		if strings.Contains(formatted, encoded) || !strings.Contains(formatted, "REDACTED") {
			t.Fatalf("credential formatting = %q", formatted)
		}
	}
}

func TestBindingPolicyIsCanonicalImmutableAndRejectsDuplicates(t *testing.T) {
	t.Parallel()
	ingress := []string{"scope-b", "scope-a"}
	profiles := []string{"profile-b", "profile-a"}
	grants := []controlprincipal.GrantKind{
		controlprincipal.GrantManualCapture,
		controlprincipal.GrantCaptureRun,
	}
	policy, err := NewBindingPolicy(ingress, profiles, "quota-default", grants)
	if err != nil {
		t.Fatal(err)
	}
	ingress[0] = "changed"
	profiles[0] = "changed"
	grants[0] = "changed"
	if !slices.Equal(policy.AllowedIngressScopes(), []string{"scope-a", "scope-b"}) ||
		!slices.Equal(policy.AllowedProfileIDs(), []string{"profile-a", "profile-b"}) ||
		!slices.Equal(policy.AllowedGrantKinds(), []controlprincipal.GrantKind{
			controlprincipal.GrantCaptureRun,
			controlprincipal.GrantManualCapture,
		}) {
		t.Fatalf("canonical policy = %+v", policy)
	}
	copy := policy.AllowedIngressScopes()
	copy[0] = "mutated"
	if policy.AllowedIngressScopes()[0] != "scope-a" {
		t.Fatal("policy getter exposed mutable storage")
	}
	if _, err := NewBindingPolicy(
		[]string{"same", "same"},
		[]string{"profile"},
		"quota",
		[]controlprincipal.GrantKind{controlprincipal.GrantCaptureRun},
	); err == nil {
		t.Fatal("duplicate policy identity was accepted")
	}
}
