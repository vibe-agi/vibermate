package nativetool

import (
	"context"
	"crypto/rand"
	"time"
)

// StreamAuthority is process-local observation authority. It cannot authorize
// an outbound send and has no serializable fields or operation-ID constructor.
type StreamAuthority struct {
	issuer    [32]byte
	token     [32]byte
	expiresAt time.Time
}

type CallbackAuthority struct{ issuer, token [32]byte }
type CallbackKind string

const (
	CallbackPing         CallbackKind = "ping"
	CallbackForm         CallbackKind = "form"
	CallbackURL          CallbackKind = "url"
	CallbackVerification CallbackKind = "verification"
)

type CallbackFacts struct {
	RPCIdentityDigest, RequestDigest Digest
	Kind                             CallbackKind
}

func (issuer *StartReceiptIssuer) IssueCallback() (CallbackAuthority, error) {
	if issuer == nil || issuer.identity == ([32]byte{}) {
		return CallbackAuthority{}, ErrDenied
	}
	a := CallbackAuthority{issuer: issuer.identity}
	if _, err := rand.Read(a.token[:]); err != nil {
		return CallbackAuthority{}, err
	}
	if a.token == ([32]byte{}) {
		return CallbackAuthority{}, ErrUnavailable
	}
	return a, nil
}

// ExpiresAt is immutable observation metadata, not live send permission.
func (a StreamAuthority) ExpiresAt() time.Time { return a.expiresAt }

// IssueStream is used only by the repository after it verifies the original
// receipt and commits the observation transaction. Its registry owns validity.
func (issuer *StartReceiptIssuer) IssueStream(expires time.Time) (StreamAuthority, error) {
	if issuer == nil || issuer.identity == ([32]byte{}) || expires.IsZero() || expires.UnixMilli() <= 0 {
		return StreamAuthority{}, ErrDenied
	}
	var authority StreamAuthority
	authority.issuer = issuer.identity
	authority.expiresAt = expires.UTC()
	if _, err := rand.Read(authority.token[:]); err != nil {
		return StreamAuthority{}, err
	}
	if authority.token == ([32]byte{}) {
		return StreamAuthority{}, ErrUnavailable
	}
	return authority, nil
}

type ObservedCounts struct{ BytesIn, BytesOut int64 }
type PhysicalFinishInput struct {
	Outcome, Reason string
	Counts          *ObservedCounts
	ObservedAt      time.Time
}
type LogicalFinishInput struct {
	Outcome, Reason string
	ObservedAt      time.Time
}

// StreamRepository separates HTTP attempts, logical RPCs and receipt-backed
// callback observations. Observation capabilities never grant outbound sends.
type StreamRepository interface {
	RegisterObservedCallback(context.Context, StreamAuthority, CallbackFacts, time.Time) (CallbackAuthority, error)
	BindObservedReply(context.Context, ID, Binding, CallbackAuthority, RegisteredParentWitness, time.Time) error
	RetireObservedCallback(context.Context, CallbackAuthority, time.Time) error
	OpenObservedStream(context.Context, StartReceipt, Binding, time.Duration, time.Time) (StreamAuthority, error)
	BindRecoveryGET(context.Context, ID, Binding, StreamAuthority, time.Time) error
	ValidateLocalReplay(context.Context, StreamAuthority, time.Time) error
	FinishAttempt(context.Context, StartReceipt, PhysicalFinishInput) error
	// FinishObservedRPC closes an attached, already sent POST with a fully
	// validated terminal result. It never creates observation or send authority.
	FinishObservedRPC(context.Context, StartReceipt, PhysicalFinishInput, LogicalFinishInput) error
	CompleteStreamRPC(context.Context, StreamAuthority, LogicalFinishInput) error
	RetireObservedStream(context.Context, StreamAuthority, time.Time) error
}
