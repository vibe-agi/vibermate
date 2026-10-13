package nativemcp

import (
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"testing"
)

func authoredEgress(t testing.TB, profile egressprofile.ProfileRevision) egressprofile.Choice {
	t.Helper()
	c, err := egressprofile.Explicit(profile)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
