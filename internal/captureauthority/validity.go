// Package captureauthority contains the validity facts and change hints shared
// by the existing Capture and Login Session authorities. It grants no authority.
package captureauthority

import (
	"context"
	"errors"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
)

// ErrUnavailable marks a validity read that failed without learning anything
// about the credential: storage could not be read. It is never a grant. The
// caller still refuses the operation, but may tell the client to retry rather
// than report a rejected credential.
var ErrUnavailable = errors.New("capture authority is unavailable")

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
