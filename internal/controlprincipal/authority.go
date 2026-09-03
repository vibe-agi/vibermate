package controlprincipal

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"sync"
)

const (
	credentialBytes  = 32
	credentialDomain = "vibermate:control-principal:v1:"
)

type CredentialGrant struct {
	Credential string
	Principal  Principal
}

type credential struct {
	digest    [sha256.Size]byte
	principal Principal
}

// Authority owns one connection's digest-only control credential. Time does
// not silently change login authority: lifecycle revocation or an explicit
// revision-increasing rotation does.
type Authority struct {
	mu      sync.Mutex
	current credential
	pending *credential
	revoked bool
}

func NewAuthority(grant CredentialGrant) (*Authority, error) {
	current, err := validateCredentialGrant(grant)
	if err != nil {
		return nil, err
	}
	return &Authority{current: current}, nil
}

// Authenticate returns only the typed principal. The raw credential is never
// copied into the result or retained outside the digest authority.
func (authority *Authority) Authenticate(ctx context.Context, value string) (Principal, bool) {
	if authority == nil || ctx == nil || ctx.Err() != nil || !validCredential(value) {
		return Principal{}, false
	}
	digest := credentialDigest(value)
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.revoked {
		return Principal{}, false
	}
	if subtle.ConstantTimeCompare(digest[:], authority.current.digest[:]) == 1 {
		return authority.current.principal, true
	}
	if authority.pending != nil &&
		subtle.ConstantTimeCompare(digest[:], authority.pending.digest[:]) == 1 {
		return authority.pending.principal, true
	}
	return Principal{}, false
}

// Rotation admits the old and candidate credentials only while a Host
// publishes the candidate. Commit removes the old credential atomically;
// abort removes the candidate.
type Rotation struct {
	authority *Authority
	candidate credential
	once      sync.Once
	committed bool
	result    error
}

func (authority *Authority) Prepare(grant CredentialGrant) (*Rotation, error) {
	if authority == nil {
		return nil, errors.New("control principal authority is unavailable")
	}
	candidate, err := validateCredentialGrant(grant)
	if err != nil {
		return nil, err
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.revoked {
		return nil, errors.New("control principal authority is revoked")
	}
	if authority.pending != nil {
		return nil, errors.New("control credential rotation is already pending")
	}
	if subtle.ConstantTimeCompare(
		candidate.digest[:],
		authority.current.digest[:],
	) == 1 {
		return nil, errors.New("control credential rotation requires a new credential")
	}
	if !authority.current.principal.sameConnection(candidate.principal) ||
		authority.current.principal.credentialRevision ==
			CredentialRevision(^uint64(0)) ||
		candidate.principal.credentialRevision !=
			authority.current.principal.credentialRevision+1 {
		return nil, errors.New("control credential rotation revision is invalid")
	}
	copy := candidate
	authority.pending = &copy
	return &Rotation{authority: authority, candidate: candidate}, nil
}

func (rotation *Rotation) Commit() error {
	if rotation == nil {
		return errors.New("control credential rotation is unavailable")
	}
	rotation.once.Do(func() {
		rotation.result = rotation.authority.finishRotation(rotation.candidate, true)
		rotation.committed = rotation.result == nil
	})
	if !rotation.committed && rotation.result == nil {
		return errors.New("control credential rotation was already aborted")
	}
	return rotation.result
}

func (rotation *Rotation) Abort() error {
	if rotation == nil {
		return nil
	}
	rotation.once.Do(func() {
		rotation.result = rotation.authority.finishRotation(rotation.candidate, false)
	})
	if rotation.committed {
		return errors.New("control credential rotation was already committed")
	}
	return rotation.result
}

func (authority *Authority) finishRotation(candidate credential, commit bool) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.pending == nil ||
		subtle.ConstantTimeCompare(
			authority.pending.digest[:],
			candidate.digest[:],
		) != 1 {
		return errors.New("control credential rotation ownership changed")
	}
	if commit {
		authority.current = candidate
	}
	authority.pending = nil
	return nil
}

func (authority *Authority) Revoke() {
	if authority == nil {
		return
	}
	authority.mu.Lock()
	authority.revoked = true
	authority.current = credential{}
	authority.pending = nil
	authority.mu.Unlock()
}

func validateCredentialGrant(grant CredentialGrant) (credential, error) {
	if !grant.Principal.Valid() || !validCredential(grant.Credential) {
		return credential{}, errors.New("control credential grant is invalid")
	}
	return credential{
		digest:    credentialDigest(grant.Credential),
		principal: grant.Principal,
	}, nil
}

func validCredential(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == credentialBytes
}

func credentialDigest(value string) [sha256.Size]byte {
	return sha256.Sum256([]byte(credentialDomain + value))
}
