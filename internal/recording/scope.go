package recording

import (
	"errors"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
)

// Scope identifies persisted observation ownership, never execution authority.
type Scope struct {
	Capture captureidentity.Reference
	Fence   Fence
}

func (s Scope) Validate() error {
	if s.Fence.ArchiveEpoch == 0 {
		return errors.New("recording archive epoch is required")
	}
	if s.Capture == (captureidentity.Reference{}) {
		if s.Fence.CaptureGeneration != 0 {
			return errors.New("global recording generation must be zero")
		}
	} else if s.Capture.Validate() != nil || s.Fence.CaptureGeneration == 0 {
		return errors.New("invalid recording capture scope")
	}
	return nil
}
func (e *Epoch) Value() uint64 {
	if e == nil {
		return 0
	}
	return e.value.Load()
}
func (s Stamp) Scope() Scope { return Scope{Capture: s.Capture(), Fence: s.Fence()} }

// ScopeIssuer supplies observation coordinates to already-admitted work.
// Implementations must not perform I/O or grant execution authority.
type ScopeIssuer interface {
	ScopeStamp(captureidentity.Reference) Stamp
}
