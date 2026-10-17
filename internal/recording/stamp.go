package recording

import (
	"errors"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"strings"
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

// ScopeToken retains fixed scope identity and one revocation bit. No token
// authorizes execution. Tokens must not be copied; publish their Stamp values.
type ScopeToken struct {
	epoch   *Epoch
	capture captureidentity.Reference
	fence   Fence
	revoked atomic.Bool
}

// Stamp is an immutable, revocable observation scope. Its zero value is invalid.
type Stamp struct{ token *ScopeToken }

func NewScopeToken(epoch *Epoch, reference captureidentity.Reference, fence Fence) (*ScopeToken, error) {
	if epoch == nil || fence.ArchiveEpoch == 0 || epoch.value.Load() != fence.ArchiveEpoch {
		return nil, errors.New("recording epoch is not current")
	}
	if reference == (captureidentity.Reference{}) {
		if fence.CaptureGeneration != 0 {
			return nil, errors.New("runtime recording scope cannot have a capture generation")
		}
	} else if reference.Validate() != nil || fence.CaptureGeneration == 0 {
		return nil, errors.New("invalid recording capture scope")
	}
	return &ScopeToken{epoch: epoch, capture: captureidentity.Reference{Kind: captureidentity.Kind(strings.Clone(string(reference.Kind))), ID: strings.Clone(reference.ID)}, fence: fence}, nil
}
func (t *ScopeToken) Stamp() Stamp { return Stamp{token: t} }
func (t *ScopeToken) Revoke() {
	if t != nil {
		t.revoked.Store(true)
	}
}
func (s Stamp) Current() bool {
	t := s.token
	return t != nil && t.epoch != nil && !t.revoked.Load() && t.fence.ArchiveEpoch != 0 && t.epoch.value.Load() == t.fence.ArchiveEpoch
}
func (s Stamp) Capture() captureidentity.Reference {
	if s.token == nil {
		return captureidentity.Reference{}
	}
	return s.token.capture
}
func (s Stamp) Fence() Fence {
	if s.token == nil {
		return Fence{}
	}
	return s.token.fence
}
