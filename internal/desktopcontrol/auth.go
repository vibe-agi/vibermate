package desktopcontrol

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	capabilityBytes = 32
	readDomain      = "vibermate:desktop-control:read:v1:"
	writeDomain     = "vibermate:desktop-control:write:v1:"
)

type Scope string

const (
	ScopeRead  Scope = "read"
	ScopeWrite Scope = "write"
)

type Clock interface {
	Now() time.Time
}

type CapabilityGrant struct {
	ReadToken  string
	WriteToken string
	ExpiresAt  time.Time
}

type Authenticator struct {
	readDigest  [sha256.Size]byte
	writeDigest [sha256.Size]byte
	expiresAt   time.Time
	clock       Clock
}

func NewAuthenticator(
	grant CapabilityGrant,
	clock Clock,
) (*Authenticator, error) {
	if clock == nil ||
		grant.ExpiresAt.IsZero() ||
		!clock.Now().UTC().Before(grant.ExpiresAt.UTC()) ||
		grant.ReadToken == grant.WriteToken {
		return nil, errors.New("Desktop control capability grant is invalid")
	}
	if !validCapability(grant.ReadToken) || !validCapability(grant.WriteToken) {
		return nil, errors.New("Desktop control capability is invalid")
	}
	return &Authenticator{
		readDigest:  capabilityDigest(readDomain, grant.ReadToken),
		writeDigest: capabilityDigest(writeDomain, grant.WriteToken),
		expiresAt:   grant.ExpiresAt.UTC(),
		clock:       clock,
	}, nil
}

func (authenticator *Authenticator) Authorize(
	request *http.Request,
	scope Scope,
) bool {
	if authenticator == nil ||
		request == nil ||
		!authenticator.clock.Now().UTC().Before(authenticator.expiresAt) {
		return false
	}
	values := request.Header.Values("Authorization")
	request.Header.Del("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return false
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	if !validCapability(token) {
		return false
	}
	switch scope {
	case ScopeRead:
		digest := capabilityDigest(readDomain, token)
		return subtle.ConstantTimeCompare(
			digest[:],
			authenticator.readDigest[:],
		) == 1
	case ScopeWrite:
		digest := capabilityDigest(writeDomain, token)
		return subtle.ConstantTimeCompare(
			digest[:],
			authenticator.writeDigest[:],
		) == 1
	default:
		return false
	}
}

func validCapability(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == capabilityBytes
}

func capabilityDigest(domain string, value string) [sha256.Size]byte {
	return sha256.Sum256([]byte(domain + value))
}
