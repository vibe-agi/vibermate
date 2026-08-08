// Package providerauth owns provider authentication identities and immutable
// credential leases. It does not select routes or persist secret bytes.
package providerauth

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/secretstore"
)

const (
	MaxIdentityBytes = 128
	MaxRevision      = uint64(1<<63 - 1)

	StaticHeaderDriverValue    = "static_header"
	AnthropicAPIKeyDriverValue = "anthropic_api_key"
)

var ErrInvalidAuthentication = errors.New("provider authentication is invalid")

type DriverRef struct{ value string }

func NewDriverRef(value string) (DriverRef, error) {
	if !validIdentity(value) {
		return DriverRef{}, ErrInvalidAuthentication
	}
	return DriverRef{value: value}, nil
}

func (ref DriverRef) String() string { return ref.value }

func StaticHeaderDriverRef() DriverRef    { return DriverRef{value: StaticHeaderDriverValue} }
func AnthropicAPIKeyDriverRef() DriverRef { return DriverRef{value: AnthropicAPIKeyDriverValue} }

type CredentialMode string

const (
	CredentialClientPassthrough CredentialMode = "client_passthrough"
	CredentialManaged           CredentialMode = "managed"
)

func (mode CredentialMode) Valid() bool {
	return mode == CredentialClientPassthrough || mode == CredentialManaged
}

type AccountRef struct {
	ID              string
	Revision        uint64
	CredentialEpoch uint64
	RealmID         string
}

func (ref AccountRef) Validate() error {
	if !validIdentity(ref.ID) || !validIdentity(ref.RealmID) || ref.Revision == 0 ||
		ref.Revision > MaxRevision || ref.CredentialEpoch == 0 || ref.CredentialEpoch > MaxRevision {
		return ErrInvalidAuthentication
	}
	return nil
}

// Lease is one Attempt-scoped authorization. Release must be idempotent.
// Implementations own the underlying secret lifetime; callers receive only a
// typed secret reference for the configured SecretStore.
type Lease interface {
	Mode() CredentialMode
	Driver() DriverRef
	Secret() secretstore.Reference
	Account() (AccountRef, bool)
	Release()
}

func validIdentity(value string) bool {
	if value == "" || len(value) > MaxIdentityBytes || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || character > unicode.MaxASCII ||
			!(character >= 'a' && character <= 'z') &&
				!(character >= 'A' && character <= 'Z') &&
				!(character >= '0' && character <= '9') &&
				character != '-' && character != '_' && character != '.' && character != ':' {
			return false
		}
	}
	return true
}
