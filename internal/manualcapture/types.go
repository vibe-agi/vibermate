// Package manualcapture owns durable, revocable proxy capabilities for
// independently launched command-line and desktop applications. It records
// ingress attribution only; it cannot select an Access, Profile, route,
// account, model, plugin, or provider credential.
package manualcapture

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/capturecredential"
)

const (
	MaxIDBytes                               = 128
	MaxOwnerIDBytes                          = 128
	MaxDisplayNameBytes                      = 128
	DefaultPageLimit                         = 50
	MaxPageLimit                             = 200
	ProxyUsername                            = "capture"
	MaxCredentialRevision CredentialRevision = 1<<63 - 1
)

var (
	ErrInvalidCommand     = errors.New("invalid ManualCapture command")
	ErrInvalidRecord      = errors.New("invalid ManualCapture record")
	ErrCredentialRejected = errors.New("ManualCapture credential rejected")
	ErrNotFound           = errors.New("ManualCapture not found")
	ErrRevisionConflict   = errors.New("ManualCapture revision conflict")
	ErrNotActive          = errors.New("ManualCapture is not active")
	ErrStateConflict      = errors.New("ManualCapture state conflict")
	ErrRuntimeStopping    = errors.New("ManualCapture runtime is stopping")
)

type ID struct {
	value string
}

func ParseID(value string) (ID, error) {
	if !validIdentity(value, MaxIDBytes) {
		return ID{}, fmt.Errorf("%w: ID is invalid", ErrInvalidCommand)
	}
	return ID{value: value}, nil
}

func (id ID) String() string {
	return id.value
}

func (id ID) Valid() bool {
	return validIdentity(id.value, MaxIDBytes)
}

type OwnerKind string

const (
	OwnerLocalInstallation  OwnerKind = "local_installation"
	OwnerProxyClientBinding OwnerKind = "proxy_client_binding"
)

type OwnerScope struct {
	kind                 OwnerKind
	proxyClientBindingID string
}

func NewLocalOwnerScope() OwnerScope {
	return OwnerScope{kind: OwnerLocalInstallation}
}

func NewProxyClientOwnerScope(bindingID string) (OwnerScope, error) {
	owner := OwnerScope{
		kind:                 OwnerProxyClientBinding,
		proxyClientBindingID: bindingID,
	}
	if !owner.Valid() {
		return OwnerScope{}, fmt.Errorf("%w: owner scope is invalid", ErrInvalidCommand)
	}
	return owner, nil
}

func RestoreOwnerScope(kind OwnerKind, bindingID string) (OwnerScope, error) {
	owner := OwnerScope{kind: kind, proxyClientBindingID: bindingID}
	if !owner.Valid() {
		return OwnerScope{}, fmt.Errorf("%w: owner scope is invalid", ErrInvalidRecord)
	}
	return owner, nil
}

func (owner OwnerScope) Valid() bool {
	switch owner.kind {
	case OwnerLocalInstallation:
		return owner.proxyClientBindingID == ""
	case OwnerProxyClientBinding:
		return validIdentity(owner.proxyClientBindingID, MaxOwnerIDBytes)
	default:
		return false
	}
}

func (owner OwnerScope) Kind() OwnerKind {
	return owner.kind
}

func (owner OwnerScope) ProxyClientBindingID() (string, bool) {
	return owner.proxyClientBindingID, owner.proxyClientBindingID != ""
}

type ClientClass string

const (
	ClientCLI        ClientClass = "cli"
	ClientDesktopApp ClientClass = "desktop_app"
	ClientOther      ClientClass = "other"
)

func (class ClientClass) Valid() bool {
	return class == ClientCLI || class == ClientDesktopApp || class == ClientOther
}

type Lifetime string

const (
	LifetimeTemporary    Lifetime = "temporary"
	LifetimeUntilRevoked Lifetime = "until_revoked"
)

func (lifetime Lifetime) Valid() bool {
	return lifetime == LifetimeTemporary || lifetime == LifetimeUntilRevoked
}

type State string

const (
	StateActive  State = "active"
	StateRevoked State = "revoked"
	StateExpired State = "expired"
)

func (state State) Valid() bool {
	return state == StateActive || state == StateRevoked || state == StateExpired
}

type Observation string

const (
	ObservationWaiting  Observation = "waiting_for_traffic"
	ObservationObserved Observation = "observed"
)

func (observation Observation) Valid() bool {
	return observation == ObservationWaiting || observation == ObservationObserved
}

type CredentialRevision uint64

func (revision CredentialRevision) Valid() bool {
	return revision > 0 && revision <= MaxCredentialRevision
}

type CredentialDigest [sha256.Size]byte

func (digest CredentialDigest) Valid() bool {
	return digest != CredentialDigest{}
}

// DurableRecord is the complete SQLite authority. It stores only a
// domain-separated credential digest. IngressProfileID is deliberately absent
// because it is derived as manual-capture/<ID>.
type DurableRecord struct {
	ID                  ID
	Owner               OwnerScope
	DisplayName         string
	ClientClass         ClientClass
	Lifetime            Lifetime
	State               State
	CredentialRevision  CredentialRevision
	ProxyCredentialHash CredentialDigest
	Observation         Observation
	CreatedAt           time.Time
	UpdatedAt           time.Time
	ExpiresAt           time.Time
	LastObservedAt      time.Time
}

func (record DurableRecord) Validate() error {
	if !record.ID.Valid() || !record.Owner.Valid() ||
		!validDisplayName(record.DisplayName) || !record.ClientClass.Valid() ||
		!record.Lifetime.Valid() || !record.State.Valid() ||
		!record.CredentialRevision.Valid() || !record.ProxyCredentialHash.Valid() ||
		!record.Observation.Valid() || record.CreatedAt.IsZero() ||
		record.UpdatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) {
		return ErrInvalidRecord
	}
	switch record.Lifetime {
	case LifetimeTemporary:
		if record.ExpiresAt.IsZero() || record.ExpiresAt.Before(record.CreatedAt) {
			return fmt.Errorf("%w: temporary lifetime is incomplete", ErrInvalidRecord)
		}
	case LifetimeUntilRevoked:
		if !record.ExpiresAt.IsZero() || record.State == StateExpired {
			return fmt.Errorf("%w: until-revoked lifetime is inconsistent", ErrInvalidRecord)
		}
	}
	if record.State == StateExpired && record.Lifetime != LifetimeTemporary {
		return fmt.Errorf("%w: only a temporary capture can expire", ErrInvalidRecord)
	}
	if record.Observation == ObservationWaiting {
		if !record.LastObservedAt.IsZero() {
			return fmt.Errorf("%w: waiting capture has an observation time", ErrInvalidRecord)
		}
	} else if record.LastObservedAt.IsZero() ||
		record.LastObservedAt.Before(record.CreatedAt) ||
		record.LastObservedAt.After(record.UpdatedAt) {
		return fmt.Errorf("%w: observed capture time is inconsistent", ErrInvalidRecord)
	}
	return nil
}

type View struct {
	ID                 string             `json:"id"`
	IngressProfileID   string             `json:"ingressProfileId"`
	DisplayName        string             `json:"displayName"`
	ClientClass        ClientClass        `json:"clientClass"`
	Lifetime           Lifetime           `json:"lifetime"`
	State              State              `json:"state"`
	CredentialRevision CredentialRevision `json:"credentialRevision"`
	Observation        Observation        `json:"observation"`
	CreatedAt          time.Time          `json:"createdAt"`
	UpdatedAt          time.Time          `json:"updatedAt"`
	ExpiresAt          *time.Time         `json:"expiresAt,omitempty"`
	LastObservedAt     *time.Time         `json:"lastObservedAt,omitempty"`
}

func ViewOf(record DurableRecord) View {
	view := View{
		ID:                 record.ID.String(),
		IngressProfileID:   "manual-capture/" + record.ID.String(),
		DisplayName:        record.DisplayName,
		ClientClass:        record.ClientClass,
		Lifetime:           record.Lifetime,
		State:              record.State,
		CredentialRevision: record.CredentialRevision,
		Observation:        record.Observation,
		CreatedAt:          record.CreatedAt,
		UpdatedAt:          record.UpdatedAt,
	}
	if !record.ExpiresAt.IsZero() {
		value := record.ExpiresAt
		view.ExpiresAt = &value
	}
	if !record.LastObservedAt.IsZero() {
		value := record.LastObservedAt
		view.LastObservedAt = &value
	}
	return view
}

// ProxyCredential is returned only on create or rotation. The manager never
// retains its raw value and formatting always redacts it.
type ProxyCredential struct {
	value string
}

func NewProxyCredential(value string) (ProxyCredential, error) {
	credential, err := capturecredential.Parse(value)
	if err != nil || credential.Kind() != capturecredential.KindManualCapture {
		return ProxyCredential{}, ErrCredentialRejected
	}
	return ProxyCredential{value: value}, nil
}

func (credential ProxyCredential) Value() string {
	return credential.value
}

func (ProxyCredential) String() string {
	return "[REDACTED]"
}

func (ProxyCredential) GoString() string {
	return "manualcapture.ProxyCredential{[REDACTED]}"
}

type Grant struct {
	Capture    View
	Credential ProxyCredential
}

func (grant Grant) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("manual_capture_id", grant.Capture.ID),
		slog.Uint64("credential_revision", uint64(grant.Capture.CredentialRevision)),
		slog.String("proxy_credential", "[REDACTED]"),
	)
}

type CreateCommand struct {
	Owner       OwnerScope
	DisplayName string
	ClientClass ClientClass
	Lifetime    Lifetime
	ExpiresIn   time.Duration
}

type RotateCommand struct {
	Owner                      OwnerScope
	ID                         ID
	ExpectedCredentialRevision CredentialRevision
}

type RevokeCommand struct {
	Owner                      OwnerScope
	ID                         ID
	ExpectedCredentialRevision CredentialRevision
}

type Evidence struct {
	ManualCaptureID    ID
	CredentialRevision CredentialRevision
	DisplayName        string
	Owner              OwnerScope
}

func (evidence Evidence) Valid() bool {
	return evidence.ManualCaptureID.Valid() && evidence.CredentialRevision.Valid() &&
		validDisplayName(evidence.DisplayName) && evidence.Owner.Valid()
}

type Recovery struct {
	ExpiredCount int
	ActiveCount  int
}

type PageRequest struct {
	Owner OwnerScope
	Limit int
}

func (request PageRequest) Normalized() PageRequest {
	if request.Limit <= 0 {
		request.Limit = DefaultPageLimit
	}
	if request.Limit > MaxPageLimit {
		request.Limit = MaxPageLimit
	}
	return request
}

type Page struct {
	Items []View `json:"items"`
}

type Repository interface {
	Create(context.Context, DurableRecord) error
	Rotate(
		context.Context,
		OwnerScope,
		ID,
		CredentialRevision,
		CredentialDigest,
		time.Time,
	) (DurableRecord, error)
	Revoke(
		context.Context,
		OwnerScope,
		ID,
		CredentialRevision,
		time.Time,
	) (DurableRecord, error)
	AuthorizeProxy(context.Context, CredentialDigest, time.Time) (DurableRecord, error)
	Get(context.Context, OwnerScope, ID, time.Time) (DurableRecord, error)
	List(context.Context, PageRequest, time.Time) ([]DurableRecord, error)
	Recover(context.Context, time.Time) (Recovery, error)
}

type Controller interface {
	Create(context.Context, CreateCommand) (Grant, error)
	Rotate(context.Context, RotateCommand) (Grant, error)
	Revoke(context.Context, RevokeCommand) (View, error)
	Get(context.Context, OwnerScope, ID) (View, error)
	List(context.Context, PageRequest) (Page, error)
}

type ProxyAuthorizer interface {
	AuthorizeProxy(context.Context, ProxyCredential) (Evidence, error)
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

func validIdentity(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' ||
			character == ':' {
			continue
		}
		return false
	}
	return true
}
