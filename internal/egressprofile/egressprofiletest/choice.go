// Package egressprofiletest holds test fixtures for egress profile choices.
package egressprofiletest

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
)

// Explicit returns the explicit choice of profile, failing the test if the
// profile cannot be chosen.
func Explicit(t testing.TB, profile egressprofile.ProfileRevision) egressprofile.Choice {
	t.Helper()
	choice, err := egressprofile.Explicit(profile)
	if err != nil {
		t.Fatal(err)
	}
	return choice
}
