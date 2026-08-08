// Package provideraccount owns global upstream account configuration and
// attempt-scoped credential leases. Secret bytes remain in SecretStore.
package provideraccount

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

const (
	MaxIdentityBytes    = 128
	MaxDisplayNameBytes = 256
	MaxRevision         = uint64(1<<63 - 1)
)

var (
	ErrInvalidAccount    = errors.New("ProviderAccount is invalid")
	ErrAccountNotFound   = errors.New("ProviderAccount was not found")
	ErrRevisionConflict  = errors.New("ProviderAccount revision conflicts with the expected revision")
	ErrAccountDisabled   = errors.New("ProviderAccount is disabled")
	ErrRealmMismatch     = errors.New("ProviderAccount does not belong to the requested realm")
	ErrCredentialMissing = errors.New("ProviderAccount credential is unavailable")
	ErrManagerClosing    = errors.New("ProviderAccount manager is closing")
)

type ID string

func NewID(value string) (ID, error) {
	if !validIdentity(value) {
		return "", ErrInvalidAccount
	}
	return ID(value), nil
}

func (id ID) String() string { return string(id) }

type State string

const (
	StateActive   State = "active"
	StateDisabled State = "disabled"
)

func (state State) Valid() bool {
	return state == StateActive || state == StateDisabled
}

type Realm struct {
	ID               string
	BackendProtocols []string
	Drivers          []providerauth.DriverRef
}

func (realm Realm) Validate() error {
	if !validIdentity(realm.ID) || len(realm.BackendProtocols) == 0 || len(realm.Drivers) == 0 {
		return ErrInvalidAccount
	}
	protocols := make(map[string]struct{}, len(realm.BackendProtocols))
	for _, protocol := range realm.BackendProtocols {
		if !validIdentity(protocol) {
			return ErrInvalidAccount
		}
		if _, duplicate := protocols[protocol]; duplicate {
			return ErrInvalidAccount
		}
		protocols[protocol] = struct{}{}
	}
	drivers := make(map[string]struct{}, len(realm.Drivers))
	for _, driver := range realm.Drivers {
		if _, err := providerauth.NewDriverRef(driver.String()); err != nil {
			return ErrInvalidAccount
		}
		if _, duplicate := drivers[driver.String()]; duplicate {
			return ErrInvalidAccount
		}
		drivers[driver.String()] = struct{}{}
	}
	return nil
}

type Account struct {
	ID          ID
	DisplayName string
	RealmID     string
	Driver      providerauth.DriverRef
	SecretRef   secretstore.Reference
	State       State
	Revision    uint64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (account Account) Validate() error {
	parsedID, err := NewID(account.ID.String())
	if err != nil || parsedID != account.ID ||
		!validDisplayName(account.DisplayName) ||
		!validIdentity(account.RealmID) ||
		!account.State.Valid() ||
		account.Revision == 0 || account.Revision > MaxRevision ||
		account.SecretRef.String() == "" ||
		account.CreatedAt.IsZero() || account.UpdatedAt.IsZero() ||
		account.UpdatedAt.Before(account.CreatedAt) {
		return ErrInvalidAccount
	}
	parsedDriver, err := providerauth.NewDriverRef(account.Driver.String())
	if err != nil || parsedDriver != account.Driver {
		return ErrInvalidAccount
	}
	parsedSecret, err := secretstore.ParseReference(account.SecretRef.String())
	if err != nil || parsedSecret != account.SecretRef {
		return ErrInvalidAccount
	}
	return nil
}

func (account Account) Descriptor(realm Realm) environment.AccountDescriptor {
	return environment.AccountDescriptor{
		ID: account.ID.String(), Revision: environment.Revision(account.Revision),
		RealmID: account.RealmID, Active: account.State == StateActive,
		BackendProtocols: append([]string(nil), realm.BackendProtocols...),
	}
}

type HealthState string

const (
	HealthReady       HealthState = "ready"
	HealthDisabled    HealthState = "disabled"
	HealthMissing     HealthState = "credential_missing"
	HealthUnavailable HealthState = "credential_unavailable"
)

type Health struct {
	State           HealthState
	CredentialEpoch uint64
}

func (health Health) Validate() error {
	switch health.State {
	case HealthReady:
		if health.CredentialEpoch == 0 || health.CredentialEpoch > MaxRevision {
			return ErrInvalidAccount
		}
	case HealthDisabled, HealthMissing:
		if health.CredentialEpoch != 0 {
			return ErrInvalidAccount
		}
	case HealthUnavailable:
		if health.CredentialEpoch > MaxRevision {
			return ErrInvalidAccount
		}
	default:
		return ErrInvalidAccount
	}
	return nil
}

type View struct {
	Account Account
	Health  Health
}

type CommitOutcome string

const (
	CommitCommitted     CommitOutcome = "committed"
	CommitConflict      CommitOutcome = "conflict"
	CommitNotCommitted  CommitOutcome = "not_committed"
	CommitIndeterminate CommitOutcome = "indeterminate"
)

type CommitResult struct {
	Outcome CommitOutcome
	Account Account
	Actual  uint64
}

type Repository interface {
	LoadAll(context.Context) ([]Account, error)
	Load(context.Context, ID) (Account, bool, error)
	Write(context.Context, uint64, Account) (CommitResult, error)
}

type CreateCommand struct {
	ID          ID
	DisplayName string
	RealmID     string
	Driver      providerauth.DriverRef
	Secret      *secretstore.Value
}

type ReplaceSecretCommand struct {
	ID                      ID
	ExpectedCredentialEpoch uint64
	Secret                  *secretstore.Value
}

type Controller interface {
	List(context.Context) ([]View, error)
	Get(context.Context, ID) (View, error)
	Create(context.Context, CreateCommand) (View, error)
	ReplaceSecret(context.Context, ReplaceSecretCommand) (View, error)
}

func secretReference(id ID) (secretstore.Reference, error) {
	return secretstore.ParseReference("secret://provider-account/" + id.String())
}

func validIdentity(value string) bool {
	if value == "" || len(value) > MaxIdentityBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
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

func validDisplayName(value string) bool {
	if value == "" || len(value) > MaxDisplayNameBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
