package captureassignment

import (
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"testing"
)

func authoredEgress(t testing.TB, p egressprofile.ProfileRevision) egressprofile.Choice {
	t.Helper()
	c, err := egressprofile.Explicit(p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
