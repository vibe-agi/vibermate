package desktopcontrol

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
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
	// Revision is the optimistic-concurrency revision of this token pair. A
	// zero value selects the initial revision, one, for bootstrap compatibility.
	Revision uint64
	// Rotation is nil only for deliberately non-rotating test or embedded
	// authorities. DesktopHost always supplies a policy.
	Rotation *SessionRotationPolicy
}

type Authenticator struct {
	mu sync.Mutex

	current  sessionCapabilityGeneration
	replay   *sessionRotationReplay
	rotation *sessionRotationPolicy
	clock    Clock
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
	revision := grant.Revision
	if revision == 0 {
		revision = initialSessionRevision
	}
	rotation, err := compileSessionRotationPolicy(grant.Rotation)
	if err != nil {
		return nil, err
	}
	return &Authenticator{
		current: sessionCapabilityGeneration{
			revision:    revision,
			readDigest:  capabilityDigest(readDomain, grant.ReadToken),
			writeDigest: capabilityDigest(writeDomain, grant.WriteToken),
			expiresAt:   grant.ExpiresAt.UTC(),
		},
		rotation: rotation,
		clock:    clock,
	}, nil
}

func (authenticator *Authenticator) Authorize(
	request *http.Request,
	scope Scope,
) bool {
	if authenticator == nil || request == nil {
		return false
	}
	token, valid := takeBearerCapability(request)
	if !valid {
		return false
	}
	authenticator.mu.Lock()
	defer authenticator.mu.Unlock()
	now := authenticator.clock.Now().UTC()
	authenticator.clearExpiredReplayLocked(now)
	if !now.Before(authenticator.current.expiresAt) {
		return false
	}
	switch scope {
	case ScopeRead:
		digest := capabilityDigest(readDomain, token)
		return subtle.ConstantTimeCompare(
			digest[:],
			authenticator.current.readDigest[:],
		) == 1
	case ScopeWrite:
		digest := capabilityDigest(writeDomain, token)
		return subtle.ConstantTimeCompare(
			digest[:],
			authenticator.current.writeDigest[:],
		) == 1
	default:
		return false
	}
}

func takeBearerCapability(request *http.Request) (string, bool) {
	if request == nil {
		return "", false
	}
	values := request.Header.Values("Authorization")
	request.Header.Del("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	return token, validCapability(token)
}

func validCapability(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == capabilityBytes
}

func capabilityDigest(domain string, value string) [sha256.Size]byte {
	return sha256.Sum256([]byte(domain + value))
}
