package capturecontrol

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

type LauncherGrant struct {
	Token     string
	ExpiresAt time.Time
}

type launcherCredential struct {
	digest    [sha256.Size]byte
	expiresAt time.Time
}

// LauncherAuthority owns the rotating create-only launcher capability. It
// stores only domain-separated digests.
type LauncherAuthority struct {
	mu      sync.Mutex
	clock   Clock
	current launcherCredential
	pending *launcherCredential
	revoked bool
}

func NewLauncherAuthority(
	grant LauncherGrant,
	clock Clock,
) (*LauncherAuthority, error) {
	credential, err := validateLauncherGrant(grant, clock)
	if err != nil {
		return nil, err
	}
	return &LauncherAuthority{
		clock:   clock,
		current: credential,
	}, nil
}

// LauncherRotation allows a Host to accept the candidate, atomically publish
// discovery, and then either commit or abort without an authentication gap.
type LauncherRotation struct {
	authority *LauncherAuthority
	candidate launcherCredential
	once      sync.Once
	committed bool
	result    error
}

func (authority *LauncherAuthority) Prepare(
	grant LauncherGrant,
) (*LauncherRotation, error) {
	if authority == nil {
		return nil, errors.New("launcher authority is unavailable")
	}
	credential, err := validateLauncherGrant(grant, authority.clock)
	if err != nil {
		return nil, err
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.revoked {
		return nil, errors.New("launcher authority is revoked")
	}
	if authority.pending != nil {
		return nil, errors.New("launcher capability rotation is already pending")
	}
	if subtle.ConstantTimeCompare(
		credential.digest[:],
		authority.current.digest[:],
	) == 1 {
		return nil, errors.New("launcher rotation must use a new capability")
	}
	candidate := credential
	authority.pending = &candidate
	return &LauncherRotation{
		authority: authority,
		candidate: candidate,
	}, nil
}

func (rotation *LauncherRotation) Commit() error {
	if rotation == nil {
		return errors.New("launcher rotation is unavailable")
	}
	rotation.once.Do(func() {
		rotation.result = rotation.authority.finishRotation(
			rotation.candidate,
			true,
		)
		rotation.committed = rotation.result == nil
	})
	if !rotation.committed && rotation.result == nil {
		return errors.New("launcher rotation was already aborted")
	}
	return rotation.result
}

func (rotation *LauncherRotation) Abort() error {
	if rotation == nil {
		return nil
	}
	rotation.once.Do(func() {
		rotation.result = rotation.authority.finishRotation(
			rotation.candidate,
			false,
		)
	})
	if rotation.committed {
		return errors.New("launcher rotation was already committed")
	}
	return rotation.result
}

func (authority *LauncherAuthority) finishRotation(
	candidate launcherCredential,
	commit bool,
) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.pending == nil ||
		subtle.ConstantTimeCompare(
			authority.pending.digest[:],
			candidate.digest[:],
		) != 1 {
		return errors.New("launcher rotation ownership changed")
	}
	if commit {
		authority.current = candidate
	}
	authority.pending = nil
	return nil
}

func (authority *LauncherAuthority) Authorize(request *http.Request) bool {
	if authority == nil || request == nil {
		return false
	}
	values := request.Header.Values("Authorization")
	request.Header.Del("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return false
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	if _, err := decodeCapability(token); err != nil {
		return false
	}
	digest := launcherDigest(token)
	now := authority.clock.Now().UTC()
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.revoked {
		return false
	}
	if now.Before(authority.current.expiresAt) &&
		subtle.ConstantTimeCompare(digest[:], authority.current.digest[:]) == 1 {
		return true
	}
	return authority.pending != nil &&
		now.Before(authority.pending.expiresAt) &&
		subtle.ConstantTimeCompare(digest[:], authority.pending.digest[:]) == 1
}

func (authority *LauncherAuthority) Revoke() {
	if authority == nil {
		return
	}
	authority.mu.Lock()
	authority.revoked = true
	authority.current = launcherCredential{}
	authority.pending = nil
	authority.mu.Unlock()
}

func validateLauncherGrant(
	grant LauncherGrant,
	clock Clock,
) (launcherCredential, error) {
	if clock == nil ||
		grant.ExpiresAt.IsZero() ||
		!clock.Now().UTC().Before(grant.ExpiresAt.UTC()) {
		return launcherCredential{}, errors.New("launcher capability grant is invalid")
	}
	if _, err := decodeCapability(grant.Token); err != nil {
		return launcherCredential{}, errors.New("launcher capability is invalid")
	}
	return launcherCredential{
		digest:    launcherDigest(grant.Token),
		expiresAt: grant.ExpiresAt.UTC(),
	}, nil
}
