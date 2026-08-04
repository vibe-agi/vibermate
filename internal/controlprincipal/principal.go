// Package controlprincipal owns authenticated control-plane identity. A
// Principal contains authorization facts produced by a Host authenticator; it
// never contains the credential that proved those facts.
package controlprincipal

import (
	"errors"
	"fmt"
)

const maxIdentityBytes = 128

type Kind string

const (
	KindDesktopApp     Kind = "desktop_app"
	KindLocalCLI       Kind = "local_cli"
	KindEnrolledClient Kind = "enrolled_client"
)

func (kind Kind) Valid() bool {
	switch kind {
	case KindDesktopApp, KindLocalCLI, KindEnrolledClient:
		return true
	default:
		return false
	}
}

type GrantKind string

const (
	GrantCaptureRun    GrantKind = "capture_run"
	GrantManualCapture GrantKind = "manual_capture"
)

func (kind GrantKind) Valid() bool {
	return kind == GrantCaptureRun || kind == GrantManualCapture
}

type CredentialRevision uint64

func (revision CredentialRevision) Valid() bool {
	return revision > 0
}

type Attributes struct {
	ID                    string
	Kind                  Kind
	ProxyClientBindingID  string
	MachineRegistrationID string
	CredentialRevision    CredentialRevision
	AllowedGrantKinds     []GrantKind
}

// Principal is immutable after construction. Its grant allowlist is stored as
// a closed bit set, so callers cannot widen it through an aliased slice.
type Principal struct {
	id                    string
	kind                  Kind
	proxyClientBindingID  string
	machineRegistrationID string
	credentialRevision    CredentialRevision
	allowed               uint8
}

func New(attributes Attributes) (Principal, error) {
	if !validIdentity(attributes.ID) ||
		!attributes.Kind.Valid() ||
		!attributes.CredentialRevision.Valid() ||
		len(attributes.AllowedGrantKinds) == 0 {
		return Principal{}, errors.New("control principal is incomplete")
	}
	switch attributes.Kind {
	case KindDesktopApp, KindLocalCLI:
		if attributes.ProxyClientBindingID != "" ||
			attributes.MachineRegistrationID != "" {
			return Principal{}, errors.New(
				"local control principal carries remote scope",
			)
		}
	case KindEnrolledClient:
		if !validIdentity(attributes.ProxyClientBindingID) ||
			!validIdentity(attributes.MachineRegistrationID) {
			return Principal{}, errors.New(
				"enrolled control principal scope is incomplete",
			)
		}
	}
	principal := Principal{
		id:                    attributes.ID,
		kind:                  attributes.Kind,
		proxyClientBindingID:  attributes.ProxyClientBindingID,
		machineRegistrationID: attributes.MachineRegistrationID,
		credentialRevision:    attributes.CredentialRevision,
	}
	for _, kind := range attributes.AllowedGrantKinds {
		bit, err := grantBit(kind)
		if err != nil {
			return Principal{}, err
		}
		if principal.allowed&bit != 0 {
			return Principal{}, fmt.Errorf(
				"control principal grant kind %q is duplicated",
				kind,
			)
		}
		principal.allowed |= bit
	}
	return principal, nil
}

func (principal Principal) Valid() bool {
	if !validIdentity(principal.id) ||
		!principal.kind.Valid() ||
		!principal.credentialRevision.Valid() ||
		principal.allowed == 0 ||
		principal.allowed&^(grantCaptureRunBit|grantManualCaptureBit) != 0 {
		return false
	}
	switch principal.kind {
	case KindDesktopApp, KindLocalCLI:
		return principal.proxyClientBindingID == "" &&
			principal.machineRegistrationID == ""
	case KindEnrolledClient:
		return validIdentity(principal.proxyClientBindingID) &&
			validIdentity(principal.machineRegistrationID)
	default:
		return false
	}
}

func (principal Principal) ID() string {
	return principal.id
}

func (principal Principal) Kind() Kind {
	return principal.kind
}

func (principal Principal) CredentialRevision() CredentialRevision {
	return principal.credentialRevision
}

func (principal Principal) ProxyClientBindingID() (string, bool) {
	return principal.proxyClientBindingID, principal.proxyClientBindingID != ""
}

func (principal Principal) MachineRegistrationID() (string, bool) {
	return principal.machineRegistrationID, principal.machineRegistrationID != ""
}

func (principal Principal) Allows(kind GrantKind) bool {
	bit, err := grantBit(kind)
	return err == nil && principal.Valid() && principal.allowed&bit != 0
}

func (principal Principal) AllowedGrantKinds() []GrantKind {
	if !principal.Valid() {
		return nil
	}
	kinds := make([]GrantKind, 0, 2)
	for _, kind := range []GrantKind{GrantCaptureRun, GrantManualCapture} {
		if principal.Allows(kind) {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

func (principal Principal) sameConnection(other Principal) bool {
	return principal.Valid() && other.Valid() &&
		principal.id == other.id &&
		principal.kind == other.kind &&
		principal.proxyClientBindingID == other.proxyClientBindingID &&
		principal.machineRegistrationID == other.machineRegistrationID
}

const (
	grantCaptureRunBit uint8 = 1 << iota
	grantManualCaptureBit
)

func grantBit(kind GrantKind) (uint8, error) {
	switch kind {
	case GrantCaptureRun:
		return grantCaptureRunBit, nil
	case GrantManualCapture:
		return grantManualCaptureBit, nil
	default:
		return 0, fmt.Errorf("control principal grant kind %q is invalid", kind)
	}
}

func validIdentity(value string) bool {
	if len(value) == 0 || len(value) > maxIdentityBytes {
		return false
	}
	for _, character := range []byte(value) {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') {
			continue
		}
		switch character {
		case '-', '_', '.', ':':
		default:
			return false
		}
	}
	return true
}
