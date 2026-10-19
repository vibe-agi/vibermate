package recording

import (
	"errors"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"sync/atomic"
)

// Fence identifies observation visibility; it never authorizes execution.
type Fence struct{ ArchiveEpoch, CaptureGeneration uint64 }

// Epoch is owned by one Runtime, must not be copied, and retains no token list.
type Epoch struct{ value atomic.Uint64 }

func NewEpoch(value uint64) (*Epoch, error) {
	if value == 0 {
		return nil, errors.New("recording epoch must be positive")
	}
	e := &Epoch{}
	e.value.Store(value)
	return e, nil
}

// Advance invalidates older stamps in constant space without walking them.
func (e *Epoch) Advance(value uint64) bool {
	if e == nil || value == 0 {
		return false
	}
	for {
		old := e.value.Load()
		if value <= old {
			return false
		}
		if e.value.CompareAndSwap(old, value) {
			return true
		}
	}
}

// Stamp keeps bounded coordinates inline. Runtime producers retain this value
// in their existing execution owner without allocating a recording-only token
// or string backing. Scope/Capture materialization is for admitted consumers.
type Stamp struct {
	epoch         *Epoch
	revoked       *atomic.Bool
	fence         Fence
	captureID     [captureidentity.MaxIDBytes]byte
	captureLength uint8
	captureKind   uint8
}

// NewStamp makes epoch-revocable coordinates without a heap-owned scope token.
func NewStamp(epoch *Epoch, reference captureidentity.Reference, fence Fence) (Stamp, error) {
	if epoch == nil || epoch.Value() != fence.ArchiveEpoch {
		return Stamp{}, errors.New("recording epoch is not current")
	}
	if err := (Scope{Capture: reference, Fence: fence}).Validate(); err != nil {
		return Stamp{}, err
	}
	s := Stamp{epoch: epoch, fence: fence, captureLength: uint8(len(reference.ID))}
	switch reference.Kind {
	case captureidentity.KindManagedRun:
		s.captureKind = 1
	case captureidentity.KindManualCapture:
		s.captureKind = 2
	}
	copy(s.captureID[:], reference.ID)
	return s, nil
}

// ScopeToken is an explicit individual revocation owner, retained by callers
// needing that control lifetime. Runtime per-request producers use NewStamp.
// Tokens must not be copied; their Stamp values remain safe to copy.
type ScopeToken struct {
	stamp   Stamp
	revoked atomic.Bool
}

func NewScopeToken(epoch *Epoch, reference captureidentity.Reference, fence Fence) (*ScopeToken, error) {
	stamp, err := NewStamp(epoch, reference, fence)
	if err != nil {
		return nil, err
	}
	return &ScopeToken{stamp: stamp}, nil
}
func (t *ScopeToken) Stamp() Stamp {
	if t == nil {
		return Stamp{}
	}
	s := t.stamp
	s.revoked = &t.revoked
	return s
}
func (t *ScopeToken) Revoke() {
	if t != nil {
		t.revoked.Store(true)
	}
}
func (s Stamp) Current() bool {
	return s.epoch != nil && s.fence.ArchiveEpoch != 0 && (s.revoked == nil || !s.revoked.Load()) && s.epoch.Value() == s.fence.ArchiveEpoch
}
func (s Stamp) Fence() Fence        { return s.fence }
func (s Stamp) HasCapture() bool    { return s.captureKind != 0 }
func (s Stamp) CaptureIDBytes() int { return int(s.captureLength) }
func (s Stamp) SameScope(other Stamp) bool {
	return s.epoch == other.epoch && s.fence == other.fence && s.captureKind == other.captureKind && s.captureLength == other.captureLength && s.captureID == other.captureID
}
func (s Stamp) captureKindValue() captureidentity.Kind {
	switch s.captureKind {
	case 1:
		return captureidentity.KindManagedRun
	case 2:
		return captureidentity.KindManualCapture
	default:
		return ""
	}
}

// MatchesCapture compares producer coordinates without constructing strings.
func (s Stamp) MatchesCapture(reference captureidentity.Reference) bool {
	if reference.Kind != s.captureKindValue() || len(reference.ID) != int(s.captureLength) {
		return false
	}
	for i := 0; i < int(s.captureLength); i++ {
		if s.captureID[i] != reference.ID[i] {
			return false
		}
	}
	return true
}

// Capture materializes owned persistence coordinates. Recording workers call it
// only after acquiring their reservation, including scope conversion workspace.
func (s Stamp) Capture() captureidentity.Reference {
	return captureidentity.Reference{Kind: s.captureKindValue(), ID: string(s.captureID[:s.captureLength])}
}
