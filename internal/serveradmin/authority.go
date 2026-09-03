// Package serveradmin owns the distinct operator authentication boundary of a
// Runtime Server. Client admission never grants management authority: only the
// owner-held access key can mint short-lived browser/API sessions.
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
)

const (
	AccessKeyFileName = "admin-access-key"
	credentialBytes   = 32
	maxSessions       = 128
	accessDomain      = "vibermate:server-admin-access:v1:"
	readDomain        = "vibermate:server-admin-session:read:v1:"
	writeDomain       = "vibermate:server-admin-session:write:v1:"
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

type Clock interface{ Now() time.Time }

type Options struct {
	DataDirectory   string
	Clock           Clock
	Random          io.Reader
	SessionLifetime time.Duration
}

// Credential intentionally redacts itself in every formatting path. Value is
// reserved for the one HTTPS response that delivers a newly issued session.
type Credential struct{ value string }

func (credential Credential) Value() string { return credential.value }
func (Credential) String() string           { return "[REDACTED]" }
func (Credential) GoString() string         { return "serveradmin.Credential{[REDACTED]}" }

type Session struct {
	ReadToken  Credential
	WriteToken Credential
	ExpiresAt  time.Time
}

type sessionRecord struct {
	scope     Scope
	expiresAt time.Time
}

type Authority struct {
	mu            sync.Mutex
	accessKeyPath string
	accessDigest  [sha256.Size]byte
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
	return &Authority{
		accessKeyPath: path,
		accessDigest:  digest,
		clock:         options.Clock,
		random:        options.Random,
		lifetime:      options.SessionLifetime,
		sessions:      make(map[[sha256.Size]byte]sessionRecord),
	}, nil
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
	candidate := sha256.Sum256([]byte(accessDomain + accessKey))
	if subtle.ConstantTimeCompare(candidate[:], authority.accessDigest[:]) != 1 {
		return Session{}, ErrUnauthorized
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
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
	readDigest := sessionDigest(ScopeRead, read)
	writeDigest := sessionDigest(ScopeWrite, write)
	if _, collision := authority.sessions[readDigest]; collision {
		return Session{}, errors.New("Runtime Server admin read capability collision")
	}
	if _, collision := authority.sessions[writeDigest]; collision {
		return Session{}, errors.New("Runtime Server admin write capability collision")
	}
	authority.sessions[readDigest] = sessionRecord{scope: ScopeRead, expiresAt: expiresAt}
	authority.sessions[writeDigest] = sessionRecord{scope: ScopeWrite, expiresAt: expiresAt}
	return Session{
		ReadToken: Credential{value: read}, WriteToken: Credential{value: write},
		ExpiresAt: expiresAt,
	}, nil
}

func (authority *Authority) Authorize(value string, scope Scope) bool {
	if authority == nil || !scope.Valid() || !validCredential(value) {
		return false
	}
	digest := sessionDigest(scope, value)
	authority.mu.Lock()
	defer authority.mu.Unlock()
	now := authority.clock.Now().UTC()
	record, found := authority.sessions[digest]
	if !found || record.scope != scope || !now.Before(record.expiresAt) {
		delete(authority.sessions, digest)
		return false
	}
	return true
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
		info, statErr := os.Lstat(path)
		value := strings.TrimSuffix(string(payload), "\n")
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 ||
			!validCredential(value) || len(payload) != len(value)+1 {
			return "", ErrInvalidOptions
		}
		return value, nil
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
