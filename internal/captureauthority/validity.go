// Package captureauthority contains the validity facts and change hints shared
// by the existing Capture and Login Session authorities. It grants no authority.
package captureauthority

import (
	"context"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
)

type Validity struct {
	Capture            captureidentity.Reference
	CredentialRevision uint64
	ExpiresAt          time.Time
	UserID             string
	SessionDigest      [32]byte
}

// Change is a post-commit revalidation hint, never a grant or a tombstone.
// SessionDigest indexes the exact Login Session without retaining its bearer.
type Change struct {
	Capture       captureidentity.Reference
	UserID        string
	SessionDigest [32]byte
}

type Observer interface {
	Changed(context.Context, Change) error
}
