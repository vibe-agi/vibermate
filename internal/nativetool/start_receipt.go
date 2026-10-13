package nativetool

import (
	"crypto/rand"
	"crypto/subtle"
	"math"
)

// StartFacts are committed row facts supplied only by trusted Go composition.
// The issuer proves its own identity; the Store must separately prove SQL authority.
type StartFacts struct {
	OperationID                   ID
	AttemptID                     string
	AuthorityRevision, OwnerEpoch uint64
	BindingDigest                 Digest
}

type StartReceiptIssuer struct{ identity [32]byte }

func NewStartReceiptIssuer() (*StartReceiptIssuer, error) {
	issuer := &StartReceiptIssuer{}
	if _, err := rand.Read(issuer.identity[:]); err != nil {
		return nil, err
	}
	if issuer.identity == ([32]byte{}) {
		return nil, ErrUnavailable
	}
	return issuer, nil
}

func validStartFacts(f StartFacts) bool {
	_, err := NewID(string(f.OperationID))
	return err == nil && len(f.AttemptID) > 0 && len(f.AttemptID) <= 512 && f.AuthorityRevision > 0 && f.AuthorityRevision <= math.MaxInt64 && f.OwnerEpoch > 0 && f.OwnerEpoch <= math.MaxInt64 && f.BindingDigest != (Digest{})
}

func (issuer *StartReceiptIssuer) Issue(f StartFacts) (StartReceipt, error) {
	if issuer == nil || issuer.identity == ([32]byte{}) || !validStartFacts(f) {
		return StartReceipt{}, ErrInvalid
	}
	return StartReceipt{operationID: f.OperationID, attemptID: f.AttemptID, authorityRevision: f.AuthorityRevision, ownerEpoch: f.OwnerEpoch, bindingDigest: f.BindingDigest, issuer: issuer.identity}, nil
}

func (issuer *StartReceiptIssuer) Check(r StartReceipt, f StartFacts) error {
	if issuer == nil || issuer.identity == ([32]byte{}) || !validStartFacts(f) || subtle.ConstantTimeCompare(issuer.identity[:], r.issuer[:]) != 1 || r.operationID != f.OperationID || r.attemptID != f.AttemptID || r.authorityRevision != f.AuthorityRevision || r.ownerEpoch != f.OwnerEpoch || r.bindingDigest != f.BindingDigest {
		return ErrDenied
	}
	return nil
}
