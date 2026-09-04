package runtimeuser

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	identifierBytes   = 20
	sessionDigestKind = "vibermate:runtime-user-login-session:v1:"
)

type Clock interface{ Now() time.Time }

type Options struct {
	Repository      Repository
	Clock           Clock
	Random          io.Reader
	SessionLifetime time.Duration
}

type Manager struct {
	repository Repository
	clock      Clock
	random     io.Reader
	lifetime   time.Duration
	randomMu   sync.Mutex
}

func New(options Options) (*Manager, error) {
	if options.Repository == nil || options.Clock == nil || options.Random == nil ||
		options.SessionLifetime <= 0 || options.SessionLifetime > 30*24*time.Hour {
		return nil, ErrInvalidOptions
	}
	return &Manager{
		repository: options.Repository,
		clock:      options.Clock,
		random:     options.Random,
		lifetime:   options.SessionLifetime,
	}, nil
}

func (manager *Manager) Create(
	ctx context.Context,
	command CreateCommand,
) (User, error) {
	if manager == nil || ctx == nil {
		return User{}, ErrInvalidOptions
	}
	username := canonicalUsername(command.Username)
	password := append([]byte(nil), command.Password...)
	defer clear(password)
	if username == "" || !validPassword(password) {
		return User{}, ErrInvalidUser
	}
	userID, err := manager.randomIdentifier("user.")
	if err != nil {
		return User{}, fmt.Errorf("generate Runtime User ID: %w", err)
	}
	passwordHash, err := manager.passwordHash(password)
	if err != nil {
		return User{}, err
	}
	now := canonicalTime(manager.clock.Now())
	user := User{
		ID: UserID(userID), Username: username, State: StateActive,
		CreatedAt: now, UpdatedAt: now,
	}
	record := UserRecord{User: user, PasswordHash: passwordHash}
	if record.Validate() != nil {
		return User{}, ErrInvalidUser
	}
	if err := manager.repository.CreateUser(ctx, record); err != nil {
		if errors.Is(err, ErrUsernameConflict) {
			return User{}, ErrUsernameConflict
		}
		return User{}, fmt.Errorf("create Runtime User: %w", err)
	}
	return user, nil
}

func (manager *Manager) Login(
	ctx context.Context,
	command LoginCommand,
) (LoginSession, error) {
	if manager == nil || ctx == nil {
		return LoginSession{}, ErrInvalidOptions
	}
	username := canonicalUsername(command.Username)
	password := append([]byte(nil), command.Password...)
	defer clear(password)
	if username == "" || !validPassword(password) ||
		!validMachineID(command.MachineID) || !validDeviceName(command.DeviceName) {
		return LoginSession{}, ErrInvalidCredentials
	}
	record, found, err := manager.repository.FindUserByUsername(ctx, username)
	if err != nil {
		return LoginSession{}, fmt.Errorf("find Runtime User: %w", err)
	}
	if !found {
		consumeInvalidLoginCost(password)
		return LoginSession{}, ErrInvalidCredentials
	}
	if record.Validate() != nil || record.User.State != StateActive ||
		!verifyPassword(password, record.PasswordHash) {
		return LoginSession{}, ErrInvalidCredentials
	}
	sessionIDValue, err := manager.randomIdentifier("login.")
	if err != nil {
		return LoginSession{}, fmt.Errorf("generate Runtime User Login Session ID: %w", err)
	}
	sessionID := LoginSessionID(sessionIDValue)
	token, err := manager.randomToken()
	if err != nil {
		return LoginSession{}, fmt.Errorf("generate Runtime User Login Session token: %w", err)
	}
	now := canonicalTime(manager.clock.Now())
	sessionRecord := SessionRecord{
		ID: sessionID, UserID: record.User.ID, TokenDigest: digestToken(token),
		MachineID: command.MachineID, DeviceName: command.DeviceName,
		CreatedAt: now, ExpiresAt: now.Add(manager.lifetime),
	}
	if sessionRecord.Validate() != nil {
		return LoginSession{}, ErrInvalidSession
	}
	if err := manager.repository.CreateSession(ctx, sessionRecord); err != nil {
		return LoginSession{}, fmt.Errorf("create Runtime User Login Session: %w", err)
	}
	return LoginSession{
		ID: sessionID, Token: SessionToken{value: token}, User: record.User,
		MachineID: command.MachineID, DeviceName: command.DeviceName,
		ExpiresAt: sessionRecord.ExpiresAt,
	}, nil
}

// VerifyCredentials authenticates a person without issuing a Login Session.
// Browser-facing adapters use it before minting their separately scoped Web
// Session, so a browser credential never gains Capture creation authority.
func (manager *Manager) VerifyCredentials(
	ctx context.Context,
	username string,
	passwordInput []byte,
) (User, error) {
	if manager == nil || ctx == nil {
		return User{}, ErrInvalidOptions
	}
	canonical := canonicalUsername(username)
	password := append([]byte(nil), passwordInput...)
	defer clear(password)
	if canonical == "" || !validPassword(password) {
		return User{}, ErrInvalidCredentials
	}
	record, found, err := manager.repository.FindUserByUsername(ctx, canonical)
	if err != nil {
		return User{}, fmt.Errorf("find Runtime User: %w", err)
	}
	if !found {
		consumeInvalidLoginCost(password)
		return User{}, ErrInvalidCredentials
	}
	if record.Validate() != nil || record.User.State != StateActive ||
		!verifyPassword(password, record.PasswordHash) {
		return User{}, ErrInvalidCredentials
	}
	return record.User, nil
}

// User returns one Runtime User without projecting password material.
func (manager *Manager) User(ctx context.Context, id UserID) (User, error) {
	if manager == nil || ctx == nil || !id.Valid() {
		return User{}, ErrInvalidUser
	}
	record, found, err := manager.repository.FindUserByID(ctx, id)
	if err != nil {
		return User{}, fmt.Errorf("find Runtime User: %w", err)
	}
	if !found || record.Validate() != nil {
		return User{}, ErrInvalidUser
	}
	return record.User, nil
}

// ReplacePassword changes one Runtime User password and revokes every Login
// Session in the same durable transaction. Callers must authenticate and
// authorize the person before crossing this interface.
func (manager *Manager) ReplacePassword(
	ctx context.Context,
	id UserID,
	passwordInput []byte,
) (User, error) {
	if manager == nil || ctx == nil || !id.Valid() {
		return User{}, ErrInvalidUser
	}
	password := append([]byte(nil), passwordInput...)
	defer clear(password)
	if !validPassword(password) {
		return User{}, ErrInvalidUser
	}
	passwordHash, err := manager.passwordHash(password)
	if err != nil {
		return User{}, err
	}
	record, found, err := manager.repository.ReplacePassword(
		ctx,
		id,
		passwordHash,
		canonicalTime(manager.clock.Now()),
	)
	if err != nil {
		return User{}, fmt.Errorf("replace Runtime User password: %w", err)
	}
	if !found || record.Validate() != nil {
		return User{}, ErrInvalidUser
	}
	return record.User, nil
}

func (manager *Manager) List(ctx context.Context) ([]User, error) {
	if manager == nil || ctx == nil {
		return nil, ErrInvalidOptions
	}
	records, err := manager.repository.ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list Runtime Users: %w", err)
	}
	users := make([]User, len(records))
	for index, record := range records {
		if record.Validate() != nil {
			return nil, errors.New("stored Runtime User is invalid")
		}
		users[index] = record.User
	}
	return users, nil
}

func (manager *Manager) Disable(ctx context.Context, id UserID) (User, error) {
	if manager == nil || ctx == nil || !id.Valid() {
		return User{}, ErrInvalidUser
	}
	record, found, err := manager.repository.SetUserState(
		ctx,
		id,
		StateDisabled,
		canonicalTime(manager.clock.Now()),
	)
	if err != nil {
		return User{}, fmt.Errorf("disable Runtime User: %w", err)
	}
	if !found || record.Validate() != nil {
		return User{}, ErrInvalidUser
	}
	return record.User, nil
}

func (manager *Manager) Authenticate(ctx context.Context, token string) (Identity, error) {
	if manager == nil || ctx == nil || !validToken(token) {
		return Identity{}, ErrInvalidSession
	}
	session, user, found, err := manager.repository.FindSession(ctx, digestToken(token))
	if err != nil {
		return Identity{}, fmt.Errorf("authenticate Runtime User Login Session: %w", err)
	}
	now := canonicalTime(manager.clock.Now())
	if !found || session.Validate() != nil || user.Validate() != nil ||
		user.User.ID != session.UserID || user.User.State != StateActive ||
		!session.RevokedAt.IsZero() || !now.Before(session.ExpiresAt) {
		return Identity{}, ErrInvalidSession
	}
	return Identity{
		SessionID: session.ID, User: user.User, MachineID: session.MachineID,
		DeviceName: session.DeviceName,
	}, nil
}

func (manager *Manager) Logout(ctx context.Context, token string) error {
	if manager == nil || ctx == nil || !validToken(token) {
		return ErrInvalidSession
	}
	found, err := manager.repository.RevokeSession(
		ctx,
		digestToken(token),
		canonicalTime(manager.clock.Now()),
	)
	if err != nil {
		return fmt.Errorf("revoke Runtime User Login Session: %w", err)
	}
	if !found {
		return ErrInvalidSession
	}
	return nil
}

func (manager *Manager) passwordHash(password []byte) (string, error) {
	manager.randomMu.Lock()
	defer manager.randomMu.Unlock()
	return hashPassword(password, manager.random)
}

func (manager *Manager) randomIdentifier(prefix string) (string, error) {
	raw := make([]byte, identifierBytes)
	manager.randomMu.Lock()
	_, err := io.ReadFull(manager.random, raw)
	manager.randomMu.Unlock()
	if err != nil {
		clear(raw)
		return "", err
	}
	encoded := prefix + base64.RawURLEncoding.EncodeToString(raw)
	clear(raw)
	return encoded, nil
}

func (manager *Manager) randomToken() (string, error) {
	raw := make([]byte, tokenBytes)
	manager.randomMu.Lock()
	_, err := io.ReadFull(manager.random, raw)
	manager.randomMu.Unlock()
	if err != nil {
		clear(raw)
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	clear(raw)
	return encoded, nil
}

func validToken(value string) bool {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	valid := err == nil && len(decoded) == tokenBytes &&
		base64.RawURLEncoding.EncodeToString(decoded) == value
	clear(decoded)
	return valid
}

func digestToken(value string) SessionDigest {
	return sha256.Sum256([]byte(sessionDigestKind + value))
}

func canonicalTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Millisecond)
}
