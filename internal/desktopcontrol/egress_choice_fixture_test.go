package desktopcontrol_test

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

func explicitProfile(t testing.TB, choice egressprofile.Choice) egressprofile.ProfileRevision {
	t.Helper()
	p, ok := choice.ExplicitProfile()
	if !ok {
		t.Fatal("expected explicit authored profile")
	}
	return p
}
