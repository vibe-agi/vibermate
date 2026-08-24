package controlprincipal_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"

	"github.com/vibe-agi/vibermate/internal/controlprincipal"
)

func TestPrincipalKeepsGrantScopeTypedAndImmutable(t *testing.T) {
	t.Parallel()

	allowed := []controlprincipal.GrantKind{
		controlprincipal.GrantCaptureRun,
		controlprincipal.GrantManualCapture,
	}
	principal, err := controlprincipal.New(controlprincipal.Attributes{
		ID:                 "local-cli:instance_1",
		Kind:               controlprincipal.KindLocalCLI,
		CredentialRevision: 1,
		AllowedGrantKinds:  allowed,
	})
	if err != nil {
		t.Fatal(err)
	}
	allowed[0] = "not_a_grant"
	projected := principal.AllowedGrantKinds()
	projected[0] = "not_a_grant"
	if !principal.Valid() ||
		!principal.Allows(controlprincipal.GrantCaptureRun) ||
		!principal.Allows(controlprincipal.GrantManualCapture) ||
		principal.Allows("not_a_grant") {
		t.Fatalf("principal scope changed through an alias: %+v", principal)
	}
}

func TestPrincipalRejectsRemoteScopeClaimsOnLocalIdentity(t *testing.T) {
	t.Parallel()

	_, err := controlprincipal.New(controlprincipal.Attributes{
		ID:                    "local-cli",
		Kind:                  controlprincipal.KindLocalCLI,
		ProxyClientBindingID:  "binding_1",
		MachineRegistrationID: "machine_1",
		CredentialRevision:    1,
		AllowedGrantKinds: []controlprincipal.GrantKind{
			controlprincipal.GrantCaptureRun,
		},
	})
	if err == nil {
		t.Fatal("local principal accepted caller-supplied remote scope")
	}
}

func TestAuthorityRotatesByRevisionWithoutAuthenticationGapAndRevokes(
	t *testing.T,
) {
	t.Parallel()

	firstPrincipal := localPrincipal(t, 1)
	secondPrincipal := localPrincipal(t, 2)
	first := capability(0x11)
	second := capability(0x22)
	authority, err := controlprincipal.NewAuthority(controlprincipal.CredentialGrant{
		Credential: first,
		Principal:  firstPrincipal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if principal, ok := authority.Authenticate(context.Background(), first); !ok ||
		principal.CredentialRevision() != 1 {
		t.Fatal("initial control credential was rejected")
	}
	rotation, err := authority.Prepare(controlprincipal.CredentialGrant{
		Credential: second,
		Principal:  secondPrincipal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := authority.Authenticate(context.Background(), first); !ok {
		t.Fatal("prepared rotation rejected the current credential")
	}
	if principal, ok := authority.Authenticate(context.Background(), second); !ok ||
		principal.CredentialRevision() != 2 {
		t.Fatal("prepared rotation rejected the candidate credential")
	}
	if err := rotation.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, ok := authority.Authenticate(context.Background(), first); ok {
		t.Fatal("committed rotation retained the previous credential")
	}
	if _, ok := authority.Authenticate(context.Background(), second); !ok {
		t.Fatal("committed rotation rejected the current credential")
	}
	authority.Revoke()
	if _, ok := authority.Authenticate(context.Background(), second); ok {
		t.Fatal("revoked authority accepted a credential")
	}
}

func TestAuthorityRejectsCrossConnectionAndSkippedRevisionRotation(t *testing.T) {
	t.Parallel()

	authority, err := controlprincipal.NewAuthority(controlprincipal.CredentialGrant{
		Credential: capability(0x31),
		Principal:  localPrincipal(t, 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := controlprincipal.New(controlprincipal.Attributes{
		ID:                 "local-cli:other",
		Kind:               controlprincipal.KindLocalCLI,
		CredentialRevision: 2,
		AllowedGrantKinds: []controlprincipal.GrantKind{
			controlprincipal.GrantCaptureRun,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, principal := range []controlprincipal.Principal{
		other,
		localPrincipal(t, 3),
	} {
		if _, err := authority.Prepare(controlprincipal.CredentialGrant{
			Credential: capability(byte(principal.CredentialRevision())),
			Principal:  principal,
		}); err == nil {
			t.Fatalf("invalid rotation principal=%+v succeeded", principal)
		}
	}
}

func TestRemoteGuestAndEnrolledClientCarryDifferentAuthenticatedMachineScopes(t *testing.T) {
	t.Parallel()

	guest, err := controlprincipal.New(controlprincipal.Attributes{
		ID:                 "guest:one",
		Kind:               controlprincipal.KindRemoteGuest,
		MachineID:          "machine-source-one",
		CredentialRevision: 1,
		AllowedGrantKinds:  []controlprincipal.GrantKind{controlprincipal.GrantCaptureRun},
	})
	if err != nil {
		t.Fatal(err)
	}
	if binding, ok := guest.ProxyClientBindingID(); ok || binding != "" {
		t.Fatalf("guest received durable binding %q", binding)
	}
	if machine, ok := guest.MachineID(); !ok || machine != "machine-source-one" {
		t.Fatalf("guest machine = %q, %v", machine, ok)
	}

	enrolled, err := controlprincipal.New(controlprincipal.Attributes{
		ID:                    "principal:one",
		Kind:                  controlprincipal.KindEnrolledClient,
		ProxyClientBindingID:  "binding:one",
		MachineRegistrationID: "registration:one",
		MachineID:             "machine-source-one",
		CredentialRevision:    1,
		AllowedGrantKinds:     []controlprincipal.GrantKind{controlprincipal.GrantCaptureRun},
	})
	if err != nil {
		t.Fatal(err)
	}
	if binding, ok := enrolled.ProxyClientBindingID(); !ok || binding != "binding:one" {
		t.Fatalf("enrolled binding = %q, %v", binding, ok)
	}
	if machine, ok := enrolled.MachineID(); !ok || machine != "machine-source-one" {
		t.Fatalf("enrolled machine = %q, %v", machine, ok)
	}
}

func TestRuntimeUserPrincipalFreezesAuthenticatedDeviceAttribution(t *testing.T) {
	t.Parallel()

	principal, err := controlprincipal.New(controlprincipal.Attributes{
		ID:                 "runtime-user:login-one",
		Kind:               controlprincipal.KindRuntimeUser,
		MachineID:          "machine-source-one",
		DeviceName:         "Linux workstation",
		RuntimeUserID:      "user.source-one",
		LoginSessionID:     "login.source-one",
		CredentialRevision: 1,
		AllowedGrantKinds:  []controlprincipal.GrantKind{controlprincipal.GrantCaptureRun},
	})
	if err != nil {
		t.Fatal(err)
	}
	if device, ok := principal.DeviceName(); !ok || device != "Linux workstation" {
		t.Fatalf("Runtime User device = %q, %v", device, ok)
	}
	if !principal.Valid() {
		t.Fatal("Runtime User principal became invalid after construction")
	}
}

func localPrincipal(
	t *testing.T,
	revision controlprincipal.CredentialRevision,
) controlprincipal.Principal {
	t.Helper()
	principal, err := controlprincipal.New(controlprincipal.Attributes{
		ID:                 "local-cli:instance_1",
		Kind:               controlprincipal.KindLocalCLI,
		CredentialRevision: revision,
		AllowedGrantKinds: []controlprincipal.GrantKind{
			controlprincipal.GrantCaptureRun,
			controlprincipal.GrantManualCapture,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return principal
}

func capability(fill byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}
