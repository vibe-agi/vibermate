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
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/filetransaction"
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
	path string
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
		path: filepath.Join(directory, loginStoreFileName),
	}
	snapshot, err := filetransaction.Read(store.transactionOptions())
	if err != nil {
		return nil, fmt.Errorf("read Runtime Server Login store: %w", err)
	}
	if !snapshot.Exists {
		return store, nil
	}
	if _, err := decodeLoginSessions(snapshot.Payload); err != nil {
		return nil, err
	}
	if err := os.Chmod(store.path, 0o600); err != nil {
		return nil, err
	}
	return store, nil
}

func decodeLoginSessions(payload []byte) (map[string]loginRecord, error) {
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
	sessions := make(map[string]loginRecord, len(document.Sessions))
	for origin, record := range document.Sessions {
		credential, err := loginCredentialOf(record)
		if err != nil || credential.Target().Origin() != origin || record.Target != origin {
			return nil, errors.New("Runtime Server Login store contains an invalid session")
		}
		sessions[origin] = record
	}
	return sessions, nil
}

func (store *LoginStore) Save(credential LoginCredential) error {
	if store == nil || !credential.valid() {
		return ErrInvalidLoginCredential
	}
	record := loginRecordOf(credential)
	return filetransaction.Update(
		store.transactionOptions(),
		func(snapshot filetransaction.Snapshot) (filetransaction.Mutation, error) {
			sessions := make(map[string]loginRecord)
			if snapshot.Exists {
				stored, err := decodeLoginSessions(snapshot.Payload)
				if err != nil {
					return filetransaction.Mutation{}, err
				}
				sessions = stored
			}
			sessions[credential.Target().Origin()] = record
			return encodeLoginSessions(sessions)
		},
	)
}

func (store *LoginStore) Load(
	target Target,
	now time.Time,
) (LoginCredential, error) {
	if store == nil || !target.Valid() || now.IsZero() {
		return LoginCredential{}, ErrLoginRequired
	}
	snapshot, err := filetransaction.Read(store.transactionOptions())
	if err != nil {
		return LoginCredential{}, fmt.Errorf(
			"read Runtime Server Login store: %w",
			err,
		)
	}
	if !snapshot.Exists {
		return LoginCredential{}, ErrLoginRequired
	}
	sessions, err := decodeLoginSessions(snapshot.Payload)
	if err != nil {
		return LoginCredential{}, fmt.Errorf(
			"decode Runtime Server Login store: %w",
			err,
		)
	}
	record, found := sessions[target.Origin()]
	if !found {
		return LoginCredential{}, ErrLoginRequired
	}
	credential, err := loginCredentialOf(record)
	if err != nil {
		return LoginCredential{}, fmt.Errorf(
			"decode Runtime Server Login credential: %w",
			err,
		)
	}
	if !now.UTC().Before(credential.ExpiresAt()) {
		return LoginCredential{}, ErrLoginRequired
	}
	return credential, nil
}

func (store *LoginStore) Remove(target Target) error {
	if store == nil || !target.Valid() {
		return ErrInvalidLoginCredential
	}
	return filetransaction.Update(
		store.transactionOptions(),
		func(snapshot filetransaction.Snapshot) (filetransaction.Mutation, error) {
			if !snapshot.Exists {
				return filetransaction.Mutation{}, nil
			}
			sessions, err := decodeLoginSessions(snapshot.Payload)
			if err != nil {
				return filetransaction.Mutation{}, err
			}
			if _, found := sessions[target.Origin()]; !found {
				return filetransaction.Mutation{}, nil
			}
			delete(sessions, target.Origin())
			return encodeLoginSessions(sessions)
		},
	)
}

func encodeLoginSessions(
	sessions map[string]loginRecord,
) (filetransaction.Mutation, error) {
	payload, err := json.Marshal(loginDocument{
		Schema: loginStoreSchema, Sessions: sessions,
	})
	if err != nil {
		return filetransaction.Mutation{}, err
	}
	return filetransaction.Mutation{Payload: payload, Write: true}, nil
}

func (store *LoginStore) transactionOptions() filetransaction.Options {
	return filetransaction.Options{
		Path: store.path, MaximumBytes: maxLoginStoreBytes, Mode: 0o600,
		TemporaryPrefix: ".login-sessions-*",
	}
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
