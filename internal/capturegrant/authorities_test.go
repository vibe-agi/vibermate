package capturegrant

import "testing"

func TestCaptureAuthoritySetKeepsManagedAuthoritiesAsADefensiveSubset(
	t *testing.T,
) {
	t.Parallel()

	protected := []string{"api.openai.com:443", "api.anthropic.com:443"}
	managed := []string{"api.openai.com:443"}
	set, err := NewCaptureAuthoritySet(protected, managed)
	if err != nil {
		t.Fatal(err)
	}
	protected[0] = "mutated.example:443"
	managed[0] = "mutated.example:443"
	gotProtected := set.ProtectedAuthorities()
	gotManaged := set.ManagedCredentialAuthorities()
	if len(gotProtected) != 2 ||
		gotProtected[0] != "api.anthropic.com:443" ||
		gotProtected[1] != "api.openai.com:443" ||
		len(gotManaged) != 1 ||
		gotManaged[0] != "api.openai.com:443" {
		t.Fatalf("CaptureAuthoritySet protected=%v managed=%v", gotProtected, gotManaged)
	}
	gotProtected[0] = "changed.example:443"
	if set.ProtectedAuthorities()[0] != "api.anthropic.com:443" {
		t.Fatal("CaptureAuthoritySet getter returned mutable authority storage")
	}
}

func TestCaptureAuthoritySetRejectsCredentialAuthorityOutsideProtection(
	t *testing.T,
) {
	t.Parallel()

	if _, err := NewCaptureAuthoritySet(
		[]string{"api.anthropic.com:443"},
		[]string{"api.openai.com:443"},
	); err == nil {
		t.Fatal("NewCaptureAuthoritySet() accepted a non-protected managed authority")
	}
}
