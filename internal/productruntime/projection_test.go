package productruntime

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/certidentity"
)

type discardLeafCacheInvalidator struct{}

func (discardLeafCacheInvalidator) InvalidateLeafCache(
	access.LeafCacheInvalidation,
) {
}

func newTestSnapshotProjection(t *testing.T) *access.AtomicSnapshotProjection {
	t.Helper()
	projection, err := access.NewSnapshotProjection(
		certidentity.InitialRootRevision,
		discardLeafCacheInvalidator{},
	)
	if err != nil {
		t.Fatalf("construct Access projection: %v", err)
	}
	return projection
}
