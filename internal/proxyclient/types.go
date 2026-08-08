// Package proxyclient owns durable remote-client enrollment authority. It
// binds one enrolled control principal and one machine registration to an
// active ProxyClientBinding without turning any of those identities into a
// proxy credential, route selector, or provider account selector.
package proxyclient

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/environment"
)

const (
	MaxIDBytes          = 128
	MaxDisplayNameBytes = 128
	MaxPolicyIDBytes    = 128
	CredentialBytes     = 32
	MaxRevision         = uint64(1<<63 - 1)
)

var (
	ErrInvalidCommand      = errors.New("invalid ProxyClient command")
	ErrInvalidRecord       = errors.New("invalid ProxyClient record")
	ErrBindingNotFound     = errors.New("ProxyClientBinding not found")
	ErrBindingInactive     = errors.New("ProxyClientBinding is not active")
	ErrBindingConflict     = errors.New("ProxyClientBinding changed")
	ErrEnrollmentRejected  = errors.New("client enrollment rejected")
	ErrEnrollmentExpired   = errors.New("client enrollment expired")
	ErrEnrollmentConsumed  = errors.New("client enrollment already consumed")
	ErrControlRejected     = errors.New("enrolled control credential rejected")
	ErrStateConflict       = errors.New("ProxyClient durable state conflict")
	ErrCommitIndeterminate = errors.New("client enrollment commit is indeterminate")
	ErrRuntimeStopping     = errors.New("ProxyClient runtime is stopping")
)

type BindingID struct{ value string }
type EnrollmentID struct{ value string }
type MachineRegistrationID struct{ value string }
type PrincipalID struct{ value string }
type MachineID struct{ value string }

func ParseBindingID(value string) (BindingID, error) {
	if !validIdentity(value, MaxIDBytes) {
		return BindingID{}, fmt.Errorf("%w: binding ID is invalid", ErrInvalidCommand)
	}
	return BindingID{value: value}, nil
}

func ParseEnrollmentID(value string) (EnrollmentID, error) {
	if !validIdentity(value, MaxIDBytes) {
		return EnrollmentID{}, fmt.Errorf("%w: enrollment ID is invalid", ErrInvalidCommand)
	}
	return EnrollmentID{value: value}, nil
}

func ParseMachineRegistrationID(value string) (MachineRegistrationID, error) {
	if !validIdentity(value, MaxIDBytes) {
		return MachineRegistrationID{}, fmt.Errorf(
			"%w: machine registration ID is invalid",
			ErrInvalidCommand,
		)
	}
	return MachineRegistrationID{value: value}, nil
}

func ParsePrincipalID(value string) (PrincipalID, error) {
	if !validIdentity(value, MaxIDBytes) {
		return PrincipalID{}, fmt.Errorf("%w: principal ID is invalid", ErrInvalidCommand)
	}
	return PrincipalID{value: value}, nil
}

func ParseMachineID(value string) (MachineID, error) {
	if !validIdentity(value, MaxIDBytes) {
		return MachineID{}, fmt.Errorf("%w: machine ID is invalid", ErrInvalidCommand)
	}
	return MachineID{value: value}, nil
}

func (id BindingID) String() string             { return id.value }
func (id EnrollmentID) String() string          { return id.value }
func (id MachineRegistrationID) String() string { return id.value }
func (id PrincipalID) String() string           { return id.value }
func (id MachineID) String() string             { return id.value }

func (id BindingID) Valid() bool             { return validIdentity(id.value, MaxIDBytes) }
func (id EnrollmentID) Valid() bool          { return validIdentity(id.value, MaxIDBytes) }
func (id MachineRegistrationID) Valid() bool { return validIdentity(id.value, MaxIDBytes) }
func (id PrincipalID) Valid() bool           { return validIdentity(id.value, MaxIDBytes) }
func (id MachineID) Valid() bool             { return validIdentity(id.value, MaxIDBytes) }

type BindingState string

const (
	BindingActive  BindingState = "active"
	BindingRevoked BindingState = "revoked"
)

func (state BindingState) Valid() bool {
	return state == BindingActive || state == BindingRevoked
}

type EnrollmentState string

const (
	EnrollmentActive   EnrollmentState = "active"
	EnrollmentConsumed EnrollmentState = "consumed"
	EnrollmentRevoked  EnrollmentState = "revoked"
	EnrollmentExpired  EnrollmentState = "expired"
)

func (state EnrollmentState) Valid() bool {
	switch state {
	case EnrollmentActive, EnrollmentConsumed, EnrollmentRevoked, EnrollmentExpired:
		return true
	default:
		return false
	}
}

type MachineState string

const (
	MachineActive               MachineState = "active"
	MachineRevoked              MachineState = "revoked"
	MachineReEnrollmentRequired MachineState = "re_enrollment_required"
)

func (state MachineState) Valid() bool {
	switch state {
	case MachineActive, MachineRevoked, MachineReEnrollmentRequired:
		return true
	default:
		return false
	}
}

type PrincipalState string

const (
	PrincipalActive  PrincipalState = "active"
	PrincipalRevoked PrincipalState = "revoked"
)

func (state PrincipalState) Valid() bool {
	return state == PrincipalActive || state == PrincipalRevoked
}

type BindingPolicy struct {
	allowedIngressScopes  []string
	allowedEnvironmentIDs []environment.EnvironmentID
	quotaPolicyID         string
	allowedGrantKinds     []controlprincipal.GrantKind
}

func NewBindingPolicy(
	ingressScopes []string,
	environmentIDs []environment.EnvironmentID,
	quotaPolicyID string,
	grantKinds []controlprincipal.GrantKind,
) (BindingPolicy, error) {
	policy := BindingPolicy{
		allowedIngressScopes:  slices.Clone(ingressScopes),
		allowedEnvironmentIDs: slices.Clone(environmentIDs),
		quotaPolicyID:         quotaPolicyID,
		allowedGrantKinds:     slices.Clone(grantKinds),
	}
	if err := policy.normalizeAndValidate(); err != nil {
		return BindingPolicy{}, err
	}
	return policy, nil
}

func (policy *BindingPolicy) normalizeAndValidate() error {
	if policy == nil || len(policy.allowedIngressScopes) == 0 ||
		len(policy.allowedEnvironmentIDs) == 0 ||
		!validIdentity(policy.quotaPolicyID, MaxPolicyIDBytes) ||
		len(policy.allowedGrantKinds) == 0 {
		return fmt.Errorf("%w: binding policy is incomplete", ErrInvalidCommand)
	}
	if err := normalizeIdentities(policy.allowedIngressScopes); err != nil {
		return err
	}
	if err := normalizeEnvironmentIDs(policy.allowedEnvironmentIDs); err != nil {
		return err
	}
	sort.Slice(policy.allowedGrantKinds, func(left, right int) bool {
		return policy.allowedGrantKinds[left] < policy.allowedGrantKinds[right]
	})
	for index, kind := range policy.allowedGrantKinds {
		if !kind.Valid() || (index > 0 && policy.allowedGrantKinds[index-1] == kind) {
			return fmt.Errorf("%w: binding grant policy is invalid", ErrInvalidCommand)
		}
	}
	return nil
}

func (policy BindingPolicy) Valid() bool {
	copy := policy.Clone()
	return copy.normalizeAndValidate() == nil && policy.Equal(copy)
}

func (policy BindingPolicy) Clone() BindingPolicy {
	return BindingPolicy{
		allowedIngressScopes:  slices.Clone(policy.allowedIngressScopes),
		allowedEnvironmentIDs: slices.Clone(policy.allowedEnvironmentIDs),
		quotaPolicyID:         policy.quotaPolicyID,
		allowedGrantKinds:     slices.Clone(policy.allowedGrantKinds),
	}
}

func (policy BindingPolicy) Equal(other BindingPolicy) bool {
	return policy.quotaPolicyID == other.quotaPolicyID &&
		slices.Equal(policy.allowedIngressScopes, other.allowedIngressScopes) &&
		slices.Equal(policy.allowedEnvironmentIDs, other.allowedEnvironmentIDs) &&
		slices.Equal(policy.allowedGrantKinds, other.allowedGrantKinds)
}

func (policy BindingPolicy) AllowedIngressScopes() []string {
	return slices.Clone(policy.allowedIngressScopes)
}

func (policy BindingPolicy) AllowedEnvironmentIDs() []environment.EnvironmentID {
	return slices.Clone(policy.allowedEnvironmentIDs)
}

func (policy BindingPolicy) QuotaPolicyID() string { return policy.quotaPolicyID }

func (policy BindingPolicy) AllowedGrantKinds() []controlprincipal.GrantKind {
	return slices.Clone(policy.allowedGrantKinds)
}

type Revision uint64

func (revision Revision) Valid() bool {
	return revision > 0 && uint64(revision) <= MaxRevision
}

type EnrollmentDigest [sha256.Size]byte
type ControlDigest [sha256.Size]byte

func (digest EnrollmentDigest) Valid() bool { return digest != EnrollmentDigest{} }
func (digest ControlDigest) Valid() bool    { return digest != ControlDigest{} }

type BindingRecord struct {
	ID          BindingID
	Revision    Revision
	State       BindingState
	DisplayName string
	Policy      BindingPolicy
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (record BindingRecord) Validate() error {
	if !record.ID.Valid() || !record.Revision.Valid() || !record.State.Valid() ||
		!validDisplayName(record.DisplayName) || !record.Policy.Valid() ||
		!validTimeRange(record.CreatedAt, record.UpdatedAt) {
		return ErrInvalidRecord
	}
	return nil
}

type EnrollmentRecord struct {
	ID                    EnrollmentID
	BindingID             BindingID
	BindingRevision       Revision
	State                 EnrollmentState
	CredentialDigest      EnrollmentDigest
	CreatedAt             time.Time
	ExpiresAt             time.Time
	UpdatedAt             time.Time
	ConsumedAt            time.Time
	MachineRegistrationID MachineRegistrationID
}

func (record EnrollmentRecord) Validate() error {
	if !record.ID.Valid() || !record.BindingID.Valid() ||
		!record.BindingRevision.Valid() || !record.State.Valid() ||
		!record.CredentialDigest.Valid() ||
		!validTimeRange(record.CreatedAt, record.UpdatedAt) ||
		record.ExpiresAt.IsZero() || !record.ExpiresAt.After(record.CreatedAt) {
		return ErrInvalidRecord
	}
	switch record.State {
	case EnrollmentConsumed:
		if record.ConsumedAt.IsZero() || !record.MachineRegistrationID.Valid() ||
			record.ConsumedAt.Before(record.CreatedAt) ||
			record.ConsumedAt.After(record.UpdatedAt) {
			return ErrInvalidRecord
		}
	case EnrollmentActive, EnrollmentRevoked, EnrollmentExpired:
		if !record.ConsumedAt.IsZero() || record.MachineRegistrationID.Valid() {
			return ErrInvalidRecord
		}
	}
	return nil
}

type MachineRecord struct {
	ID              MachineRegistrationID
	MachineID       MachineID
	BindingID       BindingID
	BindingRevision Revision
	Revision        Revision
	State           MachineState
	DisplayName     string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (record MachineRecord) Validate() error {
	if !record.ID.Valid() || !record.MachineID.Valid() || !record.BindingID.Valid() ||
		!record.BindingRevision.Valid() || !record.Revision.Valid() ||
		!record.State.Valid() || !validDisplayName(record.DisplayName) ||
		!validTimeRange(record.CreatedAt, record.UpdatedAt) {
		return ErrInvalidRecord
	}
	return nil
}

type PrincipalRecord struct {
	ID                    PrincipalID
	BindingID             BindingID
	BindingRevision       Revision
	MachineRegistrationID MachineRegistrationID
	CredentialRevision    Revision
	CredentialDigest      ControlDigest
	AllowedGrantKinds     []controlprincipal.GrantKind
	State                 PrincipalState
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

func (record PrincipalRecord) Validate() error {
	if !record.ID.Valid() || !record.BindingID.Valid() ||
		!record.BindingRevision.Valid() || !record.MachineRegistrationID.Valid() ||
		!record.CredentialRevision.Valid() || !record.CredentialDigest.Valid() ||
		!record.State.Valid() || !validTimeRange(record.CreatedAt, record.UpdatedAt) ||
		!validGrantKinds(record.AllowedGrantKinds) {
		return ErrInvalidRecord
	}
	return nil
}

type CompletionCandidate struct {
	EnrollmentID     EnrollmentID
	EnrollmentDigest EnrollmentDigest
	Machine          MachineRecord
	Principal        PrincipalRecord
	CompletedAt      time.Time
}

func (candidate CompletionCandidate) Validate() error {
	if !candidate.EnrollmentID.Valid() || !candidate.EnrollmentDigest.Valid() ||
		!candidate.Machine.ID.Valid() || !candidate.Machine.MachineID.Valid() ||
		candidate.Machine.BindingID.Valid() || candidate.Machine.BindingRevision != 0 ||
		candidate.Machine.Revision != 1 || candidate.Machine.State != MachineActive ||
		!validDisplayName(candidate.Machine.DisplayName) ||
		!validTimeRange(candidate.Machine.CreatedAt, candidate.Machine.UpdatedAt) ||
		!candidate.Principal.ID.Valid() || candidate.Principal.BindingID.Valid() ||
		candidate.Principal.BindingRevision != 0 ||
		candidate.Principal.MachineRegistrationID != candidate.Machine.ID ||
		candidate.Principal.CredentialRevision != 1 ||
		!candidate.Principal.CredentialDigest.Valid() ||
		len(candidate.Principal.AllowedGrantKinds) != 0 ||
		candidate.Principal.State != PrincipalActive ||
		!validTimeRange(candidate.Principal.CreatedAt, candidate.Principal.UpdatedAt) ||
		candidate.CompletedAt.IsZero() ||
		candidate.Machine.CreatedAt != candidate.CompletedAt ||
		candidate.Principal.CreatedAt != candidate.CompletedAt {
		return ErrInvalidCommand
	}
	return nil
}

type AuthenticationRecord struct {
	Binding   BindingRecord
	Machine   MachineRecord
	Principal PrincipalRecord
}

func (record AuthenticationRecord) Validate() error {
	if record.Binding.Validate() != nil || record.Machine.Validate() != nil ||
		record.Principal.Validate() != nil ||
		record.Binding.State != BindingActive || record.Machine.State != MachineActive ||
		record.Principal.State != PrincipalActive ||
		record.Binding.ID != record.Machine.BindingID ||
		record.Binding.ID != record.Principal.BindingID ||
		record.Binding.Revision != record.Machine.BindingRevision ||
		record.Binding.Revision != record.Principal.BindingRevision ||
		record.Machine.ID != record.Principal.MachineRegistrationID ||
		!slices.Equal(
			record.Binding.Policy.AllowedGrantKinds(),
			record.Principal.AllowedGrantKinds,
		) {
		return ErrInvalidRecord
	}
	return nil
}

type CompletionOutcome string

const (
	CompletionCommitted     CompletionOutcome = "committed"
	CompletionNotCommitted  CompletionOutcome = "not_committed"
	CompletionIndeterminate CompletionOutcome = "indeterminate"
)

type CompletionResult struct {
	Outcome CompletionOutcome
	Record  AuthenticationRecord
}

type BindingView struct {
	ID          string       `json:"id"`
	State       BindingState `json:"state"`
	DisplayName string       `json:"displayName"`
	CreatedAt   time.Time    `json:"createdAt"`
	UpdatedAt   time.Time    `json:"updatedAt"`
}

type EnrollmentView struct {
	ID        string          `json:"id"`
	BindingID string          `json:"proxyClientBindingId"`
	State     EnrollmentState `json:"state"`
	CreatedAt time.Time       `json:"createdAt"`
	ExpiresAt time.Time       `json:"expiresAt"`
}

type MachineView struct {
	ID          string       `json:"id"`
	MachineID   string       `json:"machineId"`
	BindingID   string       `json:"proxyClientBindingId"`
	State       MachineState `json:"state"`
	DisplayName string       `json:"displayName"`
	CreatedAt   time.Time    `json:"createdAt"`
	UpdatedAt   time.Time    `json:"updatedAt"`
}

func BindingViewOf(record BindingRecord) BindingView {
	return BindingView{
		ID:          record.ID.String(),
		State:       record.State,
		DisplayName: record.DisplayName,
		CreatedAt:   record.CreatedAt,
		UpdatedAt:   record.UpdatedAt,
	}
}

func EnrollmentViewOf(record EnrollmentRecord) EnrollmentView {
	return EnrollmentView{
		ID:        record.ID.String(),
		BindingID: record.BindingID.String(),
		State:     record.State,
		CreatedAt: record.CreatedAt,
		ExpiresAt: record.ExpiresAt,
	}
}

func MachineViewOf(record MachineRecord) MachineView {
	return MachineView{
		ID:          record.ID.String(),
		MachineID:   record.MachineID.String(),
		BindingID:   record.BindingID.String(),
		State:       record.State,
		DisplayName: record.DisplayName,
		CreatedAt:   record.CreatedAt,
		UpdatedAt:   record.UpdatedAt,
	}
}

type EnrollmentCredential struct{ value string }
type ControlCredential struct{ value string }

func ParseEnrollmentCredential(value string) (EnrollmentCredential, error) {
	if !validCredential(value, "enroll_") {
		return EnrollmentCredential{}, ErrEnrollmentRejected
	}
	return EnrollmentCredential{value: value}, nil
}

func ParseControlCredential(value string) (ControlCredential, error) {
	if !validCredential(value, "control_") {
		return ControlCredential{}, ErrControlRejected
	}
	return ControlCredential{value: value}, nil
}

func (credential EnrollmentCredential) Value() string { return credential.value }
func (credential ControlCredential) Value() string    { return credential.value }

func (EnrollmentCredential) String() string { return "[REDACTED]" }
func (ControlCredential) String() string    { return "[REDACTED]" }
func (EnrollmentCredential) GoString() string {
	return "proxyclient.EnrollmentCredential{[REDACTED]}"
}
func (ControlCredential) GoString() string {
	return "proxyclient.ControlCredential{[REDACTED]}"
}

type EnrollmentGrant struct {
	Enrollment EnrollmentView
	Credential EnrollmentCredential
}

func (grant EnrollmentGrant) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("client_enrollment_id", grant.Enrollment.ID),
		slog.String("proxy_client_binding_id", grant.Enrollment.BindingID),
		slog.String("credential", "[REDACTED]"),
	)
}

type CompletionGrant struct {
	Machine    MachineView
	Principal  controlprincipal.Principal
	Credential ControlCredential
}

func (grant CompletionGrant) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("machine_registration_id", grant.Machine.ID),
		slog.String("control_principal_id", grant.Principal.ID()),
		slog.String("credential", "[REDACTED]"),
	)
}

type CreateBindingCommand struct {
	DisplayName string
	Policy      BindingPolicy
}

type CreateEnrollmentCommand struct {
	BindingID               BindingID
	ExpectedBindingRevision Revision
	ExpiresIn               time.Duration
}

type CompleteEnrollmentCommand struct {
	EnrollmentID EnrollmentID
	Credential   EnrollmentCredential
	MachineID    MachineID
	DisplayName  string
}

type RevokeBindingCommand struct {
	BindingID        BindingID
	ExpectedRevision Revision
}

type Repository interface {
	CreateBinding(context.Context, BindingRecord) error
	CreateEnrollment(context.Context, EnrollmentRecord) error
	CompleteEnrollment(context.Context, CompletionCandidate) (CompletionResult, error)
	Authenticate(context.Context, ControlDigest) (AuthenticationRecord, error)
	RevokeBinding(
		context.Context,
		BindingID,
		Revision,
		time.Time,
	) (BindingRecord, error)
}

func normalizeIdentities(values []string) error {
	for _, value := range values {
		if !validIdentity(value, MaxPolicyIDBytes) {
			return fmt.Errorf("%w: binding policy identity is invalid", ErrInvalidCommand)
		}
	}
	sort.Strings(values)
	for index := 1; index < len(values); index++ {
		if values[index-1] == values[index] {
			return fmt.Errorf("%w: binding policy identity is duplicated", ErrInvalidCommand)
		}
	}
	return nil
}

func normalizeEnvironmentIDs(values []environment.EnvironmentID) error {
	for _, value := range values {
		if _, err := environment.NewEnvironmentID(value.String()); err != nil {
			return fmt.Errorf("%w: binding Environment ID is invalid", ErrInvalidCommand)
		}
	}
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	for index := 1; index < len(values); index++ {
		if values[index-1] == values[index] {
			return fmt.Errorf("%w: binding Environment ID is duplicated", ErrInvalidCommand)
		}
	}
	return nil
}

func validGrantKinds(kinds []controlprincipal.GrantKind) bool {
	return len(kinds) > 0 && slices.Equal(kinds, canonicalGrantKinds(kinds))
}

func canonicalGrantKinds(kinds []controlprincipal.GrantKind) []controlprincipal.GrantKind {
	canonical := slices.Clone(kinds)
	sort.Slice(canonical, func(left, right int) bool { return canonical[left] < canonical[right] })
	for index, kind := range canonical {
		if !kind.Valid() || (index > 0 && canonical[index-1] == kind) {
			return nil
		}
	}
	return canonical
}

func validCredential(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	encoded := strings.TrimPrefix(value, prefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	return err == nil && len(decoded) == CredentialBytes &&
		base64.RawURLEncoding.EncodeToString(decoded) == encoded
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
	if value == "" || len(value) > maxBytes {
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

func validTimeRange(createdAt, updatedAt time.Time) bool {
	return !createdAt.IsZero() && !updatedAt.IsZero() && !updatedAt.Before(createdAt)
}
