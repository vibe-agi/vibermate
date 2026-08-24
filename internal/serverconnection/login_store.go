package serverconnection

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	loginStoreSchema   = "vibermate-runtime-server-logins-v1"
	loginStoreFileName = "login-sessions.json"
	maxLoginStoreBytes = 4 << 20
	loginTokenBytes    = 32
)

var (
	ErrInvalidLoginCredential = errors.New("Runtime Server Login credential is invalid")
	ErrLoginRequired          = errors.New("Runtime Server login is required")
)

type LoginToken struct{ value string }

func (token LoginToken) Value() string { return token.value }
func (LoginToken) String() string      { return "[REDACTED]" }
func (LoginToken) GoString() string {
	return "serverconnection.LoginToken{[REDACTED]}"
}

type LoginCredentialInput struct {
	Target       Target
	InstanceID   string
	UserID       string
	Username     string
	SessionID    string
	SessionToken string
	ExpiresAt    time.Time
}

type LoginCredential struct {
	target       Target
	instanceID   string
	userID       string
	username     string
	sessionID    string
	sessionToken LoginToken
	expiresAt    time.Time
}

func NewLoginCredential(input LoginCredentialInput) (LoginCredential, error) {
	credential := LoginCredential{
		target: input.Target, instanceID: input.InstanceID, userID: input.UserID,
		username: input.Username, sessionID: input.SessionID,
		sessionToken: LoginToken{value: input.SessionToken}, expiresAt: input.ExpiresAt,
	}
	if !credential.valid() {
		return LoginCredential{}, ErrInvalidLoginCredential
	}
	return credential, nil
}

func (credential LoginCredential) Target() Target           { return credential.target }
func (credential LoginCredential) InstanceID() string       { return credential.instanceID }
func (credential LoginCredential) UserID() string           { return credential.userID }
func (credential LoginCredential) Username() string         { return credential.username }
func (credential LoginCredential) SessionID() string        { return credential.sessionID }
func (credential LoginCredential) SessionToken() LoginToken { return credential.sessionToken }
func (credential LoginCredential) ExpiresAt() time.Time     { return credential.expiresAt }

func (credential LoginCredential) valid() bool {
	return credential.target.Valid() && validLoginText(credential.instanceID, 128) &&
		validLoginText(credential.userID, 128) && validLoginText(credential.username, 64) &&
		validLoginText(credential.sessionID, 128) &&
		validLoginToken(credential.sessionToken.value) &&
		!credential.expiresAt.IsZero() && credential.expiresAt.Location() == time.UTC &&
		credential.expiresAt.Equal(credential.expiresAt.Truncate(time.Millisecond))
}

type loginRecord struct {
	Target       string    `json:"target"`
	InstanceID   string    `json:"instanceId"`
	UserID       string    `json:"userId"`
	Username     string    `json:"username"`
	SessionID    string    `json:"sessionId"`
	SessionToken string    `json:"sessionToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type loginDocument struct {
	Schema   string                 `json:"schema"`
	Sessions map[string]loginRecord `json:"sessions"`
}

type LoginStore struct {
	mu        sync.Mutex
	directory string
	path      string
	sessions  map[string]loginRecord
}

func OpenLoginStore(directory string) (*LoginStore, error) {
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, errors.New("Runtime Server Login directory is invalid")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("prepare Runtime Server Login directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, err
	}
	store := &LoginStore{
		directory: directory,
		path:      filepath.Join(directory, loginStoreFileName),
		sessions:  make(map[string]loginRecord),
	}
	payload, err := os.ReadFile(store.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return store, nil
	case err != nil:
		return nil, fmt.Errorf("read Runtime Server Login store: %w", err)
	}
	if len(payload) == 0 || len(payload) > maxLoginStoreBytes {
		return nil, errors.New("Runtime Server Login store is invalid")
	}
	var document loginDocument
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil ||
		document.Schema != loginStoreSchema || document.Sessions == nil {
		return nil, errors.New("Runtime Server Login store is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("Runtime Server Login store has trailing data")
	}
	for origin, record := range document.Sessions {
		credential, err := loginCredentialOf(record)
		if err != nil || credential.Target().Origin() != origin || record.Target != origin {
			return nil, errors.New("Runtime Server Login store contains an invalid session")
		}
		store.sessions[origin] = record
	}
	if err := os.Chmod(store.path, 0o600); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *LoginStore) Save(credential LoginCredential) error {
	if store == nil || !credential.valid() {
		return ErrInvalidLoginCredential
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	candidate := make(map[string]loginRecord, len(store.sessions)+1)
	for origin, record := range store.sessions {
		candidate[origin] = record
	}
	record := loginRecordOf(credential)
	candidate[credential.Target().Origin()] = record
	if err := store.persist(candidate); err != nil {
		return err
	}
	store.sessions = candidate
	return nil
}

func (store *LoginStore) Load(
	target Target,
	now time.Time,
) (LoginCredential, error) {
	if store == nil || !target.Valid() || now.IsZero() {
		return LoginCredential{}, ErrLoginRequired
	}
	store.mu.Lock()
	record, found := store.sessions[target.Origin()]
	store.mu.Unlock()
	if !found {
		return LoginCredential{}, ErrLoginRequired
	}
	credential, err := loginCredentialOf(record)
	if err != nil || !now.UTC().Before(credential.ExpiresAt()) {
		return LoginCredential{}, ErrLoginRequired
	}
	return credential, nil
}

func (store *LoginStore) Remove(target Target) error {
	if store == nil || !target.Valid() {
		return ErrInvalidLoginCredential
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, found := store.sessions[target.Origin()]; !found {
		return nil
	}
	candidate := make(map[string]loginRecord, len(store.sessions)-1)
	for origin, record := range store.sessions {
		if origin != target.Origin() {
			candidate[origin] = record
		}
	}
	if err := store.persist(candidate); err != nil {
		return err
	}
	store.sessions = candidate
	return nil
}

func (store *LoginStore) persist(sessions map[string]loginRecord) error {
	payload, err := json.Marshal(loginDocument{
		Schema: loginStoreSchema, Sessions: sessions,
	})
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(store.directory, ".login-sessions-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return err
	}
	committed = true
	return nil
}

func loginRecordOf(credential LoginCredential) loginRecord {
	return loginRecord{
		Target: credential.Target().Origin(), InstanceID: credential.InstanceID(),
		UserID: credential.UserID(), Username: credential.Username(),
		SessionID: credential.SessionID(), SessionToken: credential.SessionToken().Value(),
		ExpiresAt: credential.ExpiresAt(),
	}
}

func loginCredentialOf(record loginRecord) (LoginCredential, error) {
	target, err := ParseTarget(record.Target)
	if err != nil {
		return LoginCredential{}, err
	}
	return NewLoginCredential(LoginCredentialInput{
		Target: target, InstanceID: record.InstanceID, UserID: record.UserID,
		Username: record.Username, SessionID: record.SessionID,
		SessionToken: record.SessionToken, ExpiresAt: record.ExpiresAt,
	})
}

func validLoginToken(value string) bool {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	valid := err == nil && len(decoded) == loginTokenBytes &&
		base64.RawURLEncoding.EncodeToString(decoded) == value
	clear(decoded)
	return valid
}

func validLoginText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) ||
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
