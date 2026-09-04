// Package serveradmin owns the distinct human Web Session and recovery
// boundary of a Runtime Server. Client admission never grants management
// authority: only the configured owner receives management capabilities.
package serveradmin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

const (
	AccessKeyFileName = "admin-access-key"
	OwnerFileName     = "owner-user-id"
	credentialBytes   = 32
	maxSessions       = 128
	accessDomain      = "vibermate:server-admin-access:v1:"
	readDomain        = "vibermate:server-admin-session:read:v1:"
	writeDomain       = "vibermate:server-admin-session:write:v1:"
	sessionDomain     = "vibermate:server-web-session:v1:"
)

var (
	ErrInvalidOptions  = errors.New("Runtime Server admin options are invalid")
	ErrUnauthorized    = errors.New("Runtime Server admin access is unauthorized")
	ErrSessionCapacity = errors.New("Runtime Server admin session capacity is exhausted")
)

type Scope string

const (
	ScopeRead  Scope = "read"
	ScopeWrite Scope = "write"
)

func (scope Scope) Valid() bool { return scope == ScopeRead || scope == ScopeWrite }

type Role string

const (
	RoleOwner  Role = "owner"
	RoleMember Role = "member"
)

func (role Role) Valid() bool { return role == RoleOwner || role == RoleMember }

type Principal struct {
	UserID   runtimeuser.UserID
	Username string
	Role     Role
}

func (principal Principal) Valid() bool {
	return principal.UserID.Valid() && runtimeuser.ValidUsername(principal.Username) &&
		principal.Role.Valid()
}

type Clock interface{ Now() time.Time }

type Options struct {
	DataDirectory   string
	Clock           Clock
	Random          io.Reader
	SessionLifetime time.Duration
}

// Credential intentionally redacts itself in every formatting path. Value is
// reserved for the one no-store, same-origin response that delivers a newly
// issued session; the transport adapter separately exposes whether it is TLS.
type Credential struct{ value string }

func (credential Credential) Value() string { return credential.value }
func (Credential) String() string           { return "[REDACTED]" }
func (Credential) GoString() string         { return "serveradmin.Credential{[REDACTED]}" }

type Session struct {
	ReadToken  Credential
	WriteToken Credential
	ExpiresAt  time.Time
	Principal  Principal
}

type sessionRecord struct {
	scope     Scope
	expiresAt time.Time
	principal Principal
	sessionID [sha256.Size]byte
}

type Authority struct {
	mu            sync.Mutex
	accessKeyPath string
	ownerPath     string
	accessDigest  [sha256.Size]byte
	ownerID       runtimeuser.UserID
	clock         Clock
	random        io.Reader
	lifetime      time.Duration
	sessions      map[[sha256.Size]byte]sessionRecord
}

func Open(options Options) (*Authority, error) {
	if options.DataDirectory == "" || !filepath.IsAbs(options.DataDirectory) ||
		filepath.Clean(options.DataDirectory) != options.DataDirectory ||
		options.Clock == nil || options.Random == nil || options.SessionLifetime <= 0 ||
		options.SessionLifetime > 24*time.Hour {
		return nil, ErrInvalidOptions
	}
	if err := os.MkdirAll(options.DataDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("prepare Runtime Server admin directory: %w", err)
	}
	if err := os.Chmod(options.DataDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("protect Runtime Server admin directory: %w", err)
	}
	path := filepath.Join(options.DataDirectory, AccessKeyFileName)
	key, err := openOrCreateAccessKey(path, options.Random)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(accessDomain + key))
	ownerPath := filepath.Join(options.DataDirectory, OwnerFileName)
	ownerID, err := readOwner(ownerPath)
	if err != nil {
		return nil, err
	}
	return &Authority{
		accessKeyPath: path,
		ownerPath:     ownerPath,
		accessDigest:  digest,
		ownerID:       ownerID,
		clock:         options.Clock,
		random:        options.Random,
		lifetime:      options.SessionLifetime,
		sessions:      make(map[[sha256.Size]byte]sessionRecord),
	}, nil
}

func (authority *Authority) Owner() (runtimeuser.UserID, bool) {
	if authority == nil {
		return "", false
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return authority.ownerID, authority.ownerID.Valid()
}

func (authority *Authority) IsOwner(id runtimeuser.UserID) bool {
	owner, configured := authority.Owner()
	return configured && owner == id
}

// EnsureOwner binds the first Runtime User when called through an already
// trusted local Desktop or legacy recovery-key management boundary. Normal Web
// setup must use ClaimOwner, which proves possession explicitly.
func (authority *Authority) EnsureOwner(id runtimeuser.UserID) (bool, error) {
	if authority == nil || !id.Valid() {
		return false, ErrInvalidOptions
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.ownerID.Valid() {
		return false, nil
	}
	value, err := randomCredential(authority.random)
	if err != nil {
		return false, fmt.Errorf("generate Runtime Server recovery key: %w", err)
	}
	ownerPersisted, err := writeOwner(authority.ownerPath, id)
	if ownerPersisted {
		authority.ownerID = id
		clear(authority.sessions)
	}
	if err != nil {
		return ownerPersisted, err
	}
	keyReplaced, err := replaceAccessKey(authority.accessKeyPath, value)
	if keyReplaced {
		authority.accessDigest = sha256.Sum256([]byte(accessDomain + value))
	}
	if err != nil {
		return true, err
	}
	return true, nil
}

func (authority *Authority) RecoveryKeyValid(value string) bool {
	if authority == nil || !validCredential(value) {
		return false
	}
	candidate := sha256.Sum256([]byte(accessDomain + value))
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return subtle.ConstantTimeCompare(candidate[:], authority.accessDigest[:]) == 1
}

// ClaimOwner consumes proof of server-local Recovery Key possession to bind
// the first Server Owner. It never silently promotes an existing Runtime User.
func (authority *Authority) ClaimOwner(
	recoveryKey string,
	id runtimeuser.UserID,
) error {
	if authority == nil || !id.Valid() || !validCredential(recoveryKey) {
		return ErrUnauthorized
	}
	candidate := sha256.Sum256([]byte(accessDomain + recoveryKey))
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if subtle.ConstantTimeCompare(candidate[:], authority.accessDigest[:]) != 1 {
		return ErrUnauthorized
	}
	if authority.ownerID.Valid() {
		return ErrInvalidOptions
	}
	persisted, err := writeOwner(authority.ownerPath, id)
	if persisted {
		authority.ownerID = id
		clear(authority.sessions)
	}
	return err
}

// RotateRecoveryKey invalidates the previously displayed server-local key.
func (authority *Authority) RotateRecoveryKey() error {
	if authority == nil {
		return ErrInvalidOptions
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	value, err := randomCredential(authority.random)
	if err != nil {
		return fmt.Errorf("generate Runtime Server recovery key: %w", err)
	}
	replaced, err := replaceAccessKey(authority.accessKeyPath, value)
	if replaced {
		authority.accessDigest = sha256.Sum256([]byte(accessDomain + value))
	}
	return err
}

func (authority *Authority) AccessKeyPath() string {
	if authority == nil {
		return ""
	}
	return authority.accessKeyPath
}

func (authority *Authority) Login(accessKey string) (Session, error) {
	if authority == nil || !validCredential(accessKey) {
		return Session{}, ErrUnauthorized
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	candidate := sha256.Sum256([]byte(accessDomain + accessKey))
	if authority.ownerID.Valid() ||
		subtle.ConstantTimeCompare(candidate[:], authority.accessDigest[:]) != 1 {
		return Session{}, ErrUnauthorized
	}
	return authority.issueLocked(Principal{}, true)
}

// LoginUser mints a browser-only Web Session after a network adapter has
// verified the Runtime User's password under its rate limit.
func (authority *Authority) LoginUser(user runtimeuser.User) (Session, error) {
	if authority == nil || user.Validate() != nil || user.State != runtimeuser.StateActive {
		return Session{}, ErrUnauthorized
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if !authority.ownerID.Valid() {
		return Session{}, ErrUnauthorized
	}
	role := RoleMember
	if user.ID == authority.ownerID {
		role = RoleOwner
	}
	return authority.issueLocked(Principal{
		UserID: user.ID, Username: user.Username, Role: role,
	}, false)
}

func (authority *Authority) issueLocked(
	principal Principal,
	legacyOwner bool,
) (Session, error) {
	now := authority.clock.Now().UTC()
	authority.removeExpired(now)
	if len(authority.sessions) > (maxSessions-1)*2 {
		return Session{}, ErrSessionCapacity
	}
	read, err := randomCredential(authority.random)
	if err != nil {
		return Session{}, fmt.Errorf("issue Runtime Server admin read capability: %w", err)
	}
	write, err := randomCredential(authority.random)
	if err != nil {
		return Session{}, fmt.Errorf("issue Runtime Server admin write capability: %w", err)
	}
	if read == write {
		return Session{}, errors.New("Runtime Server admin capability collision")
	}
	expiresAt := now.Add(authority.lifetime)
	storedPrincipal := principal
	if legacyOwner {
		storedPrincipal.Role = RoleOwner
	}
	readDigest := sessionDigest(ScopeRead, read)
	writeDigest := sessionDigest(ScopeWrite, write)
	sessionID := sha256.Sum256([]byte(sessionDomain + read + "\x00" + write))
	if _, collision := authority.sessions[readDigest]; collision {
		return Session{}, errors.New("Runtime Server admin read capability collision")
	}
	if _, collision := authority.sessions[writeDigest]; collision {
		return Session{}, errors.New("Runtime Server admin write capability collision")
	}
	authority.sessions[readDigest] = sessionRecord{
		scope: ScopeRead, expiresAt: expiresAt, principal: storedPrincipal,
		sessionID: sessionID,
	}
	authority.sessions[writeDigest] = sessionRecord{
		scope: ScopeWrite, expiresAt: expiresAt, principal: storedPrincipal,
		sessionID: sessionID,
	}
	return Session{
		ReadToken: Credential{value: read}, WriteToken: Credential{value: write},
		ExpiresAt: expiresAt, Principal: principal,
	}, nil
}

func (authority *Authority) Authorize(value string, scope Scope) bool {
	principal, valid := authority.Authenticate(value, scope)
	return valid && principal.Role == RoleOwner
}

func (authority *Authority) Authenticate(
	value string,
	scope Scope,
) (Principal, bool) {
	if authority == nil || !scope.Valid() || !validCredential(value) {
		return Principal{}, false
	}
	digest := sessionDigest(scope, value)
	authority.mu.Lock()
	defer authority.mu.Unlock()
	now := authority.clock.Now().UTC()
	record, found := authority.sessions[digest]
	if !found || record.scope != scope || !now.Before(record.expiresAt) {
		delete(authority.sessions, digest)
		return Principal{}, false
	}
	return record.principal, true
}

func (authority *Authority) RevokeUserSessions(id runtimeuser.UserID) {
	if authority == nil || !id.Valid() {
		return
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	for digest, record := range authority.sessions {
		if record.principal.UserID == id {
			delete(authority.sessions, digest)
		}
	}
}

func (authority *Authority) Revoke(value string) bool {
	if authority == nil || !validCredential(value) {
		return false
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	for _, scope := range []Scope{ScopeRead, ScopeWrite} {
		digest := sessionDigest(scope, value)
		if record, found := authority.sessions[digest]; found {
			for candidate, paired := range authority.sessions {
				if paired.sessionID == record.sessionID {
					delete(authority.sessions, candidate)
				}
			}
			return true
		}
	}
	return false
}

// ReadRecoveryKey returns the owner-only local recovery material for a CLI
// running on the Runtime Server machine. It applies the same strict file and
// permission validation used at startup.
func ReadRecoveryKey(dataDirectory string) (string, error) {
	if dataDirectory == "" || !filepath.IsAbs(dataDirectory) ||
		filepath.Clean(dataDirectory) != dataDirectory {
		return "", ErrInvalidOptions
	}
	return readExistingAccessKey(filepath.Join(dataDirectory, AccessKeyFileName))
}

func (authority *Authority) removeExpired(now time.Time) {
	for digest, record := range authority.sessions {
		if !now.Before(record.expiresAt) {
			delete(authority.sessions, digest)
		}
	}
}

func openOrCreateAccessKey(path string, random io.Reader) (string, error) {
	payload, err := os.ReadFile(path)
	switch {
	case err == nil:
		return validateAccessKeyFile(path, payload)
	case !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("read Runtime Server admin access key: %w", err)
	}
	value, err := randomCredential(random)
	if err != nil {
		return "", fmt.Errorf("generate Runtime Server admin access key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create Runtime Server admin access key: %w", err)
	}
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(path)
		}
	}()
	if _, err := io.WriteString(file, value+"\n"); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	committed = true
	return value, nil
}

func readExistingAccessKey(path string) (string, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read Runtime Server recovery key: %w", err)
	}
	return validateAccessKeyFile(path, payload)
}

func validateAccessKeyFile(path string, payload []byte) (string, error) {
	info, err := os.Lstat(path)
	value := strings.TrimSuffix(string(payload), "\n")
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 ||
		!validCredential(value) || len(payload) != len(value)+1 {
		return "", ErrInvalidOptions
	}
	return value, nil
}

func readOwner(path string) (runtimeuser.UserID, error) {
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read Runtime Server owner: %w", err)
	}
	info, statErr := os.Lstat(path)
	value := strings.TrimSuffix(string(payload), "\n")
	id := runtimeuser.UserID(value)
	if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 ||
		!id.Valid() || len(payload) != len(value)+1 {
		return "", ErrInvalidOptions
	}
	return id, nil
}

func writeOwner(path string, id runtimeuser.UserID) (bool, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, fmt.Errorf("create Runtime Server owner record: %w", err)
	}
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(path)
		}
	}()
	if _, err := io.WriteString(file, string(id)+"\n"); err != nil {
		return false, err
	}
	if err := file.Sync(); err != nil {
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	committed = true
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return true, err
	}
	return true, nil
}

func replaceAccessKey(path, value string) (bool, error) {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".recovery-key-*")
	if err != nil {
		return false, fmt.Errorf("create Runtime Server recovery key replacement: %w", err)
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
		return false, err
	}
	if _, err := io.WriteString(temporary, value+"\n"); err != nil {
		return false, err
	}
	if err := temporary.Sync(); err != nil {
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return false, fmt.Errorf("replace Runtime Server recovery key: %w", err)
	}
	committed = true
	if err := syncDirectory(directory); err != nil {
		return true, err
	}
	return true, nil
}

func syncDirectory(directory string) error {
	directoryFile, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryFile.Close()
	if err := directoryFile.Sync(); err != nil {
		return err
	}
	return nil
}

func randomCredential(random io.Reader) (string, error) {
	raw := make([]byte, credentialBytes)
	if _, err := io.ReadFull(random, raw); err != nil {
		return "", err
	}
	value := base64.RawURLEncoding.EncodeToString(raw)
	clear(raw)
	return value, nil
}

func validCredential(value string) bool {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	valid := err == nil && len(decoded) == credentialBytes &&
		base64.RawURLEncoding.EncodeToString(decoded) == value
	clear(decoded)
	return valid
}

func sessionDigest(scope Scope, value string) [sha256.Size]byte {
	domain := readDomain
	if scope == ScopeWrite {
		domain = writeDomain
	}
	return sha256.Sum256([]byte(domain + value))
}
