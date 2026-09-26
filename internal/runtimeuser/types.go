// Package runtimeuser owns the Runtime Server identities that may create
// Capture Runs. Runtime Users are distinct from upstream Provider Accounts.
package runtimeuser

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

const (
	maxUsernameBytes   = 64
	minUsernameBytes   = 3
	maxPasswordBytes   = 1024
	minPasswordBytes   = 8
	maxDeviceNameBytes = 128
	maxEnvironments    = 128
	maxWarningValue    = int64(1_000_000_000_000_000)
	tokenBytes         = 32
)

var (
	ErrInvalidOptions     = errors.New("Runtime User options are invalid")
	ErrInvalidUser        = errors.New("Runtime User is invalid")
	ErrUsernameConflict   = errors.New("Runtime User username already exists")
	ErrInvalidCredentials = errors.New("Runtime User credentials are invalid")
	ErrInvalidSession     = errors.New("Runtime User Login Session is invalid")
)

type UserID string

func (id UserID) Valid() bool { return validIdentifier(string(id), "user.") }

type LoginSessionID string

func (id LoginSessionID) Valid() bool {
	return validIdentifier(string(id), "login.")
}

type State string

const (
	StateActive   State = "active"
	StateDisabled State = "disabled"
)

func (state State) valid() bool {
	return state == StateActive || state == StateDisabled
}

type User struct {
	ID        UserID    `json:"id"`
	Username  string    `json:"username"`
	State     State     `json:"state"`
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt is also the credential revision. Repositories must advance it
	// on every password/state change, even within the same clock tick.
	UpdatedAt time.Time `json:"updatedAt"`
	Policy    Policy    `json:"-"`
}

func (user User) Validate() error {
	if !user.ID.Valid() ||
		canonicalUsername(user.Username) != user.Username ||
		!user.State.valid() || !canonicalTimestamp(user.CreatedAt) ||
		!canonicalTimestamp(user.UpdatedAt) || user.UpdatedAt.Before(user.CreatedAt) ||
		user.Policy.Validate() != nil {
		return ErrInvalidUser
	}
	return nil
}

// Policy limits which published Environments a Runtime User may launch and
// carries soft warnings over retained, observed usage. An empty Environment
// list means all published Environments; disabling the user is the deny-all
// operation.
type Policy struct {
	allowedEnvironmentIDs    string
	DailyAgentAPICallWarning int64
	DailyTokenWarning        int64
}

func NewPolicy(environmentIDs []string, dailyCalls, dailyTokens int64) (Policy, error) {
	if len(environmentIDs) > maxEnvironments || dailyCalls < 0 || dailyCalls > maxWarningValue ||
		dailyTokens < 0 || dailyTokens > maxWarningValue {
		return Policy{}, ErrInvalidUser
	}
	ids := slices.Clone(environmentIDs)
	for _, id := range ids {
		if !validEnvironmentID(id) {
			return Policy{}, ErrInvalidUser
		}
	}
	slices.Sort(ids)
	for index := 1; index < len(ids); index++ {
		if ids[index] == ids[index-1] {
			return Policy{}, ErrInvalidUser
		}
	}
	return Policy{
		allowedEnvironmentIDs: strings.Join(ids, "\x00"), DailyAgentAPICallWarning: dailyCalls,
		DailyTokenWarning: dailyTokens,
	}, nil
}

func (policy Policy) Validate() error {
	ids := policy.EnvironmentIDs()
	if len(ids) > maxEnvironments ||
		policy.DailyAgentAPICallWarning < 0 || policy.DailyAgentAPICallWarning > maxWarningValue ||
		policy.DailyTokenWarning < 0 || policy.DailyTokenWarning > maxWarningValue {
		return ErrInvalidUser
	}
	for index, id := range ids {
		if !validEnvironmentID(id) ||
			index > 0 && ids[index-1] >= id {
			return ErrInvalidUser
		}
	}
	return nil
}

func (policy Policy) Clone() Policy { return policy }

func (policy Policy) AllowsEnvironment(id string) bool {
	if policy.Validate() != nil {
		return false
	}
	ids := policy.EnvironmentIDs()
	return len(ids) == 0 || slices.Contains(ids, id)
}

func (policy Policy) EnvironmentIDs() []string {
	if policy.allowedEnvironmentIDs == "" {
		return []string{}
	}
	return strings.Split(policy.allowedEnvironmentIDs, "\x00")
}

func validEnvironmentID(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character > unicode.MaxASCII ||
			!(character >= 'a' && character <= 'z') &&
				!(character >= '0' && character <= '9') &&
				character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	first := value[0]
	return first >= 'a' && first <= 'z' || first >= '0' && first <= '9'
}

// UserRecord is the durable representation. PasswordHash is a one-way Argon2id
// encoding and must never be projected through a control response.
type UserRecord struct {
	User         User
	PasswordHash string
}

func (record UserRecord) Validate() error {
	if record.User.Validate() != nil || !validPasswordHash(record.PasswordHash) {
		return ErrInvalidUser
	}
	return nil
}

type SessionDigest [sha256.Size]byte

type SessionRecord struct {
	ID          LoginSessionID
	UserID      UserID
	TokenDigest SessionDigest
	MachineID   workspaceidentity.MachineID
	DeviceName  string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	RevokedAt   time.Time
}

func (record SessionRecord) Validate() error {
	if !record.ID.Valid() || !record.UserID.Valid() ||
		record.TokenDigest == (SessionDigest{}) ||
		!validMachineID(record.MachineID) || !validDeviceName(record.DeviceName) ||
		!canonicalTimestamp(record.CreatedAt) || !canonicalTimestamp(record.ExpiresAt) ||
		!record.CreatedAt.Before(record.ExpiresAt) {
		return ErrInvalidSession
	}
	if !record.RevokedAt.IsZero() &&
		(!canonicalTimestamp(record.RevokedAt) || record.RevokedAt.Before(record.CreatedAt)) {
		return ErrInvalidSession
	}
	return nil
}

type Repository interface {
	CreateUser(context.Context, UserRecord) error
	FindUserByUsername(context.Context, string) (UserRecord, bool, error)
	FindUserByID(context.Context, UserID) (UserRecord, bool, error)
	ListUsers(context.Context) ([]UserRecord, error)
	SetUserState(context.Context, UserID, State, time.Time) (UserRecord, bool, error)
	SetUserPolicy(context.Context, UserID, Policy) (UserRecord, bool, error)
	ReplacePassword(context.Context, UserID, string, time.Time, time.Time) (UserRecord, bool, error)
	CreateSession(context.Context, SessionRecord, time.Time) error
	FindSession(context.Context, SessionDigest) (SessionRecord, UserRecord, bool, error)
	RevokeSession(context.Context, SessionDigest, time.Time) (bool, error)
}

type CreateCommand struct {
	Username string
	Password []byte
}

type LoginCommand struct {
	Username   string
	Password   []byte
	MachineID  workspaceidentity.MachineID
	DeviceName string
}

// SessionToken deliberately redacts formatting. Only the login wire adapter
// and the local credential store should call Value.
type SessionToken struct{ value string }

func (token SessionToken) Value() string { return token.value }
func (SessionToken) String() string      { return "[REDACTED]" }
func (SessionToken) GoString() string {
	return "runtimeuser.SessionToken{[REDACTED]}"
}

type LoginSession struct {
	ID         LoginSessionID
	Token      SessionToken
	User       User
	MachineID  workspaceidentity.MachineID
	DeviceName string
	ExpiresAt  time.Time
}

type Identity struct {
	SessionID  LoginSessionID
	User       User
	MachineID  workspaceidentity.MachineID
	DeviceName string
}

func canonicalUsername(value string) string {
	if len(value) < minUsernameBytes || len(value) > maxUsernameBytes ||
		strings.TrimSpace(value) != value {
		return ""
	}
	var builder strings.Builder
	builder.Grow(len(value))
	for _, character := range value {
		switch {
		case character >= 'A' && character <= 'Z':
			builder.WriteRune(character + ('a' - 'A'))
		case character >= 'a' && character <= 'z',
			character >= '0' && character <= '9',
			character == '.', character == '_', character == '-':
			builder.WriteRune(character)
		default:
			return ""
		}
	}
	return builder.String()
}

func ValidUsername(value string) bool {
	return value != "" && canonicalUsername(value) == value
}

func validPassword(password []byte) bool {
	return len(password) >= minPasswordBytes && len(password) <= maxPasswordBytes &&
		utf8.Valid(password)
}

// ValidPassword reports whether a password satisfies the public Runtime User
// wire contract. It does not assess password strength beyond the enforced
// length and UTF-8 bounds.
func ValidPassword(password []byte) bool { return validPassword(password) }

func validDeviceName(value string) bool {
	if value == "" || len(value) > maxDeviceNameBytes || !utf8.ValidString(value) ||
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

// ValidDeviceName reports whether a human-readable device label is safe to
// freeze into downstream CaptureRun attribution.
func ValidDeviceName(value string) bool { return validDeviceName(value) }

func validMachineID(value workspaceidentity.MachineID) bool {
	_, err := workspaceidentity.ParseMachineID(value.String())
	return err == nil
}

func canonicalTimestamp(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC &&
		value.Equal(value.Truncate(time.Millisecond))
}

func validIdentifier(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	encoded := strings.TrimPrefix(value, prefix)
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	return err == nil && len(decoded) == 20 &&
		base64.RawURLEncoding.EncodeToString(decoded) == encoded
}
