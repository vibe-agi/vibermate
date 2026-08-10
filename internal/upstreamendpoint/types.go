// Package upstreamendpoint owns reusable upstream service endpoints. A
// ProviderAccount belongs to exactly one Endpoint; protocol compatibility is
// never sufficient authority to move credentials between Endpoints.
package upstreamendpoint

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
)

const (
	MaxIDBytes          = 128
	MaxDisplayNameBytes = 256
	MaxRevision         = uint64(1<<63 - 1)
)

var (
	ErrInvalidEndpoint  = errors.New("UpstreamEndpoint is invalid")
	ErrEndpointNotFound = errors.New("UpstreamEndpoint was not found")
	ErrRevisionConflict = errors.New("UpstreamEndpoint revision conflicts with the expected revision")
	ErrEndpointDisabled = errors.New("UpstreamEndpoint is disabled")
	ErrManagerClosing   = errors.New("UpstreamEndpoint manager is closing")
)

type ID string

func NewID(value string) (ID, error) {
	if !validID(value) {
		return "", ErrInvalidEndpoint
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

type Endpoint struct {
	ID               ID
	DisplayName      string
	Origin           originidentity.ProviderOrigin
	RealmID          string
	BackendProtocols []string
	Capabilities     []protocolspec.ProviderCapability
	Drivers          []providerauth.DriverRef
	State            State
	Revision         uint64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (endpoint Endpoint) Validate() error {
	parsedID, err := NewID(endpoint.ID.String())
	if err != nil || parsedID != endpoint.ID ||
		!validDisplayName(endpoint.DisplayName) ||
		endpoint.Origin.Validate() != nil ||
		!validIdentity(endpoint.RealmID) ||
		!endpoint.State.Valid() || endpoint.Revision == 0 || endpoint.Revision > MaxRevision ||
		endpoint.CreatedAt.IsZero() || endpoint.UpdatedAt.IsZero() ||
		endpoint.UpdatedAt.Before(endpoint.CreatedAt) ||
		len(endpoint.BackendProtocols) == 0 || len(endpoint.Capabilities) == 0 ||
		len(endpoint.Drivers) == 0 {
		return ErrInvalidEndpoint
	}
	protocols := make(map[string]struct{}, len(endpoint.BackendProtocols))
	for _, protocol := range endpoint.BackendProtocols {
		if !validIdentity(protocol) {
			return ErrInvalidEndpoint
		}
		if _, duplicate := protocols[protocol]; duplicate {
			return ErrInvalidEndpoint
		}
		protocols[protocol] = struct{}{}
	}
	capabilities := make(map[protocolspec.ProviderCapability]struct{}, len(endpoint.Capabilities))
	for _, capability := range endpoint.Capabilities {
		if !capability.Valid() {
			return ErrInvalidEndpoint
		}
		if _, duplicate := capabilities[capability]; duplicate {
			return ErrInvalidEndpoint
		}
		capabilities[capability] = struct{}{}
	}
	drivers := make(map[string]struct{}, len(endpoint.Drivers))
	for _, driver := range endpoint.Drivers {
		parsed, parseErr := providerauth.NewDriverRef(driver.String())
		if parseErr != nil || parsed != driver {
			return ErrInvalidEndpoint
		}
		if _, duplicate := drivers[driver.String()]; duplicate {
			return ErrInvalidEndpoint
		}
		drivers[driver.String()] = struct{}{}
	}
	return nil
}

func (endpoint Endpoint) Clone() Endpoint {
	cloned := endpoint
	cloned.BackendProtocols = slices.Clone(endpoint.BackendProtocols)
	cloned.Capabilities = slices.Clone(endpoint.Capabilities)
	cloned.Drivers = slices.Clone(endpoint.Drivers)
	return cloned
}

func (endpoint Endpoint) Equal(other Endpoint) bool {
	return endpoint.ID == other.ID && endpoint.DisplayName == other.DisplayName &&
		endpoint.Origin == other.Origin && endpoint.RealmID == other.RealmID &&
		slices.Equal(endpoint.BackendProtocols, other.BackendProtocols) &&
		slices.Equal(endpoint.Capabilities, other.Capabilities) &&
		slices.Equal(endpoint.Drivers, other.Drivers) && endpoint.State == other.State &&
		endpoint.Revision == other.Revision && endpoint.CreatedAt.Equal(other.CreatedAt) &&
		endpoint.UpdatedAt.Equal(other.UpdatedAt)
}

type CommitOutcome string

const (
	CommitCommitted     CommitOutcome = "committed"
	CommitConflict      CommitOutcome = "conflict"
	CommitNotCommitted  CommitOutcome = "not_committed"
	CommitIndeterminate CommitOutcome = "indeterminate"
)

type CommitResult struct {
	Outcome  CommitOutcome
	Endpoint Endpoint
	Actual   uint64
}

type Repository interface {
	LoadAll(context.Context) ([]Endpoint, error)
	Load(context.Context, ID) (Endpoint, bool, error)
	Write(context.Context, uint64, Endpoint) (CommitResult, error)
}

type CreateCommand struct {
	ID               ID
	DisplayName      string
	Origin           originidentity.ProviderOrigin
	RealmID          string
	BackendProtocols []string
	Capabilities     []protocolspec.ProviderCapability
	Drivers          []providerauth.DriverRef
}

type Catalog interface {
	LookupEndpoint(string) (Endpoint, bool)
}

type Controller interface {
	Catalog
	List(context.Context) ([]Endpoint, error)
	Get(context.Context, ID) (Endpoint, error)
	Create(context.Context, CreateCommand) (Endpoint, error)
}

func validID(value string) bool {
	if value == "" || len(value) > MaxIDBytes || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return false
	}
	for index, character := range value {
		if unicode.IsControl(character) || character > unicode.MaxASCII ||
			!(character >= 'a' && character <= 'z') &&
				!(character >= '0' && character <= '9') &&
				character != '-' && character != '_' && character != '.' ||
			index == 0 && !(character >= 'a' && character <= 'z') &&
				!(character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}

func validIdentity(value string) bool {
	if value == "" || len(value) > MaxIDBytes || !utf8.ValidString(value) ||
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

func validDisplayName(value string) bool {
	if value == "" || len(value) > MaxDisplayNameBytes || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
