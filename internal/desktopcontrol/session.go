package desktopcontrol

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	SessionStatePath      = "/api/v1/auth/sessions/current"
	SessionRenewalPath    = "/api/v1/auth/sessions/refresh"
	SessionStateSchema    = "vibermate-app-session-state-v1"
	SessionRotationSchema = "vibermate-app-session-rotation-v1"

	initialSessionRevision = uint64(1)
	sessionKeyDomain       = "vibermate:desktop-session-idempotency:v1:"
)

var (
	errSessionUnauthorized = errors.New("Desktop session is unauthorized")
	errSessionInvalid      = errors.New("Desktop session renewal request is invalid")
	errSessionConflict     = errors.New("Desktop session revision or idempotency key conflicts")
	errSessionUnavailable  = errors.New("Desktop session rotation is unavailable")
)

// SessionRotationPolicy is process-memory-only authority for replacing one
// control capability pair. No token or replay response is persisted.
type SessionRotationPolicy struct {
	Lifetime  time.Duration
	ReplayTTL time.Duration
	Random    io.Reader
}

type sessionRotationPolicy struct {
	lifetime  time.Duration
	replayTTL time.Duration
	random    io.Reader
}

type sessionCapabilityGeneration struct {
	revision    uint64
	readDigest  [sha256.Size]byte
	writeDigest [sha256.Size]byte
	expiresAt   time.Time
}

type sessionRotationReplay struct {
	retiredWriteDigest [sha256.Size]byte
	expectedRevision   uint64
	keyDigest          [sha256.Size]byte
	until              time.Time
	response           SessionRotation
}

// SessionState contains no capability. It lets a client discover the revision
// needed by If-Match and schedule renewal before expiry.
type SessionState struct {
	Schema    string    `json:"schema"`
	Revision  uint64    `json:"revision"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// SessionRotation is returned only by a successful renewal or an exact
// idempotent replay. It must remain in process memory and must never be logged.
type SessionRotation struct {
	Schema     string    `json:"schema"`
	Revision   uint64    `json:"revision"`
	ReadToken  string    `json:"readToken"`
	WriteToken string    `json:"writeToken"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

func compileSessionRotationPolicy(
	policy *SessionRotationPolicy,
) (*sessionRotationPolicy, error) {
	if policy == nil {
		return nil, nil
	}
	if policy.Lifetime <= 0 ||
		policy.ReplayTTL <= 0 ||
		policy.ReplayTTL > policy.Lifetime ||
		policy.Random == nil {
		return nil, errors.New("Desktop session rotation policy is invalid")
	}
	return &sessionRotationPolicy{
		lifetime:  policy.Lifetime,
		replayTTL: policy.ReplayTTL,
		random:    policy.Random,
	}, nil
}

// InspectSession authenticates and snapshots one exact current read
// generation under the same lock, so a concurrent rotation cannot return
// metadata for tokens the caller never received.
func (authenticator *Authenticator) InspectSession(
	request *http.Request,
) (SessionState, error) {
	if authenticator == nil || request == nil {
		return SessionState{}, errSessionUnauthorized
	}
	token, valid := takeBearerCapability(request)
	if !valid {
		return SessionState{}, errSessionUnauthorized
	}
	requestErr := request.Method != http.MethodGet ||
		request.URL == nil ||
		request.URL.Path != SessionStatePath ||
		request.URL.RawQuery != ""
	digest := capabilityDigest(readDomain, token)
	authenticator.mu.Lock()
	defer authenticator.mu.Unlock()
	now := authenticator.clock.Now().UTC()
	authenticator.clearExpiredReplayLocked(now)
	if !now.Before(authenticator.current.expiresAt) ||
		subtle.ConstantTimeCompare(
			digest[:],
			authenticator.current.readDigest[:],
		) != 1 {
		return SessionState{}, errSessionUnauthorized
	}
	if requestErr {
		return SessionState{}, errSessionInvalid
	}
	return SessionState{
		Schema:    SessionStateSchema,
		Revision:  authenticator.current.revision,
		ExpiresAt: authenticator.current.expiresAt,
	}, nil
}

// RenewSession combines write authentication, revision CAS, token generation,
// commit, and exact-key replay under one lock. A retired write token can only
// repeat the command that retired it, for the bounded replay window; normal
// Authorize calls reject it immediately.
func (authenticator *Authenticator) RenewSession(
	request *http.Request,
) (SessionRotation, error) {
	if authenticator == nil || request == nil {
		return SessionRotation{}, errSessionUnauthorized
	}
	token, valid := takeBearerCapability(request)
	if !valid {
		return SessionRotation{}, errSessionUnauthorized
	}
	expected, key, requestErr := sessionMutationHeaders(request)
	if request.Method != http.MethodPost ||
		request.URL == nil ||
		request.URL.Path != SessionRenewalPath ||
		request.URL.RawQuery != "" ||
		!emptyBody(request.Body) {
		requestErr = errSessionInvalid
	}
	writeDigest := capabilityDigest(writeDomain, token)
	keyDigest := sha256.Sum256([]byte(sessionKeyDomain + key))

	authenticator.mu.Lock()
	defer authenticator.mu.Unlock()
	now := authenticator.clock.Now().UTC()
	authenticator.clearExpiredReplayLocked(now)
	current := now.Before(authenticator.current.expiresAt) &&
		subtle.ConstantTimeCompare(
			writeDigest[:],
			authenticator.current.writeDigest[:],
		) == 1
	replayed := authenticator.replay != nil &&
		now.Before(authenticator.replay.until) &&
		subtle.ConstantTimeCompare(
			writeDigest[:],
			authenticator.replay.retiredWriteDigest[:],
		) == 1
	if !current && !replayed {
		return SessionRotation{}, errSessionUnauthorized
	}
	if requestErr != nil {
		return SessionRotation{}, errSessionInvalid
	}
	if authenticator.replay != nil &&
		subtle.ConstantTimeCompare(
			keyDigest[:],
			authenticator.replay.keyDigest[:],
		) == 1 {
		if expected != authenticator.replay.expectedRevision {
			return SessionRotation{}, errSessionConflict
		}
		return authenticator.replay.response, nil
	}
	if replayed || expected != authenticator.current.revision {
		return SessionRotation{}, errSessionConflict
	}
	if authenticator.current.revision == ^uint64(0) {
		return SessionRotation{}, errSessionConflict
	}
	if authenticator.rotation == nil {
		return SessionRotation{}, errSessionUnavailable
	}

	readToken, err := newSessionCapability(authenticator.rotation.random)
	if err != nil {
		return SessionRotation{}, errSessionUnavailable
	}
	writeToken, err := newSessionCapability(authenticator.rotation.random)
	if err != nil {
		return SessionRotation{}, errSessionUnavailable
	}
	readDigest := capabilityDigest(readDomain, readToken)
	readAsWriteDigest := capabilityDigest(writeDomain, readToken)
	newWriteDigest := capabilityDigest(writeDomain, writeToken)
	writeAsReadDigest := capabilityDigest(readDomain, writeToken)
	if readToken == writeToken ||
		subtle.ConstantTimeCompare(
			readDigest[:],
			authenticator.current.readDigest[:],
		) == 1 ||
		subtle.ConstantTimeCompare(
			readAsWriteDigest[:],
			authenticator.current.writeDigest[:],
		) == 1 ||
		subtle.ConstantTimeCompare(
			newWriteDigest[:],
			authenticator.current.writeDigest[:],
		) == 1 ||
		subtle.ConstantTimeCompare(
			writeAsReadDigest[:],
			authenticator.current.readDigest[:],
		) == 1 {
		return SessionRotation{}, errSessionUnavailable
	}
	expiresAt := now.Add(authenticator.rotation.lifetime).UTC()
	replayUntil := now.Add(authenticator.rotation.replayTTL).UTC()
	if !expiresAt.After(now) || !replayUntil.After(now) {
		return SessionRotation{}, errSessionUnavailable
	}
	response := SessionRotation{
		Schema:     SessionRotationSchema,
		Revision:   authenticator.current.revision + 1,
		ReadToken:  readToken,
		WriteToken: writeToken,
		ExpiresAt:  expiresAt,
	}
	authenticator.destroyReplayLocked()
	authenticator.replay = &sessionRotationReplay{
		retiredWriteDigest: authenticator.current.writeDigest,
		expectedRevision:   expected,
		keyDigest:          keyDigest,
		until:              replayUntil,
		response:           response,
	}
	authenticator.current = sessionCapabilityGeneration{
		revision:    response.Revision,
		readDigest:  readDigest,
		writeDigest: newWriteDigest,
		expiresAt:   expiresAt,
	}
	return response, nil
}

func sessionMutationHeaders(request *http.Request) (uint64, string, error) {
	if request == nil ||
		len(request.Header.Values("If-Match")) != 1 ||
		len(request.Header.Values("Idempotency-Key")) != 1 {
		return 0, "", errSessionInvalid
	}
	rawRevision := request.Header.Get("If-Match")
	key := request.Header.Get("Idempotency-Key")
	if rawRevision == "" ||
		len(key) < minIdempotencyBytes ||
		len(key) > maxIdempotencyBytes ||
		strings.TrimSpace(key) != key {
		return 0, "", errSessionInvalid
	}
	revision, err := strconv.ParseUint(rawRevision, 10, 64)
	if err != nil {
		return 0, "", errSessionInvalid
	}
	return revision, key, nil
}

func newSessionCapability(source io.Reader) (string, error) {
	value := make([]byte, capabilityBytes)
	defer clear(value)
	if _, err := io.ReadFull(source, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (authenticator *Authenticator) clearExpiredReplayLocked(now time.Time) {
	if authenticator.replay != nil && !now.Before(authenticator.replay.until) {
		authenticator.destroyReplayLocked()
	}
}

func (authenticator *Authenticator) destroyReplayLocked() {
	if authenticator.replay == nil {
		return
	}
	authenticator.replay.response.ReadToken = ""
	authenticator.replay.response.WriteToken = ""
	authenticator.replay = nil
}
