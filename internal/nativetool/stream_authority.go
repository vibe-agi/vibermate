package nativetool

import (
	"context"
	"crypto/rand"
	"time"
)

// StreamAuthority is process-local observation authority. It cannot authorize
// an outbound send and has no serializable fields or operation-ID constructor.
type StreamAuthority struct {
	issuer [32]byte
	token  [32]byte
}

// IssueStream is used only by the repository after it verifies the original
// receipt and commits the observation transaction. Its registry owns validity.
func (issuer *StartReceiptIssuer) IssueStream() (StreamAuthority, error) {
	if issuer == nil || issuer.identity == ([32]byte{}) {
		return StreamAuthority{}, ErrDenied
	}
	var authority StreamAuthority
	authority.issuer = issuer.identity
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

// StreamRepository separates one HTTP attempt from its originating logical RPC.
// Callback registration deliberately belongs to a later, separately sealed seam.
type StreamRepository interface {
	OpenObservedStream(context.Context, StartReceipt, Binding, time.Duration, time.Time) (StreamAuthority, error)
	BindRecoveryGET(context.Context, ID, Binding, StreamAuthority, time.Time) error
	ValidateLocalReplay(context.Context, StreamAuthority, time.Time) error
	FinishAttempt(context.Context, StartReceipt, PhysicalFinishInput) error
	CompleteStreamRPC(context.Context, StreamAuthority, LogicalFinishInput) error
	RetireObservedStream(context.Context, StreamAuthority, time.Time) error
}
