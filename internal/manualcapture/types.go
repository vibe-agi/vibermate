// Package manualcapture owns durable, revocable proxy capabilities for
// independently launched command-line and desktop applications. It records
// ingress attribution only; it cannot select an Environment, route,
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
// domain-separated credential digest. AdmissionRef is deliberately absent
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
	AdmissionRef       string             `json:"admissionRef"`
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
		AdmissionRef:       "manual-capture/" + record.ID.String(),
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
	Owner  OwnerScope
	Limit  int
	Cursor *PageCursor
}

// PageCursor is the stable position of the last ManualCapture returned by a
// running-first, most-recent-first catalog. It is owner-scoped by PageRequest;
// no credential or bearer authority is encoded in it.
type PageCursor struct {
	Running            bool
	UpdatedAt          time.Time
	AfterID            string
	IncludeAtUpdatedAt bool
}

func (cursor PageCursor) Valid() bool {
	validID := cursor.AfterID == ""
	if !validID {
		_, err := ParseID(cursor.AfterID)
		validID = err == nil
	}
	return !cursor.UpdatedAt.IsZero() &&
		cursor.UpdatedAt.Equal(cursor.UpdatedAt.UTC().Truncate(time.Millisecond)) &&
		validID && (cursor.IncludeAtUpdatedAt || cursor.AfterID == "")
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
	ActivityReader
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

// ActivityReader is the narrow internal lifecycle projection used when
// Environment impact is computed. Owner-scoped control reads remain on the
// Controller interface.
type ActivityReader interface {
	Active(context.Context, ID, time.Time) (bool, error)
}

// GlobalActivityReader is the installation-wide lifecycle projection used by
// operations, such as Root replacement, that affect every Capture owner.
// Owner-scoped catalog data remains unavailable through this seam.
type GlobalActivityReader interface {
	ActiveCount(context.Context) (int, error)
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
