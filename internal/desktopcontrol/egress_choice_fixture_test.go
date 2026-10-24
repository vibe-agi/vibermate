package desktopcontrol_test

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
)

func explicitProfile(t testing.TB, choice egressprofile.Choice) egressprofile.ProfileRevision {
	t.Helper()
	p, ok := choice.ExplicitProfile()
	if !ok {
		t.Fatal("expected explicit authored profile")
	}
	return p
}
