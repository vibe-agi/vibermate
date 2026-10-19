package rawevidence

import (
	"context"
	"encoding/hex"
	"math"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/recording"
)

// LocatedRevealAudit records an explicit inspection attempt without retaining
// payload or revealed credentials. Its source is supplied by the trusted catalog.
type LocatedRevealAudit struct {
	RevealAudit
	SourceID string
	Token    recording.InspectionToken
	Scope    recording.Scope
}

func (a LocatedRevealAudit) Validate() error {
	source, err := hex.DecodeString(a.Token.Sources.SourceSet)
	if a.RevealAudit.Validate() != nil || a.OccurredAt.UnixMilli() <= 0 || a.Scope.Validate() != nil || !a.Token.Control.Valid() || a.Scope.Fence.ArchiveEpoch != a.Token.Control.ArchiveEpoch || !validIdentity(a.SourceID) || len(a.SourceID) > 128 || !utf8.ValidString(a.SourceID) || err != nil || len(source) != 32 || hex.EncodeToString(source) != a.Token.Sources.SourceSet || a.Scope.Fence.CaptureGeneration > math.MaxInt64 || a.Token.Sources.Generation == 0 || a.Token.Sources.Generation > math.MaxInt64 {
		return ErrInvalidReveal
	}
	return nil
}

// AppendLocatedRevealAudit returns only after its actual I/O stops; it must
// never acknowledge queued/background audit work as durable disclosure proof.
type LocatedRevealAuditor interface {
	AppendLocatedRevealAudit(context.Context, LocatedRevealAudit) error
}
type RecoveryReader interface{ Recovery() Recovery }
