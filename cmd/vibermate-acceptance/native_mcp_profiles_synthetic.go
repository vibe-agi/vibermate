//go:build vibermate_acceptance_synthetic

package main

import "github.com/vibe-agi/vibermate/internal/nativemcp"

func nativeAcceptanceContentProfiles() (nativemcp.ContentProfiles, bool) {
	return nativemcp.AcceptanceSyntheticContentProfiles(), true
}
